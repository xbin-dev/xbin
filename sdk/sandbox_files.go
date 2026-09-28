package xbin

// sandbox_files.go — files and trees in a tile sandbox (the contract's
// files and tar capabilities). Paths are absolute and resolved inside the
// sandbox; neither xbind nor the SDK ever resolves one on the host. Bodies
// stream both ways.

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// FileStat is a path in a sandbox: Type is file, dir, symlink or other,
// Mode the permission bits in octal ("0644"), ETag what IfMatch takes (it
// changes whenever the content does), Target a symlink's.
type FileStat struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	MtimeMs int64  `json:"mtimeMs"`
	ETag    string `json:"etag"`
	Target  string `json:"target,omitempty"`
}

// FileList is a directory's entries, at most the limit asked for
// (Truncated when there were more).
type FileList struct {
	Path      string      `json:"path"`
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// FileEntry is one entry of a FileList.
type FileEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	MtimeMs int64  `json:"mtimeMs"`
	Mode    string `json:"mode"`
	Target  string `json:"target,omitempty"`
}

// WriteOptions tune WriteFile: Mode sets the permission bits ("0644"),
// Mkdirs creates missing parents, IfMatch replaces the file only while its
// etag is this one (otherwise a *SandboxError with refusal precondition and
// the current ETag), and IfNoneMatch "*" only creates it.
type WriteOptions struct {
	Mode        string
	Mkdirs      bool
	IfMatch     string
	IfNoneMatch string
}

// Stat is a path's stat.
func (b *Sandbox) Stat(ctx context.Context, path string) (*FileStat, error) {
	route, err := b.route("files/stat")
	if err != nil {
		return nil, err
	}
	var st FileStat
	if err := b.s.call(ctx, http.MethodGet, route, url.Values{"path": {path}}, nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// ReadFile opens a file's content from byte off, n bytes of it (n ≤ 0: to
// its end); the caller closes the body. The stat carries the path and the
// content's ETag only — Stat has the rest. A file over limits.fileMax is
// too-large unless n bounds the read.
func (b *Sandbox) ReadFile(ctx context.Context, path string, off, n int64) (io.ReadCloser, *FileStat, error) {
	route, err := b.route("files/content")
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{"path": {path}}
	if off > 0 {
		q.Set("offset", strconv.FormatInt(off, 10))
	}
	if n > 0 {
		q.Set("length", strconv.FormatInt(n, 10))
	}
	resp, err := b.s.open(ctx, http.MethodGet, route, q, nil, "")
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, &FileStat{Path: path, ETag: strings.Trim(resp.Header.Get("ETag"), `"`)}, nil
}

// WriteFile replaces a file with r's bytes, streamed (atomically: the
// runtime writes a temporary file and renames it into place), and answers
// its stat.
func (b *Sandbox) WriteFile(ctx context.Context, path string, r io.Reader, o WriteOptions) (*FileStat, error) {
	route, err := b.route("files/content")
	if err != nil {
		return nil, err
	}
	q := url.Values{"path": {path}}
	if o.Mode != "" {
		q.Set("mode", o.Mode)
	}
	if o.Mkdirs {
		q.Set("mkdirs", "1")
	}
	if o.IfMatch != "" {
		q.Set("ifMatch", o.IfMatch)
	}
	if o.IfNoneMatch != "" {
		q.Set("ifNoneMatch", o.IfNoneMatch)
	}
	if r == nil {
		r = http.NoBody
	}
	resp, err := b.s.open(ctx, http.MethodPut, route, q, r, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var st FileStat
	if err := decodeAnswer(resp, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// List lists a directory, at most limit entries (0: the runtime's default,
// 1000).
func (b *Sandbox) List(ctx context.Context, path string, limit int) (*FileList, error) {
	route, err := b.route("files/list")
	if err != nil {
		return nil, err
	}
	q := url.Values{"path": {path}}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var l FileList
	if err := b.s.call(ctx, http.MethodGet, route, q, nil, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// Mkdir makes a directory; parents makes the missing ones too (and an
// existing directory fine).
func (b *Sandbox) Mkdir(ctx context.Context, path string, parents bool) error {
	return b.fileOp(ctx, "files/mkdir", struct {
		Path    string `json:"path"`
		Parents bool   `json:"parents,omitempty"`
	}{path, parents})
}

// Remove removes a path; a non-empty directory needs recursive.
func (b *Sandbox) Remove(ctx context.Context, path string, recursive bool) error {
	return b.fileOp(ctx, "files/remove", struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive,omitempty"`
	}{path, recursive})
}

// Move renames from to to; onto an existing path it needs overwrite
// (precondition otherwise).
func (b *Sandbox) Move(ctx context.Context, from, to string, overwrite bool) error {
	return b.fileOp(ctx, "files/move", struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Overwrite bool   `json:"overwrite,omitempty"`
	}{from, to, overwrite})
}

func (b *Sandbox) fileOp(ctx context.Context, sub string, body any) error {
	route, err := b.route(sub)
	if err != nil {
		return err
	}
	return b.s.call(ctx, http.MethodPost, route, nil, body, nil)
}

// GetTar reads the tree at path as a tar stream (names relative to path),
// leaving out the exclude patterns; the caller closes it.
func (b *Sandbox) GetTar(ctx context.Context, path string, exclude []string) (io.ReadCloser, error) {
	route, err := b.route("tar")
	if err != nil {
		return nil, err
	}
	q := url.Values{"path": {path}}
	for _, e := range exclude {
		q.Add("exclude", e)
	}
	resp, err := b.s.open(ctx, http.MethodGet, route, q, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// PutTar extracts the tar stream r under the directory path (mkdirs creates
// it), streamed.
func (b *Sandbox) PutTar(ctx context.Context, path string, r io.Reader, mkdirs bool) error {
	route, err := b.route("tar")
	if err != nil {
		return err
	}
	q := url.Values{"path": {path}}
	if mkdirs {
		q.Set("mkdirs", "1")
	}
	if r == nil {
		r = http.NoBody
	}
	resp, err := b.s.open(ctx, http.MethodPut, route, q, r, "application/x-tar")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
