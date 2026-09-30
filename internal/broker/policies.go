package broker

// Workspace policies (PD-55; plans/partitions 05 §2, 06 §9, 06 §12.3): the
// workspace-wide switches for partitioned tiles that an admin sets from the
// admin tile's workspace → policies tab or `bx policies`.
//
//   - partitionConsent: a person's consent is needed before another
//     partitioned tile uses their data (the cross-tile edge rule, 05 §2).
//   - credentialResetConfirm: a sign-in link, password or SSO email an admin
//     sets for someone who holds partitions waits for them (06 §9).
//
// Both are off by default. The document is xbind-owned JSON in
// data/workspace-policies.json, `{"schema": 1, "partitionConsent": false,
// "credentialResetConfirm": false}`. It is its own file, not a users.json
// key, because an older xbind rewriting the users store would drop a key it
// does not know (the precedent is data/branding.json). For the same reason a
// rewrite here keeps every key this xbind does not know: a newer xbind's
// settings survive a downgrade. Each switch is read by its exact key.
//
// Both switches are protections, so a file that can't be read never turns
// one off: a switch whose value can't be read (the file unreadable or not a
// JSON object, a value not true or false, a mis-cased key) keeps the last
// value this xbind read, or counts as on when it has read none. Admins see
// the problem on /alerts; GET and PUT answer 500 until the file is fixed by
// hand, and PUT never overwrites it.
//
// GET /workspace-policies: admins and any signed-in person (their session or
// device, or a terminal or agent session they drive); other tile principals
// get 403. PUT: admin only; audited with the values it changed; publishes a
// `policies` event (no data: every socket hears non-bus events, so clients
// re-read GET).
//
// Readers in xbind take b.Policies(), which re-reads the file only when it
// changed on disk.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
)

// PoliciesSchema is the schema this xbind writes into
// data/workspace-policies.json.
const PoliciesSchema = 1

// policiesRel names the file in messages: the workspace-relative path, never
// the host's.
const policiesRel = "data/workspace-policies.json"

// WorkspacePolicies are the workspace's switches. The zero value is the
// default: everything off.
type WorkspacePolicies struct {
	// PartitionConsent: another partitioned tile reaches a person's data
	// only with that person's consent (05 §2).
	PartitionConsent bool `json:"partitionConsent"`
	// CredentialResetConfirm: an admin-set credential for a person holding
	// partitions takes effect only after they confirm, or 24 h after they
	// were notified (06 §9).
	CredentialResetConfirm bool `json:"credentialResetConfirm"`
}

// policySwitches are the switches by their exact key in the file (encoding/
// json would match a struct field in any case).
var policySwitches = []struct {
	key string
	at  func(*WorkspacePolicies) *bool
}{
	{"partitionConsent", func(p *WorkspacePolicies) *bool { return &p.PartitionConsent }},
	{"credentialResetConfirm", func(p *WorkspacePolicies) *bool { return &p.CredentialResetConfirm }},
}

// policiesView is the wire shape of GET and PUT: the file's own shape.
func policiesView(p WorkspacePolicies) map[string]any {
	return map[string]any{
		"schema":                 PoliciesSchema,
		"partitionConsent":       p.PartitionConsent,
		"credentialResetConfirm": p.CredentialResetConfirm,
	}
}

// policiesStore caches the file. Its zero value is ready: it loads on
// first use.
type policiesStore struct {
	mu     sync.Mutex
	loaded bool
	stamp  fileStamp
	cur    WorkspacePolicies // what xbind enforces
	err    error             // why the file, or a switch in it, can't be read
	last   map[string]bool   // each switch's last value read from the file
}

// fileStamp is what tells a changed file (a restore, a hand edit) apart
// from the one the cache holds.
type fileStamp struct {
	exists bool
	mod    int64 // mtime, ns
	size   int64
}

func stampFile(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileStamp{}, nil
	}
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{exists: true, mod: fi.ModTime().UnixNano(), size: fi.Size()}, nil
}

func (b *Broker) policiesPath() string {
	return filepath.Join(b.Reg.Root, "data", "workspace-policies.json")
}

// policiesDoc is one read of the file.
type policiesDoc struct {
	raw  map[string]json.RawMessage // every key (nil: no file, or it can't be read)
	vals WorkspacePolicies          // the switches read
	bad  map[string]string          // switch key → why its value can't be read
	err  error                      // the whole file can't be read: no switch is
}

// problem is why some switch can't be read, or nil.
func (d policiesDoc) problem() error {
	if d.err != nil {
		return d.err
	}
	for _, sw := range policySwitches {
		if why, ok := d.bad[sw.key]; ok {
			return fmt.Errorf("%s: %s", policiesRel, why)
		}
	}
	return nil
}

// hostless drops the host path an *fs.PathError carries.
func hostless(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}

// readPoliciesDoc reads the file (a missing one is every switch off).
func readPoliciesDoc(path string) policiesDoc {
	var d policiesDoc
	bts, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d
	}
	if err != nil {
		d.err = fmt.Errorf("%s: %w", policiesRel, hostless(err))
		return d
	}
	if err := json.Unmarshal(bts, &d.raw); err != nil || d.raw == nil {
		if err == nil {
			err = errors.New("not a JSON object")
		}
		d.raw, d.err = nil, fmt.Errorf("%s: %w", policiesRel, err)
		return d
	}
	why := func(key, reason string) {
		if d.bad == nil {
			d.bad = map[string]string{}
		}
		d.bad[key] = reason
	}
	for _, sw := range policySwitches {
		for k := range d.raw {
			if k != sw.key && strings.EqualFold(k, sw.key) {
				why(sw.key, fmt.Sprintf("the key %q should be spelled %q", k, sw.key))
			}
		}
		v, ok := d.raw[sw.key]
		switch v = bytes.TrimSpace(v); {
		case !ok:
		case string(v) == "true" || string(v) == "false":
			*sw.at(&d.vals) = string(v) == "true"
		default:
			if len(v) > 40 {
				v = append(v[:40:40], "…"...)
			}
			why(sw.key, fmt.Sprintf("%s is %s, not true or false", sw.key, v))
		}
	}
	return d
}

// resolve is what xbind enforces after a read: each switch as the file says,
// or, when its value can't be read, its last value read, or on (fail closed:
// a broken file never quietly turns a protection off).
func (ps *policiesStore) resolve(d policiesDoc) WorkspacePolicies {
	if ps.last == nil {
		ps.last = map[string]bool{}
	}
	var out WorkspacePolicies
	for _, sw := range policySwitches {
		if _, bad := d.bad[sw.key]; d.err != nil || bad {
			last, seen := ps.last[sw.key]
			*sw.at(&out) = !seen || last
			continue
		}
		v := *sw.at(&d.vals)
		ps.last[sw.key] = v
		*sw.at(&out) = v
	}
	return out
}

// loadLocked refreshes the cache when the file changed; ps.mu is held.
func (ps *policiesStore) loadLocked(path string) (WorkspacePolicies, error) {
	st, serr := stampFile(path)
	if serr == nil && ps.loaded && st == ps.stamp {
		return ps.cur, ps.err
	}
	var d policiesDoc
	if serr != nil {
		d.err = fmt.Errorf("%s: %w", policiesRel, hostless(serr))
	} else {
		d = readPoliciesDoc(path)
	}
	cur, err := ps.resolve(d), d.problem()
	// once per version of the file (a file it can't stat: per new error)
	if err != nil && (serr == nil || ps.err == nil || ps.err.Error() != err.Error()) {
		slog.Warn("workspace policies: can't read a switch; it keeps its last value, or is on", "path", path, "err", err,
			"partitionConsent", cur.PartitionConsent, "credentialResetConfirm", cur.CredentialResetConfirm)
	}
	ps.loaded, ps.stamp, ps.cur, ps.err = serr == nil, st, cur, err
	return cur, err
}

// loadPolicies is what xbind enforces, and why the file (or a switch in it)
// can't be read, if it can't.
func (b *Broker) loadPolicies() (WorkspacePolicies, error) {
	b.pol.mu.Lock()
	defer b.pol.mu.Unlock()
	return b.pol.loadLocked(b.policiesPath())
}

// Policies is the workspace's policies for xbind's own checks. A switch the
// file doesn't let it read keeps its last value, or is on; loadLocked logs
// it and /alerts shows it to admins.
func (b *Broker) Policies() WorkspacePolicies {
	p, _ := b.loadPolicies()
	return p
}

// policiesAlert is the admins' /alerts line while the file can't be read.
func (b *Broker) policiesAlert() (Alert, bool) {
	_, err := b.loadPolicies()
	if err == nil {
		return Alert{}, false
	}
	return Alert{Level: "crit", Kind: "policies", Message: err.Error() +
		" — fix or remove the file by hand; until then each switch in it that can't be read keeps its last value, or is on"}, true
}

// policiesPatch is a PUT body: each present key replaces that switch, an
// absent one leaves it alone.
type policiesPatch struct {
	PartitionConsent       *bool `json:"partitionConsent"`
	CredentialResetConfirm *bool `json:"credentialResetConfirm"`
}

// setPolicies applies a patch and persists it, keeping every key of the
// file this xbind doesn't know; it returns the switches before and after. A
// file with a switch it can't read is not overwritten.
func (b *Broker) setPolicies(patch policiesPatch) (old, cur WorkspacePolicies, err error) {
	b.pol.mu.Lock()
	defer b.pol.mu.Unlock()
	path := b.policiesPath()
	d := readPoliciesDoc(path)
	if err := d.problem(); err != nil {
		return old, cur, fmt.Errorf("%w — not overwritten; fix or remove it by hand", err)
	}
	doc := d.raw
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	old, cur = d.vals, d.vals
	if patch.PartitionConsent != nil {
		cur.PartitionConsent = *patch.PartitionConsent
	}
	if patch.CredentialResetConfirm != nil {
		cur.CredentialResetConfirm = *patch.CredentialResetConfirm
	}
	var schema int
	if json.Unmarshal(doc["schema"], &schema) != nil || schema < PoliciesSchema {
		doc["schema"] = json.RawMessage(fmt.Sprint(PoliciesSchema)) // a newer xbind's number stays
	}
	for _, sw := range policySwitches {
		doc[sw.key] = json.RawMessage(fmt.Sprint(*sw.at(&cur)))
	}
	bts, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return old, cur, err
	}
	if err := fsutil.WriteFileAtomicIn(path, append(bts, '\n'), 0o644); err != nil {
		slog.Warn("workspace policies: can't save", "path", path, "err", err)
		return old, cur, fmt.Errorf("can't save %s: %w", policiesRel, hostless(err))
	}
	b.pol.loaded = false // the next read re-stamps
	return old, cur, nil
}

func (b *Broker) registerPolicies(srv *server.Server) {
	srv.RegisterAPI("GET /workspace-policies", b.apiPoliciesGet)
	srv.RegisterAPI("PUT /workspace-policies", b.apiPoliciesPut)
	b.registerPartitionMode(srv)     // POST /partitions/mode: keep or switch (partitionswitch.go)
	b.registerPartitionConsents(srv) // consents, the ledger, the edges (partitionconsent.go)
	b.registerPartitionOps(srv)      // the listing, stop/reset/purge, log shares, credential confirmations (partitionops.go)
}

// canReadPolicies: admins (the admin tile through xbin:admin included), and
// a person through their own session or device, or a terminal or agent
// session they drive (both carry a terminal token: Via "terminal", the
// person in UserID) — `bx policies` runs in one. Other tile principals —
// frames, instances, cron and bus deliveries — are refused.
func (b *Broker) canReadPolicies(p auth.Principal) bool {
	if b.IsAdmin(p) {
		return true
	}
	if p.UserID == "" {
		return false
	}
	return p.Component == "" || p.Via == "terminal"
}

func (b *Broker) apiPoliciesGet(w http.ResponseWriter, r *http.Request) {
	pr := auth.PrincipalOf(r)
	if !b.canReadPolicies(pr) {
		server.WriteError(w, http.StatusForbidden, "the workspace policies are for people and admins, not tile code", "/docs/protocol.md")
		return
	}
	p, err := b.loadPolicies()
	if err != nil {
		msg := "the workspace policies can't be read; an admin must fix " + policiesRel
		if b.IsAdmin(pr) {
			msg = err.Error() + " — fix or remove the file by hand"
		}
		server.WriteError(w, http.StatusInternalServerError, msg, "/docs/protocol.md")
		return
	}
	server.WriteJSON(w, http.StatusOK, policiesView(p))
}

// apiPoliciesPut — {partitionConsent?, credentialResetConfirm?}: admin only
// (Broker.IsAdmin, as for every governance write: the admin tile's frame
// with xbin:admin is how the console reaches it, 06 §12.3).
func (b *Broker) apiPoliciesPut(w http.ResponseWriter, r *http.Request) {
	pr := auth.PrincipalOf(r)
	if !b.IsAdmin(pr) {
		server.WriteError(w, http.StatusForbidden, "admin only — needs the xbin:admin capability", "/docs/auth.md")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var patch policiesPatch
	if err := server.DecodeJSON(r, &patch); err != nil || patch == (policiesPatch{}) {
		server.WriteError(w, http.StatusBadRequest, "need {partitionConsent?: bool, credentialResetConfirm?: bool} with at least one key", "/docs/protocol.md")
		return
	}
	old, p, err := b.setPolicies(patch)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error(), "/docs/protocol.md")
		return
	}
	// The generic audit line names who and the status; this one what changed.
	args := []any{"who", pr.From(), "method", "PUT", "path", "/workspace-policies", "status", http.StatusOK}
	if pr.Component != "" && pr.UserID != "" {
		args = append(args, "user", pr.UserID)
	}
	for _, sw := range policySwitches {
		args = append(args, sw.key, fmt.Sprintf("%t→%t", *sw.at(&old), *sw.at(&p)))
	}
	slog.Info("audit", args...)
	if b.Hub != nil {
		b.Hub.Publish(events.Event{Type: "policies"})
	}
	server.WriteJSON(w, http.StatusOK, policiesView(p))
}
