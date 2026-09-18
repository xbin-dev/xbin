// files_store.go — the per-run session files: a small file store held in
// sqlite (schema in db.go), never on a host filesystem — a "path" is an opaque
// key. The model writes them with the file tools (files.go) and the tile's
// render pane shows them.
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
	maxReplFileBytes  = 64 << 10  // one file
	maxReplFiles      = 64        // files per run
	maxReplTotalBytes = 512 << 10 // all files in a run
)

type ReplFile struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Bytes   int    `json:"bytes"`
	Version int    `json:"version"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
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
	err := d.sql.QueryRow(
		`SELECT content, bytes, version, created, updated FROM repl_files WHERE run_id=? AND path=?`,
		runID, path).Scan(&f.Content, &f.Bytes, &f.Version, &f.Created, &f.Updated)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no such file %q", path)
	}
	return f, err
}

// replFiles lists a run's files without their contents — this feeds the tool
// result footer and the tile's 1.5s poll, so it must stay cheap.
func (d *DB) replFiles(runID int64) ([]*ReplFile, error) {
	rows, err := d.sql.Query(
		`SELECT path, bytes, version, created, updated FROM repl_files WHERE run_id=? ORDER BY path`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReplFile
	for rows.Next() {
		f := &ReplFile{}
		if err := rows.Scan(&f.Path, &f.Bytes, &f.Version, &f.Created, &f.Updated); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// replPutFile creates or overwrites a file and bumps its version. wantVersion
// > 0 makes the write conditional (optimistic concurrency for the human editor
// in the tile); 0 means "don't care", which is what the model's tools pass.
func (d *DB) replPutFile(runID int64, path, content string, wantVersion int) (*ReplFile, error) {
	path, err := normReplPath(path)
	if err != nil {
		return nil, err
	}
	if len(content) > maxReplFileBytes {
		return nil, fmt.Errorf("file too large: %d bytes (max %d) — split it or store less",
			len(content), maxReplFileBytes)
	}
	cur, curErr := d.replFile(runID, path)
	if wantVersion > 0 && (curErr != nil || cur.Version != wantVersion) {
		have := 0
		if curErr == nil {
			have = cur.Version
		}
		return nil, fmt.Errorf("version conflict: you edited v%d but the file is now v%d", wantVersion, have)
	}
	if curErr != nil { // new file — check the per-run ceilings
		var n, total int
		_ = d.sql.QueryRow(`SELECT count(*), coalesce(sum(bytes),0) FROM repl_files WHERE run_id=?`, runID).Scan(&n, &total)
		if n >= maxReplFiles {
			return nil, fmt.Errorf("too many files: %d (max %d) — delete some first", n, maxReplFiles)
		}
		if total+len(content) > maxReplTotalBytes {
			return nil, fmt.Errorf("run file store full: %d + %d bytes exceeds %d", total, len(content), maxReplTotalBytes)
		}
	}
	ts, ver := now(), 1
	created := ts
	if curErr == nil {
		ver, created = cur.Version+1, cur.Created
	}
	_, err = d.sql.Exec(
		`INSERT INTO repl_files (run_id, path, content, bytes, version, created, updated)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, path) DO UPDATE SET content=excluded.content, bytes=excluded.bytes,
		   version=excluded.version, updated=excluded.updated`,
		runID, path, content, len(content), ver, created, ts)
	if err != nil {
		return nil, err
	}
	return &ReplFile{Path: path, Content: content, Bytes: len(content), Version: ver, Created: created, Updated: ts}, nil
}

func (d *DB) replDeleteFile(runID int64, path string) error {
	res, err := d.sql.Exec(`DELETE FROM repl_files WHERE run_id=? AND path=?`, runID, path)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no such file %q", path)
	}
	return nil
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
			fmt.Fprintf(&b, "%s (%s)", f.Path, humanBytes(f.Bytes))
		}
		total += f.Bytes
	}
	if len(files) > 40 {
		fmt.Fprintf(&b, " · …and %d more", len(files)-40)
	}
	fmt.Fprintf(&b, " — %d file(s), %s", len(files), humanBytes(total))
	return b.String()
}

func (d *DB) replClearFiles(runID int64) error {
	_, err := d.sql.Exec(`DELETE FROM repl_files WHERE run_id=?`, runID)
	return err
}
