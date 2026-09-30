// homes_bundle.go — the conversation bundle: one conversation as a copy between
// its two homes carries it (homes.go: POST /runs/{id}/publish, POST /copy,
// GET /runs/{id}/export, POST /import).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const bundleVersion = 1

// A copy's limits (vars: tests lower them).
var (
	maxBundleBytes = 48 << 20 // a bundle as JSON — a request body — at most (binary files travel base64)
	maxBundleFiles = 16 << 20 // the session files a copy carries, together (text and binary)
)

// convBundle is one conversation as a copy carries it: its root's
// transcript as the model reads it (every message, with its tool calls and
// results, folded and masked ones marked), its task ledger (each request on
// the message it arrived as), and — when asked — its session files.
// Subagents' own transcripts, memory, grants and sandboxes stay behind.
type convBundle struct {
	Version  int          `json:"version"`
	Title    string       `json:"title"`
	Class    string       `json:"class,omitempty"`
	Model    string       `json:"model,omitempty"` // the conversation's pick
	Summary  string       `json:"summary,omitempty"`
	Owner    string       `json:"owner,omitempty"`
	Messages []bundleMsg  `json:"messages"`
	Files    []bundleFile `json:"files,omitempty"`
	Left     []string     `json:"left,omitempty"` // files not carried (over the size cap)
}

type bundleMsg struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	ToolCalls  string     `json:"toolCalls,omitempty"`
	Compacted  bool       `json:"compacted,omitempty"`
	Masked     bool       `json:"masked,omitempty"` // a tool result the model sees as a stub (D133)
	Created    int64      `json:"created"`
	Sender     string     `json:"sender,omitempty"`
	Origin     string     `json:"origin,omitempty"` // what delivered a user message when no person typed it (msgMeta)
	Label      string     `json:"label,omitempty"`
	Model      string     `json:"model,omitempty"`
	Ask        *bundleAsk `json:"ask,omitempty"`   // the request this message was, in the task ledger
	Files      []string   `json:"files,omitempty"` // the session files it carried
}

// bundleAsk is a message's row in the task ledger (D133, asks.go).
type bundleAsk struct {
	Source string `json:"source"`
	Who    string `json:"who,omitempty"`
}

type bundleFile struct {
	Path    string `json:"path"`
	Mime    string `json:"mime,omitempty"`
	Content string `json:"content,omitempty"` // a text file
	Data    []byte `json:"data,omitempty"`    // a binary one (base64 on the wire)
	Binary  bool   `json:"binary,omitempty"`
}

// exportConv reads conversation root into a bundle.
func (ag *Agent) exportConv(ctx context.Context, root int64, withFiles bool) (*convBundle, error) {
	run, err := ag.db.getRun(root)
	if err != nil {
		return nil, err
	}
	cfg, _ := ag.db.runConfig(root)
	b := &convBundle{Version: bundleVersion, Title: run.Title, Class: cfg.Class, Model: cfg.Pick, Summary: run.Summary, Owner: run.Owner}
	msgs, err := ag.db.messages(root, false)
	if err != nil {
		return nil, err
	}
	carried := ag.db.messageFiles(root)
	asks, err := ag.db.asks(root)
	if err != nil {
		return nil, err
	}
	askOf := map[int64]*Ask{}
	for _, a := range asks {
		askOf[a.MsgID] = a
	}
	for _, m := range msgs {
		if m.Role == "system" {
			continue // the new home's own system prompt is written at import
		}
		bm := bundleMsg{Role: m.Role, Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID, ToolCalls: m.ToolCalls,
			Compacted: m.Compacted, Masked: m.Masked, Created: m.Created}
		var meta msgMeta
		if len(m.Meta) > 0 && json.Unmarshal(m.Meta, &meta) == nil {
			bm.Sender, bm.Origin, bm.Label, bm.Model = meta.Sender, meta.Origin, meta.Label, meta.Model
		}
		if a := askOf[m.ID]; a != nil {
			bm.Ask = &bundleAsk{Source: a.Source, Who: a.Who}
		}
		if withFiles {
			bm.Files = carried[m.ID]
		}
		b.Messages = append(b.Messages, bm)
	}
	if !withFiles {
		return b, nil
	}
	files, err := ag.db.replFiles(root)
	if err != nil {
		return nil, err
	}
	fileBytes := 0 // text and binary alike: what a copy carries is capped as a whole
	for _, f := range files {
		if fileBytes+f.Bytes > maxBundleFiles {
			b.Left = append(b.Left, f.Path)
			continue
		}
		if !f.Binary {
			full, err := ag.db.replFile(root, f.Path)
			if err != nil {
				return nil, err
			}
			if fileBytes+len(full.Content) > maxBundleFiles {
				b.Left = append(b.Left, f.Path)
				continue
			}
			fileBytes += len(full.Content)
			b.Files = append(b.Files, bundleFile{Path: f.Path, Mime: f.Mime, Content: full.Content})
			continue
		}
		data, err := ag.readBlob(ctx, f.Blob)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.Path, err)
		}
		fileBytes += len(data)
		b.Files = append(b.Files, bundleFile{Path: f.Path, Mime: f.Mime, Data: data, Binary: true})
	}
	return b, nil
}

// tooLargeToCopy: a bundle (or an import body carrying one) over
// maxBundleBytes as JSON — 413, saying what to leave out.
type tooLargeToCopy struct {
	n     int  // its size; 0: over the limit, not measured (a request body)
	files bool // it carries session files
}

func (e tooLargeToCopy) Error() string {
	size := fmt.Sprintf("over %d MiB", maxBundleBytes>>20)
	if e.n > 0 {
		size = fmt.Sprintf("%.1f MiB, and a copy carries at most %d MiB", float64(e.n)/(1<<20), maxBundleBytes>>20)
	}
	msg := "this conversation is too large to copy: " + size
	if e.files {
		msg += " — leave its session files out"
	}
	return msg
}

// bundleJSON is v — a bundle, or an import body carrying one — as JSON, or
// tooLargeToCopy when POST /import couldn't take it.
func bundleJSON(v any, files bool) ([]byte, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(out) > maxBundleBytes {
		return nil, tooLargeToCopy{n: len(out), files: files}
	}
	return out, nil
}

// writeBundleErr answers a bundle's error: 413 when it is too large.
func writeBundleErr(w http.ResponseWriter, err error) {
	var tl tooLargeToCopy
	if errors.As(err, &tl) {
		xbin.WriteError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	xbin.WriteError(w, 500, err.Error())
}

// automationOrigins are the user-message origins (msgMeta.Origin) an
// automation delivers; copyOrigin marks a message a copy brought from
// elsewhere, which the page shows as "copied · <label>".
var automationOrigins = map[string]bool{"schedule": true, "watch": true, "learn": true, "trigger": true, "channel": true}

const copyOrigin = "copy"

// vouchedBy is a user message's meta and ledger row as a bundle that is
// c's own word (POST /import) may say them: only c is taken at their word.
// Anyone else's message — or one naming nobody — keeps the id it named as
// a copy's label (origin "copy": never a sender, so no page shows it as
// theirs and the model never reads it as a person speaking); an
// automation's prompt keeps its origin; a ledger row that isn't c's own
// request becomes a "copy" one.
func vouchedBy(c who, meta msgMeta, ask *bundleAsk) (msgMeta, *bundleAsk) {
	switch {
	case c.user != "" && meta.Sender == c.user:
		if !automationOrigins[meta.Origin] {
			meta.Origin, meta.Label = "", ""
		}
	case meta.Sender == "" && automationOrigins[meta.Origin]:
		meta.Label = clip(meta.Label, 80)
	default:
		name := meta.Sender
		if name == "" && meta.Origin == copyOrigin {
			name = meta.Label
		}
		if !userIDRe.MatchString(name) {
			name = ""
		}
		meta.Sender, meta.Origin, meta.Label = "", copyOrigin, name
	}
	meta.OriginID = 0
	if ask != nil && (ask.Source != "human" || c.user == "" || ask.Who != c.user) {
		said := ask.Who
		if ask.Source != "human" && ask.Source != copyOrigin {
			said = ask.Source + " " + said
		}
		ask = &bundleAsk{Source: copyOrigin, Who: flatClip(said, 60)}
	}
	return meta, ask
}

// importConv makes a new conversation of c's from a bundle, stamped st, in
// class cls (the class's settings are this home's), with a note saying where
// it came from. Its files first, then the transcript, then the rest in one
// transaction; a failure leaves nothing behind. callers: the bundle is c's
// own word (POST /import), so who said what is taken from it for c alone
// (vouchedBy); else this backend read it itself from the global instance
// (POST /copy), which vouches for its senders.
func (ag *Agent) importConv(ctx context.Context, b *convBundle, c who, st runStamp, cls agentClass, s *shareSpec, note string, callers bool) (*Run, error) {
	if b == nil || b.Version != bundleVersion {
		return nil, errBadRequest(fmt.Sprintf("conversation: a bundle of version %d (GET /runs/{id}/export)", bundleVersion))
	}
	if len(b.Messages) == 0 {
		return nil, errBadRequest("conversation: no messages to copy")
	}
	cfg := parseConfig(ag.db.getSetting("config"))
	cfg.setClass(cls, false)
	if validPick(b.Model) {
		cfg.Pick = b.Model
	}
	cfgJSON, _ := json.Marshal(cfg)
	title := strings.TrimSpace(b.Title)
	if title == "" {
		title = "a copy"
	}
	st.Origin, st.TitleSrc = "chat", "user"
	var id int64
	err := ag.db.Tx(func(t *DB) error {
		var err error
		if id, err = t.createRunStamped(clip(title, 120), string(cfgJSON), 0, statusIdle, st); err != nil {
			return err
		}
		t.setRunKind(id, "quick")
		if _, err := t.addMessage(&Message{RunID: id, Role: "system", Content: cfg.System}); err != nil {
			return err
		}
		return s.addMembers(t, id, c)
	})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Run, error) {
		_ = ag.deleteRunTree(id)
		return nil, err
	}
	paths := map[string]string{} // the bundle's path → where it landed
	for _, f := range b.Files {
		var got *ReplFile
		var err error
		if f.Binary {
			got, err = ag.acceptUploadSrc(ctx, id, f.Path, f.Mime, bytes.NewReader(f.Data), &fileSource{Kind: "copy"})
		} else {
			var p string
			if p, err = normReplPath(f.Path); err == nil {
				got, err = ag.db.replPutFileSrc(id, p, f.Content, 0, &fileSource{Kind: "copy"})
				if err == nil && f.Mime != "" {
					_, _ = ag.db.q.Exec(`UPDATE repl_files SET mime=? WHERE run_id=? AND path=?`, f.Mime, id, got.Path)
				}
			}
		}
		if err != nil {
			return fail(fmt.Errorf("copying %s: %w", f.Path, err))
		}
		paths[f.Path] = got.Path
	}
	err = ag.db.Tx(func(t *DB) error {
		for _, bm := range b.Messages {
			switch bm.Role {
			case "user", "assistant", "tool":
			default:
				return errBadRequest("conversation: a message's role is user, assistant or tool")
			}
			m := &Message{RunID: id, Role: bm.Role, Content: bm.Content, Name: bm.Name, ToolCallID: bm.ToolCallID,
				ToolCalls: bm.ToolCalls, Compacted: bm.Compacted}
			meta, ask := msgMeta{Model: bm.Model}, (*bundleAsk)(nil)
			if bm.Role == "user" {
				meta.Sender, meta.Origin, meta.Label, ask = bm.Sender, bm.Origin, bm.Label, bm.Ask
				if callers {
					meta, ask = vouchedBy(c, meta, ask)
				}
			}
			if meta.Sender != "" || meta.Origin != "" || meta.Model != "" {
				m.Meta, _ = json.Marshal(meta)
			}
			if _, err := t.addMessage(m); err != nil {
				return err
			}
			if bm.Masked && bm.Role == "tool" {
				if err := t.markMasked([]*Message{m}); err != nil {
					return err
				}
			}
			if ask != nil { // the task ledger (D133): the copy pins what the original did
				if err := t.recordAsk(m, ask.Source, ask.Who); err != nil {
					return err
				}
			}
			for _, p := range bm.Files {
				if at, ok := paths[p]; ok {
					if _, err := t.q.Exec(`INSERT OR IGNORE INTO message_files (msg_id, run_id, path) VALUES (?, ?, ?)`, m.ID, id, at); err != nil {
						return err
					}
				}
			}
		}
		if b.Summary != "" {
			if err := t.setSummary(id, b.Summary); err != nil {
				return err
			}
		}
		if note != "" {
			t.journal(id, "note", map[string]string{"text": note})
		}
		if ag.eng != nil {
			ag.eng.emitRun(t, id)
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if len(s.members()) > 0 {
		ag.membersChanged(id)
	}
	return ag.db.getRun(id)
}

func (s *shareSpec) members() []shareMember {
	if s == nil {
		return nil
	}
	return s.Members
}
