package broker

// partitionconsent.go — cross-tile partition edges: the per-person consent
// the workspace policy partitionConsent asks for, and the approval warning
// (plans/partitions 05 §1-§2; PD-12, PD-13 decided, PD-14, PD-46).
//
// A call (Route rule 4) or a data reach from person A's partition of a
// partitioned tile Z into A's partition of a partitioned tile X needs the
// grant (Route and allowAt), A's read access on X (addressedPartition's
// personLive), and — only while the workspace policy partitionConsent is on
// (policies.go) — A's consent record for Z → X, which this file keeps:
//
//   - data/partitions/consents/<uid>.json, {schema: 1, user, uid, edges:
//     {"<Z>→<X>": {at, via}}}, keyed by the person's uid, so a person deleted
//     and recreated under the same id inherits none;
//   - only A's own credential writes it (GET/POST/DELETE
//     /partitions/consents, PersonOnly: their session, app or device —
//     never tile code, view-as or the root token), and only while the policy
//     is on; revoking works in both settings. Turning the policy off keeps
//     the records, unused; they apply again when it returns;
//   - addressedPartition asks partitionConsentHolds (consentOrAsk,
//     partitionwire.go) at every call and reach, so a revocation holds from
//     the next call. No volume of another scope is ever bound into a
//     partition (EnvFor hands no cross-scope file resource); a revocation
//     also stops Z's instance of A (it starts again on the next request),
//     and the streams that instance held with it. Streams Z's frames,
//     terminals or agent sessions opened as A through the proxy aren't
//     tracked: they last until they end (an open end, records/F10.md);
//   - a refused edge prompts A — a `partitions` event, op consent-needed, to
//     A's own sockets and a push linking to /xbin/partitions — at most once
//     a day per edge, and only when Z holds a grant on X (tile code with no
//     grant can't make people consent ahead of an admin's approval);
//   - a shared resource of X is no person's data: no consent (reachPartition);
//   - a consent names tiles by path: a tile that goes (deleted, moved) or
//     switches mode takes every consent naming it (PartitionTileChanged,
//     the "consents" wipe hook), so a new tile at the path starts with none.
//
// The approval warning (S1) is shown in both settings, beside a pending
// grant of a partitioned tile on another partitioned tile's data (GET
// /grants' pending rows: the grants element, the admin console's grants
// view, the organisations tile, bx grants).

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	consentsDir     = "consents" // data/partitions/consents/<uid>.json
	consentSchema   = 1
	consentAskQuiet = 24 * time.Hour // one prompt a day per person and edge
	consentsDocs    = "/docs/partitions.md"
	// consentPage is where a person allows or revokes (06 §12.1): the push's
	// workspace-relative link.
	consentPage = "xbin/partitions"
)

// consentOrAsk is partitionConsentHolds (partitionwire.go): userID's
// consent to from → to, and, without one, a prompt (consentNeeded).
func (b *Broker) consentOrAsk(userID, from, to string) bool {
	if b.consentHolds(userID, from, to) {
		return true
	}
	b.consentNeeded(userID, from, to)
	return false
}

// consentDoc is one person's consent file.
type consentDoc struct {
	Schema int                    `json:"schema"`
	User   string                 `json:"user"`
	UID    string                 `json:"uid"`
	Edges  map[string]consentEdge `json:"edges"`
}

// consentEdge is one recorded consent: when, and through which credential.
type consentEdge struct {
	At  time.Time `json:"at"`
	Via string    `json:"via,omitempty"`
}

// consentArrow joins an edge's key in the file: "<from>→<to>".
const consentArrow = "→"

// consentKey is an edge's key in the file. No consent names a tile whose
// path holds the arrow (POST refuses one; consentHolds never matches one),
// so every key splits at its one arrow.
func consentKey(from, to string) string { return from + consentArrow + to }

// consentEdgeOf splits a key: ok false for one with no arrow or more.
func consentEdgeOf(key string) (from, to string, ok bool) {
	from, to, ok = strings.Cut(key, consentArrow)
	return from, to, ok && from != "" && to != "" && !strings.Contains(to, consentArrow)
}

// errConsentsUnreadable: a person's consent file this xbind can't read —
// counted as no consent, never written over; /alerts shows it to admins.
var errConsentsUnreadable = errors.New("this xbind can't read the consent record")

// consentState is one broker's consent plane: the files read (by uid, with
// the stamp they were read at) and the prompts sent. Kept beside the
// Broker, which stays as it is.
type consentState struct {
	mu    sync.Mutex
	docs  map[string]consentCached
	asked map[string]time.Time // user\x00from\x00to → the last prompt
}

type consentCached struct {
	stamp fileStamp
	doc   *consentDoc
	err   error
}

var (
	consentStates sync.Map   // *Broker → *consentState
	consentNow    = time.Now // tests stand in
)

func (b *Broker) consents() *consentState {
	v, _ := consentStates.LoadOrStore(b, &consentState{docs: map[string]consentCached{}, asked: map[string]time.Time{}})
	return v.(*consentState)
}

func (b *Broker) consentsBase() string {
	return filepath.Join(b.Reg.Root, "data", partitionsDir, consentsDir)
}

func (b *Broker) consentPath(uid string) string {
	return filepath.Join(b.consentsBase(), uid+".json")
}

// readConsentDoc reads uid's file: nil without one. A file this xbind
// can't read (it doesn't parse, another schema, another uid) is an error
// (errConsentsUnreadable): no consent, and never written over.
func readConsentDoc(path, uid string) (*consentDoc, error) {
	raw, err := os.ReadFile(path) // walk-ok: data/partitions is xbind's own; no sandbox sees it
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errConsentsUnreadable, err)
	}
	var d consentDoc
	switch {
	case json.Unmarshal(raw, &d) != nil:
		return nil, fmt.Errorf("%w: it doesn't parse", errConsentsUnreadable)
	case d.Schema != consentSchema:
		return nil, fmt.Errorf("%w: schema %d, this xbind reads %d", errConsentsUnreadable, d.Schema, consentSchema)
	case d.UID != uid:
		return nil, fmt.Errorf("%w: it names another uid than its file", errConsentsUnreadable)
	}
	if d.Edges == nil {
		d.Edges = map[string]consentEdge{}
	}
	return &d, nil
}

// consentDocOf is uid's consents as the file says now (re-read when its
// size or mtime changed: a restore or a hand edit is picked up).
func (b *Broker) consentDocOf(uid string) (*consentDoc, error) {
	if !users.UIDOK(uid) {
		return nil, fmt.Errorf("%q is no uid", uid)
	}
	path := b.consentPath(uid)
	stamp, err := stampFile(path)
	if err != nil {
		return nil, err
	}
	cs := b.consents()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if c, ok := cs.docs[uid]; ok && c.stamp == stamp {
		return c.doc, c.err
	}
	doc, err := readConsentDoc(path, uid)
	if err != nil {
		slog.Warn("partitions: a consent file this xbind can't read counts as no consent", "uid", uid, "err", err)
	}
	cs.docs[uid] = consentCached{stamp: stamp, doc: doc, err: err}
	return doc, err
}

// consentHolds reports whether userID — this incarnation, by their uid —
// let tile from use their data in tile to. No uid, no file, a file this
// xbind can't read: no.
func (b *Broker) consentHolds(userID, from, to string) bool {
	uid := b.storedPartitionUID(userID)
	if uid == "" || strings.Contains(from, consentArrow) || strings.Contains(to, consentArrow) {
		return false
	}
	doc, err := b.consentDocOf(uid)
	if err != nil || doc == nil || doc.User != userID {
		return false
	}
	_, ok := doc.Edges[consentKey(from, to)]
	return ok
}

// editConsents applies edit to userID's file under the plane's lock and
// writes it: uid is the person's (minted by the caller). A file this xbind
// can't read is never written over.
func (b *Broker) editConsents(userID, uid string, edit func(d *consentDoc) bool) (*consentDoc, error) {
	cs := b.consents()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	path := b.consentPath(uid)
	doc, err := readConsentDoc(path, uid)
	if err != nil {
		return nil, fmt.Errorf("%w — it is kept as it is until an admin fixes or removes data/partitions/consents/%s.json (/alerts names it)", err, uid)
	}
	if doc == nil {
		doc = &consentDoc{Schema: consentSchema, User: userID, UID: uid, Edges: map[string]consentEdge{}}
	}
	if doc.User != userID {
		return nil, fmt.Errorf("%w: it names another person — it is kept as it is until an admin fixes or removes data/partitions/consents/%s.json", errConsentsUnreadable, uid)
	}
	if !edit(doc) {
		return doc, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := fsutil.WriteFileAtomic(path, append(raw, '\n'), 0o600); err != nil {
		return nil, err
	}
	delete(cs.docs, uid) // the next read re-stamps
	return doc, nil
}

// ---- prompts ----

// consentNeeded tells userID, at most once a day per edge, that tile from
// asked for their data in tile to and was refused (05 §2): a `partitions`
// event, op consent-needed, to their sockets, and a push. Only for an edge
// the grant allows: tile code without one can't make people consent ahead
// of an admin. Asked from addressedPartition, possibly inside the event
// hub's filter, so the event and the push leave on their own goroutine.
func (b *Broker) consentNeeded(userID, from, to string) {
	if !b.edgeGranted(from, to) {
		return
	}
	cs := b.consents()
	key, now := userID+"\x00"+from+"\x00"+to, consentNow()
	cs.mu.Lock()
	if at, ok := cs.asked[key]; ok && now.Sub(at) < consentAskQuiet {
		cs.mu.Unlock()
		return
	}
	for k, at := range cs.asked { // keep the map small: forget old prompts
		if now.Sub(at) >= consentAskQuiet {
			delete(cs.asked, k)
		}
	}
	cs.asked[key] = now
	cs.mu.Unlock()
	go b.promptConsent(userID, from, to)
}

// personEvent is the data of a `partitions` event for the person's own
// sockets only — their session, app or device — never the principals of
// the tile it names acting in their partition (partitionEventFor asks
// PersonOnly): the callee's code never learns who tried to reach its
// people's data, nor who allowed it.
type personEvent map[string]any

// PersonOnly marks the event for the person's own sockets.
func (personEvent) PersonOnly() bool { return true }

// promptConsent sends one consent-needed prompt.
func (b *Broker) promptConsent(userID, from, to string) {
	if b.Hub != nil {
		b.Hub.Publish(events.Event{Type: "partitions", Component: to, Partition: string(util.UserPartition(userID)),
			Data: personEvent{"op": "consent-needed", "from": from, "to": to}})
	}
	b.pushPerson(userID, "tile.partition-consent", from+" asks for your "+to+" data",
		fmt.Sprintf("%s wants to use your data in %s. Your workspace asks you first: allow it with bx partition consent %s %s — or ignore it.", from, to, from, to),
		consentPage, "partition-consent:"+consentKey(from, to))
}

// askedOf are the edges userID was prompted for within the last day and
// still hasn't allowed: what the partitions page offers.
func (b *Broker) askedOf(userID string) []map[string]any {
	cs := b.consents()
	now := consentNow()
	cs.mu.Lock()
	var out []map[string]any
	for k, at := range cs.asked {
		parts := strings.Split(k, "\x00")
		if len(parts) == 3 && parts[0] == userID && now.Sub(at) < consentAskQuiet {
			out = append(out, map[string]any{"from": parts[1], "to": parts[2], "at": at.UTC()})
		}
	}
	cs.mu.Unlock()
	out = slices.DeleteFunc(out, func(m map[string]any) bool { return b.consentHolds(userID, m["from"].(string), m["to"].(string)) })
	slices.SortFunc(out, func(a, c map[string]any) int {
		return cmp.Or(strings.Compare(a["from"].(string), c["from"].(string)), strings.Compare(a["to"].(string), c["to"].(string)))
	})
	return out
}

// ---- edges ----

// edgeTile is the partitioned tile a grant target reaches people's data in:
// a tile path, or a res: target's scope root when the resource is kept per
// partition (a shared one is one copy for everyone). "" otherwise.
func (b *Broker) edgeTile(target string) string {
	if strings.HasPrefix(target, "res:") {
		rt, res, ok := b.parseRes(target)
		if !ok || res == nil || rt.Scope == "" || res.Shared != registry.SharedNone {
			return ""
		}
		if _, ok := b.Reg.PartitionedScope(rt.Scope); !ok {
			return ""
		}
		return rt.Scope
	}
	if _, partitioned, err := b.tilePartitioning(target); err != nil || !partitioned {
		return ""
	}
	return target
}

// edgeGranted reports whether tile from's primary — where its people's
// partitions run (PD-17) — holds a grant that reaches people's data in
// partitioned tile to: a call grant (a grant, an http binding, a same-scope
// use) on to, or a granted use of one of to's per-partition resources.
// Asked through resolveTarget, the single evaluation point.
func (b *Broker) edgeGranted(from, to string) bool {
	dep := b.primaryOf(from)
	if b.resolveTarget(from, dep, to).Deny == nil {
		return true
	}
	c, ok := b.Reg.Component(from)
	if !ok {
		return false
	}
	for _, u := range c.Manifest.Uses {
		if strings.HasPrefix(u.Target, "res:") && b.edgeTile(u.Target) == to && b.resolveTarget(from, dep, u.Target).Deny == nil {
			return true
		}
	}
	return false
}

// writeGrantOK answers a grant's approval or revocation: today's ok, and,
// on an approval that reaches another partitioned tile's people's data,
// the approval warning beside it (additive; `bx grant` prints it) — the
// same words the pending row carried. Every other answer is byte-identical.
func (b *Broker) writeGrantOK(w http.ResponseWriter, r *http.Request, g registry.Grant) {
	if r.Method == http.MethodPost {
		if warn := b.partitionGrantWarning(g.From, g.Target); warn != "" {
			server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true", "warning": warn})
			return
		}
	}
	server.WriteOK(w)
}

// writeBindOK answers a binding set as writeGrantOK a grant: an http
// binding is a call grant (05 §3), so binding a partitioned tile's http slot
// to another partitioned tile answers the approval warning beside today's
// ok (additive; `bx bind` prints it). Every other answer is byte-identical.
func (b *Broker) writeBindOK(w http.ResponseWriter, comp, slot string, delta registry.Binding, del bool) {
	var warns []string
	if !del && b.isHTTPSlot(comp, slot) {
		for _, e := range delta {
			prov, _ := splitRef(e.Ref)
			if warn := b.partitionGrantWarning(comp, prov); warn != "" && !slices.Contains(warns, warn) {
				warns = append(warns, warn)
			}
		}
	}
	if len(warns) == 0 {
		server.WriteOK(w)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true", "warning": strings.Join(warns, "; ")})
}

// partitionGrantWarning is the approval warning of a grant of from on
// target when both ends keep each person's data apart (05 §2, S1), in the
// words of the workspace's partitionConsent setting; "" otherwise.
func (b *Broker) partitionGrantWarning(from, target string) string {
	if _, partitioned, err := b.tilePartitioning(from); err != nil || !partitioned {
		return ""
	}
	x := b.edgeTile(target)
	if x == "" || x == from {
		return ""
	}
	who := "every person who can read " + x
	if b.Policies().PartitionConsent {
		who = "every person who allows it"
	}
	return fmt.Sprintf("%s's code — and everyone who can change it — will be able to read and write the %s data of %s", from, x, who)
}

// ---- the routes ----

func (b *Broker) registerPartitionConsents(srv *server.Server) {
	srv.RegisterAPI("GET /partitions/consents", b.apiConsentsList)
	srv.RegisterAPI("POST /partitions/consents", b.apiConsentsAdd)
	srv.RegisterAPI("DELETE /partitions/consents", b.apiConsentsRevoke)
	b.registerPartitionLedger(srv) // GET /partitions/ledger, /partitions/edges (partitionledger.go)
}

// personOwn is the PersonOnly rule (02 §8), which the server's partition
// gate applies too: a person's own session, app or device credential —
// never a tile's, view-as, or one naming no person.
func personOwn(w http.ResponseWriter, p auth.Principal) bool {
	if p.Component != "" || p.Impersonator != "" || p.UserID == "" {
		server.WriteError(w, http.StatusForbidden, "this is a person's own act: sign in and do it yourself — a tile's credentials can't", consentsDocs)
		return false
	}
	return true
}

// consentsView is GET's answer for userID: the policy, their recorded
// consents and the edges they were asked about.
func (b *Broker) consentsView(userID string) map[string]any {
	rows := []map[string]any{}
	if uid := b.storedPartitionUID(userID); uid != "" {
		if doc, err := b.consentDocOf(uid); err == nil && doc != nil && doc.User == userID {
			for k, e := range doc.Edges {
				if from, to, ok := consentEdgeOf(k); ok {
					rows = append(rows, map[string]any{"from": from, "to": to, "at": e.At.UTC(), "via": e.Via})
				}
			}
		}
	}
	slices.SortFunc(rows, func(a, c map[string]any) int {
		return cmp.Or(strings.Compare(a["from"].(string), c["from"].(string)), strings.Compare(a["to"].(string), c["to"].(string)))
	})
	asked := b.askedOf(userID)
	if asked == nil {
		asked = []map[string]any{}
	}
	return map[string]any{"policy": map[string]bool{"partitionConsent": b.Policies().PartitionConsent},
		"consents": rows, "asked": asked}
}

func (b *Broker) apiConsentsList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !personOwn(w, p) {
		return
	}
	server.WriteJSON(w, http.StatusOK, b.consentsView(p.UserID))
}

// consentBody reads {from, to} from the JSON body, or ?from=&to= (DELETE).
func consentBody(r *http.Request) (from, to string, ok bool) {
	var body struct{ From, To string }
	if r.ContentLength != 0 && r.Body != nil {
		if server.DecodeJSON(r, &body) != nil {
			return "", "", false
		}
	}
	from = strings.Trim(cmp.Or(body.From, r.URL.Query().Get("from")), "/")
	to = strings.Trim(cmp.Or(body.To, r.URL.Query().Get("to")), "/")
	return from, to, from != "" && to != "" && from != to
}

// writeConsentsErr answers a consent file edit's failure: 409 for a file
// this xbind can't read (an admin's to fix; /alerts names it), 500 else.
func writeConsentsErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, errConsentsUnreadable) {
		code = http.StatusConflict
	}
	server.WriteError(w, code, err.Error(), consentsDocs)
}

// apiConsentsAdd — {from, to}: the person lets partitioned tile from use
// their data in partitioned tile to. Only while the policy is on; both
// tiles partitioned and readable by them.
func (b *Broker) apiConsentsAdd(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !personOwn(w, p) {
		return
	}
	from, to, ok := consentBody(r)
	switch {
	case !ok:
		server.WriteError(w, http.StatusBadRequest, "need {from, to}: two different tile paths", consentsDocs)
		return
	case strings.Contains(from+to, consentArrow):
		server.WriteError(w, http.StatusBadRequest, "a tile whose path holds "+consentArrow+" can't be named in a consent: rename it", consentsDocs)
		return
	}
	if !b.Policies().PartitionConsent {
		server.WriteError(w, http.StatusConflict, "this workspace doesn't ask people before a partitioned tile uses their data (the partitionConsent policy is off): there is nothing to allow", consentsDocs)
		return
	}
	for _, t := range []string{from, to} {
		_, partitioned, err := b.tilePartitioning(t)
		switch {
		case err != nil:
			server.WriteError(w, http.StatusConflict, err.Error(), consentsDocs)
			return
		case !partitioned:
			if _, exists := b.Reg.Component(t); !exists {
				server.WriteError(w, http.StatusNotFound, "no tile "+t, consentsDocs)
				return
			}
			server.WriteError(w, http.StatusConflict, t+" doesn't keep each person's data apart: there is nothing to allow", consentsDocs)
			return
		}
		if err := b.personLive(p.UserID, t); err != nil {
			server.WriteError(w, http.StatusForbidden, err.Error(), consentsDocs)
			return
		}
	}
	uid, err := b.mintPartitionUID(p.UserID)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error(), consentsDocs)
		return
	}
	if _, err := b.editConsents(p.UserID, uid, func(d *consentDoc) bool {
		d.Edges[consentKey(from, to)] = consentEdge{At: consentNow().UTC(), Via: p.Via}
		return true
	}); err != nil {
		writeConsentsErr(w, err)
		return
	}
	slog.Info("audit", "who", p.From(), "method", "POST", "path", "/partitions/consents", "status", http.StatusOK, "from", from, "to", to)
	b.publishConsents(p.UserID, from, to, true)
	server.WriteJSON(w, http.StatusOK, b.consentsView(p.UserID))
}

// apiConsentsRevoke — {from, to} (or ?from=&to=): the person takes a
// consent back, in either setting. Its effect is at once: the next call
// and reach are refused, and from's instance of the person stops (the
// streams it held into to with it). The answer is the GET view, with
// revoked: whether there was a consent to take back.
func (b *Broker) apiConsentsRevoke(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !personOwn(w, p) {
		return
	}
	from, to, ok := consentBody(r)
	if !ok {
		server.WriteError(w, http.StatusBadRequest, "need {from, to}: two different tile paths", consentsDocs)
		return
	}
	removed := false
	if uid := b.storedPartitionUID(p.UserID); uid != "" {
		if _, err := b.editConsents(p.UserID, uid, func(d *consentDoc) bool {
			_, removed = d.Edges[consentKey(from, to)]
			delete(d.Edges, consentKey(from, to))
			return removed
		}); err != nil {
			writeConsentsErr(w, err)
			return
		}
	}
	if removed {
		slog.Info("audit", "who", p.From(), "method", "DELETE", "path", "/partitions/consents", "status", http.StatusOK, "from", from, "to", to)
		b.stopEdgeCaller(from, p.UserID)
		b.publishConsents(p.UserID, from, to, false)
	}
	view := b.consentsView(p.UserID)
	view["revoked"] = removed
	server.WriteJSON(w, http.StatusOK, view)
}

// stopEdgeCaller stops userID's instance of tile from, when it is
// partitioned, so nothing it holds open outlives a revoked consent.
func (b *Broker) stopEdgeCaller(from, userID string) {
	if _, partitioned, err := b.tilePartitioning(from); err == nil && partitioned {
		b.stopPartitionInstance(from, b.primaryOf(from), string(util.UserPartition(userID))) // SetPartitionInstanceStop (partitionwire.go)
	}
}

// publishConsents tells the person's own sockets their consents changed.
func (b *Broker) publishConsents(userID, from, to string, allowed bool) {
	if b.Hub == nil {
		return
	}
	b.Hub.Publish(events.Event{Type: "partitions", Component: to, Partition: string(util.UserPartition(userID)),
		Data: personEvent{"op": "consent", "from": from, "to": to, "allowed": allowed}})
}

// ---- the switch's wipe; a tile that goes ----

// wipeConsentsHook is the "consents" store's part of a switch that deletes
// everything (01 §2.6): every person's consent naming the tile, as caller
// or callee, goes. Removing or adding global keeps them (they are about
// people's partitions). A dry run removes and counts nothing — consents
// are metadata, not data the summary lists — but checks what the switch
// would: a consent file this xbind can't read that may name the tile fails
// both, so the confirmation says so before anything is deleted. A metadata
// hook (partitionwire.go): it runs after every data store's.
func wipeConsentsHook(b *Broker, t wipeTarget, _ *wipeSummary) error {
	if t.Kind != wipeEverything {
		return nil
	}
	return b.dropConsentsNaming(t.Tile, t.DryRun)
}

// PartitionTileChanged is the consents' partition-change hook (boot:
// Registry.OnPartitionChange): a tile that went — deleted or moved away,
// its mode now the zero one — takes every consent naming it, so a new tile
// later at its path never inherits what people allowed the old one's code.
// A switch to unpartitioned took them already (the wipe hook): nothing is
// left to find then. Consent files are few and small; this runs in the
// scan, which it never asks.
func (b *Broker) PartitionTileChanged(c *registry.Component, _, new registry.PartitionMode) {
	if c == nil || new != (registry.PartitionMode{}) {
		return
	}
	if err := b.dropConsentsNaming(c.Path, false); err != nil {
		slog.Warn("partitions: consents naming a tile that went can't all be removed", "tile", c.Path, "err", err)
	}
}

// dropConsentsNaming removes every consent naming tile, as caller or
// callee, from every person's file (dry: only checks that it could). A
// file this xbind can't read is skipped when its bytes can't name tile —
// an unrelated person's; /alerts shows it to admins — and fails the call
// otherwise: a consent this xbind can't remove would outlive the tile.
func (b *Broker) dropConsentsNaming(tile string, dry bool) error {
	entries, err := os.ReadDir(b.consentsBase()) // walk-ok: data/partitions is xbind's own
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("the consent records can't be listed: %w", err)
	}
	var errs []error
	for _, e := range entries {
		uid, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !users.UIDOK(uid) || !e.Type().IsRegular() {
			continue
		}
		path := b.consentPath(uid)
		doc, err := readConsentDoc(path, uid)
		switch {
		case err != nil:
			if raw, rerr := os.ReadFile(path); rerr == nil && !namesTile(raw, tile) { // walk-ok: data/partitions is xbind's own
				continue
			}
			errs = append(errs, fmt.Errorf("%w (data/partitions/consents/%s.json), and it may name %s: an admin fixes or removes it, then try again", err, uid, tile))
			continue
		case doc == nil || dry:
			continue
		}
		if _, err := b.editConsents(doc.User, uid, func(d *consentDoc) bool {
			n := len(d.Edges)
			for k := range d.Edges {
				if from, to, _ := strings.Cut(k, consentArrow); from == tile || to == tile {
					delete(d.Edges, k)
				}
			}
			return len(d.Edges) != n
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// namesTile reports whether raw — a consent file this xbind can't read —
// may name tile: its path appears whole, not as part of a longer path.
// Conservative: a byte beyond ASCII beside it (the arrow's are) counts as
// a boundary.
func namesTile(raw []byte, tile string) bool {
	t := []byte(tile)
	for i := 0; tile != "" && i < len(raw); {
		j := bytes.Index(raw[i:], t)
		if j < 0 {
			return false
		}
		at, end := i+j, i+j+len(t)
		if (at == 0 || !tilePathByte(raw[at-1])) && (end == len(raw) || !tilePathByte(raw[end])) {
			return true
		}
		i = at + 1
	}
	return false
}

// tilePathByte: an ASCII byte a tile path may hold.
func tilePathByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("/._-+@~", c) >= 0
}

// consentAlerts is the admins' /alerts line while a person's consent file
// can't be read by this xbind: it counts as no consent, its person can't
// consent or revoke (409), and a switch of a tile it may name stops.
func (b *Broker) consentAlerts() []Alert {
	entries, _ := os.ReadDir(b.consentsBase()) // walk-ok: data/partitions is xbind's own
	var bad []string
	for _, e := range entries {
		uid, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !users.UIDOK(uid) {
			continue
		}
		if _, err := b.consentDocOf(uid); err != nil {
			bad = append(bad, uid+".json ("+strings.TrimPrefix(err.Error(), errConsentsUnreadable.Error()+": ")+")")
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return []Alert{{Level: "warn", Kind: "partition-consents", Message: fmt.Sprintf(
		"%d consent record(s) in data/partitions/consents can't be read by this xbind: %s — each counts as no consent, its person can't consent or revoke, "+
			"and switching a tile it may name stops; fix or remove the file by hand (docs/partitions.md)", len(bad), strings.Join(bad, ", "))}}
}

// dropPartitionConsents removes a deleted person's consent file (their uid
// goes with them, so a recreated person never reads it; this frees the
// file). For the users-store delete hook (F7b).
func (b *Broker) dropPartitionConsents(uid string) error {
	if !users.UIDOK(uid) {
		return nil
	}
	cs := b.consents()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.docs, uid)
	if err := os.Remove(b.consentPath(uid)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
