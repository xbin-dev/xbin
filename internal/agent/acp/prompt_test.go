package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
)

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-pixels")

func echoEnd(f *fakeAgent, text string) { f.end("end_turn") }

// promptBlocks is the last session/prompt's content as generic JSON.
func (f *fakeAgent) promptBlocks() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	_ = json.Unmarshal(f.blocks, &out)
	return out
}

func idle(e agent.Event) bool {
	return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle
}

// A prompt with files, to an agent that takes images and embedded context:
// every file is handed to the host first; the image goes inline plus a link
// to its file, a small text file is embedded (uri = its file), a binary and
// a big text file are links; the transcript's user message names them all.
func TestPromptAttachments(t *testing.T) {
	c, f, _, _ := rigWith(t, echoEnd, "", func(f *fakeAgent) {
		f.promptCaps = &PromptCapabilities{Image: true, EmbeddedContext: true}
	})
	defer c.Close()
	collect(t, c, idle)
	bigText := bytes.Repeat([]byte("log line\n"), agent.MaxInlineText/9+1)
	atts, err := agent.PrepareAttachments([]agent.Attachment{
		{Name: "shot.png", Data: png},
		{Name: "notes.txt", Mime: "text/plain", Data: []byte("remember the milk\n")},
		{Name: "data.bin", Mime: "application/octet-stream", Data: []byte{0, 1, 2, 0xff}},
		{Name: "big.log", Mime: "text/plain", Data: bigText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Prompt(context.Background(), agent.Prompt{Text: "look", Attachments: atts}); err != nil {
		t.Fatal(err)
	}
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	var echo map[string]any
	for _, e := range es {
		if e.Type == agent.EvMessageDelta && data(e)["role"] == "user" {
			echo = data(e)
		}
	}
	names := []string{}
	for _, a := range echo["attachments"].([]any) {
		m := a.(map[string]any)
		names = append(names, m["name"].(string)+":"+m["mime"].(string))
		if m["size"] == nil || m["data"] != nil {
			t.Fatalf("the transcript keeps sizes, never bytes: %v", m)
		}
	}
	if echo["text"] != "look" || strings.Join(names, " ") != "shot.png:image/png notes.txt:text/plain data.bin:application/octet-stream big.log:text/plain" {
		t.Fatalf("the user's message: %v", echo)
	}
	f.mu.Lock()
	attached := strings.Join(f.attached, " ")
	f.mu.Unlock()
	if want := "shot.png:23 notes.txt:18 data.bin:4 big.log:" + strconv.Itoa(len(bigText)); attached != want {
		t.Fatalf("dropped %q, want %q", attached, want)
	}
	b := f.promptBlocks()
	kinds := []string{}
	for _, blk := range b {
		kinds = append(kinds, blk["type"].(string))
	}
	if strings.Join(kinds, " ") != "text image resource_link resource resource_link resource_link" {
		t.Fatalf("blocks: %v", kinds)
	}
	if b[0]["text"] != "look" {
		t.Fatalf("text block: %v", b[0])
	}
	if raw, _ := base64.StdEncoding.DecodeString(b[1]["data"].(string)); !bytes.Equal(raw, png) || b[1]["mimeType"] != "image/png" {
		t.Fatalf("image block: %v", b[1])
	}
	if b[2]["uri"] != "file:///tmp/xbin-attachments-1/shot.png" || b[2]["name"] != "shot.png" || b[2]["size"] != float64(len(png)) {
		t.Fatalf("the image's file: %v", b[2])
	}
	res := b[3]["resource"].(map[string]any)
	if res["uri"] != "file:///tmp/xbin-attachments-1/notes.txt" || res["text"] != "remember the milk\n" || res["mimeType"] != "text/plain" {
		t.Fatalf("embedded text: %v", b[3])
	}
	if b[4]["uri"] != "file:///tmp/xbin-attachments-1/data.bin" || b[4]["mimeType"] != "application/octet-stream" {
		t.Fatalf("binary link: %v", b[4])
	}
	if b[5]["name"] != "big.log" || b[5]["resource"] != nil {
		t.Fatalf("a big text file is a link: %v", b[5])
	}

	// a text-only prompt is the one text block it always was
	collect(t, c, idle)
	if err := c.Send(context.Background(), "just text"); err != nil {
		t.Fatal(err)
	}
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	f.mu.Lock()
	blocks := string(f.blocks)
	f.mu.Unlock()
	if blocks != `[{"type":"text","text":"just text"}]` {
		t.Fatalf("text-only prompt: %s", blocks)
	}
}

// An agent that advertises neither images nor embedded context: an image
// is refused before anything moves (no file dropped, not busy); a text file
// is a link. A host that cannot take the file fails the prompt, not busy.
func TestPromptAttachmentsDegrade(t *testing.T) {
	c, f, _, _ := rig(t, echoEnd, "")
	defer c.Close()
	collect(t, c, idle)
	img, _ := agent.PrepareAttachments([]agent.Attachment{{Name: "a.png", Data: png}})
	err := c.Prompt(context.Background(), agent.Prompt{Text: "see", Attachments: img})
	if !errors.Is(err, agent.ErrUnsupportedContent) || !strings.Contains(err.Error(), "images") {
		t.Fatalf("an image to a text-only agent: %v", err)
	}
	f.mu.Lock()
	n := len(f.attached)
	f.mu.Unlock()
	if n != 0 {
		t.Fatal("a refused prompt dropped files")
	}
	txt, _ := agent.PrepareAttachments([]agent.Attachment{{Name: "n.txt", Mime: "text/plain", Data: []byte("hi")}})
	if err := c.Prompt(context.Background(), agent.Prompt{Attachments: txt}); err != nil {
		t.Fatalf("the refusal left the session busy: %v", err)
	}
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	b := f.promptBlocks()
	if len(b) != 1 || b[0]["type"] != "resource_link" || b[0]["name"] != "n.txt" {
		t.Fatalf("a files-only prompt without embedded context: %v", b)
	}

	collect(t, c, idle)
	f.mu.Lock()
	f.attachErr = true
	f.mu.Unlock()
	if err := c.Prompt(context.Background(), agent.Prompt{Text: "x", Attachments: txt}); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("no host: %v", err)
	}
	if err := c.Send(context.Background(), "still usable"); err != nil {
		t.Fatalf("a failed hand-off left the session busy: %v", err)
	}
}
