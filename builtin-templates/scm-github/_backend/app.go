// app.go — the GitHub App: its public settings (state "app"), its secrets
// (the vault), the public half every partition reads (conf "public"), the
// installation of each account and the installation tokens the tile uses
// itself.
package main

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Vault names (global and legacy).
const (
	vaultAppKey      = "app-private-key"
	vaultAppSecret   = "app-client-secret"
	vaultHookSecret  = "app-webhook-secret"
	vaultHookSecretP = "app-webhook-secret-prev"
)

// appState is state "app": everything about the App but its secrets.
type appState struct {
	AppID          int64             `json:"appId"`
	ClientID       string            `json:"clientId"`
	Slug           string            `json:"slug"`
	Owner          string            `json:"owner"`
	HTMLURL        string            `json:"htmlUrl"`
	Host           string            `json:"host"`
	APIBase        string            `json:"apiBase"`
	WebBase        string            `json:"webBase"`
	KeyFingerprint string            `json:"keyFingerprint"`
	CreatedAt      int64             `json:"createdAt"`
	Permissions    map[string]string `json:"permissions,omitempty"` // as GET /app last said
	Events         []string          `json:"events,omitempty"`
	HookURL        string            `json:"hookUrl,omitempty"`
	BotID          int64             `json:"botId,omitempty"` // the App's bot user (<slug>[bot]): commits' noreply address
}

// publicConf is conf "public": what people's partitions read. Never a secret.
type publicConf struct {
	ClientID   string       `json:"clientId"`
	Slug       string       `json:"slug"`
	Host       string       `json:"host"`
	APIBase    string       `json:"apiBase"`
	WebBase    string       `json:"webBase"`
	DeviceFlow string       `json:"deviceFlow"` // on | off | unknown (POST /setup/check)
	InstallURL string       `json:"installUrl"`
	Policy     publicPolicy `json:"policy"`
	Rerun      bool         `json:"rerun"` // checks.rerun is offered (the App has actions: write, allowRerun)
	Configured bool         `json:"configured"`
	// TokenGen changes whenever tokens handed out may no longer be handed
	// out again: a policy change, "Revoke all bot tokens", a new App. A
	// partition reuses only tokens of the generation it reads here.
	TokenGen int64 `json:"tokenGen"`
}

type publicPolicy struct {
	BotForPeople    string   `json:"botForPeople"`
	AllowWorkflows  bool     `json:"allowWorkflows"`
	AllowedAccounts []string `json:"allowedAccounts"` // a partition's reads keep to them too (global enforces tokens)
	// BotRepos lets a partition re-check a bot token it would reuse (global
	// checks every one it relays); absent (an older global): not re-checked.
	BotRepos []string `json:"botRepos"`
}

// appKeys are the App's secrets in memory.
type appKeys struct {
	key    *rsa.PrivateKey
	secret secretString // the OAuth client secret
}

// app is the App at global or legacy (nil, nil: not set up).
func (s *srv) app() (*appState, error) {
	s.mu.Lock()
	if s.appC != nil {
		a := *s.appC
		s.mu.Unlock()
		return &a, nil
	}
	s.mu.Unlock()
	var a appState
	if err := s.state.Get("app", &a); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}
	s.mu.Lock()
	s.appC = &a
	s.mu.Unlock()
	b := a
	return &b, nil
}

// mustApp is app, refusing 503 setup while there is none.
func (s *srv) mustApp() (*appState, error) {
	a, err := s.app()
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, refuse(refSetup, "this scm-github isn't set up yet: a manager creates or pastes the GitHub App on its page")
	}
	return a, nil
}

// public reads conf "public" (any mode; zero when unset).
func (s *srv) public() publicConf {
	var p publicConf
	_ = s.conf.Get("public", &p)
	return p
}

// hosts are GitHub's addresses as this instance knows them.
func (s *srv) hosts() (host, apiBase, webBase string) {
	if s.mode == modeUser {
		p := s.public()
		host, apiBase, webBase = p.Host, p.APIBase, p.WebBase
	} else if a, _ := s.app(); a != nil {
		host, apiBase, webBase = a.Host, a.APIBase, a.WebBase
	}
	if apiBase == "" {
		apiBase = s.defaultAPI
	}
	if webBase == "" {
		webBase = s.defaultWeb
	}
	if host == "" {
		if u, err := url.Parse(webBase); err == nil {
			host = u.Hostname()
		}
	}
	return
}

func (s *srv) apiBase() string { _, a, _ := s.hosts(); return a }

// keys loads the App's secrets (global and legacy only).
func (s *srv) keys() (*appKeys, error) {
	s.mu.Lock()
	k := s.keyC
	s.mu.Unlock()
	if k != nil {
		return k, nil
	}
	pemText, err := s.vault.Get(vaultAppKey)
	if err != nil {
		return nil, refuse(refSetup, "the App's private key isn't in this tile's vault: paste the App again")
	}
	key, err := parseAppKey(pemText.Reveal())
	if err != nil {
		return nil, refuse(refSetup, "the App's private key in the vault doesn't parse: paste it again")
	}
	sec, _ := s.vault.Get(vaultAppSecret)
	k = &appKeys{key: key, secret: sec}
	s.mu.Lock()
	s.keyC = k
	s.mu.Unlock()
	return k, nil
}

// appAuth is the App's JWT.
func (s *srv) appAuth() (ghAuth, error) {
	a, err := s.mustApp()
	if err != nil {
		return ghAuth{}, err
	}
	k, err := s.keys()
	if err != nil {
		return ghAuth{}, err
	}
	t, err := s.jwt.get(k.key, a.ClientID, s.now())
	if err != nil {
		return ghAuth{}, err
	}
	return bearerAuth("app", t), nil
}

// basicAuth is the App's client id and secret: the OAuth applications API
// (checking, scoping and revoking people's tokens).
func (s *srv) basicAuth() (ghAuth, string, error) {
	a, err := s.mustApp()
	if err != nil {
		return ghAuth{}, "", err
	}
	k, err := s.keys()
	if err != nil {
		return ghAuth{}, "", err
	}
	if k.secret.Empty() {
		return ghAuth{}, "", refuse(refSetup, "the App's client secret isn't in this tile's vault: paste the App again")
	}
	return ghAuth{key: "basic", user: a.ClientID, pass: k.secret}, a.ClientID, nil
}

// installURL is where an account installs the App.
func (s *srv) installURL(a *appState) string {
	if a == nil || a.Slug == "" {
		return ""
	}
	_, _, web := s.hosts()
	return web + "/apps/" + a.Slug + "/installations/new"
}

// What changed, for writePublic.
const (
	pubTokens = 1 << iota // tokens handed out mustn't be reused (TokenGen moves)
	pubNewApp             // another App: what was checked of the old one goes
)

// writePublic rewrites conf "public" after any change to the App or the
// policy, so people's partitions read the current state.
func (s *srv) writePublic(change int) error {
	a, err := s.app()
	if err != nil {
		return err
	}
	pol := s.policy()
	prev := s.public()
	p := publicConf{DeviceFlow: prev.DeviceFlow, TokenGen: prev.TokenGen, Policy: publicPolicy{BotForPeople: pol.BotForPeople,
		AllowWorkflows: pol.AllowWorkflows, AllowedAccounts: pol.AllowedAccounts, BotRepos: pol.BotRepos}}
	if p.DeviceFlow == "" || change&pubNewApp != 0 {
		p.DeviceFlow = "unknown"
	}
	if change&pubTokens != 0 {
		// Never back to a value a partition saw: the clock, or one more.
		p.TokenGen = max(prev.TokenGen+1, s.now().UnixMilli())
	}
	if a != nil {
		p.ClientID, p.Slug, p.Host, p.APIBase, p.WebBase = a.ClientID, a.Slug, a.Host, a.APIBase, a.WebBase
		p.InstallURL, p.Configured = s.installURL(a), true
		p.Rerun = a.Permissions["actions"] == "write" && pol.AllowRerun
	}
	return s.conf.Put("public", p)
}

// instCache is state "inst/<owner>": the App's installation on an account.
type instCache struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
	At      int64  `json:"at"`
}

// installation finds the App's installation on owner (cached an hour):
// 409 not-installed when there is none.
func (s *srv) installation(ctx context.Context, owner, repo string) (int64, error) {
	key := "inst/" + strings.ToLower(owner)
	var c instCache
	if err := s.state.Get(key, &c); err == nil && c.ID != 0 && s.now().UnixMilli()-c.At < time.Hour.Milliseconds() {
		return c.ID, nil
	}
	a, err := s.mustApp()
	if err != nil {
		return 0, err
	}
	auth, err := s.appAuth()
	if err != nil {
		return 0, err
	}
	var inst struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	_, err = s.gh.call(ctx, auth, http.MethodGet, s.apiBase()+"/repos/"+pathEsc(owner)+"/"+pathEsc(repo)+"/installation", nil, &inst)
	if isRefusal(err, refNotFound) {
		_ = s.state.Delete(key)
		e := refuse(refNotInstalled, "the GitHub App isn't installed on %s (or can't see %s/%s): install it there", owner, owner, repo)
		e.Install = &installInfo{URL: s.installURL(a), Owner: owner}
		return 0, e
	}
	if err != nil {
		return 0, err
	}
	_ = s.state.Put(key, instCache{ID: inst.ID, Account: inst.Account.Login, At: s.now().UnixMilli()})
	return inst.ID, nil
}

// mintedToken is an installation token as GitHub answered it.
type mintedToken struct {
	Token        secretString      `json:"token"`
	ExpiresAt    time.Time         `json:"expires_at"`
	Permissions  map[string]string `json:"permissions"`
	Repositories []struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
	} `json:"repositories"`
}

// mint asks GitHub for an installation token narrowed to repos (names
// without owner; none: every repo of the installation) and permissions.
func (s *srv) mint(ctx context.Context, inst int64, repos []string, perms map[string]string) (*mintedToken, error) {
	auth, err := s.appAuth()
	if err != nil {
		return nil, err
	}
	body := map[string]any{"permissions": perms}
	if len(repos) > 0 {
		body["repositories"] = repos
	}
	var t mintedToken
	if _, err := s.gh.call(ctx, auth, http.MethodPost, s.apiBase()+"/app/installations/"+strconv.FormatInt(inst, 10)+"/access_tokens", body, &t); err != nil {
		if isRefusal(err, refNotFound) {
			_ = s.dropInstallation(inst)
		}
		return nil, err
	}
	if t.Token.Empty() {
		return nil, refuse(refUpstream, "GitHub minted no token")
	}
	return &t, nil
}

// dropInstallation forgets a cached installation (it went away).
func (s *srv) dropInstallation(inst int64) error {
	keys, err := s.state.List("inst/")
	if err != nil {
		return err
	}
	for _, k := range keys {
		var c instCache
		if s.state.Get(k, &c) == nil && c.ID == inst {
			_ = s.state.Delete(k)
		}
	}
	return nil
}

// instAuth is an installation token the tile uses itself (reads as the
// bot; kind "write": the writes made as the bot), cached and never handed
// out. Read: the read preset over every repo; write: pull requests and
// issues only.
func (s *srv) instAuth(ctx context.Context, owner, repo, kind string) (ghAuth, error) {
	inst, err := s.installation(ctx, owner, repo)
	if err != nil {
		return ghAuth{}, err
	}
	perms := presetRead()
	if kind == "write" {
		perms = map[string]string{"pull_requests": "write", "issues": "write", "metadata": "read"}
	}
	key := cacheKey{consumer: "internal", purpose: kind, inst: inst, perms: permsKey(perms)}
	if e := s.intl.get(key, s.now(), 15*time.Minute); e != nil {
		return bearerAuth(fmt.Sprintf("inst:%d", inst), e.token), nil
	}
	t, err := s.mint(ctx, inst, nil, perms)
	if err != nil {
		return ghAuth{}, err
	}
	s.intl.put(key, &cachedToken{token: t.Token, expiresAt: t.ExpiresAt, perms: t.Permissions})
	return bearerAuth(fmt.Sprintf("inst:%d", inst), t.Token), nil
}

// permsKey is a permission map in a stable order.
func permsKey(p map[string]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + p[k] + ",")
	}
	return b.String()
}
