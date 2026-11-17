//go:build linux && integration

package isolated

// wsagent_helpers_test.go — what wsagent_test.go drives the workspace agent
// with: a production xbind with the real sealed component and the scripted
// model and coding agent, people, and a person's conversation with their
// agent read the way its page reads it (the run's view, the session's
// cards, /ws/needs).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	wsAgent = "xbin/wsagent"
	wsAPI   = "/api/" + wsAgent

	wsNoteTile = "apps/wsnote" // a note with a backend, in no org; the scripted coding agent's binary is in it
)

// wsNote is a tile that holds one note and says who touched it: the person
// and — when the call is their agent's — the agent (X-XBin-Agent).
const wsNote = `package main

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	var mu sync.Mutex
	note, by, reads, writes := "first", "nobody", 0, 0
	who := func(r *http.Request) string {
		c := xbin.Caller(r)
		return fmt.Sprintf("user=%s agent=%s role=%s", c.User, c.Agent, c.Role)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /note", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		fmt.Fprintf(w, "note=%s read by %s", note, who(r))
	})
	mux.HandleFunc("PUT /note", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		note, by = string(b), who(r)
		writes++
		fmt.Fprintf(w, "written by %s", by)
	})
	// what happened to the note, for the test's own eyes: it counts nothing
	mux.HandleFunc("GET /seen", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "note=%s reads=%d writes=%d last write by %s", note, reads, writes, by)
	})
	xbin.Serve(mux)
}
`

// wsEnv is a production xbind with people, and what the tests ask of it.
type wsEnv struct {
	*sealedEnv
	t        *testing.T
	fake     string               // hack/fakeopenai's base URL
	signedIn map[string]time.Time // when each person's session (e.sess) signed in
}

// wsWakeWithin bounds how long after something it waits for changed — its
// person's decision, a tile agent's ended turn — a conversation is woken
// and has answered: seconds on an idle machine, with margin for a loaded
// one. Nothing in the path waits out a window (the workspace agent's wake
// rule: builtin-templates/agent/_backend/coord_wake.go).
const wsWakeWithin = 15 * time.Second

func wsPassword(id string) string { return "pw-" + id + "-5c2e81" }

// in makes t the test the helpers fail while it runs: a subtest's own.
func (e *wsEnv) in(t *testing.T) {
	prev := e.t
	e.t = t
	t.Cleanup(func() { e.t = prev })
}

// startWsAgent boots an xbind as an install runs one — this tree's binary
// with its release table and prebuilt backend, without --dev, under
// --isolate with owner auth and the vault — with the scripted coding agent
// registered as the provider "fake" (its binary in wsNoteTile's own
// directory, which that tile's sandbox mounts at the same path) and
// hack/fakeopenai connected as the LLM connector's one upstream.
func startWsAgent(t *testing.T) *wsEnv { return startWsAgentAt(t, wsNoteTile) }

// startWsAgentAt is startWsAgent with the scripted coding agent's binary in
// fakeTile's directory — which need not be a tile yet: a tile a plan
// creates there finds it in its sandbox (a real coding agent comes from
// the rootfs, and needs no such thing). fakeArgs are more arguments of the
// "fake" provider's command (sdk/acp's Fake reads some: --no-default-mode).
func startWsAgentAt(t *testing.T, fakeTile string, fakeArgs ...string) *wsEnv {
	t.Helper()
	a := xbindtest.Require(t)
	if a.Gocryptfs == "" {
		xbindtest.Skip(t, "no gocryptfs: people's partitions keep their data in encrypted volumes")
	}
	a = a.WithSealedBuilds(t)
	dir, err := os.MkdirTemp("", "xbindtest-wsagent-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { xbindtest.RemoveTree(dir) }) // after the daemon's own cleanup
	ws := filepath.Join(dir, "ws")
	acp := filepath.Join(ws, fakeTile, ".bin", "fakeacp")
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Production: true, WS: ws,
		Env: []string{"XBIN_VAULT_PASSPHRASE=xbindtest-vault", "XBIN_AGENT_FAKE=" + strings.Join(append([]string{acp}, fakeArgs...), " ")}})
	if err := os.MkdirAll(filepath.Dir(acp), 0o755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", acp, "./hack/fakeacp")
	build.Dir, build.Env = a.Repo, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hack/fakeacp: %v\n%s", err, out)
	}
	e := &wsEnv{sealedEnv: &sealedEnv{d: d, sess: map[string]string{}, frames: map[string]xbindtest.Header{}}, t: t, signedIn: map[string]time.Time{}}
	e.fake = xbindtest.FakeOpenAI(t, a)
	d.Must(t, "PUT", "/api/xbin/llm/upstreams/fake", map[string]any{"baseURL": e.fake + "/v1", "key": "sk-fake-WSAGENT-0000"}, 200)
	d.Must(t, "PUT", "/api/xbin/llm/config", map[string]any{"defaults": map[string]any{"model": wsModelA}}, 200)
	return e
}

// The two models of the one upstream (hack/fakeopenai serves both).
const (
	wsModelA = "fake/fake-chat"
	wsModelB = "fake/gpt-5-fake"
)

// person makes an account and signs it in.
func (e *wsEnv) person(id, role string, tiles map[string]string) {
	e.t.Helper()
	e.d.AddUser(e.t, id, wsPassword(id), role, tiles)
	e.signIn(id)
}

// signIn signs person in anew: a session of theirs whose sign-in is now.
func (e *wsEnv) signIn(id string) {
	e.t.Helper()
	e.sess[id], e.signedIn[id] = e.d.Login(e.t, id, wsPassword(id)), time.Now()
}

// staticTile writes a tile that is a page and an API.md, with no backend.
func (e *wsEnv) staticTile(tile, title string) {
	e.t.Helper()
	e.d.WriteTile(e.t, tile, map[string]string{
		"xbin.json":  fmt.Sprintf(`{"title": %q}`, title) + "\n",
		"index.html": "<!doctype html><html><head></head><body>" + title + "</body></html>\n",
		"API.md":     "# " + title + "\n\nThis tile has a page and no API of its own.\n",
	})
}

// org makes an org that owns tile, with rules on agents.
func (e *wsEnv) org(id, name, tile string, agents map[string]any) {
	e.t.Helper()
	e.d.Must(e.t, "POST", "/api/xbin/orgs", map[string]any{"id": id, "name": name}, 200)
	e.d.Must(e.t, "POST", "/api/xbin/owner", map[string]any{"tile": tile, "to": "org:" + id}, 200)
	e.d.Must(e.t, "PUT", "/api/xbin/orgs/"+id+"/agents", agents, 200)
}

// seen is what wsNoteTile says happened to its note, read as person's own
// page of the tile reads it (GET /seen counts nothing).
func (e *wsEnv) seen(person string) string {
	e.t.Helper()
	return e.d.Must(e.t, "GET", "/api/"+wsNoteTile+"/seen", nil, 200, e.frame(e.t, wsNoteTile, person)).String()
}

// wsRequest is a request as xbind keeps it (GET /delegate/requests).
type wsRequest struct {
	ID, User, Agent, Kind, Method, Target, Tile, Body, Summary, State, Grant string
	Preview                                                                  struct {
		Class, Level, Role string
		Session            struct {
			Tile, Provider, Mode, Prompt string
			Asks                         bool
		}
	}
}

// requests are person's pending requests at xbind, read with their own
// session, as the chrome reads them.
func (e *wsEnv) requests(person string) []wsRequest {
	e.t.Helper()
	var out struct{ Requests []wsRequest }
	e.d.Must(e.t, "GET", "/api/xbin/delegate/requests", nil, 200, e.as(person)...).Decode(e.t, &out)
	return out.Requests
}

// request is one request at xbind, as person's own session reads it.
func (e *wsEnv) request(person, id string) wsRequest {
	e.t.Helper()
	var out wsRequest
	e.d.Must(e.t, "GET", "/api/xbin/delegate/requests/"+id, nil, 200, e.as(person)...).Decode(e.t, &out)
	return out
}

// wsAuditRow is a row of a person's log (GET /delegate/audit).
type wsAuditRow struct {
	User, Agent, Method, Path, Tile, Class, Refused, Ticket, Under, Session string
	From, By                                                                string // a line an admin applied at another person's request
	Status                                                                  int
	Effect, DryRun                                                          bool
}

func (e *wsEnv) audit(person string) []wsAuditRow {
	e.t.Helper()
	var out struct{ Calls []wsAuditRow }
	e.d.Must(e.t, "GET", "/api/xbin/delegate/audit", nil, 200, e.as(person)...).Decode(e.t, &out)
	return out.Calls
}

// wsTileSession is a row of xbind's session directory (GET /term/sessions).
type wsTileSession struct {
	ID, Cwd, Kind, Provider, Mode, Status, Origin string
	Pending                                       int
}

func (e *wsEnv) tileAgents(person string) []wsTileSession {
	e.t.Helper()
	var all, out []wsTileSession
	e.d.Must(e.t, "GET", "/api/xbin/term/sessions", nil, 200, e.as(person)...).Decode(e.t, &all)
	for _, s := range all {
		if s.Kind == "agent" {
			out = append(out, s)
		}
	}
	return out
}

// --- a person's conversation with their agent ------------------------------------------

// wsConv is one session of a person's workspace agent, driven with the
// frame token its page holds.
type wsConv struct {
	e      *wsEnv
	person string
	fr     xbindtest.Header
	run    string // the session's id: its conversation
	handle string // its handle at xbind
	model  string
}

type wsSessionItem struct {
	ID                          int64
	Name, Model, Handle, Status string
	Summary, Waiting            string
	Main                        bool
	Tainted                     []string
	Agents, Requests            int
}

// agentPage waits for person's partition of the workspace agent to answer
// its page (the backend starts on the first call) and returns the page's
// credential.
func (e *wsEnv) agentPage(person string) xbindtest.Header {
	e.t.Helper()
	fr := e.frame(e.t, wsAgent, person)
	xbindtest.Eventually(e.t, 3*time.Minute, person+"'s agent answers GET /ws/state", func() (bool, string) {
		r := e.d.Call(e.t, "GET", wsAPI+"/ws/state", nil, fr)
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
	})
	return fr
}

// open is person's session made with body (POST /ws/sessions; {main:
// true}: their Main, made on first use).
func (e *wsEnv) open(person string, body map[string]any) *wsConv {
	e.t.Helper()
	fr := e.agentPage(person)
	var made struct{ Session wsSessionItem }
	e.d.Must(e.t, "POST", wsAPI+"/ws/sessions", body, 200, fr).Decode(e.t, &made)
	if made.Session.ID == 0 || made.Session.Handle == "" {
		e.t.Fatalf("%s's session %v: %+v, want an id and a handle at xbind", person, body, made.Session)
	}
	return &wsConv{e: e, person: person, fr: fr, run: fmt.Sprint(made.Session.ID), handle: made.Session.Handle, model: made.Session.Model}
}

// sessions are the person's sessions as their page lists them.
func (c *wsConv) sessions() []wsSessionItem {
	c.e.t.Helper()
	var out struct{ Sessions []wsSessionItem }
	c.e.d.Must(c.e.t, "GET", wsAPI+"/ws/sessions", nil, 200, c.fr).Decode(c.e.t, &out)
	return out.Sessions
}

// item is this session's row of the list.
func (c *wsConv) item() wsSessionItem {
	c.e.t.Helper()
	for _, s := range c.sessions() {
		if fmt.Sprint(s.ID) == c.run {
			return s
		}
	}
	c.e.t.Fatalf("%s's session %s is not in GET /ws/sessions", c.person, c.run)
	return wsSessionItem{}
}

// wsMsg is one message of the conversation, as the run's view has it.
type wsMsg struct {
	ID        int64
	Role      string
	Content   string
	Name      string // a tool result: the tool
	ToolCalls []struct {
		ID       string
		Function struct{ Name, Arguments string }
	}
}

func (c *wsConv) messages() []wsMsg {
	c.e.t.Helper()
	var v struct{ Messages []wsMsg }
	c.e.d.Must(c.e.t, "GET", wsAPI+"/runs/"+c.run+"/view", nil, 200, c.fr).Decode(c.e.t, &v)
	return v.Messages
}

// last is the id of the conversation's newest message.
func (c *wsConv) last() int64 {
	c.e.t.Helper()
	var id int64
	for _, m := range c.messages() {
		id = max(id, m.ID)
	}
	return id
}

func wsAfter(ms []wsMsg, id int64) []wsMsg {
	var out []wsMsg
	for _, m := range ms {
		if m.ID > id {
			out = append(out, m)
		}
	}
	return out
}

// wsTurn is what one scripted turn left in the conversation.
type wsTurn struct {
	t    *testing.T
	msgs []wsMsg
	Done string // the model's summary: "Done: <each tool result's first line>"
}

// say sends the person's message — a "ws:" script (hack/fakeopenai/ws.go) —
// and waits for the scripted model's summary: every step ran and the turn
// answered.
func (c *wsConv) say(script string) wsTurn {
	c.e.t.Helper()
	after := c.last()
	c.e.d.Must(c.e.t, "POST", wsAPI+"/runs/"+c.run+"/message", map[string]any{"text": script}, 200, c.fr)
	turn := wsTurn{t: c.e.t}
	xbindtest.Eventually(c.e.t, 2*time.Minute, c.person+"'s agent answers "+script, func() (bool, string) {
		turn.msgs = wsAfter(c.messages(), after)
		for _, m := range turn.msgs {
			if m.Role == "assistant" && strings.HasPrefix(m.Content, "Done: ") {
				turn.Done = m.Content
				return true, ""
			}
		}
		return false, wsTail(turn.msgs, 6)
	})
	return turn
}

// tool is what the nth call (from 0) of tool answered the model, whole.
func (tn wsTurn) tool(name string, nth int) string {
	tn.t.Helper()
	for _, m := range tn.msgs {
		if m.Role == "tool" && m.Name == name {
			if nth == 0 {
				return m.Content
			}
			nth--
		}
	}
	tn.t.Fatalf("the turn has no such answer of %s:\n%s", name, wsTail(tn.msgs, 12))
	return ""
}

// woken waits for the conversation to be woken after message id `after`:
// the agent's own "[workspace updates — …]" message carrying every one of
// want, and the scripted model's answer to it ("ok: [workspace updates…").
// It returns the updates as the model was told them.
func (c *wsConv) woken(after int64, timeout time.Duration, what string, want ...string) string {
	c.e.t.Helper()
	var told string
	xbindtest.Eventually(c.e.t, timeout, c.person+"'s conversation is woken: "+what, func() (bool, string) {
		ms := wsAfter(c.messages(), after)
		for i, m := range ms {
			if m.Role != "user" || !strings.HasPrefix(m.Content, "[workspace updates") || !wsHasAll(m.Content, want) {
				continue
			}
			for _, a := range ms[i+1:] {
				if a.Role == "assistant" && strings.HasPrefix(a.Content, "ok: [workspace updates") {
					told = m.Content
					return true, ""
				}
			}
		}
		return false, wsTail(ms, 6)
	})
	return told
}

func wsHasAll(s string, want []string) bool {
	for _, w := range want {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// wsTail is the last n of ms, a line each, for a failure's message.
func wsTail(ms []wsMsg, n int) string {
	if len(ms) > n {
		ms = ms[len(ms)-n:]
	}
	var b strings.Builder
	for _, m := range ms {
		text := m.Content
		for _, tc := range m.ToolCalls {
			text += " → " + tc.Function.Name + " " + tc.Function.Arguments
		}
		if len(text) > 700 {
			text = text[:700] + "…"
		}
		fmt.Fprintf(&b, "  #%d %s %s: %s\n", m.ID, m.Role, m.Name, strings.ReplaceAll(text, "\n", " ⏎ "))
	}
	if b.Len() == 0 {
		return "(no new message)"
	}
	return "\n" + b.String()
}

// wsCards is what the page draws beside the conversation (GET
// /ws/sessions/{id}/cards).
type wsCards struct {
	Requests []struct {
		ID, Brief                                                    int64
		Kind, Ref, CallID, Tile, Title, Method, Path, State, Outcome string
		By, Via, Grant, Result                                       string
		ResultStatus                                                 int
		Plan                                                         bool
		Lines                                                        []struct { // a decided plan's lines
			N                         int
			Kind, Line, Result, Error string
		}
	}
	Agents []struct {
		ID, Request                                                 int64 // Request: the request that started it
		Session, CallID, Tile, Harness, Mode, Status, Waiting, Last string
		Asks, Waits                                                 bool
		Held, Turn                                                  int
	}
	Briefs []struct {
		ID                                  int64
		Tile, Goal, Harness, State, Session string
	}
	Refusals []struct {
		ID                               int64
		CallID, Tool, Kind, Reason, Tile string
	}
}

func (c *wsConv) cards() wsCards {
	c.e.t.Helper()
	var out wsCards
	c.e.d.Must(c.e.t, "GET", wsAPI+"/ws/sessions/"+c.run+"/cards", nil, 200, c.fr).Decode(c.e.t, &out)
	return out
}

// wsNeed is one thing that waits for the person because of their agent.
type wsNeed struct {
	Kind, Request, TileSession, Tile, Text, Title string
	Session                                       int64
}

func (c *wsConv) needs() []wsNeed {
	c.e.t.Helper()
	var out struct {
		Items []wsNeed
		Count int
	}
	c.e.d.Must(c.e.t, "GET", wsAPI+"/ws/needs", nil, 200, c.fr).Decode(c.e.t, &out)
	if out.Count != len(out.Items) {
		c.e.t.Errorf("GET /ws/needs counts %d and lists %d", out.Count, len(out.Items))
	}
	return out.Items
}

// transcript logs the conversation's tail when the test failed.
func (c *wsConv) transcript(t *testing.T) {
	if t.Failed() {
		raw, _ := json.Marshal(c.cards())
		t.Logf("%s's session %s, its last messages:%s\nits cards: %s", c.person, c.run, wsTail(c.messages(), 14), raw)
	}
}

// --- the agent's own credential, from outside ------------------------------------------

// wsDelegateCall is one call made with a holder's delegate credential.
type wsDelegateCall struct {
	Status int
	Body   string
}

// asDelegate makes calls to xbind with the delegate credential of person's
// workspace agent itself — "Authorization: XBin-Delegate <the instance
// token of their partition of xbin/wsagent>" with the conversation's handle
// (sdk/delegate.go), on xbind's gateway socket, the one place that
// credential is one (internal/auth/delegate.go) — and returns each answer,
// in order. The real backend has no route that relays a call of the
// test's choosing, so the test makes them itself, from xbind's host: the
// token is a secret xbind hands to the backend's process and keeps nowhere
// else, so it is read where it lives — that process's environment — and
// never leaves the script (it is not logged, and not returned).
//
// calls are "METHOD path [json body]" lines. The first must be a read the
// agent may make: of the backend's generations found, the token whose read
// answers 200 is the live one, and proves the credential is the real thing
// before a refusal is read as one.
func (e *wsEnv) asDelegate(person, handle string, calls ...string) []wsDelegateCall {
	e.t.Helper()
	for _, v := range append([]string{person, handle, e.d.Workspace()}, calls...) {
		if strings.ContainsAny(v, "'\n") {
			e.t.Fatalf("asDelegate: %q can't be quoted for the host's shell", v)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "run=$(readlink -f -- '%s/.xbin/run') || { echo 'ERR no run dir'; exit 0; }\n", e.d.Workspace())
	b.WriteString("sock=\"$run/gateway.sock\"; [ -S \"$sock\" ] || { echo 'ERR no gateway socket'; exit 0; }\n")
	b.WriteString("command -v curl >/dev/null || { echo 'ERR no curl on the host'; exit 0; }\n")
	// every generation of that partition's backend: the processes whose environment names the component and the partition
	fmt.Fprintf(&b, `toks=$(for f in /proc/[0-9]*/environ; do tr '\0' '\n' < "$f" 2>/dev/null | awk -v c='XBIN_COMPONENT=%s' -v p='XBIN_PARTITION=user:%s' '$0==c{C=1} $0==p{P=1} /^XBIN_TOKEN=/{T=substr($0,12)} END{if(C&&P&&T!="")print T}'; done | sort -u)`+"\n", wsAgent, person)
	b.WriteString("[ -n \"$toks\" ] || { echo 'ERR no process of that partition has a readable environment'; exit 0; }\n")
	b.WriteString("out=$(mktemp) || { echo 'ERR mktemp'; exit 0; }\n")
	fmt.Fprintf(&b, `call() { m=$1; u=$2; shift 2; curl -s -m 30 -o "$out" -w '%%{http_code}' --unix-socket "$sock" -X "$m" -H "Authorization: XBin-Delegate $tok" -H 'X-XBin-Agent-Session: %s' "$@" "http://xbin$u"; }`+"\n", handle)
	first := strings.SplitN(calls[0], " ", 3)
	fmt.Fprintf(&b, "tok=; for t in $toks; do tok=$t; [ \"$(call '%s' '%s')\" = 200 ] && break; tok=; done\n", first[0], first[1])
	b.WriteString("[ -n \"$tok\" ] || { echo \"ERR none of the tokens found made the first call: $(head -c 300 \"$out\" | tr '\\n' ' ')\"; rm -f \"$out\"; exit 0; }\n")
	for _, c := range calls {
		f := strings.SplitN(c, " ", 3)
		body := ""
		if len(f) == 3 {
			body = " -H 'Content-Type: application/json' -d '" + f[2] + "'"
		}
		fmt.Fprintf(&b, "code=$(call '%s' '%s'%s); echo \"CALL $code $(head -c 2000 \"$out\" | tr '\\n' ' ')\"\n", f[0], f[1], body)
	}
	b.WriteString("rm -f \"$out\"\n")
	raw, err := e.d.HostSh(b.String())
	if err != nil {
		e.t.Fatalf("asDelegate: the host's script: %v\n%s", err, raw)
	}
	var out []wsDelegateCall
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if why, bad := strings.CutPrefix(line, "ERR "); bad {
			e.t.Fatalf("%s's agent's own credential couldn't be used from the host: %s", person, why)
		}
		if rest, ok := strings.CutPrefix(line, "CALL "); ok {
			code, body, _ := strings.Cut(rest, " ")
			var c wsDelegateCall
			fmt.Sscan(code, &c.Status)
			c.Body = body
			out = append(out, c)
		}
	}
	if len(out) != len(calls) {
		e.t.Fatalf("asDelegate: %d answers for %d calls:\n%s", len(out), len(calls), raw)
	}
	return out
}
