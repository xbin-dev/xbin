// slack — a Slack adapter for agents built from the agent template (D86). It
// holds a Socket Mode connection to Slack, so it needs no public URL; it
// reports each message to the agent it is bound to (service agent-inbox —
// docs/agent-inbox.md) and posts the agent's replies. The agent owns the
// conversations, who may talk and what they may do; this tile knows Slack:
// its tokens and socket, its event shapes, mrkdwn, its rate limits.
//
// "alwaysOn" in the manifest keeps it running: nothing would ever send it the
// request that starts an ordinary tile (D84).
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Vault keys: the bot token (xoxb-…, the Web API) and the app-level token
// (xapp-…, Socket Mode). Written by this tile's own backend (D30).
const (
	secretBot = "bot-token"
	secretApp = "app-token"
)

const defaultAPIBase = "https://slack.com/api/"

// config is the tile's settings (kv "config").
type config struct {
	// APIBase is where the Web API lives: Slack, or a loopback fake in tests
	// (hack/fakeslack) — nothing else is accepted.
	APIBase string `json:"apiBase,omitempty"`
}

// state is what the settings page shows.
type state struct {
	Phase     string `json:"phase"` // needs-tokens | needs-agent | connecting | connected | error
	Detail    string `json:"detail,omitempty"`
	Team      string `json:"team,omitempty"`
	TeamID    string `json:"teamId,omitempty"`
	BotUser   string `json:"botUser,omitempty"` // the bot's user id (how it is mentioned)
	BotName   string `json:"botName,omitempty"`
	ChannelID int64  `json:"channelId,omitempty"` // this account on the agent
	Claimed   string `json:"claimed,omitempty"`   // the channel's state there: unclaimed | active | disabled
	Since     int64  `json:"since,omitempty"`     // connected since (unix)
}

// eventLog is one line of the page's recent activity.
type eventLog struct {
	At      int64  `json:"at"`
	Kind    string `json:"kind"` // in | out | error
	Where   string `json:"where,omitempty"`
	Who     string `json:"who,omitempty"`
	Text    string `json:"text,omitempty"`
	Verdict string `json:"verdict,omitempty"`
}

// store is the tile's kv (an interface so tests keep it in memory).
type store interface {
	Get(key string) ([]byte, error)
	Put(key string, val []byte) error
	Delete(key string) error
	List(prefix string) ([]string, error)
}

// Tile is the adapter.
type Tile struct {
	kv     store
	secret func(name string) string
	agent  *agentClient

	mu     sync.Mutex
	cfg    config
	st     state
	events []eventLog
	wake   chan struct{} // tokens or settings changed: start over
	names  map[string]nameEntry
	assist map[string]bool // assistant threads (channel + thread ts)
	posted map[int64]bool  // outbox rows posted by this process (not yet acked)

	work chan spoolItem
	ow   sync.Once // the outbox loop
	wo   sync.Once // the worker
}

func newTile(kv store, secret func(string) string, agent *agentClient) *Tile {
	t := &Tile{kv: kv, secret: secret, agent: agent, wake: make(chan struct{}, 1),
		names: map[string]nameEntry{}, assist: map[string]bool{}, posted: map[int64]bool{}, work: make(chan spoolItem, 1024)}
	if b, err := kv.Get("config"); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &t.cfg)
	}
	return t
}

func (t *Tile) api() *slackAPI {
	t.mu.Lock()
	defer t.mu.Unlock()
	return newSlackAPI(orStr(t.cfg.APIBase, defaultAPIBase))
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
	t.mu.Unlock()
	if !changed {
		return
	}
	switch phase {
	case "connected":
		_ = xbin.ClearStatus()
	case "connecting":
	case "needs-tokens":
		_ = xbin.Status("warn", "add the Slack tokens on this tile's page")
	case "needs-agent":
		_ = xbin.Status("warn", "bind this tile to an agent (its agent interface)")
	default:
		_ = xbin.Status("error", clip("Slack: "+detail, 120))
	}
}

func (t *Tile) logEvent(e eventLog) {
	e.At = time.Now().Unix()
	e.Text = clip(e.Text, 140)
	t.mu.Lock()
	t.events = append(t.events, e)
	if len(t.events) > 50 {
		t.events = t.events[len(t.events)-50:]
	}
	t.mu.Unlock()
}

// --- the supervisor -------------------------------------------------------------

var (
	errNotReady     = errors.New("not ready")
	errRefresh      = errors.New("slack asked for a new connection")
	errLinkDisabled = errors.New("Socket Mode is turned off for this Slack app — turn it on under Socket Mode in the app's settings")
)

// run keeps a session up: tokens, auth.test, hello to the agent, the socket.
// It waits for a change when something is missing, reconnects at once when
// Slack asks, and backs off (1 s doubling to 2 min) after failures.
func (t *Tile) run() {
	backoff := time.Second
	for {
		started := time.Now()
		err := t.session()
		switch {
		case errors.Is(err, errNotReady):
			<-t.wake
			continue
		case errors.Is(err, errRefresh):
			continue
		case errors.Is(err, errLinkDisabled):
			t.setPhase("error", err.Error())
			<-t.wake
			continue
		}
		if time.Since(started) > 5*time.Minute {
			backoff = time.Second
		}
		t.setPhase("error", err.Error())
		select {
		case <-t.wake:
		case <-time.After(jitter(backoff)):
		}
		if backoff *= 2; backoff > 2*time.Minute {
			backoff = 2 * time.Minute
		}
	}
}

func (t *Tile) session() error {
	bot, app := t.secret(secretBot), t.secret(secretApp)
	if bot == "" || app == "" {
		t.setPhase("needs-tokens", "")
		return errNotReady
	}
	if t.agent.base == "" {
		t.setPhase("needs-agent", "")
		return errNotReady
	}
	t.setPhase("connecting", "")
	api := t.api()
	who, err := api.authTest(bot)
	if err != nil {
		return err
	}
	ch, err := t.agent.hello(who)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.st.Team, t.st.TeamID, t.st.BotUser, t.st.BotName = who.Team, who.TeamID, who.UserID, who.User
	t.st.ChannelID, t.st.Claimed = ch.ChannelID, ch.State
	t.mu.Unlock()
	t.ow.Do(func() { go t.outboxLoop() })
	t.ensureWorker()
	return t.socket(api, app)
}

// --- routes -----------------------------------------------------------------------

func (t *Tile) routes(mux *http.ServeMux) {
	mux.Handle("GET /status", xbin.RoleFunc("admin", t.handleStatus))
	mux.Handle("PUT /config/tokens", xbin.RoleFunc("admin", t.handleTokens))
	mux.Handle("DELETE /config/tokens", xbin.RoleFunc("admin", t.handleTokens))
	mux.Handle("PUT /config", xbin.RoleFunc("admin", t.handleConfig))
	mux.Handle("POST /reconnect", xbin.RoleFunc("admin", func(w http.ResponseWriter, r *http.Request) {
		t.kick()
		xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
	}))
	mux.HandleFunc("GET /manifest", handleManifest)
}

func (t *Tile) handleStatus(w http.ResponseWriter, r *http.Request) {
	t.mu.Lock()
	st, events := t.st, append([]eventLog(nil), t.events...)
	cfg := t.cfg
	t.mu.Unlock()
	xbin.WriteJSON(w, 200, map[string]any{"state": st, "events": orEvents(events), "agent": t.agent.base != "",
		"hasBotToken": t.secret(secretBot) != "", "hasAppToken": t.secret(secretApp) != "", "apiBase": orStr(cfg.APIBase, defaultAPIBase)})
}

func orEvents(e []eventLog) []eventLog {
	if e == nil {
		return []eventLog{}
	}
	return e
}

// mayWrite: tokens and settings need write access to this tile — anyone who
// can merely open its page reaches this backend at full role.
func mayWrite(w http.ResponseWriter, r *http.Request) bool {
	if c := xbin.Caller(r); c.User == "" || c.UserCanWrite() {
		return true
	}
	xbin.WriteError(w, http.StatusForbidden, "changing this tile's settings needs write access to it")
	return false
}

// handleTokens stores the tokens (PUT {botToken?, appToken?}) or removes
// both (DELETE).
func (t *Tile) handleTokens(w http.ResponseWriter, r *http.Request) {
	if !mayWrite(w, r) {
		return
	}
	if r.Method == http.MethodDelete {
		_ = xbin.DeleteSecret(secretBot)
		_ = xbin.DeleteSecret(secretApp)
		t.kick()
		xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
		return
	}
	var b struct {
		BotToken string `json:"botToken"`
		AppToken string `json:"appToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		xbin.WriteError(w, 400, "need {botToken?, appToken?}")
		return
	}
	b.BotToken, b.AppToken = strings.TrimSpace(b.BotToken), strings.TrimSpace(b.AppToken)
	if b.BotToken != "" && !strings.HasPrefix(b.BotToken, "xoxb-") {
		xbin.WriteError(w, 400, "the bot token starts with xoxb- (OAuth & Permissions → Bot User OAuth Token)")
		return
	}
	if b.AppToken != "" && !strings.HasPrefix(b.AppToken, "xapp-") {
		xbin.WriteError(w, 400, "the app-level token starts with xapp- (Basic Information → App-Level Tokens, scope connections:write)")
		return
	}
	for name, v := range map[string]string{secretBot: b.BotToken, secretApp: b.AppToken} {
		if v == "" {
			continue
		}
		if err := xbin.SetSecret(name, v); err != nil {
			xbin.WriteError(w, 500, err.Error())
			return
		}
	}
	t.kick()
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleConfig sets the Web API base: Slack's, or a loopback fake.
func (t *Tile) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !mayWrite(w, r) {
		return
	}
	var c config
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		xbin.WriteError(w, 400, "need {apiBase}")
		return
	}
	if c.APIBase != "" && !apiBaseOK(c.APIBase) {
		xbin.WriteError(w, 400, "apiBase is https://slack.com/api/ or a loopback address (a test fake)")
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

func apiBaseOK(s string) bool {
	if s == defaultAPIBase {
		return true
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	h := u.Hostname()
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// --- small helpers -------------------------------------------------------------

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

func jitter(d time.Duration) time.Duration {
	return d + time.Duration(time.Now().UnixNano()%int64(d/4+1))
}

func main() {
	t := newTile(xbin.KV(xbin.Resource("state")), func(name string) string {
		v, _ := xbin.Secret(name)
		return v
	}, newAgentClient(os.Getenv("XBIN_IFACE_AGENT_URL"), xbin.Client()))
	t.replaySpool()
	go t.run()
	mux := http.NewServeMux()
	t.routes(mux)
	xbin.Serve(mux)
}
