// mcp_oauth.go — the MCP authorization spec (2025-06-18) as a direct MCP
// server's client needs it (mcp_direct.go): finding out whether a server
// wants a sign-in (its 401's WWW-Authenticate, RFC 9728 protected resource
// metadata), its authorization server (RFC 8414 metadata, else OpenID
// discovery, else the 2025-03-26 default endpoints), dynamic client
// registration (RFC 7591, a public client), the authorization code grant
// with PKCE S256 and the resource parameter (RFC 8707), and refreshing.
// Nothing here stores anything: mcp_direct.go keeps the metadata in its row
// and the tokens in the vault.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// directHTTP reaches direct MCP servers and their authorization servers
// (outside the workspace: the agent's net slot). A var so tests can swap it.
var directHTTP = &http.Client{Timeout: 30 * time.Second}

// oauthMeta is what discovery learnt about a server's authorization.
type oauthMeta struct {
	Resource     string   `json:"resource"`               // the resource indicator sent with every grant
	Issuer       string   `json:"issuer,omitempty"`       // the authorization server
	AuthorizeURL string   `json:"authorizeUrl"`           // its authorization endpoint
	TokenURL     string   `json:"tokenUrl"`               // its token endpoint
	RegisterURL  string   `json:"registerUrl,omitempty"`  // its dynamic client registration endpoint
	Scopes       []string `json:"scopes,omitempty"`       // asked for in the authorization request
	IssParam     bool     `json:"issParam,omitempty"`     // it says iss in its authorization response (RFC 9207)
	ResourceName string   `json:"resourceName,omitempty"` // the protected resource's own name, for the UI
}

// oauthTokens is a sign-in's tokens (the vault's, never a row's).
type oauthTokens struct {
	Access       string `json:"access,omitempty"`
	Refresh      string `json:"refresh,omitempty"`
	ExpiresMs    int64  `json:"expiresMs,omitempty"`    // 0: the server didn't say
	ClientSecret string `json:"clientSecret,omitempty"` // a confidential client's, when registration gave one
	Bearer       string `json:"bearer,omitempty"`       // a static token a person pasted (auth "bearer")
}

// errNeedsSignin: the server takes no request without a sign-in that is
// missing, expired past refreshing, or revoked.
var errNeedsSignin = errors.New("needs a sign-in")

// probeResult is what one unauthenticated initialize found.
type probeResult struct {
	OK         bool   // the server answered: no sign-in needed
	Status     int    // its HTTP status otherwise
	ResMeta    string // WWW-Authenticate's resource_metadata
	Scope      string // WWW-Authenticate's scope
	ServerName string // initialize's serverInfo.name
}

// probeMCP sends initialize with headers (none: unauthenticated) and says
// what came back. A transport error is an error; any HTTP answer is a result.
func probeMCP(ctx context.Context, server string, headers map[string]string) (probeResult, error) {
	body, _ := json.Marshal(rpcReq{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "xbin-agent", "version": "0"}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server, bytes.NewReader(body))
	if err != nil {
		return probeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := directHTTP.Do(req)
	if err != nil {
		return probeResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	pr := probeResult{Status: resp.StatusCode}
	if resp.StatusCode/100 == 2 {
		payload := raw
		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			payload = sseFirstData(raw)
		}
		var rr struct {
			Result struct {
				ServerInfo struct {
					Name string `json:"name"`
				} `json:"serverInfo"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &rr) != nil {
			return pr, fmt.Errorf("it answered HTTP %d, but not as an MCP server (not JSON-RPC)", resp.StatusCode)
		}
		if rr.Error != nil {
			return pr, fmt.Errorf("its initialize failed: %s", rr.Error.Message)
		}
		pr.OK, pr.ServerName = true, rr.Result.ServerInfo.Name
		return pr, nil
	}
	params := wwwAuthParams(resp.Header.Get("WWW-Authenticate"))
	pr.ResMeta, pr.Scope = params["resource_metadata"], params["scope"]
	return pr, nil
}

// wwwAuthParams reads a Bearer challenge's parameters (quoted or not).
func wwwAuthParams(h string) map[string]string {
	out := map[string]string{}
	h = strings.TrimSpace(h)
	if len(h) < 6 || !strings.EqualFold(h[:6], "bearer") {
		return out
	}
	rest := h[6:]
	for len(rest) > 0 {
		rest = strings.TrimLeft(rest, " ,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimLeft(v, " ")
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				out[k] = v[1:]
				break
			}
			out[k], rest = v[1:1+end], v[2+end:]
		} else {
			val, after, _ := strings.Cut(v, ",")
			out[k], rest = strings.TrimSpace(val), after
		}
	}
	return out
}

// discoverOAuth finds a server's authorization from its 401 (pr).
func discoverOAuth(ctx context.Context, server string, pr probeResult) (oauthMeta, error) {
	u, err := url.Parse(server)
	if err != nil {
		return oauthMeta{}, err
	}
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimRight(u.Path, "/")
	// the protected resource's metadata: the challenge's, else the well-known places
	cands := []string{}
	if pr.ResMeta != "" {
		cands = append(cands, pr.ResMeta)
	}
	if path != "" {
		cands = append(cands, origin+"/.well-known/oauth-protected-resource"+path)
	}
	cands = append(cands, origin+"/.well-known/oauth-protected-resource")
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
		ResourceName         string   `json:"resource_name"`
	}
	for _, c := range cands {
		if getJSON(ctx, c, &prm) == nil && len(prm.AuthorizationServers) > 0 {
			break
		}
	}
	m := oauthMeta{Resource: orStr(prm.Resource, server), ResourceName: prm.ResourceName}
	issuer := origin // 2025-03-26: without resource metadata the server's origin is its authorization server
	if len(prm.AuthorizationServers) > 0 {
		issuer = strings.TrimRight(prm.AuthorizationServers[0], "/")
	}
	switch {
	case len(prm.ScopesSupported) > 0:
		m.Scopes = prm.ScopesSupported
	case pr.Scope != "":
		m.Scopes = strings.Fields(pr.Scope)
	}
	iu, err := url.Parse(issuer)
	if err != nil || iu.Host == "" {
		return m, fmt.Errorf("its authorization server %q isn't a URL", issuer)
	}
	asOrigin, ip := iu.Scheme+"://"+iu.Host, strings.TrimRight(iu.Path, "/")
	var asCands []string
	if ip != "" {
		asCands = []string{asOrigin + "/.well-known/oauth-authorization-server" + ip, asOrigin + "/.well-known/openid-configuration" + ip, issuer + "/.well-known/openid-configuration"}
	} else {
		asCands = []string{asOrigin + "/.well-known/oauth-authorization-server", asOrigin + "/.well-known/openid-configuration"}
	}
	var as struct {
		Issuer        string   `json:"issuer"`
		Authorize     string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		Register      string   `json:"registration_endpoint"`
		Scopes        []string `json:"scopes_supported"`
		Challenge     []string `json:"code_challenge_methods_supported"`
		IssParam      bool     `json:"authorization_response_iss_parameter_supported"`
		ResponseTypes []string `json:"response_types_supported"`
	}
	found := false
	for _, c := range asCands {
		if getJSON(ctx, c, &as) == nil && as.Authorize != "" && as.Token != "" {
			found = true
			break
		}
	}
	if !found { // the 2025-03-26 defaults, at the authorization server's origin
		as.Authorize, as.Token, as.Register = asOrigin+"/authorize", asOrigin+"/token", asOrigin+"/register"
	}
	if len(as.Challenge) > 0 && !contains(as.Challenge, "S256") {
		return m, fmt.Errorf("its authorization server doesn't offer PKCE S256 (it offers %s)", strings.Join(as.Challenge, ", "))
	}
	m.Issuer, m.AuthorizeURL, m.TokenURL, m.RegisterURL, m.IssParam = orStr(as.Issuer, issuer), as.Authorize, as.Token, as.Register, as.IssParam
	if len(m.Scopes) == 0 {
		m.Scopes = as.Scopes
	}
	return m, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// getJSON GETs u into v (a non-2xx is an error).
func getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := directHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// registerClient registers a public client for redirect at m's
// registration endpoint: its client id, and a secret if it handed one out.
func registerClient(ctx context.Context, m oauthMeta, redirect string) (id, secret string, err error) {
	if m.RegisterURL == "" {
		return "", "", errors.New("its authorization server offers no dynamic client registration")
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                "xbin agent",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"scope":                      strings.Join(m.Scopes, " "),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.RegisterURL, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := directHTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("registering a client: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if resp.StatusCode/100 != 2 || json.Unmarshal(raw, &out) != nil || out.ClientID == "" {
		return "", "", fmt.Errorf("registering a client: HTTP %d: %s", resp.StatusCode, clip(oauthErrText(raw), 300))
	}
	return out.ClientID, out.ClientSecret, nil
}

// pkce is a fresh verifier and its S256 challenge.
func pkce() (verifier, challenge string) {
	verifier = randToken(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// authorizeURL is where a person signs in.
func authorizeURL(m oauthMeta, clientID, redirect, state, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {m.Resource},
	}
	if len(m.Scopes) > 0 {
		q.Set("scope", strings.Join(m.Scopes, " "))
	}
	sep := "?"
	if strings.Contains(m.AuthorizeURL, "?") {
		sep = "&"
	}
	return m.AuthorizeURL + sep + q.Encode()
}

// tokenGrant posts form to m's token endpoint; the tokens it answered.
func tokenGrant(ctx context.Context, m oauthMeta, clientID, secret string, form url.Values) (oauthTokens, error) {
	form.Set("client_id", clientID)
	if secret != "" {
		form.Set("client_secret", secret)
	}
	if m.Resource != "" {
		form.Set("resource", m.Resource)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := directHTTP.Do(req)
	if err != nil {
		return oauthTokens{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.AccessToken == "" {
		if out.Error == "invalid_grant" || out.Error == "invalid_client" || out.Error == "unauthorized_client" {
			return oauthTokens{}, fmt.Errorf("%w: %s", errNeedsSignin, clip(oauthErrText(raw), 200))
		}
		return oauthTokens{}, fmt.Errorf("its token endpoint: HTTP %d: %s", resp.StatusCode, clip(oauthErrText(raw), 300))
	}
	t := oauthTokens{Access: out.AccessToken, Refresh: out.RefreshToken, ClientSecret: secret}
	if out.ExpiresIn > 0 {
		t.ExpiresMs = nowMs() + out.ExpiresIn*1000
	}
	return t, nil
}

// oauthErrText is an OAuth error body as words (error: description), else the body.
func oauthErrText(raw []byte) string {
	var e struct {
		Error string `json:"error"`
		Desc  string `json:"error_description"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		if e.Desc != "" {
			return e.Error + ": " + e.Desc
		}
		return e.Error
	}
	return strings.TrimSpace(string(raw))
}
