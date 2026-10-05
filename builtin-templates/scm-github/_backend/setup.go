// setup.go — managers set the GitHub App up at global (API.md §Setting it
// up): paste an existing App's keys, or create one from a manifest; check
// the device flow; list installations. Secrets are write-only: nothing
// here answers one.
package main

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// appView is what the page sees of the App.
type appView struct {
	AppID          int64             `json:"appId"`
	ClientID       string            `json:"clientId"`
	Slug           string            `json:"slug"`
	Owner          string            `json:"owner"`
	HTMLURL        string            `json:"htmlUrl"`
	Host           string            `json:"host"`
	KeyFingerprint string            `json:"keyFingerprint"`
	CreatedAt      int64             `json:"createdAt"`
	Permissions    map[string]string `json:"permissions,omitempty"`
	Events         []string          `json:"events,omitempty"`
	InstallURL     string            `json:"installUrl"`
	SettingsURL    string            `json:"settingsUrl"`
}

func (s *srv) view(a *appState) *appView {
	if a == nil {
		return nil
	}
	_, _, web := s.hosts()
	settings := web + "/settings/apps/" + a.Slug
	if a.Owner != "" && !strings.EqualFold(a.Owner, a.Slug) {
		settings = web + "/organizations/" + a.Owner + "/settings/apps/" + a.Slug
	}
	return &appView{AppID: a.AppID, ClientID: a.ClientID, Slug: a.Slug, Owner: a.Owner, HTMLURL: a.HTMLURL, Host: a.Host,
		KeyFingerprint: a.KeyFingerprint, CreatedAt: a.CreatedAt, Permissions: a.Permissions, Events: a.Events,
		InstallURL: s.installURL(a), SettingsURL: settings}
}

func (s *srv) handleSetupGet(w http.ResponseWriter, r *http.Request, _ who) {
	a, err := s.app()
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"configured": a != nil, "app": s.view(a), "deviceFlow": s.public().DeviceFlow}
	if a != nil {
		out["hook"] = map[string]any{"url": a.HookURL, "active": a.HookURL != ""}
	}
	writeGET(w, r, out)
}

// ghApp is GET /app's answer (and the manifest conversion's, which adds
// the secrets).
type ghApp struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	ClientID string `json:"client_id"`
	HTMLURL  string `json:"html_url"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions   map[string]string `json:"permissions"`
	Events        []string          `json:"events"`
	ClientSecret  secretString      `json:"client_secret"`
	WebhookSecret secretString      `json:"webhook_secret"`
	PEM           secretString      `json:"pem"`
}

// handleSetupPaste takes an existing App: validated by signing a JWT and
// asking GitHub for the App (same id) and its webhook config, which it
// points at hookUrl with the webhook secret.
func (s *srv) handleSetupPaste(w http.ResponseWriter, r *http.Request, _ who) {
	var body struct {
		AppID         int64        `json:"appId"`
		ClientID      string       `json:"clientId"`
		ClientSecret  secretString `json:"clientSecret"`
		PrivateKey    secretString `json:"privateKey"`
		WebhookSecret secretString `json:"webhookSecret"`
		HookURL       string       `json:"hookUrl"`
		APIBase       string       `json:"apiBase"`
		WebBase       string       `json:"webBase"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	switch {
	case body.AppID <= 0:
		fail(w, refuse(refInvalid, "appId is the App's number (its settings page, About)"))
		return
	case body.ClientID == "" || len(body.ClientID) > 100:
		fail(w, refuse(refInvalid, "clientId is the App's client ID"))
		return
	case body.ClientSecret.Empty():
		fail(w, refuse(refInvalid, "clientSecret is a client secret made in the App's settings"))
		return
	case body.HookURL != "" && !strings.HasPrefix(body.HookURL, "https://"):
		fail(w, refuse(refInvalid, "hookUrl is the https address GitHub delivers to (the hooks exposure's URL + /hook/github)"))
		return
	}
	apiBase, webBase, err := s.bases(body.APIBase, body.WebBase)
	if err != nil {
		fail(w, err)
		return
	}
	key, err := parseAppKey(body.PrivateKey.Reveal())
	if err != nil {
		fail(w, refuse(refInvalid, "%s", err.Error()))
		return
	}
	jwt, err := signJWT(key, body.ClientID, s.now())
	if err != nil {
		fail(w, err)
		return
	}
	ctx := r.Context()
	auth := bearerAuth("", newSecret(jwt))
	var ga ghApp
	if _, err := s.gh.call(ctx, auth, http.MethodGet, apiBase+"/app", nil, &ga); err != nil {
		if isRefusal(err, refUpstream) || isRefusal(err, refNotFound) {
			err = refuse(refInvalid, "GitHub didn't accept the key for that client ID: check the App's client ID and private key")
		}
		fail(w, err)
		return
	}
	if ga.ID != body.AppID {
		fail(w, refuse(refInvalid, "that key belongs to App %d, not %d", ga.ID, body.AppID))
		return
	}
	var hook struct {
		URL string `json:"url"`
	}
	// An App whose webhook is off (no URL, or "Active" unticked) has no
	// hook config: GitHub answers 404 (seen live), not an empty one.
	if _, err := s.gh.call(ctx, auth, http.MethodGet, apiBase+"/app/hook/config", nil, &hook); err != nil && !isRefusal(err, refNotFound) {
		fail(w, err)
		return
	}
	secret := body.WebhookSecret
	if secret.Empty() {
		if old, err := s.vault.Get(vaultHookSecret); err == nil && s.sameApp(body.AppID) {
			secret = old
		} else {
			secret = newSecret(randomID(32))
		}
	}
	hookURL := hook.URL
	if body.HookURL != "" || !body.WebhookSecret.Empty() || secret.Reveal() != s.vaultValue(vaultHookSecret) {
		patch := map[string]any{"secret": secret.Reveal(), "content_type": "json"}
		if body.HookURL != "" {
			patch["url"], hookURL = body.HookURL, body.HookURL
		}
		if hookURL != "" {
			// The vault takes the new secret first (the current one kept as
			// the previous, accepted a day): GitHub signs with it from the
			// moment the PATCH lands, and nothing after the PATCH can leave
			// GitHub with a secret this tile never stored. Refused, the
			// current one is put back — GitHub still signs with it.
			cur := s.vaultValue(vaultHookSecret)
			if err := s.rotateHookSecret(cur, secret); err != nil {
				fail(w, err)
				return
			}
			if _, err := s.gh.call(ctx, auth, http.MethodPatch, apiBase+"/app/hook/config", patch, nil); err != nil {
				if cur == "" {
					_ = s.vault.Delete(vaultHookSecret)
				} else if cur != secret.Reveal() {
					_ = s.vault.Set(vaultHookSecret, newSecret(cur))
				}
				if isRefusal(err, refNotFound) {
					err = refuse(refInvalid, "the App's webhook is off, so GitHub keeps no webhook address for it: tick Active under Webhook in the App's settings, then paste again (or paste without hookUrl)")
				}
				fail(w, err)
				return
			}
		}
	}
	a := s.appFrom(ga, apiBase, webBase, hookURL)
	if err := s.storeApp(ctx, a, body.PrivateKey, body.ClientSecret, secret); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": s.view(a), "hook": map[string]any{"url": hookURL, "active": hookURL != ""}})
}

// bases checks a GitHub Enterprise Server's addresses (default: github.com).
func (s *srv) bases(apiBase, webBase string) (string, string, error) {
	if apiBase == "" && webBase == "" {
		return s.defaultAPI, s.defaultWeb, nil
	}
	for _, u := range []string{apiBase, webBase} {
		p, err := url.Parse(u)
		if err != nil || p.Scheme != "https" || p.Host == "" || p.RawQuery != "" {
			return "", "", refuse(refInvalid, "apiBase and webBase are https addresses (GitHub Enterprise Server: https://<host>/api/v3 and https://<host>)")
		}
	}
	return strings.TrimRight(apiBase, "/"), strings.TrimRight(webBase, "/"), nil
}

// rotateHookSecret makes next the webhook secret, cur (when another)
// kept as the previous one, accepted for a day.
func (s *srv) rotateHookSecret(cur string, next secretString) error {
	if cur == next.Reveal() {
		return nil
	}
	if cur != "" {
		if err := s.vault.Set(vaultHookSecretP, newSecret(cur)); err != nil {
			return err
		}
		_ = s.state.Put("hook-secret-rotated", s.now().UnixMilli())
	}
	return s.vault.Set(vaultHookSecret, next)
}

func (s *srv) sameApp(id int64) bool {
	a, _ := s.app()
	return a != nil && a.AppID == id
}

func (s *srv) vaultValue(name string) string {
	v, err := s.vault.Get(name)
	if err != nil {
		return ""
	}
	return v.Reveal()
}

func (s *srv) appFrom(ga ghApp, apiBase, webBase, hookURL string) *appState {
	host := ""
	if u, err := url.Parse(webBase); err == nil {
		host = u.Hostname()
	}
	a := &appState{AppID: ga.ID, ClientID: ga.ClientID, Slug: ga.Slug, Owner: ga.Owner.Login, HTMLURL: ga.HTMLURL,
		Host: host, APIBase: apiBase, WebBase: webBase, CreatedAt: s.now().UnixMilli(),
		Permissions: ga.Permissions, Events: ga.Events, HookURL: hookURL}
	return a
}

// storeApp keeps a new App: secrets in the vault, the rest in state, the
// policy's first allowedAccounts (the App's own account), conf "public".
func (s *srv) storeApp(ctx context.Context, a *appState, pemText, clientSecret, hookSecret secretString) error {
	key, err := parseAppKey(pemText.Reveal())
	if err != nil {
		return refuse(refInvalid, "%s", err.Error())
	}
	a.KeyFingerprint = keyFingerprint(key)
	if err := s.rotateHookSecret(s.vaultValue(vaultHookSecret), hookSecret); err != nil {
		return err
	}
	for name, v := range map[string]secretString{vaultAppKey: pemText, vaultAppSecret: clientSecret} {
		if err := s.vault.Set(name, v); err != nil {
			return err
		}
	}
	var bot struct {
		ID int64 `json:"id"`
	}
	if _, err := s.gh.call(ctx, ghAuth{}, http.MethodGet, a.APIBase+"/users/"+pathEsc(a.Slug+"[bot]"), nil, &bot); err == nil {
		a.BotID = bot.ID
	}
	prev, _ := s.app()
	if err := s.state.Put("app", a); err != nil {
		return err
	}
	s.mu.Lock()
	s.appC, s.keyC = nil, nil
	s.mu.Unlock()
	s.jwt.reset()
	s.bot.clear()
	s.intl.clear()
	if prev == nil || prev.AppID != a.AppID {
		keys, _ := s.state.List("inst/")
		for _, k := range keys {
			_ = s.state.Delete(k)
		}
	}
	var raw map[string]any
	if s.state.Get("policy", &raw) != nil {
		p := defaultPolicy()
		p.AllowedAccounts = []string{a.Owner}
		if err := s.state.Put("policy", p); err != nil {
			return err
		}
	}
	change := pubTokens
	if prev == nil || prev.AppID != a.AppID {
		change |= pubNewApp
	}
	return s.writePublic(change)
}

// manifestState is state "mstate/<state>": one manifest flow, single use,
// an hour, bound to the manager who started it.
type manifestState struct {
	By string `json:"by"`
	At int64  `json:"at"`
}

func managerID(c who) string {
	switch c.cls {
	case clsOwner:
		return "owner"
	case clsSelf:
		return "self"
	}
	return "user:" + c.person
}

// handleManifestStart makes an App manifest for the page to POST to GitHub
// (a form in a new tab): the App's permissions and events, the hooks
// exposure's addresses when the manager gives its public host.
func (s *srv) handleManifestStart(w http.ResponseWriter, r *http.Request, c who) {
	var body struct {
		Org        string   `json:"org"`
		Name       string   `json:"name"`
		PublicHost string   `json:"publicHost"`
		Presets    []string `json:"presets"`
		Public     bool     `json:"public"`
		WebBase    string   `json:"webBase"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.Name == "" || len(body.Name) > 34 {
		fail(w, refuse(refInvalid, "name is the App's name on GitHub (at most 34 characters)"))
		return
	}
	if body.Org != "" && !validLogin(body.Org) {
		fail(w, refuse(refInvalid, "org is an organization's login"))
		return
	}
	if body.PublicHost != "" && (strings.ContainsAny(body.PublicHost, "/:?#@ ") || !strings.Contains(body.PublicHost, ".")) {
		fail(w, refuse(refInvalid, "publicHost is the hooks exposure's host name (scm.example.com)"))
		return
	}
	perms := map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read",
		"statuses": "read", "actions": "read", "metadata": "read", "members": "read"}
	for _, p := range body.Presets {
		switch p {
		case "ci":
			perms["actions"] = "write"
		case "workflows":
			perms["workflows"] = "write"
		default:
			fail(w, refuse(refInvalid, "presets are ci and workflows"))
			return
		}
	}
	_, _, web := s.hosts()
	if body.WebBase != "" {
		if _, wb, err := s.bases("https://unused", body.WebBase); err == nil {
			web = wb
		} else {
			fail(w, err)
			return
		}
	}
	state := randomID(32)
	if err := s.state.Put("mstate/"+state, manifestState{By: managerID(c), At: s.now().UnixMilli()}); err != nil {
		fail(w, err)
		return
	}
	m := map[string]any{
		"name": body.Name, "public": body.Public, "request_oauth_on_install": false, "setup_on_update": false,
		"default_permissions": perms,
		"default_events": []string{"pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment",
			"issues", "push", "check_suite", "check_run", "status", "workflow_run", "workflow_job", "member", "membership", "organization"},
	}
	if body.PublicHost != "" {
		base := "https://" + body.PublicHost
		m["url"] = base
		m["hook_attributes"] = map[string]any{"url": base + "/hook/github", "active": true}
		m["redirect_url"] = base + "/setup/github"
	} else {
		// No public host yet: the hook is set later (paste, hookUrl) and
		// GitHub sends the manager to the App list, whose address they
		// paste back (POST /setup/manifest/code).
		m["url"] = web
		m["hook_attributes"] = map[string]any{"url": "https://example.invalid/hook/github", "active": false}
		m["redirect_url"] = web + "/settings/apps"
	}
	post := web + "/settings/apps/new?state=" + url.QueryEscape(state)
	if body.Org != "" {
		post = web + "/organizations/" + body.Org + "/settings/apps/new?state=" + url.QueryEscape(state)
	}
	writeJSON(w, http.StatusOK, map[string]any{"postUrl": post, "manifest": m, "state": state})
}

// takeState checks and spends a manifest state: unknown, older than an
// hour, or another manager's (by "" for the ingress callback, which only
// proves possession) is refused.
func (s *srv) takeState(state, by string) error {
	if state == "" || len(state) > 100 {
		return refuse(refInvalid, "the address has no state from this tile")
	}
	key := "mstate/" + state
	var m manifestState
	if s.state.Get(key, &m) != nil {
		return refuse(refNotFound, "that App-manifest flow isn't this tile's, or was used already")
	}
	if s.now().UnixMilli()-m.At > time.Hour.Milliseconds() {
		_ = s.state.Delete(key)
		return refuse(refNotFound, "that App-manifest flow is over an hour old: start again")
	}
	if by != "" && m.By != by {
		return refuse(refNotAllowed, "another manager started that App-manifest flow")
	}
	return s.state.Delete(key)
}

func (s *srv) pruneManifestStates() {
	keys, _ := s.state.List("mstate/")
	for _, k := range keys {
		var m manifestState
		if s.state.Get(k, &m) == nil && s.now().UnixMilli()-m.At > time.Hour.Milliseconds() {
			_ = s.state.Delete(k)
		}
	}
}

// convert trades the manifest flow's code for the App and keeps it.
func (s *srv) convert(ctx context.Context, code string) (*appState, error) {
	if code == "" || len(code) > 100 || strings.ContainsAny(code, "/?#") {
		return nil, refuse(refInvalid, "the address has no code from GitHub")
	}
	_, apiBase, webBase := s.hosts()
	var ga ghApp
	if _, err := s.gh.call(ctx, ghAuth{}, http.MethodPost, apiBase+"/app-manifests/"+pathEsc(code)+"/conversions", nil, &ga); err != nil {
		return nil, err
	}
	if ga.PEM.Empty() || ga.ClientSecret.Empty() || ga.ID == 0 {
		return nil, refuse(refUpstream, "GitHub's answer had no App")
	}
	key, err := parseAppKey(ga.PEM.Reveal())
	if err != nil {
		return nil, refuse(refUpstream, "GitHub's App key doesn't parse")
	}
	hook := ""
	if ga.WebhookSecret.Empty() {
		ga.WebhookSecret = newSecret(randomID(32))
	}
	a := s.appFrom(ga, apiBase, webBase, hook)
	if jwt, err := signJWT(key, a.ClientID, s.now()); err == nil {
		var cfg struct {
			URL string `json:"url"`
		}
		if _, err := s.gh.call(ctx, bearerAuth("", newSecret(jwt)), http.MethodGet, apiBase+"/app/hook/config", nil, &cfg); err == nil && !strings.Contains(cfg.URL, "example.invalid") {
			a.HookURL = cfg.URL
		}
	}
	if err := s.storeApp(ctx, a, ga.PEM, ga.ClientSecret, ga.WebhookSecret); err != nil {
		return nil, err
	}
	return a, nil
}

// handleManifestCode takes the address GitHub sent the manager to (way c:
// pasted by the manager).
func (s *srv) handleManifestCode(w http.ResponseWriter, r *http.Request, c who) {
	var body struct {
		URL string `json:"url"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	u, err := url.Parse(strings.TrimSpace(body.URL))
	if err != nil {
		fail(w, refuse(refInvalid, "paste the whole address GitHub sent you to"))
		return
	}
	q := u.Query()
	if err := s.takeState(q.Get("state"), managerID(c)); err != nil {
		fail(w, err)
		return
	}
	a, err := s.convert(r.Context(), q.Get("code"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": s.view(a), "hook": map[string]any{"url": a.HookURL, "active": a.HookURL != ""}})
}

// handleManifestCallback is GitHub's redirect: through the hooks exposure
// (way a, ingress — the state proves the flow) or the tile's own address
// loaded at top level by the manager (way b).
func (s *srv) handleManifestCallback(w http.ResponseWriter, r *http.Request) {
	if s.mode == modeUser {
		http.NotFound(w, r)
		return
	}
	c := s.classify(r)
	by := ""
	switch {
	case c.cls == clsIngress:
	case s.manager(c):
		by = managerID(c)
	default:
		htmlPage(w, http.StatusForbidden, "Only this tile's managers finish setting up its GitHub App.")
		return
	}
	q := r.URL.Query()
	if err := s.takeState(q.Get("state"), by); err != nil {
		htmlPage(w, asRefusal(err).Status, asRefusal(err).Message)
		return
	}
	a, err := s.convert(r.Context(), q.Get("code"))
	if err != nil {
		htmlPage(w, asRefusal(err).Status, "GitHub didn't hand over the App: "+asRefusal(err).Message)
		return
	}
	htmlPage(w, http.StatusOK, fmt.Sprintf("The GitHub App %s is set up. Go back to xbin: its page shows what is left (Device Flow, installing it).", a.Slug))
}

// page answers a small HTML page (the manifest callback).
func htmlPage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>scm-github</title><p style=\"font:15px system-ui;margin:3em\">%s</p>\n", html.EscapeString(msg))
}

// handleSetupCheck probes the device flow with the App's client id: GitHub
// answers device_flow_disabled while "Enable Device Flow" is off.
func (s *srv) handleSetupCheck(w http.ResponseWriter, r *http.Request, _ who) {
	a, err := s.mustApp()
	if err != nil {
		fail(w, err)
		return
	}
	var out struct {
		DeviceCode string `json:"device_code"`
		Error      string `json:"error"`
	}
	if err := s.oauthPost(r.Context(), a.WebBase+"/login/device/code", url.Values{"client_id": {a.ClientID}, "scope": {""}}, &out); err != nil {
		fail(w, err)
		return
	}
	on := out.DeviceCode != "" && out.Error == ""
	p := s.public()
	p.DeviceFlow = map[bool]string{true: "on", false: "off"}[on]
	if err := s.conf.Put("public", p); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deviceFlow": on})
}

// handleInstallations lists the App's installations; one on an account
// outside allowedAccounts is foreign (a public App can be installed by
// anyone): served nothing, shown with where to remove it.
func (s *srv) handleInstallations(w http.ResponseWriter, r *http.Request, _ who) {
	auth, err := s.appAuth()
	if err != nil {
		fail(w, err)
		return
	}
	type ghInst struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
		RepositorySelection string  `json:"repository_selection"`
		SuspendedAt         *string `json:"suspended_at"`
	}
	all, err := getAll[ghInst](r.Context(), s.gh, auth, s.apiBase()+"/app/installations?per_page=100", "", 500)
	if err != nil {
		fail(w, err)
		return
	}
	pol := s.policy()
	items := []map[string]any{}
	foreign := 0
	for _, in := range all {
		ok := pol.accountAllowed(in.Account.Login)
		if !ok {
			foreign++
		}
		items = append(items, map[string]any{"id": strconv.FormatInt(in.ID, 10), "account": in.Account.Login, "type": in.Account.Type,
			"url": in.HTMLURL, "repositorySelection": in.RepositorySelection, "suspended": in.SuspendedAt != nil, "allowed": ok, "foreign": !ok})
	}
	writeGET(w, r, map[string]any{"items": items, "foreign": foreign})
}
