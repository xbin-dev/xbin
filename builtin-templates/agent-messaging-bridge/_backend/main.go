// agent-messaging-bridge — connects an agent built from the agent template to
// a chat platform (D86): it reports what people write to the agent this tile's
// `agent` interface is bound to (service agent-inbox, docs/agent-inbox.md) and
// posts the agent's replies. The agent owns the conversations, who may talk,
// what they may do and who they are; this tile owns the platform.
//
// It is a TEMPLATE: which platform is up to whoever customises the copy — a
// coding agent in this tile's terminal writes _backend/platform_<name>.go
// (AGENTS.md). Until then the built-in console platform lets people try the
// whole flow from the tile's page.
//
// "alwaysOn" keeps it running: a platform connection it opens never sends it
// the request that starts an ordinary tile (D84).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// config is the tile's settings (kv "config").
type config struct {
	Platform string `json:"platform,omitempty"` // which registered platform runs ("" = the first real one, else the console)
}

// state is what the page shows.
type state struct {
	Platform string        `json:"platform"`
	Phase    string        `json:"phase"` // needs-agent | needs-secrets | connecting | connected | error
	Detail   string        `json:"detail,omitempty"`
	Since    int64         `json:"since,omitempty"`
	Accounts []accountView `json:"accounts"`
}

type accountView struct {
	Account
	ChannelID int64  `json:"channelId"`
	Claimed   string `json:"claimed"` // the channel on the agent: unclaimed | active | disabled
}

type eventLog struct {
	At      int64  `json:"at"`
	Kind    string `json:"kind"` // in | out | error | note
	Where   string `json:"where,omitempty"`
	Who     string `json:"who,omitempty"`
	Text    string `json:"text,omitempty"`
	Verdict string `json:"verdict,omitempty"`
}

// Tile is the bridge.
type Tile struct {
	kv        Store
	secret    func(name string) string
	setSecret func(name, value string) error // "" deletes
	agent     *agentClient

	mu       sync.Mutex
	cfg      config
	st       state
	plat     Platform
	channels map[string]accountView // account id → its channel
	events   []eventLog
	wake     chan struct{}
	work     chan spoolItem
	ow, wo   sync.Once
	stop     context.CancelFunc // the running platform
}

func newTile(kv Store, secret func(string) string, agent *agentClient) *Tile {
	t := &Tile{kv: kv, secret: secret, agent: agent, wake: make(chan struct{}, 1), work: make(chan spoolItem, 1024),
		channels: map[string]accountView{}}
	if b, err := kv.Get("config"); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &t.cfg)
	}
	return t
}

func (t *Tile) kick() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *Tile) setPhase(phase, detail string) {
	t.mu.Lock()
	changed := t.st.Phase != phase || t.st.Detail != detail
	t.st.Phase, t.st.Detail = phase, detail
	if phase == "connected" && changed {
		t.st.Since = time.Now().Unix()
	}
	name := t.st.Platform
	t.mu.Unlock()
	if !changed {
		return
	}
	switch phase {
	case "connected":
		_ = xbin.ClearStatus()
	case "connecting":
	case "needs-agent":
		_ = xbin.Status("warn", "bind this bridge to an agent (its agent interface)")
	case "needs-secrets":
		_ = xbin.Status("warn", "add the "+name+" credentials on this tile's page")
	default:
		_ = xbin.Status("error", clip(name+": "+detail, 120))
	}
}

func (t *Tile) logEvent(e eventLog) {
	e.At = time.Now().Unix()
	e.Text = clip(e.Text, 160)
	t.mu.Lock()
	t.events = append(t.events, e)
	if len(t.events) > 50 {
		t.events = t.events[len(t.events)-50:]
	}
	t.mu.Unlock()
}

// --- the Bridge a platform uses ---------------------------------------------------

type bridge struct{ t *Tile }

func (b bridge) Secret(name string) string { return b.t.secret(name) }
func (b bridge) Store() Store              { return prefixStore{b.t.kv, "p/"} }
func (b bridge) Logf(format string, args ...any) {
	b.t.logEvent(eventLog{Kind: "note", Text: fmt.Sprintf(format, args...)})
}

// Account says hello to the agent for one account and remembers its channel.
func (b bridge) Account(ctx context.Context, a Account) error {
	t := b.t
	t.mu.Lock()
	name := t.st.Platform
	t.mu.Unlock()
	h, err := t.agent.hello(name, a)
	if err != nil {
		return err
	}
	v := accountView{Account: a, ChannelID: h.ChannelID, Claimed: h.State}
	t.mu.Lock()
	t.channels[a.ID] = v
	t.mu.Unlock()
	t.setPhase("connected", "")
	t.ensureWorker()
	return nil
}

func (b bridge) Receive(e Event) error { return b.t.spool(e) }

// prefixStore keeps a platform's keys apart from the bridge's.
type prefixStore struct {
	s      Store
	prefix string
}

func (p prefixStore) Get(k string) ([]byte, error) { return p.s.Get(p.prefix + k) }
func (p prefixStore) Put(k string, v []byte) error { return p.s.Put(p.prefix+k, v) }
func (p prefixStore) Delete(k string) error        { return p.s.Delete(p.prefix + k) }
func (p prefixStore) List(prefix string) ([]string, error) {
	keys, err := p.s.List(p.prefix + prefix)
	for i := range keys {
		keys[i] = strings.TrimPrefix(keys[i], p.prefix)
	}
	return keys, err
}

func (t *Tile) channelOf(account string) (accountView, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.channels[account]
	return v, ok
}

// channelState records a channel's state on the agent (for the page). A
// removed channel's account says hello again: the agent offers it anew.
func (t *Tile) channelState(chID int64, state string) {
	t.mu.Lock()
	var gone *Account
	for id, v := range t.channels {
		if v.ChannelID == chID && state != "" {
			v.Claimed = state
			t.channels[id] = v
			if state == "removed" {
				a := v.Account
				gone = &a
			}
		}
	}
	t.mu.Unlock()
	if gone != nil && t.platform() != nil {
		go func() { _ = bridge{t}.Account(context.Background(), *gone) }()
	}
}

func (t *Tile) accountOf(chID int64) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, v := range t.channels {
		if v.ChannelID == chID {
			return id, true
		}
	}
	return "", false
}

func (t *Tile) platform() Platform {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.plat
}

// --- the supervisor ---------------------------------------------------------------

var errNotReady = errors.New("not ready")

// run keeps the platform connected: it waits when something is missing,
// restarts it after a failure (1 s doubling to 2 min), and starts over when
// the settings change.
func (t *Tile) run() {
	backoff := time.Second
	for {
		started := time.Now()
		err := t.session()
		if errors.Is(err, errNotReady) {
			<-t.wake
			continue
		}
		if err == nil || errors.Is(err, context.Canceled) {
			continue // restarted on purpose
		}
		if time.Since(started) > 5*time.Minute {
			backoff = time.Second
		}
		t.setPhase("error", err.Error())
		select {
		case <-t.wake:
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 2*time.Minute {
			backoff = 2 * time.Minute
		}
	}
}

func (t *Tile) session() error {
	t.mu.Lock()
	name := choosePlatform(t.cfg.Platform)
	t.st.Platform = name
	t.mu.Unlock()
	if t.agent.base == "" {
		t.setPhase("needs-agent", "")
		return errNotReady
	}
	p := newPlatform(name, bridge{t})
	if p == nil {
		t.setPhase("error", "no platform is registered")
		return errNotReady
	}
	for _, s := range p.Info().Secrets {
		if t.secret(s.Name) == "" {
			t.mu.Lock()
			t.plat = p
			t.mu.Unlock()
			t.setPhase("needs-secrets", s.Label)
			return errNotReady
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.plat, t.stop = p, cancel
	t.mu.Unlock()
	defer cancel()
	go func() { // a settings change restarts the platform
		select {
		case <-t.wake:
			cancel()
		case <-ctx.Done():
		}
	}()
	t.setPhase("connecting", "")
	t.ow.Do(func() { go t.outboxLoop() })
	err := p.Start(ctx, bridge{t})
	switch {
	case ctx.Err() != nil:
		return context.Canceled // stopped on purpose (settings changed)
	case err == nil:
		return errors.New("the platform stopped")
	}
	return err
}

// --- routes -------------------------------------------------------------------------

func (t *Tile) routes(mux *http.ServeMux) {
	mux.Handle("GET /status", xbin.RoleFunc("admin", t.handleStatus))
	mux.Handle("PUT /config", xbin.RoleFunc("admin", t.handleConfig))
	mux.Handle("PUT /config/secrets", xbin.RoleFunc("admin", t.handleSecrets))
	mux.Handle("POST /reconnect", xbin.RoleFunc("admin", func(w http.ResponseWriter, r *http.Request) {
		t.kick()
		xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
	}))
	consoleRoutes(t, mux)
}

func (t *Tile) handleStatus(w http.ResponseWriter, r *http.Request) {
	t.mu.Lock()
	st := t.st
	st.Accounts = []accountView{}
	for _, v := range t.channels {
		st.Accounts = append(st.Accounts, v)
	}
	events := append([]eventLog{}, t.events...)
	p := t.plat
	t.mu.Unlock()
	info := Info{Name: st.Platform}
	secrets := map[string]bool{}
	if p != nil {
		info = p.Info()
		for _, s := range info.Secrets {
			secrets[s.Name] = t.secret(s.Name) != ""
		}
	}
	xbin.WriteJSON(w, 200, map[string]any{"state": st, "info": info, "secretsSet": secrets, "events": events,
		"platforms": platformNames(), "agent": t.agent.base != ""})
}

// mayWrite: settings and credentials need write access to this tile — anyone
// who can open its page reaches this backend at full role.
func mayWrite(w http.ResponseWriter, r *http.Request) bool {
	if c := xbin.Caller(r); c.User == "" || c.UserCanWrite() {
		return true
	}
	xbin.WriteError(w, http.StatusForbidden, "changing this bridge needs write access to it")
	return false
}

// handleConfig picks the platform: PUT /config {platform}.
func (t *Tile) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !mayWrite(w, r) {
		return
	}
	var c config
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		xbin.WriteError(w, 400, "need {platform}")
		return
	}
	if c.Platform != "" && choosePlatform(c.Platform) != c.Platform {
		xbin.WriteError(w, 400, "no platform "+c.Platform+" (registered: "+strings.Join(platformNames(), ", ")+")")
		return
	}
	b, _ := json.Marshal(c)
	if err := t.kv.Put("config", b); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	t.mu.Lock()
	t.cfg = c
	t.mu.Unlock()
	t.kick()
	xbin.WriteJSON(w, 200, c)
}

// handleSecrets stores the platform's declared secrets: PUT /config/secrets
// {name: value} ("" removes one). Other names are refused.
func (t *Tile) handleSecrets(w http.ResponseWriter, r *http.Request) {
	if !mayWrite(w, r) {
		return
	}
	var b map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&b); err != nil {
		xbin.WriteError(w, 400, "need {name: value}")
		return
	}
	p := t.platform()
	if p == nil {
		xbin.WriteError(w, 409, "no platform yet")
		return
	}
	fields := map[string]SecretField{}
	for _, s := range p.Info().Secrets {
		fields[s.Name] = s
	}
	for name, v := range b {
		f, ok := fields[name]
		v = strings.TrimSpace(v)
		switch {
		case !ok:
			xbin.WriteError(w, 400, name+" is not one of this platform's secrets")
			return
		case v != "" && f.Prefix != "" && !strings.HasPrefix(v, f.Prefix):
			xbin.WriteError(w, 400, f.Label+" starts with "+f.Prefix)
			return
		}
	}
	for name, v := range b {
		if err := t.setSecret(name, strings.TrimSpace(v)); err != nil {
			xbin.WriteError(w, 500, err.Error())
			return
		}
	}
	t.kick()
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// --- helpers ------------------------------------------------------------------------

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func main() {
	t := newTile(xbin.KV(xbin.Resource("state")), func(name string) string {
		v, _ := xbin.Secret(name)
		return v
	}, newAgentClient(os.Getenv("XBIN_IFACE_AGENT_URL"), xbin.Client()))
	t.setSecret = func(name, v string) error {
		if v == "" {
			return xbin.DeleteSecret(name)
		}
		return xbin.SetSecret(name, v)
	}
	t.replaySpool()
	go t.run()
	mux := http.NewServeMux()
	t.routes(mux)
	xbin.Serve(mux)
}
