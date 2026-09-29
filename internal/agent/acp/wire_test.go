package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
)

// The wire golden: every frame xbind's client writes to the agent (through
// the host) over a handshake with a mode and an option, a prompt with files,
// a permission answered, a question answered, a turn cancelled, and a
// resume — byte for byte what the client wrote before the ACP client moved
// to sdk/acp (captured on master 7b54e7fb). The move must not change one
// byte of what an adapter sees; so must nothing else, without updating this
// on purpose.

// recorder keeps each frame the client writes (Encode writes one per call).
type recorder struct {
	w      io.WriteCloser
	mu     sync.Mutex
	frames []string
}

func (r *recorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	r.frames = append(r.frames, strings.TrimSuffix(string(b), "\n"))
	r.mu.Unlock()
	return r.w.Write(b)
}

func (r *recorder) Close() error { return r.w.Close() }

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.frames...)
}

func recording(spawn agent.Spawner) (agent.Spawner, *recorder) {
	rec := &recorder{}
	return func(ctx context.Context, cfg agent.Config) (*agent.Process, error) {
		p, err := spawn(ctx, cfg)
		if err == nil {
			rec.w = p.Stdin
			p.Stdin = rec
		}
		return p, err
	}, rec
}

// wireScript: "perm" asks a permission, "ask" a question, anything else ends.
func wireScript(f *fakeAgent, text string) {
	switch text {
	case "perm":
		standard(f, text)
	case "ask":
		var res json.RawMessage
		_ = f.conn.Call(MElicitCreate, map[string]any{"mode": "form", "sessionId": f.sid, "toolCallId": "ask1", "message": "Pick one",
			"requestedSchema": map[string]any{"type": "object"}}, &res)
		f.end("end_turn")
	default:
		f.end("end_turn")
	}
}

func checkFrames(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s: the frames the agent sees changed\n got:\n%s\nwant:\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWireGolden(t *testing.T) {
	claude, _ := agent.Lookup("claude")
	f, spawn := newFake(wireScript)
	f.promptCaps = &PromptCapabilities{Image: true, EmbeddedContext: true}
	spawn, rec := recording(spawn)
	perms := agent.NewPermissions()
	c := New()
	cfg := agent.Config{Provider: agent.Provider{ID: "fake", Login: "fake-login", SessionMeta: claude.SessionMeta}, Mode: "yolo",
		Options: map[string]string{"model": "m-fast"}, Cwd: "/w/apps/x", Spawn: spawn, Perms: perms, Version: "test",
		Log: func(string) {}, Meta: map[string]string{"tile": "apps/x"}}
	if err := c.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, idle)
	atts, err := agent.PrepareAttachments([]agent.Attachment{{Name: "shot.png", Data: png}, {Name: "notes.txt", Mime: "text/plain", Data: []byte("milk\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Prompt(context.Background(), agent.Prompt{Text: "perm", Attachments: atts}); err != nil {
		t.Fatal(err)
	}
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvPermissionRequest })
	res, err := perms.Resolve("p1", "", agent.AllowOnce, "user:a")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.RespondPermission(res)
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	collect(t, c, idle)
	if err := c.Send(context.Background(), "ask"); err != nil {
		t.Fatal(err)
	}
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvElicitRequest })
	var eid string
	for _, e := range c.PendingElicitations() {
		eid = e.EID
	}
	if err := c.RespondElicitation(eid, "accept", json.RawMessage(`{"q":"A"}`), "user:a"); err != nil {
		t.Fatal(err)
	}
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	collect(t, c, idle)
	if err := c.Send(context.Background(), "perm"); err != nil {
		t.Fatal(err)
	}
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvPermissionRequest })
	if err := c.Cancel(); err != nil {
		t.Fatal(err)
	}
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	checkFrames(t, "session", rec.all(), wantSessionFrames)

	// resume: session/load instead of session/new
	_, spawn2 := newFake(wireScript)
	spawn2, rec2 := recording(spawn2)
	cfg.Spawn, cfg.ResumeID, cfg.Mode, cfg.Options = spawn2, "s-old", "", nil
	c2 := New()
	if err := c2.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	collect(t, c2, idle)
	checkFrames(t, "resume", rec2.all(), wantResumeFrames)
}

// The sign-in wording a status carries when the agent is signed out — the
// operator's next step, exactly as before.
func TestAuthDetailGolden(t *testing.T) {
	c, _, _, _ := rig(t, func(f *fakeAgent, text string) {
		f.mu.Lock()
		id := f.prompt
		f.prompt = nil
		f.mu.Unlock()
		_ = f.conn.Reply(id, nil, &Error{Code: ErrAuthRequired, Message: "Authentication required"})
	}, "")
	defer c.Close()
	collect(t, c, idle)
	_ = c.Send(context.Background(), "hi")
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusError })
	got := string(es[len(es)-1].Data)
	if got != wantAuthStatus {
		t.Errorf("the signed-out status changed\n got %s\nwant %s", got, wantAuthStatus)
	}
}

var wantSessionFrames = []string{
	`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true,"_meta":{"subagent-transcript":true,"terminal_output":true,"terminal_output_delta":true},"elicitation":{"form":{}}},"clientInfo":{"name":"xbin","version":"test"}}}`,
	`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/w/apps/x","mcpServers":[],"_meta":{"claudeCode":{"options":{"thinking":{"display":"summarized","type":"adaptive"}}}}}}`,
	`{"jsonrpc":"2.0","id":3,"method":"session/set_mode","params":{"sessionId":"s-1","modeId":"yolo"}}`,
	`{"jsonrpc":"2.0","id":4,"method":"session/set_config_option","params":{"sessionId":"s-1","configId":"model","value":"m-fast"}}`,
	`{"jsonrpc":"2.0","id":5,"method":"_xbin/attach","params":{"name":"shot.png","data":"iVBORw0KGgoAAAANSUhEUi1waXhlbHM="}}`,
	`{"jsonrpc":"2.0","id":6,"method":"_xbin/attach","params":{"name":"notes.txt","data":"bWlsawo="}}`,
	`{"jsonrpc":"2.0","id":7,"method":"session/prompt","params":{"sessionId":"s-1","prompt":[{"type":"text","text":"perm"},{"type":"image","data":"iVBORw0KGgoAAAANSUhEUi1waXhlbHM=","mimeType":"image/png"},{"type":"resource_link","mimeType":"image/png","uri":"file:///tmp/xbin-attachments-1/shot.png","name":"shot.png","size":23},{"type":"resource","resource":{"uri":"file:///tmp/xbin-attachments-1/notes.txt","mimeType":"text/plain","text":"milk\n"}}]}}`,
	`{"jsonrpc":"2.0","id":1,"result":{"outcome":{"outcome":"selected","optionId":"once"}}}`,
	`{"jsonrpc":"2.0","id":8,"method":"session/prompt","params":{"sessionId":"s-1","prompt":[{"type":"text","text":"ask"}]}}`,
	`{"jsonrpc":"2.0","id":2,"result":{"action":"accept","content":{"q":"A"}}}`,
	`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s-1","prompt":[{"type":"text","text":"perm"}]}}`,
	`{"jsonrpc":"2.0","id":3,"result":{"outcome":{"outcome":"cancelled"}}}`,
	`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s-1"}}`,
}

var wantResumeFrames = []string{
	`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true,"_meta":{"subagent-transcript":true,"terminal_output":true,"terminal_output_delta":true},"elicitation":{"form":{}}},"clientInfo":{"name":"xbin","version":"test"}}}`,
	`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"s-old","cwd":"/w/apps/x","mcpServers":[],"_meta":{"claudeCode":{"options":{"thinking":{"display":"summarized","type":"adaptive"}}}}}}`,
}

const wantAuthStatus = `{"currentMode":"ask","detail":"Authentication required: the agent isn't signed in — open a terminal on apps/x and run: fake-login  (your terminal $HOME is shared with agent sessions, so one login serves every tile)","login":{"command":"fake-login","needed":true,"provider":""},"status":"error"}`
