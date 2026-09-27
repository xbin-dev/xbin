package tilesbx

// api_files.go — files, tar and copy (§3.8): the routes over files.go.
// Every path is resolved inside the sandbox by its agent, never on the
// host. Bodies stream both ways; a file over limits.fileMax, or a tar over
// limits.tarMax, is 413 too-large. A response whose status is out when the
// stream fails part-way (a tar that grows past tarMax, a read error) is
// cut, never ended cleanly.

import (
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

const (
	fileBodyMax = 16 << 10 // a mkdir, remove, move or copy body
	listMax     = 100000   // a listing's limit is clamped to this
	etagMax     = 128      // an ifMatch
)

// ServeStat answers GET /sandboxes/{name}/files/stat?path=.
func (m *Manager) ServeStat(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	p, err := sandboxPath("path", r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := m.oneLine(r.Context(), k, d.Name, proto.FileOp{Op: "stat", Path: p})
	if err == nil && res.Stat == nil {
		err = errNoStat
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statOf(p, res.Stat))
}

var errNoStat = refuse(RefUnavailable, "the sandbox's agent answered without a stat")

// ServeReadFile answers GET /sandboxes/{name}/files/content?path=&offset=&length=:
// the bytes, with the stat's etag as the ETag (quoted). A range over
// limits.fileMax is 413 too-large.
func (m *Manager) ServeReadFile(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	p, err := sandboxPath("path", q.Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	off, err := byteCount(q.Get("offset"), "offset")
	if err != nil {
		writeErr(w, err)
		return
	}
	length, err := byteCount(q.Get("length"), "length")
	if err != nil {
		writeErr(w, err)
		return
	}
	fc, err := m.begin(r.Context(), k, d.Name, proto.FileOp{Op: "read", Path: p, Offset: off, Length: length, Max: fileMax})
	if err != nil {
		writeErr(w, err)
		return
	}
	defer fc.end()
	first, err := fc.result()
	if err == nil && first.Stat == nil {
		err = errNoStat
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	n := max(first.Stat.Size-off, 0)
	if length > 0 {
		n = min(n, length)
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(n, 10))
	h.Set("Cache-Control", "no-store")
	if e := safeETag(first.Stat.ETag); e != "" {
		h.Set("ETag", `"`+e+`"`)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return // (the agent's writes end when the connection closes)
	}
	if got, err := fc.pipeOut(w); err != nil || got != n {
		abort()
	}
}

// ServeWriteFile answers PUT /sandboxes/{name}/files/content?path=&mode=&mkdirs=1&ifMatch=&ifNoneMatch=*:
// the raw body replaces the file atomically (nothing is committed unless
// all of it arrived) → its stat. ifMatch takes a stat's etag or a read's
// ETag header; a mismatch is 412 precondition with the current etag.
// ifNoneMatch=* creates only.
func (m *Manager) ServeWriteFile(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	op, err := writeOp(r)
	if err == nil && r.ContentLength > fileMax {
		err = refuse(RefTooLarge, "the body is over %d bytes (limits.fileMax)", fileMax)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	op.Owner = ownerOf(d)
	fc, err := m.begin(r.Context(), k, d.Name, op)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer fc.end()
	res, err := fc.sendBody(r.Body, fileMax, "fileMax")
	if err == nil && res.Stat == nil {
		err = errNoStat
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statOf(op.Path, res.Stat))
}

// writeOp reads a content PUT's query.
func writeOp(r *http.Request) (proto.FileOp, error) {
	q := r.URL.Query()
	op := proto.FileOp{Op: "write", Max: fileMax}
	var err error
	if op.Path, err = sandboxPath("path", q.Get("path")); err != nil {
		return op, err
	}
	if s := q.Get("mode"); s != "" {
		mode, err := strconv.ParseUint(s, 8, 32)
		if err != nil || mode > 0o7777 {
			return op, refuse(RefInvalid, "mode %.16q is permission bits in octal (\"0644\")", s)
		}
		op.Mode = uint32(mode)
	}
	if op.Mkdirs, err = flagParam(q, "mkdirs"); err != nil {
		return op, err
	}
	op.IfMatch = q.Get("ifMatch")
	if len(op.IfMatch) > etagMax || !utf8.ValidString(op.IfMatch) {
		return op, refuse(RefInvalid, "ifMatch is an etag (a stat's etag, or a read's ETag)")
	}
	switch q.Get("ifNoneMatch") {
	case "":
	case "*":
		op.Create = true
	default:
		return op, refuse(RefInvalid, "ifNoneMatch takes only * (create the file only if it doesn't exist)")
	}
	if op.Create && op.IfMatch != "" {
		return op, refuse(RefInvalid, "ifMatch and ifNoneMatch=* can't both hold")
	}
	return op, nil
}

// byteCount reads an offset or a length: a non-negative integer, 0 when
// absent.
func byteCount(s, name string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, refuse(RefInvalid, "%s is a byte count", name)
	}
	return n, nil
}

// ServeListDir answers GET /sandboxes/{name}/files/list?path=&limit=: at
// most limit entries (1000 by default), sorted by name.
func (m *Manager) ServeListDir(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	p, err := sandboxPath("path", q.Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := 1000
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeErr(w, refuse(RefInvalid, "limit is a positive count"))
			return
		}
		limit = min(n, listMax)
	}
	res, err := m.oneLine(r.Context(), k, d.Name, proto.FileOp{Op: "list", Path: p, Limit: limit})
	if err != nil {
		writeErr(w, err)
		return
	}
	out := fileList{Path: p, Entries: make([]fileEntry, 0, len(res.Entries)), Truncated: res.Truncated}
	for _, e := range res.Entries {
		out.Entries = append(out.Entries, fileEntry{Name: e.Name, Type: e.Type, Size: e.Size, MtimeMs: e.MTimeMs,
			Mode: octal(e.Mode), Target: e.Target})
	}
	writeJSON(w, http.StatusOK, out)
}

// ServeMkdir answers POST /sandboxes/{name}/files/mkdir {path, parents} → 204.
func (m *Manager) ServeMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Parents bool   `json:"parents"`
	}
	m.fileChange(w, r, &req, func(d *Def) (proto.FileOp, error) {
		p, err := sandboxPath("path", req.Path)
		return proto.FileOp{Op: "mkdir", Path: p, Parents: req.Parents, Owner: ownerOf(d)}, err
	})
}

// ServeRemove answers POST /sandboxes/{name}/files/remove {path, recursive} → 204.
func (m *Manager) ServeRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	m.fileChange(w, r, &req, func(*Def) (proto.FileOp, error) {
		p, err := sandboxPath("path", req.Path)
		return proto.FileOp{Op: "remove", Path: p, Recursive: req.Recursive}, err
	})
}

// ServeMove answers POST /sandboxes/{name}/files/move {from, to, overwrite}
// → 204; onto an existing path without overwrite it is 412 precondition.
func (m *Manager) ServeMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Overwrite bool   `json:"overwrite"`
	}
	m.fileChange(w, r, &req, func(*Def) (proto.FileOp, error) {
		from, err := sandboxPath("from", req.From)
		if err != nil {
			return proto.FileOp{}, err
		}
		to, err := sandboxPath("to", req.To)
		return proto.FileOp{Op: "move", Path: from, To: to, Overwrite: req.Overwrite}, err
	})
}

// fileChange runs a one-line change whose body is JSON (mkdir, remove,
// move): 204 when it's done.
func (m *Manager) fileChange(w http.ResponseWriter, r *http.Request, req any, op func(*Def) (proto.FileOp, error)) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	if err := decode(w, r, fileBodyMax, req); err != nil {
		writeErr(w, err)
		return
	}
	o, err := op(d)
	if err == nil {
		_, err = m.oneLine(r.Context(), k, d.Name, o)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeGetTar answers GET /sandboxes/{name}/tar?path=&exclude=…: the
// directory as a tar stream, names relative to path, symlinks stored as
// links. A tar of / leaves out /proc, /sys and /dev.
func (m *Manager) ServeGetTar(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	p, err := sandboxPath("path", q.Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, x := range q["exclude"] {
		if _, perr := path.Match(x, ""); x == "" || perr != nil || strings.IndexByte(x, 0) >= 0 || !utf8.ValidString(x) {
			writeErr(w, refuse(RefInvalid, "exclude %.64q is not a glob", x))
			return
		}
	}
	fc, err := m.begin(r.Context(), k, d.Name, proto.FileOp{Op: "tar-get", Path: p, Exclude: q["exclude"], Max: tarMax})
	if err != nil {
		writeErr(w, err)
		return
	}
	defer fc.end()
	if _, err := fc.result(); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := fc.pipeOut(w); err != nil {
		abort() // past tarMax, or a read failed: the tar isn't whole
	}
}

// ServePutTar answers PUT /sandboxes/{name}/tar?path=&mkdirs=1: the tar
// body extracted under path (made first with mkdirs), never outside it →
// 204. Created entries are owned by the definition's default uid/gid.
func (m *Manager) ServePutTar(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	p, err := sandboxPath("path", q.Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	mkdirs, err := flagParam(q, "mkdirs")
	if err == nil && r.ContentLength > tarMax {
		err = refuse(RefTooLarge, "the body is over %d bytes (limits.tarMax)", tarMax)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	fc, err := m.begin(r.Context(), k, d.Name, proto.FileOp{Op: "tar-put", Path: p, Mkdirs: mkdirs, Owner: ownerOf(d), Max: tarMax})
	if err != nil {
		writeErr(w, err)
		return
	}
	defer fc.end()
	if _, err := fc.sendBody(r.Body, tarMax, "tarMax"); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeCopy answers POST /sandboxes/copy {from:{sandbox, path}, to:{sandbox,
// path}, overwrite}: between two sandboxes of the caller's own → 204.
func (m *Manager) ServeCopy(w http.ResponseWriter, r *http.Request) {
	k, ok := m.manager(w, r)
	if !ok {
		return
	}
	var req copyReq
	if err := decode(w, r, fileBodyMax, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := req.check(); err != nil {
		writeErr(w, err)
		return
	}
	from, ok := m.lookup(w, k, req.From.Sandbox)
	if !ok {
		return
	}
	to, ok := m.lookup(w, k, req.To.Sandbox)
	if !ok {
		return
	}
	if err := m.copyPaths(r.Context(), k, from, to, req); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
