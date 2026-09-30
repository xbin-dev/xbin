package broker

// partitionswitch.go — a tile manager's decision on a partition mode switch
// request (plans/partitions/01 §2.4-§2.5; PD-44, PD-49, PD-19; owner ruling
// H1): POST /api/xbin/partitions/mode {tile, act: "keep"|"switch", from, to,
// confirm?, yes?, dryRun?}.
//
//   - Who: a tile manager (mayManageTile: a workspace admin, the tile's user
//     owner, an admin of its owning org) acting as a person — their own
//     session, app, device or the root token, or the admin tile's frame under
//     their login (AdminFrameDriver). No tile principal decides: not the
//     tile's instance, frames, terminals or agent sessions, whatever grants
//     its tile holds. Writers raise requests by changing code; deleting other
//     people's data is governance.
//   - from/to must still be the request's R and Q (a race answers 409 with
//     the current ones). "keep" answers an open request; "switch" an open or
//     a declined one (bx doctor and the surfaces offer Switch… on a declined
//     tile).
//   - keep records the decline (partitionmode.go's recordDecision): R runs
//     again at once, nothing is deleted.
//   - switch needs the typed tile path (confirm), --isolate for user
//     partitions (PD-19), a tile that isn't offloaded, and — when the tile
//     binds sandbox managers whose hello.caps lack "partitions" (C12) — yes.
//     Then, in order: the namespaces are held (holdNS), every instance
//     stops (the tile's deployments, the scope's other tiles, each plane's
//     stop hook — the partition instances, revoked synchronously), the
//     tile's backups are locked (holdBackups), every wipe hook runs
//     (partitionwipe.go), the backup keys of the wiped data are erased
//     (F17a's eraseBackupSubjectsHeld: every ns: and part: key, or global's
//     ns: key alone when "global" goes; an erase that fails fails the
//     switch), R := Q is recorded with the wiped summary, and people whose
//     partition went are told. Instances start lazily in Q.
//   - dryRun counts what a switch would delete and names what it keeps,
//     deleting nothing: the typed confirmation shows it.
//
// While a switch runs neither the tile's primary nor the scope's other
// tiles may start (switchStartHold, read by PartitionHoldReason), even on a
// declined tile whose R was running.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
)

const (
	modeActKeep   = modeOpKeep
	modeActSwitch = modeOpSwitch
	modeDocs      = "/docs/partitions.md"
	// modeDeciders names who decides, as the refusals and the page say it.
	modeDeciders = "the tile's owner, an admin of its owning org, or a workspace admin"
)

// partitionIsolated reports whether backends run sandboxed (--isolate): user
// partitions need it (PD-19). Tests stand in.
var partitionIsolated = confine.Isolated

// partitionNotice is the person's /xbin/partitions notice of a wipe
// (06 §8, §12.1): F7b's notices store fills it; until then the push alone
// tells them.
var partitionNotice = func(b *Broker, user, tile, text string) {}

// modeActBody is POST /partitions/mode's body.
type modeActBody struct {
	Tile    string                  `json:"tile"`
	Act     string                  `json:"act"`
	From    *registry.PartitionSpec `json:"from"`
	To      *registry.PartitionSpec `json:"to"`
	Confirm string                  `json:"confirm,omitempty"`
	Yes     bool                    `json:"yes,omitempty"`
	DryRun  bool                    `json:"dryRun,omitempty"`
}

// ---- wiring: what boot installs, per broker ----

// partitionWiring is the push plane and the deploy log as the mode acts
// reach them; kept beside the Broker, which stays as it is.
type partitionWiring struct {
	mu sync.Mutex
	// push sends one notification to user (kind, title, body, a
	// workspace-relative link, a collapse id); nil: push is off.
	push func(user, kind, title, body, link, collapse string)
	// deployLog writes a line in the tile's deploy log (D127); nil: none.
	deployLog func(tile, by, via string) error
}

var partitionWirings sync.Map // *Broker → *partitionWiring

func (b *Broker) partitionWiring() *partitionWiring {
	v, _ := partitionWirings.LoadOrStore(b, &partitionWiring{})
	return v.(*partitionWiring)
}

// SetPartitionPush installs how the mode acts push to a person: switch
// requests to managers, wipe notices to people (boot: the push plane).
func (b *Broker) SetPartitionPush(push func(user, kind, title, body, link, collapse string)) {
	w := b.partitionWiring()
	w.mu.Lock()
	w.push = push
	w.mu.Unlock()
}

// SetPartitionDeployLog installs the deploy log a switch writes its line in
// (boot: the deployments plane).
func (b *Broker) SetPartitionDeployLog(log func(tile, by, via string) error) {
	w := b.partitionWiring()
	w.mu.Lock()
	w.deployLog = log
	w.mu.Unlock()
}

func (b *Broker) pushPerson(user, kind, title, body, link, collapse string) {
	w := b.partitionWiring()
	w.mu.Lock()
	push := w.push
	w.mu.Unlock()
	if push != nil {
		push(user, kind, title, body, link, collapse)
	}
}

// ---- the switch hold ----

var (
	switching  sync.Map     // switchKey → the act's hold text
	switchingN atomic.Int32 // switches running: a start's check reads nothing more while none does
)

type switchKey struct {
	b    *Broker
	tile string
}

// beginSwitch holds tile for a switch with the text hold; ok false when a
// switch of it already runs. end releases the hold, once.
func (b *Broker) beginSwitch(tile, hold string) (end func(), ok bool) {
	if _, busy := switching.LoadOrStore(switchKey{b, tile}, hold); busy {
		return nil, false
	}
	switchingN.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() { switching.Delete(switchKey{b, tile}); switchingN.Add(-1) })
	}, true
}

// switchHold is the hold text of a switch running on tile itself ("":
// none): "holds data" answers yes meanwhile (partitionmode.go's decide).
func (b *Broker) switchHold(tile string) string {
	if switchingN.Load() == 0 {
		return ""
	}
	if v, ok := switching.Load(switchKey{b, tile}); ok {
		return v.(string)
	}
	return ""
}

// switchStartHold is why tile's backends may not start while a switch runs
// ("": none) — read by PartitionHoldReason: tile's own switch, or one of
// the tile rooting tile's scope, which empties the namespaces tile reads
// too. Nothing of the scope starts between the switch's stop and its wipe.
func (b *Broker) switchStartHold(tile string) string {
	if switchingN.Load() == 0 {
		return ""
	}
	if why := b.switchHold(tile); why != "" {
		return why
	}
	if c, ok := b.Reg.Component(tile); ok && c.Scope != "" && c.Scope != tile && b.switchHold(c.Scope) != "" {
		return "is paused: " + c.Scope + "'s partition mode is switching and the data of its scope is being deleted"
	}
	return ""
}

// ---- the route ----

func (b *Broker) registerPartitionMode(srv *server.Server) {
	srv.RegisterAPI("POST /partitions/mode", b.apiPartitionMode)
}

// modeDecider is the person p acts as, when p may decide at all: a person's
// own credential, or the admin tile's frame under their login. ok false: the
// refusal is written.
func (b *Broker) modeDecider(w http.ResponseWriter, p auth.Principal) (auth.Principal, bool) {
	if p.Component == "" {
		if p.Owner || p.UserID != "" {
			return p, true
		}
	} else if d, ok := b.AdminFrameDriver(p); ok {
		return d, true
	}
	server.WriteError(w, http.StatusForbidden, "deciding a partition mode switch is a person's act — "+modeDeciders+
		", in their own session (bx, the shell, the admin console); no tile's backend, frame, terminal or agent decides", modeDocs)
	return p, false
}

// deciderName is who decided, as the record and the notices name them.
func deciderName(p auth.Principal) string {
	if p.UserID != "" {
		return p.UserID
	}
	return "owner"
}

func (b *Broker) apiPartitionMode(w http.ResponseWriter, r *http.Request) {
	person, ok := b.modeDecider(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	var body modeActBody
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {tile, act: keep|switch, from, to, confirm?, yes?, dryRun?}: "+err.Error(), "/docs/protocol.md")
		return
	}
	tile := strings.Trim(body.Tile, "/")
	if tile == "" || body.Act != modeActKeep && body.Act != modeActSwitch {
		server.WriteError(w, http.StatusBadRequest, "need {tile, act: keep|switch, from, to}", "/docs/protocol.md")
		return
	}
	if !b.mayManageTile(person, tile) {
		server.WriteError(w, http.StatusForbidden, "deciding "+tile+"'s partition mode is a tile manager's act: "+modeDeciders, modeDocs)
		return
	}
	c, ok := b.Reg.Component(tile)
	if !ok {
		server.WriteError(w, http.StatusNotFound, "no such tile: "+tile)
		return
	}
	from, to := registry.SpecOf(body.From), registry.SpecOf(body.To)
	if status, msg := modeActStale(c, body.Act, from, to); status != 0 {
		writeModeRefusal(w, status, msg, c)
		return
	}
	if body.Act == modeActKeep {
		b.actKeep(w, person, c, from, to, body.DryRun)
		return
	}
	b.actSwitch(w, person, c, from, to, body)
}

// modeActStale judges act on c's request from → to against the registry's
// settled state: 0 when it still matches, else the status and why.
func modeActStale(c *registry.Component, act string, from, to registry.PartitionSpec) (int, string) {
	st, r, req := c.PartitionState()
	switch {
	case c.PartitionRecordUnknown():
		return http.StatusConflict, c.Path + "'s partition mode record can't be read: nothing is decided until an admin repairs it (" + c.PartitionErr + ")"
	case st == registry.PartitionInvalid:
		return http.StatusConflict, c.Path + "'s partition request is invalid, so there is nothing to decide: " + cmp.Or(c.PartitionErr, "fix the code's partition key")
	case req == nil:
		return http.StatusConflict, c.Path + " has no partition mode switch request (it runs " + r.String() + ")"
	case r != from || registry.SpecOf(req.Spec) != to:
		return http.StatusConflict, fmt.Sprintf("%s's request changed: it is now %s → %s, not %s → %s — look again", c.Path, r, registry.SpecOf(req.Spec), from, to)
	case act == modeActKeep && req.Declined:
		return http.StatusConflict, c.Path + " already keeps its current mode (" + r.String() + ")"
	}
	return 0, ""
}

// writeModeRefusal answers a refusal with c's current partition state, so a
// client can look again.
func writeModeRefusal(w http.ResponseWriter, status int, msg string, c *registry.Component) {
	st, r, req := c.PartitionState()
	cur := map[string]any{"state": st.String(), "from": r}
	if req != nil {
		cur["to"], cur["declined"] = registry.SpecOf(req.Spec), req.Declined
	}
	server.WriteJSON(w, status, map[string]any{"error": msg, "docs": modeDocs, "partition": cur})
}

// ---- keep ----

func (b *Broker) actKeep(w http.ResponseWriter, person auth.Principal, c *registry.Component, from, to registry.PartitionSpec, dry bool) {
	out := map[string]any{"tile": c.Path, "act": modeActKeep, "mode": from, "declined": to}
	if dry {
		out["dryRun"], out["deletes"] = true, "nothing"
		server.WriteJSON(w, http.StatusOK, out)
		return
	}
	by := deciderName(person)
	if err := b.recordDecision(c.Path, modeOpKeep, from, to, by, nil); err != nil {
		b.writeDecisionErr(w, c, err)
		return
	}
	b.settleAfterDecision(c.Path)
	slog.Info("partitions: kept the current mode", "tile", c.Path, "mode", from.String(), "declined", to.String(), "by", by)
	out["ok"] = true
	server.WriteJSON(w, http.StatusOK, out)
}

// writeDecisionErr answers recordDecision's refusal.
func (b *Broker) writeDecisionErr(w http.ResponseWriter, c *registry.Component, err error) {
	switch {
	case errors.Is(err, errModeStale):
		writeModeRefusal(w, http.StatusConflict, c.Path+"'s partition mode or request changed since: look again", c)
	case errors.Is(err, errRecordUnread):
		server.WriteError(w, http.StatusConflict, err.Error(), modeDocs)
	default:
		server.WriteError(w, http.StatusInternalServerError, "recording the decision: "+err.Error())
	}
}

// settleAfterDecision settles the tile's new record (the registry reads it
// at a rescan) and tells what shows it: the tile's frames and the shell
// reload.
func (b *Broker) settleAfterDecision(tile string) {
	b.rescanAfterDecision(tile)
	b.reloadAfterDecision(tile)
}

func (b *Broker) rescanAfterDecision(tile string) {
	if err := b.Reg.Rescan(); err != nil {
		slog.Warn("partitions: rescan after a mode decision", "tile", tile, "err", err)
	}
}

func (b *Broker) reloadAfterDecision(tile string) {
	b.Hub.Publish(events.Event{Type: "reload", Component: tile})
}

// ---- switch ----

func (b *Broker) actSwitch(w http.ResponseWriter, person auth.Principal, c *registry.Component, from, to registry.PartitionSpec, body modeActBody) {
	tile := c.Path
	switch {
	case to.User && !partitionIsolated():
		server.WriteError(w, http.StatusConflict, "user partitions need isolation: run xbind with --isolate before switching "+tile+" to "+to.String(), modeDocs)
		return
	case registry.IsOffloaded(b.Reg.LifecycleState(tile)):
		server.WriteError(w, http.StatusConflict, tile+" is offloaded: restore it (bx enable) before switching its partition mode, so its data can be deleted everywhere", modeDocs)
		return
	case !body.DryRun && body.Confirm != tile:
		server.WriteError(w, http.StatusBadRequest, "a switch deletes "+registry.SwitchDeletes(from, to)+": confirm by typing the tile's path ("+tile+")", modeDocs)
		return
	}
	var lacking []string
	if to.User {
		lacking = b.managersLackingPartitions(c)
	}
	roots := c.Scope != "" && c.Scope == c.Path
	t := wipeTarget{Tile: tile, Scope: c.Scope, RootsScope: roots, OwnsMain: roots && b.ownsMainData(c.Scope), From: from, To: to,
		Kind: wipeKindOf(from, to), By: deciderName(person), At: time.Now(), DryRun: body.DryRun}
	if body.DryRun {
		sum, err := b.countWipe(t)
		if err != nil {
			server.WriteError(w, http.StatusInternalServerError, "counting what the switch deletes: "+err.Error())
			return
		}
		server.WriteJSON(w, http.StatusOK, switchAnswer(t, sum, lacking, map[string]any{"dryRun": true}))
		return
	}
	if len(lacking) > 0 && !body.Yes {
		server.WriteJSON(w, http.StatusConflict, map[string]any{"docs": "/docs/sandbox-manager.md", "managers": lacking,
			"error": tile + " binds sandbox managers that don't keep people apart (their hello.caps lack \"partitions\"): " +
				strings.Join(lacking, ", ") + " — each person's partition would see every person's sandboxes there; update them, or switch anyway with yes"})
		return
	}
	hold := "is paused: its partition mode is switching (" + from.String() + " → " + to.String() + ") and its data is being deleted"
	end, ok := b.beginSwitch(tile, hold)
	if !ok {
		server.WriteError(w, http.StatusConflict, "a partition mode switch of "+tile+" is already running", modeDocs)
		return
	}
	defer end()
	sum, gc, err := b.runSwitch(t)
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]any{"docs": modeDocs, "wiped": sum.counts(),
			"error": "the switch of " + tile + " stopped part-way: " + err.Error() + " — nothing is recorded and the request stays open; what was deleted stays deleted: try again"})
		return
	}
	if err := b.recordDecision(tile, modeOpSwitch, from, to, t.By, sum.counts()); err != nil {
		// the code asked for something else meanwhile: the data is gone, the
		// mode isn't recorded — the next settle judges the tile, now empty
		slog.Warn("partitions: a switch deleted the data, but its decision can't be recorded", "tile", tile, "err", err)
		server.WriteJSON(w, http.StatusConflict, map[string]any{"docs": modeDocs, "wiped": sum.counts(),
			"error": "the switch of " + tile + " deleted its data, but the mode wasn't recorded: " + err.Error() + " — look again"})
		return
	}
	// The hold stays on until the rescan has settled the new record: until
	// then the registry still shows the old mode — on a tile that was running
	// (a switch after "keep"), a person's partition running and writable —
	// so a partition's write released early would land in what was just
	// wiped. Released before the reload, so reloading frames find the tile
	// running in its new mode.
	b.rescanAfterDecision(tile)
	end()
	b.reloadAfterDecision(tile)
	b.afterSwitch(t, sum, person)
	extra := map[string]any{"ok": true}
	if gc != "" {
		extra["archiver"] = gc
	}
	if sum.EraseErr != "" {
		extra["eraseError"] = sum.EraseErr
	}
	server.WriteJSON(w, http.StatusOK, switchAnswer(t, sum, lacking, extra))
}

// runSwitch holds, stops, wipes and erases (01 §2.5 steps 1–3). Every
// namespace to wipe is held first (holdNS: no backend starts in it, no API
// write lands), then every instance stops — the tile's primary and the
// scope's other tiles can't start again meanwhile (switchStartHold) — so
// nothing started in between keeps writing into what is wiped. A failure
// before anything is removed removes nothing; one during the wipe leaves
// what was removed removed. An erase that wrote no tombstone fails the
// switch as well: the data went, but its sealed backups would still read,
// so nothing is recorded and a retry erases (the wipe finds nothing left).
func (b *Broker) runSwitch(t wipeTarget) (sum wipeSummary, gc string, err error) {
	for _, id := range b.wipeHeldNamespaces(t) {
		release, err := b.holdNS(id, nsResetting)
		if err != nil {
			return sum, "", fmt.Errorf("%s's data is busy (%w): nothing was deleted", id.dep, err)
		}
		defer release()
	}
	b.StopBackendSafe(t.Tile) // every deployment of the tile
	if t.RootsScope {
		for _, o := range b.Reg.Components() {
			if o.Scope == t.Scope && o.Path != t.Tile {
				b.StopBackendSafe(o.Path) // the scope's other tiles read its namespaces too
			}
		}
	}
	for _, h := range wipeHooks {
		if h.stop != nil {
			h.stop(b, t)
		}
	}
	defer b.holdBackups(t.Tile)() // no backup seals the doomed data under a fresh key meanwhile (F17a)
	if t.OwnsMain && t.Kind != wipeNone && !t.DryRun {
		// main's volumes the wipe removes mount again, empty, as at
		// provision (a restore's precedent), on every way out — a wipe that
		// stops part-way too — while the holds still stand: only
		// MountEncrypted mounts main's, so without it the tile's instance at
		// today's keys (the unpartitioned one, global, or a person's shared
		// volumes) stays held until xbind restarts or the workspace changes
		defer b.MountEncrypted()
	}
	for _, h := range wipeHooks {
		if err := h.wipe(b, t, &sum); err != nil {
			return sum, "", fmt.Errorf("%s: %w", h.name, err)
		}
	}
	match := erasedSubjects(t)
	if match == nil {
		return sum, "", nil
	}
	erased, gc, err := b.eraseBackupSubjectsHeld(t.Tile, match, "partition mode switch "+t.From.String()+" → "+t.To.String(), t.By)
	sum.Subkeys = int64(len(erased))
	switch {
	case err != nil && len(erased) == 0: // no tombstone written: every key as it was
		return sum, "", fmt.Errorf("erasing the deleted data's backup keys: %w", err)
	case err != nil: // erased all the same; the key files left are removed wherever met
		sum.EraseErr, sum.KeyFilesLeft = err.Error(), int64(countErrs(err))
		slog.Warn("partitions: a switch's backup keys are erased, but not every key file is removed yet", "tile", t.Tile, "err", err)
	}
	return sum, gc, nil
}

// countErrs counts the errors err joins (1 for a single one).
func countErrs(err error) int {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return len(j.Unwrap())
	}
	return 1
}

// erasedSubjects picks the backup subjects a switch erases (11 §3): every
// ns: and part: key of the tile when it deletes everything; global's
// namespace key alone when "global" goes; none when it comes. Never tile:
// (source backups survive a switch).
func erasedSubjects(t wipeTarget) func(subject string) bool {
	switch t.Kind {
	case wipeEverything:
		return func(s string) bool { return strings.HasPrefix(s, "ns:") || strings.HasPrefix(s, "part:") }
	case wipeGlobal:
		if !t.OwnsMain {
			return nil // global's namespace at today's keys isn't the tile's: nothing of it went
		}
		main := nsSubject(t.Scope, "")
		return func(s string) bool { return s == main }
	}
	return nil
}

// countWipe is the dry run: every hook counts, and the keys an erase would
// take are counted.
func (b *Broker) countWipe(t wipeTarget) (wipeSummary, error) {
	var sum wipeSummary
	for _, h := range wipeHooks {
		if err := h.wipe(b, t, &sum); err != nil {
			return sum, fmt.Errorf("%s: %w", h.name, err)
		}
	}
	if match := erasedSubjects(t); match != nil {
		keys, err := b.backupKeys().list()
		if err != nil {
			return sum, fmt.Errorf("the backup keys: %w", err)
		}
		for _, k := range keys {
			if k.Tile == t.Tile && match(k.Subject) {
				sum.Subkeys++
			}
		}
	}
	return sum, nil
}

// switchKeeps is what a switch keeps (01 §2.6).
func switchKeeps(k wipeKind) []string {
	keeps := []string{
		"the code: the tile directory, its checkpoints and deployment records",
		"grants and global bindings",
		"the tile's own terminal layer",
		"people's homes and their own agent-session history",
		"records a provider keeps, such as sandboxes at a sandbox manager (clean them up there)",
		"backups made before sealing (plaintext archives), which no key erases",
	}
	if k == wipeGlobal {
		keeps = append(keeps, "every person's partition: its data, vault and registrations",
			"the data of the tile's other deployments")
	}
	return keeps
}

// switchAnswer is a switch's answer, or its dry run's.
func switchAnswer(t wipeTarget, sum wipeSummary, lacking []string, extra map[string]any) map[string]any {
	out := map[string]any{"tile": t.Tile, "act": modeActSwitch, "from": t.From, "to": t.To,
		"deletes": registry.SwitchDeletes(t.From, t.To), "wiped": sum.counts(), "keeps": switchKeeps(t.Kind)}
	if len(lacking) > 0 {
		out["managers"] = lacking
	}
	if len(sum.People) > 0 {
		out["people"] = len(sum.People)
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// afterSwitch writes the deploy-log line, the audit line and tells each
// person whose partition went (01 §2.5 steps 4–5).
func (b *Broker) afterSwitch(t wipeTarget, sum wipeSummary, person auth.Principal) {
	slog.Info("partitions: switched the mode", "tile", t.Tile, "from", t.From.String(), "to", t.To.String(),
		"by", t.By, "wiped", sum.counts(), "people", len(sum.People))
	w := b.partitionWiring()
	w.mu.Lock()
	log := w.deployLog
	w.mu.Unlock()
	if log != nil {
		if err := log(t.Tile, person.From(), person.Via); err != nil {
			slog.Warn("partitions: the switch's deploy-log line", "tile", t.Tile, "err", err)
		}
	}
	when := t.At.UTC().Format("2006-01-02 15:04 UTC")
	for _, user := range sum.People {
		text := fmt.Sprintf("%s changed how it keeps data; your data in it was deleted by %s at %s", t.Tile, t.By, when)
		partitionNotice(b, user, t.Tile, text)
		b.pushPerson(user, "tile.partition-deleted", t.Tile+" changed how it keeps data",
			fmt.Sprintf("Your data in it was deleted by %s at %s.", t.By, when), "c/"+t.Tile+"/", "")
	}
}

// PartitionHoldsData answers "holds data" for tile now (01 §2.2): the
// deployments plane's promote and roll-back dry runs warn with it that a
// move will pause the tile (01 §2.7). A tile the registry doesn't know
// holds none.
func (b *Broker) PartitionHoldsData(tile string) bool {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return false
	}
	held, _, _ := b.tileHoldsData(registry.PartitionAsk{Tile: tile, Scope: c.Scope, RootsScope: c.Scope != "" && c.Scope == c.Path})
	return held
}

// ---- sandbox managers (C12) ----

// managersLackingPartitions names the sandbox managers c binds whose hello
// doesn't carry the capability "partitions": each would see a partitioned
// consumer's people as one. A manager that can't be asked counts as lacking.
func (b *Broker) managersLackingPartitions(c *registry.Component) []string {
	ws := b.Reg.Workspace()
	var out []string
	for slot, it := range c.Manifest.Interfaces {
		if it.Service != "sandbox-manager" {
			continue
		}
		for _, ref := range ws.Bindings[c.Path][slot].Refs() {
			prov, _ := splitRef(ref)
			if prov == "" || slices.Contains(out, prov) || b.managerKeepsPartitions(prov) {
				continue
			}
			out = append(out, prov)
		}
	}
	slices.Sort(out)
	return out
}

// managerKeepsPartitions asks prov's GET /sbx/hello whether its caps carry
// "partitions".
func (b *Broker) managerKeepsPartitions(prov string) bool {
	code, body, err := b.archiveDo(http.MethodGet, prov, "/sbx/hello?protocol=1", nil)
	if err != nil || code != http.StatusOK {
		return false
	}
	var hello struct {
		Caps []string `json:"caps"`
	}
	return json.Unmarshal(body, &hello) == nil && slices.Contains(hello.Caps, "partitions")
}
