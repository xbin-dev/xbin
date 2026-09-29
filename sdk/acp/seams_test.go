package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// frameRecorder keeps each frame the client writes (Encode writes one per
// call).
type frameRecorder struct {
	w      io.WriteCloser
	mu     sync.Mutex
	frames []string
}

func (r *frameRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	r.frames = append(r.frames, strings.TrimSuffix(string(b), "\n"))
	r.mu.Unlock()
	return r.w.Write(b)
}

func (r *frameRecorder) Close() error { return r.w.Close() }

func (r *frameRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.frames...)
}

func recordFrames(spawn Spawner) (Spawner, *frameRecorder) {
	rec := &frameRecorder{}
	return func(ctx context.Context, cfg Config) (*Process, error) {
		p, err := spawn(ctx, cfg)
		if err == nil {
			rec.w, p.Stdin = p.Stdin, rec
		}
		return p, err
	}, rec
}

// startWith starts a client with options against the standard fake; setup
// prepares the fake (and may name its Drop in o).
func startWith(t *testing.T, o ClientOptions, setup func(f *fakeAgent, o *ClientOptions), cfg Config) (*Client, *fakeAgent, *frameRecorder, error) {
	t.Helper()
	f, spawn := newFake(echoEnd)
	if setup != nil {
		setup(f, &o)
	}
	spawn, rec := recordFrames(spawn)
	if cfg.Provider.ID == "" {
		cfg.Provider = Provider{ID: "fake", Login: "fake-login"}
	}
	cfg.Cwd, cfg.Spawn, cfg.Version = "/w/apps/x", spawn, "test"
	if cfg.Perms == nil {
		cfg.Perms = NewPermissions()
	}
	c := NewWith(o)
	err := c.Start(context.Background(), cfg)
	return c, f, rec, err
}

// With no Caps the client advertises exactly what xbind's always did: the
// initialize and session/new frames are byte for byte those captured before
// the client moved into the SDK (xbind's own golden:
// internal/agent/acp/wire_test.go).
func TestDefaultWireGolden(t *testing.T) {
	claude, _ := Lookup("claude")
	c, _, rec, err := startWith(t, ClientOptions{}, nil, Config{Provider: Provider{ID: "fake", SessionMeta: claude.SessionMeta}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, idle)
	want := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true,"_meta":{"subagent-transcript":true,"terminal_output":true,"terminal_output_delta":true},"elicitation":{"form":{}}},"clientInfo":{"name":"xbin","version":"test"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/w/apps/x","mcpServers":[],"_meta":{"claudeCode":{"options":{"thinking":{"display":"summarized","type":"adaptive"}}}}}}`,
	}
	if got := rec.all(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the default handshake changed\n got %s\nwant %s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	d := DefaultCaps()
	d.Meta["x"] = true // a fresh value each call
	if _, shared := DefaultCaps().Meta["x"]; shared {
		t.Fatal("DefaultCaps shares its _meta map")
	}
}

// Caps replaces the advertised capabilities verbatim: a client that serves
// no files or terminals says so.
func TestCapsSeam(t *testing.T) {
	caps := ClientCapabilities{Meta: map[string]any{"terminal_output": true, "terminal-auth": true}, Elicitation: &ElicitationCaps{Form: &struct{}{}}}
	c, f, _, err := startWith(t, ClientOptions{Caps: &caps}, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, idle)
	f.mu.Lock()
	got := string(f.caps)
	f.mu.Unlock()
	if want := `{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false,"_meta":{"terminal-auth":true,"terminal_output":true},"elicitation":{"form":{}}}`; got != want {
		t.Fatalf("caps\n got %s\nwant %s", got, want)
	}
}

// OnExt sees the notifications the client does not handle itself (an
// extension, an unknown method) — never the ones it does — and what it
// handles is not logged as ignored.
func TestOnExtSeam(t *testing.T) {
	var mu sync.Mutex
	var seen, logs []string
	o := ClientOptions{OnExt: func(cfg Config, m *Message) bool {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, m.Method+" "+cfg.Meta["k"])
		return m.Method == "_x/log"
	}}
	c, f, _, err := startWith(t, o, nil, Config{Meta: map[string]string{"k": "v"}, Log: func(s string) { mu.Lock(); logs = append(logs, s); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, idle)
	_ = f.conn.Notify("_x/log", map[string]string{"text": "hi"})
	_ = f.conn.Notify("weird/thing", nil)
	f.update(map[string]any{"sessionUpdate": UpCurrentMode, "currentModeId": "yolo"})
	collect(t, c, func(e Event) bool { return e.Type == EvStatus && data(e)["currentMode"] == "yolo" })
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seen, ",") != "_x/log v,weird/thing v" {
		t.Fatalf("OnExt saw %q", seen)
	}
	if strings.Join(logs, ",") != "ignoring notification weird/thing" {
		t.Fatalf("logs %q", logs)
	}
}

// AuthHint words the sign-in; the error still unwraps to the agent's
// -32000, and the status carries the login.
func TestAuthHintSeam(t *testing.T) {
	o := ClientOptions{AuthHint: func(cfg Config, msg string) string { return msg + " — sign " + cfg.Provider.ID + " in" }}
	c, _, _, err := startWith(t, o, func(f *fakeAgent, _ *ClientOptions) {
		f.newErr = &Error{Code: CodeAuthRequired, Message: "Authentication required"}
	}, Config{})
	var re *Error
	if err == nil || err.Error() != "session/new: Authentication required — sign fake in" || !errors.As(err, &re) || re.Code != CodeAuthRequired {
		t.Fatalf("Start: %v", err)
	}
	es := collect(t, c, func(e Event) bool { return false })
	last := data(es[len(es)-1])
	if last["status"] != StatusError || last["login"] == nil {
		t.Fatalf("the error status names the login: %v", last)
	}
}

// Without Drop a prompt with files is refused before anything moves, and
// the session stays usable.
func TestNilDropRefusesFiles(t *testing.T) {
	c, f, _, err := startWith(t, ClientOptions{}, func(f *fakeAgent, _ *ClientOptions) { f.promptCaps = &PromptCapabilities{Image: true} }, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, idle)
	atts, _ := PrepareAttachments([]Attachment{{Name: "a.png", Data: png}})
	if err := c.Prompt(context.Background(), Prompt{Text: "see", Attachments: atts}); !errors.Is(err, ErrUnsupportedContent) {
		t.Fatalf("files without a Drop: %v", err)
	}
	if err := c.Send(context.Background(), "text"); err != nil {
		t.Fatalf("the refusal left the session busy: %v", err)
	}
	collect(t, c, func(e Event) bool { return e.Type == EvTurnEnd })
	f.mu.Lock()
	n := f.nprompts
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("turns: %d", n)
	}
}

// InlineBudget bounds a prompt's inline images: past it an image is a file
// only; negative sends none inline (and an agent without the image
// capability takes the images as files).
func TestInlineBudgetSeam(t *testing.T) {
	img := append(append([]byte(nil), png...), make([]byte, 100)...)
	run := func(budget int, image bool) string {
		t.Helper()
		c, f, _, err := startWith(t, ClientOptions{InlineBudget: budget}, func(f *fakeAgent, o *ClientOptions) {
			f.promptCaps = &PromptCapabilities{Image: image}
			o.Drop = f.drop
		}, Config{})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		collect(t, c, idle)
		atts, _ := PrepareAttachments([]Attachment{{Name: "a.png", Data: img}, {Name: "b.png", Data: img}})
		if err := c.Prompt(context.Background(), Prompt{Text: "see", Attachments: atts}); err != nil {
			t.Fatal(err)
		}
		collect(t, c, func(e Event) bool { return e.Type == EvTurnEnd })
		var kinds []string
		for _, b := range f.promptBlocks() {
			kinds = append(kinds, b["type"].(string))
		}
		return strings.Join(kinds, " ")
	}
	if got := run(len(img)+10, true); got != "text image resource_link resource_link" {
		t.Fatalf("a budget for one image: %s", got)
	}
	if got := run(-1, false); got != "text resource_link resource_link" {
		t.Fatalf("no inline images: %s", got)
	}
}

// The reserved options are refused, as is a session without Perms: the
// event stream still ends with the error status.
func TestReservedOptionsRefused(t *testing.T) {
	for name, o := range map[string]ClientOptions{"IDPrefix": {IDPrefix: "x"}, "Attach": {Attach: &SessionState{}}} {
		c := NewWith(o)
		err := c.Start(context.Background(), Config{Perms: NewPermissions(), Spawn: func(context.Context, Config) (*Process, error) {
			t.Fatal("spawned")
			return nil, nil
		}})
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("%s: %v", name, err)
		}
		if es := collect(t, c, func(Event) bool { return false }); data(es[len(es)-1])["status"] != StatusError {
			t.Fatalf("%s: %s", name, types(es))
		}
	}
	if err := New().Start(context.Background(), Config{Spawn: func(context.Context, Config) (*Process, error) { return nil, nil }}); err == nil {
		t.Fatal("no Perms accepted")
	}
}

// Wire never reaches the JSON form of an event.
func TestWireNotSerialized(t *testing.T) {
	e := NewEvent(EvStatus, map[string]string{"status": "idle"})
	e.Wire = &Wire{Off: 7, RPCID: json.RawMessage(`3`), Replay: true}
	b, _ := json.Marshal(e)
	if string(b) != `{"seq":0,"ts":0,"type":"status","data":{"status":"idle"}}` {
		t.Fatalf("%s", b)
	}
}
