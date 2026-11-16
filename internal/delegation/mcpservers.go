package delegation

// mcpservers.go — the outside MCP servers a workspace admin lists for
// people's workspace agents (mcp.mode "listed", D240; plans/wsagent/
// web-mcp.md §6.1): each by URL, with how xbind signs in to it. The list
// is kept with the admin's other settings (workspace.json) and the secrets
// apart, sealed under the vault (Store.Secrets: internal/vault's SealedMap,
// label "delegation/mcp-secrets"); the host's file may name a list of its
// own, which then is the list in force (a lock, unlike a mode's ceiling),
// with each secret read from a file or an environment variable by the file's
// loader (internal/llm/hostagent.go).
//
// A secret is never in an answer, a log line or an error: reads say
// hasSecret, a Secret prints as [secret], and the server package scrubs
// whatever a server answers of it.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/vault"
	"github.com/xbin-dev/xbin/internal/webfetch"
)

// MCPServer is one outside MCP server as a policy layer lists it. The
// admin's records carry HasSecret (the secret itself is in Store.Secrets);
// the host's carry SecretFile or SecretEnv, and the secret its file named,
// read when the file loaded (HostSecret).
type MCPServer struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Auth           string   `json:"auth,omitempty"` // none | bearer | header
	Header         string   `json:"header,omitempty"`
	Note           string   `json:"note,omitempty"`
	Orgs           []string `json:"orgs,omitempty"`
	PlainHTTP      bool     `json:"plainHttp,omitempty"`
	PrivateNetwork bool     `json:"privateNetwork,omitempty"`
	TrustReadOnly  bool     `json:"trustReadOnly,omitempty"`
	HasSecret      bool     `json:"hasSecret,omitempty"`
	// the host's file: where the secret is read from
	SecretFile string `json:"secretFile,omitempty"`
	SecretEnv  string `json:"secretEnv,omitempty"`
	// HostSecret is a host server's secret, as its file named it.
	HostSecret Secret `json:"-"`
	host       bool
}

// The ways xbind signs in to an outside server.
const (
	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthHeader = "header"
)

// Bounds of the list.
const (
	MaxMCPServers  = 50
	maxServerName  = 80
	maxServerNote  = 500
	MaxSecret      = 4 << 10
	ExtPrefix      = "ext:" // what the agent names an outside server by: ext:<id>
	mcpSecretLabel = "delegation/mcp-secrets"
)

// Secret is a credential xbind holds. It prints, logs and marshals as
// [secret]; Reveal is the one way to the value.
type Secret struct{ v string }

// NewSecret wraps s.
func NewSecret(s string) Secret { return Secret{v: s} }

// Reveal is the secret's value.
func (s Secret) Reveal() string { return s.v }

// Set reports whether there is one.
func (s Secret) Set() bool { return s.v != "" }

func (Secret) String() string                  { return "[secret]" }
func (Secret) GoString() string                { return "[secret]" }
func (Secret) MarshalJSON() ([]byte, error)    { return []byte(`"[secret]"`), nil }
func (Secret) LogValue() slog.Value            { return slog.StringValue("[secret]") }
func (s *Secret) UnmarshalJSON([]byte) error   { *s = Secret{}; return nil }
func (Secret) MarshalText() ([]byte, error)    { return []byte("[secret]"), nil }
func (s *Secret) UnmarshalText(_ []byte) error { *s = Secret{}; return nil }

var serverID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// headerName is an HTTP token.
var headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")

// reservedHeaders are the headers xbind sets itself, or that steer the
// connection: a server's credential is never one of them.
var reservedHeaders = []string{"host", "content-type", "content-length", "content-encoding", "content-location", "accept", "accept-encoding", "accept-charset",
	"accept-language", "mcp-session-id", "mcp-protocol-version", "connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade", "cookie",
	"user-agent", "referer", "origin", "forwarded", "expect", "last-event-id", "via", "range", "if-match", "if-none-match"}

func reservedHeader(h string) bool {
	h = strings.ToLower(h)
	return slices.Contains(reservedHeaders, h) || strings.HasPrefix(h, "proxy-") || strings.HasPrefix(h, "x-forwarded-") || strings.HasPrefix(h, "x-xbin-") || strings.HasPrefix(h, "sec-")
}

// Server is the name the agent calls the server by.
func (m MCPServer) Server() string { return ExtPrefix + m.ID }

// Key is what a person's approvals and pins of the server are kept under:
// its id and its address — a server whose URL changes, or another given
// the same id, is one nobody allowed yet.
func (m MCPServer) Key() string {
	h := sha256.Sum256([]byte(m.URL))
	return ExtPrefix + m.ID + "@" + hex.EncodeToString(h[:4])
}

// IsExt reports a server name or key of an outside server.
func IsExt(server string) bool { return strings.HasPrefix(server, ExtPrefix) }

// origin is a URL's scheme, host and port.
func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Scheme + "://" + u.Host
}

// Host is the server's host as its URL names it (no port).
func (m MCPServer) Host() string {
	u, err := url.Parse(m.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ByHost reports a server of the host's file.
func (m MCPServer) ByHost() bool { return m.host }

// Visible reports whether a person in orgs (their active memberships) sees
// the server: one listed for no org is everyone's.
func (m MCPServer) Visible(orgs []string) bool {
	if len(m.Orgs) == 0 {
		return true
	}
	for _, o := range orgs {
		if slices.Contains(m.Orgs, o) {
			return true
		}
	}
	return false
}

// Inside reports a server listed with org: for that org's mcp rule it is
// the org's own.
func (m MCPServer) Inside(org string) bool { return slices.Contains(m.Orgs, org) }

func plainText(s string, newlines bool) bool {
	for _, r := range s {
		if r == '\n' && newlines {
			continue
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 || r == ' ' || r == ' ' {
			return false
		}
	}
	return utf8.ValidString(s)
}

// check is m validated, in its canonical form; host says the host's file
// lists it (its secret by reference) rather than an admin.
func (m MCPServer) check(host bool) (MCPServer, error) {
	bad := func(field, format string, args ...any) error {
		return fmt.Errorf("mcp.servers %s: %s %s", orStr(m.ID, "(no id)"), field, fmt.Sprintf(format, args...))
	}
	if !serverID.MatchString(m.ID) {
		return m, fmt.Errorf("mcp.servers: id %q: lower-case letters, digits and -, at most 32, starting with a letter or digit", m.ID)
	}
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" || len(m.Name) > maxServerName || !plainText(m.Name, false) {
		return m, bad("name", "is what people read: 1 to %d bytes, no control characters", maxServerName)
	}
	m.Note = strings.TrimSpace(m.Note)
	if len(m.Note) > maxServerNote || !plainText(m.Note, true) {
		return m, bad("note", "is at most %d bytes, no control characters", maxServerNote)
	}
	u, err := webfetch.CheckServerURL(m.URL, m.PrivateNetwork)
	if err != nil {
		return m, bad("url", "%v", strings.TrimPrefix(err.Error(), "url: "))
	}
	if u.Scheme == "http" && !m.PlainHTTP {
		return m, bad("url", "is http: the secret and every argument would cross the network in the clear — use https, or say plainHttp: true for a server you know is reached so")
	}
	m.URL = u.String()
	switch m.Auth {
	case "", AuthNone:
		m.Auth, m.Header = AuthNone, ""
	case AuthBearer:
		m.Header = ""
	case AuthHeader:
		if !headerName.MatchString(m.Header) {
			return m, bad("header", "names the header the secret goes in (a token, at most 64 characters)")
		}
		if reservedHeader(m.Header) {
			return m, bad("header", "%q is a header xbin sets itself or that steers the connection: name another", m.Header)
		}
	default:
		return m, bad("auth", `is "none", "bearer" or "header", not %q`, m.Auth)
	}
	orgs := []string{}
	for _, o := range m.Orgs {
		o = strings.TrimSpace(o)
		if o == "" || len(o) > 200 || o == "workspace" {
			return m, bad("orgs", "names organisations by id (people of one of them see the server)")
		}
		if !slices.Contains(orgs, o) {
			orgs = append(orgs, o)
		}
	}
	slices.Sort(orgs)
	m.Orgs = orgs
	if len(m.Orgs) == 0 {
		m.Orgs = nil
	}
	if host {
		if m.SecretFile != "" && m.SecretEnv != "" {
			return m, bad("secret", "is read from secretFile or secretEnv, not both")
		}
		if m.Auth != AuthNone && m.SecretFile == "" && m.SecretEnv == "" {
			return m, bad("secret", "auth %s needs secretFile or secretEnv", m.Auth)
		}
		m.HasSecret = m.Auth != AuthNone
	} else if m.SecretFile != "" || m.SecretEnv != "" {
		return m, bad("secret", "secretFile and secretEnv are the host's file's: an admin sets the secret itself")
	}
	m.host = host
	return m, nil
}

// CheckHostServers validates the list a host's file names, as the file's
// loader reads it (secrets by reference; HostSecret set by the loader).
func CheckHostServers(in []MCPServer) ([]MCPServer, error) {
	if len(in) > MaxMCPServers {
		return nil, fmt.Errorf("mcp.servers: at most %d", MaxMCPServers)
	}
	out := make([]MCPServer, 0, len(in))
	for _, m := range in {
		c, err := m.check(true)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(out, func(o MCPServer) bool { return o.ID == c.ID }) {
			return nil, fmt.Errorf("mcp.servers: the id %s twice", c.ID)
		}
		c.HostSecret = m.HostSecret
		out = append(out, c)
	}
	return out, nil
}

// MCPServerView is a server as a read answers it: never its secret.
type MCPServerView struct {
	ID             string   `json:"id"`
	Server         string   `json:"server"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Host           string   `json:"host"`
	Auth           string   `json:"auth"`
	Header         string   `json:"header,omitempty"`
	HasSecret      bool     `json:"hasSecret"`
	Note           string   `json:"note,omitempty"`
	Orgs           []string `json:"orgs"`
	PlainHTTP      bool     `json:"plainHttp"`
	PrivateNetwork bool     `json:"privateNetwork"`
	TrustReadOnly  bool     `json:"trustReadOnly"`
	ByHost         bool     `json:"byHost,omitempty"`
}

// View is m as a read answers it.
func (m MCPServer) View() MCPServerView {
	return MCPServerView{ID: m.ID, Server: m.Server(), Name: m.Name, URL: m.URL, Host: m.Host(), Auth: orStr(m.Auth, AuthNone), Header: m.Header,
		HasSecret: m.HasSecret, Note: m.Note, Orgs: orEmpty(m.Orgs), PlainHTTP: m.PlainHTTP, PrivateNetwork: m.PrivateNetwork, TrustReadOnly: m.TrustReadOnly, ByHost: m.host}
}

// Views is the list as a read answers it.
func Views(list []MCPServer) []MCPServerView {
	out := make([]MCPServerView, 0, len(list))
	for _, m := range list {
		out = append(out, m.View())
	}
	return out
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ---- in force ----

// MCPServers is the list of outside servers in force: the host's when its
// file names one, else the admin's — whatever the mode (the mode says
// whether they are used: MCPInForce).
func (s *Store) MCPServers() []MCPServer {
	if h := s.Host(); h.MCP != nil && h.MCP.Servers != nil {
		out := slices.Clone(*h.MCP.Servers)
		for i := range out {
			out[i].host = true
		}
		return out
	}
	w := s.Workspace()
	if w.MCP == nil || w.MCP.Servers == nil {
		return nil
	}
	return slices.Clone(*w.MCP.Servers)
}

// MCPServerNamed is the server in force the agent names server
// ("ext:<id>"), or false.
func (s *Store) MCPServerNamed(server string) (MCPServer, bool) {
	id, ok := strings.CutPrefix(server, ExtPrefix)
	if !ok {
		return MCPServer{}, false
	}
	for _, m := range s.MCPServers() {
		if m.ID == id {
			return m, true
		}
	}
	return MCPServer{}, false
}

// MCPKeyInForce reports whether key (a server's Key) is a server in force.
func (s *Store) MCPKeyInForce(key string) (MCPServer, bool) {
	for _, m := range s.MCPServers() {
		if m.Key() == key {
			return m, true
		}
	}
	return MCPServer{}, false
}

// ErrSecretUnreadable: a server's secret can't be read now; Sealed says the
// vault is why.
type ErrSecretUnreadable struct {
	Sealed bool
	Msg    string
}

func (e *ErrSecretUnreadable) Error() string { return e.Msg }

// MCPSecret is the secret xbind signs in to m with ("" for auth none).
func (s *Store) MCPSecret(m MCPServer) (Secret, error) {
	if m.Auth == AuthNone || m.Auth == "" {
		return Secret{}, nil
	}
	if m.host {
		if !m.HostSecret.Set() {
			return Secret{}, &ErrSecretUnreadable{Msg: m.Name + "'s secret, named by the host's file, can't be read"}
		}
		return m.HostSecret, nil
	}
	if s == nil || s.Secrets == nil {
		return Secret{}, &ErrSecretUnreadable{Msg: "this xbind keeps no vault for outside servers' secrets"}
	}
	v, err := s.Secrets.Get(m.ID)
	switch {
	case errors.Is(err, vault.ErrMapSealed):
		return Secret{}, &ErrSecretUnreadable{Sealed: true, Msg: "the workspace vault is sealed: " + m.Name + "'s secret can't be read until an admin unseals it"}
	case err != nil:
		return Secret{}, &ErrSecretUnreadable{Msg: m.Name + "'s secret can't be read: " + err.Error()}
	case v == "":
		return Secret{}, &ErrSecretUnreadable{Msg: m.Name + " has no secret stored: an admin sets it on the workspace agent's card"}
	}
	// a secret is the address's it was given for: on every path that could
	// pair it with another (a list saved apart from its secrets), it isn't sent
	given, secret, ok := openSecret(v)
	if !ok || given != urlHash(m.URL) {
		return Secret{}, &ErrSecretUnreadable{Msg: m.Name + "'s secret was given for another address: an admin gives it again on the workspace agent's card"}
	}
	return NewSecret(secret), nil
}

// A stored secret is "u1:<hash of the URL it was given for>:<secret>".
const secretForm = "u1:"

func urlHash(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:12])
}

func sealSecret(u, secret string) string { return secretForm + urlHash(u) + ":" + secret }

func openSecret(v string) (forURL, secret string, ok bool) {
	rest, ok := strings.CutPrefix(v, secretForm)
	if !ok {
		return "", "", false
	}
	forURL, secret, ok = strings.Cut(rest, ":")
	return forURL, secret, ok && secret != ""
}

// SetSecrets installs the sealed map the admin's servers' secrets live in
// (boot: data/delegation/mcp-secrets.json under the barrier).
func (s *Store) SetSecrets(path string, v vault.Sealer, allowPlain bool) {
	if s != nil {
		s.Secrets = &vault.SealedMap{Path: path, Label: mcpSecretLabel, Name: "data/delegation/mcp-secrets.json", Vault: v, AllowPlain: allowPlain}
	}
}

// ---- the admin's write ----

// MCPServerPatch is one server of an admin's write: the server, and its
// secret — nil keeps the stored one, "" removes it.
type MCPServerPatch struct {
	MCPServer
	Secret *string
}

// ErrSecretVault: a secret can't be written now (the vault is sealed or not
// set up): 409 at the route.
type ErrSecretVault struct{ Msg string }

func (e *ErrSecretVault) Error() string { return e.Msg }

// checkSecret: one line, at most MaxSecret bytes, no control characters.
func checkSecret(id, v string) error {
	if len(v) > MaxSecret || strings.TrimSpace(v) != v || !plainText(v, false) {
		return fmt.Errorf("mcp.servers %s: secret is one line, at most %d bytes, no spaces around it, no control characters", id, MaxSecret>>10<<10)
	}
	return nil
}

// planServers is the admin's new list from the old one and the patch, and
// the secrets to write (id → value, "" removes). Every rule is judged
// before anything is written.
func planServers(old []MCPServer, in []MCPServerPatch, hasVault bool) ([]MCPServer, map[string]string, error) {
	if len(in) > MaxMCPServers {
		return nil, nil, fmt.Errorf("mcp.servers: at most %d", MaxMCPServers)
	}
	was := map[string]MCPServer{}
	for _, o := range old {
		was[o.ID] = o
	}
	out := make([]MCPServer, 0, len(in))
	secrets := map[string]string{}
	for _, p := range in {
		m, err := p.MCPServer.check(false)
		if err != nil {
			return nil, nil, err
		}
		if slices.ContainsFunc(out, func(o MCPServer) bool { return o.ID == m.ID }) {
			return nil, nil, fmt.Errorf("mcp.servers: the id %s twice", m.ID)
		}
		prev, existed := was[m.ID]
		has := existed && prev.HasSecret
		switch {
		case m.Auth == AuthNone:
			if p.Secret != nil && *p.Secret != "" {
				return nil, nil, fmt.Errorf("mcp.servers %s: auth none takes no secret", m.ID)
			}
			if has {
				secrets[m.ID] = ""
			}
			has = false
		case p.Secret == nil:
			// kept — but never sent elsewhere than where it was given for: any
			// change of the URL (a path routes on a shared host) needs it again
			if has && prev.URL != m.URL {
				return nil, nil, fmt.Errorf("mcp.servers %s: its address changed (%s) — give its secret again: xbin never sends a secret to another address than the one it was given for", m.ID, origin(m.URL))
			}
		case *p.Secret == "":
			if has {
				secrets[m.ID] = ""
			}
			has = false
		default:
			if err := checkSecret(m.ID, *p.Secret); err != nil {
				return nil, nil, err
			}
			secrets[m.ID], has = sealSecret(m.URL, *p.Secret), true
		}
		if m.Auth != AuthNone && !has {
			return nil, nil, fmt.Errorf("mcp.servers %s: auth %s needs its secret", m.ID, m.Auth)
		}
		m.HasSecret = has
		out = append(out, m)
	}
	for id, o := range was { // a server that left the list takes its secret with it
		if o.HasSecret && !slices.ContainsFunc(out, func(m MCPServer) bool { return m.ID == id }) {
			secrets[id] = ""
		}
	}
	for _, v := range secrets {
		if v != "" && !hasVault {
			return nil, nil, &ErrSecretVault{Msg: "this xbind keeps no vault for outside servers' secrets"}
		}
	}
	return out, secrets, nil
}

// writeSecrets applies the planned secrets in one write of the sealed map.
func (s *Store) writeSecrets(secrets map[string]string) error {
	if len(secrets) == 0 {
		return nil
	}
	if s.Secrets == nil {
		return &ErrSecretVault{Msg: "this xbind keeps no vault for outside servers' secrets"}
	}
	err := s.Secrets.Update(func(m map[string]string) {
		for id, v := range secrets {
			if v == "" {
				delete(m, id)
			} else {
				m[id] = v
			}
		}
	})
	switch {
	case errors.Is(err, vault.ErrMapSealed):
		return &ErrSecretVault{Msg: "the workspace vault is sealed: an admin unseals it (bx vault unseal, or the admin console) before an outside server's secret can be set"}
	case errors.Is(err, vault.ErrMapLocked):
		return &ErrSecretVault{Msg: "the workspace vault isn't set up yet: an admin sets it up (bx vault unseal) before an outside server's secret can be stored — secrets are never kept in the clear"}
	}
	return err
}
