package runner

// deployments.go — runner state keyed by (tile, deployment) (07-runtime
// §1.2, §9): the state map's key, the view a non-primary deployment spawns
// from, what a generation of a deployment spawns with (its run dir, log,
// env and instance token), the admission caps on non-primary deployments
// (§10.3), and the restarts a primary reassignment asks for (§8.8). A tile
// without a deployment record only ever has main, the primary, whose key,
// run dir, log, env and token are today's (§1.4).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

// The admission caps on non-primary deployments (07-runtime §10.3) (D127q):
// v1 constants, not settings. The deployments plane refuses to add past the
// first two; the runner refuses a start past any of them, so a record that
// holds more than the plane allows still never runs more.
const (
	MaxNonPrimaryPerTile      = 3
	MaxNonPrimaryPerWorkspace = 24
	MaxNonPrimaryRunning      = 12 // backends running at once, workspace-wide
)

// stateKey keys a deployment's runner state: main's key is the tile path,
// today's key, so a tile without a record keeps a one-to-one map with
// today's (07-runtime §1.4); any other deployment's joins the tile path and
// its name with a NUL, which no path contains.
func stateKey(tile, dep string) string {
	if dep == util.MainDeployment {
		return tile
	}
	return tile + "\x00" + dep
}

// stateOf is deployment dep of tile's runner state, created dirty when
// missing (a fresh state builds on its first Ensure).
func (r *Runner) stateOf(tile, dep string) *state {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := stateKey(tile, dep)
	s, ok := r.states[k]
	if !ok {
		s = &state{comp: tile, dep: dep, dirty: true}
		r.states[k] = s
	}
	return s
}

// existingStateOf is deployment dep of tile's runner state, never creating
// one.
func (r *Runner) existingStateOf(tile, dep string) *state {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.states[stateKey(tile, dep)]
}

// existingState is comp's primary's runner state, never creating one.
func (r *Runner) existingState(comp string) *state {
	return r.existingStateOf(comp, r.primary(comp))
}

// allStates is every state, in no order; tile narrows to one tile's.
func (r *Runner) allStates(tile string) []*state {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*state, 0, len(r.states))
	for _, s := range r.states {
		if tile == "" || s.comp == tile {
			out = append(out, s)
		}
	}
	return out
}

// dropState forgets s (its deployment was removed) and marks it gone, so a
// build that finishes on it afterwards stops its generation instead of
// installing it (install).
func (r *Runner) dropState(s *state) {
	r.mu.Lock()
	if k := stateKey(s.comp, s.dep); r.states[k] == s {
		delete(r.states, k)
	}
	r.mu.Unlock()
	s.mu.Lock()
	s.gone = true
	s.mu.Unlock()
}

// install makes inst s's current generation, unless s's deployment was
// removed meanwhile: then inst stops and install reports false. stamp
// counts the swap as a request (a deploy's), for the idle reaper.
func (r *Runner) install(s *state, inst *instance, stamp bool) bool {
	s.mu.Lock()
	gone := s.gone
	if !gone {
		s.cur = inst
		if stamp {
			s.lastReq = r.now()
		}
	}
	s.mu.Unlock()
	if gone {
		r.stopGen(inst, 2*time.Second)
	}
	return !gone
}

// live: a generation runs, or one is being built; callers hold s.mu.
func (s *state) live() bool { return s.cur != nil || s.building }

// ensureOther is EnsureDeployment for a deployment that isn't the primary:
// a healthy generation answers at once; otherwise the record must hold the
// deployment and its own code must have a backend, which then builds and
// starts under the state's single flight, as Ensure's do. Nothing about a
// name the record doesn't hold creates a state.
func (r *Runner) ensureOther(ctx context.Context, c *registry.Component, dep string) (string, error) {
	if s := r.existingStateOf(c.Path, dep); s != nil {
		s.mu.Lock()
		if !s.dirty && s.cur != nil {
			sock := s.cur.sock
			s.lastReq = r.now()
			s.mu.Unlock()
			return sock, nil
		}
		s.mu.Unlock()
	}
	code, err := r.codeFor(c.Path, dep)
	if err != nil {
		return "", err
	}
	v, err := r.viewOf(c, dep, code, "")
	if err != nil {
		return "", err
	}
	if err := registry.ValidateRuntime(v.Manifest); err != nil {
		return "", fmt.Errorf("component %s: %w", c.Path, err) // runtime "cgi" (D117): never runs
	}
	if !v.HasBackend() {
		return "", fmt.Errorf("deployment %s of %s has no long-running backend", dep, c.Path)
	}
	if !r.Isolate { // nothing binds its own data at its paths (D119h)
		return "", fmt.Errorf("%s: deployment %s runs only in a sandbox (--isolate), and this xbind runs backends without one", c.Path, dep)
	}
	if !r.shouldRun(c.Path, dep) {
		return "", fmt.Errorf("component %s is not enabled", c.Path)
	}
	return r.ensureState(ctx, c, r.stateOf(c.Path, dep))
}

// viewOf is the component a generation of deployment dep of c running code
// spawns from (07-runtime §5.1). The primary's is View's, as before
// deployments. Any other deployment's is the registry's view that names it
// (Deployment set, so the spawn-time hooks answer it as non-primary, its
// documents never chrome), over its own code: the work tree's scan, or the
// checkpoint's materialized tree (root; "" materializes it here).
func (r *Runner) viewOf(c *registry.Component, dep string, code Code, root string) (*registry.Component, error) {
	if dep == r.primary(c.Path) {
		return r.view(c, code)
	}
	if !code.WorkTree && root == "" {
		var err error
		if root, err = r.materialize(c.Path, code.Tree); err != nil {
			return nil, err
		}
	}
	if r.Reg == nil {
		return nil, fmt.Errorf("%s: deployment %s has no view without the registry", c.Path, dep)
	}
	return r.Reg.View(c, registry.ViewCode{Deployment: dep, Tree: code.Tree, Root: root})
}

// startFor starts generation gen of deployment dep from bin, its view c
// (07-runtime §9): past the admission caps, through the engine's start in
// tests, startDeployment otherwise.
func (r *Runner) startFor(c *registry.Component, dep, bin string, gen int) (*instance, error) {
	if c.Partition == "" && c.Deployment == "" && r.noGlobal(c.Path, dep) { // every path's one door (partitions.go)
		return nil, globalRefusal(c.Path)
	}
	if err := r.admit(c, dep); err != nil {
		r.sbxFail(c, sbx.Start, err)
		return nil, err
	}
	var inst *instance
	var err error
	if e := r.engine; e != nil && e.start != nil {
		inst, err = r.startGen(c, bin, gen)
	} else {
		inst, err = r.startDeployment(c, dep, bin, gen)
	}
	if inst != nil {
		inst.dep = dep
	}
	return inst, err
}

// genSpawn is what one generation of a deployment spawns with.
type genSpawn struct {
	dir, sock string   // its run dir and listen socket
	log       string   // its backend log, absolute
	token     string   // its instance token
	env       []string // XBIN_* and the deployment's resource env
}

// spawnSetup prepares what generation gen of deployment dep, spawning from
// its view c, starts with (07-runtime §9; 11-contract §5):
//   - its run dir: main's is today's <RunDir>/<CompKey>; any other
//     deployment's the sibling sockDir names, never inside main's, which is
//     bound read-write into main's sandbox;
//   - its log: main's is today's .xbin/log/<CompKey>.log, any other
//     deployment's .xbin/deploy/<TileKey>/d/<name>/backend.log;
//   - a fresh instance token;
//   - today's XBIN_SOCKET, XBIN_COMPONENT (the bare tile path), XBIN_GATEWAY
//     and XBIN_TOKEN, plus XBIN_DEPLOYMENT=<name> when the view is not the
//     primary's (the role rule), then EnvFor's env for the deployment;
//   - XBIN_PARTITION where partitionEnvKey says, right after XBIN_COMPONENT
//     (after XBIN_DEPLOYMENT when that is set); never on an unpartitioned
//     tile, whose env stays today's.
//
// A person's partition's view (c.Partition) takes its own run dir
// (partSockDir), log (partitionLog), env (PartitionEnv) and token
// (registerGen) instead (plans/partitions/03 §A.4).
//
// A view that names another deployment than dep is refused, and so is a
// deployment other than main while auth can't bind its instance token to
// it (the token would authenticate as main).
func (r *Runner) spawnSetup(c *registry.Component, dep string, gen int) (genSpawn, error) {
	if c.Deployment != "" && c.Deployment != dep {
		return genSpawn{}, fmt.Errorf("%s: the view of deployment %s can't spawn deployment %s", c.Path, c.Deployment, dep)
	}
	if dep != util.MainDeployment && r.Auth != nil {
		if _, ok := any(r.Auth).(deploymentRegistrar); !ok {
			return genSpawn{}, fmt.Errorf("%s: deployment %s can't start: its instance token can't be bound to it", c.Path, dep)
		}
	}
	dir, logRel := sockDir(c.Path, dep), deploymentLog(c.Path, dep)
	if c.Partition != "" {
		if err := r.partitionSpawnable(c, dep); err != nil {
			return genSpawn{}, err
		}
		dir, logRel = partSockDir(c.Path, dep, c.PartitionID), partitionLog(c.Path, dep, c.PartitionID)
	}
	sp := genSpawn{dir: filepath.Join(r.RunDir, dir), log: filepath.Join(r.Root, filepath.FromSlash(logRel))}
	if err := os.MkdirAll(sp.dir, 0o755); err != nil {
		return genSpawn{}, err
	}
	if dep != util.MainDeployment || c.Partition != "" {
		if err := os.MkdirAll(filepath.Dir(sp.log), 0o755); err != nil {
			return genSpawn{}, err
		}
	}
	sp.sock = filepath.Join(sp.dir, fmt.Sprintf("g%d.sock", gen))
	_ = os.Remove(sp.sock)
	sp.token = util.RandomToken(24)
	sp.env = append(backendEnv(r.Isolate && sandboxable(c.Manifest.Runtime)),
		"XBIN_SOCKET="+sp.sock,
		"XBIN_COMPONENT="+c.Path,
	)
	part := r.partitionEnvKey(c)
	if part != "" && c.Deployment == "" {
		sp.env = append(sp.env, "XBIN_PARTITION="+part)
	}
	sp.env = append(sp.env,
		"XBIN_GATEWAY="+filepath.Join(r.RunDir, "gateway.sock"),
		"XBIN_TOKEN="+sp.token,
	)
	if c.Deployment != "" {
		sp.env = append(sp.env, "XBIN_DEPLOYMENT="+dep)
		if part != "" {
			sp.env = append(sp.env, "XBIN_PARTITION="+part)
		}
	}
	sp.env = append(sp.env, r.genEnv(c, dep)...)
	return sp, nil
}

// deploymentRegistrar is auth's per-deployment instance registration
// (internal/auth/deployment.go): the token authenticates as the tile's
// principal bound to that deployment (D127g).
type deploymentRegistrar interface {
	RegisterInstanceDeployment(token, component, deployment string)
}

// registerInstance registers a generation's instance token for deployment
// dep of tile, from the runner's own state, never from env or headers
// (06-security T3c): main's through today's call.
func (r *Runner) registerInstance(token, tile, dep string) {
	if dep == util.MainDeployment {
		r.Auth.RegisterInstance(token, tile)
		return
	}
	if a, ok := any(r.Auth).(deploymentRegistrar); ok { // spawnSetup refused the start otherwise
		a.RegisterInstanceDeployment(token, tile, dep)
	}
}

// ErrAdmission refuses a start of a non-primary deployment past the
// admission caps (D127q); it is an sbx refusal.
var ErrAdmission = errors.New("non-primary deployments at their cap")

// admit is the admission check of a generation spawning from view c
// (07-runtime §10.3) (D127q). The primary's view always starts. A
// non-primary deployment's starts only while its tile has fewer than
// MaxNonPrimaryPerTile other non-primary deployments running or building,
// the workspace fewer than MaxNonPrimaryPerWorkspace, and fewer than
// MaxNonPrimaryRunning non-primary backends run; its own generations never
// count, so blue/green over them is always admitted.
func (r *Runner) admit(c *registry.Component, dep string) error {
	if c.Deployment == "" {
		return nil
	}
	tileN, wsN, running := 0, 0, 0
	for _, s := range r.allStates("") {
		if s.comp == c.Path && s.dep == dep {
			continue
		}
		s.mu.Lock()
		live, runs := s.live(), s.cur != nil
		s.mu.Unlock()
		if !live || s.dep == r.primary(s.comp) {
			continue
		}
		wsN++
		if s.comp == c.Path {
			tileN++
		}
		if runs {
			running++
		}
	}
	var why string
	switch {
	case tileN >= MaxNonPrimaryPerTile:
		why = fmt.Sprintf("%s already runs %d non-primary deployments, the most a tile runs", c.Path, tileN)
	case wsN >= MaxNonPrimaryPerWorkspace:
		why = fmt.Sprintf("the workspace already runs %d non-primary deployments, the most it runs", wsN)
	case running >= MaxNonPrimaryRunning:
		why = fmt.Sprintf("%d non-primary backends already run, the most that run at once", running)
	default:
		return nil
	}
	return sbx.Refuse(fmt.Errorf("deployment %s of %s isn't started: %s: %w", dep, c.Path, why, ErrAdmission))
}

// Reassign restarts a tile's running generations after the deployments
// plane reassigned its primary from one deployment to another, so every
// backend's spawn-time env and wiring say what it is now (07-runtime §8.8;
// XBIN_DEPLOYMENT holds for a process's life, 11-contract §0.2). Primary
// already answers to. The new primary restarts first, blue/green, from the
// code its record names (its kept artifact: nothing compiles for a
// checkpoint), then the old one, now non-primary; a restart of the old one
// that fails stops it, so no generation keeps the primary's wiring. A
// deployment with no generation starts on its next request. Then the frames
// learn: one bare reload for the tile (the bare URL serves other code), and a
// deployments reload naming the old primary. Events of today's types never
// name a deployment (rule C2).
func (r *Runner) Reassign(ctx context.Context, c *registry.Component, from, to string) error {
	r.StopPartitions(c.Path) // people's partitions start again on the new primary, on their next use
	var errs []error
	order := []string{to, from}
	if from == to {
		order = order[:1]
	}
	for _, dep := range order {
		s := r.existingStateOf(c.Path, dep)
		if s == nil {
			continue
		}
		s.mu.Lock()
		live := s.live()
		s.mu.Unlock()
		if !live {
			continue
		}
		if err := r.Restart(ctx, c, dep, nil); err != nil {
			errs = append(errs, fmt.Errorf("restarting %s: %w", dep, err))
			if dep == from {
				r.stopState(s)
			}
		}
	}
	r.emit(c.Path, to, "reload", "")
	if from != to {
		r.emit(c.Path, from, "reload", "")
	}
	return errors.Join(errs...)
}

// stopState stops s's current generation, leaving the state (its next
// request starts one again).
func (r *Runner) stopState(s *state) {
	s.mu.Lock()
	inst := s.cur
	s.cur = nil
	s.mu.Unlock()
	if inst != nil {
		r.stopGen(inst, 5*time.Second)
	}
}
