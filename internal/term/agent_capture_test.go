package term

// The native client's agent fixtures (native/ios/Packages/XbinAgent): real
// event logs of the scripted agent (hack/fakeacp) driven through a real
// session — the same numbering, delta coalescing, permission bookkeeping and
// snapshot diffs the API serves. Skipped unless XBIN_AGENT_CAPTURE names the
// directory to write them to:
//
//	XBIN_AGENT_CAPTURE=$PWD/native/ios/Packages/XbinAgent/Tests/XbinAgentTests/Fixtures \
//	  go test ./internal/term -run TestAgentCaptureFixtures -count=1
//
// Each file is {name, about, session, providers?, snapshot?, events, history?}:
// session the SessionInfo the create returned, snapshot a GET
// /term/sessions/<id> body taken while a request waited, events the whole
// log (GET …/events?since=0), history the persisted transcript
// (GET /agent/history/<id>/events) after the session ended.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

type captureRun struct {
	t   *testing.T
	r   *agentRig
	id  string
	out map[string]any
	ctx context.Context
}

func (c *captureRun) idle() {
	c.t.Helper()
	c.r.until(c.t, func(e SessionEvent) bool {
		return e.ID == c.id && e.Type == agent.EvStatus && (edata(e.Event)["status"] == agent.StatusIdle || edata(e.Event)["status"] == agent.StatusError)
	})
}

func (c *captureRun) prompt(text string) {
	c.t.Helper()
	if _, err := c.r.m.AgentPrompt(c.ctx, c.id, text); err != nil {
		c.t.Fatalf("prompt %q: %v", text, err)
	}
}

func (c *captureRun) waitFor(typ string) map[string]any {
	c.t.Helper()
	return edata(c.r.until(c.t, func(e SessionEvent) bool { return e.ID == c.id && e.Type == typ }).Event)
}

func (c *captureRun) snapshot() {
	info, _ := c.r.m.Info(c.id)
	pend, _ := c.r.m.AgentPending(c.id)
	c.out["snapshot"] = map[string]any{"session": info, "permissions": pend}
}

// finish ends the session and records the live log and the persisted one.
func (c *captureRun) finish(dir string) {
	c.t.Helper()
	evs, _, _, err := c.r.m.AgentEvents(c.id, 0)
	if err != nil {
		c.t.Fatal(err)
	}
	c.out["events"] = evs
	c.r.m.Kill(c.id)
	waitClose(c.t, c.r.change, "close:"+c.id)
	if hs := c.r.m.ListHistory("owner", "apps/x", nil); len(hs) > 0 {
		meta, hev, err := c.r.m.ReadHistory("owner", hs[0].ID)
		if err == nil {
			c.out["history"] = map[string]any{"meta": meta, "events": hev}
			c.out["historyList"] = hs
		}
	}
	b, _ := json.MarshalIndent(c.out, "", " ")
	if err := os.WriteFile(filepath.Join(dir, c.out["name"].(string)+".json"), append(b, '\n'), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

func startCapture(t *testing.T, name, about string, git bool) *captureRun {
	r := newAgentRig(t)
	if git {
		tile := filepath.Join(r.root, "apps", "x")
		if err := os.WriteFile(filepath.Join(tile, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRepo(t, tile)
	}
	info, code, err := r.m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	c := &captureRun{t: t, r: r, id: info.ID, ctx: context.Background(),
		out: map[string]any{"name": name, "about": about, "session": info}}
	c.idle()
	return c
}

func TestAgentCaptureFixtures(t *testing.T) {
	dir := os.Getenv("XBIN_AGENT_CAPTURE")
	if dir == "" {
		t.Skip("set XBIN_AGENT_CAPTURE=<dir> to write the native client's agent fixtures")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("basic", func(t *testing.T) {
		c := startCapture(t, "basic", "handshake; an echo turn (the agent titles the session); a thinking turn (six thought chunks 300 ms apart); a burst of one-character chunks the daemon coalesces", false)
		c.out["providers"] = agent.Providers()
		c.prompt("hello there")
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.prompt("think about it")
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.prompt("burst")
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.finish(dir)
	})

	t.Run("permissions", func(t *testing.T) {
		c := startCapture(t, "permissions", "a permission answered allow once; one rejected; one answered allow always (a session rule); the next one auto-allowed by that rule; the slash commands and config options on idle", false)
		c.prompt("perm one")
		p := c.waitFor(agent.EvPermissionRequest)
		c.snapshot()
		if err := c.r.m.AgentPermit(c.id, p["pid"].(string), "once", "", "user:alice"); err != nil {
			t.Fatal(err)
		}
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.prompt("perm two")
		p = c.waitFor(agent.EvPermissionRequest)
		if err := c.r.m.AgentPermit(c.id, p["pid"].(string), "no", "", "user:bob"); err != nil {
			t.Fatal(err)
		}
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.prompt("perm three")
		p = c.waitFor(agent.EvPermissionRequest)
		if err := c.r.m.AgentPermit(c.id, p["pid"].(string), "", agent.AllowAlways, "owner"); err != nil {
			t.Fatal(err)
		}
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.prompt("perm four")
		c.waitFor(agent.EvPermissionResolved) // the rule answers it
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.finish(dir)
	})

	t.Run("plan", func(t *testing.T) {
		c := startCapture(t, "plan", "Claude's plan approval (switch_mode, meta.title Ready to code?): approved with a mode option; then a plan kept (reject) — the turn ends cancelled", false)
		c.prompt("plan the work")
		p := c.waitFor(agent.EvPermissionRequest)
		c.snapshot()
		if err := c.r.m.AgentPermit(c.id, p["pid"].(string), "auto", "", "user:alice"); err != nil {
			t.Fatal(err)
		}
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.prompt("plan it again")
		p = c.waitFor(agent.EvPermissionRequest)
		if err := c.r.m.AgentPermit(c.id, p["pid"].(string), "reject", "", "user:bob"); err != nil {
			t.Fatal(err)
		}
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.finish(dir)
	})

	t.Run("ask", func(t *testing.T) {
		c := startCapture(t, "ask", "Claude's AskUserQuestion as an elicitation (single + multi choice, each with its Other box): answered; then skipped (decline)", false)
		c.prompt("ask me")
		q := c.waitFor(agent.EvElicitRequest)
		if err := c.r.m.AgentElicit(c.id, q["eid"].(string), "accept", json.RawMessage(`{"question_0":"SQLite","question_1":["Metrics"],"question_1_custom":"Admin UI"}`), "user:alice"); err != nil {
			t.Fatal(err)
		}
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.prompt("ask again")
		q = c.waitFor(agent.EvElicitRequest)
		if err := c.r.m.AgentElicit(c.id, q["eid"].(string), "decline", nil, "owner"); err != nil {
			t.Fatal(err)
		}
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.finish(dir)
	})

	t.Run("subagent", func(t *testing.T) {
		c := startCapture(t, "subagent", "Claude's Task call: the subagent's thought, a Read call and its text nested by parent; the Task completes with its answer", false)
		c.prompt("subagent explore")
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.finish(dir)
	})

	t.Run("shell", func(t *testing.T) {
		c := startCapture(t, "shell", "shell calls in a git tile: coloured output and a non-zero exit; a write that the snapshotter reports per call and per turn (files.changed)", true)
		c.prompt(`run: printf '\033[1;31merror\033[0m: \033[32mok\033[0m\n'; echo line2; exit 3`)
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.prompt("run: echo hi > made.txt && sed -i 's/main() {}/main() { println(1) }/' main.go")
		c.waitFor(agent.EvTurnEnd)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) { // the turn's snapshot lands after turn.end
			evs, _, _, _ := c.r.m.AgentEvents(c.id, 0)
			n := 0
			for _, e := range evs {
				if e.Type == EvFilesChanged {
					n++
				}
			}
			if n >= 3 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		c.idle()
		c.finish(dir)
	})

	t.Run("cancel", func(t *testing.T) {
		c := startCapture(t, "cancel", "a slow turn cancelled mid-stream (stopReason cancelled); a pending permission cancelled by a cancel", false)
		c.prompt("slow")
		c.r.until(t, func(e SessionEvent) bool {
			return e.ID == c.id && e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "agent"
		})
		if err := c.r.m.AgentCancel(c.id); err != nil {
			t.Fatal(err)
		}
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.prompt("perm then cancel")
		c.waitFor(agent.EvPermissionRequest)
		if err := c.r.m.AgentCancel(c.id); err != nil {
			t.Fatal(err)
		}
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.finish(dir)
	})

	t.Run("attach", func(t *testing.T) {
		c := startCapture(t, "attach", "prompts with files: an image and a text file with text (the image inline, the text as embedded context); files alone (empty text); then text alone", false)
		png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
		send := func(p agent.Prompt) {
			t.Helper()
			atts, err := agent.PrepareAttachments(p.Attachments)
			if err != nil {
				t.Fatal(err)
			}
			p.Attachments = atts
			if _, err := c.r.m.AgentPromptWith(c.ctx, c.id, p); err != nil {
				t.Fatalf("prompt %q: %v", p.Text, err)
			}
			c.waitFor(agent.EvTurnEnd)
			c.idle()
		}
		send(agent.Prompt{Text: "what is in these?", Attachments: []agent.Attachment{
			{Name: "shot.png", Mime: "image/png", Data: png},
			{Name: "notes.txt", Data: []byte("hello notes\n")},
		}})
		send(agent.Prompt{Attachments: []agent.Attachment{{Name: "Photo-1.jpg", Mime: "image/jpeg", Data: append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 40)...)}}})
		c.prompt("and now just text")
		c.waitFor(agent.EvTurnEnd)
		c.idle()
		c.finish(dir)
	})

	t.Run("signedout", func(t *testing.T) {
		c := startCapture(t, "signedout", "a signed-out agent: _auth/status_update{kind:none} then the prompt fails with auth required — turn.end error, status error with login", false)
		c.prompt("fail")
		c.waitFor(agent.EvTurnEnd)
		c.r.until(t, func(e SessionEvent) bool {
			return e.ID == c.id && e.Type == agent.EvStatus && edata(e.Event)["login"] != nil
		})
		time.Sleep(100 * time.Millisecond)
		c.finish(dir)
	})
}
