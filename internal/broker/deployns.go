package broker

// deployns.go — a (scope, name) data namespace's life (08-data §6.3, §6.4,
// §8.2, §9) (D127i) (D127t): ns.json, from which the data state is derived; the
// hold and the write gate; reset; deletion by the last claimant (claimants
// are derived, never stored); the sweep of orphans. GC never deletes main's
// data, every ns.json write happens under the namespace's hold, and a
// workspace without deployments gains nothing on disk (12-compat PO-7).

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	nsMetaFile    = "ns.json"
	nsHistoryMax  = 32                  // ns.json keeps the latest entries
	nsOrphanGrace = 14 * 24 * time.Hour // outlives a scope.json that vanished for a while (§6.6)
	nsRetryAfter  = "5"                 // seconds, on every 503 of a hold or the write gate
)

// nsWriteWait bounds an API write's wait on the write gate (§8.2).
var nsWriteWait = 30 * time.Second

// The data states (11-contract §1.1's data.state; original: main without
// metadata, today's keys untouched), and the acts that hold a namespace.
const (
	nsOriginal, nsEmpty, nsSeeded, nsRestored, nsPartial        = "original", "empty", "seeded", "restored", "partial"
	nsSeeding, nsResetting, nsRestoring, nsRemoving, nsSweeping = "seeding", "resetting", "restoring", "removing", "sweeping"
)

// nsActs: the 409's "<name>'s data is being <done>", and a crash's act.
var nsActs = map[string]struct{ done, act string }{
	nsSeeding: {"seeded", "seed"}, nsResetting: {"reset", "reset"}, nsRestoring: {"restored", "restore"},
	nsRemoving: {"removed", "removal"}, nsSweeping: {"collected", "sweep"},
}

// nsMeta is ns.json (08-data §2, §8.2 step 7).
type nsMeta struct {
	Schema         int              `json:"schema"`
	Scope          string           `json:"scope"`
	Deployment     string           `json:"deployment"`
	State          string           `json:"state,omitempty"` // empty | seeded | restored | partial ("" reads as empty; main's as original)
	Busy           string           `json:"busy,omitempty"`  // the act holding it, while it runs
	From           string           `json:"from,omitempty"`
	FromCheckpoint string           `json:"fromCheckpoint,omitempty"`
	At             string           `json:"at,omitempty"`
	By             string           `json:"by,omitempty"`
	Reset          bool             `json:"reset,omitempty"`
	Consistency    string           `json:"consistency,omitempty"` // online | stopped
	Resources      map[string]int64 `json:"resources,omitempty"`   // bytes per resource copied
	Skipped        []string         `json:"skipped,omitempty"`
	Failed         string           `json:"failed,omitempty"` // partial: the act (seed, reset, restore), its step, why
	Step           string           `json:"step,omitempty"`
	Error          string           `json:"error,omitempty"`
	Orphaned       string           `json:"orphaned,omitempty"` // since when no tile claims it
	History        []nsEvent        `json:"history,omitempty"`
}

// nsEvent is a history entry: seed, reset, restore, partial, orphaned, claimed.
type nsEvent struct {
	Op    string `json:"op"`
	At    string `json:"at"`
	By    string `json:"by,omitempty"`
	Error string `json:"error,omitempty"`
}

// nsID names a data namespace: a scope and a deployment name ("main").
type nsID struct{ scope, dep string }

func nsOf(scope, dep string) nsID { return nsID{scope, cmp.Or(dep, util.MainDeployment)} }

func (id nsID) main() bool { return id.dep == util.MainDeployment }

// keys are id's keys beyond main (main keeps today's, per resource).
func (id nsID) keys() (nsKeys, error) {
	if id.main() {
		return nsKeys{}, errors.New("main's data keeps today's keys")
	}
	return scopeKeys(id.scope, id.dep)
}

func nowStamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ---- in memory: holds and write gates ----

// nsTable is a workspace's holds and write gates, kept by its root: they
// belong to its data, and the Broker struct stays as it is.
type nsTable struct {
	mu    sync.Mutex
	held  map[nsID]string // the act holding it
	gates map[nsID]*sync.RWMutex
}

var nsTables sync.Map // workspace root → *nsTable

func (b *Broker) nsTab() *nsTable {
	if t, ok := nsTables.Load(b.Reg.Root); ok {
		return t.(*nsTable)
	}
	t, _ := nsTables.LoadOrStore(b.Reg.Root, &nsTable{held: map[nsID]string{}, gates: map[nsID]*sync.RWMutex{}})
	return t.(*nsTable)
}

// gate is id's write gate (08-data §8.2): API writes hold it shared, an act
// reading or erasing the namespace exclusively.
func (t *nsTable) gate(id nsID) *sync.RWMutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gates[id] == nil {
		t.gates[id] = &sync.RWMutex{}
	}
	return t.gates[id]
}

// holdNS holds id for act, for the act's whole run: requests answer 503
// (nsAvailable), backends don't start (nsStartBlocked), other acts 409.
func (b *Broker) holdNS(id nsID, act string) (release func(), err error) {
	t := b.nsTab()
	t.mu.Lock()
	defer t.mu.Unlock()
	if other := t.held[id]; other != "" {
		return nil, nsBusy(id.dep, other)
	}
	t.held[id] = act
	return func() { t.mu.Lock(); delete(t.held, id); t.mu.Unlock() }, nil
}

// busyAct is the act holding id; the sweep's brief hold keeps nothing off.
func (b *Broker) busyAct(id nsID) string {
	t := b.nsTab()
	t.mu.Lock()
	defer t.mu.Unlock()
	if act := t.held[id]; act != nsSweeping {
		return act
	}
	return ""
}

// nsErr is an answer of the deployments family (11-contract §1.14).
func nsErr(status int, kind, msg string) error {
	return &deployments.Error{Status: status, Kind: kind, Msg: msg}
}

// nsBusy is the 409 of an act on a held namespace.
func nsBusy(dep, act string) error {
	return nsErr(http.StatusConflict, deployments.KindState, fmt.Sprintf("%s's data is being %s", dep, cmp.Or(nsActs[act].done, act)))
}

// markBusy records act in id's ns.json while it runs (the caller holds
// id), so the sweep turns a crash into partial (08-data §8.5); unmark clears
// it unless the commit did. Without create, no directory is made.
func (b *Broker) markBusy(id nsID, act string, create bool) (unmark func()) {
	if err := b.updateNS(id, create, func(m *nsMeta) { m.Busy = act }); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("namespace metadata", "scope", id.scope, "deployment", id.dep, "err", err)
		}
		return func() {}
	}
	return func() {
		if m, ok, _ := b.readNS(id); ok && m.Busy == act {
			_ = b.updateNS(id, false, func(m *nsMeta) { m.Busy = "" })
		}
	}
}

// ---- the data plane's seams ----

// nsAvailable answers whether a data-plane request may reach scope's
// namespace in dep; while held, it writes 503 with Retry-After (§8.2).
func (b *Broker) nsAvailable(w http.ResponseWriter, scope, dep string) bool {
	id := nsOf(scope, dep)
	act := b.busyAct(id)
	if act == "" {
		return true
	}
	w.Header().Set("Retry-After", nsRetryAfter)
	server.WriteError(w, http.StatusServiceUnavailable, nsBusy(id.dep, act).Error(), "/docs/protocol.md")
	return false
}

// nsWriting holds the write gate shared for one API write (kv, blob);
// past nsWriteWait it writes 503 with Retry-After and answers false.
func (b *Broker) nsWriting(w http.ResponseWriter, scope, dep string) (release func(), ok bool) {
	g := b.nsTab().gate(nsOf(scope, dep))
	for deadline := time.Now().Add(nsWriteWait); !g.TryRLock(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			w.Header().Set("Retry-After", nsRetryAfter)
			server.WriteError(w, http.StatusServiceUnavailable,
				cmp.Or(dep, util.MainDeployment)+"'s data is being copied; retry shortly", "/docs/protocol.md")
			return nil, false
		}
	}
	return g.RUnlock, true
}

// nsStartBlocked is why a backend addressing scope's namespace in dep may
// not start: held, or left partial by a failed act (08-data §8.5).
func (b *Broker) nsStartBlocked(scope, dep string) error {
	id := nsOf(scope, dep)
	if act := b.busyAct(id); act != "" {
		return nsBusy(id.dep, act)
	}
	switch m, ok, err := b.readNS(id); {
	case err != nil:
		return err
	case ok && m.State == nsPartial:
		return fmt.Errorf("%s of %s for %s failed at %s: reset or seed again",
			cmp.Or(m.Failed, "seed"), scope, id.dep, cmp.Or(m.Step, "an unknown step"))
	}
	return nil
}

// ---- ns.json ----

// nsDir holds id's metadata: the namespace root; main's is metadata-only.
func (b *Broker) nsDir(id nsID) (string, error) {
	dep := id.dep
	if id.main() {
		dep = "m" // main's scope is checked as any other's
	}
	k, err := scopeKeys(id.scope, dep)
	if err != nil {
		return "", err
	}
	ns := k.NS
	if id.main() {
		ns = path.Dir(ns) + "/" + util.MainDeployment
	}
	return filepath.Join(b.Reg.Root, "data", "resources-enc", filepath.FromSlash(ns)), nil
}

// readNS reads id's ns.json; ok false without one.
func (b *Broker) readNS(id nsID) (m nsMeta, ok bool, err error) {
	dir, err := b.nsDir(id)
	if err != nil {
		return nsMeta{}, false, nil // no namespace can exist there
	}
	data, err := os.ReadFile(filepath.Join(dir, nsMetaFile)) // walk-ok: data/ is xbind's own; no sandbox sees it
	if errors.Is(err, fs.ErrNotExist) {
		return nsMeta{}, false, nil
	} else if err == nil {
		err = json.Unmarshal(data, &m)
	}
	if err != nil {
		return nsMeta{}, false, fmt.Errorf("%s/%s: %w", dir, nsMetaFile, err)
	}
	return m, true, nil
}

// updateNS applies fn to id's metadata and writes it atomically; the caller
// holds id. Without create a missing directory is fs.ErrNotExist. Metadata
// that doesn't parse is replaced, so a reset always recovers.
func (b *Broker) updateNS(id nsID, create bool, fn func(*nsMeta)) error {
	dir, err := b.nsDir(id)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err != nil && !(create && errors.Is(err, fs.ErrNotExist)) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { // nothing to do when it exists
		return err
	}
	m, _, err := b.readNS(id)
	if err != nil {
		slog.Warn("namespace metadata unreadable, rewritten", "scope", id.scope, "deployment", id.dep, "err", err)
		m = nsMeta{State: nsPartial, Error: err.Error()}
	}
	fn(&m)
	m.Schema, m.Scope, m.Deployment = 1, id.scope, id.dep
	if n := len(m.History); n > nsHistoryMax {
		m.History = m.History[n-nsHistoryMax:]
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, nsMetaFile), append(data, '\n'), 0o600)
}

// ---- claimants and the state ----

// claimants are scope's member tiles that have deployment dep (§6.4).
func (b *Broker) claimants(scope, dep string) []string {
	var out []string
	for _, c := range b.Reg.Components() {
		if c.Scope == scope && b.claims(c.Path, cmp.Or(dep, util.MainDeployment)) {
			out = append(out, c.Path)
		}
	}
	sort.Strings(out)
	return out
}

func (b *Broker) claims(tile, dep string) bool {
	_, names := b.deploymentsOf(tile)
	return slices.Contains(names, dep)
}

// DeploymentData is Deployment.data (11-contract §1.1) of dep of tile, from
// ns.json and the holds; nil for an unknown tile.
func (b *Broker) DeploymentData(tile, dep string) *deployments.DataState {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return nil
	}
	id := nsOf(c.Scope, dep)
	st := &deployments.DataState{State: map[bool]string{false: nsEmpty, true: nsOriginal}[id.main()]}
	switch m, ok, err := b.readNS(id); {
	case err != nil:
		st.State = nsPartial
	case ok:
		st.State = cmp.Or(m.State, st.State)
		st.From, st.At, st.By, st.Reset = m.From, m.At, m.By, m.Reset
	}
	if act := b.busyAct(id); act == nsSeeding || act == nsResetting || act == nsRestoring {
		st.Busy = act
	}
	return st
}

// JoinDeploymentData answers the namespace a new deployment dep of tile
// joins, for add and its dry run (08-data §6.2; 11-contract §1.5): nil when
// none exists. Joining one seeded, restored or partial needs manager, the
// plane's gate for the actor. A scope too long for namespaces answers 409.
func (b *Broker) JoinDeploymentData(tile, dep string, manager bool) (*deployments.Joins, error) {
	c, ok := b.Reg.Component(tile)
	if !ok || cmp.Or(dep, util.MainDeployment) == util.MainDeployment || c.Scope == "" {
		return nil, nil // main is never joined; the workspace scope is reached as an edge (§4.2)
	}
	id := nsOf(c.Scope, dep)
	dir, err := b.nsDir(id)
	if err != nil {
		return nil, nsErr(http.StatusConflict, deployments.KindPolicy, tile+" can't have non-primary deployments: "+err.Error())
	}
	if _, err := os.Lstat(dir); err != nil {
		return nil, nil
	}
	if act := b.busyAct(id); act != "" {
		return nil, nsBusy(id.dep, act)
	}
	m, _, err := b.readNS(id)
	if err != nil {
		m = nsMeta{State: nsPartial}
	}
	j := &deployments.Joins{Scope: id.scope, State: cmp.Or(m.State, nsEmpty), By: m.By, At: m.At}
	if manager || j.State == nsEmpty {
		return j, nil
	}
	verb, ok := map[string]string{nsSeeded: "seeded", nsRestored: "restored"}[j.State]
	if !ok { // partial: named by the act that left it so
		verb = "partly " + cmp.Or(map[string]string{"reset": "reset", "restore": "restored"}[m.Failed], "seeded")
	}
	return nil, nsErr(http.StatusForbidden, deployments.KindAuthority, fmt.Sprintf("%s's %q data was %s by %s %s: joining it is a tile manager's act",
		id.scope, id.dep, verb, cmp.Or(m.By, "someone"), cmp.Or(m.At, "earlier")))
}

// judgeClaimant asks the plane's judgement on another claimant (D127t): a 403
// becomes §1.14's text naming the tile; other refusals pass as they are.
func judgeClaimant(op, level string, id nsID, tile string, authorize func(string) error) error {
	err := errors.New("no judgement")
	if authorize != nil {
		err = authorize(tile)
	}
	var de *deployments.Error
	if err == nil || errors.As(err, &de) && de.Status != http.StatusForbidden {
		return err
	}
	return nsErr(http.StatusForbidden, deployments.KindAuthority, fmt.Sprintf("%s of %s's %q data needs %s on %s too", op, id.scope, id.dep, level, tile))
}

// ---- reset ----

// ResetDeploymentData empties the namespace dep of tile claims (08-data
// §9.1; 11-contract §1.8), for the plane's reset, which judged tile;
// authorize judges every other claimant (D127t), none of which may serve it
// as its primary. It holds the namespace, stops each claimant's dep, and
// wipes it; ns.json becomes empty with reset. vault also removes the
// deployment's vault file. A dry run changes nothing. It answers the
// claimants.
func (b *Broker) ResetDeploymentData(tile, dep, by string, vault, dryRun bool,
	authorize func(tile string) error, stop func(tile, dep string)) ([]string, error) {
	dep = cmp.Or(dep, util.MainDeployment)
	c, ok := b.Reg.Component(tile)
	switch {
	case !ok:
		return nil, nsErr(http.StatusNotFound, "", "no such tile: "+tile)
	case !util.DeploymentNameOK(dep):
		return nil, nsErr(http.StatusBadRequest, "", `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters`)
	case !b.claims(tile, dep):
		return nil, nsErr(http.StatusNotFound, "", util.NoDeployment(tile, dep).Error())
	}
	id, claimants := nsOf(c.Scope, dep), []string{tile} // the workspace scope has no namespace beyond main: the vault alone resets
	switch {
	case c.Scope == "" && id.main():
		return nil, nsErr(http.StatusConflict, deployments.KindPolicy,
			"main of "+tile+" holds the workspace's resources, which have one namespace: a reset never erases res:workspace/*")
	case c.Scope != "":
		claimants = b.claimants(c.Scope, dep)
	}
	for _, t := range claimants {
		if b.primaryOf(t) == dep {
			return nil, nsErr(http.StatusConflict, deployments.KindState, dep+" is the primary of "+t)
		}
	}
	level := map[bool]string{false: "terminal-level access", true: "a tile manager"}[id.main()]
	for _, t := range claimants {
		if t != tile {
			if err := judgeClaimant("reset", level, id, t, authorize); err != nil {
				return nil, err
			}
		}
	}
	if act := b.busyAct(id); act != "" {
		return nil, nsBusy(dep, act)
	}
	if dryRun {
		return claimants, nil
	}
	release, err := b.holdNS(id, nsResetting)
	if err != nil {
		return nil, err
	}
	defer release()
	if c.Scope != "" {
		defer b.markBusy(id, nsResetting, id.main())()
	}
	for _, t := range claimants {
		if stop != nil {
			stop(t, dep) // every claimant's dep addresses the namespace
		}
	}
	if c.Scope != "" {
		if err := b.wipe(id); err != nil {
			return nil, err
		}
		now := nowStamp(time.Now())
		err := b.updateNS(id, id.main(), func(m *nsMeta) {
			*m = nsMeta{State: nsEmpty, Reset: true, At: now, By: by, History: append(m.History, nsEvent{Op: "reset", At: now, By: by})}
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) { // a namespace never made stays unmade
			return nil, err
		}
	}
	if vault {
		if err := b.dropVaultFile(tile, dep); err != nil {
			return nil, err
		}
	}
	slog.Info("deployment data reset", "tile", tile, "scope", c.Scope, "deployment", dep, "by", by, "claimants", claimants, "vault", vault)
	return claimants, nil
}

// dropVaultFile removes dep of tile's vault file: main's today's (PO-3).
func (b *Broker) dropVaultFile(tile, dep string) error {
	rel := "data/vault/" + util.CompKey(tile) + ".json"
	if f, err := deploymentFiles(tile, dep); err == nil { // an error for main alone: dep is checked
		rel = f.Vault
	}
	if err := os.Remove(filepath.Join(b.Reg.Root, filepath.FromSlash(rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// wipe empties id's data, keeping ns.json (reset; a seed's step 4), under
// its write gate; the caller holds id.
func (b *Broker) wipe(id nsID) error {
	g := b.nsTab().gate(id)
	g.Lock()
	defer g.Unlock()
	if id.main() {
		return b.wipeMain(id.scope)
	}
	return b.wipeNS(id, false)
}

// wipeNS removes id's data beyond main (08-data §9.1 steps 2–3): volumes
// unmounted and verified first, then the kv file closed and all but ns.json
// removed — or, whole, the root and emptied parents too. RemoveAll never
// follows a link; cipher directories and the kv file are xbind's own.
func (b *Broker) wipeNS(id nsID, whole bool) error {
	k, err := id.keys()
	if err != nil {
		return err
	}
	data := filepath.Join(b.Reg.Root, "data", "resources-enc", filepath.FromSlash(k.NS))
	mounts := filepath.Join(b.Reg.Root, ".xbin", "resenc", filepath.FromSlash(k.NS))
	if err := b.unmountUnder(mounts, k.DirKey); err != nil {
		return err
	}
	if b.kv != nil {
		if err := b.kv.closeNamespace(k.NS); err != nil {
			return err
		}
	}
	var errs []error
	if whole {
		errs = append(errs, os.RemoveAll(data))
	} else if ents, err := os.ReadDir(data); err == nil { // walk-ok: data/ is xbind's own
		for _, e := range ents {
			if e.Name() != nsMetaFile {
				errs = append(errs, os.RemoveAll(filepath.Join(data, e.Name())))
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	errs = append(errs, os.RemoveAll(mounts))
	for _, dir := range []string{data, mounts} { // <escS>, then .deployments, each only once empty
		for d := filepath.Dir(dir); whole && filepath.Base(d) != "resources-enc" && filepath.Base(d) != "resenc"; d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
	return errors.Join(errs...)
}

// wipeMain empties main's storage of scope (main not the primary, §9.1):
// the resources the scope declares, at today's keys, volumes verified
// unmounted first, kv buckets deleted from data/kv.db.
func (b *Broker) wipeMain(scope string) error {
	sm := b.Reg.Scopes()[scope]
	if sm == nil || b.resenc == nil {
		return nil
	}
	vols, buckets := []resKeys{}, []string{}
	for name, res := range sm.Resources {
		switch k, err := b.resKeys(resTarget{Scope: scope, Name: name}, util.MainDeployment); {
		case err != nil: // a refused name has no storage
		case fileBackedType(res.Type):
			vols = append(vols, k)
		case res.Type == "kv":
			buckets = append(buckets, k.Bucket)
		}
	}
	for _, k := range vols {
		if err := b.unmountUnder(b.resenc.MountDir(k.DirKey, k.Name), k.DirKey); err != nil {
			return err
		}
	}
	var errs []error
	for _, k := range vols {
		errs = append(errs, os.RemoveAll(b.resenc.CipherDir(k.DirKey, k.Name)), os.RemoveAll(b.resenc.MountDir(k.DirKey, k.Name)))
	}
	if b.kv != nil && len(buckets) > 0 {
		errs = append(errs, b.kv.db.Update(func(tx *bolt.Tx) error {
			for _, name := range buckets {
				if err := tx.DeleteBucket([]byte(name)); err != nil && !errors.Is(err, bolt.ErrBucketNotFound) {
					return err
				}
			}
			return nil
		}))
	}
	return errors.Join(errs...)
}

// nsMountPoints is mountPointsUnder; tests fake it.
var nsMountPoints = mountPointsUnder

// unmountUnder unmounts every volume at or under dir (resenc's, under
// dirKey); one still mounted is an error, and the caller removes nothing.
func (b *Broker) unmountUnder(dir, dirKey string) error {
	pts, err := nsMountPoints(dir)
	if err != nil || len(pts) == 0 {
		return err
	}
	base := filepath.Join(b.Reg.Root, ".xbin", "resenc", filepath.FromSlash(dirKey))
	for _, p := range pts {
		if name, err := filepath.Rel(base, p); err == nil && name != "." && !strings.HasPrefix(name, "..") && b.resenc != nil {
			if err := b.resenc.Unmount(dirKey, filepath.ToSlash(name)); err != nil {
				slog.Warn("resource encryption: unmount failed", "mount", p, "err", err)
			}
		}
	}
	if pts, err = nsMountPoints(dir); err == nil && len(pts) > 0 {
		err = fmt.Errorf("%s is still mounted, so nothing was removed: stop what uses it and try again", pts[0])
	}
	return err
}

// mountPointsUnder lists the mount points at or under dir: directories
// whose device differs from the parent's, or whose stat fails (a dead FUSE
// mount: ENOTCONN). It never follows a link or enters a mount, whose content
// alone is a sandbox's.
func mountPointsUnder(dir string) ([]string, error) {
	dev := func(fi fs.FileInfo) uint64 {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			return uint64(st.Dev) // an int32 on darwin
		}
		return 0
	}
	parent, err := os.Lstat(filepath.Dir(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []string
	var walk func(p string, up uint64) error
	walk = func(p string, up uint64) error {
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist) || err == nil && !fi.IsDir():
			return nil
		case err != nil || dev(fi) != up:
			out = append(out, p)
			return nil
		}
		ents, err := os.ReadDir(p) // walk-ok: an unmounted directory of .xbin/resenc, xbind's own
		for _, e := range ents {
			if err == nil && e.IsDir() { // the entry's own type: a symlink is never entered
				err = walk(filepath.Join(p, e.Name()), dev(fi))
			}
		}
		return err
	}
	return out, walk(dir, dev(parent))
}

// ---- removal and GC ----

// DropDeploymentData deletes the namespace dep of tile claims if tile is its
// last claimant (08-data §9.2 step 3; D127t), for the plane's removal once dep
// is stopped; never main's. It answers whether it was (dryRun: would be)
// deleted; the sweep finishes a removal cut short.
func (b *Broker) DropDeploymentData(tile, dep string, dryRun bool) (bool, error) {
	c, ok := b.Reg.Component(tile)
	if !ok || cmp.Or(dep, util.MainDeployment) == util.MainDeployment || c.Scope == "" {
		return false, nil
	}
	id := nsOf(c.Scope, dep)
	for _, t := range b.claimants(id.scope, id.dep) {
		if t != tile {
			return false, nil
		}
	}
	dir, err := b.nsDir(id)
	if err == nil {
		_, err = os.Lstat(dir)
	}
	if err != nil || dryRun {
		return err == nil, nil
	}
	release, err := b.holdNS(id, nsRemoving)
	if err != nil {
		return false, err
	}
	defer release()
	_ = b.markBusy(id, nsRemoving, false) // left on failure, for the sweep
	if err := b.wipeNS(id, true); err != nil {
		return false, err
	}
	slog.Info("deployment data deleted", "tile", tile, "scope", id.scope, "deployment", id.dep)
	return true, nil
}

// eachNamespace calls fn for each namespace directory on disk.
func (b *Broker) eachNamespace(fn func(id nsID)) {
	base := filepath.Join(b.Reg.Root, "data", "resources-enc", deploymentsLevel)
	scopes, err := os.ReadDir(base) // walk-ok: data/ is xbind's own
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("namespace sweep", "err", err)
	}
	for _, s := range scopes {
		if scope, ok := unescS(s.Name()); s.IsDir() && ok {
			deps, _ := os.ReadDir(filepath.Join(base, s.Name())) // walk-ok: data/ is xbind's own
			for _, d := range deps {
				if d.IsDir() && util.DeploymentNameOK(d.Name()) {
					fn(nsOf(scope, d.Name()))
				}
			}
		}
	}
}

// SweepNamespaces reconciles the namespaces on disk with their claimants
// (08-data §9.3; NP-08-14), at boot after the plane's answers are installed
// and after every structure change. A crashed act becomes partial (§8.5); an
// unclaimed namespace is unmounted and marked orphaned, and deleted after
// nsOrphanGrace unless a claimant reappears. main's is never collected;
// without the plane's answers nothing is orphaned.
func (b *Broker) SweepNamespaces() {
	now := time.Now()
	b.eachNamespace(func(id nsID) {
		if err := b.sweepOne(id, now); err != nil {
			slog.Warn("namespace sweep", "scope", id.scope, "deployment", id.dep, "err", err)
		}
	})
}

func (b *Broker) sweepOne(id nsID, now time.Time) error {
	release, err := b.holdNS(id, nsSweeping)
	if err != nil {
		return nil // an act holds it: nothing to reconcile now
	}
	defer release()
	m, _, err := b.readNS(id)
	if err != nil {
		return err
	}
	claimed, stamp := b.DeploymentsOf == nil || len(b.claimants(id.scope, id.dep)) > 0, nowStamp(now)
	note := func(op string, set func(m *nsMeta)) error {
		return b.updateNS(id, false, func(m *nsMeta) {
			set(m)
			if op != "" {
				m.History = append(m.History, nsEvent{Op: op, At: stamp, Error: m.Error})
			}
		})
	}
	switch {
	case m.Busy == nsRemoving && !claimed:
		return b.wipeNS(id, true)
	case m.Busy == nsRemoving:
		return note("", func(m *nsMeta) { m.Busy = "" })
	case m.Busy != "":
		act := cmp.Or(nsActs[m.Busy].act, m.Busy)
		slog.Warn("deployment data left partial", "scope", id.scope, "deployment", id.dep, "act", act)
		return note("partial", func(m *nsMeta) {
			m.State, m.Busy, m.Failed, m.Step, m.Error = nsPartial, "", act, act, "xbind stopped during the "+act
		})
	case id.main() || claimed && m.Orphaned == "":
		return nil
	case claimed:
		return note("claimed", func(m *nsMeta) { m.Orphaned = "" })
	case m.Orphaned == "":
		if k, err := id.keys(); err == nil {
			_ = b.unmountUnder(filepath.Join(b.Reg.Root, ".xbin", "resenc", filepath.FromSlash(k.NS)), k.DirKey)
		}
		slog.Warn("deployment data orphaned: no tile claims it", "scope", id.scope, "deployment", id.dep,
			"deletes", nowStamp(now.Add(nsOrphanGrace)))
		return note("orphaned", func(m *nsMeta) { m.Orphaned = stamp })
	}
	if since, err := time.Parse(time.RFC3339, m.Orphaned); err != nil || now.Sub(since) < nsOrphanGrace {
		return nil
	}
	slog.Info("deployment data deleted: orphaned past the grace period", "scope", id.scope, "deployment", id.dep, "since", m.Orphaned)
	return b.wipeNS(id, true)
}

// OrphanedNamespace is a namespace no tile claims, as admins see it.
type OrphanedNamespace struct {
	Scope      string `json:"scope"`
	Deployment string `json:"deployment"`
	State      string `json:"state"`
	Since      string `json:"since"`
	Deletes    string `json:"deletes"` // when GC deletes it
}

// OrphanedNamespaces lists the orphans, for admins (§9.3).
func (b *Broker) OrphanedNamespaces() []OrphanedNamespace {
	var out []OrphanedNamespace
	b.eachNamespace(func(id nsID) {
		if m, ok, _ := b.readNS(id); ok && m.Orphaned != "" {
			o := OrphanedNamespace{Scope: id.scope, Deployment: id.dep, State: cmp.Or(m.State, nsEmpty), Since: m.Orphaned}
			if t, err := time.Parse(time.RFC3339, m.Orphaned); err == nil {
				o.Deletes = nowStamp(t.Add(nsOrphanGrace))
			}
			out = append(out, o)
		}
	})
	return out
}

// DeleteOrphanedNamespace deletes an orphan at once, an admin's act the
// caller judges (§9.3): the sweep, as if its grace were over.
func (b *Broker) DeleteOrphanedNamespace(scope, dep string) error {
	id := nsOf(scope, dep)
	if m, ok, err := b.readNS(id); err != nil || !ok || m.Orphaned == "" {
		return cmp.Or(err, nsErr(http.StatusNotFound, "", fmt.Sprintf("%s's %q data isn't orphaned", scope, id.dep)))
	}
	slog.Info("deployment data: an admin deletes an orphan", "scope", scope, "deployment", id.dep)
	return b.sweepOne(id, time.Now().Add(nsOrphanGrace))
}
