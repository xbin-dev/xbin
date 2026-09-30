package broker

// partitionops.go — operating people's partitions (plans/partitions/06 §5,
// §6, §10; PD-24, PD-26, PD-46): stopping, resetting and purging one
// person's partition of a tile, and the runner's view of which partitions
// run.
//
//	POST /api/xbin/partitions/stop  {tile, partition}
//	POST /api/xbin/partitions/reset {tile, partition, confirm: "<tile> <partition>"}
//	POST /api/xbin/partitions/purge {tile?, partition?}
//
// stop is the person's own act on their partition, or a tile manager's or
// an admin's on anyone's: the instance stops (its token revoked first), its
// data stays, and a request starts it again. reset deletes one partition —
// the person's own, or anyone's for an admin (audited, and the person is
// told) — after a typed confirmation: its instance and terminals end, and
// its namespaces, vault, registrations, records, log, terminal layers and
// history are deleted and its backup subkey erased (11 §3). purge is the
// admins': it deletes orphaned partitions (their person deleted, their tile
// removed) now instead of after the retention; a live person's partition
// is never purged (PD-26 — no partition is ever dormant by mode).
//
// Acting is a person's act — their own session, app or device, the root
// token, or the admin tile's frame driven by one (AdminFrameDriver) — never
// a tile's backend, frame, terminal or agent. A partition is named by its
// wire key (user:<id>, the person's current incarnation) or, for purge, by
// its partition id (u-<32 hex>), which also names an orphan whose person's
// id is someone else's now.

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// PartitionInstance is one running (or starting, or stopping) instance of a
// person's partition as the runner reports it: metadata, never content.
type PartitionInstance struct {
	Tile, Dep string
	Partition string // user:<id>
	State     string
	Gen       int
	UptimeSec int64
	RSSKB     int64
	Restarts  int
	Error     string
}

// partitionOps is the runner's and the identity plane's side of the
// operations, as boot installs it (SetPartitionOps); kept beside the Broker.
type partitionOps struct {
	mu sync.RWMutex
	// instances lists people's partition instances (runner.InspectPartitions).
	instances func() []PartitionInstance
	// stopOf stops every partition instance of a person (runner.StopPartitionsOf),
	// tokens revoked first; revokeUser drops their instance tokens
	// (auth.RevokeUserPartitionInstances).
	stopOf     func(userID string)
	revokeUser func(userID string) int
	// liveReload: do saves reach tile's running code (SetPartitionLiveReload).
	liveReload func(tile string) bool
}

var partitionOpsOf sync.Map // *Broker → *partitionOps

func (b *Broker) partOps() *partitionOps {
	v, _ := partitionOpsOf.LoadOrStore(b, &partitionOps{})
	return v.(*partitionOps)
}

// SetPartitionOps installs the runner's instance listing and the stops and
// revocations of a person's partitions (boot: wirePartitionOps).
func (b *Broker) SetPartitionOps(instances func() []PartitionInstance, stopOf func(userID string), revokeUser func(userID string) int) {
	o := b.partOps()
	o.mu.Lock()
	o.instances, o.stopOf, o.revokeUser = instances, stopOf, revokeUser
	o.mu.Unlock()
}

// PartitionOpsWired reports whether boot installed SetPartitionOps (its
// wiring test's guard).
func (b *Broker) PartitionOpsWired() bool {
	o := b.partOps()
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.instances != nil && o.stopOf != nil && o.revokeUser != nil
}

// partitionInstances is the runner's rows (none without it).
func (b *Broker) partitionInstances() []PartitionInstance {
	o := b.partOps()
	o.mu.RLock()
	f := o.instances
	o.mu.RUnlock()
	if f == nil {
		return nil
	}
	return f()
}

// stopPartitionsOfPerson stops every partition instance of userID and
// revokes their tokens (the runner's stop revokes first; the revocation
// here also covers a token no running state holds).
func (b *Broker) stopPartitionsOfPerson(userID string) {
	o := b.partOps()
	o.mu.RLock()
	stop, revoke := o.stopOf, o.revokeUser
	o.mu.RUnlock()
	if revoke != nil {
		revoke(userID)
	}
	if stop != nil {
		stop(userID)
	}
}

// ---- drop hooks: the planes that keep a partition's data beside these ----

// partitionDropHook is one plane's part of deleting one user partition
// whole (a reset, a purge): a plane registered after this pack (F6's mail,
// F17b's archives) deletes its own store of (tile, dep, pkey) here. The
// partition's instance and terminals are stopped, and the tile's backups
// held, when it runs.
type partitionDropHook struct {
	name string
	drop func(b *Broker, tile, dep, pkey string) error
}

var partitionDropHooks []partitionDropHook

func registerPartitionDropHook(h partitionDropHook) {
	partitionDropHooks = append(partitionDropHooks, h)
}

// partitionLogDir is a user partition's log directory,
// .xbin/partition/<TileKey>/<dep>/<pkey> (the runner writes backend.log in it).
func (b *Broker) partitionLogDir(tile, dep, pkey string) (string, error) {
	dep = cmp.Or(dep, util.MainDeployment)
	if tile == "" || !util.DeploymentNameOK(dep) || !util.PartitionKeyOK(pkey) {
		return "", fmt.Errorf("%s: no partition log for %q, %q", tile, dep, pkey)
	}
	return filepath.Join(b.Reg.Root, ".xbin", "partition", util.TileKey(tile), dep, pkey), nil
}

// partitionDrops counts the holds on partitions being deleted (workspace
// root, tile, pkey): no instance of one starts meanwhile
// (ShouldRunPartition asks).
var (
	partitionDropsMu sync.Mutex
	partitionDrops   = map[string]int{}
)

func (b *Broker) dropKey(tile, pkey string) string { return b.Reg.Root + "\x00" + tile + "\x00" + pkey }

// holdPartitionDrop keeps partition pkey of tile from starting until
// release: a reset holds it from before its stop until its data is gone.
func (b *Broker) holdPartitionDrop(tile, pkey string) (release func()) {
	k := b.dropKey(tile, pkey)
	partitionDropsMu.Lock()
	partitionDrops[k]++
	partitionDropsMu.Unlock()
	return func() {
		partitionDropsMu.Lock()
		defer partitionDropsMu.Unlock()
		if partitionDrops[k]--; partitionDrops[k] <= 0 {
			delete(partitionDrops, k)
		}
	}
}

// partitionDropping reports a partition held by holdPartitionDrop.
func (b *Broker) partitionDropping(tile, pkey string) bool {
	partitionDropsMu.Lock()
	defer partitionDropsMu.Unlock()
	return partitionDrops[b.dropKey(tile, pkey)] > 0
}

// dropSummary is what deleting one partition removed.
type dropSummary struct {
	Namespaces int   `json:"namespaces"`
	Layers     int   `json:"layers"`
	Histories  int   `json:"histories"`
	Subkeys    int   `json:"subkeys"`
	Bytes      int64 `json:"bytes"`
}

// dropOnePartition deletes user partition pkey of deployment dep of tile
// whole: its person's sessions end and their layers and history go
// (wipePersonTerminalsOf, which holds the partition throughout), then —
// under the tile's backup lock — its namespaces (when tile roots its scope,
// which owns them), registrations, record, vault, log and the other planes'
// stores (partitionDropHooks), and last its backup subkey is erased, so no
// backup seals the deleted data under a fresh key in between (11 §3). The
// caller stopped its instance first.
func (b *Broker) dropOnePartition(tile, dep, pkey, reason, by string) (dropSummary, error) {
	var sum dropSummary
	dep = cmp.Or(dep, util.MainDeployment)
	tw, err := b.wipePersonTerminalsOf(tile, pkey, false)
	sum.Layers, sum.Histories = tw.Layers, tw.Histories
	if err != nil {
		return sum, fmt.Errorf("ending the partition's terminals: %w", err)
	}
	scope := tile
	if c, ok := b.Reg.Component(tile); ok && c.Scope != "" {
		scope = c.Scope
	}
	defer b.holdBackups(scope)()
	if scope != tile {
		defer b.holdBackups(tile)()
	}
	var errs []error
	if scope == tile {
		var ids []nsID
		_ = b.eachPartitionNamespace(scope, func(id nsID) {
			if id.pkey == pkey {
				ids = append(ids, id)
			}
		})
		for _, id := range ids {
			if dir, err := b.nsDir(id); err == nil {
				n, _ := treeUsage(dir)
				sum.Bytes += n
			}
			if err := b.dropPartitionNS(id); err != nil {
				errs = append(errs, err)
				continue
			}
			sum.Namespaces++
		}
	}
	errs = append(errs, b.DropPartition(tile, dep, pkey))
	if dir, err := b.partitionLogDir(tile, dep, pkey); err == nil {
		errs = append(errs, os.RemoveAll(dir)) // xbind's own: the runner writes it
		removeEmptyDirs(filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)))
	}
	for _, h := range partitionDropHooks {
		if err := h.drop(b, tile, dep, pkey); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", h.name, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return sum, err // the subkey stays until what it seals is gone: a retry erases it
	}
	subjects := []string{partitionBackupSubject(tile, dep, pkey)}
	if scope != tile {
		subjects = append(subjects, partitionBackupSubject(scope, dep, pkey))
	}
	for _, owner := range slices.Compact([]string{scope, tile}) {
		erased, _, err := b.eraseBackupSubjectsHeld(owner, func(s string) bool { return slices.Contains(subjects, s) }, reason, by)
		sum.Subkeys += len(erased)
		if err != nil {
			slog.Warn("partitions: a partition's backup key erase", "tile", owner, "partition", pkey, "err", err)
		}
	}
	return sum, nil
}

func init() {
	registerWipeHook(wipeHook{name: "partition-logs", wipe: wipePartitionLogs})
}

// errPartitionedOffload refuses offloading a partitioned tile (06 §10,
// C16): v1 archives and restores no person's partition through offload.
type errPartitionedOffload struct{ tile, why string }

func (e *errPartitionedOffload) Error() string {
	return "can't offload " + e.tile + ": " + e.why + " — offloading a partitioned tile isn't supported yet (docs/partitions.md)"
}

// partitionOffloadCheck is offload's refusal for a tile whose recorded mode
// has people's partitions (or can't be read): nothing archived or stopped.
func (b *Broker) partitionOffloadCheck(tile string) error {
	if _, on, err := b.tilePartitioning(tile); err != nil {
		return &errPartitionedOffload{tile: tile, why: "its partition mode record can't be read"}
	} else if on {
		return &errPartitionedOffload{tile: tile, why: "it keeps each person's data in their own partition"}
	}
	return nil
}

// wipePartitionLogs is a switch's part for people's partition logs
// (.xbin/partition/<TileKey>/): a switch that deletes everything removes
// them all; adding or removing "global" keeps them (H1), as it keeps
// people's partitions. Logs aren't "holds data" (01 §2.2): nothing to
// count. The partition instances are stopped when it runs.
func wipePartitionLogs(b *Broker, t wipeTarget, _ *wipeSummary) error {
	if t.Kind != wipeEverything || t.DryRun || t.Tile == "" {
		return nil
	}
	dir := filepath.Join(b.Reg.Root, ".xbin", "partition", util.TileKey(t.Tile))
	if err := os.RemoveAll(dir); err != nil { // xbind's own: the runner writes it
		return err
	}
	removeEmptyDirs(filepath.Dir(dir))
	return nil
}

// ---- the routes ----

func (b *Broker) registerPartitionOps(srv *server.Server) {
	srv.RegisterAPI("GET /partitions", b.apiPartitionsList) // partitionlist.go
	srv.RegisterAPI("POST /partitions/stop", b.apiPartitionStop)
	srv.RegisterAPI("POST /partitions/reset", b.apiPartitionReset)
	srv.RegisterAPI("POST /partitions/purge", b.apiPartitionPurge)
	srv.RegisterAPI("POST /partitions/share-log", b.apiPartitionShareLog)          // partitionlogshare.go
	srv.RegisterAPI("DELETE /partitions/share-log", b.apiPartitionShareLog)        // partitionlogshare.go
	srv.RegisterAPI("POST /partitions/credential-confirm", b.apiCredentialConfirm) // partitioncreds.go
}

// partitionActor is the person acting on a partitions route: a person's
// own session, app or device, the root token, or the admin tile's frame
// under its driver's login (AdminFrameDriver). Every other credential is
// refused (answered here).
func (b *Broker) partitionActor(w http.ResponseWriter, p auth.Principal) (auth.Principal, bool) {
	if p.Component == "" && p.Impersonator == "" && (p.Owner || p.UserID != "") {
		return p, true
	}
	if d, ok := b.AdminFrameDriver(p); ok {
		return d, true
	}
	server.WriteError(w, http.StatusForbidden, "operating a partition is a person's act, in their own session (bx, the shell, the admin console); "+
		"no tile's backend, frame, terminal or agent does it — and viewing as someone doesn't", modeDocs)
	return p, false
}

// partitionOpBody is the stop/reset/purge body.
type partitionOpBody struct {
	Tile      string `json:"tile"`
	Partition string `json:"partition"`
	Confirm   string `json:"confirm"`
}

// livePartitionOf resolves partition key part ("user:<id>") of tile to its
// person and current partition id; the error says why not (404).
func (b *Broker) livePartitionOf(tile, part string) (user, pkey string, err error) {
	pt, err := util.ParsePartition(part)
	id, ok := pt.User()
	if err != nil || !ok {
		return "", "", fmt.Errorf("partition is user:<id> (the global instance is stopped like any backend)")
	}
	uid := b.storedPartitionUID(id)
	if uid == "" {
		return "", "", fmt.Errorf("%s has no partition of %s", id, tile)
	}
	return id, util.PartitionKey(id, uid), nil
}

// partitionedTile answers the registered tile whose recorded mode has user
// partitions, or the status and text of why not.
func (b *Broker) partitionedTile(tile string) (int, string) {
	if _, ok := b.Reg.Component(tile); !ok {
		return http.StatusNotFound, "no such tile: " + tile
	}
	if _, on, err := b.tilePartitioning(tile); err != nil {
		return http.StatusConflict, err.Error()
	} else if !on {
		return http.StatusConflict, tile + " isn't partitioned: it has no people's partitions"
	}
	return 0, ""
}

// apiPartitionStop — the person's own partition, or anyone's for a tile
// manager or an admin. Data stays; the next request starts it.
func (b *Broker) apiPartitionStop(w http.ResponseWriter, r *http.Request) {
	person, ok := b.partitionActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	var body partitionOpBody
	if err := server.DecodeJSON(r, &body); err != nil || body.Tile == "" || body.Partition == "" {
		server.WriteError(w, http.StatusBadRequest, "need {tile, partition}", "/docs/protocol.md")
		return
	}
	tile := strings.Trim(body.Tile, "/")
	if status, msg := b.partitionedTile(tile); status != 0 {
		server.WriteError(w, status, msg, modeDocs)
		return
	}
	user, _, err := b.livePartitionOf(tile, body.Partition)
	if err != nil {
		server.WriteError(w, http.StatusNotFound, err.Error(), modeDocs)
		return
	}
	if user != person.UserID && !b.IsAdmin(person) && !b.mayManageTile(person, tile) {
		server.WriteError(w, http.StatusForbidden, "stopping someone else's partition is a tile manager's or an admin's act", modeDocs)
		return
	}
	dep := cmp.Or(b.primaryOf(tile), util.MainDeployment)
	b.stopPartitionInstance(tile, dep, string(util.UserPartition(user)))
	slog.Info("audit", "who", person.From(), "method", "POST", "path", "/partitions/stop", "status", http.StatusOK,
		"tile", tile, "partition", "user:"+user)
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tile": tile, "partition": "user:" + user})
}

// apiPartitionReset — delete one partition: the person's own, or anyone's
// for an admin (audited; the person is told). confirm must be "<tile>
// <partition>".
func (b *Broker) apiPartitionReset(w http.ResponseWriter, r *http.Request) {
	person, ok := b.partitionActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	var body partitionOpBody
	if err := server.DecodeJSON(r, &body); err != nil || body.Tile == "" || body.Partition == "" {
		server.WriteError(w, http.StatusBadRequest, "need {tile, partition, confirm: \"<tile> <partition>\"}", "/docs/protocol.md")
		return
	}
	tile := strings.Trim(body.Tile, "/")
	if status, msg := b.partitionedTile(tile); status != 0 {
		server.WriteError(w, status, msg, modeDocs)
		return
	}
	user, pkey, err := b.livePartitionOf(tile, body.Partition)
	if err != nil {
		server.WriteError(w, http.StatusNotFound, err.Error(), modeDocs)
		return
	}
	own := user == person.UserID
	if !own && !b.IsAdmin(person) {
		server.WriteError(w, http.StatusForbidden, "resetting someone else's partition is an admin's act", modeDocs)
		return
	}
	part := "user:" + user
	if want := tile + " " + part; strings.TrimSpace(body.Confirm) != want {
		server.WriteJSON(w, http.StatusConflict, map[string]any{"error": "a reset deletes every piece of this partition's data; confirm with the text " + strconvQuote(want),
			"confirm": want, "docs": modeDocs})
		return
	}
	if b.PartitionHoldReason(tile) != "" {
		server.WriteError(w, http.StatusConflict, tile+" is paused (a partition mode request waits for a manager): decide it first", modeDocs)
		return
	}
	dep := cmp.Or(b.primaryOf(tile), util.MainDeployment)
	defer b.holdPartitionDrop(tile, pkey)() // nothing starts it between the stop and the drop
	b.stopPartitionInstance(tile, dep, part)
	by := deciderName(person)
	sum, err := b.dropOnePartition(tile, dep, pkey, "partition reset: "+tile+" "+part, by)
	slog.Info("audit", "who", person.From(), "method", "POST", "path", "/partitions/reset", "status", statusOf(err),
		"tile", tile, "partition", part, "own", own, "namespaces", sum.Namespaces, "subkeys", sum.Subkeys)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, "the reset stopped part-way: "+hostless(err).Error()+" — what went is gone; try again", modeDocs)
		return
	}
	if !own {
		when := time.Now().UTC().Format("2006-01-02 15:04 UTC")
		text := fmt.Sprintf("your data in %s was deleted by %s at %s (a partition reset)", tile, by, when)
		b.notePartitionNotice(user, tile, "partition-reset", text)
		b.pushPerson(user, "tile.partition-reset", tile+": your data was reset", "Deleted by "+by+" at "+when+".", "xbin/partitions", "")
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tile": tile, "partition": part, "deleted": sum})
}

// statusOf is 200, or 500 for an error (the audit line's status).
func statusOf(err error) int {
	if err != nil {
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

// strconvQuote quotes s as the refusals show a typed confirmation.
func strconvQuote(s string) string { return `"` + s + `"` }

// orphanRow is one orphaned partition purge may delete.
type orphanRow struct {
	Tile      string `json:"tile"`
	Dep       string `json:"deployment"`
	Partition string `json:"partition"` // its partition id (u-…)
	User      string `json:"user"`      // whose it was
	Reason    string `json:"reason"`    // user-deleted | tile-removed
	Since     string `json:"since"`
}

// partitionOrphans lists the orphaned partitions (of tile, or all): their
// record says so, or their namespace's ns.json does.
func (b *Broker) partitionOrphans(tile string) []orphanRow {
	seen := map[string]bool{}
	out := []orphanRow{}
	_ = b.eachPartitionRecord(tile, func(d partitionDirOf, rec partitionRecord) {
		if rec.State == partStateOrphaned {
			seen[rec.Tile+"\x00"+d.dep+"\x00"+d.pkey] = true
			out = append(out, orphanRow{Tile: rec.Tile, Dep: d.dep, Partition: d.pkey, User: rec.User, Reason: rec.Reason, Since: rec.Orphaned})
		}
	})
	_ = b.eachPartitionNamespace("", func(id nsID) {
		if tile != "" && id.scope != tile || seen[id.scope+"\x00"+id.dep+"\x00"+id.pkey] {
			return
		}
		m, ok, _ := b.readNS(id)
		if !ok || m.Partition == nil || m.Partition.Orphan == "" {
			return
		}
		seen[id.scope+"\x00"+id.dep+"\x00"+id.pkey] = true
		out = append(out, orphanRow{Tile: id.scope, Dep: id.dep, Partition: id.pkey, User: m.Partition.User, Reason: m.Partition.Orphan, Since: m.Orphaned})
	})
	slices.SortFunc(out, func(x, y orphanRow) int {
		return cmp.Or(cmp.Compare(x.Tile, y.Tile), cmp.Compare(x.User, y.User), cmp.Compare(x.Partition, y.Partition))
	})
	return out
}

// apiPartitionPurge — admins delete orphaned partitions now: of a tile, one
// of them (by its partition id or its former person's key), or all.
func (b *Broker) apiPartitionPurge(w http.ResponseWriter, r *http.Request) {
	person, ok := b.partitionActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	if !b.IsAdmin(person) {
		server.WriteError(w, http.StatusForbidden, "purging orphaned partitions is an admin's act", modeDocs)
		return
	}
	var body partitionOpBody
	if r.ContentLength != 0 {
		if err := server.DecodeJSON(r, &body); err != nil {
			server.WriteError(w, http.StatusBadRequest, "need {tile?, partition?}", "/docs/protocol.md")
			return
		}
	}
	tile := strings.Trim(body.Tile, "/")
	var picked []orphanRow
	for _, o := range b.partitionOrphans(tile) {
		if body.Partition == "" || body.Partition == o.Partition || body.Partition == "user:"+o.User {
			picked = append(picked, o)
		}
	}
	if body.Partition != "" && len(picked) == 0 {
		if _, _, err := b.livePartitionOf(tile, body.Partition); err == nil {
			server.WriteError(w, http.StatusConflict, "only orphaned partitions are purged — a person's live partition is reset instead (POST /partitions/reset)", modeDocs)
			return
		}
		server.WriteError(w, http.StatusNotFound, "no orphaned partition "+body.Partition, modeDocs)
		return
	}
	type purged struct {
		orphanRow
		Deleted dropSummary `json:"deleted"`
		Error   string      `json:"error,omitempty"`
	}
	out := []purged{}
	by := deciderName(person)
	for _, o := range picked {
		if c, ok := b.Reg.Component(o.Tile); ok {
			if st, _, _ := c.PartitionState(); st.Held() {
				out = append(out, purged{orphanRow: o, Error: o.Tile + " is paused: decide its mode request first"})
				continue
			}
		}
		sum, err := b.dropOnePartition(o.Tile, o.Dep, o.Partition, "partition purged: "+o.Reason, by)
		row := purged{orphanRow: o, Deleted: sum}
		if err != nil {
			row.Error = hostless(err).Error()
		}
		out = append(out, row)
		b.removedTileModeRecord(o.Tile) // a removed tile's last partition gone: its mode record goes too
	}
	slog.Info("audit", "who", person.From(), "method", "POST", "path", "/partitions/purge", "status", http.StatusOK,
		"tile", tile, "partition", body.Partition, "purged", len(out))
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "purged": out})
}

// PartitionMeta is the metadata admins see of person part's partition of
// tile beside its runner row (GET /backends, 06 §5; PD-24): when it last
// started, its last unrequested exit, how many there were, whether the crash
// breaker holds it. ok false: no record.
func (b *Broker) PartitionMeta(tile, part string) (m map[string]any, ok bool) {
	id, isUser := util.Partition(part).User()
	uid := b.storedPartitionUID(id)
	if !isUser || uid == "" {
		return nil, false
	}
	dir, err := b.partitionRecordDir(tile, b.primaryOf(tile), util.PartitionKey(id, uid))
	if err != nil {
		return nil, false
	}
	rec, found, err := readPartitionRecordAt(dir)
	if err != nil || !found {
		return nil, false
	}
	m = map[string]any{"restarts": rec.Restarts, "crashLoop": rec.CrashLoop}
	if rec.LastStarted != "" {
		m["lastStarted"] = rec.LastStarted
	}
	if rec.LastExit != "" {
		m["lastExit"] = rec.LastExit
	}
	return m, true
}

// PartitionDisk is what person part's partition of tile holds, and the
// per-partition ceiling (0: none set) — its tile-status disk (06 §5).
func (b *Broker) PartitionDisk(tile string, part util.Partition) (usage, quota int64) {
	id, ok := part.User()
	if uid := b.storedPartitionUID(id); ok && uid != "" {
		usage = b.partitionBytes(tile, util.PartitionKey(id, uid))
	}
	return usage, b.PartitionBytes(tile)
}
