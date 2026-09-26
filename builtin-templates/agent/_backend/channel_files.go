// channel_files.go — files through chat channels (D86). In: the adapter
// uploads an attachment first (POST /adapter/files: staged, owned by its
// channel), then names it in the message; when the message finds its
// conversation the file becomes one of that run's session files, attached to
// the message — the model sees images and reads documents like anything the
// owner attaches from the page. Out: the model attaches session files to its
// reply (attach_to_reply); the turn's outbox row lists them and the adapter
// downloads each (GET /adapter/files/{row}/{i}) and posts it with the text.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const channelFilesSchemaSQL = `
CREATE TABLE IF NOT EXISTS channel_files (
  id TEXT PRIMARY KEY, channel_id INTEGER NOT NULL,
  name TEXT NOT NULL, mime TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0,
  content TEXT NOT NULL DEFAULT '', blob TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS idx_chfiles_created ON channel_files(created);
CREATE TABLE IF NOT EXISTS reply_files (
  run_id INTEGER NOT NULL, path TEXT NOT NULL, created INTEGER NOT NULL,
  PRIMARY KEY (run_id, path));
`

func (d *DB) addChannelFilesSchema() error {
	_, err := d.q.Exec(channelFilesSchemaSQL)
	return err
}

// stagedTTL is how long an uploaded file waits for its message.
const stagedTTL = 86400

// handleAdapterUpload stages one attachment for a message to come.
//
//	POST /adapter/files?channelId=&name=&mime=   body: the bytes (≤16 MiB) → {fileId}
func handleAdapterUpload(w http.ResponseWriter, r *http.Request) {
	chID, _ := strconv.ParseInt(r.URL.Query().Get("channelId"), 10, 64)
	ch, err := agent.db.getChannel(chID)
	if err != nil || ch.Adapter != adapterOf(r) {
		xbin.WriteError(w, 404, errNoChannel.Error())
		return
	}
	name := sanitizeUploadName(orStr(r.URL.Query().Get("name"), "file"))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBinaryFileBytes+1))
	if err != nil || len(data) > maxBinaryFileBytes {
		xbin.WriteError(w, http.StatusRequestEntityTooLarge, errTooLarge.Error())
		return
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	mime := normalizeMime(orStr(r.URL.Query().Get("mime"), r.Header.Get("Content-Type")), head)
	var b [9]byte
	_, _ = rand.Read(b[:])
	id := "f" + hex.EncodeToString(b[:])
	content, blob := "", ""
	if isTextMime(mime) && utf8.Valid(data) && len(data) <= maxReplFileBytes {
		content = string(data)
	} else {
		blob = "chanfiles/" + strconv.FormatInt(ch.ID, 10) + "/" + id
		if err := agent.blobs.Put(r.Context(), blob, data, mime); err != nil {
			xbin.WriteError(w, 502, "storing the file failed: "+err.Error())
			return
		}
	}
	var stale []string
	err = agent.db.Tx(func(t *DB) error {
		if _, err := t.q.Exec(`INSERT INTO channel_files (id, channel_id, name, mime, size, content, blob, created) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, ch.ID, name, mime, len(data), content, blob, now()); err != nil {
			return err
		}
		// files whose message never came
		rows, err := t.q.Query(`SELECT blob FROM channel_files WHERE created<? AND blob<>''`, now()-stagedTTL)
		if err == nil {
			for rows.Next() {
				var b string
				if rows.Scan(&b) == nil {
					stale = append(stale, b)
				}
			}
			rows.Close()
		}
		_, err = t.q.Exec(`DELETE FROM channel_files WHERE created<?`, now()-stagedTTL)
		return err
	})
	if err != nil {
		agent.dropBlobs([]string{blob})
		xbin.WriteError(w, 500, err.Error())
		return
	}
	agent.dropBlobs(stale)
	xbin.WriteJSON(w, 200, map[string]any{"fileId": id, "name": name, "mime": mime, "bytes": len(data)})
}

// adoptChannelFiles turns staged files into the run's session files (inside
// the delivering transaction) and returns their paths. A file that isn't this
// channel's, or is gone, is skipped: the message still goes.
func (d *DB) adoptChannelFiles(chID, runID int64, ids []string) ([]string, error) {
	var paths []string
	for _, id := range ids {
		var name, mime, content, blob string
		var size int
		if err := d.q.QueryRow(`SELECT name, mime, size, content, blob FROM channel_files WHERE id=? AND channel_id=?`, id, chID).
			Scan(&name, &mime, &size, &content, &blob); err != nil {
			continue
		}
		p := d.freePath(runID, name)
		var err error
		if blob == "" {
			var f *ReplFile
			if f, err = d.replPutFile(runID, p, content, 0); err == nil {
				_, _ = d.q.Exec(`UPDATE repl_files SET mime=? WHERE run_id=? AND path=?`, mime, runID, f.Path)
				p = f.Path
			}
		} else {
			var f *ReplFile
			if f, err = d.replPutBinary(runID, p, mime, size, blob); err == nil {
				p = f.Path
			}
		}
		if err != nil {
			return nil, err
		}
		// the object now belongs to the session file (the run's cleanup drops it)
		if _, err := d.q.Exec(`DELETE FROM channel_files WHERE id=?`, id); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// --- attaching files to a reply ----------------------------------------------

func attachReplySpec() toolSpec {
	return toolSpec{Type: "function", Function: funcDef{
		Name: "attach_to_reply",
		Description: "Send session files (images, documents — see file_list) with your reply in this chat: they are posted " +
			"together with your answer when this turn ends. Attach a file after you have written it.",
		Parameters: obj([]string{"paths"}, map[string]any{
			"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "session file paths"},
		}),
	}}
}

// toolAttachReply records files for the turn's reply.
func (ag *Agent) toolAttachReply(run *Run, args map[string]any) (string, error) {
	var paths []string
	switch v := args["paths"].(type) {
	case []any:
		for _, p := range v {
			if s, ok := p.(string); ok {
				paths = append(paths, s)
			}
		}
	case string:
		paths = []string{v}
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("attach_to_reply needs paths")
	}
	if len(paths) > 10 {
		return "", fmt.Errorf("at most 10 files per reply")
	}
	files, err := ag.checkAttachments(run.ID, paths)
	if err != nil {
		return "", err
	}
	var names []string
	for _, f := range files {
		if _, err := ag.db.q.Exec(`INSERT OR IGNORE INTO reply_files (run_id, path, created) VALUES (?, ?, ?)`, run.ID, f.Path, now()); err != nil {
			return "", err
		}
		names = append(names, f.Path)
	}
	return "will be sent with your reply: " + strings.Join(names, ", "), nil
}

// outFile is a file on an outbox row (the adapter downloads it by index).
type outFile struct {
	Name  string `json:"name"`
	Mime  string `json:"mime"`
	Bytes int    `json:"bytes"`
	Path  string `json:"path"`
}

// takeReplyFiles returns (and forgets) the files attached to a run's reply.
func (d *DB) takeReplyFiles(runID int64) []outFile {
	rows, err := d.q.Query(`SELECT path FROM reply_files WHERE run_id=? ORDER BY created, path`, runID)
	if err != nil {
		return nil
	}
	var paths []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			paths = append(paths, p)
		}
	}
	rows.Close()
	var out []outFile
	for _, p := range paths {
		if f, err := d.replFile(runID, p); err == nil {
			out = append(out, outFile{Name: f.Path, Mime: orStr(f.Mime, "text/plain"), Bytes: f.Bytes, Path: f.Path})
		}
	}
	_, _ = d.q.Exec(`DELETE FROM reply_files WHERE run_id=?`, runID)
	return out
}

// handleAdapterFile serves one file of an outbox row — only the adapter's
// own channels' rows.
//
//	GET /adapter/files/{oid}/{i}
func handleAdapterFile(w http.ResponseWriter, r *http.Request) {
	oid, _ := strconv.ParseInt(r.PathValue("oid"), 10, 64)
	i, _ := strconv.Atoi(r.PathValue("i"))
	rows := agent.db.outRows(`WHERE id=? AND channel_id IN (SELECT id FROM channels WHERE adapter=?)`, oid, adapterOf(r))
	if len(rows) != 1 || i < 0 || i >= len(rows[0].Body.Files) {
		xbin.WriteError(w, 404, "no such file")
		return
	}
	of := rows[0].Body.Files[i]
	f, err := agent.db.replFile(rows[0].RunID, of.Path)
	if err != nil {
		xbin.WriteError(w, 404, "the file is gone from its conversation")
		return
	}
	data := []byte(f.Content)
	if f.Binary {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if data, err = agent.readBlob(ctx, f.Blob); err != nil {
			xbin.WriteError(w, 502, err.Error())
			return
		}
	}
	w.Header().Set("Content-Type", orStr(f.Mime, "text/plain; charset=utf-8"))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", of.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
