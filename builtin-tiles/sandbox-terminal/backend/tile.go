// tile.go — the tile's state and its HTTP routes (API.md), which its page
// calls with the frame token: the verified person is X-XBin-User.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"golang.org/x/crypto/ssh"
)

// store is the tile's kv (`state`).
type store interface {
	Get(key string) ([]byte, error)
	Put(key string, val []byte) error
}

var errNotFound = xbin.ErrNotFound

// Tile is the sandbox-terminal backend.
type Tile struct {
	self       string                       // this tile's path: the consumer managers see
	kv         store                        // keys, settings
	secret     func(string) (string, error) // the vault: the host key
	setSecret  func(string, string) error
	managers   func() []manager // the bound managers
	hc         *http.Client     // reaches them (xbin.Client(): the gateway)
	sshPort    int              // the port the `ssh` expose declares
	hupGrace   time.Duration    // HUP → DELETE for a command whose client left
	loginGrace time.Duration    // a connection's time to authenticate

	mu         sync.Mutex
	keys       []keyRec
	keysLoaded bool
	settings   settings
	signer     ssh.Signer
	listening  bool
	sshErr     string
	conns      map[*liveConn]struct{}
	hellos     helloCache
	limit      *limiter
	preauth    chan struct{}
}

// settings are the managers' knobs.
type settings struct {
	// SSHAddress is where people reach the SSH port from outside ("host" or
	// "host:port") — the page shows it in the ssh command; xbind's port
	// binding isn't visible to the tile.
	SSHAddress string `json:"sshAddress"`
}

func newTile(self string, kv store, secret func(string) (string, error), setSecret func(string, string) error,
	managers func() []manager, hc *http.Client) *Tile {
	return &Tile{self: self, kv: kv, secret: secret, setSecret: setSecret, managers: managers, hc: hc,
		sshPort: 2222, hupGrace: 2 * time.Second, loginGrace: 30 * time.Second,
		conns: map[*liveConn]struct{}{}, limit: newLimiter(), preauth: make(chan struct{}, maxPreauth)}
}

// load reads the keys and settings, retrying while the kv doesn't answer
// (until then key changes are refused and nobody logs in).
func (t *Tile) load() {
	for wait := time.Second; ; wait = min(2*wait, time.Minute) {
		err := t.loadKeys()
		if err == nil {
			var st settings
			if b, err := t.kv.Get("settings"); err == nil {
				_ = json.Unmarshal(b, &st)
			}
			t.mu.Lock()
			t.settings = st
			t.mu.Unlock()
			return
		}
		time.Sleep(wait)
	}
}

// --- who is asking ------------------------------------------------------------------

type who struct {
	user     string // a person (through the tile's page or a terminal)
	level    string // their access to this tile: read | write | terminal
	viewedBy string // an admin viewing the workspace as user (D64): reads only
	system   bool   // the owner token, or the tile itself
}

func (t *Tile) principal(r *http.Request) who {
	c := xbin.Caller(r)
	switch {
	case c.Owner:
		return who{system: true}
	case c.User != "" && (c.From == t.self || strings.HasPrefix(c.From, "user:")):
		lvl := c.UserLevel
		if lvl == "" {
			lvl = "read"
		}
		return who{user: c.User, level: lvl, viewedBy: c.ViewedBy}
	case c.From != "" && c.From == t.self:
		return who{system: true}
	}
	return who{}
}

// manager: may list and revoke everyone's keys, see every session, change
// the settings — write or terminal on the tile (which can change its code
// anyway), or the owner.
func (w who) manager() bool { return w.system || w.level == "write" || w.level == "terminal" }

func (w who) anyone() bool { return w.system || w.user != "" }

// --- routes ---------------------------------------------------------------------------

func (t *Tile) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /me", t.handleMe)
	mux.HandleFunc("GET /keys", t.handleKeys)
	mux.HandleFunc("POST /keys", t.handleAddKey)
	mux.HandleFunc("DELETE /keys/{id}", t.handleDelKey)
	mux.HandleFunc("GET /sandboxes", t.handleSandboxes)
	mux.HandleFunc("GET /sessions", t.handleSessions)
	mux.HandleFunc("PUT /settings", t.handleSettings)
	return mux
}

// sshView is the SSH ingress as the page shows it.
type sshView struct {
	Port      int      `json:"port"`              // in the tile's sandbox (the `ssh` expose)
	Address   string   `json:"address,omitempty"` // settings.sshAddress
	Listening bool     `json:"listening"`
	Error     string   `json:"error,omitempty"`
	HostKey   *hostKey `json:"hostKey,omitempty"`
}

type hostKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"` // SHA256:…
	PublicKey   string `json:"publicKey"`   // for known_hosts
}

func (t *Tile) sshView() sshView {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := sshView{Port: t.sshPort, Address: t.settings.SSHAddress, Listening: t.listening, Error: t.sshErr}
	if t.signer != nil {
		pk := t.signer.PublicKey()
		v.HostKey = &hostKey{Type: pk.Type(), Fingerprint: ssh.FingerprintSHA256(pk),
			PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))}
	}
	return v
}

func (t *Tile) handleMe(w http.ResponseWriter, r *http.Request) {
	p := t.principal(r)
	if !p.anyone() {
		xbin.WriteError(w, http.StatusForbidden, "this tile answers its own page and the owner")
		return
	}
	ms := []string{}
	for _, m := range t.managers() {
		ms = append(ms, m.Provider)
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"user": p.user, "level": p.level, "manager": p.manager(),
		"viewedBy": p.viewedBy, "self": t.self, "ssh": t.sshView(), "managers": ms, "keys": len(t.keysOf(p.user))})
}

func (t *Tile) handleKeys(w http.ResponseWriter, r *http.Request) {
	p := t.principal(r)
	if !p.anyone() {
		xbin.WriteError(w, http.StatusForbidden, "this tile answers its own page and the owner")
		return
	}
	if r.URL.Query().Get("all") == "1" {
		if !p.manager() {
			xbin.WriteError(w, http.StatusForbidden, "everyone's keys are for the tile's managers (write access to it)")
			return
		}
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"keys": t.keysOf("")})
		return
	}
	if p.user == "" {
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"keys": []keyRec{}})
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"keys": t.keysOf(p.user)})
}

func (t *Tile) handleAddKey(w http.ResponseWriter, r *http.Request) {
	p := t.principal(r)
	switch {
	case p.user == "":
		xbin.WriteError(w, http.StatusForbidden, "keys are people's: register one from the tile's page, signed in")
		return
	case p.viewedBy != "":
		xbin.WriteError(w, http.StatusForbidden, "viewing as someone registers nothing for them")
		return
	}
	var in struct {
		PublicKey string `json:"publicKey"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		xbin.WriteError(w, http.StatusBadRequest, "a JSON body {publicKey, name?}")
		return
	}
	k, status, err := t.addKey(p.user, in.PublicKey, in.Name)
	if err != nil {
		xbin.WriteError(w, status, err.Error())
		return
	}
	xbin.WriteJSON(w, status, k)
}

func (t *Tile) handleDelKey(w http.ResponseWriter, r *http.Request) {
	p := t.principal(r)
	if !p.anyone() || p.viewedBy != "" {
		xbin.WriteError(w, http.StatusForbidden, "only the key's person or the tile's managers remove a key")
		return
	}
	k, status, err := t.removeKey(r.PathValue("id"), p.user, p.manager())
	if err != nil {
		xbin.WriteError(w, status, err.Error())
		return
	}
	t.closeKey(k.ID)
	w.WriteHeader(http.StatusNoContent)
}

// sandboxView is one sandbox as the page lists it.
type sandboxView struct {
	Provider   string   `json:"provider"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Login      string   `json:"login"` // the SSH user name that picks it
	State      string   `json:"state"`
	Owner      string   `json:"owner,omitempty"`
	Via        string   `json:"via,omitempty"` // its home consumer
	Visibility string   `json:"visibility"`
	Members    []string `json:"members"`
	Shared     bool     `json:"shared"`
	Egress     string   `json:"egress,omitempty"`
	Isolation  string   `json:"isolation,omitempty"`
	Image      string   `json:"image,omitempty"`
	Workdir    string   `json:"workdir,omitempty"`
	TTY        bool     `json:"tty"` // a browser terminal (and ssh -t) can open
	Created    int64    `json:"created"`
}

func (t *Tile) handleSandboxes(w http.ResponseWriter, r *http.Request) {
	p := t.principal(r)
	if p.user == "" {
		xbin.WriteError(w, http.StatusForbidden, "sandboxes are listed for a person: call from the tile's page, signed in")
		return
	}
	es, views := t.usable(r.Context(), p.user)
	out := make([]sandboxView, 0, len(es))
	for _, e := range es {
		img := e.SB.Image.Title
		if img == "" {
			img = e.SB.Image.ID
		}
		members := e.SB.Members
		if members == nil {
			members = []string{}
		}
		out = append(out, sandboxView{Provider: e.M.Provider, ID: e.SB.ID, Name: e.SB.Name, Login: e.Login, State: e.SB.State,
			Owner: e.SB.Owner.User, Via: e.SB.Owner.Via, Visibility: e.SB.Visibility, Members: members,
			Shared: e.SB.Shared || e.SB.Owner.Via != t.self, Egress: e.SB.Egress, Isolation: e.SB.Isolation, Image: img,
			Workdir: e.SB.Workdir, TTY: e.tty(), Created: e.SB.Created})
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"managers": views, "sandboxes": out, "ssh": t.sshView()})
}

func (t *Tile) handleSessions(w http.ResponseWriter, r *http.Request) {
	p := t.principal(r)
	switch {
	case p.manager() && r.URL.Query().Get("all") == "1":
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"sessions": t.sessions("")})
	case p.user != "":
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"sessions": t.sessions(p.user)})
	case p.system:
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"sessions": []sessionView{}})
	default:
		xbin.WriteError(w, http.StatusForbidden, "this tile answers its own page and the owner")
	}
}

func (t *Tile) handleSettings(w http.ResponseWriter, r *http.Request) {
	p := t.principal(r)
	if !p.manager() || p.viewedBy != "" {
		xbin.WriteError(w, http.StatusForbidden, "the settings are the tile's managers' (write access to it)")
		return
	}
	var in settings
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		xbin.WriteError(w, http.StatusBadRequest, "a JSON body {sshAddress}")
		return
	}
	in.SSHAddress = strings.TrimSpace(in.SSHAddress)
	if len(in.SSHAddress) > 253 || strings.ContainsAny(in.SSHAddress, " \t\r\n/@") {
		xbin.WriteError(w, http.StatusBadRequest, "sshAddress is a host name or address, with :port when it isn't 22")
		return
	}
	b, _ := json.Marshal(in)
	if err := t.kv.Put("settings", b); err != nil {
		xbin.WriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	t.mu.Lock()
	t.settings = in
	t.mu.Unlock()
	xbin.WriteJSON(w, http.StatusOK, in)
}
