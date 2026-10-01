// harness_creds.go — saved sign-ins for coding agents (D179; API.md "Coding
// agents" → "Saved sign-ins"): a person's own credentials for a coding
// agent's CLI, named per harness ("Personal", "Work"), one of them the
// harness's default, kept in their partition's vault and handed to the CLI
// in its environment — never copied between sandboxes' $HOMEs (a refresh
// token is single-use: copies log each other out), never written into one.
//
//   - Where: only in a person's own partition (user mode), whose vault is
//     theirs alone. The global instance answers 409 (A-M1's rule: no
//     person's credentials there); an unpartitioned agent has none (its one
//     vault is everybody's) and keeps the per-sandbox sign-in only.
//   - What: a token `claude setup-token` minted through the guided sign-in
//     (harness_guided.go: the backend scrapes it, it never reaches a page),
//     or a key or token the person pastes (POST /prefs/harness-signins):
//     acp.Provider.Keys say which variable it goes in.
//   - The secret: in the vault (signinVault, key harness-signin.<id>) and in
//     the adapter's exec request, nowhere else — no row, log line, answer or
//     event holds it. harness_signins keeps the rest (name, kind, variable,
//     when minted, when it expires, when refused, default).
//   - The gate (credWhy): spawnHarness puts it in the adapter's environment
//     only for the person's own conversation, in a sandbox that is theirs
//     and no one else's (not shared: its co-users could read the process's
//     environment), in their partition, and not refused. It wins over the
//     sandbox's own $HOME login (Claude Code: CLAUDE_CODE_OAUTH_TOKEN
//     outranks /login). harness_sessions.cred says which one the current
//     generation got.
//   - Its life: a warning from 14 days before it expires; an auth failure
//     while it is in use marks it refused (the run parks on its sign-in,
//     saying so) and the gate leaves it out until it is replaced; Forget
//     deletes it and stops the coding agents using it; a sandbox shared
//     mid-run stops the ones it went into; the partition's purge (a
//     person's removal) takes the vault with it.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/acp"
)

const harnessSigninSQL = `
CREATE TABLE IF NOT EXISTS harness_signins (
  id TEXT PRIMARY KEY,
  user TEXT NOT NULL,
  harness TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'api-key',
  env TEXT NOT NULL DEFAULT '',
  minted_ms INTEGER NOT NULL DEFAULT 0,
  expires_ms INTEGER NOT NULL DEFAULT 0,
  refused_ms INTEGER NOT NULL DEFAULT 0,
  refused_why TEXT NOT NULL DEFAULT '',
  is_default INTEGER NOT NULL DEFAULT 0,
  created_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_harness_signins_user ON harness_signins(user, harness);
`

// A conversation's pick (HarnessConfig.Signin) besides a saved sign-in's id.
const (
	pickDefault = ""  // the person's default for the harness
	pickSandbox = "-" // the sandbox's own sign-in ($HOME)
)

const (
	// signinWarn is how long before a saved sign-in expires it says so.
	signinWarn = 14 * 24 * time.Hour
	// setupTokenLife is a `claude setup-token` token's: one year.
	setupTokenLife = 365 * 24 * time.Hour
	// signinMax bounds a pasted secret; signinNameMax a name (runes).
	signinMax     = 8 << 10
	signinNameMax = 40
)

// Why saved sign-ins aren't here.
const (
	signinsAtGlobal = "saved sign-ins live only in a person's own space — the shared space holds no one's credentials"
	signinsLegacy   = "saved sign-ins need a partitioned agent, where each person has a space of their own: this agent is one " +
		"instance for everyone — sign each sandbox in on its own"
	credNotOwnBox = "a saved sign-in goes only into a sandbox of your own that no one else uses"
	credNotOwnRun = "a saved sign-in goes only into your own conversations"
)

// signinsWhy is "" where saved sign-ins exist — a person's own partition —
// else why not.
func signinsWhy() string {
	switch {
	case globalMode():
		return signinsAtGlobal
	case !userMode():
		return signinsLegacy
	}
	return ""
}

// hSignin is one saved sign-in as GET /prefs/harness-signins lists it: no
// secret.
type hSignin struct {
	ID        string `json:"id"`
	Harness   string `json:"harness"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // setup-token | api-key
	Env       string `json:"env"`  // the variable the CLI reads it from
	MintedAt  int64  `json:"mintedAt,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	RefusedAt int64  `json:"refusedAt,omitempty"`
	Refused   string `json:"refused,omitempty"` // what said so
	IsDefault bool   `json:"isDefault"`
	Expiring  bool   `json:"expiring,omitempty"` // within signinWarn of ExpiresAt, or past it
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	user      string
}

func (s *hSignin) view(now time.Time) *hSignin {
	v := *s
	v.Expiring = v.ExpiresAt > 0 && now.Add(signinWarn).UnixMilli() >= v.ExpiresAt
	return &v
}

// usable: not refused, not past its expiry.
func (s *hSignin) usable(now time.Time) bool {
	return s != nil && s.RefusedAt == 0 && (s.ExpiresAt == 0 || now.UnixMilli() < s.ExpiresAt)
}

const signinCols = `id, user, harness, name, kind, env, minted_ms, expires_ms, refused_ms, refused_why, is_default, created_ms, updated_ms`

func scanSignin(sc interface{ Scan(...any) error }) (*hSignin, error) {
	s := &hSignin{}
	var def int
	err := sc.Scan(&s.ID, &s.user, &s.Harness, &s.Name, &s.Kind, &s.Env, &s.MintedAt, &s.ExpiresAt, &s.RefusedAt, &s.Refused,
		&def, &s.CreatedAt, &s.UpdatedAt)
	s.IsDefault = def != 0
	return s, err
}

// signins are user's saved sign-ins (harness "": every harness's), by
// harness, the default first, then by name.
func (d *DB) signins(user, harness string) []*hSignin {
	q := `SELECT ` + signinCols + ` FROM harness_signins WHERE user=?`
	args := []any{user}
	if harness != "" {
		q += ` AND harness=?`
		args = append(args, harness)
	}
	rows, err := d.q.Query(q+` ORDER BY harness, is_default DESC, name COLLATE NOCASE, id`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*hSignin
	for rows.Next() {
		if s, err := scanSignin(rows); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// signin is saved sign-in id (nil: none).
func (d *DB) signin(id string) *hSignin {
	if id == "" {
		return nil
	}
	s, err := scanSignin(d.q.QueryRow(`SELECT `+signinCols+` FROM harness_signins WHERE id=?`, id))
	if err != nil {
		return nil
	}
	return s
}

// signinNamed is user's saved sign-in for harness named name (any case).
func (d *DB) signinNamed(user, harness, name string) *hSignin {
	for _, s := range d.signins(user, harness) {
		if strings.EqualFold(s.Name, name) {
			return s
		}
	}
	return nil
}

func (d *DB) putSignin(s *hSignin) error {
	ms := nowMs()
	if s.CreatedAt == 0 {
		s.CreatedAt = ms
	}
	s.UpdatedAt = ms
	_, err := d.q.Exec(`INSERT INTO harness_signins (`+signinCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, env=excluded.env, minted_ms=excluded.minted_ms,
		  expires_ms=excluded.expires_ms, refused_ms=excluded.refused_ms, refused_why=excluded.refused_why,
		  is_default=excluded.is_default, updated_ms=excluded.updated_ms`,
		s.ID, s.user, s.Harness, s.Name, s.Kind, s.Env, s.MintedAt, s.ExpiresAt, s.RefusedAt, s.Refused, b2i(s.IsDefault), s.CreatedAt, s.UpdatedAt)
	return err
}

// setDefaultSignin makes id user's default for harness (id "": none).
func (d *DB) setDefaultSignin(user, harness, id string) error {
	_, err := d.q.Exec(`UPDATE harness_signins SET is_default=CASE WHEN id=? THEN 1 ELSE 0 END, updated_ms=? WHERE user=? AND harness=?`,
		id, nowMs(), user, harness)
	return err
}

func (d *DB) dropSignin(id string) error {
	_, err := d.q.Exec(`DELETE FROM harness_signins WHERE id=?`, id)
	return err
}

// refuseSigninTx marks id refused (once): the gate leaves it out from now.
func (d *DB) refuseSigninTx(id, why string) (*hSignin, bool) {
	s := d.signin(id)
	if s == nil || s.RefusedAt != 0 {
		return s, false
	}
	s.RefusedAt, s.Refused = nowMs(), clip(why, 300)
	return s, d.putSignin(s) == nil
}

// --- the vault ---------------------------------------------------------------------------

// signinSecrets is where saved sign-ins' secrets live: the partition's own
// vault (the SDK's Secret/SetSecret/DeleteSecret reach the calling
// partition's — docs/partitions.md §Vault and registrations). Tests replace
// it.
type signinSecrets interface {
	get(key string) (string, error)
	set(key, value string) error
	del(key string) error
}

type sdkSecrets struct{}

func (sdkSecrets) get(k string) (string, error) { return xbin.Secret(k) }
func (sdkSecrets) set(k, v string) error        { return xbin.SetSecret(k, v) }
func (sdkSecrets) del(k string) error           { return xbin.DeleteSecret(k) }

var signinVault signinSecrets = sdkSecrets{}

// signinKey is saved sign-in id's vault key.
func signinKey(id string) string { return "harness-signin." + id }

func newSigninID() string {
	var b [9]byte
	_, _ = rand.Read(b[:])
	return "hs" + hex.EncodeToString(b[:])
}

// --- what a harness takes --------------------------------------------------------------

// signinProvider is what the agent knows of harness id's sign-ins: the
// catalog's (the fake signs in as Claude Code: harnessProvider).
func signinProvider(id string) acp.Provider { return harnessProvider(id, nil) }

// cleanSecret is a pasted secret as the CLI reads it: one line, nothing but
// what a key is made of ("" when it holds anything else).
func cleanSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > signinMax {
		return ""
	}
	for _, r := range v {
		if r <= ' ' || r == 0x7f || r >= utf8.RuneSelf {
			return ""
		}
	}
	return v
}

// cleanName is a saved sign-in's name, trimmed ("" when unfit).
func cleanName(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if utf8.RuneCountInString(v) > signinNameMax || strings.ContainsFunc(v, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return ""
	}
	return v
}

// nextSigninName is the name a sign-in saved without one gets: "Personal"
// for the harness's first, else "Sign-in N".
func (d *DB) nextSigninName(user, harness string) string {
	have := d.signins(user, harness)
	if len(have) == 0 {
		return "Personal"
	}
	for n := len(have) + 1; ; n++ {
		if name := fmt.Sprintf("Sign-in %d", n); d.signinNamed(user, harness, name) == nil {
			return name
		}
	}
}

// saveSignin keeps secret as user's saved sign-in for harness named name:
// replacing the secret of the one of that name (its id, default and the
// conversations that picked it stay; a refusal is cleared), else a new one
// — the harness's default when it is the first. The vault first: a row
// never names a secret that isn't there.
func (d *DB) saveSignin(user, harness, name string, key acp.Key, secret string, minted time.Time) (*hSignin, error) {
	s := d.signinNamed(user, harness, name)
	if s == nil {
		s = &hSignin{ID: newSigninID(), user: user, Harness: harness, Name: name, IsDefault: len(d.signins(user, harness)) == 0}
	}
	s.Kind, s.Env, s.RefusedAt, s.Refused, s.MintedAt, s.ExpiresAt = key.Kind, key.Env, 0, "", 0, 0
	if !minted.IsZero() {
		s.MintedAt, s.ExpiresAt = minted.UnixMilli(), minted.Add(setupTokenLife).UnixMilli()
	}
	if err := signinVault.set(signinKey(s.ID), secret); err != nil {
		return nil, fmt.Errorf("keeping it in your vault: %w", err)
	}
	if err := d.putSignin(s); err != nil {
		return nil, err
	}
	return s, nil
}

// --- the gate ----------------------------------------------------------------------------

// credWhy is why no saved sign-in may go into run's coding agent in sandbox
// box ("" = one may): only in a person's own partition, for their own
// conversation (no hosted one: harnessBarred), in a sandbox that is theirs
// and no one else's — its co-users could read the adapter's environment.
func credWhy(run *Run, box *sbxSandbox) string {
	if why := signinsWhy(); why != "" {
		return why
	}
	if run == nil {
		return credNotOwnRun
	}
	if why := harnessBarred(run); why != "" {
		return why
	}
	if root := rootRunOf(run); root.Owner != runUser {
		return credNotOwnRun
	}
	if box == nil || sandboxShared(box) || box.Owner.User != runUser {
		return credNotOwnBox
	}
	return ""
}

// credPick is the saved sign-in run's coding agent starts with (nil: the
// sandbox's own sign-in) — the conversation's pick, else the person's
// default for the harness — and, when there is one it can't use, why.
func (d *DB) credPick(run *Run, cfg Config, box *sbxSandbox) (*hSignin, string) {
	h := cfg.Harness
	if h == nil || h.Signin == pickSandbox || !userMode() {
		return nil, ""
	}
	var s *hSignin
	if h.Signin != pickDefault {
		if s = d.signin(h.Signin); s != nil && (s.user != runUser || s.Harness != h.Provider) {
			s = nil
		}
	}
	if s == nil { // the default (a pick forgotten since falls back to it)
		for _, x := range d.signins(runUser, h.Provider) {
			if x.IsDefault {
				s = x
			}
		}
	}
	switch {
	case s == nil:
		return nil, ""
	case !s.usable(time.Now()):
		why := "it was refused — sign in again"
		if s.RefusedAt == 0 {
			why = "it has expired — sign in again"
		}
		return nil, fmt.Sprintf("your saved sign-in %s isn't used: %s", s.Name, why)
	}
	if why := credWhy(run, box); why != "" {
		return nil, fmt.Sprintf("your saved sign-in %s isn't used: %s", s.Name, why)
	}
	return s, ""
}

// credEnv is the environment the adapter starts with: the provider's, plus
// the saved sign-in's secret when there is one (a fresh map — the
// catalog's is shared).
func credEnv(base map[string]string, cred *hSignin) (map[string]string, error) {
	env := map[string]string{}
	for k, v := range base {
		env[k] = v
	}
	if cred == nil {
		return env, nil
	}
	secret, err := signinVault.get(signinKey(cred.ID))
	if err != nil || secret == "" {
		if err == nil {
			err = errors.New("it is gone from your vault")
		}
		return nil, fmt.Errorf("couldn't read your saved sign-in %s: %v", cred.Name, err)
	}
	env[cred.Env] = secret
	return env, nil
}

// credSecret is saved sign-in s's secret ("" when it can't be read).
func credSecret(s *hSignin) string {
	if s == nil {
		return ""
	}
	v, _ := signinVault.get(signinKey(s.ID))
	return v
}

// apiKeyMeta is authenticate's _meta for an API key, in the shape the
// adapter reads: gemini-cli takes _meta["api-key"] as the key itself
// (acpRpcDispatcher authenticate, 0.60), codex-acp and the fake an object
// {apiKey}.
func apiKeyMeta(provider, key string) map[string]any {
	if provider == "gemini" {
		return map[string]any{"api-key": key}
	}
	return map[string]any{"api-key": map[string]any{"apiKey": key}}
}

// refuseCredTx marks the saved sign-in session hs started with refused —
// the adapter said it isn't signed in while it held it — with a note.
func (s *hsess) refuseCredTx(t *DB, hs *harnessSession, why string) {
	if hs.Cred == "" {
		return
	}
	sg, now := t.refuseSigninTx(hs.Cred, why)
	if sg == nil || !now {
		return
	}
	s.e.emitStep(t, s.root, t.journal(s.run, "note", map[string]string{"text": fmt.Sprintf(
		"%s refused your saved sign-in %s — saved sign-in refused: sign in again (Remember replaces it)", s.prov.Name, sg.Name)}))
}

// authHint is the client's word for an agent's -32000 (ClientOptions
// .AuthHint): it notes that the adapter itself refused (signedOutNow,
// onTurnEnd) and says how to sign in.
func (s *hsess) authHint(cfg acp.Config, msg string) string {
	s.authRefused.Store(true)
	how := orStr(cfg.Provider.LoginCmd, orStr(cfg.Provider.Login, "the CLI's own login"))
	return msg + ": the agent isn't signed in — run: " + how
}

// signedOutNow: st says the adapter is signed out — and, while a saved
// sign-in is in its environment, the adapter itself refused (a status
// update alone doesn't count: claude-agent-acp 0.81 reports an env token's
// sign-in as "none").
func (s *hsess) signedOutNow(st acp.SessionState) bool {
	return st.AuthNeeded && (s.cred == "" || s.authRefused.Load())
}

// credAuthenticate signs an adapter that refused its session signed out in
// with the saved API key it was started with, through its own API-key
// method — for an adapter that reads no key from its environment at start
// (codex-acp; gemini with another sign-in selected). The key rides
// authenticate's _meta once (apiKeyMeta), never logged. codex keeps it in
// its own ~/.codex/auth.json then (its store): a sandbox of the person's
// own only, the gate says. false: not that kind, no such method, refused.
func (s *hsess) credAuthenticate(ctx context.Context, cred *hSignin) bool {
	if cred.Kind != "api-key" {
		return false
	}
	method := ""
	for _, m := range s.c.AuthMethods() {
		if authKind(m) == "api-key" {
			method = m.ID
			break
		}
	}
	secret := credSecret(cred)
	if method == "" || secret == "" {
		return false
	}
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := s.c.Authenticate(actx, method, apiKeyMeta(s.prov.ID, secret))
	return err == nil
}

// signinSummary is a coding agent's summary's `signin` (a person's
// partition): pick — "default", "sandbox" or a saved sign-in's id — and
// using, the saved sign-in its adapter started with ({id, name, refused?};
// null: the sandbox's own). Names only, never a secret.
func signinSummary(d *DB, h *HarnessConfig, hs *harnessSession) map[string]any {
	pick := "default"
	switch h.Signin {
	case pickDefault:
	case pickSandbox:
		pick = "sandbox"
	default:
		pick = h.Signin
	}
	out := map[string]any{"pick": pick, "using": nil}
	if hs.Cred != "" {
		u := map[string]any{"id": hs.Cred, "name": ""}
		if s := d.signin(hs.Cred); s != nil {
			u["name"] = s.Name
			if s.RefusedAt != 0 {
				u["refused"] = true
			}
		} else {
			u["forgotten"] = true
		}
		out["using"] = u
	}
	return out
}

// --- routes ------------------------------------------------------------------------------

func signinRoutes() []routeDef {
	return []routeDef{
		{"GET /prefs/harness-signins", needAny, handleSignins},
		{"POST /prefs/harness-signins", needAny, handleNewSignin},
		{"PUT /prefs/harness-signins/{id}", needAny, handlePutSignin},
		{"DELETE /prefs/harness-signins/{id}", needAny, handleForgetSignin},
		{"PUT /runs/{id}/harness/signin", needParticipant, handlePickSignin},
	}
}

// signinsCaller is the person a saved-sign-ins route acts for (false once
// it answered): their own, in their own partition.
func signinsCaller(w http.ResponseWriter, r *http.Request) (who, bool) {
	c := callerOf(r)
	if globalMode() {
		xbin.WriteError(w, http.StatusConflict, signinsAtGlobal)
		return c, false
	}
	if errNotPerson(w, c) {
		return c, false
	}
	if userMode() && c.user != runUser {
		xbin.WriteError(w, http.StatusForbidden, "saved sign-ins are their person's own")
		return c, false
	}
	return c, true
}

// hsKind is a harness's sign-ins as the page offers them.
type hsKind struct {
	Name string    `json:"name"`
	Keys []acp.Key `json:"keys"`
	Mint bool      `json:"mint"` // the guided sign-in can mint one (Remember)
}

// handleSignins lists the caller's saved sign-ins.
//
//	GET /prefs/harness-signins → {available, why?, signins: [hSignin…], harnesses: {id: {name, keys, mint}}, warnDays}
func handleSignins(w http.ResponseWriter, r *http.Request) {
	c, ok := signinsCaller(w, r)
	if !ok {
		return
	}
	out := map[string]any{"available": userMode(), "signins": []*hSignin{}, "warnDays": int(signinWarn / (24 * time.Hour))}
	if why := signinsWhy(); why != "" {
		out["why"] = why
	}
	kinds := map[string]hsKind{}
	for _, p := range acp.Providers() {
		if len(p.Keys) > 0 {
			kinds[p.ID] = hsKind{Name: p.Name, Keys: p.Keys, Mint: p.Mint != nil}
		}
	}
	if fp := signinProvider(fakeHarness); harnessSeenAnywhere(fakeHarness) {
		kinds[fakeHarness] = hsKind{Name: fp.Name, Keys: fp.Keys, Mint: fp.Mint != nil}
	}
	out["harnesses"] = kinds
	if userMode() {
		now := time.Now()
		list := []*hSignin{}
		for _, s := range agent.db.signins(c.user, "") {
			list = append(list, s.view(now))
		}
		out["signins"] = list
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}

// harnessSeenAnywhere: a bound manager advertised id or a session ran it
// (the fake's sign-ins are offered only where it is: tests).
func harnessSeenAnywhere(id string) bool {
	var n int
	return agent.db.q.QueryRow(`SELECT 1 FROM harness_sessions WHERE provider=? LIMIT 1`, id).Scan(&n) == nil ||
		agent.db.q.QueryRow(`SELECT 1 FROM harness_seen WHERE provider=? LIMIT 1`, id).Scan(&n) == nil
}

// signinBody is POST/PUT /prefs/harness-signins' body. Secret is read once
// and dropped; nothing echoes it.
type signinBody struct {
	Harness string  `json:"harness"`
	Name    *string `json:"name"`
	Env     string  `json:"env"`
	Secret  string  `json:"secret"`
	Default *bool   `json:"default"`
}

func decodeSignin(w http.ResponseWriter, r *http.Request) (signinBody, bool) {
	var b signinBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 2*signinMax)).Decode(&b); err != nil && !errors.Is(err, io.EOF) {
		xbin.WriteError(w, http.StatusBadRequest, "bad body")
		return b, false
	}
	return b, true
}

// handleNewSignin saves a key or token the person pasted (the secret goes
// to their vault; the answer is the row, never the secret).
//
//	POST /prefs/harness-signins {harness, name?, secret, env?, default?} → 201 {signin}
func handleNewSignin(w http.ResponseWriter, r *http.Request) {
	c, ok := signinsCaller(w, r)
	if !ok {
		return
	}
	if why := signinsWhy(); why != "" {
		xbin.WriteError(w, http.StatusConflict, why)
		return
	}
	b, ok := decodeSignin(w, r)
	if !ok {
		return
	}
	p := signinProvider(b.Harness)
	secret := cleanSecret(b.Secret)
	b.Secret = ""
	var key acp.Key
	switch {
	case !harnessIDRe.MatchString(b.Harness) || len(p.Keys) == 0:
		xbin.WriteError(w, http.StatusBadRequest, fmt.Sprintf("harness: %q takes no saved sign-in", b.Harness))
		return
	case secret == "":
		xbin.WriteError(w, http.StatusBadRequest, "secret: the key or token, one line")
		return
	case b.Env != "":
		for _, k := range p.Keys {
			if k.Env == b.Env {
				key, ok = k, true
			}
		}
		if key.Env == "" {
			var envs []string
			for _, k := range p.Keys {
				envs = append(envs, k.Env)
			}
			xbin.WriteError(w, http.StatusBadRequest, "env: one of "+strings.Join(envs, ", "))
			return
		}
	default:
		if key, ok = p.KeyFor(secret); !ok {
			xbin.WriteError(w, http.StatusBadRequest, "env: say which key this is ("+p.Name+" takes several)")
			return
		}
	}
	name := pickName(b.Name, c.user, b.Harness)
	if name == "" {
		xbin.WriteError(w, http.StatusBadRequest, fmt.Sprintf("name: up to %d characters, one line", signinNameMax))
		return
	}
	s, err := agent.db.saveSignin(c.user, b.Harness, name, key, secret, time.Time{})
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	if b.Default != nil && *b.Default {
		_ = agent.db.setDefaultSignin(c.user, s.Harness, s.ID)
		s.IsDefault = true
	}
	xbin.WriteJSON(w, http.StatusCreated, map[string]any{"signin": s.view(time.Now())})
}

// pickName is the name asked for, else the next free one ("" unfit).
func pickName(name *string, user, harness string) string {
	if name == nil || strings.TrimSpace(*name) == "" {
		return agent.db.nextSigninName(user, harness)
	}
	return cleanName(*name)
}

// ownSignin is the caller's saved sign-in {id} (false once it answered 404).
func ownSignin(w http.ResponseWriter, r *http.Request, c who) (*hSignin, bool) {
	s := agent.db.signin(r.PathValue("id"))
	if s == nil || s.user != c.user {
		xbin.WriteError(w, http.StatusNotFound, "no such saved sign-in")
		return nil, false
	}
	return s, true
}

// handlePutSignin renames a saved sign-in, makes it (or no longer) its
// harness's default, or replaces its secret (a pasted one).
//
//	PUT /prefs/harness-signins/{id} {name?, default?, secret?, env?} → {signin}
func handlePutSignin(w http.ResponseWriter, r *http.Request) {
	c, ok := signinsCaller(w, r)
	if !ok {
		return
	}
	if why := signinsWhy(); why != "" {
		xbin.WriteError(w, http.StatusConflict, why)
		return
	}
	s, ok := ownSignin(w, r, c)
	if !ok {
		return
	}
	b, ok := decodeSignin(w, r)
	if !ok {
		return
	}
	secret := cleanSecret(b.Secret)
	if b.Secret != "" && secret == "" {
		xbin.WriteError(w, http.StatusBadRequest, "secret: the key or token, one line")
		return
	}
	b.Secret = ""
	if b.Name != nil {
		name := cleanName(*b.Name)
		if name == "" {
			xbin.WriteError(w, http.StatusBadRequest, fmt.Sprintf("name: up to %d characters, one line", signinNameMax))
			return
		}
		if o := agent.db.signinNamed(c.user, s.Harness, name); o != nil && o.ID != s.ID {
			xbin.WriteError(w, http.StatusConflict, fmt.Sprintf("you have a saved sign-in named %s already", o.Name))
			return
		}
		s.Name = name
	}
	if secret != "" {
		p := signinProvider(s.Harness)
		key, ok := p.KeyFor(secret)
		for _, k := range p.Keys {
			if b.Env != "" && k.Env == b.Env {
				key, ok = k, true
			}
		}
		if !ok {
			xbin.WriteError(w, http.StatusBadRequest, "env: say which key this is")
			return
		}
		if err := signinVault.set(signinKey(s.ID), secret); err != nil {
			xbin.WriteError(w, http.StatusBadGateway, "keeping it in your vault: "+err.Error())
			return
		}
		s.Kind, s.Env, s.RefusedAt, s.Refused, s.MintedAt, s.ExpiresAt = key.Kind, key.Env, 0, "", 0, 0
	}
	if err := agent.db.putSignin(s); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b.Default != nil {
		id := ""
		if *b.Default {
			id = s.ID
		} else if !s.IsDefault {
			id = currentDefault(c.user, s.Harness)
		}
		if err := agent.db.setDefaultSignin(c.user, s.Harness, id); err != nil {
			xbin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"signin": agent.db.signin(s.ID).view(time.Now())})
}

func currentDefault(user, harness string) string {
	for _, s := range agent.db.signins(user, harness) {
		if s.IsDefault {
			return s.ID
		}
	}
	return ""
}

// handleForgetSignin deletes a saved sign-in — its secret from the vault,
// its row — and stops the person's coding agents that started with it (the
// next message starts each again without it).
//
//	DELETE /prefs/harness-signins/{id} → {ok, stopped}
func handleForgetSignin(w http.ResponseWriter, r *http.Request) {
	c, ok := signinsCaller(w, r)
	if !ok {
		return
	}
	s, ok := ownSignin(w, r, c)
	if !ok {
		return
	}
	if err := signinVault.del(signinKey(s.ID)); err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "taking it out of your vault: "+err.Error())
		return
	}
	if err := agent.db.dropSignin(s.ID); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n := stopCredUsers(s, fmt.Sprintf("your saved sign-in %s was forgotten — the next message starts it again without it", s.Name))
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"ok": "true", "stopped": n})
}

// stopCredUsers stops every coding agent whose generation started with
// saved sign-in s (an hstop each, applied by its pass: a turn in flight
// ends with why) and answers how many.
func stopCredUsers(s *hSignin, why string) int {
	rows, err := agent.db.q.Query(`SELECT run_id, state FROM harness_sessions WHERE cred=?`, s.ID)
	if err != nil {
		return 0
	}
	var runs []int64
	for rows.Next() {
		var id int64
		var st string
		if rows.Scan(&id, &st) == nil && hsExecMayRun(st) {
			runs = append(runs, id)
		}
	}
	rows.Close()
	for _, id := range runs {
		if _, _, err := agent.queue(id, inboxHStop, inboxBody{Reason: why}, ""); err != nil {
			logf("run #%d: stopping its coding agent (saved sign-in forgotten): %v", id, err)
		}
	}
	return len(runs)
}

// stopCredsIn stops at once the coding agents that started with a saved
// sign-in in sandbox ref — shared through this agent's own route (a share
// made at the manager is caught by the next re-check: before every message,
// at most every minute while one works) — and answers how many.
func stopCredsIn(ref, what string) int {
	rows, err := agent.db.q.Query(`SELECT run_id, state FROM harness_sessions WHERE ref=? AND cred<>''`, ref)
	if err != nil {
		return 0
	}
	var runs []int64
	for rows.Next() {
		var id int64
		var st string
		if rows.Scan(&id, &st) == nil && hsExecMayRun(st) {
			runs = append(runs, id)
		}
	}
	rows.Close()
	for _, id := range runs {
		why := fmt.Sprintf("the coding agent was stopped: %s — %s; the next message starts it with the sandbox's own sign-in", what, credNotOwnBox)
		if _, _, err := agent.queue(id, inboxHStop, inboxBody{Reason: why}, ""); err != nil {
			logf("run #%d: stopping its coding agent (sandbox shared): %v", id, err)
		}
	}
	return len(runs)
}

// handlePickSignin picks the saved sign-in a coding agent's conversation
// uses — one of the person's for its harness, "default" or "sandbox" (the
// sandbox's own) — and switches to it: an adapter at rest restarts with it
// and resumes the same session (session/load: the transcript lives in the
// sandbox, not with the account); one parked on its sign-in starts again
// as a Retry does. While a turn runs or a question waits: 409.
//
//	PUT /runs/{id}/harness/signin {signin: "<id>" | "default" | "sandbox"} → {harness}
func handlePickSignin(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, cfg, sum, ok := harnessRoute(w, id)
	if !ok {
		return
	}
	c := callerOf(r)
	if why := signinsWhy(); why != "" {
		xbin.WriteError(w, http.StatusConflict, why)
		return
	}
	if !grantOwner(c, rootRunOf(run)) {
		xbin.WriteError(w, http.StatusForbidden, credNotOwnRun)
		return
	}
	var body struct {
		Signin string `json:"signin"`
	}
	if !decodeHarnessBody(w, r, &body) {
		return
	}
	name, _ := sum["name"].(string)
	pick, label := pickDefault, "your default sign-in"
	switch body.Signin {
	case "default", "":
	case "sandbox":
		pick, label = pickSandbox, "the sandbox's own sign-in"
	default:
		s := agent.db.signin(body.Signin)
		if s == nil || s.user != c.user || s.Harness != cfg.Harness.Provider {
			xbin.WriteError(w, http.StatusBadRequest, "signin: one of your saved sign-ins for "+name+", \"default\" or \"sandbox\"")
			return
		}
		if !s.usable(time.Now()) {
			xbin.WriteError(w, http.StatusConflict, fmt.Sprintf("your saved sign-in %s was refused or has expired — sign in again first", s.Name))
			return
		}
		pick, label = s.ID, s.Name
	}
	p := parsePending(run.Pending)
	if run.Status == statusRunning || (run.Status == statusWaiting && p.Kind != "login") {
		xbin.WriteError(w, http.StatusConflict, name+" is at work — switch once its turn is over")
		return
	}
	if err := agent.eng.fenced(func(t *DB) error {
		cur, err := t.runConfig(id)
		if err != nil || cur.Harness == nil {
			return err
		}
		cur.Harness.Signin = pick
		raw, _ := json.Marshal(cur)
		_, err = t.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), id)
		return err
	}); err != nil {
		if errors.Is(err, errHandoff) || errors.Is(err, errFenced) {
			writeHeld(w, name)
			return
		}
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	why := fmt.Sprintf("switched to %s — %s resumes this conversation with it", label, name)
	kind := inboxHSwitch
	if run.Status == statusWaiting && p.Kind == "login" {
		kind = inboxWake // a Retry with the new pick
	}
	if _, _, err := agent.queue(id, kind, inboxBody{Reason: why}, ""); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	run, _ = agent.db.getRun(id)
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"harness": harnessSummaryOf(run)})
}

// --- a switch ------------------------------------------------------------------------------

// inboxHSwitch restarts a coding agent's adapter at rest with the sign-in
// its conversation picks now (Reason: the note). Older binaries ignore it.
const inboxHSwitch = "hswitch"

// harnessSwitch applies hswitch rows: an adapter at rest is stopped (state
// stopped — the next message starts one with the new pick, resuming the
// session), with a note; one at work keeps the rows until its turn ends
// (false: not consumed).
func (e *Engine) harnessSwitch(ctx context.Context, run *Run, rows []*InboxRow) bool {
	hs, _ := e.db.harnessSession(run.ID)
	if hs != nil && (hs.PromptState != "" || run.Status == statusRunning ||
		(run.Status == statusWaiting && parsePending(run.Pending).Kind != "login")) {
		return false
	}
	e.consumeRows(rows)
	why := rows[len(rows)-1].Body.Reason
	if hs != nil && hsRunning(hs.State) {
		if s := e.harnessOf(run.ID); s != nil {
			if s.commit(nil, func(t *DB, hs *harnessSession) error {
				hs.State = hsStopped
				e.emitRun(t, s.run)
				return nil
			}) == nil {
				s.stop()
			}
		} else if cfg, err := e.db.runConfig(run.ID); err == nil && cfg.Harness != nil {
			e.dropExec(ctx, run, cfg, hs)
			_ = e.fenced(func(t *DB) error {
				cur, _ := t.harnessSession(run.ID)
				if cur == nil || cur.Gen != hs.Gen {
					return nil
				}
				cur.State = hsStopped
				return t.putHarnessSession(cur)
			})
		}
	}
	if why != "" {
		e.note(run, why)
	}
	e.publishHarness(run.ID)
	return true
}

// credStillFits re-checks, as the sandbox's use is re-checked, that the
// saved sign-in s started with may stay in its adapter: the sandbox shared
// since (or the run no longer the person's own) stops it ("" = it may).
func (s *hsess) credStillFits(run *Run, box *sbxSandbox) string {
	if s.cred == "" {
		return ""
	}
	if why := credWhy(run, box); why != "" {
		where := "its sandbox"
		if box != nil {
			where = sbxLabel(box)
		}
		return fmt.Sprintf("%s was stopped: %s (%s) — the next message starts it with the sandbox's own sign-in", s.prov.Name, why, where)
	}
	return ""
}
