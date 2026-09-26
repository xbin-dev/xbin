// console.go — the built-in "console" platform: no network, no credentials.
// The tile's page plays the chat platform — you type as a pretend person, in
// a DM or a channel, in a thread or not, mentioning the bot or not, with
// files — and the agent's replies appear in the transcript. It lets people try
// the whole flow (claiming, pairing and linking, sessions, attachments)
// before the bridge is customised, and it is the smallest complete example
// of a Platform.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	registerPlatform("console", func(b Bridge) Platform {
		c := &console{b: b, files: map[string]OutFile{}}
		theConsole.Store(c)
		return c
	})
}

// theConsole is the running console (the page's routes reach it).
var theConsole atomicConsole

type atomicConsole struct {
	mu sync.Mutex
	c  *console
}

func (a *atomicConsole) Store(c *console) { a.mu.Lock(); a.c = c; a.mu.Unlock() }
func (a *atomicConsole) Load() *console   { a.mu.Lock(); defer a.mu.Unlock(); return a.c }

type console struct {
	b     Bridge
	mu    sync.Mutex
	seq   int
	lines []consoleLine
	files map[string]OutFile
	order []string // file ids, oldest first (the last 20 are kept)
}

// consoleLine is one message in the transcript, either way.
type consoleLine struct {
	At        int64         `json:"at"`
	Dir       string        `json:"dir"` // in | out | typing
	Conv      Conversation  `json:"conversation"`
	Thread    string        `json:"thread,omitempty"`
	MessageID string        `json:"messageId,omitempty"`
	From      string        `json:"from,omitempty"`
	Kind      string        `json:"kind,omitempty"`
	Text      string        `json:"text"`
	Files     []consoleFile `json:"files,omitempty"`
}

type consoleFile struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int    `json:"size"`
}

func (c *console) Info() Info {
	return Info{Name: "console", Title: "Console",
		Setup: []string{"The console needs no platform: type as a pretend person below, and the agent answers here."}}
}

func (c *console) Start(ctx context.Context, b Bridge) error {
	if err := b.Account(ctx, Account{ID: "console", Name: "try-out", BotID: "bot", BotName: "agent", Features: []string{"threads", "files"}}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (c *console) Send(ctx context.Context, account string, to Address, msg Outgoing) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	ref := "b" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "-" + strconv.Itoa(c.seq)
	line := consoleLine{At: time.Now().Unix(), Dir: "out", Conv: Conversation{ID: to.Conversation, Type: to.Type},
		Thread: to.Thread, MessageID: ref, From: "agent", Kind: msg.Kind, Text: msg.Text}
	for _, f := range msg.Files {
		c.seq++
		id := "f" + strconv.Itoa(c.seq)
		c.files[id] = f
		c.order = append(c.order, id)
		if len(c.order) > 20 {
			delete(c.files, c.order[0])
			c.order = c.order[1:]
		}
		line.Files = append(line.Files, consoleFile{ID: id, Name: f.Name, Mime: f.Mime, Size: len(f.Data)})
	}
	c.add(line)
	return ref, nil
}

func (c *console) Typing(ctx context.Context, account string, to Address, on bool) error {
	if on {
		c.mu.Lock()
		c.add(consoleLine{At: time.Now().Unix(), Dir: "typing", Conv: Conversation{ID: to.Conversation, Type: to.Type}, Thread: to.Thread, Text: "…"})
		c.mu.Unlock()
	}
	return nil
}

func (c *console) Format(markdown string) string { return markdown } // the page shows Markdown as is
func (c *console) Limit() int                    { return 4000 }

func (c *console) Fetch(ctx context.Context, account string, f FileRef) (io.ReadCloser, error) {
	return nil, errors.New("the console sends files inline")
}

// add appends a line (c.mu held); the last 200 are kept.
func (c *console) add(l consoleLine) {
	c.lines = append(c.lines, l)
	if len(c.lines) > 200 {
		c.lines = c.lines[len(c.lines)-200:]
	}
}

// --- the page's side -----------------------------------------------------------------

func consoleRoutes(t *Tile, mux *http.ServeMux) {
	running := func(w http.ResponseWriter) *console {
		c := theConsole.Load()
		if c == nil || t.platform() != Platform(c) {
			xbin.WriteError(w, 409, "the console isn't the running platform")
			return nil
		}
		return c
	}
	mux.Handle("POST /console/send", xbin.RoleFunc("admin", func(w http.ResponseWriter, r *http.Request) {
		if !mayWrite(w, r) {
			return
		}
		c := running(w)
		if c == nil {
			return
		}
		var b struct {
			As           Person       `json:"as"`
			Conversation Conversation `json:"conversation"`
			Thread       string       `json:"thread"`
			Mentioned    bool         `json:"mentioned"`
			Text         string       `json:"text"`
			Command      string       `json:"command"`
			Files        []struct {
				Name string `json:"name"`
				Mime string `json:"mime"`
				Data string `json:"data"` // base64
			} `json:"files"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 24<<20)).Decode(&b); err != nil || b.As.ID == "" || b.Conversation.ID == "" {
			xbin.WriteError(w, 400, "need {as: {id, name}, conversation: {id, type, name}, text, thread?, mentioned?, files?}")
			return
		}
		b.Conversation.Type = orStr(b.Conversation.Type, "dm")
		// ids unique across restarts: the agent dedupes event ids for a week,
		// and a thread root must never be reused
		c.mu.Lock()
		c.seq++
		id := "m" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "-" + strconv.Itoa(c.seq)
		c.mu.Unlock()
		e := Event{Account: "console", ID: "console-" + id, Conversation: b.Conversation, Thread: b.Thread, MessageID: id,
			Sender: b.As, Mentioned: b.Mentioned || b.Conversation.Type == "dm", Text: b.Text, Command: b.Command}
		line := consoleLine{At: time.Now().Unix(), Dir: "in", Conv: b.Conversation, Thread: b.Thread, MessageID: id, From: orStr(b.As.Name, b.As.ID), Text: b.Text}
		for _, f := range b.Files {
			data, err := base64.StdEncoding.DecodeString(f.Data)
			if err != nil {
				xbin.WriteError(w, 400, "files[].data is base64")
				return
			}
			e.Files = append(e.Files, FileRef{Name: f.Name, Mime: f.Mime, Size: int64(len(data)), Data: data})
			line.Files = append(line.Files, consoleFile{Name: f.Name, Mime: f.Mime, Size: len(data)})
		}
		if err := t.spool(e); err != nil {
			xbin.WriteError(w, 500, err.Error())
			return
		}
		c.mu.Lock()
		c.add(line)
		c.mu.Unlock()
		xbin.WriteJSON(w, 200, map[string]string{"messageId": id})
	}))
	mux.Handle("GET /console/transcript", xbin.RoleFunc("admin", func(w http.ResponseWriter, r *http.Request) {
		c := running(w)
		if c == nil {
			return
		}
		c.mu.Lock()
		lines := append([]consoleLine{}, c.lines...)
		c.mu.Unlock()
		xbin.WriteJSON(w, 200, map[string]any{"lines": lines})
	}))
	mux.Handle("GET /console/files/{id}", xbin.RoleFunc("admin", func(w http.ResponseWriter, r *http.Request) {
		c := running(w)
		if c == nil {
			return
		}
		c.mu.Lock()
		f, ok := c.files[r.PathValue("id")]
		c.mu.Unlock()
		if !ok {
			xbin.WriteError(w, 404, "no such file (the console keeps the last 20)")
			return
		}
		w.Header().Set("Content-Type", orStr(f.Mime, "application/octet-stream"))
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", f.Name))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(f.Data)
	}))
}
