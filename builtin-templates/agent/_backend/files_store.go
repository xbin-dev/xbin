// files_store.go — the per-run session files: a small file store held in
// sqlite (schema in db.go), never on a host filesystem — a "path" is an opaque
// key. The model writes them with the file tools (files.go), the tile's render
// pane shows them, and the REPL (repl.go) reads and writes them — hence the
// table name, repl_files. Binary files (attachments) keep only metadata here;
// their bytes are in the blob store.
package main

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// Caps. Generous enough to hold a real script or a rendered page, small enough
// that a run's whole store stays far under the sqlite/context budgets.
const (
	maxReplFileBytes  = 64 << 10  // one text file
	maxReplFiles      = 64        // files per run, text and binary together
	maxReplTotalBytes = 512 << 10 // all text files in a run
	// Binary files (attachments) are bounded separately: their bytes live in
	// the blob resource rather than the run's database, and they are read by
	// people and by the vision path, not pasted into the transcript.
	maxBinaryFileBytes = 16 << 20
	maxBinaryRunBytes  = 64 << 20
	// maxRenderBytes bounds what render_html shows: a sandbox's report is
	// often over the text cap (then stored as a binary file, text/html),
	// and a preview pane paints a few MB of HTML without trouble.
	maxRenderBytes = 2 << 20
)

// renderable: a binary-stored file render_html shows and GET /runs/{id}/file
// answers with its content — HTML (or text) over the text cap, up to
// maxRenderBytes. Anything else binary stays bytes (GET …/raw).
func renderable(f *ReplFile) bool {
	return f.Binary && isTextMime(f.Mime) && f.Mime != "image/svg+xml" && f.Bytes <= maxRenderBytes
}

type ReplFile struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Bytes   int    `json:"bytes"`
	Version int    `json:"version"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
	// Mime is set for attachments; empty means a text file written by the model.
	Mime string `json:"mime,omitempty"`
	// Binary files keep their bytes in the blob resource at Blob, with an empty
	// Content. The path is internal, so it never goes on the wire.
	Binary bool   `json:"binary,omitempty"`
	Blob   string `json:"-"`
	// SHA256, Source and Parent (D136, files_meta.go) describe this version:
	// its content's hash, where it came from, and the version it replaced
	// (0: none). Rows written before them — or by an older backend during a
	// blue/green overlap — have none (meta_ver ≠ version): unknown, and a
	// text file's hash is computed when asked (fileSHA).
	SHA256 string      `json:"sha256,omitempty"`
	Source *fileSource `json:"source,omitempty"`
	Parent int         `json:"parent,omitempty"`
}

// replPathRE keeps session paths to a boring, unambiguous shape. These keys
// never reach a filesystem, so this is not a traversal guard — it is an
// ALIASING guard: without it "a/../b" and "b" would be two rows the model
// believes are one file.
var replPathRE = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]{0,127}$`)

func normReplPath(p string) (string, error) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "./")
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	if !replPathRE.MatchString(p) {
		return "", fmt.Errorf("bad path %q — letters, digits, . _ - / only, no leading slash, max 128 chars", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("bad path %q — no empty, '.' or '..' segments", p)
		}
	}
	return p, nil
}

// --- session files ------------------------------------------------------

func (d *DB) replFile(runID int64, path string) (*ReplFile, error) {
	f := &ReplFile{Path: path}
	var m fileMetaCols
	err := d.q.QueryRow(
		`SELECT content, bytes, version, created, updated, mime, blob, `+fileMetaSel+` FROM repl_files WHERE run_id=? AND path=?`,
		runID, path).Scan(&f.Content, &f.Bytes, &f.Version, &f.Created, &f.Updated, &f.Mime, &f.Blob, &m.sha, &m.src, &m.parent, &m.ver)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no such file %q", path)
	}
	f.Binary = f.Blob != ""
	m.apply(f)
	return f, err
}

// replFiles lists a run's files without their contents — this feeds the tool
// result footer and the tile's 1.5s poll, so it must stay cheap.
func (d *DB) replFiles(runID int64) ([]*ReplFile, error) {
	rows, err := d.q.Query(
		`SELECT path, bytes, version, created, updated, mime, blob, `+fileMetaSel+` FROM repl_files WHERE run_id=? ORDER BY path`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReplFile
	for rows.Next() {
		f := &ReplFile{}
		var m fileMetaCols
		if err := rows.Scan(&f.Path, &f.Bytes, &f.Version, &f.Created, &f.Updated, &f.Mime, &f.Blob, &m.sha, &m.src, &m.parent, &m.ver); err != nil {
			return nil, err
		}
		f.Binary = f.Blob != ""
		m.apply(f)
		out = append(out, f)
	}
	return out, rows.Err()
}

// replPutFile creates or overwrites a file and bumps its version. wantVersion
// > 0 makes the write conditional (optimistic concurrency for the human editor
// in the tile); 0 means "don't care", which is what the model's tools pass.
func (d *DB) replPutFile(runID int64, path, content string, wantVersion int) (*ReplFile, error) {
	return d.replPutFileSrc(runID, path, content, wantVersion, nil)
}

// replPutFileSrc is replPutFile recording where the content came from (nil:
// unknown). An overwrite keeps the version it replaces (keepVersion), so
// file_diff can show what changed.
func (d *DB) replPutFileSrc(runID int64, path, content string, wantVersion int, src *fileSource) (*ReplFile, error) {
	path, err := normReplPath(path)
	if err != nil {
		return nil, err
	}
	if len(content) > maxReplFileBytes {
		return nil, fmt.Errorf("file too large: %d bytes (max %d) — split it or store less",
			len(content), maxReplFileBytes)
	}
	cur, curErr := d.replFile(runID, path)
	if curErr == nil && cur.Binary {
		// Overwriting would orphan the blob and hand text readers a file whose
		// type just changed under them. Make the caller choose another name.
		return nil, fmt.Errorf("%s is a binary file (%s); choose another path, or delete it first", path, cur.Mime)
	}
	if wantVersion > 0 && (curErr != nil || cur.Version != wantVersion) {
		have := 0
		if curErr == nil {
			have = cur.Version
		}
		return nil, fmt.Errorf("version conflict: you edited v%d but the file is now v%d", wantVersion, have)
	}
	if curErr != nil { // new file — check the per-run ceilings
		var n, total int
		_ = d.q.QueryRow(`SELECT count(*), coalesce(sum(CASE WHEN blob='' THEN bytes ELSE 0 END),0) FROM repl_files WHERE run_id=?`, runID).Scan(&n, &total)
		if n >= maxReplFiles {
			return nil, fmt.Errorf("too many files: %d (max %d) — delete some first", n, maxReplFiles)
		}
		if total+len(content) > maxReplTotalBytes {
			return nil, fmt.Errorf("run file store full: %d + %d bytes exceeds %d", total, len(content), maxReplTotalBytes)
		}
	}
	ts, ver, parent := now(), 1, 0
	created := ts
	if curErr == nil {
		ver, created, parent = cur.Version+1, cur.Created, cur.Version
	}
	sum := shaHex([]byte(content))
	err = d.Tx(func(t *DB) error {
		if curErr == nil {
			if err := t.keepVersion(runID, cur); err != nil {
				return err
			}
		} else {
			// a new file has no history: rows an older backend's delete left
			// behind (blue/green) aren't this file's
			t.dropVersions(runID, path)
		}
		_, err := t.q.Exec(
			`INSERT INTO repl_files (run_id, path, content, bytes, version, created, updated, sha256, source, parent, meta_ver)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(run_id, path) DO UPDATE SET content=excluded.content, bytes=excluded.bytes,
			   version=excluded.version, updated=excluded.updated, sha256=excluded.sha256, source=excluded.source,
			   parent=excluded.parent, meta_ver=excluded.meta_ver`,
			runID, path, content, len(content), ver, created, ts, sum, src.encode(), parent, ver)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &ReplFile{Path: path, Content: content, Bytes: len(content), Version: ver, Created: created, Updated: ts,
		SHA256: sum, Source: src, Parent: parent}, nil
}

// replPutBinary records an attachment whose bytes are already in the blob
// store. Always a NEW path — the caller picks a free one — because objects are
// immutable and an overwrite would orphan the old one (putFileData replaces
// one in place, keeping the old object as the previous version).
func (d *DB) replPutBinary(runID int64, path, mime string, size int, blob string) (*ReplFile, error) {
	return d.replPutBinarySrc(runID, path, mime, size, blob, "", nil)
}

// replPutBinarySrc is replPutBinary with the content's hash and source.
func (d *DB) replPutBinarySrc(runID int64, path, mime string, size int, blob, sum string, src *fileSource) (*ReplFile, error) {
	path, err := normReplPath(path)
	if err != nil {
		return nil, err
	}
	if size > maxBinaryFileBytes {
		return nil, fmt.Errorf("file too large: %s (max %s)", humanBytes(size), humanBytes(maxBinaryFileBytes))
	}
	var n, total int
	_ = d.q.QueryRow(`SELECT count(*), coalesce(sum(CASE WHEN blob<>'' THEN bytes ELSE 0 END),0) FROM repl_files WHERE run_id=?`,
		runID).Scan(&n, &total)
	if n >= maxReplFiles {
		return nil, fmt.Errorf("too many files: %d (max %d) — delete some first", n, maxReplFiles)
	}
	if total+size > maxBinaryRunBytes {
		return nil, fmt.Errorf("run attachment store full: %s + %s exceeds %s",
			humanBytes(total), humanBytes(size), humanBytes(maxBinaryRunBytes))
	}
	ts := now()
	if _, err := d.replFile(runID, path); err != nil {
		d.dropVersions(runID, path) // a new file has no history (see replPutFileSrc)
	}
	if _, err := d.q.Exec(
		`INSERT INTO repl_files (run_id, path, content, bytes, version, created, updated, mime, blob, sha256, source, parent, meta_ver)
		 VALUES (?, ?, '', ?, 1, ?, ?, ?, ?, ?, ?, 0, 1)`, runID, path, size, ts, ts, mime, blob, sum, src.encode()); err != nil {
		return nil, err
	}
	return &ReplFile{Path: path, Bytes: size, Version: 1, Created: ts, Updated: ts,
		Mime: mime, Binary: true, Blob: blob, SHA256: sum, Source: src}, nil
}

// replDeleteFile removes a file and returns its blob path (empty for a text
// file) so the caller can drop the object after the row is gone. Its earlier
// versions go too; an object an earlier version held (a file that was once
// binary) is left — replDeleteFileBlobs returns those as well.
func (d *DB) replDeleteFile(runID int64, path string) (string, error) {
	blobs, err := d.replDeleteFileBlobs(runID, path)
	if err != nil || len(blobs) == 0 {
		return "", err
	}
	return blobs[0], nil
}

// replDeleteFileBlobs removes a file and its earlier versions, and returns
// every blob object they held (the current one first, "" for a text file).
func (d *DB) replDeleteFileBlobs(runID int64, path string) ([]string, error) {
	var blob string
	_ = d.q.QueryRow(`SELECT blob FROM repl_files WHERE run_id=? AND path=?`, runID, path).Scan(&blob)
	res, err := d.q.Exec(`DELETE FROM repl_files WHERE run_id=? AND path=?`, runID, path)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("no such file %q", path)
	}
	_, _ = d.q.Exec(`DELETE FROM message_files WHERE run_id=? AND path=?`, runID, path)
	return append([]string{blob}, d.dropVersions(runID, path)...), nil
}

// runBlobs lists the blob objects owned by these runs, so a delete can drop
// them once the rows are gone.
func (d *DB) runBlobs(ids []int64) []string {
	var out []string
	for _, id := range ids {
		rows, err := d.q.Query(`SELECT blob FROM repl_files WHERE run_id=?1 AND blob<>''
			UNION SELECT blob FROM repl_file_versions WHERE run_id=?1 AND blob<>''`, id)
		if err != nil {
			continue
		}
		for rows.Next() {
			var b string
			if rows.Scan(&b) == nil {
				out = append(out, b)
			}
		}
		rows.Close()
	}
	return out
}

// replFileIndex renders the one-line inventory appended to file tool results.
// It exists so the model keeps an accurate picture WITHOUT the system prompt
// carrying a file list — that would invalidate the cacheable prompt prefix
// (loop.go assembleContext) on every write, which is the commonest operation.
func (d *DB) replFileIndex(runID int64) string {
	files, err := d.replFiles(runID)
	if err != nil || len(files) == 0 {
		return "session files: (none)"
	}
	var b strings.Builder
	b.WriteString("session files: ")
	total := 0
	for i, f := range files {
		if i > 0 {
			b.WriteString(" · ")
		}
		if i < 40 {
			if f.Binary {
				fmt.Fprintf(&b, "%s (%s, %s)", f.Path, f.Mime, humanBytes(f.Bytes))
			} else {
				fmt.Fprintf(&b, "%s (%s)", f.Path, humanBytes(f.Bytes))
			}
		}
		total += f.Bytes
	}
	if len(files) > 40 {
		fmt.Fprintf(&b, " · …and %d more", len(files)-40)
	}
	fmt.Fprintf(&b, " — %d file(s), %s", len(files), humanBytes(total))
	return b.String()
}

// replClearFiles removes every file in a run and returns the blob objects to
// drop.
func (d *DB) replClearFiles(runID int64) ([]string, error) {
	blobs := d.runBlobs([]int64{runID})
	if _, err := d.q.Exec(`DELETE FROM repl_files WHERE run_id=?`, runID); err != nil {
		return nil, err
	}
	_, _ = d.q.Exec(`DELETE FROM repl_file_versions WHERE run_id=?`, runID)
	_, _ = d.q.Exec(`DELETE FROM message_files WHERE run_id=?`, runID)
	return blobs, nil
}
