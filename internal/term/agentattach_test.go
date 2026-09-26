package term

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// A prompt with files through the real host and the fake: the host drops
// each file in the sandbox, the fake sees the image inline, the small text
// embedded and the binary as a link it can read (its size proves the drop);
// the log's user message names the files without their bytes; a history
// preview of a files-only first prompt names them. Isolation off, the files
// sit in a directory the daemon made under its TMPDIR, and it is gone once
// the session is — although the host is SIGKILLed, not asked to clean up.
func TestAgentSessionAttachments(t *testing.T) {
	r := newAgentRig(t)
	info, code, err := r.m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	ended := false
	defer func() {
		if !ended {
			r.m.Kill(info.ID)
			waitClose(t, r.change, "close:"+info.ID)
		}
	}()
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-pixels")
	atts, err := agent.PrepareAttachments([]agent.Attachment{
		{Name: "shot.png", Mime: "image/png", Data: png},
		{Name: "notes.txt", Mime: "text/plain", Data: []byte("remember the milk\n")},
		{Name: "data.bin", Mime: "application/octet-stream", Data: []byte{0, 1, 2, 0xff}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.AgentPromptWith(context.Background(), info.ID, agent.Prompt{Attachments: atts}); err != nil {
		t.Fatal(err)
	}
	user := edata(r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "user"
	}).Event)
	if b, _ := json.Marshal(user["attachments"]); string(b) != `[{"inline":true,"mime":"image/png","name":"shot.png","size":23},{"inline":true,"mime":"text/plain","name":"notes.txt","size":18},{"mime":"application/octet-stream","name":"data.bin","size":4}]` {
		t.Fatalf("the user's message: %v", user)
	}
	said := r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvMessageDelta && strings.HasPrefix(fmt.Sprint(edata(e.Event)["text"]), "echo:")
	})
	if got := fmt.Sprint(edata(said.Event)["text"]); got != "echo:  [image image/png 23B] [file shot.png 23B] [resource notes.txt 18B] [file data.bin 4B]" {
		t.Fatalf("the agent got: %q", got)
	}
	r.until(t, ofType(agent.EvTurnEnd))
	evs, _, _, _ := r.m.AgentEvents(info.ID, 0)
	if p := firstPrompt(evs); p != "[shot.png, notes.txt, data.bin]" {
		t.Fatalf("history preview: %q", p)
	}
	dirs, _ := filepath.Glob(filepath.Join(r.tmp, "xbin-attachments-*"))
	if len(dirs) != 1 {
		t.Fatalf("attachment dirs under TMPDIR: %v", dirs)
	}
	if names, _ := os.ReadDir(dirs[0]); len(names) != 3 {
		t.Fatalf("dropped: %v", names)
	}
	r.m.Kill(info.ID)
	waitClose(t, r.change, "close:"+info.ID)
	ended = true
	if left, _ := filepath.Glob(filepath.Join(r.tmp, "xbin-attach*")); len(left) != 0 {
		t.Fatalf("the session left its attachments behind: %v", left)
	}
}

// ReservePrompt is the session's one prompt slot, taken before a prompt's
// body is read: a second reservation is busy until the first is released,
// and so is one while a turn runs; an unknown session is ErrNoSession.
func TestAgentReservePrompt(t *testing.T) {
	r := newAgentRig(t)
	info, code, err := r.m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	defer func() {
		r.m.Kill(info.ID)
		waitClose(t, r.change, "close:"+info.ID)
	}()
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if _, err := r.m.ReservePrompt("nope"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown session: %v", err)
	}
	release, err := r.m.ReservePrompt(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.ReservePrompt(info.ID); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("a second reservation: %v", err)
	}
	release()
	release, err = r.m.ReservePrompt(info.ID)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	if _, err := r.m.AgentPrompt(context.Background(), info.ID, "slow"); err != nil {
		t.Fatal(err)
	}
	release()
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusRunning
	})
	if _, err := r.m.ReservePrompt(info.ID); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("while a turn runs: %v", err)
	}
	r.until(t, ofType(agent.EvTurnEnd))
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if release, err := r.m.ReservePrompt(info.ID); err != nil {
		t.Fatalf("after the turn: %v", err)
	} else {
		release()
	}
}

type stubTokens struct{ n int }

func (t *stubTokens) MintTerminal(component, userID string) string {
	t.n++
	return fmt.Sprintf("tok-%s-%d", component, t.n)
}
func (t *stubTokens) RevokeTerminal(string) {}

// SelfToken recognises the session's own terminal token (its sandbox's
// XBIN_TOKEN) and nothing else: another session's, an empty one.
func TestAgentSelfToken(t *testing.T) {
	r := newAgentRig(t)
	r.m.Tokens = &stubTokens{}
	info, code, err := r.m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	defer func() {
		r.m.Kill(info.ID)
		waitClose(t, r.change, "close:"+info.ID)
	}()
	if !r.m.SelfToken(info.ID, "tok-apps/x-1") {
		t.Fatal("the session's own token")
	}
	for _, tok := range []string{"", "tok-apps/x-2", "tok-apps/x-1 "} {
		if r.m.SelfToken(info.ID, tok) {
			t.Fatalf("%q taken for the session's own", tok)
		}
	}
	if r.m.SelfToken("nope", "tok-apps/x-1") {
		t.Fatal("an unknown session")
	}
}
