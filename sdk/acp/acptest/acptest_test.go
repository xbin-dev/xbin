package acptest

// The flags, scripts and methods added with the move, driven over io.Pipe
// with raw JSON-RPC lines as a client would; and the test binary serving as
// the agent (TestMain → MainIfAdapter, Command).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	MainIfAdapter()
	os.Exit(m.Run())
}

// get walks a frame's JSON by keys (object fields, or list indexes as "0"…).
func get(f *frame, keys ...string) any {
	var v any
	_ = json.Unmarshal([]byte(f.raw), &v)
	for _, k := range keys {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			i := 0
			for _, c := range k {
				i = i*10 + int(c-'0')
			}
			if i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

func str(v any) string { s, _ := v.(string); return s }

// quick skips the scripts' pauses but keeps the 50 ms before the slash
// commands, which the handshake waits for after session/new's answer.
func quick(d time.Duration) {
	if d < 100*time.Millisecond {
		time.Sleep(d)
	}
}

// start is Serve over pipes with HOME in a fresh directory.
func start(t *testing.T, o Options) (*driver, string) {
	home := t.TempDir()
	o.Getenv = func(k string) string { return map[string]string{"HOME": home}[k] }
	return runServe(t, home, o), home
}

func (d *driver) chunkText(text string) *frame {
	d.t.Helper()
	return d.update("agent_message_chunk", text)
}

func TestParseArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		want Options
	}{
		{nil, Options{}},
		{[]string{"--steer", "--auto-mode", "--require-login", "--persist", "--device-ms=250"},
			Options{Steer: true, AutoMode: true, RequireLogin: true, Persist: true, DeviceDelay: 250 * time.Millisecond}},
		{[]string{"-steer", "--script", "x", "positional", "--device-ms", "40", "--persist=false"},
			Options{Steer: true, DeviceDelay: 40 * time.Millisecond}},
		{[]string{"--device-ms=0"}, Options{DeviceDelay: -1}},
		{[]string{"--device-ms", "--steer"}, Options{Steer: true}},
		{[]string{"--device-ms=soon", "--unknown=1"}, Options{}},
	} {
		if got := ParseArgs(c.args); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseArgs(%q) = %+v, want %+v", c.args, got, c.want)
		}
	}
}

func TestSteer(t *testing.T) {
	d, _ := start(t, Options{Steer: true, wait: quick})
	d.call(1, "initialize", initParams)
	if r := d.response(1); get(r, "result", "_meta", "steering", "supported") != true {
		t.Fatalf("initialize doesn't advertise steering: %s", r.raw)
	}
	d.call(2, "session/new", newParams)
	d.response(2)
	steer := `{"sessionId":"fake-1","prompt":[{"type":"text","text":"use tabs"}]}`
	optIn := `{"sessionId":"fake-1","prompt":[{"type":"text","text":"later"}],"_meta":{"steering":{"idleBehavior":"promptRequired"}}}`

	// idle, opted in: the client sends it as the next prompt itself
	d.call(3, "_session/steering", optIn)
	if r := d.response(3); str(get(r, "result", "outcome")) != "promptRequired" || str(get(r, "result", "reason")) != "noRunningTurn" {
		t.Fatalf("idle steer with promptRequired: %s", r.raw)
	}
	// idle, not opted in: a turn of its own, with no prompt to answer
	d.call(4, "_session/steering", steer)
	if r := d.response(4); str(get(r, "result", "outcome")) != "startedNewTurn" {
		t.Fatalf("idle steer: %s", r.raw)
	}
	d.chunkText("echo: use tabs")
	d.update("usage_update", "")
	// bad params
	d.call(5, "_session/steering", `{"sessionId":"fake-1","prompt":[]}`)
	if r := d.response(5); get(r, "error", "code") != float64(-32602) {
		t.Fatalf("an empty steer: %s", r.raw)
	}
	d.call(6, "_session/steering", `{"sessionId":"fake-1","prompt":[{"type":"text","text":"x"}],"_meta":{"steering":{"idleBehavior":"queue"}}}`)
	if r := d.response(6); get(r, "error", "code") != float64(-32602) {
		t.Fatalf("an unknown idleBehavior: %s", r.raw)
	}
	d.finish()
}

func TestSteerIntoTurn(t *testing.T) {
	release := make(chan struct{})
	var first sync.Once
	d, _ := start(t, Options{Steer: true, wait: func(d time.Duration) {
		if d == 200*time.Millisecond { // the first tick's pause holds until both steers are in
			first.Do(func() { <-release })
		}
		quick(d)
	}})
	d.handshake()
	d.call(3, "session/prompt", promptParams("steer me"))
	d.chunkText("tick 0 ")
	d.call(4, "_session/steering", `{"sessionId":"fake-1","prompt":[{"type":"text","text":"use tabs"}],"_meta":{"steering":{"idleBehavior":"promptRequired"}}}`)
	d.call(5, "_session/steering", `{"sessionId":"fake-1","prompt":[{"type":"text","text":"and tests"}]}`)
	for _, id := range []int{4, 5} {
		if r := d.response(id); str(get(r, "result", "outcome")) != "injected" {
			t.Fatalf("steer during a turn: %s", r.raw)
		}
	}
	close(release)
	d.chunkText("steered: use tabs")
	d.chunkText("steered: and tests")
	d.chunkText("tick 1 ")
	d.chunkText("steers: use tabs | and tests")
	d.response(3)

	// any script's next chunk reports a steer; without --steer the method is unknown
	d.finish()
	d2, _ := start(t, Options{})
	d2.handshake()
	d2.call(3, "_session/steering", `{"sessionId":"fake-1","prompt":[{"type":"text","text":"x"}]}`)
	if r := d2.response(3); get(r, "error", "code") != float64(-32601) {
		t.Fatalf("steering without --steer: %s", r.raw)
	}
	d2.finish()
}

func TestAutoMode(t *testing.T) {
	d, _ := start(t, Options{AutoMode: true, wait: quick})
	d.call(1, "initialize", initParams)
	d.response(1)
	d.call(2, "session/new", newParams)
	r := d.response(2)
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, str(get(r, "result", "modes", "availableModes", string(rune('0'+i)), "id")))
	}
	if strings.Join(ids, ",") != "ask,auto,yolo" {
		t.Fatalf("modes %v, want ask,auto,yolo: %s", ids, r.raw)
	}
	// ask: the edit asks
	d.call(3, "session/prompt", promptParams("perm-edit"))
	req := d.request("session/request_permission")
	if get(req, "params", "toolCall", "kind") != "edit" || str(get(req, "params", "toolCall", "content", "0", "path")) != "/work/hello.txt" {
		t.Fatalf("perm-edit's request: %s", req.raw)
	}
	d.reply(req, selected("once"))
	d.chunkText("edited hello.txt")
	d.response(3)
	// auto: the edit doesn't ask, a command still does
	d.call(4, "session/set_mode", `{"sessionId":"fake-1","modeId":"auto"}`)
	d.response(4)
	d.call(5, "session/prompt", promptParams("perm-edit"))
	d.until("the edit's end", func(f *frame) bool {
		if f.isRequest() {
			t.Fatalf("auto asked for an edit: %s", f.raw)
		}
		return f.text() == "edited hello.txt"
	})
	d.response(5)
	d.call(6, "session/prompt", promptParams("perm"))
	d.reply(d.request("session/request_permission"), selected("no"))
	d.chunkText("denied")
	d.response(6)
	d.finish()
}

func TestRequireLogin(t *testing.T) {
	d, home := start(t, Options{RequireLogin: true, Self: []string{"/bin/fakeacp"}})
	d.call(1, "initialize", `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false,"_meta":{"terminal-auth":true}}}`)
	r := d.response(1)
	var methods []string
	for i := 0; i < 3; i++ {
		methods = append(methods, str(get(r, "result", "authMethods", string(rune('0'+i)), "id")))
	}
	if strings.Join(methods, ",") != "fake-login,fake-api-key,fake-device" ||
		get(r, "result", "authMethods", "0", "type") != "terminal" || get(r, "result", "authMethods", "0", "args", "0") != "login" ||
		get(r, "result", "authMethods", "0", "_meta", "terminal-auth", "command") != "/bin/fakeacp" ||
		get(r, "result", "authMethods", "0", "_meta", "terminal-auth", "args", "0") != "login" ||
		get(r, "result", "authMethods", "1", "_meta", "api-key") == nil {
		t.Fatalf("authMethods: %s", r.raw)
	}
	d.call(2, "session/new", newParams)
	d.response(2)
	// signed out: the status, then -32000
	d.call(3, "session/prompt", promptParams("hello"))
	d.until("the sign-out status", func(f *frame) bool {
		return f.Method == "_auth/status_update" && get(f, "params", "authStatus", "kind") == "none"
	})
	if r := d.response(3); get(r, "error", "code") != float64(-32000) {
		t.Fatalf("a signed-out prompt: %s", r.raw)
	}
	for id, c := range map[int]struct{ params, err string }{
		4: {`{"methodId":"fake-api-key"}`, "no key"},
		5: {`{"methodId":"fake-api-key","_meta":{"api-key":{"apiKey":"bad"}}}`, "invalid API key"},
		6: {`{"methodId":"fake-device"}`, "device code needs URL elicitation"},
		7: {`{"methodId":"fake-login"}`, "terminal"},
	} {
		d.call(id, "authenticate", c.params)
		if r := d.response(id); !strings.Contains(str(get(r, "error", "message")), c.err) {
			t.Fatalf("authenticate %s: %s, want %q", c.params, r.raw, c.err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".fakeacp", "credentials")); err == nil {
		t.Fatal("a failed sign-in wrote the credentials")
	}
	d.call(8, "authenticate", `{"methodId":"fake-api-key","_meta":{"api-key":{"apiKey":"sk-secret-1"}}}`)
	if r := d.response(8); string(r.Result) != "{}" {
		t.Fatalf("authenticate with a key: %s", r.raw)
	}
	creds, err := os.ReadFile(filepath.Join(home, ".fakeacp", "credentials"))
	if err != nil || strings.Contains(string(creds), "sk-secret") {
		t.Fatalf("credentials %q (%v): written, never the key", creds, err)
	}
	d.turn(9, "hello")
	d.finish()
}

func TestDeviceCode(t *testing.T) {
	for _, accept := range []bool{true, false} {
		d, home := start(t, Options{RequireLogin: true, DeviceDelay: 20 * time.Millisecond})
		d.call(1, "initialize", `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false,"elicitation":{"form":{},"url":{}}}}`)
		d.response(1)
		d.call(2, "authenticate", `{"methodId":"fake-device"}`)
		req := d.request("elicitation/create")
		if get(req, "params", "mode") != "url" || get(req, "params", "url") != "https://example.invalid/device" ||
			get(req, "params", "message") != "Enter code FAKE-1234 at https://example.invalid/device" || str(get(req, "params", "elicitationId")) == "" {
			t.Fatalf("the device code's elicitation: %s", req.raw)
		}
		if !accept {
			d.reply(req, `"result":{"action":"decline"}`)
			if r := d.response(2); r.Error == nil {
				t.Fatalf("a declined device sign-in: %s", r.raw)
			}
			d.finish()
			continue
		}
		d.reply(req, `"result":{"action":"accept"}`)
		done := d.until("elicitation/complete", func(f *frame) bool { return f.Method == "elicitation/complete" })
		if get(done, "params", "elicitationId") != get(req, "params", "elicitationId") {
			t.Fatalf("elicitation/complete names another elicitation: %s", done.raw)
		}
		if r := d.response(2); string(r.Result) != "{}" {
			t.Fatalf("authenticate: %s", r.raw)
		}
		if _, err := os.Stat(filepath.Join(home, ".fakeacp", "credentials")); err != nil {
			t.Fatal("the device sign-in wrote no credentials")
		}
		d.finish()
	}
}

// A terminal sign-in (Login) lands in the HOME a signed-out agent reads:
// its next prompt goes through.
func TestTerminalLogin(t *testing.T) {
	d, home := start(t, Options{RequireLogin: true})
	d.handshake()
	d.call(3, "session/prompt", promptParams("hello"))
	d.response(3)
	var out strings.Builder
	if code := Login(strings.NewReader("nope\n"), &out, home); code != 1 || !strings.Contains(out.String(), "Wrong code.") {
		t.Fatalf("a wrong code: %d %q", code, out.String())
	}
	out.Reset()
	if code := Login(strings.NewReader("fake-code\n"), &out, home); code != 0 ||
		out.String() != "Open https://example.invalid/login and paste the code:\nSigned in.\n" {
		t.Fatalf("the right code: %d %q", code, out.String())
	}
	d.turn(4, "hello")
	d.finish()
}

func TestPersist(t *testing.T) {
	home := t.TempDir()
	env := func(k string) string { return map[string]string{"HOME": home}[k] }
	d := runServe(t, home, Options{Persist: true, Getenv: env})
	d.call(1, "initialize", initParams)
	d.response(1)
	sid := str(get(d.sessionNew(2, newParams), "result", "sessionId"))
	if !strings.HasPrefix(sid, "fake-") || sid == "fake-1" {
		t.Fatalf("a persisted session's id %q", sid)
	}
	d.call(3, "session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"hello"}]}`)
	d.response(3)
	d.finish()
	// the session's history: its prompt (as a user chunk) and what the
	// agent sent, in the order they happened
	var sent []string
	for _, l := range d.log {
		if strings.HasPrefix(l, `> {"jsonrpc":"2.0","id":3,"method":"session/prompt"`) {
			sent = append(sent, `{"content":{"text":"hello","type":"text"},"sessionUpdate":"user_message_chunk"}`)
		}
		if strings.HasPrefix(l, `< {"jsonrpc":"2.0","method":"session/update"`) {
			var f frame
			_ = json.Unmarshal([]byte(l[2:]), &f)
			f.raw = l[2:]
			b, _ := json.Marshal(get(&f, "params", "update"))
			sent = append(sent, string(b))
		}
	}

	// another process loads it: exactly that history
	d = runServe(t, home, Options{Persist: true, Getenv: env})
	d.call(1, "initialize", initParams)
	d.response(1)
	d.call(2, "session/load", `{"sessionId":"`+sid+`","cwd":"/work","mcpServers":[]}`)
	d.response(2)
	var replayed []string
	for _, l := range d.log {
		if strings.HasPrefix(l, `< {"jsonrpc":"2.0","method":"session/update"`) {
			var f frame
			_ = json.Unmarshal([]byte(l[2:]), &f)
			f.raw = l[2:]
			if get(&f, "params", "sessionId") != sid {
				t.Fatalf("a replayed update for another session: %s", l)
			}
			b, _ := json.Marshal(get(&f, "params", "update"))
			replayed = append(replayed, string(b))
		}
	}
	if !reflect.DeepEqual(replayed, sent) || len(sent) < 5 {
		t.Fatalf("replayed\n  %s\nwant\n  %s", strings.Join(replayed, "\n  "), strings.Join(sent, "\n  "))
	}
	for i, id := range []string{"fake-none", "../escape", ".hidden"} {
		d.call(3+i, "session/load", `{"sessionId":"`+id+`","cwd":"/work","mcpServers":[]}`)
		if r := d.response(3 + i); get(r, "error", "code") != float64(-32002) {
			t.Fatalf("loading %q: %s", id, r.raw)
		}
	}
	d.finish()
}

func TestNewScripts(t *testing.T) {
	d, _ := start(t, Options{wait: quick})
	d.handshake()

	d.call(3, "session/prompt", promptParams("todo"))
	var plans [][]string
	d.until("todo done", func(f *frame) bool {
		if f.Params.Update.Kind == "plan" {
			var st []string
			for i := 0; i < 3; i++ {
				st = append(st, str(get(f, "params", "update", "entries", string(rune('0'+i)), "status")))
			}
			plans = append(plans, st)
		}
		return f.text() == "todo done"
	})
	d.response(3)
	if want := [][]string{{"pending", "pending", "pending"}, {"in_progress", "pending", "pending"}, {"completed", "completed", "completed"}}; !reflect.DeepEqual(plans, want) {
		t.Fatalf("todo's plans %v", plans)
	}

	d.call(4, "session/prompt", promptParams("cards"))
	kinds := map[string]string{} // toolCallId → kind
	completed := map[string]bool{}
	d.until("cards done", func(f *frame) bool {
		switch f.Params.Update.Kind {
		case "tool_call":
			kinds[str(get(f, "params", "update", "toolCallId"))] = str(get(f, "params", "update", "kind"))
		case "tool_call_update":
			completed[str(get(f, "params", "update", "toolCallId"))] = f.Params.Update.Status == "completed"
		}
		return f.text() == "cards done"
	})
	d.response(4)
	var got []string
	for id, k := range kinds {
		if !completed[id] {
			t.Errorf("card %s (%s) never completed", id, k)
		}
		got = append(got, k)
	}
	if want := "delete,edit,execute,fetch,move,other,read,search,think"; strings.Join(sorted(got), ",") != want {
		t.Fatalf("cards' kinds %v, want %s", sorted(got), want)
	}

	d.call(5, "session/prompt", promptParams("stall"))
	d.chunkText("stalling")
	d.notify("session/cancel", `{"sessionId":"fake-1"}`)
	if r := d.response(5); get(r, "result", "stopReason") != "cancelled" {
		t.Fatalf("a cancelled stall: %s", r.raw)
	}
	d.turn(6, "after") // nothing of the stall's in between
	if tail := d.log[len(d.log)-5:]; !strings.Contains(tail[0], `"id":5`) || !strings.Contains(tail[1], `"after"`) {
		t.Fatalf("the stall said more after its cancel:\n%s", strings.Join(tail, "\n"))
	}
	d.finish()
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// The test binary is the agent: Command starts it (TestMain serves), with
// flags, and `login` is its terminal sign-in.
func TestCommand(t *testing.T) {
	home := t.TempDir()
	argv := Command("--steer", "--require-login")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"HOME=" + home}
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := newDriver(t, in, out, home)
	d.exit = func() int {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("the agent: %v", err)
		}
		return 0
	}
	d.call(1, "initialize", `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false,"_meta":{"terminal-auth":true}}}`)
	r := d.response(1)
	if get(r, "result", "_meta", "steering", "supported") != true ||
		!reflect.DeepEqual(get(r, "result", "authMethods", "0", "_meta", "terminal-auth", "args"), []any{AdapterArg, "login"}) {
		t.Fatalf("the test binary's initialize: %s", r.raw)
	}
	d.finish()

	login := exec.Command(argv[0], AdapterArg, "login")
	login.Env = []string{"HOME=" + home}
	login.Stdin = strings.NewReader("fake-code\n")
	if b, err := login.CombinedOutput(); err != nil || !strings.Contains(string(b), "Signed in.") {
		t.Fatalf("login: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(home, ".fakeacp", "credentials")); err != nil {
		t.Fatal("login wrote no credentials")
	}
}

// Without --require-login authenticate answers {} for any method, as the
// plain agent always did, and signs nobody in.
func TestAuthenticateWithoutFlag(t *testing.T) {
	d, home := start(t, Options{})
	d.call(1, "initialize", `{"protocolVersion":1,"clientCapabilities":{"elicitation":{"url":{}}}}`)
	d.response(1)
	for i, p := range []string{`{"methodId":"fake-api-key"}`, `{"methodId":"fake-api-key","_meta":{"api-key":{"apiKey":"k"}}}`,
		`{"methodId":"fake-device"}`, `{"methodId":"fake-login"}`} {
		d.call(2+i, "authenticate", p)
		if r := d.response(2 + i); string(r.Result) != "{}" {
			t.Fatalf("authenticate %s without --require-login: %s", p, r.raw)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".fakeacp")); err == nil {
		t.Fatal("authenticate without --require-login wrote under $HOME/.fakeacp")
	}
	d.finish()
}

// Once Serve returns, a turn still winding down writes nothing more to
// $HOME (a test's TempDir is being removed).
func TestNoWritesAfterServe(t *testing.T) {
	release := make(chan struct{})
	tenth := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	d, home := start(t, Options{Persist: true, wait: func(dur time.Duration) {
		if dur != 200*time.Millisecond {
			quick(dur)
			return
		}
		<-release
		mu.Lock()
		calls++
		if calls == 10 {
			close(tenth)
		}
		mu.Unlock()
	}})
	d.call(1, "initialize", initParams)
	d.response(1)
	sid := str(get(d.sessionNew(2, newParams), "result", "sessionId"))
	d.call(3, "session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"slow"}]}`)
	d.chunkText("tick 0 ")
	d.finish() // Serve has returned
	file := filepath.Join(home, ".fakeacp", "sessions", sid+".jsonl")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-tenth:
	case <-time.After(wait):
		t.Fatal("the turn never wound down")
	}
	if after, _ := os.ReadFile(file); len(after) != len(before) {
		t.Fatalf("the history grew after Serve returned:\n%s", after[len(before):])
	}
}
