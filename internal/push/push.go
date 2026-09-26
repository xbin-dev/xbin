package push

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// Notification kinds. A device registers the kinds it wants; a registered
// kind matches itself and every kind under it ("agent" matches
// "agent.turn", "tile" matches "tile.alert"). No kinds = everything.
const (
	KindAgentPermission = "agent.permission" // an ACP permission.request
	KindAgentQuestion   = "agent.question"   // an ACP elicitation.request
	KindAgentTurn       = "agent.turn"       // an ACP turn.end
	KindTile            = "tile"             // POST /api/xbin/notify (tile.<kind> when the tile names one)
	KindTest            = "test"             // POST /api/xbin/push/test — always delivered
)

// OwnerUser is the user key of the bootstrap owner token (no user id), the
// same fallback terminal homes use.
const OwnerUser = "owner"

// Limits are the xbind-side rate limits. Tile bounds POST /notify per
// calling tile (a backend) or per tile and person (a frontend or terminal,
// which notify only the person using them — a bucket of their own, so a
// reader can't exhaust the backend's); User what every tile together sends
// one user; Agent what agent sessions send one user and Session what one
// session raises — a budget of their own, so tiles cannot starve a
// permission request; Test the user's own POST /push/test; Register the
// user's POST /devices/push. User, Agent and Test count relay posts — a
// notification costs one per device it goes to — so the workspace's relay
// budget, shared by everyone, bounds what one person can spend of it.
type Limits struct {
	Tile, User, Agent, Session, Test, Register Rate
}

// DefaultLimits are the limits when Options.Limits is zero.
var DefaultLimits = Limits{
	Tile:     Rate{PerHour: 120, Burst: 20},
	User:     Rate{PerHour: 240, Burst: 40},
	Agent:    Rate{PerHour: 240, Burst: 40},
	Session:  Rate{PerHour: 120, Burst: 20},
	Test:     Rate{PerHour: 60, Burst: 10},
	Register: Rate{PerHour: 30, Burst: 10},
}

// Account is what the push plane needs to know about a user.
type Account struct {
	Exists   bool
	Disabled bool
	Created  int64 // unix; registrations older than this belong to an earlier account with the id
}

// Options configure a Service.
type Options struct {
	Dir string // where push.json lives (data/push)
	// RelayURL / RelayKey come from XBIN_PUSH_RELAY / XBIN_PUSH_RELAY_KEY.
	// Both set: the relay is configured by the environment and the admin
	// route is read-only. URL alone: the relay an admin's opt-in uses when
	// it names none.
	RelayURL, RelayKey string
	// CanRead reports whether user may read tile (false for unknown or
	// disabled users) — the gate on POST /notify.
	CanRead func(user, tile string) bool
	// Account looks a user up (nil: every user exists and is enabled).
	// Nothing is pushed to a disabled user, a deleted user's registrations
	// go, and so do registrations older than the account.
	Account func(user string) Account
	// Live reports whether the login a registration was made with (its
	// Session, a credential generation) still lives for user (OwnerUser for
	// the bootstrap owner) — without counting as its activity. Registrations
	// of ended logins go. nil: every login lives (no auth to ask).
	Live func(user, gen string) bool
	// IsAdmin gates the relay configuration routes.
	IsAdmin func(auth.Principal) bool
	HTTP    *http.Client
	Now     func() time.Time
	Limits  Limits
	// Backoff is the wait before retry attempt n (1-based); nil = 2s·4ⁿ⁻¹,
	// at most 5 min. A relay's Retry-After wins when longer.
	Backoff func(attempt int) time.Duration
	// Grace holds an agent permission request or question this long before
	// pushing it, so one answered at once (a session rule, a user at the
	// desk) raises nothing. 0 = 3s; negative = no hold.
	Grace     time.Duration
	QueueSize int // pending notifications before new ones are dropped (0 = 256)
	Workers   int // concurrent relay posts (0 = 2)
	Log       *slog.Logger
}

// Service is the push plane of one workspace.
type Service struct {
	o     Options
	st    *store
	snd   *sender
	tile  *limiter
	user  *limiter
	agent *limiter
	sess  *limiter
	self  *limiter
	reg   *limiter

	mu   sync.Mutex
	held map[string]*time.Timer // agent requests inside their grace period
}

// New opens the store and starts the sender.
func New(o Options) (*Service, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if o.Limits == (Limits{}) {
		o.Limits = DefaultLimits
	}
	if o.Grace == 0 {
		o.Grace = 3 * time.Second
	}
	if o.CanRead == nil {
		o.CanRead = func(string, string) bool { return false }
	}
	if o.IsAdmin == nil {
		o.IsAdmin = func(p auth.Principal) bool { return p.IsAdmin() }
	}
	st, err := openStore(o.Dir)
	if err != nil {
		return nil, err
	}
	s := &Service{o: o, st: st, held: map[string]*time.Timer{},
		tile: newLimiter(o.Limits.Tile, o.Now), user: newLimiter(o.Limits.User, o.Now), agent: newLimiter(o.Limits.Agent, o.Now),
		sess: newLimiter(o.Limits.Session, o.Now), self: newLimiter(o.Limits.Test, o.Now), reg: newLimiter(o.Limits.Register, o.Now)}
	s.snd = newSender(s)
	return s, nil
}

// Close stops the sender; queued notifications are dropped.
func (s *Service) Close() {
	s.mu.Lock()
	for k, t := range s.held {
		t.Stop()
		delete(s.held, k)
	}
	s.mu.Unlock()
	s.snd.close()
}

// Workspace is this workspace's push id, the `ws` of every payload.
func (s *Service) Workspace() string { return s.st.workspace() }

// storedRelay is the relay configuration in force or kept: the
// environment's, else the admin's (Off when the admin turned push off).
func (s *Service) storedRelay() (c *RelayConfig, source string) {
	if s.o.RelayURL != "" && s.o.RelayKey != "" {
		return &RelayConfig{URL: s.o.RelayURL, Key: s.o.RelayKey}, "env"
	}
	if c := s.st.relay(); c != nil {
		return c, "admin"
	}
	return nil, ""
}

// relayConfig is the relay in force: nil while push is off.
func (s *Service) relayConfig() (c *RelayConfig, source string) {
	c, src := s.storedRelay()
	if c == nil || c.Off {
		return nil, ""
	}
	return c, src
}

// Enabled reports whether a relay is configured.
func (s *Service) Enabled() bool { c, _ := s.relayConfig(); return c != nil }

// epoch names the relay workspace a key belongs to — every handle that
// delivered under one epoch is bound to it at the relay. A hash of the key,
// never the key.
func epoch(key string) string {
	if key == "" {
		return ""
	}
	h := sha256.Sum256([]byte("xbin-push-epoch\x00" + key))
	return b64.EncodeToString(h[:12])
}

// currentEpoch is the epoch of the stored relay (even while off: the apps
// may renew their handles before push comes back).
func (s *Service) currentEpoch() string {
	c, _ := s.storedRelay()
	if c == nil {
		return ""
	}
	return epoch(c.Key)
}

// ForgetDevice drops a device's registration (device login calls it when a
// device is revoked). False when it had none.
func (s *Service) ForgetDevice(user, deviceID string) bool { return s.st.remove(user, deviceID) }

// ForgetUser drops a user's registrations and preferences (the account is
// gone).
func (s *Service) ForgetUser(user string) { s.st.removeUser(user, true) }

// SignedOut drops a user's registrations and keeps their preferences
// ("sign out everywhere", a disabled account): a device the user no longer
// holds must not keep reading their notifications. The app registers again
// when the user signs back in. Returns how many went.
func (s *Service) SignedOut(user string) int { return s.st.removeUser(user, false) }

// live reports whether d's login still lives: an enrolled device's own
// registration always (its removal drops it), another login's while that
// login does.
func (s *Service) live(d Device) bool {
	return d.Session == "" || s.o.Live == nil || s.o.Live(d.User, d.Session)
}

// devices is the user's registrations whose logins still live; the others
// are dropped on the way.
func (s *Service) devices(user string) []Device {
	all := s.st.devices(user)
	out := all[:0]
	dead := false
	for _, d := range all {
		if s.live(d) {
			out = append(out, d)
		} else {
			dead = true
		}
	}
	if dead {
		s.st.removeIf(func(d Device) bool { return d.User == user && !s.live(d) })
	}
	return out
}

// pruneDead drops every registration whose login ended (the admin views).
func (s *Service) pruneDead() { s.st.removeIf(func(d Device) bool { return !s.live(d) }) }

// account resolves a user for delivery: false for unknown or disabled
// users. An unknown user's registrations are dropped on the way.
func (s *Service) account(user string) (Account, bool) {
	if s.o.Account == nil || user == OwnerUser {
		return Account{Exists: true}, true
	}
	a := s.o.Account(user)
	if !a.Exists {
		if s.st.hasDevices(user) {
			s.st.removeUser(user, true)
		}
		return a, false
	}
	return a, !a.Disabled
}

// UserKey is the push identity of a human principal: the user id, or
// "owner" for the bootstrap token. False for element (tile) principals.
func UserKey(p auth.Principal) (string, bool) {
	switch {
	case p.Component != "":
		return "", false
	case p.UserID != "":
		return p.UserID, true
	case p.Owner:
		return OwnerUser, true
	}
	return "", false
}

// note is one notification to one user, before it is fanned out to their
// devices and sealed.
type note struct {
	user, tile string // tile: the source tile, for mutes ("" = none)
	kind       string
	title      string
	body       string
	link       string
	collapse   string
	priority   int
}

func kindAllowed(kinds []string, k string) bool {
	if k == KindTest || len(kinds) == 0 {
		return true
	}
	for _, want := range kinds {
		if want == k || strings.HasPrefix(k, want+".") {
			return true
		}
	}
	return false
}

// Payload size bounds: the sealed payload must fit the relay's envelope
// limit (3200 base64url characters of ciphertext ≈ 2384 bytes of JSON).
const (
	maxTitle = 120  // runes
	maxBody  = 1000 // runes
	maxLink  = 512  // bytes
	maxPlain = 2300 // bytes of payload JSON
)

func marshalPayload(p Payload) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func cut(s string, runes int) string {
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	r := []rune(s)
	return string(r[:max(runes-1, 0)]) + "…"
}

// fit truncates a payload until its JSON fits maxPlain.
func fit(p Payload) ([]byte, error) {
	p.Title, p.Body = cut(p.Title, maxTitle), cut(p.Body, maxBody)
	if len(p.Link) > maxLink {
		p.Link = ""
	}
	for {
		b, err := marshalPayload(p)
		if err != nil || len(b) <= maxPlain {
			return b, err
		}
		switch n := utf8.RuneCountInString(p.Body); {
		case n > 1:
			p.Body = cut(p.Body, n*3/4)
		case p.Body != "":
			p.Body = ""
		case utf8.RuneCountInString(p.Title) > 1:
			p.Title = cut(p.Title, utf8.RuneCountInString(p.Title)*3/4)
		default:
			return b, nil // nothing left to cut (cannot happen with our own fields)
		}
	}
}

// relayCollapse is the collapse id the relay (and APNs) see: a hash, so
// neither learns session ids or tile paths.
func (s *Service) relayCollapse(c string) string {
	if c == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s.Workspace() + "\x00" + c))
	return b64.EncodeToString(h[:24])
}

// enqueue hands a note to the sender without blocking.
func (s *Service) enqueue(n note) bool { return s.snd.enqueue(job{note: &n}) }

// tileBase names a tile in notification text: its last path element.
func tileBase(tile string) string {
	if tile == "" {
		return "workspace"
	}
	return path.Base(tile)
}

// AgentEvent is the ACP session hook (wired next to the /ws/events
// publisher): a permission request, a question or a turn end becomes a push
// to the session owner's devices. Requests wait out the grace period and
// are dropped when answered within it. Never blocks.
func (s *Service) AgentEvent(user, session, tile string, ev agent.Event) {
	switch ev.Type {
	case agent.EvPermissionResolved, agent.EvElicitResolved:
		// may release a held request (below)
	case agent.EvPermissionRequest, agent.EvElicitRequest, agent.EvTurnEnd:
		if !s.Enabled() || !s.st.hasDevices(user) {
			return
		}
		if _, ok := s.account(user); !ok {
			return // disabled (or gone): nothing reaches their devices
		}
	default:
		return // the pump calls this for every event: stay cheap
	}
	if user == "" || session == "" {
		return
	}
	var d struct {
		PID        json.RawMessage `json:"pid"`
		EID        json.RawMessage `json:"eid"`
		Message    string          `json:"message"`
		StopReason string          `json:"stopReason"`
		ToolCall   struct {
			Title string `json:"title"`
		} `json:"toolCall"`
		Meta *struct {
			Title string `json:"title"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(ev.Data, &d)
	link := "agent/" + session
	base := tileBase(tile)
	switch ev.Type {
	case agent.EvPermissionRequest:
		what := d.ToolCall.Title
		if d.Meta != nil && d.Meta.Title != "" {
			what = d.Meta.Title
		}
		if what == "" {
			what = "A tool call is waiting for your answer."
		}
		id := "agent:" + session + ":perm:" + rawID(d.PID)
		s.hold(id, note{user: user, kind: KindAgentPermission, title: "Permission needed — " + base, body: what, link: link, collapse: id}, session)
	case agent.EvPermissionResolved:
		s.release("agent:" + session + ":perm:" + rawID(d.PID))
	case agent.EvElicitRequest:
		msg := d.Message
		if msg == "" {
			msg = "The agent is asking you something."
		}
		id := "agent:" + session + ":q:" + rawID(d.EID)
		s.hold(id, note{user: user, kind: KindAgentQuestion, title: "Question — " + base, body: msg, link: link, collapse: id}, session)
	case agent.EvElicitResolved:
		s.release("agent:" + session + ":q:" + rawID(d.EID))
	case agent.EvTurnEnd:
		title, body := "Agent finished — "+base, "The turn is done."
		switch d.StopReason {
		case "cancelled":
			return // the user stopped it themselves
		case "end_turn", "":
		case "error":
			title, body = "Agent stopped — "+base, "The turn ended with an error."
		default:
			title, body = "Agent stopped — "+base, "Stop reason: "+d.StopReason
		}
		s.agentPush(note{user: user, kind: KindAgentTurn, title: title, body: body, link: link, collapse: "agent:" + session + ":turn"}, session)
	}
}

func rawID(r json.RawMessage) string {
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	return strings.Trim(string(r), `"`)
}

func (s *Service) hold(id string, n note, session string) {
	if s.o.Grace < 0 {
		s.agentPush(n, session)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.held[id]; t != nil {
		t.Stop()
	}
	s.held[id] = time.AfterFunc(s.o.Grace, func() {
		s.mu.Lock()
		_, still := s.held[id]
		delete(s.held, id)
		s.mu.Unlock()
		if still {
			s.agentPush(n, session)
		}
	})
}

func (s *Service) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.held[id]; t != nil {
		t.Stop()
		delete(s.held, id)
	}
}

// agentPush applies the session and per-user agent limits (a limited agent
// push is dropped, not queued) and enqueues. Tiles have a budget of their
// own (Limits.User) and cannot spend this one. The user's agent budget is
// charged per device the push goes to.
func (s *Service) agentPush(n note, session string) {
	if !s.Enabled() {
		return
	}
	posts := s.posts(n.user, n.kind)
	if posts == 0 {
		return
	}
	if ok, _ := s.sess.allow(session); !ok {
		s.snd.limited.Add(1)
		s.o.Log.Debug("push: agent session rate-limited", "session", session)
		return
	}
	if ok, _ := s.agent.allowN(n.user, posts); !ok {
		s.snd.limited.Add(1)
		s.o.Log.Debug("push: user's agent pushes rate-limited", "user", n.user)
		return
	}
	s.enqueue(n)
}

// wants reports whether any of the user's usable registrations takes kind.
func (s *Service) wants(user, kind string) bool { return s.posts(user, kind) > 0 }

// posts is how many relay posts a note of kind to user makes: one per
// usable registration that takes it.
func (s *Service) posts(user, kind string) int {
	e, n := s.currentEpoch(), 0
	for _, d := range s.devices(user) {
		if !d.stale(e) && kindAllowed(d.Kinds, kind) {
			n++
		}
	}
	return n
}
