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
// settings survive a downgrade.
//
// GET /workspace-policies: admins and any signed-in person (their session,
// device, terminal or agent session); other tile principals get 403, so
// tile code can't probe the workspace's rules. PUT: admin only; audited
// like every admin write; publishes a `policies` event (no data: every
// socket hears non-bus events, so clients re-read GET).
//
// Readers in xbind take b.Policies(), which re-reads the file only when it
// changed on disk.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
)

// PoliciesSchema is the schema this xbind writes into
// data/workspace-policies.json.
const PoliciesSchema = 1

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
	cur    WorkspacePolicies
	err    error
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

// readPoliciesDoc reads the file as raw keys (nil for a missing file) and
// the switches in it.
func readPoliciesDoc(path string) (map[string]json.RawMessage, WorkspacePolicies, error) {
	var p WorkspacePolicies
	bts, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, p, nil
	}
	if err != nil {
		return nil, p, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(bts, &doc); err != nil {
		return nil, p, fmt.Errorf("%s: %w", path, err)
	}
	if err := json.Unmarshal(bts, &p); err != nil {
		return nil, WorkspacePolicies{}, fmt.Errorf("%s: %w", path, err)
	}
	return doc, p, nil
}

// loadLocked refreshes the cache when the file changed; ps.mu is held.
func (ps *policiesStore) loadLocked(path string) (WorkspacePolicies, error) {
	st, err := stampFile(path)
	if err != nil {
		return WorkspacePolicies{}, err
	}
	if ps.loaded && st == ps.stamp {
		return ps.cur, ps.err
	}
	_, cur, err := readPoliciesDoc(path)
	ps.loaded, ps.stamp, ps.cur, ps.err = true, st, cur, err
	if err != nil { // once per version of the file, not per read
		slog.Warn("workspace policies: unreadable, using the defaults (every switch off)", "err", err)
		ps.cur = WorkspacePolicies{}
	}
	return ps.cur, err
}

// loadPolicies is the current policies, or the error that stops them from
// being read (an unreadable or malformed file).
func (b *Broker) loadPolicies() (WorkspacePolicies, error) {
	b.pol.mu.Lock()
	defer b.pol.mu.Unlock()
	return b.pol.loadLocked(b.policiesPath())
}

// Policies is the workspace's policies for xbind's own checks. A file that
// can't be read counts as every switch off (the defaults); loadLocked logs
// it.
func (b *Broker) Policies() WorkspacePolicies {
	p, _ := b.loadPolicies()
	return p
}

// policiesPatch is a PUT body: each present key replaces that switch, an
// absent one leaves it alone.
type policiesPatch struct {
	PartitionConsent       *bool `json:"partitionConsent"`
	CredentialResetConfirm *bool `json:"credentialResetConfirm"`
}

// setPolicies applies a patch and persists it, keeping every key of the
// file this xbind doesn't know. A file it can't read is not overwritten.
func (b *Broker) setPolicies(patch policiesPatch) (WorkspacePolicies, error) {
	b.pol.mu.Lock()
	defer b.pol.mu.Unlock()
	path := b.policiesPath()
	doc, cur, err := readPoliciesDoc(path)
	if err != nil {
		return WorkspacePolicies{}, err
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
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
	doc["partitionConsent"] = json.RawMessage(fmt.Sprint(cur.PartitionConsent))
	doc["credentialResetConfirm"] = json.RawMessage(fmt.Sprint(cur.CredentialResetConfirm))
	bts, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return WorkspacePolicies{}, err
	}
	if err := fsutil.WriteFileAtomicIn(path, append(bts, '\n'), 0o644); err != nil {
		return WorkspacePolicies{}, err
	}
	b.pol.loaded = false // the next read re-stamps
	return cur, nil
}

func (b *Broker) registerPolicies(srv *server.Server) {
	srv.RegisterAPI("GET /workspace-policies", b.apiPoliciesGet)
	srv.RegisterAPI("PUT /workspace-policies", b.apiPoliciesPut)
}

// canReadPolicies: admins (the admin tile through xbin:admin included), and
// a person through their own session or device, or a terminal or agent
// session they drive. Other tile principals — frames, instances, cron and
// bus deliveries — are refused.
func (b *Broker) canReadPolicies(p auth.Principal) bool {
	if b.IsAdmin(p) {
		return true
	}
	if p.UserID == "" {
		return false
	}
	return p.Component == "" || p.Via == "terminal" || p.Via == "agent"
}

func (b *Broker) apiPoliciesGet(w http.ResponseWriter, r *http.Request) {
	if !b.canReadPolicies(auth.PrincipalOf(r)) {
		server.WriteError(w, http.StatusForbidden, "the workspace policies are for people and admins, not tile code", "/docs/protocol.md")
		return
	}
	p, err := b.loadPolicies()
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, policiesView(p))
}

// apiPoliciesPut — {partitionConsent?, credentialResetConfirm?}: admin only.
func (b *Broker) apiPoliciesPut(w http.ResponseWriter, r *http.Request) {
	if !b.IsAdmin(auth.PrincipalOf(r)) {
		server.WriteError(w, http.StatusForbidden, "admin only — needs the xbin:admin capability", "/docs/auth.md")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var patch policiesPatch
	if err := server.DecodeJSON(r, &patch); err != nil || patch == (policiesPatch{}) {
		server.WriteError(w, http.StatusBadRequest, "need {partitionConsent?: bool, credentialResetConfirm?: bool} with at least one key", "/docs/protocol.md")
		return
	}
	p, err := b.setPolicies(patch)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b.Hub != nil {
		b.Hub.Publish(events.Event{Type: "policies"})
	}
	server.WriteJSON(w, http.StatusOK, policiesView(p))
}
