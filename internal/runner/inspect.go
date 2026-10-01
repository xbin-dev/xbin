package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/util"
)

// nsKinds are the namespaces we report per backend.
var nsKinds = []string{"net", "mnt", "pid", "user", "ipc", "uts", "cgroup"}

// NS is one namespace's identity + whether it differs from xbind's (isolated).
type NS struct {
	ID       string `json:"id"`
	Isolated bool   `json:"isolated"`
}

// Backend is the full runtime picture of one component's backend.
type Backend struct {
	Path     string `json:"path"`
	Runtime  string `json:"runtime"`
	State    string `json:"state"`
	Isolated bool   `json:"isolated"`
	// Sandbox is how it is isolated — "vm", "namespace" or "host": a running
	// generation's, else how it would start. For a VM, PID, Namespaces, RSS,
	// Threads and FDs describe its host-side jail (the shim, whose child VMM
	// holds the guest's memory); Cgroup covers both.
	Sandbox     string        `json:"sandbox"`
	VM          *BackendVM    `json:"vm,omitempty"`
	PID         int           `json:"pid,omitempty"`
	Gen         int           `json:"gen"`
	Restarts    int           `json:"restarts"`
	ActiveConns int           `json:"activeConns"`
	UptimeSec   int64         `json:"uptimeSec,omitempty"`
	LastReqSec  int64         `json:"lastReqSec"` // seconds since last request (-1 = never)
	Error       string        `json:"error,omitempty"`
	RSSKB       int64         `json:"rssKb,omitempty"`
	Threads     int           `json:"threads,omitempty"`
	FDs         int           `json:"fds,omitempty"`
	CPUSec      float64       `json:"cpuSec,omitempty"`
	Namespaces  map[string]NS `json:"namespaces,omitempty"`
	Egress      []string      `json:"egress,omitempty"`
	Activity    *relay.Stats  `json:"activity,omitempty"`
	Cgroup      *cgroup.Usage `json:"cgroup,omitempty"`
	// Network labels (D54), filled by the daemon from the broker's view:
	// the bound/defaulted ref, the effective mode, where the reach comes
	// from, and why the binding is inert (if it is).
	NetRef    string `json:"netRef,omitempty"`
	Net       string `json:"net,omitempty"`
	NetSource string `json:"netSource,omitempty"`
	NetNote   string `json:"netNote,omitempty"`
	// Checkpoint is the full tree id of the checkpoint the running
	// generation runs; absent while it runs the work tree (D119e).
	Checkpoint string `json:"checkpoint,omitempty"`
	// Deployment names the deployment of a row InspectDeployments gives;
	// Inspect's rows, the primaries', leave it empty.
	Deployment string `json:"deployment,omitempty"`
	// Partition names the person's partition of a row InspectPartitions
	// gives ("user:<id>"): metadata, never what it holds.
	Partition string `json:"partition,omitempty"`
}

// Inspect returns the runtime picture of every known component backend: one
// row per tile, its primary's (11-contract §8).
func (r *Runner) Inspect() []Backend {
	return r.inspect(true)
}

// InspectDeployments returns the runtime picture of every deployment's
// backend that isn't its tile's primary, each row naming its deployment;
// empty while no tile runs one.
func (r *Runner) InspectDeployments() []Backend {
	return r.inspect(false)
}

// InspectPartitions returns the runtime picture of every person's
// partition instance, each row naming its partition — the admin's metadata
// rows (plans/partitions/06 §5); empty while no tile runs one.
func (r *Runner) InspectPartitions() []Backend {
	return r.inspectStates(r.partStates(""), false)
}

func (r *Runner) inspect(primaries bool) []Backend {
	var states []*state
	for _, s := range r.allStates("") {
		if (s.dep == r.primary(s.comp)) == primaries {
			states = append(states, s)
		}
	}
	return r.inspectStates(states, !primaries)
}

// inspectStates is the rows of states; named: each names its deployment.
func (r *Runner) inspectStates(states []*state, named bool) []Backend {
	self := selfNS()
	out := make([]Backend, 0, len(states))
	for _, s := range states {
		s.mu.Lock()
		b := Backend{
			Path: s.comp, State: stateName(s), Gen: s.gen, Restarts: len(s.crashes),
			ActiveConns: s.active,
		}
		if named {
			b.Deployment = s.dep
		}
		if s.pt != nil {
			b.Partition = s.pt.part
		}
		c, known := r.Reg.Component(s.comp)
		if known {
			b.Runtime = c.Manifest.Runtime
			b.Isolated = r.Isolate && sandboxable(c.Manifest.Runtime)
			b.Sandbox = string(r.intendedMode(c))
		}
		if s.lastErr != nil {
			b.Error = s.lastErr.Error()
		}
		if !s.lastReq.IsZero() {
			b.LastReqSec = int64(time.Since(s.lastReq).Seconds())
		} else {
			b.LastReqSec = -1
		}
		inst := s.cur
		s.mu.Unlock()

		if inst != nil {
			if known {
				b.Sandbox = string(r.modeOf(c, inst.sock))
			}
			if v, ok := r.vmInfo(inst.sock); ok {
				b.VM = &v
			}
			b.UptimeSec = int64(time.Since(inst.started).Seconds())
			b.Checkpoint = inst.code.Tree
			b.Egress = inst.egress
			if inst.relay != nil {
				st := inst.relay.Stats()
				b.Activity = &st
			}
			if inst.cmd != nil && inst.cmd.Process != nil {
				b.PID = inst.cmd.Process.Pid
				fillProc(&b, self)
			}
			if r.Cgroup != nil {
				leaf := inst.leaf
				if leaf == "" {
					leaf = util.CompKey(b.Path) // the flat leaf (07-runtime §10.3)
				}
				if u, ok := r.Cgroup.Usage(leaf); ok {
					b.Cgroup = &u
				}
			}
		}
		out = append(out, b)
	}
	// Stable order (r.states is a map) so the admin runtime view doesn't reshuffle
	// between polls.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Deployment != out[j].Deployment {
			return out[i].Deployment < out[j].Deployment
		}
		return out[i].Partition < out[j].Partition
	})
	return out
}

func stateName(s *state) string {
	switch {
	case s.building:
		return "building"
	case s.cur != nil:
		return "healthy"
	case s.lastErr != nil:
		return "failed"
	}
	return "idle"
}

// fillProc reads per-process stats from /proc (best-effort; the process may exit
// under us). self is xbind's own namespace ids for the isolation comparison.
func fillProc(b *Backend, self map[string]string) {
	proc := filepath.Join("/proc", strconv.Itoa(b.PID))

	// namespaces
	ns := map[string]NS{}
	for _, k := range nsKinds {
		if link, err := os.Readlink(filepath.Join(proc, "ns", k)); err == nil {
			ns[k] = NS{ID: link, Isolated: self[k] != "" && link != self[k]}
		}
	}
	if len(ns) > 0 {
		b.Namespaces = ns
	}

	// memory + threads from /proc/<pid>/status
	if data, err := os.ReadFile(filepath.Join(proc, "status")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			switch {
			case strings.HasPrefix(line, "VmRSS:"):
				b.RSSKB = firstInt(line)
			case strings.HasPrefix(line, "Threads:"):
				b.Threads = int(firstInt(line))
			}
		}
	}

	// cpu from /proc/<pid>/stat (utime+stime jiffies → seconds)
	if data, err := os.ReadFile(filepath.Join(proc, "stat")); err == nil {
		if f := statFields(string(data)); len(f) > 15 {
			utime, _ := strconv.ParseInt(f[13], 10, 64)
			stime, _ := strconv.ParseInt(f[14], 10, 64)
			b.CPUSec = float64(utime+stime) / 100.0 // USER_HZ = 100
		}
	}

	// open fds
	if ents, err := os.ReadDir(filepath.Join(proc, "fd")); err == nil {
		b.FDs = len(ents)
	}
}

func selfNS() map[string]string {
	m := map[string]string{}
	for _, k := range nsKinds {
		if link, err := os.Readlink(filepath.Join("/proc/self/ns", k)); err == nil {
			m[k] = link
		}
	}
	return m
}

func firstInt(line string) int64 {
	for _, f := range strings.Fields(line) {
		if n, err := strconv.ParseInt(f, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// statFields splits /proc/<pid>/stat, treating the parenthesized comm (which
// may contain spaces) as one field so later indices line up.
func statFields(s string) []string {
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close < 0 || close < open {
		return strings.Fields(s)
	}
	out := []string{s[:open-1], s[open+1 : close]}
	return append(out, strings.Fields(s[close+1:])...)
}

// ---- what a generation runs (D119e, 07-runtime §1, §7, §9) ----

// genPlan is what one generation starts from.
type genPlan struct {
	view     *registry.Component // what it spawns from: c itself for the work tree
	bin      string              // its runnable entry
	root     string              // the materialized checkpoint bound at c.Dir; "" = the work tree
	artifact string              // the checkpoint artifact's directory; "" = none kept (the work tree's bin, node, python)
	release  func()              // drops root and artifact from what generations use; idempotent
}

// resolveGen is resolveGenFor c's primary.
func (r *Runner) resolveGen(c *registry.Component, code Code) (genPlan, error) {
	return r.resolveGenFor(c, r.primary(c.Path), code)
}

// resolveGenFor resolves what a generation of deployment dep of c running
// code starts from. The primary's work tree: c itself and a build of it,
// exactly as before tile deployments, with no hook but CodeFor asked (D119c,
// D119d); another deployment's work tree: its view of it, built the same way. A
// checkpoint: its materialized tree, the deployment's view of it (whose
// CodeRoot is that tree, never a mutated copy: views are shared), and for Go
// its artifact, kept per (tile, checkpoint) whichever deployment runs it and
// built only when none is (a restart never compiles). Nothing falls back to
// the work tree (06-security C7): a checkpoint that can't be materialized,
// viewed or built fails the start, and without isolation nothing can show
// it at c.Dir, so it never starts (07-runtime §12).
func (r *Runner) resolveGenFor(c *registry.Component, dep string, code Code) (genPlan, error) {
	if code.WorkTree {
		v := c
		if dep != r.primary(c.Path) {
			var err error
			if v, err = r.viewOf(c, dep, code, ""); err != nil {
				return genPlan{}, err
			}
		}
		bin, err := r.buildWorkTree(c, dep, v) // once per change for a partitioned tile (partadmit.go)
		return genPlan{view: v, bin: bin, release: func() {}}, err
	}
	switch {
	case !isTreeID(code.Tree):
		return genPlan{}, fmt.Errorf("%s: no code to run (checkpoint %q)", c.Path, code.Tree)
	case !r.Isolate:
		return genPlan{}, fmt.Errorf("%s is pinned to checkpoint c:%.7s, and a pinned backend runs only with --isolate: it isn't started, and its work tree never runs in its place", c.Path, code.Tree)
	}
	root, err := r.materialize(c.Path, code.Tree)
	if err != nil {
		return genPlan{}, err
	}
	// Held from here, so checkpoint GC can't take the tree before it is bound.
	g := genPlan{root: root, release: r.inUse.hold(&r.inUse.roots, root)}
	fail := func(err error) (genPlan, error) { g.release(); return genPlan{}, err }
	v, err := r.viewOf(c, dep, code, root)
	switch {
	case err != nil:
		return fail(err)
	case root == "" || v == nil || filepath.Clean(v.CodeRoot) != filepath.Clean(root):
		return fail(fmt.Errorf("%s: the view of checkpoint c:%.7s doesn't run from its materialized tree", c.Path, code.Tree))
	}
	g.view = v
	if v.Manifest.Runtime != "go" { // node and python run their entry from the tree itself
		if g.bin, err = r.buildCheckpoint(v, code); err != nil {
			return fail(err)
		}
		return g, nil
	}
	dir := filepath.Join(r.checkpointArtifacts(c.Path), code.Tree)
	relArt := r.inUse.hold(&r.inUse.arts, dir) // before the lookup: pruning skips it
	relRoot := g.release
	g.artifact, g.release = dir, func() { relRoot(); relArt() }
	if bin, ok := r.Artifact(c, code.Tree); ok {
		g.bin = bin
		return g, nil
	}
	if _, err := r.buildCheckpoint(v, code); err != nil {
		return fail(err)
	}
	bin, ok := r.Artifact(c, code.Tree)
	if !ok {
		return fail(fmt.Errorf("%s: building checkpoint c:%.7s left no artifact", c.Path, code.Tree))
	}
	g.bin = bin
	return g, nil
}

// buildCheckpoint builds checkpoint code from its view v, whose CodeRoot is
// the materialized tree: a Go artifact in checkpointArtifacts, or a node or
// python entry checked beneath the tree (buildCode, build.go). The engine's
// build stands in for it in tests. Never the work tree in its place.
func (r *Runner) buildCheckpoint(v *registry.Component, code Code) (string, error) {
	if e := r.engine; e != nil && e.build != nil {
		return e.build(v)
	}
	return r.buildCode(v, code)
}

// inUse counts, per host path, the generations that use it, from their
// preparation until they exit: the materialized trees they bind (roots) and
// the checkpoint artifacts they run (arts). GC never removes either
// (06-security T18). The zero value is ready.
type inUse struct {
	mu          sync.Mutex
	roots, arts map[string]int
}

// hold counts one more user of p in *m until the returned release, which is
// idempotent. An empty p holds nothing.
func (u *inUse) hold(m *map[string]int, p string) func() {
	if p == "" {
		return func() {}
	}
	u.mu.Lock()
	if *m == nil {
		*m = map[string]int{}
	}
	(*m)[p]++
	u.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			u.mu.Lock()
			if (*m)[p]--; (*m)[p] <= 0 {
				delete(*m, p)
			}
			u.mu.Unlock()
		})
	}
}

// RootsInUse lists the materialized checkpoint trees that generations bind,
// sorted, which checkpoint GC must never remove (its keep set). A generation
// of the work tree binds none, so it is nil while no checkpoint runs.
func (r *Runner) RootsInUse() []string {
	r.inUse.mu.Lock()
	defer r.inUse.mu.Unlock()
	if len(r.inUse.roots) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.inUse.roots))
	for p := range r.inUse.roots {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// checkpointArtifacts holds tile's checkpoint artifacts, beside the live
// reload target's own .xbin/build/<CompKey>/bin, which is never one of them
// (07-runtime §3.1, §9). A checkpoint build writes <tree>.tmp-<rand>/ there —
// bin, and build.json recording at least {"tile": <tile path>, "tree":
// <tree>} — and renames it to <tree> on success, so a crash never leaves a
// partial artifact that looks valid. Artifacts are keyed by (tile, tree),
// not by deployment.
func (r *Runner) checkpointArtifacts(tile string) string {
	return filepath.Join(r.Root, ".xbin", "build", util.CompKey(tile), "c")
}

// Artifact returns the Go binary kept for tile c's checkpoint tree, and
// whether one is: a regular bin beside a build.json that records c's path
// and the tree (the CompKey in the directory keeps 32 bits of hash, so the
// path is checked). Nothing is followed out of, or into, the artifact's
// directory.
func (r *Runner) Artifact(c *registry.Component, tree string) (string, bool) {
	if !isTreeID(tree) || artifactTile(r.Root, c.Path, tree) != c.Path {
		return "", false
	}
	f, err := fsutil.OpenIn(filepath.Join(r.Root, ".xbin", "build"), artifactSub(c.Path, tree), "bin")
	if err != nil {
		return "", false
	}
	fi, err := f.Stat()
	f.Close()
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return filepath.Join(r.checkpointArtifacts(c.Path), tree, "bin"), true
}

// artifactSub is an artifact's directory relative to .xbin/build.
func artifactSub(tile, tree string) string { return path.Join(util.CompKey(tile), "c", tree) }

// artifactTile is the tile path the build.json of tile's artifact directory
// entry records: "" when it has none that parses, or it records another
// tree than the entry's name.
func artifactTile(root, tile, entry string) string {
	f, err := fsutil.OpenIn(filepath.Join(root, ".xbin", "build"), artifactSub(tile, entry), "build.json")
	if err != nil {
		return ""
	}
	defer f.Close()
	var stamp struct {
		Tile string `json:"tile"`
		Tree string `json:"tree"`
	}
	if json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&stamp) != nil || stamp.Tree != entry {
		return ""
	}
	return stamp.Tile
}

// artifactTmpAge is how old a build's temporary directory must be before
// pruning takes it for a crashed build's leftover: well past a build's
// 20-minute timeout (build.go).
const artifactTmpAge = time.Hour

// PruneArtifacts removes tile's checkpoint artifacts except those of the
// trees in keep and those a generation still runs (06-security T18). The
// deployments plane names keep: each deployment's current checkpoint and
// those of its previous three deploy-log entries (NP-02-3), so a restart or
// a roll back to them never compiles. It runs after a successful deploy, at
// boot and on a deployment's removal (07-runtime §2.8). Left alone: the
// live reload target's bin, another tile's artifact under the same CompKey,
// and a build still writing its temporary directory. An entry with no valid
// build.json is no artifact (Artifact refuses it, and it would block the
// rebuild's rename), so it goes even when kept.
func (r *Runner) PruneArtifacts(tile string, keep []string) error {
	dir := r.checkpointArtifacts(tile)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	kept := make(map[string]bool, len(keep))
	for _, t := range keep {
		kept[t] = true
	}
	var errs []error
	for _, e := range ents {
		name := e.Name()
		if strings.Contains(name, ".tmp-") {
			if fi, err := e.Info(); err != nil || time.Since(fi.ModTime()) < artifactTmpAge {
				continue
			}
		} else if owner := artifactTile(r.Root, tile, name); owner != "" && (owner != tile || kept[name]) {
			continue
		}
		p := filepath.Join(dir, name)
		r.inUse.mu.Lock() // held across the removal: a start can't take it meanwhile
		if r.inUse.arts[p] == 0 {
			if err := os.RemoveAll(p); err != nil {
				errs = append(errs, err)
			}
		}
		r.inUse.mu.Unlock()
	}
	return errors.Join(errs...)
}

// isTreeID reports whether s is a full git tree id: 40 lowercase hex digits
// (SHA-1, the store's format), or 64 (SHA-256).
func isTreeID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
