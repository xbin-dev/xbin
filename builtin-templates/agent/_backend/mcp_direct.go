// mcp_direct.go — MCP servers connected by URL (API.md "Direct MCP
// servers"): a person adds a streamable-HTTP MCP server's address in
// Settings → MCP, as theirs (personal) or — a manager — for everyone
// (global). One that wants a sign-in is signed in to with OAuth
// (mcp_oauth.go): the page opens the server's authorization page, which
// sends the person back to mcp-callback.html, which hands the code to
// POST /mcp/oauth/callback. One that takes a static token is given it.
//
// Where they live: a person's own servers in their own partition (an
// unpartitioned agent: its one db, with their owner), the global ones in
// the global instance — each home's row in mcp_servers, its tokens in that
// home's vault (never a row, never the conf mirror everyone can read). A
// person's partition reaches a global server through the global instance
// (POST /mcp/servers/{id}/rpc there), so a global sign-in's tokens never
// leave it.
//
// Their tools join the bound and static servers' (mcp.go) as
// mcp:<name>:<tool>, under the same rules: offered only to a class with
// internal reach whose mcp list allows the name; a person's own server only
// in conversations that person owns (iface_personal.go). A personal
// server's name is my-<slug>, a global one's <slug>.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	schemaAdds = append(schemaAdds, (*DB).addDirectMCPSchema)
	routeTables = append(routeTables, directMCPRoutes)
}

const directMCPSQL = `
CREATE TABLE IF NOT EXISTS mcp_servers (
  id          TEXT PRIMARY KEY,
  slug        TEXT NOT NULL,
  url         TEXT NOT NULL,
  scope       TEXT NOT NULL,
  owner       TEXT NOT NULL DEFAULT '',
  auth        TEXT NOT NULL DEFAULT 'none',
  state       TEXT NOT NULL DEFAULT 'ready',
  error       TEXT NOT NULL DEFAULT '',
  meta        TEXT NOT NULL DEFAULT '{}',
  client_id   TEXT NOT NULL DEFAULT '',
  redirect    TEXT NOT NULL DEFAULT '',
  title       TEXT NOT NULL DEFAULT '',
  created_by  TEXT NOT NULL DEFAULT '',
  created_ms  INTEGER NOT NULL DEFAULT 0,
  updated_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_servers_slug ON mcp_servers(scope, owner, slug);
CREATE TABLE IF NOT EXISTS mcp_oauth_pending (
  state       TEXT PRIMARY KEY,
  server_id   TEXT NOT NULL,
  verifier    TEXT NOT NULL,
  redirect    TEXT NOT NULL,
  by_user     TEXT NOT NULL,
  created_ms  INTEGER NOT NULL
);`

func (d *DB) addDirectMCPSchema() error {
	_, err := d.q.Exec(directMCPSQL)
	return err
}

// Scopes, auth kinds and states.
const (
	mcpPersonal = "personal"
	mcpGlobal   = "global"

	mcpAuthNone   = "none"
	mcpAuthOAuth  = "oauth"
	mcpAuthBearer = "bearer"

	mcpReady  = "ready"  // usable
	mcpSignin = "signin" // waits for a (new) sign-in
)

// mcpPendingTTL bounds a sign-in between its start and its callback.
const mcpPendingTTL = 15 * time.Minute

// directServer is an mcp_servers row.
type directServer struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	URL       string    `json:"url"`
	Scope     string    `json:"scope"`
	Owner     string    `json:"owner,omitempty"`
	Auth      string    `json:"auth"`
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	Meta      oauthMeta `json:"-"`
	ClientID  string    `json:"-"`
	Redirect  string    `json:"-"`
	Title     string    `json:"title,omitempty"` // the server's own name (initialize's serverInfo, the resource's name)
	CreatedBy string    `json:"createdBy,omitempty"`
	CreatedMs int64     `json:"created"`
	UpdatedMs int64     `json:"updated"`
}

// name is how its tools are named (mcp:<name>:<tool>) and how a class's
// mcp list names it.
func (s *directServer) name() string {
	if s.Scope == mcpPersonal {
		return "my-" + s.Slug
	}
	return s.Slug
}

// directView is a row as the page sees it.
type directView struct {
	directServer
	Name     string   `json:"name"`
	Issuer   string   `json:"issuer,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	Tools    []string `json:"tools"`
	ToolsAt  int64    `json:"toolsAt,omitempty"`
	CanEdit  bool     `json:"canEdit"`
	SignedIn bool     `json:"signedIn"`
}

const directCols = `id, slug, url, scope, owner, auth, state, error, meta, client_id, redirect, title, created_by, created_ms, updated_ms`

func scanDirect(sc interface{ Scan(...any) error }) (*directServer, error) {
	var s directServer
	var meta string
	if err := sc.Scan(&s.ID, &s.Slug, &s.URL, &s.Scope, &s.Owner, &s.Auth, &s.State, &s.Error, &meta, &s.ClientID, &s.Redirect,
		&s.Title, &s.CreatedBy, &s.CreatedMs, &s.UpdatedMs); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(meta), &s.Meta)
	return &s, nil
}

func (d *DB) directServers() []*directServer {
	rows, err := d.q.Query(`SELECT ` + directCols + ` FROM mcp_servers ORDER BY scope, slug`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*directServer
	for rows.Next() {
		if s, err := scanDirect(rows); err == nil {
			out = append(out, s)
		}
	}
	return out
}

func (d *DB) directServer(id string) (*directServer, error) {
	return scanDirect(d.q.QueryRow(`SELECT `+directCols+` FROM mcp_servers WHERE id=?`, id))
}

func (d *DB) setDirectState(id, state, errText string) {
	_, _ = d.q.Exec(`UPDATE mcp_servers SET state=?, error=?, updated_ms=? WHERE id=?`, state, clip(errText, 500), nowMs(), id)
}

// --- the vault ------------------------------------------------------------------------------

// The tokens live in this home's vault (signinVault: the SDK's, tests'
// memVault), one entry per server.
func directVaultKey(id string) string { return "mcp-direct." + id }

func directTokens(id string) oauthTokens {
	var t oauthTokens
	if raw, err := signinVault.get(directVaultKey(id)); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &t)
	}
	return t
}

func putDirectTokens(id string, t oauthTokens) error {
	b, _ := json.Marshal(t)
	return signinVault.set(directVaultKey(id), string(b))
}

// --- who may ---------------------------------------------------------------------------------

// directHome is the scope this process keeps: a person's partition their
// own servers, the global instance the global ones, an unpartitioned agent
// both.
func directHomeKeeps(scope string) bool {
	switch {
	case userMode():
		return scope == mcpPersonal
	case globalMode():
		return scope == mcpGlobal
	}
	return scope == mcpPersonal || scope == mcpGlobal
}

// ownPerson: c is a person acting as themself (not viewed as) — and, in a
// person's partition, that partition's person.
func ownPerson(c who) bool {
	return c.kind == whoUser && c.viewedBy == "" && (!userMode() || c.user == runUser)
}

// canEditDirect: c may change s (remove it, sign it in or out).
func canEditDirect(c who, s *directServer) bool {
	if s.Scope == mcpGlobal {
		return c.manager() && c.viewedBy == ""
	}
	return ownPerson(c) && (userMode() || c.user == s.Owner)
}

// visibleDirect: c sees s in the list (a global one everyone who may open
// the agent; a personal one its person).
func visibleDirect(c who, s *directServer) bool {
	if s.Scope == mcpGlobal {
		return true
	}
	if userMode() {
		return c.kind == whoUser && c.user == runUser || c.kind == whoSystem
	}
	return c.kind == whoUser && c.user == s.Owner || c.kind == whoSystem
}

// --- routes ----------------------------------------------------------------------------------

func directMCPRoutes() []routeDef {
	return []routeDef{
		{"GET /mcp/servers", needAny, handleListDirect},
		{"POST /mcp/servers", needAny, handleAddDirect},
		{"DELETE /mcp/servers/{sid}", needAny, handleRemoveDirect},
		{"POST /mcp/servers/{sid}/signin", needAny, handleDirectSignin},
		{"POST /mcp/servers/{sid}/signout", needAny, handleDirectSignout},
		{"POST /mcp/servers/{sid}/refresh", needAny, handleDirectRefresh},
		{"POST /mcp/servers/{sid}/rpc", needAny, handleDirectRPC},
		{"POST /mcp/oauth/callback", needAny, handleDirectCallback},
	}
}

func (s *directServer) view(c who) directView {
	v := directView{directServer: *s, Name: s.name(), Issuer: s.Meta.Issuer, Scopes: s.Meta.Scopes, Tools: []string{}, CanEdit: canEditDirect(c, s)}
	if l, ok := agent.cachedMCPTools(s.mcpServer()); ok {
		v.ToolsAt = l.At
		for _, t := range l.Tools {
			v.Tools = append(v.Tools, strings.TrimPrefix(t.Function.Name, "mcp:"+s.name()+":"))
		}
	}
	switch s.Auth {
	case mcpAuthOAuth:
		v.SignedIn = s.State != mcpSignin && directTokens(s.ID).Access != ""
	case mcpAuthBearer:
		v.SignedIn = true
	}
	return v
}

// handleListDirect: GET /mcp/servers — the servers this home keeps that the
// caller may see, and whether they may add a global one here.
func handleListDirect(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	out := []directView{}
	for _, s := range agent.db.directServers() {
		if directHomeKeeps(s.Scope) && visibleDirect(c, s) {
			out = append(out, s.view(c))
		}
	}
	xbin.WriteJSON(w, 200, map[string]any{
		"servers":   out,
		"personal":  directHomeKeeps(mcpPersonal),
		"global":    directHomeKeeps(mcpGlobal),
		"canGlobal": directHomeKeeps(mcpGlobal) && c.manager() && c.viewedBy == "",
	})
}

var slugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// checkDirectURL: an https address (http only to a loopback host — a
// sign-in's tokens never cross a network in the clear).
func checkDirectURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("url: an https:// address of a streamable-HTTP MCP server")
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("url: https:// only (http:// is for a server on this machine)")
		}
	}
	if u.User != nil {
		return "", errors.New("url: no credentials in the address — paste a token instead")
	}
	u.Fragment = ""
	return u.String(), nil
}

// handleAddDirect: POST /mcp/servers {url, name?, scope, token?} — the
// server is probed: it answers (auth none), takes the token (bearer), or
// wants an OAuth sign-in (state signin: POST …/signin next).
func handleAddDirect(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	var body struct {
		URL   string `json:"url"`
		Name  string `json:"name"`
		Scope string `json:"scope"`
		Token string `json:"token"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	scope := orStr(body.Scope, mcpPersonal)
	switch {
	case scope != mcpPersonal && scope != mcpGlobal:
		xbin.WriteError(w, 400, `scope: "personal" or "global"`)
		return
	case !directHomeKeeps(scope) && scope == mcpGlobal:
		xbin.WriteError(w, http.StatusConflict, "a global MCP server is added in the agent's shared space (?xbin-partition=global)")
		return
	case !directHomeKeeps(scope):
		xbin.WriteError(w, http.StatusConflict, "your own MCP servers are added in your own space")
		return
	case scope == mcpGlobal && (!c.manager() || c.viewedBy != ""):
		xbin.WriteError(w, http.StatusForbidden, "adding an MCP server for everyone needs write access to this tile")
		return
	case scope == mcpPersonal && !ownPerson(c):
		xbin.WriteError(w, http.StatusForbidden, "a personal MCP server is a person's own: add it yourself")
		return
	}
	u, err := checkDirectURL(body.URL)
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	slug := strings.TrimSpace(strings.ToLower(body.Name))
	if slug == "" {
		slug = slugFromURL(u)
	}
	switch {
	case !slugRE.MatchString(slug):
		xbin.WriteError(w, 400, "name: a–z, 0–9 and -, a letter first, at most 32")
		return
	case scope == mcpGlobal && strings.HasPrefix(slug, "my-"):
		xbin.WriteError(w, 400, `name: "my-" starts a personal server's name`)
		return
	}
	owner := ""
	if scope == mcpPersonal {
		owner = c.user
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	s := &directServer{ID: "mcp" + strings.ToLower(randToken(9)), Slug: slug, URL: u, Scope: scope, Owner: owner, CreatedBy: c.tag(),
		CreatedMs: nowMs(), UpdatedMs: nowMs(), State: mcpReady}
	s.ID = strings.NewReplacer("-", "", "_", "").Replace(s.ID)
	var headers map[string]string
	if tok := strings.TrimSpace(body.Token); tok != "" {
		headers = map[string]string{"Authorization": "Bearer " + tok}
	}
	pr, err := probeMCP(ctx, u, headers)
	switch {
	case err != nil:
		xbin.WriteError(w, http.StatusBadGateway, fmt.Sprintf("reaching %s: %v", u, err))
		return
	case pr.OK && headers != nil:
		s.Auth, s.Title = mcpAuthBearer, pr.ServerName
	case pr.OK:
		s.Auth, s.Title = mcpAuthNone, pr.ServerName
	case pr.Status == http.StatusUnauthorized && headers != nil:
		xbin.WriteError(w, 400, fmt.Sprintf("%s refused the token (HTTP 401)", u))
		return
	case pr.Status == http.StatusUnauthorized:
		m, err := discoverOAuth(ctx, u, pr)
		if err != nil {
			xbin.WriteError(w, http.StatusBadGateway, fmt.Sprintf("%s wants a sign-in, but %v", u, err))
			return
		}
		s.Auth, s.State, s.Meta, s.Title = mcpAuthOAuth, mcpSignin, m, m.ResourceName
	default:
		xbin.WriteError(w, http.StatusBadGateway, fmt.Sprintf("%s answered HTTP %d to an MCP initialize: is it a streamable-HTTP MCP server?", u, pr.Status))
		return
	}
	if s.Auth == mcpAuthBearer {
		if err := putDirectTokens(s.ID, oauthTokens{Bearer: strings.TrimSpace(body.Token)}); err != nil {
			xbin.WriteError(w, http.StatusServiceUnavailable, "keeping the token in the vault: "+err.Error())
			return
		}
	}
	meta, _ := json.Marshal(s.Meta)
	if _, err := agent.db.q.Exec(`INSERT INTO mcp_servers (`+directCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, '', '', ?, ?, ?, ?)`,
		s.ID, s.Slug, s.URL, s.Scope, s.Owner, s.Auth, s.State, string(meta), clip(s.Title, 120), s.CreatedBy, s.CreatedMs, s.UpdatedMs); err != nil {
		_ = signinVault.del(directVaultKey(s.ID))
		if strings.Contains(err.Error(), "UNIQUE") {
			xbin.WriteError(w, http.StatusConflict, fmt.Sprintf("name: there is a server named %s already", s.name()))
			return
		}
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if s.State == mcpReady {
		_, _ = agent.discoverMCPTools(ctx, s.mcpServer())
	}
	xbin.WriteJSON(w, 201, map[string]any{"server": s.view(c)})
}

// slugFromURL names a server after its host: api.ottotheagent.com → ottotheagent.
func slugFromURL(u string) string {
	pu, _ := url.Parse(u)
	parts := strings.Split(pu.Hostname(), ".")
	pick := parts[0]
	if len(parts) >= 2 {
		pick = parts[len(parts)-2]
	}
	pick = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(pick), "-")
	pick = strings.Trim(pick, "-")
	if pick == "" || pick[0] < 'a' || pick[0] > 'z' {
		pick = "mcp-" + pick
	}
	return clip(pick, 32)
}

// directFor loads {sid} and checks c may change it (404 when they may not see it).
func directFor(w http.ResponseWriter, r *http.Request) (*directServer, who, bool) {
	c := callerOf(r)
	s, err := agent.db.directServer(r.PathValue("sid"))
	if err != nil || !visibleDirect(c, s) {
		xbin.WriteError(w, 404, "no such MCP server")
		return nil, c, false
	}
	if !canEditDirect(c, s) {
		xbin.WriteError(w, http.StatusForbidden, "only its person (a global one: a manager) changes this MCP server")
		return nil, c, false
	}
	return s, c, true
}

func handleRemoveDirect(w http.ResponseWriter, r *http.Request) {
	s, _, ok := directFor(w, r)
	if !ok {
		return
	}
	_ = signinVault.del(directVaultKey(s.ID))
	_, _ = agent.db.q.Exec(`DELETE FROM mcp_servers WHERE id=?`, s.ID)
	_, _ = agent.db.q.Exec(`DELETE FROM mcp_oauth_pending WHERE server_id=?`, s.ID)
	_, _ = agent.db.q.Exec(`DELETE FROM settings WHERE k=?`, mcpCacheKey(s.mcpServer()))
	w.WriteHeader(http.StatusNoContent)
}

// handleDirectSignin: POST /mcp/servers/{sid}/signin {redirectUri} — the
// authorization page to open ({url}). The client is registered for
// redirectUri once (again when it changes).
func handleDirectSignin(w http.ResponseWriter, r *http.Request) {
	s, c, ok := directFor(w, r)
	if !ok {
		return
	}
	if !ownPerson(c) {
		xbin.WriteError(w, http.StatusForbidden, "a sign-in is a person's, as themself")
		return
	}
	if s.Auth != mcpAuthOAuth {
		xbin.WriteError(w, http.StatusConflict, "this server takes no sign-in")
		return
	}
	var body struct {
		RedirectURI string `json:"redirectUri"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	ru, err := url.Parse(body.RedirectURI)
	if err != nil || (ru.Scheme != "https" && ru.Scheme != "http") || ru.Host == "" || !strings.HasSuffix(ru.Path, "/mcp-callback.html") {
		xbin.WriteError(w, 400, "redirectUri: this tile's mcp-callback.html, as the page's address says it")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if s.ClientID == "" || s.Redirect != body.RedirectURI {
		id, secret, err := registerClient(ctx, s.Meta, body.RedirectURI)
		if err != nil {
			xbin.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
		t := directTokens(s.ID)
		t.ClientSecret = secret
		if err := putDirectTokens(s.ID, t); err != nil {
			xbin.WriteError(w, http.StatusServiceUnavailable, "the vault: "+err.Error())
			return
		}
		s.ClientID, s.Redirect = id, body.RedirectURI
		_, _ = agent.db.q.Exec(`UPDATE mcp_servers SET client_id=?, redirect=?, updated_ms=? WHERE id=?`, id, body.RedirectURI, nowMs(), s.ID)
	}
	verifier, challenge := pkce()
	home := "p" // where the callback page sends the code back to
	if globalMode() {
		home = "g"
	}
	state := home + randToken(24)
	_, _ = agent.db.q.Exec(`DELETE FROM mcp_oauth_pending WHERE created_ms<?`, nowMs()-mcpPendingTTL.Milliseconds())
	if _, err := agent.db.q.Exec(`INSERT INTO mcp_oauth_pending (state, server_id, verifier, redirect, by_user, created_ms) VALUES (?, ?, ?, ?, ?, ?)`,
		state, s.ID, verifier, body.RedirectURI, c.user, nowMs()); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"url": authorizeURL(s.Meta, s.ClientID, body.RedirectURI, state, challenge)})
}

// handleDirectCallback: POST /mcp/oauth/callback {state, code, iss?, error?}
// — what the authorization server sent the person back with; the code is
// exchanged for tokens, which go to the vault.
func handleDirectCallback(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	var body struct {
		State string `json:"state"`
		Code  string `json:"code"`
		Iss   string `json:"iss"`
		Error string `json:"error"`
		Desc  string `json:"errorDescription"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	var serverID, verifier, redirect, by string
	var created int64
	err := agent.db.q.QueryRow(`SELECT server_id, verifier, redirect, by_user, created_ms FROM mcp_oauth_pending WHERE state=?`, body.State).
		Scan(&serverID, &verifier, &redirect, &by, &created)
	if err != nil || nowMs()-created > mcpPendingTTL.Milliseconds() {
		xbin.WriteError(w, 400, "this sign-in isn't one waiting here (it expired, or was finished already): start it again")
		return
	}
	if c.kind != whoUser || c.user != by || c.viewedBy != "" {
		xbin.WriteError(w, http.StatusForbidden, "this sign-in was started by someone else")
		return
	}
	_, _ = agent.db.q.Exec(`DELETE FROM mcp_oauth_pending WHERE state=?`, body.State) // once
	s, err := agent.db.directServer(serverID)
	if err != nil {
		xbin.WriteError(w, 404, "the MCP server was removed meanwhile")
		return
	}
	if body.Error != "" {
		xbin.WriteError(w, 400, fmt.Sprintf("the sign-in was refused: %s", clip(strings.TrimSpace(body.Error+": "+body.Desc), 300)))
		return
	}
	if s.Meta.IssParam && body.Iss != "" && s.Meta.Issuer != "" && body.Iss != s.Meta.Issuer {
		xbin.WriteError(w, 400, "the sign-in came back from another authorization server than the one asked (iss)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	prev := directTokens(s.ID)
	t, err := tokenGrant(ctx, s.Meta, s.ClientID, prev.ClientSecret, url.Values{
		"grant_type": {"authorization_code"}, "code": {body.Code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "exchanging the code: "+err.Error())
		return
	}
	if err := putDirectTokens(s.ID, t); err != nil {
		xbin.WriteError(w, http.StatusServiceUnavailable, "the vault: "+err.Error())
		return
	}
	agent.db.setDirectState(s.ID, mcpReady, "")
	s.State, s.Error = mcpReady, ""
	_, _ = agent.discoverMCPTools(ctx, s.mcpServer())
	xbin.WriteJSON(w, 200, map[string]any{"server": s.view(c), "home": map[bool]string{true: "global", false: ""}[globalMode()]})
}

// handleDirectSignout: POST /mcp/servers/{sid}/signout — its tokens out of
// the vault (the client registration stays).
func handleDirectSignout(w http.ResponseWriter, r *http.Request) {
	s, c, ok := directFor(w, r)
	if !ok {
		return
	}
	if s.Auth != mcpAuthOAuth {
		xbin.WriteError(w, http.StatusConflict, "this server takes no sign-in")
		return
	}
	t := directTokens(s.ID)
	if err := putDirectTokens(s.ID, oauthTokens{ClientSecret: t.ClientSecret}); err != nil {
		xbin.WriteError(w, http.StatusServiceUnavailable, "the vault: "+err.Error())
		return
	}
	agent.db.setDirectState(s.ID, mcpSignin, "")
	s, _ = agent.db.directServer(s.ID)
	xbin.WriteJSON(w, 200, map[string]any{"server": s.view(c)})
}

// handleDirectRefresh: POST /mcp/servers/{sid}/refresh — its tools listed again now.
func handleDirectRefresh(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	s, err := agent.db.directServer(r.PathValue("sid"))
	if err != nil || !visibleDirect(c, s) {
		xbin.WriteError(w, 404, "no such MCP server")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := listMCPTools(ctx, s.mcpServer()); err != nil {
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	_, _ = agent.discoverMCPTools(ctx, s.mcpServer())
	s, _ = agent.db.directServer(s.ID)
	xbin.WriteJSON(w, 200, map[string]any{"server": s.view(c)})
}

// handleDirectRPC: POST /mcp/servers/{sid}/rpc {method, params, session} at
// the global instance — a person's partition using a global server for one
// of that person's conversations (directRPC); the answer is {result,
// session} or an error. Its tokens stay here.
func handleDirectRPC(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	s, err := agent.db.directServer(r.PathValue("sid"))
	if err != nil || s.Scope != mcpGlobal || !globalMode() || (c.kind != whoUser && c.kind != whoSystem) {
		xbin.WriteError(w, 404, "no such global MCP server")
		return
	}
	var body struct {
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		Session string          `json:"session"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	switch body.Method {
	case "initialize", "tools/list", "tools/call":
	default:
		xbin.WriteError(w, 400, "method: initialize, tools/list or tools/call")
		return
	}
	var params any
	if len(body.Params) > 0 {
		params = body.Params
	}
	res, sid, err := s.localRPC(r.Context(), body.Method, params, body.Session)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errNeedsSignin) {
			status = http.StatusConflict
		}
		xbin.WriteJSON(w, status, map[string]any{"error": err.Error(), "signin": errors.Is(err, errNeedsSignin)})
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"result": res, "session": sid})
}

// --- using them --------------------------------------------------------------------------------

// mcpServer is s as mcp.go's client takes it (rpc goes through directRPC).
func (s *directServer) mcpServer() MCPServer {
	return MCPServer{Name: s.name(), URL: "direct:" + s.ID, direct: s, personal: s.Scope == mcpPersonal && userMode()}
}

// directMCPServers is what ctx's conversation may use: this home's ready
// global servers, its person's own (an unpartitioned agent: the
// conversation owner's), and — in a person's partition — the global
// instance's, reached through it.
func directMCPServers(ctx context.Context) []MCPServer {
	if agent == nil || agent.db == nil {
		return nil
	}
	var out []MCPServer
	owner := mcpOwnerOf(ctx)
	for _, s := range agent.db.directServers() {
		if s.State == mcpSignin || !directHomeKeeps(s.Scope) {
			continue
		}
		if s.Scope == mcpPersonal && !userMode() && (owner == "" || owner != s.Owner) {
			continue
		}
		out = append(out, s.mcpServer())
	}
	if userMode() {
		out = append(out, globalDirectServers(ctx)...)
	}
	return out
}

// The global instance's servers as a person's partition sees them (its
// list, a minute at a time).
var (
	globalDirectMu   sync.Mutex
	globalDirectAt   time.Time
	globalDirectList []MCPServer
)

func globalDirectServers(ctx context.Context) []MCPServer {
	globalDirectMu.Lock()
	defer globalDirectMu.Unlock()
	if time.Since(globalDirectAt) < time.Minute {
		return globalDirectList
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := callGlobal(cctx, http.MethodGet, "/mcp/servers", nil, "")
	globalDirectAt = time.Now()
	if err != nil || res.Status != 200 {
		return globalDirectList // the last known, until it answers again
	}
	var list struct {
		Servers []directView `json:"servers"`
	}
	if json.Unmarshal(res.Body, &list) != nil {
		return globalDirectList
	}
	globalDirectList = nil
	for _, v := range list.Servers {
		if v.Scope != mcpGlobal || v.State == mcpSignin {
			continue
		}
		s := v.directServer
		globalDirectList = append(globalDirectList, MCPServer{Name: s.name(), URL: "global:" + s.ID, direct: &s, viaGlobal: true})
	}
	return globalDirectList
}

// directRPC is rpc for a direct server: at the global instance for a
// partition's global server, else here with its token — refreshed when it is
// near its end or the server says 401, the sign-in marked needed when that
// fails.
func directRPC(ctx context.Context, srv MCPServer, method string, params any, sessionID string) (json.RawMessage, string, error) {
	s := srv.direct
	if srv.viaGlobal {
		body, _ := json.Marshal(map[string]any{"method": method, "params": params, "session": sessionID})
		res, err := callGlobal(ctx, http.MethodPost, "/mcp/servers/"+s.ID+"/rpc", body, "application/json")
		if err != nil {
			return nil, sessionID, err
		}
		var out struct {
			Result  json.RawMessage `json:"result"`
			Session string          `json:"session"`
			Error   string          `json:"error"`
		}
		_ = json.Unmarshal(res.Body, &out)
		if res.Status != 200 {
			if out.Error == "" {
				out.Error = fmt.Sprintf("HTTP %d", res.Status)
			}
			return nil, sessionID, fmt.Errorf("mcp %s (global %s): %s", method, s.name(), out.Error)
		}
		return out.Result, out.Session, nil
	}
	return s.localRPC(ctx, method, params, sessionID)
}

func (s *directServer) localRPC(ctx context.Context, method string, params any, sessionID string) (json.RawMessage, string, error) {
	plain := MCPServer{Name: s.name(), URL: s.URL, external: true}
	if s.Auth == mcpAuthNone {
		return plain.rpc(ctx, method, params, sessionID)
	}
	tok, err := s.token(ctx, false)
	if err != nil {
		return nil, sessionID, err
	}
	plain.Headers = map[string]string{"Authorization": "Bearer " + tok}
	res, sid, err := plain.rpc(ctx, method, params, sessionID)
	var he *mcpHTTPError
	if errors.As(err, &he) && he.Status == http.StatusUnauthorized && s.Auth == mcpAuthOAuth {
		if tok, err = s.token(ctx, true); err != nil {
			return nil, sessionID, err
		}
		plain.Headers["Authorization"] = "Bearer " + tok
		res, sid, err = plain.rpc(ctx, method, params, sessionID)
		if errors.As(err, &he) && he.Status == http.StatusUnauthorized {
			agent.db.setDirectState(s.ID, mcpSignin, "the server refused the sign-in (HTTP 401)")
			return nil, sessionID, fmt.Errorf("mcp %s %s: %w — sign in again in Settings → MCP", s.name(), method, errNeedsSignin)
		}
	}
	return res, sid, err
}

// directRefreshMu serialises a server's refreshes (a refresh token is often
// single-use).
var directRefreshMu sync.Map // id → *sync.Mutex

// token is s's bearer: the pasted one, or the OAuth access token —
// refreshed when it ends within a minute, or when force.
func (s *directServer) token(ctx context.Context, force bool) (string, error) {
	t := directTokens(s.ID)
	if s.Auth == mcpAuthBearer {
		if t.Bearer == "" {
			return "", fmt.Errorf("mcp %s: its token is gone from the vault: add it again", s.name())
		}
		return t.Bearer, nil
	}
	fresh := func(t oauthTokens) bool {
		return t.Access != "" && (t.ExpiresMs == 0 || nowMs() < t.ExpiresMs-60_000)
	}
	if !force && fresh(t) {
		return t.Access, nil
	}
	mu, _ := directRefreshMu.LoadOrStore(s.ID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if again := directTokens(s.ID); again.Access != t.Access && fresh(again) {
		return again.Access, nil // refreshed by another call meanwhile
	}
	if t.Refresh == "" {
		agent.db.setDirectState(s.ID, mcpSignin, "")
		return "", fmt.Errorf("mcp %s: %w — sign in in Settings → MCP", s.name(), errNeedsSignin)
	}
	nt, err := tokenGrant(ctx, s.Meta, s.ClientID, t.ClientSecret, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}})
	if err != nil {
		if errors.Is(err, errNeedsSignin) {
			agent.db.setDirectState(s.ID, mcpSignin, err.Error())
			return "", fmt.Errorf("mcp %s: %w — sign in again in Settings → MCP", s.name(), errNeedsSignin)
		}
		return "", fmt.Errorf("mcp %s: refreshing its sign-in: %v", s.name(), err)
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh // a server that keeps the refresh token says none
	}
	if err := putDirectTokens(s.ID, nt); err != nil {
		return "", fmt.Errorf("mcp %s: the vault: %v", s.name(), err)
	}
	return nt.Access, nil
}

// --- the conversation's owner (an unpartitioned agent's personal servers) ------------------

type mcpOwnerKey struct{}

// withMCPOwner marks ctx with its conversation's owner (an unpartitioned
// agent: whose personal servers it may use).
func withMCPOwner(ctx context.Context, owner string) context.Context {
	if owner == "" {
		return ctx
	}
	return context.WithValue(ctx, mcpOwnerKey{}, owner)
}

func mcpOwnerOf(ctx context.Context) string {
	o, _ := ctx.Value(mcpOwnerKey{}).(string)
	return o
}
