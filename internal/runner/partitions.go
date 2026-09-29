package runner

// partitions.go — people's partitions in the runner (plans/partitions/03 §A;
// PD-17, PD-18, PD-19, PD-20, PD-35). A partitioned tile runs one backend
// instance per person who uses it — their user partition, "user:<id>" —
// beside the optional global instance, which is the primary's state at
// today's key (PD-04): nothing moves when a tile turns partitions on.
//
// A user partition's state is keyed (tile, deployment, pkey) — partStateKey,
// in a map of its own, so nothing that walks today's states (status, stats,
// alwaysOn, admission of deployments, VMs) meets one — and exists only on
// the primary (PD-17), only with --isolate (PD-19), and only while the tile's
// recorded mode has user partitions and the person may run it
// (ShouldRunPartition, PD-20). A generation spawns from a copy of the
// primary's view that names the partition (registry.Component.Partition):
// its own run dir, log, token, env and data, and the non-primary network
// rule (launchSpecWith, netPlanFor, spawnEgress, ingressFwd, hostDialFor).
//
// What the runner can't know — who a person is (their pkey and uid), whether
// they may run, their partition's env and data, how their token is bound,
// where their events go — it asks through PartitionHooks, installed at boot
// by the identity (F2) and data (F4) planes. Each hook is fail-closed: while
// one is missing, no user partition starts and no partition event is
// published.
//
// Admission, the 10-minute idle stop and the shared build are
// partadmit.go's.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

// PartitionGlobal is the global instance's partition key: today's instance
// of the primary (PD-04).
const PartitionGlobal = "global"

var (
	// ErrNoPartition: the partition a call names doesn't exist — "global" on
	// a partitioned tile that declares no global instance (a 404).
	ErrNoPartition = errors.New("no such partition")
	// ErrPartitionRefused: a person's partition can't run here or now — the
	// tile isn't partitioned, the deployment isn't the primary, xbind runs
	// without --isolate, the person may not run it, or the planes that give
	// it an identity and data aren't wired.
	ErrPartitionRefused = errors.New("the partition can't run")
)

// StartClass is why a start of a partition is asked for (03 §A.5): a
// person's own request, or a delivery (cron, bus) or a mail doorbell.
type StartClass uint8

const (
	// StartInteractive: the person's frame, terminal, agent session, path
	// ticket or backend (Route's rules 2-4).
	StartInteractive StartClass = iota
	// StartBackground: a cron or bus delivery (Route's rule 1).
	StartBackground
	// StartMail: a mail doorbell; a background start that also counts
	// against the tile's mail start rate.
	StartMail
)

// PartitionHooks are the runner's seams for people's partitions, installed
// at boot (Runner embeds them). Every one is fail-closed: nil means no user
// partition starts (the four start hooks) or no event goes out.
type PartitionHooks struct {
	// PartitionIdent resolves the person of partition part ("user:<id>") of
	// tile to their partition id, the pkey ("u-" and 32 hex digits), minting
	// their uid when they have none (the identity plane, 02 §1, 03 §E). An
	// error, or an answer of any other shape, refuses the start.
	PartitionIdent func(tile, part string) (pkey string, err error)
	// ShouldRunPartition gates a start of partition part of deployment dep
	// of tile beyond the tile's own gates: the person exists with the same
	// uid, is enabled and can read the tile (PD-20), and the partition's
	// namespaces aren't held by encryption (03 §B.6).
	ShouldRunPartition func(tile, dep, part string) bool
	// PartitionEnv is the resource env of a generation of partition
	// c.Partition (the data plane's PartitionEnv, 03 §B.4): the values
	// EnvFor gives the tile, and the remap onto the partition's own
	// namespaces, which dataBinds binds (fail closed, resourceBindsFor).
	PartitionEnv func(c *registry.Component, dep, part string) (env []string, remap map[string]ResBind)
	// RegisterPartitionInstance registers a generation's instance token as
	// tile's principal acting in partition part of deployment dep (auth's
	// RegisterInstancePartition, 02 §2), from the runner's own state.
	RegisterPartitionInstance func(token, tile, dep, part string)
	// PartitionEvent publishes a runner event of partition part to its
	// person's sockets only (02 §9): build-start/-ok/-error and reload.
	// nil: the event is dropped — never published tile-wide.
	PartitionEvent func(tile, dep, part, typ, text string)
	// PartitionCapsFor answers the running caps an admin or a tile manager
	// set for tile's user partitions and the workspace's (POST
	// /partitions/limits); 0 = the default derived from memory
	// (DefaultPartitionCaps). nil: the defaults.
	PartitionCapsFor func(tile string) (perTile, workspace int)
}

// partInfo is a user partition's side of a runner state.
type partInfo struct {
	part, pkey      string    // "user:<id>" and its pkey
	passive         int       // tracked passive streams (SSE): no use, no eviction guard (03 §A.5)
	lastInteractive time.Time // the last interactive request: in use for 2 min after
}

// partitionsState is the runner's partition bookkeeping.
type partitionsState struct {
	states map[string]*state                 // partStateKey → a user partition's state
	index  map[string]string                 // tile\0dep\0part → the pkey last resolved for it
	mode   map[string]registry.PartitionSpec // tile → its running spec, as PartitionsChanged last said
	adm    partAdmission                     // partadmit.go
	builds partBuilds                        // partadmit.go
}

// partStateKey keys user partition pkey of deployment dep of tile: its
// deployment's stateKey, then "\x00p\x00" and the pkey. No path or
// deployment name holds a NUL, so it is distinct from every stateKey.
func partStateKey(tile, dep, pkey string) string {
	return stateKey(tile, dep) + "\x00p\x00" + pkey
}

func partIndexKey(tile, dep, part string) string { return tile + "\x00" + dep + "\x00" + part }

// pkeyRe is the shape of a partition id (00-overview §2).
var pkeyRe = regexp.MustCompile(`^u-[0-9a-f]{32}$`)

// partSockDir names a user partition's directory under RunDir: "p-" and 16
// hex digits of SHA-256(tile ‖ 0 ‖ dep ‖ 0 ‖ pkey), which no CompKey or
// deployment's "d-" name takes, within the 108-byte socket limit.
func partSockDir(tile, dep, pkey string) string {
	h := sha256.Sum256([]byte(tile + "\x00" + dep + "\x00" + pkey))
	return "p-" + hex.EncodeToString(h[:8])
}

// partitionLog is a user partition's backend log, workspace-relative
// (03 §A.4, 06 §5).
func partitionLog(tile, dep, pkey string) string {
	return ".xbin/partition/" + util.TileKey(tile) + "/" + dep + "/" + pkey + "/backend.log"
}

// partitionLeaf is a user partition's cgroup leaf: its own node under the
// tile's parent, so each person's instance has the tile's caps to itself.
func partitionLeaf(tile, dep, pkey string) string {
	return cgroup.TileNode(util.CompKey(tile)) + partSockDir(tile, dep, pkey) + "/backend"
}

// partitionView is a copy of view v that spawns for partition part (views
// are shared: never written).
func partitionView(v *registry.Component, part, pkey string) *registry.Component {
	pv := *v
	pv.Partition, pv.PartitionID = part, pkey
	return &pv
}

// partitionSpec is tile's running partition spec: what PartitionsChanged
// last said, which it learns before a rescan publishes it, else the
// registry's settled answer (a tile that was partitioned when the runner
// started). ok: the tile runs user partitions.
func (r *Runner) partitionSpec(tile string) (registry.PartitionSpec, bool) {
	r.mu.Lock()
	spec, known := r.parts.mode[tile]
	r.mu.Unlock()
	if !known && r.Reg != nil {
		if c, ok := r.Reg.Component(tile); ok {
			spec, _ = c.Partitioned()
		}
	}
	return spec, spec.User
}

// noGlobal: deployment dep of tile is the primary of a partitioned tile
// that declares no global instance, which never runs.
func (r *Runner) noGlobal(tile, dep string) bool {
	spec, ok := r.partitionSpec(tile)
	return ok && !spec.Global && dep == r.primary(tile)
}

// globalRefusal refuses the primary's instance of a partitioned tile without
// a global instance.
func globalRefusal(tile string) error {
	return fmt.Errorf("%w: %s keeps each person's data apart and has no global instance", ErrNoPartition, tile)
}

// EnsurePartition returns the socket of a healthy backend for partition
// part of deployment dep of c (03 §A.2): "" is the deployment's one
// instance and "global" the primary's global instance, both
// EnsureDeployment's (global only while the tile declares it, else
// ErrNoPartition); "user:<id>" is that person's instance, started on its
// first use past admission (partadmit.go). c is the registry's component.
func (r *Runner) EnsurePartition(ctx context.Context, c *registry.Component, dep, part string, class StartClass) (string, error) {
	switch {
	case part == "":
		return r.EnsureDeployment(ctx, c, dep) // deployment: the caller's, Route's answer; its one instance
	case part == PartitionGlobal:
		if spec, ok := r.partitionSpec(c.Path); dep == r.primary(c.Path) && (!ok || !spec.Global) {
			return "", fmt.Errorf("%w: %s has no global instance", ErrNoPartition, c.Path)
		}
		return r.EnsureDeployment(ctx, c, dep) // deployment: the caller's, Route's answer; global is its instance at today's key
	}
	pkey, err := r.partitionGate(c, dep, part)
	if err != nil {
		return "", err
	}
	key := partStateKey(c.Path, dep, pkey)
	if s := r.existingPart(key); s != nil {
		s.mu.Lock()
		r.touchLocked(s, class)
		switch {
		case !s.dirty && s.cur != nil:
			sock := s.cur.sock
			s.mu.Unlock()
			return sock, nil
		case !s.dirty && s.lastErr != nil:
			err := s.lastErr
			s.mu.Unlock()
			return "", err
		case s.live(): // its own rebuild or start: blue/green is always admitted
			s.mu.Unlock()
			return r.ensureState(ctx, c, s)
		}
		s.mu.Unlock()
	}
	release, err := r.admitPartition(c.Path, key, class)
	if err != nil {
		if class == StartInteractive { // a deferred delivery retries; a refused person is an admin's metadata
			r.sbxFail(partitionView(c, part, pkey), sbx.Start, err)
		}
		return "", err
	}
	defer release()
	s := r.partStateOf(c.Path, dep, part, pkey)
	s.mu.Lock()
	r.touchLocked(s, class)
	s.mu.Unlock()
	return r.ensureState(ctx, c, s)
}

// partitionGate is every check before a person's partition may exist: its
// key, the primary, isolation, the tile's running mode, a backend, the
// tile's own gates, the person (ShouldRunPartition), the planes' hooks and
// the pkey. Nothing creates a state for a partition that fails one.
func (r *Runner) partitionGate(c *registry.Component, dep, part string) (string, error) {
	refuse := func(format string, a ...any) (string, error) {
		return "", fmt.Errorf("%w: %s", ErrPartitionRefused, fmt.Sprintf(format, a...))
	}
	id, ok := strings.CutPrefix(part, "user:")
	switch {
	case !ok || id == "":
		return refuse("%q is not a partition key", part)
	case dep != r.primary(c.Path):
		return refuse("people's partitions of %s run on its primary deployment only, not %s", c.Path, dep)
	case !r.Isolate:
		return refuse("a person's partition runs only in a sandbox (--isolate), and this xbind runs backends without one")
	}
	if _, ok := r.partitionSpec(c.Path); !ok {
		return refuse("%s doesn't run people's partitions", c.Path)
	}
	if err := registry.ValidateRuntime(c.Manifest); err != nil {
		return "", fmt.Errorf("component %s: %w", c.Path, err)
	}
	if !c.HasBackend() {
		return "", fmt.Errorf("component %s has no long-running backend", c.Path)
	}
	if !r.shouldRun(c.Path, dep) {
		why := "is not enabled"
		if r.HoldReason != nil {
			if w := r.HoldReason(c.Path); w != "" {
				why = w
			}
		}
		return "", fmt.Errorf("component %s %s", c.Path, why)
	}
	h := r.PartitionHooks
	if h.PartitionIdent == nil || h.ShouldRunPartition == nil || h.PartitionEnv == nil || h.RegisterPartitionInstance == nil {
		return refuse("this xbind can't give %s's partition of %s its own identity and data yet", id, c.Path)
	}
	if !h.ShouldRunPartition(c.Path, dep, part) {
		return refuse("%s's partition of %s may not run now", id, c.Path)
	}
	pkey, err := h.PartitionIdent(c.Path, part)
	if err != nil {
		return refuse("%s's partition of %s: %v", id, c.Path, err)
	}
	if !pkeyRe.MatchString(pkey) {
		return refuse("%s's partition of %s has no valid partition id", id, c.Path)
	}
	r.mu.Lock()
	if r.parts.index == nil {
		r.parts.index = map[string]string{}
	}
	r.parts.index[partIndexKey(c.Path, dep, part)] = pkey
	r.mu.Unlock()
	return pkey, nil
}

// touchLocked stamps a request of class on partition state s; callers hold
// s.mu.
func (r *Runner) touchLocked(s *state, class StartClass) {
	now := r.now()
	s.lastReq = now
	if class == StartInteractive && s.pt != nil {
		s.pt.lastInteractive = now
	}
}

// partStateOf is the state of user partition pkey, created dirty.
func (r *Runner) partStateOf(tile, dep, part, pkey string) *state {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := partStateKey(tile, dep, pkey)
	if r.parts.states == nil {
		r.parts.states = map[string]*state{}
	}
	s, ok := r.parts.states[k]
	if !ok {
		s = &state{comp: tile, dep: dep, dirty: true, pt: &partInfo{part: part, pkey: pkey}}
		r.parts.states[k] = s
	}
	return s
}

// existingPart is the state at key, never creating one.
func (r *Runner) existingPart(key string) *state {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.parts.states[key]
}

// partStates is every user partition's state of tile ("" = every tile's),
// in no order.
func (r *Runner) partStates(tile string) []*state {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*state
	for _, s := range r.parts.states {
		if tile == "" || s.comp == tile {
			out = append(out, s)
		}
	}
	return out
}

// partStateFor is the state the last EnsurePartition of (tile, dep, part)
// resolved, if it exists.
func (r *Runner) partStateFor(tile, dep, part string) *state {
	r.mu.Lock()
	defer r.mu.Unlock()
	pkey, ok := r.parts.index[partIndexKey(tile, dep, part)]
	if !ok {
		return nil
	}
	return r.parts.states[partStateKey(tile, dep, pkey)]
}

// TrackPartition marks one in-flight connection to partition part of
// deployment dep of tile until the release, as TrackDeployment does for ""
// and "global". A passive one — a stream (SSE) — neither counts as use nor
// keeps the partition from the idle stop or from eviction by an interactive
// start (03 §A.5); an active one does, and a hold (the agent's
// /engine/hold) is one.
func (r *Runner) TrackPartition(tile, dep, part string, passive bool) func() {
	t := r.TrackPartitionConn(tile, dep, part)
	if passive {
		t.Passive()
	}
	return t.Release
}

// PartitionConn is one tracked connection to a partition.
type PartitionConn struct {
	r       *Runner
	s       *state // nil: tracked by its deployment's state (release), or nothing
	release func()
	mu      sync.Mutex
	passive bool
	done    bool
}

// TrackPartitionConn tracks one active connection to partition part of
// deployment dep of tile; Passive turns it passive once its response turns
// out to be a stream (the proxy learns that from Content-Type:
// text/event-stream), and Release ends it. Both are idempotent.
func (r *Runner) TrackPartitionConn(tile, dep, part string) *PartitionConn {
	if part == "" || part == PartitionGlobal {
		return &PartitionConn{r: r, release: r.TrackDeployment(tile, dep)}
	}
	s := r.partStateFor(tile, dep, part)
	if s == nil {
		return &PartitionConn{r: r, release: func() {}}
	}
	s.mu.Lock()
	s.active++
	s.lastReq = r.now()
	s.mu.Unlock()
	return &PartitionConn{r: r, s: s}
}

// Passive turns an active tracked connection into a passive one. A
// connection to "" or "global" stays tracked as today's.
func (t *PartitionConn) Passive() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.s == nil || t.passive || t.done {
		return
	}
	t.passive = true
	t.s.mu.Lock()
	t.s.active--
	t.s.pt.passive++
	t.s.lastReq = t.r.now()
	t.s.mu.Unlock()
}

// Release ends the tracked connection.
func (t *PartitionConn) Release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	if t.s == nil {
		if t.release != nil {
			t.release()
		}
		return
	}
	t.s.mu.Lock()
	if t.passive {
		t.s.pt.passive--
	} else {
		t.s.active--
		t.s.lastReq = t.r.now()
	}
	t.s.mu.Unlock()
}

// ---- stops (03 §A.8) ----

// stopPart stops partition state s and forgets it: its instance token is
// revoked first, so it authenticates no more from the moment this returns;
// the process drains in the background (wait: in this call). Data is never
// touched.
func (r *Runner) stopPart(s *state, wait bool) {
	r.mu.Lock()
	if k := partStateKey(s.comp, s.dep, s.pt.pkey); r.parts.states[k] == s {
		delete(r.parts.states, k)
	}
	r.mu.Unlock()
	s.mu.Lock()
	s.gone = true
	inst := s.cur
	s.cur = nil
	s.mu.Unlock()
	if inst == nil {
		return
	}
	if r.Auth != nil {
		r.Auth.RevokeInstance(inst.token)
	}
	if wait {
		r.stopGen(inst, 5*time.Second)
	} else {
		go r.stopGen(inst, 5*time.Second)
	}
}

// stopParts stops every partition state the filter keeps, revoking all
// their tokens before it returns, and waits for their processes when wait.
func (r *Runner) stopParts(keep func(*state) bool, wait bool) int {
	var wg sync.WaitGroup
	n := 0
	for _, s := range r.partStates("") {
		if !keep(s) {
			continue
		}
		n++
		if !wait {
			r.stopPart(s, false)
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); r.stopPart(s, true) }()
	}
	wg.Wait()
	return n
}

// StopPartition stops partition part of deployment dep of tile — every
// incarnation of it (a recreated person's included) — revoking its tokens
// first; its data stays (06 §6 stop). A request starts it again.
func (r *Runner) StopPartition(tile, dep, part string) {
	r.stopParts(func(s *state) bool { return s.comp == tile && s.dep == dep && s.pt.part == part }, true)
}

// StopPartitionsOf stops every partition of person userID on every tile —
// their deletion, disabling or loss of access (06 §9) — revoking the tokens
// before it returns.
func (r *Runner) StopPartitionsOf(userID string) {
	part := "user:" + userID
	if n := r.stopParts(func(s *state) bool { return s.pt.part == part }, false); n > 0 {
		slog.Info("partitions stopped", "user", userID, "instances", n)
	}
}

// StopPartitions stops every user partition of tile, revoking their tokens
// before it returns (a mode change, 01 §6; a hold; a disable).
func (r *Runner) StopPartitions(tile string) {
	r.stopParts(func(s *state) bool { return s.comp == tile }, false)
}

// PartitionsChanged is told a tile's running partition spec changed (01 §6):
// old and new have User only while the tile runs user partitions (its
// state is Partitioned). Boot calls it from the registry's
// OnPartitionChange, before the scan that changed it is published, so
// the runner answers from the new spec at once (partitionSpec). Without
// user partitions every partition instance stops, its token revoked here;
// the global instance stops when the new spec has none, and restarts when
// the tile turned partitioned or unpartitioned, so XBIN_PARTITION says what
// it is.
func (r *Runner) PartitionsChanged(c *registry.Component, old, new registry.PartitionSpec) {
	r.mu.Lock()
	if r.parts.mode == nil {
		r.parts.mode = map[string]registry.PartitionSpec{}
	}
	r.parts.mode[c.Path] = new
	r.mu.Unlock()
	if !new.User {
		r.StopPartitions(c.Path)
	}
	switch primary := r.primary(c.Path); {
	case old == new:
	case new.User && !new.Global:
		if s := r.existingStateOf(c.Path, primary); s != nil {
			go r.stopState(s) // under the scan lock: never wait for a drain here
		}
	case old.User != new.User:
		r.Changed(c) // the running generation restarts with the new env; none starts
	}
}

// changedPartitions is Changed's part for c's user partitions (03 §A.7):
// the next build is a new one, every partition state is dirty and forgets
// its crashes, and the live ones restart, at most partitionSwapsPerTile at
// once, after the one shared build; the others build (reuse) on their next
// request. A partition its person may no longer run stops instead.
func (r *Runner) changedPartitions(c *registry.Component) {
	r.nextBuild(c.Path)
	var live []*state
	for _, s := range r.partStates(c.Path) {
		s.mu.Lock()
		s.dirty, s.crashes = true, nil
		l := s.cur != nil || s.building
		s.mu.Unlock()
		if l {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return
	}
	sem := make(chan struct{}, partitionSwapsPerTile)
	for _, s := range live {
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if _, ok := r.partitionSpec(c.Path); !ok || !r.shouldRun(c.Path, s.dep) ||
				r.ShouldRunPartition == nil || !r.ShouldRunPartition(c.Path, s.dep, s.pt.part) {
				r.stopPart(s, false)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, _ = r.ensureState(ctx, c, s)
		}()
	}
}

// ---- what a partition's generation spawns with ----

// partitionEnvKey is XBIN_PARTITION for a generation spawning from view c:
// its partition for a user partition's view; "global" for the primary of a
// tile running user partitions (its global instance) and for a
// non-primary deployment whose own code asks for partitions (PD-17: its one
// instance is global); "" otherwise — every unpartitioned tile's env is
// today's.
func (r *Runner) partitionEnvKey(c *registry.Component) string {
	switch {
	case c.Partition != "":
		return c.Partition
	case c.Deployment != "":
		if q, err := registry.ValidatePartition(c.Manifest); err == nil && q != nil {
			return PartitionGlobal
		}
		return ""
	}
	if _, ok := r.partitionSpec(c.Path); ok {
		return PartitionGlobal
	}
	return ""
}

// partitionSpawnable refuses a user partition's spawn that EnsurePartition
// wouldn't have admitted: another deployment than the primary, no
// isolation, a malformed id, a missing hook.
func (r *Runner) partitionSpawnable(c *registry.Component, dep string) error {
	switch {
	case !c.UserPartition() || !pkeyRe.MatchString(c.PartitionID) || c.Deployment != "":
		return fmt.Errorf("%w: %s: a malformed partition view", ErrPartitionRefused, c.Path)
	case dep != r.primary(c.Path):
		return fmt.Errorf("%w: people's partitions of %s run on its primary deployment only", ErrPartitionRefused, c.Path)
	case !r.Isolate:
		return fmt.Errorf("%w: a person's partition runs only in a sandbox (--isolate)", ErrPartitionRefused)
	case r.PartitionEnv == nil || r.RegisterPartitionInstance == nil:
		return fmt.Errorf("%w: %s's partition has no env or instance token binding", ErrPartitionRefused, c.Path)
	}
	return nil
}

// genEnv is the resource env of a generation spawning from view c.
func (r *Runner) genEnv(c *registry.Component, dep string) []string {
	if c.Partition != "" {
		env, _ := r.PartitionEnv(c, dep, c.Partition) // partitionSpawnable checked it
		return env
	}
	env, _ := r.envFor(c, dep)
	return env
}

// registerGen registers a generation's instance token from the runner's
// own state: a user partition's with its partition, any other through
// registerInstance.
func (r *Runner) registerGen(token string, c *registry.Component, dep string) {
	if c.Partition == "" {
		r.registerInstance(token, c.Path, dep)
		return
	}
	if f := r.RegisterPartitionInstance; f != nil {
		f(token, c.Path, dep, c.Partition)
	}
}

// leafOf is the cgroup leaf a generation spawning from view c starts in.
func (r *Runner) leafOf(c *registry.Component, dep string) string {
	if c.Partition != "" {
		return partitionLeaf(c.Path, dep, c.PartitionID)
	}
	return r.chooseLeaf(c.Path, dep)
}

// emitState publishes one runner event of state s: today's for every state
// but a user partition's, which goes to its person only (PartitionEvent),
// or nowhere.
func (r *Runner) emitState(s *state, typ, text string) {
	if s.pt == nil {
		r.emit(s.comp, s.dep, typ, text)
		return
	}
	if f := r.PartitionEvent; f != nil {
		f(s.comp, s.dep, s.pt.part, typ, text)
	}
}

// crashLoop is the breaker's error for state s running code: a user
// partition's names the partition, never its log's path (06 §5).
func (r *Runner) crashLoop(s *state, code Code, n int) error {
	if s.pt == nil {
		return crashLoopError(s.comp, s.dep, code, n)
	}
	return fmt.Errorf("%s's instance is crash-looping (%d exits); a change to the tile's code retries it — its log is theirs (bx logs in their terminal)", s.pt.part, n)
}

// afterExit is the crash watch's alwaysOn hook for state s: a user
// partition is never kept up (03 §A.6).
func (r *Runner) afterExit(c *registry.Component, s *state) {
	if s.pt == nil {
		r.afterExitOf(c, s.dep)
	}
}
