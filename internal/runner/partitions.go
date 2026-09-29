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
// partadmit.go's; the stops, the mode transitions and the restarts after a
// change partstop.go's; the spawn window, which every stop reaches,
// partstart.go's.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
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
	// tile to their partition id, the pkey ("u-" and 32 hex digits), and
	// their uid, minting the uid when they have none (the identity plane,
	// 02 §1, 03 §E). An error, or an answer of any other shape, refuses the
	// start. It is asked again before every spawn and on every change: a
	// state whose pkey is no longer the answer — its person was deleted and
	// recreated — stops, and never starts again.
	PartitionIdent func(tile, part string) (pkey, uid string, err error)
	// ShouldRunPartition gates a start of partition part of deployment dep
	// of tile, for the person PartitionIdent resolved as uid, beyond the
	// tile's own gates: that person exists with that same uid, is enabled
	// and can read the tile (PD-20), and the partition's namespaces aren't
	// held by encryption (03 §B.6). Asked at admission, again right before
	// every spawn, and on every change.
	ShouldRunPartition func(tile, dep, part, uid string) bool
	// PartitionEnv is the resource env of a generation of partition
	// c.Partition (the data plane's PartitionEnv, 03 §B.4): the values
	// EnvFor gives the tile, and the remap onto the partition's own
	// namespaces, which dataBinds binds (fail closed, resourceBindsFor).
	PartitionEnv func(c *registry.Component, dep, part string) (env []string, remap map[string]ResBind)
	// RegisterPartitionInstance registers a generation's instance token as
	// tile's principal acting in partition part of deployment dep for the
	// person whose uid is uid (auth's RegisterInstancePartition(token, tile,
	// dep, part, uid), 02 §2), from the runner's own state. It is called
	// under that state's lock, so no stop lands between it and the stop's
	// revocation (partstart.go): it must not call into the runner.
	RegisterPartitionInstance func(token, tile, dep, part, uid string)
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
	part, pkey, uid string    // "user:<id>", its pkey and its person's uid, as resolved when the state was made
	passive         int       // tracked passive streams (SSE): no use, no eviction guard (03 §A.5)
	lastInteractive time.Time // the last interactive request: in use for 2 min after
	handoff         time.Time // the last EnsurePartition answer: not evicted for partitionHandoff after it (the proxy tracks it meanwhile)
	// The spawn window (partstart.go): a generation past its gates and not
	// yet installed, which a stop must reach too.
	spawning chan struct{} // non-nil while a generation spawns; closed once the state holds it, or it stopped
	token    string        // the instance token registered for the spawning generation
	starting *instance     // a generation spawned, not yet installed
}

// partitionsState is the runner's partition bookkeeping.
type partitionsState struct {
	states map[string]*state                 // partStateKey → a user partition's state
	index  map[string]string                 // tile\0dep\0part → the pkey last resolved for it
	mode   map[string]registry.PartitionSpec // tile → its running spec, as PartitionsChanged last said
	spawns map[*registry.Component]*state    // a spawning generation's view → its state (partstart.go)
	adm    partAdmission                     // partadmit.go
	builds partBuilds                        // partadmit.go

	// draining counts, per partStateKey, the stops whose processes (a spawn
	// in flight included) may still run: PartitionRunning answers them.
	draining map[string]*partDrain
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
	pkey, uid, err := r.partitionGate(c, dep, part)
	if err != nil {
		return "", err
	}
	r.stopStale(c.Path, dep, part, pkey) // an earlier incarnation of the person never serves again
	key := partStateKey(c.Path, dep, pkey)
	if s := r.existingPart(key); s != nil {
		s.mu.Lock()
		r.touchLocked(s, class)
		switch {
		case !s.dirty && s.cur != nil:
			sock := s.cur.sock
			s.mu.Unlock()
			return sock, nil
		case !s.dirty && s.lastErr != nil && sticky(s.lastErr):
			err := s.lastErr
			s.mu.Unlock()
			return "", err
		case s.live(): // its own rebuild or start: blue/green is always admitted
			s.mu.Unlock()
			return r.ensureState(ctx, c, s)
		}
		failed := !s.dirty && s.lastErr != nil
		s.mu.Unlock()
		if failed { // a start that failed for this person alone: a fresh try, through admission
			r.stopPart(s, false)
		}
	}
	release, err := r.admitPartition(c.Path, key, class)
	if err != nil {
		if class == StartInteractive { // a deferred delivery retries; a refused person is an admin's metadata
			r.sbxFail(partitionView(c, part, pkey), sbx.Start, err)
		}
		return "", err
	}
	defer release()
	s := r.partStateOf(c.Path, dep, part, pkey, uid)
	s.mu.Lock()
	r.touchLocked(s, class)
	s.mu.Unlock()
	return r.ensureState(ctx, c, s)
}

// partitionGate is every check before a person's partition may exist: its
// key, the primary, isolation, the tile's running mode, a backend, the
// tile's own gates, the planes' hooks, the person's pkey and uid, and
// whether that person may run it (ShouldRunPartition). Nothing creates a
// state for a partition that fails one.
func (r *Runner) partitionGate(c *registry.Component, dep, part string) (pkey, uid string, err error) {
	refuse := func(format string, a ...any) (string, string, error) {
		return "", "", fmt.Errorf("%w: %s", ErrPartitionRefused, fmt.Sprintf(format, a...))
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
		return "", "", fmt.Errorf("component %s: %w", c.Path, err)
	}
	if !c.HasBackend() {
		return "", "", fmt.Errorf("component %s has no long-running backend", c.Path)
	}
	if !r.shouldRun(c.Path, dep) {
		why := "is not enabled"
		if r.HoldReason != nil {
			if w := r.HoldReason(c.Path); w != "" {
				why = w
			}
		}
		return "", "", fmt.Errorf("component %s %s", c.Path, why)
	}
	h := r.PartitionHooks
	if h.PartitionIdent == nil || h.ShouldRunPartition == nil || h.PartitionEnv == nil || h.RegisterPartitionInstance == nil {
		return refuse("this xbind can't give %s's partition of %s its own identity and data yet", id, c.Path)
	}
	pkey, uid, err = h.PartitionIdent(c.Path, part)
	if err != nil {
		return refuse("%s's partition of %s: %v", id, c.Path, err)
	}
	if !pkeyRe.MatchString(pkey) || uid == "" {
		return refuse("%s's partition of %s has no valid partition id", id, c.Path)
	}
	if !h.ShouldRunPartition(c.Path, dep, part, uid) { // the same uid (PD-20): the hook compares it
		return refuse("%s's partition of %s may not run now", id, c.Path)
	}
	r.mu.Lock()
	if r.parts.index == nil {
		r.parts.index = map[string]string{}
	}
	r.parts.index[partIndexKey(c.Path, dep, part)] = pkey
	r.mu.Unlock()
	return pkey, uid, nil
}

// touchLocked stamps a request of class on partition state s; callers hold
// s.mu. Every answer holds off eviction for partitionHandoff, so a
// delivery's socket isn't stopped before the proxy tracks its connection.
func (r *Runner) touchLocked(s *state, class StartClass) {
	now := r.now()
	s.lastReq = now
	if s.pt != nil {
		s.pt.handoff = now
		if class == StartInteractive {
			s.pt.lastInteractive = now
		}
	}
}

// partStateOf is the state of user partition pkey, created dirty.
func (r *Runner) partStateOf(tile, dep, part, pkey, uid string) *state {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := partStateKey(tile, dep, pkey)
	if r.parts.states == nil {
		r.parts.states = map[string]*state{}
	}
	s, ok := r.parts.states[k]
	if !ok {
		s = &state{comp: tile, dep: dep, dirty: true, pt: &partInfo{part: part, pkey: pkey, uid: uid}}
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

// PartitionDataBinds is what a generation of person partition part (key
// pkey) of view c would bind of its data — its PartitionEnv's env through
// dataBinds — or why its start would refuse it. It starts nothing: the
// planes' integration tests and diagnostics ask it.
func (r *Runner) PartitionDataBinds(c *registry.Component, part, pkey string) ([]sandbox.Bind, error) {
	v := partitionView(c, part, pkey)
	if r.PartitionEnv == nil || !v.UserPartition() {
		return nil, fmt.Errorf("%w: %s's partition %q has no env", ErrPartitionRefused, c.Path, part)
	}
	return r.dataBinds(v, r.genEnv(v, r.viewDeployment(v)))
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

// emitPartition publishes one runner event of a person's partition's view
// c (its env layer's setup, env.go) to its person only, or nowhere.
func (r *Runner) emitPartition(c *registry.Component, typ, text string) {
	if f := r.PartitionEvent; f != nil {
		f(c.Path, r.primary(c.Path), c.Partition, typ, text)
	}
}

// crashLoop is the breaker's error for state s running code: a user
// partition's names the partition, never its log's path (06 §5).
func (r *Runner) crashLoop(s *state, code Code, n int) error {
	if s.pt == nil {
		return crashLoopError(s.comp, s.dep, code, n)
	}
	return partCrashLoop{fmt.Errorf("%s's instance is crash-looping (%d exits); a change to the tile's code retries it — its log is theirs (bx logs in their terminal)", s.pt.part, n)}
}

// afterExit is the crash watch's alwaysOn hook for state s: a user
// partition is never kept up (03 §A.6).
func (r *Runner) afterExit(c *registry.Component, s *state) {
	if s.pt == nil {
		r.afterExitOf(c, s.dep)
	}
}
