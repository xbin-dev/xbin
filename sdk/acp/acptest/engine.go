package acptest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// turnState is one running turn: a prompt's, or one a steer started.
type turnState struct {
	cancel  chan struct{}
	pending []string // steered in, not yet said
	steered []string // every steer the turn took
}

type fake struct {
	o      Options
	conn   *acp.Conn
	getenv func(string) string
	done   chan struct{} // closed when Serve returns: sleeping scripts wake
	crash  chan int      // a script's exit code

	mu        sync.Mutex
	mode      string
	model     string
	titled    bool
	cwd       string
	sid       string          // "fake-1"; --persist gives each session its own
	prompt    json.RawMessage // in-flight prompt id
	cur       *turnState      // the running turn
	signedIn  bool            // --require-login
	elicitURL bool            // the client takes URL elicitation (device code)
	termAuth  bool            // the client takes _meta terminal-auth
}

func newFake(o Options) *fake {
	f := &fake{o: o, getenv: o.Getenv, mode: "ask", model: "fake-default", sid: "fake-1",
		done: make(chan struct{}), crash: make(chan int, 1)}
	if f.getenv == nil {
		f.getenv = os.Getenv
	}
	if o.RequireLogin {
		f.signedIn = f.credentials()
	}
	return f
}

func (f *fake) onRequest(m *acp.Message) (any, *acp.Error) {
	switch m.Method {
	case acp.MInitialize:
		f.clientCaps(m.Params)
		return f.initialize(), nil
	case acp.MAuthenticate:
		return f.authenticate(m)
	case acp.MSessionNew:
		var p acp.SessionNewParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.cwd = p.Cwd
		if f.o.Persist {
			f.sid = f.newSession()
		}
		sid := f.sid
		f.mu.Unlock()
		go func() { // the adapters advertise their slash commands just after session/new
			f.sleep(50 * time.Millisecond)
			f.update(map[string]any{"sessionUpdate": acp.UpAvailableCmds, "availableCommands": []map[string]any{
				{"name": "review", "description": "Review the pending changes", "input": map[string]string{"hint": "what to focus on"}},
				{"name": "compact", "description": "Summarize the conversation to free context"},
				{"name": "init", "description": "Write a CLAUDE.md for this project"}}})
		}()
		return acp.SessionNewResult{SessionID: sid, Modes: f.modes(), ConfigOptions: f.configOptions()}, nil
	case acp.MSessionLoad:
		// resume: the prior turns stream back as session/update BEFORE the
		// answer — a user line and the agent's echo of it, tagged with the id
		var p acp.SessionLoadParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.cwd = p.Cwd
		f.mu.Unlock()
		if f.o.Persist {
			if err := f.replay(p.SessionID); err != nil {
				return nil, &acp.Error{Code: acp.CodeResourceNotFound, Message: err.Error()}
			}
		} else {
			f.update(map[string]any{"sessionUpdate": acp.UpUserChunk, "content": acp.ContentBlock{Type: "text", Text: "resumed " + p.SessionID}})
			f.update(map[string]any{"sessionUpdate": acp.UpAgentChunk, "content": acp.ContentBlock{Type: "text", Text: "echo: resumed " + p.SessionID}, "messageId": "m0"})
		}
		return acp.SessionLoadResult{Modes: f.modes(), ConfigOptions: f.configOptions()}, nil
	case acp.MSessionSetConfig:
		var p acp.SetConfigParams
		_ = json.Unmarshal(m.Params, &p)
		if p.ConfigID != "model" || (p.Value != "fake-default" && p.Value != "fake-fast") {
			return nil, &acp.Error{Code: acp.CodeInvalidParams, Message: "unknown option or value"}
		}
		f.mu.Lock()
		f.model = p.Value
		f.mu.Unlock()
		opts := f.configOptions()
		f.update(map[string]any{"sessionUpdate": acp.UpConfigOption, "configOptions": opts})
		return acp.SetConfigResult{ConfigOptions: opts}, nil
	case acp.MSessionSetMode:
		var p acp.SetModeParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.mode = p.ModeID
		f.mu.Unlock()
		f.update(map[string]any{"sessionUpdate": acp.UpCurrentMode, "currentModeId": p.ModeID})
		return map[string]any{}, nil
	case acp.MSessionPrompt:
		var p acp.PromptParams
		_ = json.Unmarshal(m.Params, &p)
		if rerr := f.requireLogin(); rerr != nil {
			return nil, rerr
		}
		text, files := readPrompt(p.Prompt)
		f.record(map[string]any{"sessionUpdate": acp.UpUserChunk, "content": acp.ContentBlock{Type: "text", Text: text}})
		f.mu.Lock()
		f.prompt = m.ID
		t := &turnState{cancel: make(chan struct{})}
		f.cur = t
		f.mu.Unlock()
		go f.turn(t, text, files)
		return nil, nil
	case mSteering:
		if f.o.Steer {
			return f.steer(m)
		}
	}
	return nil, &acp.Error{Code: acp.CodeMethodNotFound, Message: "method not found: " + m.Method}
}

// modes is the session's modes: ask, (auto,) yolo.
func (f *fake) modes() *acp.SessionModes {
	modes := []acp.ModeEntry{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo"}}
	if f.o.AutoMode {
		modes = []acp.ModeEntry{modes[0], {ID: "auto", Name: "Auto", Description: "Edits without asking; asks for the rest"}, modes[1]}
	}
	return &acp.SessionModes{CurrentModeID: "ask", AvailableModes: modes}
}

// configOptions is the one advertised setting, with its current value.
func (f *fake) configOptions() []acp.ConfigOption {
	f.mu.Lock()
	cur := f.model
	f.mu.Unlock()
	return []acp.ConfigOption{{ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: cur,
		Options: []acp.ConfigValue{{Value: "fake-default", Name: "Fake (default)"}, {Value: "fake-fast", Name: "Fake fast"}}}}
}

func (f *fake) onNotify(m *acp.Message) {
	if m.Method == acp.MSessionCancel {
		f.mu.Lock()
		var c chan struct{}
		if f.cur != nil {
			c = f.cur.cancel
		}
		f.mu.Unlock()
		if c != nil {
			select {
			case <-c:
			default:
				close(c)
			}
		}
		f.end("cancelled")
	}
}

// update sends a session/update (and, with --persist, records it).
func (f *fake) update(v any) {
	f.mu.Lock()
	sid := f.sid
	f.mu.Unlock()
	if !f.o.Persist {
		_ = f.conn.Notify(acp.MSessionUpdate, map[string]any{"sessionId": sid, "update": v})
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	f.append(sid, raw)
	_ = f.conn.Notify(acp.MSessionUpdate, map[string]any{"sessionId": sid, "update": json.RawMessage(raw)})
}

// say is one agent message chunk; steers the running turn took come first.
func (f *fake) say(text string) {
	f.flushSteers()
	f.chunk(text)
}

func (f *fake) chunk(text string) {
	f.update(map[string]any{"sessionUpdate": acp.UpAgentChunk, "content": acp.ContentBlock{Type: "text", Text: text}, "messageId": "m"})
}

func (f *fake) end(reason string) {
	f.mu.Lock()
	id := f.prompt
	f.prompt = nil
	f.cur = nil
	f.mu.Unlock()
	if id != nil {
		_ = f.conn.Reply(id, acp.PromptResult{StopReason: reason}, nil)
	}
}

// sleep is a script's pause; it ends early when Serve returns.
func (f *fake) sleep(d time.Duration) {
	if f.o.wait != nil {
		f.o.wait(d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-f.done:
	}
}

// exit ends the agent with code, as a process exiting mid-turn would.
func (f *fake) exit(code int) {
	select {
	case f.crash <- code:
	default:
	}
}

// cancelled reports whether THIS turn was cancelled (c is the turn's own
// channel: a cancelled turn still sleeping must not see the next turn's).
func cancelled(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// readPrompt is a prompt's text (its text blocks) and a description of
// every other block — what the default script echoes: [image <type>
// <bytes>B], [resource <name> <bytes>B] for embedded text, [file <name>
// <bytes>B] for a resource_link, read from the path it names (the file the
// host dropped in the sandbox: the size proves it arrived).
func readPrompt(blocks []acp.ContentBlock) (string, []string) {
	var text []string
	var files []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "image":
			raw, err := base64.StdEncoding.DecodeString(b.Data)
			if err != nil {
				files = append(files, "[image "+b.MimeType+" undecodable]")
				continue
			}
			files = append(files, fmt.Sprintf("[image %s %dB]", b.MimeType, len(raw)))
		case "resource":
			if b.Resource != nil {
				files = append(files, fmt.Sprintf("[resource %s %dB]", path.Base(b.Resource.URI), len(b.Resource.Text)))
			}
		case "resource_link":
			u, err := url.Parse(b.URI)
			if err != nil || u.Scheme != "file" {
				files = append(files, "[link "+b.URI+"]")
				continue
			}
			data, err := os.ReadFile(u.Path)
			if err != nil {
				files = append(files, "[file "+b.Name+" unreadable]")
				continue
			}
			files = append(files, fmt.Sprintf("[file %s %dB]", b.Name, len(data)))
		default:
			files = append(files, "["+b.Type+"]")
		}
	}
	return strings.Join(text, "\n"), files
}

func strp(s string) *string { return &s }
