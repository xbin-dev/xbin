// attach.go — files the owner attaches to a run, and images the model sees.
//
// Attachments land in the same virtual filesystem the model's own file tools
// use (repl_files), so the model finds them with file_list/file_read and the
// REPL with files.read. Text stays in sqlite; binary bytes go to the blob
// resource (blobstore.go).
//
// Images reach the model WITHOUT being stored in the transcript. They are
// added to the wire context when it is assembled: an image the owner attached
// rides the user message it came with, and an image the model asks for with
// file_view rides one message placed after that turn's whole tool-result
// block. Storing them would put megabytes into every 1.5s poll of the run (which
// returns all messages in full) and into search, token estimates and
// compaction; and tool results cannot carry images at all on an
// OpenAI-compatible wire.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// Only the most recent images in the context are sent; older ones become a
	// one-line note. Each image is bounded too. Together these keep a request
	// well under llm-gw's 32 MiB proxy cutoff, beyond which it truncates the
	// body silently, and under the per-prompt image limits some upstreams set.
	maxInlineImages     = 4
	maxInlineImageBytes = 3 << 20
)

// visionMimes are the formats inlined for the model. SVG is deliberately
// absent: it is text, stored as text, and readable with file_read.
var visionMimes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// --- accepting an upload ------------------------------------------------

var uploadBadChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sanitizeUploadName turns a browser file name into a session-file path the
// store accepts: no directories, no spaces, a sane length.
func sanitizeUploadName(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), `\`, "/"))
	name = uploadBadChars.ReplaceAllString(name, "-")
	name = strings.ReplaceAll(name, "-.", ".") // "shot (1).png" → "shot-1.png", not "shot-1-.png"
	name = strings.Trim(name, "-")
	name = strings.TrimLeft(name, ".") // no dotfiles, no "..": "...x" → "x"
	if len(name) > 100 {
		ext := path.Ext(name)
		if len(ext) > 12 {
			ext = ""
		}
		name = name[:100-len(ext)] + ext
	}
	if name == "" || name == "." {
		name = "upload"
	}
	return name
}

// freePath picks a path that is not taken, adding -2, -3… before the extension.
// Uploads never overwrite: blob objects are immutable, and the model may
// already have referred to the existing file.
func (d *DB) freePath(runID int64, name string) string {
	if _, err := d.replFile(runID, name); err != nil {
		return name
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		p := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := d.replFile(runID, p); err != nil {
			return p
		}
	}
	return fmt.Sprintf("%s-%s%s", stem, newBlobPath(runID)[len("runs/"):], ext)
}

// isTextMime says whether content of this type belongs in sqlite as text,
// where file_read, the REPL and the render pane can use it.
func isTextMime(m string) bool {
	if strings.HasPrefix(m, "text/") {
		return true
	}
	switch m {
	case "application/json", "application/xml", "application/javascript",
		"application/x-javascript", "application/ecmascript", "application/csv",
		"application/x-ndjson", "application/yaml", "application/x-yaml",
		"application/toml", "application/sql", "image/svg+xml":
		return true
	}
	return strings.HasSuffix(m, "+json") || strings.HasSuffix(m, "+xml")
}

// normalizeMime strips parameters and falls back to sniffing when the browser
// sent nothing useful.
func normalizeMime(declared string, head []byte) string {
	if m, _, err := mime.ParseMediaType(declared); err == nil && m != "" && m != "application/octet-stream" {
		return strings.ToLower(m)
	}
	m, _, _ := mime.ParseMediaType(http.DetectContentType(head))
	if m == "" {
		return "application/octet-stream"
	}
	return m
}

// acceptUpload stores one attached file. Text within the text cap is stored as
// text; everything else goes to the blob store with a metadata row.
func (ag *Agent) acceptUpload(ctx context.Context, runID int64, name, declared string, body io.Reader) (*ReplFile, error) {
	if _, err := ag.db.getRun(runID); err != nil {
		return nil, fmt.Errorf("no such run")
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBinaryFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBinaryFileBytes {
		return nil, errTooLarge
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	m := normalizeMime(declared, head)
	p := ag.db.freePath(runID, sanitizeUploadName(name))

	if isTextMime(m) && utf8.Valid(data) && len(data) <= maxReplFileBytes {
		f, err := ag.db.replPutFile(runID, p, string(data), 0)
		if err != nil {
			return nil, err
		}
		_, _ = ag.db.q.Exec(`UPDATE repl_files SET mime=? WHERE run_id=? AND path=?`, m, runID, f.Path)
		f.Mime = m
		return f, nil
	}

	blob := newBlobPath(runID)
	if err := ag.blobs.Put(ctx, blob, data, m); err != nil {
		return nil, fmt.Errorf("storing the file failed: %w", err)
	}
	f, err := ag.db.replPutBinary(runID, p, m, len(data), blob)
	if err != nil {
		ag.dropBlobs([]string{blob}) // the row was refused; don't leave the object
		return nil, err
	}
	ag.blobCache.put(blob, data)
	return f, nil
}

var errTooLarge = fmt.Errorf("file too large (max %s)", humanBytes(maxBinaryFileBytes))

// --- linking attachments to a message ------------------------------------

// checkAttachments validates paths before a message is written, so a bad
// request fails without leaving a half-written turn behind.
func (ag *Agent) checkAttachments(runID int64, paths []string) ([]*ReplFile, error) {
	return ag.checkAttachmentsTx(ag.db, runID, paths)
}

// checkAttachmentsTx is checkAttachments inside a transaction (the engine
// re-checks when it delivers a queued message: a file may have been deleted
// since it was queued).
func (ag *Agent) checkAttachmentsTx(d *DB, runID int64, paths []string) ([]*ReplFile, error) {
	var files []*ReplFile
	for _, p := range paths {
		np, err := normReplPath(p)
		if err != nil {
			return nil, err
		}
		f, err := d.replFile(runID, np)
		if err != nil {
			return nil, fmt.Errorf("attachment %q is not in this run's files", p)
		}
		files = append(files, f)
	}
	return files, nil
}

// linkMessageFiles records which (already checked) files a message carried.
func (ag *Agent) linkMessageFiles(runID, msgID int64, files []*ReplFile) error {
	return ag.linkMessageFilesTx(ag.db, runID, msgID, files)
}

func (ag *Agent) linkMessageFilesTx(d *DB, runID, msgID int64, files []*ReplFile) error {
	for _, f := range files {
		if _, err := d.q.Exec(
			`INSERT OR IGNORE INTO message_files (msg_id, run_id, path) VALUES (?, ?, ?)`,
			msgID, runID, f.Path); err != nil {
			return err
		}
	}
	return nil
}

// attachmentNote is appended to the stored user text, so the model knows what
// arrived even when it cannot see images, and the transcript reads correctly.
func attachmentNote(files []*ReplFile) string {
	if len(files) == 0 {
		return ""
	}
	parts := make([]string, len(files))
	for i, f := range files {
		m := f.Mime
		if m == "" {
			m = "text"
		}
		parts[i] = fmt.Sprintf("%s (%s, %s)", f.Path, m, humanBytes(f.Bytes))
	}
	return "\n\n[attached: " + strings.Join(parts, ", ") + "]"
}

// messageFiles maps each message in a run to the files it carried.
func (d *DB) messageFiles(runID int64) map[int64][]string {
	out := map[int64][]string{}
	rows, err := d.q.Query(`SELECT msg_id, path FROM message_files WHERE run_id=? ORDER BY path`, runID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p string
		if rows.Scan(&id, &p) == nil {
			out[id] = append(out[id], p)
		}
	}
	return out
}

// --- the model sees images ----------------------------------------------

const fileViewOK = "Showing "

func fileViewSpec() toolSpec {
	return toolSpec{Type: "function", Function: funcDef{
		Name: "file_view",
		Description: "Look at an image in this run's files — one the owner attached, or one you saved. " +
			"The image is attached right after this tool's result. Supports PNG, JPEG, GIF and WebP up to 3 MB. " +
			"Only the few most recent images stay visible; call this again to see an older one.",
		Parameters: obj([]string{"path"}, map[string]any{
			"path": strProp("file key of the image, e.g. 'photo.png'"),
		}),
	}}
}

// toolFileView validates the request. The image itself is added when the
// context is assembled (see withImages), because a tool result is text.
func (ag *Agent) toolFileView(run *Run, cfg Config, args map[string]any) (string, error) {
	if !cfg.feature("vision") {
		return "", fmt.Errorf("vision is turned off for this run, so images cannot be shown")
	}
	p, err := normReplPath(str(args["path"]))
	if err != nil {
		return "", err
	}
	f, err := ag.db.replFile(run.ID, p)
	if err != nil {
		return "", err
	}
	if !f.Binary || !visionMimes[f.Mime] {
		kind := f.Mime
		if kind == "" {
			kind = "text"
		}
		return "", fmt.Errorf("%s is %s, not an image this can show — use file_read for text", p, kind)
	}
	if f.Bytes > maxInlineImageBytes {
		return "", fmt.Errorf("%s is %s, over the %s limit for showing an image", p,
			humanBytes(f.Bytes), humanBytes(maxInlineImageBytes))
	}
	return fmt.Sprintf("%s%s (%s, %s) — the image is attached after these tool results.",
		fileViewOK, p, f.Mime, humanBytes(f.Bytes)), nil
}

// imageRef is one image the context would like to show.
type imageRef struct {
	path string
	at   int  // index in the wire list it belongs to (or follows)
	view bool // requested with file_view (placed after a tool block)
}

// withImages adds images to an assembled wire context. It never touches the
// stored transcript. Only the newest maxInlineImages are sent; the rest become
// a note, so a long run does not grow its prompt with every image it ever saw.
func (ag *Agent) withImages(ctx context.Context, run *Run, cfg Config, live []*Message, out []wireMsg, pos []int) []wireMsg {
	linked := ag.db.messageFiles(run.ID)
	var refs []imageRef
	for i, m := range live {
		if pos[i] < 0 {
			continue
		}
		switch m.Role {
		case "user":
			for _, p := range linked[m.ID] {
				if f, err := ag.db.replFile(run.ID, p); err == nil && f.Binary && visionMimes[f.Mime] {
					refs = append(refs, imageRef{path: p, at: pos[i]})
				}
			}
		case "assistant":
			if m.ToolCalls == "" {
				continue
			}
			var calls []toolCall
			if json.Unmarshal([]byte(m.ToolCalls), &calls) != nil {
				continue
			}
			// The block ends at the last tool row answering this turn.
			end := pos[i]
			ok := map[string]bool{}
			for j := i + 1; j < len(live) && live[j].Role == "tool"; j++ {
				if pos[j] >= 0 {
					end = pos[j]
				}
				if strings.HasPrefix(live[j].Content, fileViewOK) {
					ok[live[j].ToolCallID] = true
				}
			}
			for _, c := range calls {
				if c.Function.Name != "file_view" || !ok[c.ID] {
					continue
				}
				if p, err := normReplPath(str(decodeArgs(c.Function.Arguments)["path"])); err == nil {
					refs = append(refs, imageRef{path: p, at: end, view: true})
				}
			}
		}
	}
	if len(refs) == 0 {
		return out
	}

	// The newest images get the slots; with vision off, none do.
	inline := map[int]bool{}
	if cfg.feature("vision") {
		for k := len(refs) - 1; k >= 0 && len(inline) < maxInlineImages; k-- {
			inline[k] = true
		}
	}

	// Build the parts for each wire position.
	userParts := map[int][]json.RawMessage{}
	viewParts := map[int][]json.RawMessage{}
	for k, r := range refs {
		var part json.RawMessage
		if inline[k] {
			part = ag.imagePart(ctx, run.ID, r.path)
		} else {
			why := "file_view to see it again"
			if !cfg.feature("vision") {
				why = "vision is off"
			}
			part = textPart(fmt.Sprintf("[image %s not shown — %s]", r.path, why))
		}
		if r.view {
			viewParts[r.at] = append(viewParts[r.at], part)
		} else {
			userParts[r.at] = append(userParts[r.at], part)
		}
	}

	const lead = "(images you asked to see with file_view — attached by your tools, not the owner)"
	var result []wireMsg
	var carry []json.RawMessage // file_view images riding the owner's next message
	for i, wm := range out {
		if ps, ok := userParts[i]; ok || carry != nil {
			// Chronological: what the tools showed, then the owner's words,
			// then what the owner attached.
			wm.Content = partsOf(carry, asText(wm.Content), ps)
			carry = nil
		}
		result = append(result, wm)
		if ps, ok := viewParts[i]; ok {
			withLead := append([]json.RawMessage{textPart(lead)}, ps...)
			// If the owner's next message follows immediately, ride on it
			// rather than sending two user turns in a row, which some
			// upstreams reject.
			if i+1 < len(out) && out[i+1].Role == "user" {
				carry = withLead
				continue
			}
			result = append(result, wireMsg{Role: "user", Content: partsOf(nil, "", withLead)})
		}
	}
	return result
}

// imagePart loads one image as an OpenAI-style image_url part, or a note when
// it cannot be loaded.
func (ag *Agent) imagePart(ctx context.Context, runID int64, p string) json.RawMessage {
	f, err := ag.db.replFile(runID, p)
	if err != nil {
		return textPart(fmt.Sprintf("[image %s is gone]", p))
	}
	if f.Bytes > maxInlineImageBytes {
		return textPart(fmt.Sprintf("[image %s not shown — %s is over the %s limit]",
			p, humanBytes(f.Bytes), humanBytes(maxInlineImageBytes)))
	}
	data, err := ag.readBlob(ctx, f.Blob)
	if err != nil {
		return textPart(fmt.Sprintf("[image %s could not be loaded: %v]", p, err))
	}
	uri := "data:" + f.Mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	b, _ := json.Marshal(map[string]any{"type": "image_url", "image_url": map[string]string{"url": uri}})
	return b
}

func textPart(s string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"type": "text", "text": s})
	return b
}

// partsOf builds a multimodal content array: the parts before, the text, the
// parts after. It is a json.RawMessage, which is what hasVisionContent looks
// for when deciding to route the call to a vision model.
func partsOf(before []json.RawMessage, text string, after []json.RawMessage) json.RawMessage {
	all := make([]json.RawMessage, 0, len(before)+len(after)+1)
	all = append(all, before...)
	if strings.TrimSpace(text) != "" {
		all = append(all, textPart(text))
	}
	all = append(all, after...)
	b, _ := json.Marshal(all)
	return b
}

// asText recovers the text of a wire message's content, whether it is a plain
// string or an existing parts array.
func asText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case json.RawMessage:
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(v, &parts) == nil {
			var b strings.Builder
			for _, p := range parts {
				if p.Type == "text" {
					b.WriteString(p.Text)
				}
			}
			return b.String()
		}
		return string(v)
	}
	return fmt.Sprint(c)
}
