package term

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// A prompt with files through the real host and the fake: the host drops
// each file in the sandbox, the fake sees the image inline, the small text
// embedded and the binary as a link it can read (its size proves the drop);
// the log's user message names the files without their bytes; a history
// preview of a files-only first prompt names them.
func TestAgentSessionAttachments(t *testing.T) {
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
	if b, _ := json.Marshal(user["attachments"]); string(b) != `[{"mime":"image/png","name":"shot.png","size":23},{"mime":"text/plain","name":"notes.txt","size":18},{"mime":"application/octet-stream","name":"data.bin","size":4}]` {
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
}
