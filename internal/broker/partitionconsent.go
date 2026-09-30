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
//   - addressedPartition asks partitionConsentHolds (filled here) at every
//     call and reach, so a revocation holds from the next call. No volume of
//     another scope is ever bound into a partition (EnvFor hands no
//     cross-scope file resource), so nothing a running instance holds needs
//     dropping — but its open streams into X do: a revocation stops Z's
//     instance of A (it starts again on the next request);
//   - a refused edge prompts A — a `partitions` event, op consent-needed, to
//     A's sockets and a push linking to /xbin/partitions — at most once a
//     day per edge, and only when Z holds a grant on X (tile code with no
//     grant can't make people consent ahead of an admin's approval).
//
// The approval warning (S1) is shown in both settings, beside a pending
// grant of a partitioned tile on another partitioned tile's data (GET
// /grants' pending rows, bx grants, bx grant, the grants element).

import (
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

func init() {
	partitionConsentHolds = func(b *Broker, userID, from, to string) bool {
		if b.consentHolds(userID, from, to) {
			return true
		}
		b.consentNeeded(userID, from, to)
		return false
	}
	registerWipeHook(wipeHook{name: "consents", wipe: wipeConsentsHook})
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

// consentKey is an edge's key in the file: "<from>→<to>" (no tile path
// holds the arrow).
func consentKey(from, to string) string { return from + "→" + to }

// consentState is one broker's consent plane: the files read (by uid, with
// the stamp they were read at), the prompts sent, and the runner's stop
// of one partition. Kept beside the Broker, which stays as it is.
type consentState struct {
	mu    sync.Mutex
	docs  map[string]consentCached
	asked map[string]time.Time // user\x00from\x00to → the last prompt
	stop  func(tile, dep, part string)
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

// SetPartitionEdgeStop installs the runner's stop of one partition instance
// (runner.StopPartition): a consent's revocation stops the caller tile's
// instance of that person, dropping the streams it holds into the other
// tile (boot).
func (b *Broker) SetPartitionEdgeStop(stop func(tile, dep, part string)) {
	cs := b.consents()
	cs.mu.Lock()
	cs.stop = stop
	cs.mu.Unlock()
}

func (b *Broker) consentsBase() string {
	return filepath.Join(b.Reg.Root, "data", partitionsDir, consentsDir)
}

func (b *Broker) consentPath(uid string) string {
	return filepath.Join(b.consentsBase(), uid+".json")
}

// readConsentDoc reads uid's file: nil without one. A file this xbind
// can't read (it doesn't parse, another schema, another uid) is an error:
// no consent, and never written over.
func readConsentDoc(path, uid string) (*consentDoc, error) {
	raw, err := os.ReadFile(path) // walk-ok: data/partitions is xbind's own; no sandbox sees it
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d consentDoc
	switch {
	case json.Unmarshal(raw, &d) != nil:
		return nil, errors.New("it doesn't parse")
	case d.Schema != consentSchema:
		return nil, fmt.Errorf("schema %d, this xbind reads %d", d.Schema, consentSchema)
	case d.UID != uid:
		return nil, errors.New("it names another uid than its file")
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
	if uid == "" {
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
		return nil, fmt.Errorf("the consent record can't be read by this xbind (%v): it is kept as it is", err)
	}
	if doc == nil {
		doc = &consentDoc{Schema: consentSchema, User: userID, UID: uid, Edges: map[string]consentEdge{}}
	}
	if doc.User != userID {
		return nil, errors.New("the consent record names another person: it is kept as it is")
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

// promptConsent sends one consent-needed prompt.
func (b *Broker) promptConsent(userID, from, to string) {
	if b.Hub != nil {
		b.Hub.Publish(events.Event{Type: "partitions", Component: to, Partition: string(util.UserPartition(userID)),
			Data: map[string]any{"op": "consent-needed", "from": from, "to": to}})
	}
	b.pushPerson(userID, "tile.partition-consent", from+" asks for your "+to+" data",
		fmt.Sprintf("%s wants to use your data in %s. Your workspace asks you first: allow or ignore it on the partitions page (bx partition consent %s %s).", from, to, from, to),
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
				from, to, _ := strings.Cut(k, "→")
				rows = append(rows, map[string]any{"from": from, "to": to, "at": e.At.UTC(), "via": e.Via})
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

// apiConsentsAdd — {from, to}: the person lets partitioned tile from use
// their data in partitioned tile to. Only while the policy is on; both
// tiles partitioned and readable by them.
func (b *Broker) apiConsentsAdd(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !personOwn(w, p) {
		return
	}
	from, to, ok := consentBody(r)
	if !ok {
		server.WriteError(w, http.StatusBadRequest, "need {from, to}: two different tile paths", consentsDocs)
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
		server.WriteError(w, http.StatusInternalServerError, err.Error(), consentsDocs)
		return
	}
	slog.Info("audit", "who", p.From(), "method", "POST", "path", "/partitions/consents", "status", http.StatusOK, "from", from, "to", to)
	b.publishConsents(p.UserID, from, to, true)
	server.WriteJSON(w, http.StatusOK, b.consentsView(p.UserID))
}

// apiConsentsRevoke — {from, to} (or ?from=&to=): the person takes a
// consent back, in either setting. Its effect is at once: the next call
// and reach are refused, and from's instance of the person stops (its open
// streams into to with it).
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
			server.WriteError(w, http.StatusInternalServerError, err.Error(), consentsDocs)
			return
		}
	}
	if removed {
		slog.Info("audit", "who", p.From(), "method", "DELETE", "path", "/partitions/consents", "status", http.StatusOK, "from", from, "to", to)
		b.stopEdgeCaller(from, p.UserID)
		b.publishConsents(p.UserID, from, to, false)
	}
	server.WriteJSON(w, http.StatusOK, b.consentsView(p.UserID))
}

// stopEdgeCaller stops userID's instance of tile from, when it is
// partitioned, so nothing it holds open outlives a revoked consent.
func (b *Broker) stopEdgeCaller(from, userID string) {
	cs := b.consents()
	cs.mu.Lock()
	stop := cs.stop
	cs.mu.Unlock()
	if _, partitioned, err := b.tilePartitioning(from); stop != nil && err == nil && partitioned {
		stop(from, b.primaryOf(from), string(util.UserPartition(userID)))
	}
}

// publishConsents tells the person's own sockets their consents changed.
func (b *Broker) publishConsents(userID, from, to string, allowed bool) {
	if b.Hub == nil {
		return
	}
	b.Hub.Publish(events.Event{Type: "partitions", Component: to, Partition: string(util.UserPartition(userID)),
		Data: map[string]any{"op": "consent", "from": from, "to": to, "allowed": allowed}})
}

// ---- the switch's wipe ----

// wipeConsentsHook is the "consents" store's part of a switch that deletes
// everything (01 §2.6): every person's consent naming the tile, as caller
// or callee, goes. Removing or adding global keeps them (they are about
// people's partitions). A dry run counts nothing: consents are metadata,
// not data the summary lists.
func wipeConsentsHook(b *Broker, t wipeTarget, _ *wipeSummary) error {
	if t.Kind != wipeEverything || t.DryRun {
		return nil
	}
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
		doc, err := readConsentDoc(b.consentPath(uid), uid)
		if err != nil || doc == nil {
			if err != nil {
				errs = append(errs, fmt.Errorf("consents of %s: %w", uid, err))
			}
			continue
		}
		if _, err := b.editConsents(doc.User, uid, func(d *consentDoc) bool {
			n := len(d.Edges)
			for k := range d.Edges {
				if from, to, _ := strings.Cut(k, "→"); from == t.Tile || to == t.Tile {
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
