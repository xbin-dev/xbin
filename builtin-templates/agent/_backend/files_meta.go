// files_meta.go — what the session files know about themselves (D136): each
// version's content hash, where it came from (a tool call, a sandbox file and
// its etag, a person's upload, a browser_check screenshot) and the version it
// replaced; the earlier versions themselves (repl_file_versions), so an
// overwrite never loses what file_diff needs; and putFileData, the one write
// that replaces a file in place whatever its kind — sandbox_download and the
// sandbox paths the file tools accept (files_paths.go) go through it.
//
// Everything is additive: four columns on repl_files and a new table. A row
// an older backend wrote (before them, or during a blue/green overlap, when it
// bumps the version without them) carries a meta_ver that isn't its version:
// its hash and source are unknown, never wrong.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxFileVersions  = 10       // earlier versions kept per file
	maxHistoryText   = 1 << 20  // earlier text versions' bytes, per run
	maxHistoryBinary = 32 << 20 // earlier binary versions' bytes, per run (their blob objects)
)

func (d *DB) addFileMetaSchema() error {
	for _, q := range []string{
		`ALTER TABLE repl_files ADD COLUMN sha256 TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE repl_files ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE repl_files ADD COLUMN parent INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE repl_files ADD COLUMN meta_ver INTEGER NOT NULL DEFAULT 0`,
	} {
		_, _ = d.q.Exec(q) // fails harmlessly when the column is there
	}
	_, err := d.q.Exec(`CREATE TABLE IF NOT EXISTS repl_file_versions (
  run_id INTEGER NOT NULL,
  path TEXT NOT NULL,
  version INTEGER NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  bytes INTEGER NOT NULL DEFAULT 0,
  mime TEXT NOT NULL DEFAULT '',
  blob TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  parent INTEGER NOT NULL DEFAULT 0,
  updated INTEGER NOT NULL,
  PRIMARY KEY (run_id, path, version)
)`)
	return err
}

// fileSource is where a version's content came from.
type fileSource struct {
	Kind    string `json:"kind"`              // tool | sandbox | browser | upload
	Tool    string `json:"tool,omitempty"`    // the tool that wrote it
	Call    string `json:"call,omitempty"`    // its call id
	Sandbox string `json:"sandbox,omitempty"` // the sandbox's ref (sandbox, browser)
	Box     string `json:"box,omitempty"`     // its name, for people
	Path    string `json:"path,omitempty"`    // the file in it (sandbox)
	ETag    string `json:"etag,omitempty"`    // its etag when copied (sandbox)
	Target  string `json:"target,omitempty"`  // the page (browser)
	AtMs    int    `json:"atMs,omitempty"`    // the moment after its load (browser)
}

func (s *fileSource) encode() string {
	if s == nil {
		return ""
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func decodeSource(s string) *fileSource {
	if s == "" {
		return nil
	}
	var f fileSource
	if json.Unmarshal([]byte(s), &f) != nil || f.Kind == "" {
		return nil
	}
	return &f
}

// toolSource: a tool call of this turn wrote it.
func toolSource(ctx context.Context, tool string) *fileSource {
	return &fileSource{Kind: "tool", Tool: tool, Call: toolCallOf(ctx)}
}

// words says where a version came from, for the model and people.
func (s *fileSource) words() string {
	if s == nil {
		return ""
	}
	switch s.Kind {
	case "tool":
		return "written by " + orStr(s.Tool, "a tool")
	case "sandbox":
		w := fmt.Sprintf("copied from the sandbox %q: %s", orStr(s.Box, s.Sandbox), s.Path)
		if s.ETag != "" {
			w += " (etag " + clip(s.ETag, 16) + ")"
		}
		return w
	case "browser":
		return fmt.Sprintf("browser_check screenshot of %s, %d ms after its load", clip(s.Target, 80), s.AtMs)
	case "upload":
		return "uploaded by a person"
	}
	return s.Kind
}

// fileMetaSel and fileMetaCols read the columns; apply keeps them only when
// they describe the row's current version.
const fileMetaSel = `sha256, source, parent, meta_ver`

type fileMetaCols struct {
	sha, src    string
	parent, ver int
}

func (m fileMetaCols) apply(f *ReplFile) {
	if m.ver == 0 || m.ver != f.Version {
		return
	}
	f.SHA256, f.Source, f.Parent = m.sha, decodeSource(m.src), m.parent
}

func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// fileSHA is a file's content hash: recorded, else computed (a text file's
// from its content, a binary one's by reading its object).
func (ag *Agent) fileSHA(ctx context.Context, f *ReplFile) string {
	switch {
	case f.SHA256 != "":
		return f.SHA256
	case !f.Binary:
		return shaHex([]byte(f.Content))
	}
	if b, err := ag.readBlob(ctx, f.Blob); err == nil {
		return shaHex(b)
	}
	return ""
}

// --- earlier versions --------------------------------------------------------

// keepVersion files cur — the version an overwrite replaces — as an earlier
// version, and lets the oldest text versions go past the limits (binary ones
// hold blob objects: evictBinaryVersions, from putFileData, drops those).
func (d *DB) keepVersion(runID int64, cur *ReplFile) error {
	sum := cur.SHA256
	if sum == "" && !cur.Binary {
		sum = shaHex([]byte(cur.Content))
	}
	if _, err := d.q.Exec(`INSERT OR REPLACE INTO repl_file_versions
		(run_id, path, version, content, bytes, mime, blob, sha256, source, parent, updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, cur.Path, cur.Version, cur.Content, cur.Bytes, cur.Mime, cur.Blob, sum, cur.Source.encode(), cur.Parent, cur.Updated); err != nil {
		return err
	}
	_, _ = d.q.Exec(`DELETE FROM repl_file_versions WHERE run_id=? AND path=? AND blob='' AND version<=?`,
		runID, cur.Path, cur.Version-maxFileVersions)
	var total int
	_ = d.q.QueryRow(`SELECT coalesce(sum(bytes),0) FROM repl_file_versions WHERE run_id=? AND blob=''`, runID).Scan(&total)
	for i := 0; total > maxHistoryText && i < 1000; i++ {
		var p string
		var v, n int
		if d.q.QueryRow(`SELECT path, version, bytes FROM repl_file_versions WHERE run_id=? AND blob=''
			ORDER BY updated, version LIMIT 1`, runID).Scan(&p, &v, &n) != nil {
			break
		}
		_, _ = d.q.Exec(`DELETE FROM repl_file_versions WHERE run_id=? AND path=? AND version=?`, runID, p, v)
		total -= n
	}
	return nil
}

// evictBinaryVersions lets earlier binary versions go past the limits and
// returns their objects, to drop once the transaction commits.
func (d *DB) evictBinaryVersions(runID int64, path string, cur int) []string {
	var drop []string
	del := func(p string, v int, blob string) {
		if _, err := d.q.Exec(`DELETE FROM repl_file_versions WHERE run_id=? AND path=? AND version=?`, runID, p, v); err == nil {
			drop = append(drop, blob)
		}
	}
	type row struct {
		p    string
		v, n int
		blob string
	}
	scan := func(q string, args ...any) []row {
		rows, err := d.q.Query(q, args...)
		if err != nil {
			return nil
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if rows.Scan(&r.p, &r.v, &r.n, &r.blob) == nil {
				out = append(out, r)
			}
		}
		return out
	}
	for _, r := range scan(`SELECT path, version, bytes, blob FROM repl_file_versions WHERE run_id=? AND path=? AND blob<>'' AND version<=?`,
		runID, path, cur-maxFileVersions) {
		del(r.p, r.v, r.blob)
	}
	var total int
	_ = d.q.QueryRow(`SELECT coalesce(sum(bytes),0) FROM repl_file_versions WHERE run_id=? AND blob<>''`, runID).Scan(&total)
	if total > maxHistoryBinary {
		for _, r := range scan(`SELECT path, version, bytes, blob FROM repl_file_versions WHERE run_id=? AND blob<>'' ORDER BY updated, version`, runID) {
			if total <= maxHistoryBinary {
				break
			}
			del(r.p, r.v, r.blob)
			total -= r.n
		}
	}
	return drop
}

// dropVersions removes a file's earlier versions and returns their objects.
func (d *DB) dropVersions(runID int64, path string) []string {
	var blobs []string
	if rows, err := d.q.Query(`SELECT blob FROM repl_file_versions WHERE run_id=? AND path=? AND blob<>''`, runID, path); err == nil {
		for rows.Next() {
			var b string
			if rows.Scan(&b) == nil {
				blobs = append(blobs, b)
			}
		}
		rows.Close()
	}
	_, _ = d.q.Exec(`DELETE FROM repl_file_versions WHERE run_id=? AND path=?`, runID, path)
	return blobs
}

// fileVersions lists a file's earlier versions, newest first, without their
// content.
func (d *DB) fileVersions(runID int64, path string) []*ReplFile {
	rows, err := d.q.Query(`SELECT version, bytes, mime, blob, sha256, source, parent, updated FROM repl_file_versions
		WHERE run_id=? AND path=? ORDER BY version DESC`, runID, path)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*ReplFile
	for rows.Next() {
		f := &ReplFile{Path: path}
		var src string
		if rows.Scan(&f.Version, &f.Bytes, &f.Mime, &f.Blob, &f.SHA256, &src, &f.Parent, &f.Updated) == nil {
			f.Binary, f.Source = f.Blob != "", decodeSource(src)
			out = append(out, f)
		}
	}
	return out
}

// fileVersion is one version of a file, content and all: the current one, or
// an earlier one still kept.
func (d *DB) fileVersion(runID int64, path string, v int) (*ReplFile, error) {
	cur, err := d.replFile(runID, path)
	if err != nil {
		return nil, err
	}
	if v <= 0 || v == cur.Version {
		return cur, nil
	}
	f := &ReplFile{Path: path, Version: v}
	var src string
	err = d.q.QueryRow(`SELECT content, bytes, mime, blob, sha256, source, parent, updated FROM repl_file_versions
		WHERE run_id=? AND path=? AND version=?`, runID, path, v).Scan(&f.Content, &f.Bytes, &f.Mime, &f.Blob, &f.SHA256, &src, &f.Parent, &f.Updated)
	if err != nil {
		var have []string
		for _, e := range d.fileVersions(runID, path) {
			have = append(have, fmt.Sprintf("v%d", e.Version))
		}
		return nil, fmt.Errorf("%s has no v%d kept (it is at v%d; earlier ones kept: %s)", path, v, cur.Version, orStr(strings.Join(have, ", "), "none"))
	}
	f.Binary, f.Source = f.Blob != "", decodeSource(src)
	return f, nil
}

// --- writing in place ------------------------------------------------------------

// putFileData writes data at name, replacing whatever is there — text or
// binary — as its next version, the replaced one kept (keepVersion). The
// same content writes nothing (unchanged; a newer source is recorded — the
// sandbox file's etag, say). declared is the content type, when known.
// Where acceptUpload finds a free name for a person's upload, this is the
// agent keeping one file up to date.
func (ag *Agent) putFileData(ctx context.Context, runID int64, name string, data []byte, declared string, src *fileSource) (f, prev *ReplFile, unchanged bool, err error) {
	p, err := normReplPath(name)
	if err != nil {
		return nil, nil, false, err
	}
	if len(data) > maxBinaryFileBytes {
		return nil, nil, false, errTooLarge
	}
	sum := shaHex(data)
	cur, curErr := ag.db.replFile(runID, p)
	if curErr == nil && ag.fileSHA(ctx, cur) == sum {
		if src != nil { // where it is up to date with now
			_, _ = ag.db.q.Exec(`UPDATE repl_files SET sha256=?, source=?, meta_ver=version WHERE run_id=? AND path=? AND version=?`,
				sum, src.encode(), runID, p, cur.Version)
			cur.SHA256, cur.Source = sum, src
		}
		return cur, cur, true, nil
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	m := normalizeMime(declared, head)
	text := isTextMime(m) && utf8.Valid(data) && len(data) <= maxReplFileBytes
	if curErr == nil {
		prev = cur
	}
	if text && (curErr != nil || !cur.Binary) {
		if f, err = ag.db.replPutFileSrc(runID, p, string(data), 0, src); err != nil {
			return nil, nil, false, err
		}
		_, _ = ag.db.q.Exec(`UPDATE repl_files SET mime=? WHERE run_id=? AND path=?`, m, runID, p)
		f.Mime = m
		return f, prev, false, nil
	}
	blob := ""
	if !text {
		blob = newBlobPath(runID)
		if err := ag.blobs.Put(ctx, blob, data, m); err != nil {
			return nil, nil, false, fmt.Errorf("storing the file failed: %w", err)
		}
	}
	fail := func(err error) (*ReplFile, *ReplFile, bool, error) {
		if blob != "" {
			ag.dropBlobs([]string{blob})
		}
		return nil, nil, false, err
	}
	if curErr != nil { // a new binary file
		if f, err = ag.db.replPutBinarySrc(runID, p, m, len(data), blob, sum, src); err != nil {
			return fail(err)
		}
		ag.blobCache.put(blob, data)
		return f, nil, false, nil
	}
	// replacing a binary file, or a text file with a binary one
	content := ""
	if text {
		content = string(data)
	}
	var drop []string
	err = ag.db.Tx(func(t *DB) error {
		if blob != "" {
			var total int
			_ = t.q.QueryRow(`SELECT coalesce(sum(bytes),0) FROM repl_files WHERE run_id=? AND blob<>'' AND path<>?`, runID, p).Scan(&total)
			if total+len(data) > maxBinaryRunBytes {
				return fmt.Errorf("run attachment store full: %s + %s exceeds %s",
					humanBytes(total), humanBytes(len(data)), humanBytes(maxBinaryRunBytes))
			}
		}
		if err := t.keepVersion(runID, cur); err != nil {
			return err
		}
		ver := cur.Version + 1
		res, err := t.q.Exec(`UPDATE repl_files SET content=?, bytes=?, version=?, updated=?, mime=?, blob=?,
			sha256=?, source=?, parent=?, meta_ver=? WHERE run_id=? AND path=? AND version=?`,
			content, len(data), ver, now(), m, blob, sum, src.encode(), cur.Version, ver, runID, p, cur.Version)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%s changed while it was being replaced — try again", p)
		}
		drop = t.evictBinaryVersions(runID, p, ver)
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if len(drop) > 0 {
		ag.dropBlobs(drop)
	}
	if blob != "" {
		ag.blobCache.put(blob, data)
	}
	f, err = ag.db.replFile(runID, p)
	return f, prev, false, err
}

// nonEmpty drops the empty strings.
func nonEmpty(ss []string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// --- unified diffs -----------------------------------------------------------------

const (
	diffContext  = 3
	diffCells    = 1 << 20 // the LCS table's size, past the common head and tail
	maxDiffBytes = 12 << 10
)

type diffOp struct {
	kind byte // ' ', '-', '+'
	line string
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lineDiff is a line diff: the common head and tail, and the LCS of what
// lies between (or, past diffCells, all of it out and all of it in — still a
// correct diff, just not a minimal one).
func lineDiff(a, b []string) []diffOp {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	ops := make([]diffOp, 0, len(a)+len(b))
	for _, l := range a[:p] {
		ops = append(ops, diffOp{' ', l})
	}
	am, bm := a[p:len(a)-s], b[p:len(b)-s]
	n, m := len(am), len(bm)
	if n > 0 && m > 0 && n*m <= diffCells {
		dp := make([][]int32, n+1)
		for i := range dp {
			dp[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if am[i] == bm[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else {
					dp[i][j] = max(dp[i+1][j], dp[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n && j < m {
			switch {
			case am[i] == bm[j]:
				ops = append(ops, diffOp{' ', am[i]})
				i, j = i+1, j+1
			case dp[i+1][j] >= dp[i][j+1]:
				ops = append(ops, diffOp{'-', am[i]})
				i++
			default:
				ops = append(ops, diffOp{'+', bm[j]})
				j++
			}
		}
		am, bm = am[i:], bm[j:]
	}
	for _, l := range am {
		ops = append(ops, diffOp{'-', l})
	}
	for _, l := range bm {
		ops = append(ops, diffOp{'+', l})
	}
	for _, l := range a[len(a)-s:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops
}

// unifiedDiff renders a diff of two texts as `diff -u` does; changed says
// how many lines went out and came in.
func unifiedDiff(nameA, nameB, a, b string) (out string, removed, added int) {
	ops := lineDiff(splitLines(a), splitLines(b))
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", nameA, nameB)
	// line numbers before each op
	la, lb := make([]int, len(ops)+1), make([]int, len(ops)+1)
	for i, o := range ops {
		la[i+1], lb[i+1] = la[i], lb[i]
		if o.kind != '+' {
			la[i+1]++
		}
		if o.kind != '-' {
			lb[i+1]++
		}
		switch o.kind {
		case '-':
			removed++
		case '+':
			added++
		}
	}
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(0, i-diffContext)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end = min(len(ops), end+diffContext)
				break
			}
			end = run
		}
		na, nb := la[end]-la[start], lb[end]-lb[start]
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", hunkRange(la[start], na), hunkRange(lb[start], nb))
		for _, o := range ops[start:end] {
			sb.WriteByte(o.kind)
			sb.WriteString(o.line)
			sb.WriteByte('\n')
		}
		i = end
	}
	return sb.String(), removed, added
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if n == 1 {
		return fmt.Sprint(start + 1)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}
