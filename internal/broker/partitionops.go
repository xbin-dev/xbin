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
	Tile      string `json:"tile"`
	Dep       string `json:"deployment"`
	Partition string `json:"partition"` // user:<id>
	State     string `json:"state"`
	Gen       int    `json:"gen"`
	UptimeSec int64  `json:"uptimeSec"`
	RSSKB     int64  `json:"rssKb"`
	Restarts  int    `json:"restarts"`
	// Error is the runner's text, which the person's code may have written:
	// only its person reads it (instanceView); others get its class.
	Error string `json:"-"`
}

// instanceView is a partition instance on the wire: the error's text for
// the person whose partition it is, its class (PartitionErrorClass) for
// everyone.
type instanceView struct {
	PartitionInstance
	ErrorText  string `json:"error,omitempty"`
	ErrorClass string `json:"errorClass,omitempty"`
}

func (in PartitionInstance) view(own bool) *instanceView {
	v := &instanceView{PartitionInstance: in}
	if in.Error != "" {
		v.ErrorClass = PartitionErrorClass(in.Error)
		if own {
			v.ErrorText = in.Error
		}
	}
	return v
}

// PartitionErrorClass is a runner error's class, never its text (which a
// person's code may have written): crash-loop, build, start, exit or other
// (GET /backends' and GET /partitions' errorClass).
func PartitionErrorClass(err string) string {
	e := strings.ToLower(err)
	switch {
	case strings.Contains(e, "crash"):
		return "crash-loop"
	case strings.Contains(e, "build"):
		return "build"
	case strings.Contains(e, "start") || strings.Contains(e, "spawn"):
		return "start"
	case strings.Contains(e, "exit"):
		return "exit"
	}
	return "other"
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
	// capHits: when each tile's people's partitions last met the running
	// caps (runner.PartitionCapHits; SetPartitionCapHits).
	capHits func() map[string]PartitionCapHit
	// lastCode: the primary's newest finished code move (deploy, promote,
	// rollback, …) per the deployments plane (SetPartitionLastCode).
	lastCode func(tile string) (map[string]any, bool)
}

// SetPartitionLastCode installs the deployments plane's last code move of a
// tile's primary: the trust panel's "their last code changes" (06 §4).
func (b *Broker) SetPartitionLastCode(f func(tile string) (map[string]any, bool)) {
	o := b.partOps()
	o.mu.Lock()
	o.lastCode = f
	o.mu.Unlock()
}

// lastCodeChange is tile's primary's last code move, nil when unknown (a
// tile without deployments runs its work tree: every save is a change).
func (b *Broker) lastCodeChange(tile string) map[string]any {
	o := b.partOps()
	o.mu.RLock()
	f := o.lastCode
	o.mu.RUnlock()
	if f == nil {
		return nil
	}
	if m, ok := f(tile); ok {
		return m
	}
	return nil
}

// PartitionCapHit is when a tile's people's partitions last met the running
// caps, how (evicted, refused, deferred) and how often since xbind started.
type PartitionCapHit struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
	Count int       `json:"count"`
}

// capsHitRecently is how far back bx doctor's "caps hit recently" looks.
const capsHitRecently = 24 * time.Hour

// SetPartitionCapHits installs the runner's cap hits (boot).
func (b *Broker) SetPartitionCapHits(f func() map[string]PartitionCapHit) {
	o := b.partOps()
	o.mu.Lock()
	o.capHits = f
	o.mu.Unlock()
}

// recentCapHit is tile's cap hit within capsHitRecently, if any.
func (b *Broker) recentCapHit(tile string) (PartitionCapHit, bool) {
	o := b.partOps()
	o.mu.RLock()
	f := o.capHits
	o.mu.RUnlock()
	if f == nil {
		return PartitionCapHit{}, false
	}
	h, ok := f()[tile]
	return h, ok && time.Since(h.At) < capsHitRecently
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
	srv.RegisterAPI("POST /partitions/reviewed", b.apiPartitionReviewed)           // partitionreviewed.go
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

// partitionUser parses partition key part: "user:<id>" names person id;
// anything else is a 400's text.
func partitionUser(part string) (string, error) {
	pt, err := util.ParsePartition(part)
	id, ok := pt.User()
	if err != nil || !ok {
		return "", fmt.Errorf("partition is user:<id> (the global instance is stopped like any backend)")
	}
	return id, nil
}

// livePartitionOf resolves person user's partition of tile ("" any tile)
// to its current partition id: their incarnation's, when a record or (the
// tile roots its scope) a namespace of it exists; the error says why not
// (404).
func (b *Broker) livePartitionOf(tile, user string) (pkey string, err error) {
	if uid := b.storedPartitionUID(user); uid != "" && b.hasPartition(tile, util.PartitionKey(user, uid)) {
		return util.PartitionKey(user, uid), nil
	}
	if tile == "" {
		return "", fmt.Errorf("%s has no partition", user)
	}
	return "", fmt.Errorf("%s has no partition of %s", user, tile)
}

// livePartitionOfOK reports whether person user holds a partition of tile
// ("" any tile) in their current incarnation.
func (b *Broker) livePartitionOfOK(tile, user string) bool {
	_, err := b.livePartitionOf(tile, user)
	return err == nil
}

// hasPartition reports whether partition pkey of tile ("" any tile) has a
// record directory, or — the tile rooting its scope — a namespace.
func (b *Broker) hasPartition(tile, pkey string) bool {
	found := false
	_ = b.eachPartitionDir(tile, func(d partitionDirOf) { found = found || d.pkey == pkey })
	if !found && b.rootsOwnScope(tile) {
		_ = b.eachPartitionNamespace(tile, func(id nsID) { found = found || id.pkey == pkey })
	}
	return found
}

// partitionOpTarget reads a stop's or reset's body and resolves its
// partition, checking the actor's rights before it says anything of the
// tile or the partition: a tile the actor can't read answers as a missing
// one (unless they name their own partition of it); someone else's
// partition is refused unless mayOthers says the actor may (403); only then
// the tile's mode (409) and the partition (404). ok false: answered.
func (b *Broker) partitionOpTarget(w http.ResponseWriter, r *http.Request, need string, mayOthers func(person auth.Principal, tile string) bool, refuse string) (person auth.Principal, body partitionOpBody, tile, user, pkey string, ok bool) {
	person, ok = b.partitionActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	ok = false
	if err := server.DecodeJSON(r, &body); err != nil || body.Tile == "" || body.Partition == "" {
		server.WriteError(w, http.StatusBadRequest, "need "+need, "/docs/protocol.md")
		return
	}
	tile = strings.Trim(body.Tile, "/")
	user, err := partitionUser(body.Partition)
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error(), modeDocs)
		return
	}
	own := user == person.UserID
	if !b.IsAdmin(person) && !person.CanReadTile(tile) && !(own && b.hasPartition(tile, util.PartitionKey(user, b.storedPartitionUID(user)))) {
		server.WriteError(w, http.StatusNotFound, "no such tile: "+tile, modeDocs)
		return
	}
	if !own && !mayOthers(person, tile) {
		server.WriteError(w, http.StatusForbidden, refuse, modeDocs)
		return
	}
	if status, msg := b.partitionedTile(tile); status != 0 {
		server.WriteError(w, status, msg, modeDocs)
		return
	}
	if pkey, err = b.livePartitionOf(tile, user); err != nil {
		server.WriteError(w, http.StatusNotFound, err.Error(), modeDocs)
		return
	}
	return person, body, tile, user, pkey, true
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
	person, _, tile, user, _, ok := b.partitionOpTarget(w, r, "{tile, partition}", func(p auth.Principal, tile string) bool {
		return b.IsAdmin(p) || b.mayManageTile(p, tile)
	}, "stopping someone else's partition is a tile manager's or an admin's act")
	if !ok {
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
	person, body, tile, user, pkey, ok := b.partitionOpTarget(w, r, "{tile, partition, confirm: \"<tile> <partition>\"}", func(p auth.Principal, _ string) bool {
		return b.IsAdmin(p)
	}, "resetting someone else's partition is an admin's act")
	if !ok {
		return
	}
	own := user == person.UserID
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
		if user, err := partitionUser(body.Partition); err == nil && b.livePartitionOfOK(tile, user) {
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
		usage = b.partitionBytesCached(tile, util.PartitionKey(id, uid))
	}
	return usage, b.PartitionBytes(tile)
}
