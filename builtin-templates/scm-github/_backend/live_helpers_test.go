// live_helpers_test.go — the live checks' workspace: this tile's global
// instance and one person's partition wired to real GitHub (api.github.com)
// with the owner's test App, its PAT and a device-flow sign-in, loaded from
// ~/.config/xbin-test at run time only. Every live test skips unless
// XBIN_GH_LIVE=1 and the files are there: they are run by hand, never in
// CI (live_test.go says how). Nothing here prints a token: answers are
// logged through redact, which keeps a token's first characters only.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	liveAPI = "https://api.github.com"
	liveWeb = "https://github.com"
)

// liveCreds are the owner's test credentials (secrets in secretString).
type liveCreds struct {
	dir          string
	repo         string // owner/name
	appID        int64
	clientID     string
	clientSecret secretString
	pat          secretString
	pem          secretString
}

func liveDir() string {
	if d := os.Getenv("XBIN_GH_LIVE_DIR"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "xbin-test")
}

// loadLive skips the test unless the live checks were asked for and the
// credential files exist.
func loadLive(t *testing.T) *liveCreds {
	t.Helper()
	if os.Getenv("XBIN_GH_LIVE") != "1" {
		t.Skip("live GitHub checks: set XBIN_GH_LIVE=1 (run by hand, never in CI)")
	}
	dir := liveDir()
	env, err := os.ReadFile(filepath.Join(dir, "github.env"))
	if err != nil {
		t.Skip("live GitHub checks: no github.env in " + dir)
	}
	pem, err := os.ReadFile(filepath.Join(dir, "app.pem"))
	if err != nil {
		t.Skip("live GitHub checks: no app.pem in " + dir)
	}
	c := &liveCreds{dir: dir, pem: newSecret(string(pem))}
	for _, line := range strings.Split(string(env), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "GH_TEST_REPO":
			c.repo = v
		case "GH_APP_ID":
			c.appID, _ = strconv.ParseInt(v, 10, 64)
		case "GH_CLIENT_ID":
			c.clientID = v
		case "GH_CLIENT_SECRET":
			c.clientSecret = newSecret(v)
		case "GH_PAT":
			c.pat = newSecret(v)
		}
	}
	if c.repo == "" || c.appID == 0 || c.clientID == "" || c.clientSecret.Empty() || c.pat.Empty() {
		t.Fatal("github.env lacks one of GH_TEST_REPO, GH_APP_ID, GH_CLIENT_ID, GH_CLIENT_SECRET, GH_PAT")
	}
	return c
}

// live is the workspace: global (the App pasted) and, once asked for, the
// person's partition relaying to it.
type live struct {
	t      *testing.T
	c      *liveCreds
	hc     *http.Client
	conf   *memKV
	global *srv
	gH     http.Handler
	app    *appState
	user   *srv
	uH     http.Handler
	pj     *personFile
}

func newLive(t *testing.T) *live {
	t.Helper()
	c := loadLive(t)
	l := &live{t: t, c: c, hc: &http.Client{Timeout: 60 * time.Second}, conf: newMemKV()}
	l.global = newSrv("global", tilePath, newMemKV(), l.conf, newMemVault(), l.hc, time.Now)
	// The App as Paste stores it, minus Paste's PATCH of the App's webhook
	// config (that would rotate the owner's webhook secret): GET /app with
	// the template's own JWT, then storeApp.
	key, err := parseAppKey(c.pem.Reveal())
	if err != nil {
		t.Fatalf("app.pem: %v", err)
	}
	jwt, err := signJWT(key, c.clientID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var ga ghApp
	if _, err := l.global.gh.call(context.Background(), bearerAuth("", newSecret(jwt)), "GET", liveAPI+"/app", nil, &ga); err != nil {
		t.Fatalf("GET /app with the template's JWT: %v", err)
	}
	if ga.ID != c.appID {
		t.Fatalf("GET /app answered App %d, not %d", ga.ID, c.appID)
	}
	l.app = l.global.appFrom(ga, liveAPI, liveWeb, "")
	if err := l.global.storeApp(context.Background(), l.app, c.pem, c.clientSecret, newSecret(randomID(32))); err != nil {
		t.Fatalf("storeApp: %v", err)
	}
	l.app, _ = l.global.app()
	l.gH = l.global.routes()
	return l
}

func (l *live) owner() string { return ownerOf(l.c.repo) }
func (l *live) name() string  { return nameOf(l.c.repo) }

// call is one request to an instance with xbind's verified headers; the
// answer is logged redacted.
func (l *live) call(h http.Handler, c caller, method, path string, body any) *httptest.ResponseRecorder {
	l.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	c.set(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	l.t.Logf("%s %s → %d %s", method, path, w.Code, clip(redact(w.Body.String()), 1500))
	return w
}

// rawResp is one GitHub answer seen directly (no ETag cache, no redirect
// followed).
type rawResp struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r *rawResp) json(v any) error { return json.Unmarshal(r.Body, v) }

// raw calls GitHub directly: auth is "Bearer <tok>", "Basic" (the App's
// client id and secret) or "" (anonymous); hdr adds headers.
func (l *live) raw(method, u string, auth secretString, basic bool, body any, hdr map[string]string) *rawResp {
	l.t.Helper()
	if !strings.HasPrefix(u, "https://") {
		u = liveAPI + u
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		l.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "xbin-scm-github-live-test")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case basic:
		req.SetBasicAuth(l.c.clientID, l.c.clientSecret.Reveal())
	case !auth.Empty():
		req.Header.Set("Authorization", "Bearer "+auth.Reveal())
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		l.t.Fatalf("%s %s: %v", method, scrubURLs(u), err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	l.t.Logf("raw %s %s → %d %s", method, scrubURLs(u), resp.StatusCode, clip(redact(string(b)), 800))
	return &rawResp{resp.StatusCode, resp.Header, b}
}

// tokenRe matches GitHub's token shapes, a JWT and a signed URL's query.
var tokenRe = regexp.MustCompile(`(gh[pousr]_|github_pat_)[A-Za-z0-9_.\-]{4,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_.-]+|([?&](sig|sv|se|sp|sr|skoid|sktid|skt|ske|sks|skv|token|rscd|rsct)=)[^&"\s]+`)

// redact keeps a token's prefix only (ghu_…) and drops signed URLs'
// signatures, so a log line never carries a credential.
func redact(s string) string {
	return tokenRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "?") || strings.HasPrefix(m, "&") {
			k, _, _ := strings.Cut(m, "=")
			return k + "=…"
		}
		if strings.HasPrefix(m, "eyJ") {
			return "eyJ…"
		}
		if strings.HasPrefix(m, "github_pat_") {
			return "github_pat_…"
		}
		return m[:4] + "…"
	})
}

// botToken asks global for a bot token as a tile would.
func (l *live) botToken(purpose, access string, perms map[string]string) (tokenResp, secretString) {
	l.t.Helper()
	body := map[string]any{"repo": l.c.repo, "access": access, "purpose": purpose}
	if perms != nil {
		body["permissions"] = perms
	}
	r := l.call(l.gH, agentC, "POST", "/scm/token", body)
	if r.Code != 200 {
		l.t.Fatalf("POST /scm/token: %d %s", r.Code, redact(r.Body.String()))
	}
	var raw struct {
		tokenResp
		Token string `json:"token"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &raw); err != nil {
		l.t.Fatal(err)
	}
	return raw.tokenResp, newSecret(raw.Token)
}

// personFile is person.json: the owner's device-flow pair. A refresh
// retires the old pair, so the vault writes every new pair back here at
// once (mode 600, atomically) — the only place a person token is written.
type personFile struct {
	path string
	mu   sync.Mutex
	raw  map[string]any // the file as read (obtained_at's format kept)
}

func (p *personFile) str(k string) string { s, _ := p.raw[k].(string); return s }
func (p *personFile) num(k string) int64 {
	switch v := p.raw[k].(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

// obtained is when the pair was made (unix seconds, unix ms or RFC 3339).
func (p *personFile) obtained() time.Time {
	switch v := p.raw["obtained_at"].(type) {
	case float64:
		if v > 1e12 {
			return time.UnixMilli(int64(v))
		}
		return time.Unix(int64(v), 0)
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// save writes a new pair (made now) over the file, atomically, mode 600.
func (p *personFile) save(access, refresh string, accessExp, refreshExp int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	m := map[string]any{}
	for k, v := range p.raw {
		m[k] = v
	}
	m["access_token"], m["refresh_token"] = access, refresh
	m["expires_in"] = max((accessExp-now.UnixMilli())/1000, 0)
	if refreshExp > 0 {
		m["refresh_token_expires_in"] = max((refreshExp-now.UnixMilli())/1000, 0)
	}
	switch v := p.raw["obtained_at"].(type) {
	case string:
		if _, err := time.Parse(time.RFC3339, v); err == nil {
			m["obtained_at"] = now.UTC().Format(time.RFC3339)
		} else {
			m["obtained_at"] = strconv.FormatInt(now.Unix(), 10)
		}
	case float64:
		if v > 1e12 {
			m["obtained_at"] = now.UnixMilli()
		} else {
			m["obtained_at"] = now.Unix()
		}
	default:
		m["obtained_at"] = now.Unix()
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.path), ".person-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), p.path); err != nil {
		return err
	}
	p.raw = m
	return nil
}

// savingVault is the person's partition vault: a new refresh token (the
// last half of storeUser's pair) is written back to person.json.
type savingVault struct {
	*memVault
	pf *personFile
	t  *testing.T
}

func (v *savingVault) Set(name string, s secretString) error {
	if err := v.memVault.Set(name, s); err != nil {
		return err
	}
	if name != vaultUserRefresh {
		return nil
	}
	var acc, ref vaultTok
	if vaultJSON(v.memVault, vaultUserAccess, &acc) != nil || vaultJSON(v.memVault, vaultUserRefresh, &ref) != nil {
		return nil
	}
	if acc.Token == v.pf.str("access_token") {
		return nil // the pair as loaded
	}
	if err := v.pf.save(acc.Token, ref.Token, acc.ExpiresAt, ref.ExpiresAt); err != nil {
		v.t.Errorf("writing the refreshed pair back to person.json: %v (the pair now lives only in this run)", err)
		return err
	}
	v.t.Logf("person.json: the refreshed pair written back (%s)", redact(acc.Token))
	return nil
}

// loadPerson reads person.json (skipping when it isn't there yet).
func (l *live) loadPerson() *personFile {
	l.t.Helper()
	path := filepath.Join(l.c.dir, "person.json")
	b, err := os.ReadFile(path)
	if err != nil {
		l.t.Skip("person.json isn't there yet (the owner runs the App's device flow)")
	}
	pf := &personFile{path: path}
	if err := json.Unmarshal(b, &pf.raw); err != nil {
		l.t.Fatalf("person.json doesn't parse: %v", err)
	}
	if pf.str("access_token") == "" || pf.str("refresh_token") == "" {
		l.t.Fatal("person.json lacks access_token or refresh_token")
	}
	return pf
}

// personSrv is the person's partition ("user:live"), signed in with
// person.json's pair, its relay wired to global as xbind would attribute it.
func (l *live) personSrv() (*srv, http.Handler) {
	l.t.Helper()
	if l.user != nil {
		return l.user, l.uH
	}
	pf := l.loadPerson()
	l.pj = pf
	v := &savingVault{memVault: newMemVault(), pf: pf, t: l.t}
	s := newSrv("user:live", tilePath, newMemKV(), l.conf, v, l.hc, time.Now)
	s.relayCall = func(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
		r := httptest.NewRequest(method, "/"+strings.TrimPrefix(path, "/"), bytes.NewReader(body)).WithContext(ctx)
		relayC("live", "write").set(r)
		w := httptest.NewRecorder()
		l.gH.ServeHTTP(w, r)
		return w.Result(), nil
	}
	ob := pf.obtained()
	if ob.IsZero() {
		ob = time.Now()
	}
	accExp := ob.Add(time.Duration(pf.num("expires_in")) * time.Second).UnixMilli()
	var refExp int64
	if n := pf.num("refresh_token_expires_in"); n > 0 {
		refExp = ob.Add(time.Duration(n) * time.Second).UnixMilli()
	}
	acc := newSecret(pf.str("access_token"))
	var me struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	rr := l.raw("GET", "/user", acc, false, nil, nil)
	if rr.Status == 401 && time.Now().UnixMilli() >= accExp-60_000 {
		l.t.Log("person.json's access token has expired: the partition refreshes it first")
	} else if rr.Status != 200 || rr.json(&me) != nil {
		l.t.Fatalf("GET /user with person.json's token: %d", rr.Status)
	}
	if err := setVaultJSON(v.memVault, vaultUserAccess, vaultTok{acc.Reveal(), accExp}); err != nil {
		l.t.Fatal(err)
	}
	if err := setVaultJSON(v.memVault, vaultUserRefresh, vaultTok{pf.str("refresh_token"), refExp}); err != nil {
		l.t.Fatal(err)
	}
	if me.Login == "" { // expired: refresh, then ask who it is
		_ = s.state.Put("person", personRec{Login: "?", ID: 0, ExpiresAt: accExp, RefreshExpiresAt: refExp, Epoch: 1})
		tok, _, err := s.refresh(context.Background(), acc.Reveal())
		if err != nil {
			l.t.Fatalf("refreshing person.json's expired pair: %v", err)
		}
		if rr := l.raw("GET", "/user", tok, false, nil, nil); rr.Status != 200 || rr.json(&me) != nil {
			l.t.Fatalf("GET /user after the refresh: %d", rr.Status)
		}
		var rec personRec
		_ = s.state.Get("person", &rec)
		rec.Login, rec.ID = me.Login, me.ID
		_ = s.state.Put("person", rec)
	} else {
		_ = s.state.Put("person", personRec{Login: me.Login, ID: me.ID, ExpiresAt: accExp, RefreshExpiresAt: refExp, Epoch: 1})
	}
	l.user, l.uH = s, s.routes()
	return l.user, l.uH
}

// parent is the person's current parent access token (the vault's).
func (l *live) parent() secretString {
	var acc vaultTok
	if err := vaultJSON(l.user.vault, vaultUserAccess, &acc); err != nil {
		l.t.Fatal(err)
	}
	return newSecret(acc.Token)
}

// liveNote collects the answers a test found, logged at its end as one
// block (what the live-check record is written from).
type liveNote struct {
	t     *testing.T
	lines []string
}

func (n *liveNote) add(format string, args ...any) {
	s := redact(fmt.Sprintf(format, args...))
	n.lines = append(n.lines, s)
	n.t.Log("ANSWER: " + s)
}
