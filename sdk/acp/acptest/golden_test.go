package acptest

// The golden transcripts: every script and reply path of the scripted agent
// as it played when it was hack/fakeacp's main package, driven over its
// stdio like a client would and recorded frame by frame ("> " the client's
// line, "< " the agent's, then how it exited). The moved engine must
// reproduce them byte for byte.
//
//	ACPTEST_CAPTURE=<binary> go test -run TestGolden   re-record from a binary
//	ACPTEST_BIN=<binary> go test -run TestGolden       compare a binary

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// frame is one line the agent wrote.
type frame struct {
	raw    string
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Update struct {
			Kind    string          `json:"sessionUpdate"`
			Status  string          `json:"status"`
			Content json.RawMessage `json:"content"` // a block, or a call's list
		} `json:"update"`
	} `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (f *frame) isRequest() bool { return f.Method != "" && len(f.ID) > 0 }

// text is a chunk's text.
func (f *frame) text() string {
	var b struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(f.Params.Update.Content, &b)
	return b.Text
}

// driver plays the client's half of one scenario and keeps the transcript.
type driver struct {
	t     testing.TB
	in    io.WriteCloser
	lines chan string // the agent's lines; closed at its EOF
	log   []string
	dir   string            // $DIR in a client line: a scratch directory
	auto  map[string]string // an agent request's method → the reply's body
	exit  func() int        // waits for the agent to end: its exit code
}

const wait = 10 * time.Second

func newDriver(t testing.TB, in io.WriteCloser, out io.Reader, dir string) *driver {
	d := &driver{t: t, in: in, lines: make(chan string, 1024), dir: dir, auto: map[string]string{}}
	go func() {
		r := bufio.NewReaderSize(out, 1<<20)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				d.lines <- strings.TrimSuffix(line, "\n")
			}
			if err != nil {
				close(d.lines)
				return
			}
		}
	}()
	return d
}

// send writes one client line ($DIR expanded) and records it as written.
func (d *driver) send(line string) {
	d.t.Helper()
	d.log = append(d.log, "> "+line)
	if _, err := io.WriteString(d.in, strings.ReplaceAll(line, "$DIR", d.dir)+"\n"); err != nil {
		d.t.Fatalf("send: %v\n%s", err, d.transcript())
	}
}

func (d *driver) call(id int, method, params string) {
	d.t.Helper()
	d.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"` + method + `","params":` + params + `}`)
}

func (d *driver) notify(method, params string) {
	d.t.Helper()
	d.send(`{"jsonrpc":"2.0","method":"` + method + `","params":` + params + `}`)
}

// reply answers an agent's request; body is `"result":…` or `"error":…`.
func (d *driver) reply(f *frame, body string) {
	d.t.Helper()
	d.send(`{"jsonrpc":"2.0","id":` + string(f.ID) + `,` + body + `}`)
}

// until reads (and records) the agent's lines until one matches; requests
// with an auto reply are answered on the way.
func (d *driver) until(what string, match func(*frame) bool) *frame {
	d.t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-d.lines:
			if !ok {
				d.t.Fatalf("agent ended waiting for %s\n%s", what, d.transcript())
			}
			d.log = append(d.log, "< "+line)
			var f frame
			if err := json.Unmarshal([]byte(line), &f); err != nil {
				d.t.Fatalf("agent wrote a bad line %q\n%s", line, d.transcript())
			}
			f.raw = line
			if body, ok := d.auto[f.Method]; ok && f.isRequest() {
				d.reply(&f, body)
			}
			if match(&f) {
				return &f
			}
		case <-timer.C:
			d.t.Fatalf("timed out waiting for %s\n%s", what, d.transcript())
		}
	}
}

func (d *driver) response(id int) *frame {
	d.t.Helper()
	want := strconv.Itoa(id)
	return d.until("response "+want, func(f *frame) bool { return f.Method == "" && string(f.ID) == want })
}

func (d *driver) request(method string) *frame {
	d.t.Helper()
	return d.until(method, func(f *frame) bool { return f.isRequest() && f.Method == method })
}

func (d *driver) update(kind, text string) *frame {
	d.t.Helper()
	return d.until(kind+" "+text, func(f *frame) bool {
		return f.Method == "session/update" && f.Params.Update.Kind == kind && (text == "" || f.text() == text)
	})
}

// eof reads the agent's remaining lines until it closes its output.
func (d *driver) eof() {
	d.t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-d.lines:
			if !ok {
				return
			}
			d.log = append(d.log, "< "+line)
		case <-timer.C:
			d.t.Fatalf("the agent did not end\n%s", d.transcript())
		}
	}
}

// finish closes the agent's input, reads it out and records its exit.
func (d *driver) finish() {
	d.t.Helper()
	_ = d.in.Close()
	d.eof()
	d.log = append(d.log, "exit "+strconv.Itoa(d.exit()))
}

func (d *driver) transcript() string { return strings.Join(d.log, "\n") + "\n" }

const (
	initParams = `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true},"clientInfo":{"name":"golden","version":"1"}}`
	newParams  = `{"cwd":"/work","mcpServers":[]}`
)

func promptParams(text string) string {
	b, _ := json.Marshal(text)
	return `{"sessionId":"fake-1","prompt":[{"type":"text","text":` + string(b) + `}]}`
}

// handshake: initialize, session/new, and the slash commands that follow.
func (d *driver) handshake() {
	d.t.Helper()
	d.call(1, "initialize", initParams)
	d.response(1)
	d.call(2, "session/new", newParams)
	d.response(2)
	d.update("available_commands_update", "")
}

// turn prompts and waits for the turn's answer.
func (d *driver) turn(id int, text string) {
	d.t.Helper()
	d.call(id, "session/prompt", promptParams(text))
	d.response(id)
}

// permission prompts, answers the permission request with body, and waits
// for the turn's answer.
func (d *driver) permission(prompt, body string) {
	d.t.Helper()
	d.call(3, "session/prompt", promptParams(prompt))
	d.reply(d.request("session/request_permission"), body)
	d.response(3)
}

func selected(option string) string {
	return `"result":{"outcome":{"outcome":"selected","optionId":"` + option + `"}}`
}

const (
	cancelledOutcome = `"result":{"outcome":{"outcome":"cancelled"}}`
	rpcFailure       = `"error":{"code":-32603,"message":"boom"}`
)

// terminal answers the terminal/* requests of the term and run: scripts.
func (d *driver) terminal(exit int, output string) {
	out, _ := json.Marshal(output)
	d.auto["terminal/create"] = `"result":{"terminalId":"term-1"}`
	d.auto["terminal/wait_for_exit"] = `"result":{"exitCode":` + strconv.Itoa(exit) + `,"signal":null}`
	d.auto["terminal/output"] = `"result":{"output":` + string(out) + `,"truncated":false}`
	d.auto["terminal/release"] = `"result":{}`
}

// goldenScenarios is every behaviour hack/fakeacp had when its engine moved.
var goldenScenarios = []struct {
	name string
	play func(d *driver)
}{
	{"handshake", func(d *driver) {
		d.handshake()
		d.call(3, "authenticate", `{"methodId":"api-key"}`)
		d.response(3)
		d.call(4, "foo/bar", `{}`)
		d.response(4)
		d.send(`not json`)
		d.send(`{"jsonrpc":"2.0"}`)
		d.notify("session/cancel", `{"sessionId":"fake-1"}`) // nothing in flight: no answer
		d.call(5, "session/set_mode", `{"sessionId":"fake-1","modeId":"yolo"}`)
		d.response(5)
		d.call(6, "session/set_config_option", `{"sessionId":"fake-1","configId":"model","value":"fake-fast"}`)
		d.response(6)
		d.call(7, "session/set_config_option", `{"sessionId":"fake-1","configId":"model","value":"nope"}`)
		d.response(7)
		d.call(8, "session/set_config_option", `{"sessionId":"fake-1","configId":"effort","value":"high"}`)
		d.response(8)
		d.finish()
	}},
	{"echo", func(d *driver) {
		d.handshake()
		d.turn(3, "hello")
		d.turn(4, "again, <with> & \"quotes\"")
		d.finish()
	}},
	{"title-long", func(d *driver) {
		d.handshake()
		d.turn(3, "a first prompt that is certainly longer than forty characters")
		d.finish()
	}},
	{"attachments", func(d *driver) {
		d.handshake()
		d.call(3, "session/prompt", `{"sessionId":"fake-1","prompt":[`+
			`{"type":"text","text":"look"},{"type":"text","text":"at these"},`+
			`{"type":"image","mimeType":"image/png","data":"aGVsbG8="},`+
			`{"type":"image","mimeType":"image/gif","data":"!!"},`+
			`{"type":"resource","resource":{"uri":"file:///x/notes.md","mimeType":"text/markdown","text":"abc"}},`+
			`{"type":"resource"},`+
			`{"type":"resource_link","uri":"file://$DIR/data.bin","name":"data.bin"},`+
			`{"type":"resource_link","uri":"https://example.com/x","name":"x"},`+
			`{"type":"resource_link","uri":"file:///nonexistent/acptest/y","name":"y"},`+
			`{"type":"audio","mimeType":"audio/wav","data":"AAAA"}]}`)
		d.response(3)
		d.finish()
	}},
	{"perm-once", func(d *driver) { d.handshake(); d.permission("perm", selected("once")); d.finish() }},
	{"perm-always", func(d *driver) { d.handshake(); d.permission("perm please", selected("always")); d.finish() }},
	{"perm-no", func(d *driver) { d.handshake(); d.permission("perm", selected("no")); d.finish() }},
	{"perm-cancelled", func(d *driver) { d.handshake(); d.permission("perm", cancelledOutcome); d.finish() }},
	{"perm-error", func(d *driver) { d.handshake(); d.permission("perm", rpcFailure); d.finish() }},
	{"perm-yolo", func(d *driver) {
		d.handshake()
		d.call(3, "session/set_mode", `{"sessionId":"fake-1","modeId":"yolo"}`)
		d.response(3)
		d.turn(4, "perm")
		d.finish()
	}},
	{"perm-cancel", func(d *driver) {
		d.handshake()
		d.call(3, "session/prompt", promptParams("perm"))
		req := d.request("session/request_permission")
		d.notify("session/cancel", `{"sessionId":"fake-1"}`)
		d.response(3)
		d.reply(req, cancelledOutcome)
		d.until("the failed call", func(f *frame) bool { return f.Params.Update.Status == "failed" })
		d.finish()
	}},
	{"plan-approve", func(d *driver) { d.handshake(); d.permission("plan it", selected("exit-plan-default")); d.finish() }},
	{"plan-reject", func(d *driver) { d.handshake(); d.permission("plan", selected("reject")); d.finish() }},
	{"plan-cancelled", func(d *driver) { d.handshake(); d.permission("plan", cancelledOutcome); d.finish() }},
	{"ask-accept", func(d *driver) {
		d.handshake()
		d.auto["elicitation/create"] = `"result":{"action":"accept","content":{"question_0":"Postgres","question_1":["Metrics","Tracing"],"question_1_custom":"Logs"}}`
		d.turn(3, "ask me")
		d.finish()
	}},
	{"ask-decline", func(d *driver) {
		d.handshake()
		d.auto["elicitation/create"] = `"result":{"action":"decline"}`
		d.turn(3, "ask")
		d.finish()
	}},
	{"ask-cancel", func(d *driver) {
		d.handshake()
		d.auto["elicitation/create"] = `"result":{"action":"cancel"}`
		d.turn(3, "ask")
		d.finish()
	}},
	{"ask-error", func(d *driver) {
		d.handshake()
		d.auto["elicitation/create"] = rpcFailure
		d.turn(3, "ask")
		d.finish()
	}},
	{"subagent", func(d *driver) { d.handshake(); d.turn(3, "subagent please"); d.finish() }},
	{"think", func(d *driver) { d.handshake(); d.turn(3, "think hard"); d.finish() }},
	{"slow", func(d *driver) { d.handshake(); d.turn(3, "slow"); d.finish() }},
	{"slow-cancel", func(d *driver) {
		d.handshake()
		d.call(3, "session/prompt", promptParams("go slow"))
		d.update("agent_message_chunk", "tick 2 ")
		d.notify("session/cancel", `{"sessionId":"fake-1"}`)
		d.response(3)
		d.finish()
	}},
	{"burst", func(d *driver) { d.handshake(); d.turn(3, "burst"); d.finish() }},
	{"paras", func(d *driver) { d.handshake(); d.turn(3, "paras 2"); d.turn(4, "parasx"); d.finish() }},
	{"chatty", func(d *driver) { d.handshake(); d.turn(3, "chatty 3"); d.turn(4, "chatty"); d.finish() }},
	{"long", func(d *driver) { d.handshake(); d.turn(3, "long 12"); d.finish() }},
	{"term", func(d *driver) { d.handshake(); d.terminal(0, "hi\n0\n"); d.turn(3, "a term please"); d.finish() }},
	{"term-error", func(d *driver) {
		d.handshake()
		d.auto["terminal/create"] = `"error":{"code":-32601,"message":"method not found: terminal/create"}`
		d.turn(3, "term")
		d.finish()
	}},
	{"run-ok", func(d *driver) { d.handshake(); d.terminal(0, "ok\n"); d.turn(3, "run: echo ok"); d.finish() }},
	{"run-fail", func(d *driver) {
		d.handshake()
		d.terminal(1, "no such\nfile\n")
		d.turn(3, "run:  ls /nope ")
		d.finish()
	}},
	{"env", func(d *driver) {
		d.handshake()
		d.call(3, "session/set_config_option", `{"sessionId":"fake-1","configId":"model","value":"fake-fast"}`)
		d.response(3)
		d.auto["fs/read_text_file"] = `"result":{"content":" {\"theme\":\"dark\"}\n"}`
		d.turn(4, "env")
		d.finish()
	}},
	{"env-none", func(d *driver) {
		d.handshake()
		d.auto["fs/read_text_file"] = `"error":{"code":-32002,"message":"no such file"}`
		d.turn(3, "env")
		d.finish()
	}},
	{"write", func(d *driver) {
		d.handshake()
		d.auto["fs/write_text_file"] = `"result":{}`
		d.turn(3, "write")
		d.finish()
	}},
	{"write-error", func(d *driver) {
		d.handshake()
		d.auto["fs/write_text_file"] = `"error":{"code":-32603,"message":"read-only"}`
		d.turn(3, "write")
		d.finish()
	}},
	{"fail", func(d *driver) { d.handshake(); d.turn(3, "fail"); d.finish() }},
	{"crash", func(d *driver) {
		d.handshake()
		d.call(3, "session/prompt", promptParams("crash"))
		d.eof() // the agent exits by itself
		_ = d.in.Close()
		d.log = append(d.log, "exit "+strconv.Itoa(d.exit()))
	}},
	{"load", func(d *driver) {
		d.call(1, "initialize", initParams)
		d.response(1)
		d.call(2, "session/load", `{"sessionId":"s-old","cwd":"/work","mcpServers":[]}`)
		d.response(2)
		d.call(3, "session/prompt", `{"sessionId":"s-old","prompt":[{"type":"text","text":"hi"}]}`)
		d.response(3)
		d.finish()
	}},
}

// runBinary starts bin as the agent, with a fixed environment.
func runBinary(t *testing.T, bin, dir string) *driver {
	cmd := exec.Command(bin)
	cmd.Env = []string{"HOME=/home/fake", "FAKE_API_KEY=sekrit"}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := newDriver(t, in, out, dir)
	d.exit = func() int {
		err := cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
		if !strings.Contains(stderr.String(), "fakeacp: up") {
			t.Errorf("stderr lacks the greeting: %q", stderr.String())
		}
		return 0
	}
	return d
}

func TestGolden(t *testing.T) {
	capture, bin := os.Getenv("ACPTEST_CAPTURE"), os.Getenv("ACPTEST_BIN")
	if capture == "" && bin == "" {
		t.Skip("set ACPTEST_CAPTURE or ACPTEST_BIN to a fakeacp binary")
	}
	if capture != "" {
		bin = capture
		if err := os.MkdirAll(filepath.Join("testdata", "golden"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, sc := range goldenScenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "data.bin"), []byte("7 bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			d := runBinary(t, bin, dir)
			sc.play(d)
			file := filepath.Join("testdata", "golden", sc.name+".txt")
			if capture != "" {
				if err := os.WriteFile(file, []byte(d.transcript()), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if got := d.transcript(); got != string(want) {
				t.Errorf("%s differs from its golden:\n%s", sc.name, diffLines(string(want), got))
			}
		})
	}
}

// diffLines names the first line where two transcripts part.
func diffLines(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "line " + strconv.Itoa(i+1) + ":\n  want " + a + "\n  got  " + b
		}
	}
	return "(equal)"
}
