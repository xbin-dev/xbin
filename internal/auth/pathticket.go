package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// PATH TICKETS (D135) authenticate one prefix of one tile's API, carried
// in the URL path: /api/~<ticket>/<rest> reaches /api/<tile>/<prefix>/<rest>
// as the frame principal that minted it — and nothing else. A tile's page
// mints one (its frame token: component and user) for a document it frames
// from its own backend, whose relative loads need a credential that rides
// in the path: the agent template's live preview of a sandbox's server,
// served in an opaque-origin sandboxed frame that holds no token and gets
// no cookie. The framed content may read its own URL, and so the ticket:
// what it gets is the prefix it already is, never the tile's other routes,
// another tile or /api/xbin.
//
// A ticket is "p1." + base64url(JSON claims) + "." + base64url(HMAC-SHA256
// under the workspace secret of the purpose, 0, the claims' base64). The
// purpose tag keeps it from ever verifying as a frame token, an asset
// token or a tile-origin credential, or they as it. It binds the tile, the
// user, the login generation (it dies with the login, as a frame token
// does), the deployment, a view-as impersonator (it stays read-only), the
// prefix and an expiry.
const (
	pathTicketPurpose = "xbin-path-ticket-v1"
	pathTicketPrefix  = "p1."
	// PathTicketTTL bounds a ticket: a live page keeps loading for as long
	// as it is open, and a reload mints a fresh one.
	PathTicketTTL = 12 * time.Hour
	// PathTicketMaxPrefix bounds a ticket's prefix.
	PathTicketMaxPrefix = 256
)

// pathTicketSeg is a prefix's segment: RFC 3986 unreserved characters only,
// so a prefix is its own escaping and never holds an encoded "/", ".." or
// "?".
var pathTicketPrefixRE = regexp.MustCompile(`^[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*$`)

// ValidPathTicketPrefix: a relative path of unreserved-character segments,
// none "." or "..", at most PathTicketMaxPrefix bytes.
func ValidPathTicketPrefix(p string) bool {
	if len(p) > PathTicketMaxPrefix || !pathTicketPrefixRE.MatchString(p) {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." {
			return false
		}
	}
	return true
}

type pathClaims struct {
	Tile   string `json:"t"`
	User   string `json:"u,omitempty"`
	Gen    string `json:"g"`
	Dep    string `json:"d,omitempty"`
	Imp    string `json:"i,omitempty"`
	Prefix string `json:"p"`
	Exp    int64  `json:"e"`
}

// ErrPathTicket is MintPathTicket's refusal of a principal or a prefix.
var ErrPathTicket = errors.New("a path ticket is minted by a tile's page (its frame token), for a prefix of unreserved-character segments")

// MintPathTicket mints a ticket for p — a tile's page: a frame principal —
// to reach prefix of its own tile's API, and when it expires.
func (a *Auth) MintPathTicket(p Principal, prefix string) (string, time.Time, error) {
	if p.Via != "frame" || p.Component == "" || !ValidPathTicketPrefix(prefix) {
		return "", time.Time{}, ErrPathTicket
	}
	gen := p.Gen
	if !validGen(gen) {
		gen = a.defaultGen(p.UserID)
	}
	exp := time.Now().Add(PathTicketTTL)
	c := pathClaims{Tile: p.Component, User: p.UserID, Gen: gen, Dep: p.Deployment, Imp: p.Impersonator, Prefix: prefix, Exp: exp.Unix()}
	b, err := json.Marshal(c)
	if err != nil {
		return "", time.Time{}, err
	}
	body := base64.RawURLEncoding.EncodeToString(b)
	return pathTicketPrefix + body + "." + a.pathTicketMAC(body), exp, nil
}

func (a *Auth) pathTicketMAC(body string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(pathTicketPurpose))
	m.Write([]byte{0})
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyPathTicket returns the frame principal a valid, unexpired ticket
// acts as — only while its login lives and its user exists — and the
// prefix of its tile's API it reaches.
func (a *Auth) VerifyPathTicket(tok string) (Principal, string, bool) {
	rest, ok := strings.CutPrefix(tok, pathTicketPrefix)
	if !ok || len(tok) > 2048 {
		return Principal{}, "", false
	}
	body, sig, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(a.pathTicketMAC(body))) {
		return Principal{}, "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Principal{}, "", false
	}
	var c pathClaims
	if err := json.Unmarshal(b, &c); err != nil || c.Tile == "" || !validGen(c.Gen) || !ValidPathTicketPrefix(c.Prefix) {
		return Principal{}, "", false
	}
	if time.Now().Unix() > c.Exp {
		return Principal{}, "", false
	}
	imp, live := a.genLive(c.Gen, c.User)
	if !live || imp != c.Imp {
		return Principal{}, "", false
	}
	p := Principal{Component: c.Tile, UserID: c.User, Via: "frame", Gen: c.Gen, Impersonator: c.Imp, Deployment: c.Dep}
	if c.User != "" {
		if _, found := a.userSnapshot(c.User); !found {
			return Principal{}, "", false
		}
		p.Access = a.accessSnapshot(c.User)
	}
	return p, c.Prefix, true
}
