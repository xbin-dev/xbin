// sandbox_files.go — the contract's file and tar routes (docs/sandbox-manager.md
// §Files), as typed calls on sbxConn (sandbox_client.go).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// --- files -------------------------------------------------------------------------

// sbxMode is a file mode as the manager writes it ("0644", or a number).
type sbxMode string

func (m *sbxMode) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*m = sbxMode(s)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		*m = ""
		return nil // a mode we can't read isn't worth failing the call over
	}
	*m = sbxMode(fmt.Sprintf("%04o", n))
	return nil
}

// sbxStat is GET …/files/stat (and PUT …/files/content's answer).
type sbxStat struct {
	Path    string  `json:"path"`
	Type    string  `json:"type"` // file | dir | symlink | other
	Size    int64   `json:"size"`
	Mode    sbxMode `json:"mode"`
	MtimeMs int64   `json:"mtimeMs"`
	ETag    string  `json:"etag"`
	Target  string  `json:"target,omitempty"`
}

type sbxDirEntry struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Size    int64   `json:"size"`
	MtimeMs int64   `json:"mtimeMs"`
	Mode    sbxMode `json:"mode"`
	Target  string  `json:"target,omitempty"`
}

type sbxListing struct {
	Path      string        `json:"path"`
	Entries   []sbxDirEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

func (c *sbxConn) Stat(ctx context.Context, id, path string) (*sbxStat, error) {
	var out sbxStat
	return &out, c.call(ctx, "GET", sbxPath(id, "files", "stat"), map[string]string{"path": path}, nil, &out, sbxCallTimeout)
}

// OpenFile streams a file (or length bytes of it from offset; length 0: to
// its end); the caller closes it. etag is the content's.
func (c *sbxConn) OpenFile(ctx context.Context, id, path string, offset, length int64) (body io.ReadCloser, etag string, err error) {
	q := url.Values{"path": {path}}
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	if length > 0 {
		q.Set("length", strconv.FormatInt(length, 10))
	}
	resp, err := c.open(ctx, "GET", sbxPath(id, "files", "content"), q, nil, "")
	if err != nil {
		return nil, "", err
	}
	return resp.Body, strings.Trim(resp.Header.Get("ETag"), `"`), nil
}

// ReadFile reads a file (a range of it, as OpenFile) into memory, refusing
// more than maxBytes (too-large).
func (c *sbxConn) ReadFile(ctx context.Context, id, path string, offset, length, maxBytes int64) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, sbxCallTimeout)
	defer cancel()
	body, etag, err := c.OpenFile(ctx, id, path, offset, length)
	if err != nil {
		return nil, "", err
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, "", &sbxError{Provider: c.M.Provider, Refusal: "unavailable", Msg: "reading " + path + ": " + err.Error()}
	}
	if int64(len(b)) > maxBytes {
		return nil, "", &sbxError{Provider: c.M.Provider, Refusal: "too-large", Msg: fmt.Sprintf("%s is over %d bytes", path, maxBytes)}
	}
	return b, etag, nil
}

// sbxWrite says how PUT …/files/content replaces a file.
type sbxWrite struct {
	Mode        string // octal ("0755"); "" keeps the file's, or the manager's default
	Mkdirs      bool   // create missing parent directories
	IfMatch     string // replace only while the file's etag is this one (precondition otherwise)
	IfNoneMatch bool   // create only: refuse when the file exists
}

// WriteFile replaces a file atomically with body.
func (c *sbxConn) WriteFile(ctx context.Context, id, path string, body io.Reader, w sbxWrite) (*sbxStat, error) {
	q := url.Values{"path": {path}}
	if w.Mode != "" {
		q.Set("mode", w.Mode)
	}
	if w.Mkdirs {
		q.Set("mkdirs", "1")
	}
	if w.IfMatch != "" {
		q.Set("ifMatch", w.IfMatch)
	}
	if w.IfNoneMatch {
		q.Set("ifNoneMatch", "*")
	}
	resp, err := c.open(ctx, "PUT", sbxPath(id, "files", "content"), q, body, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out sbxStat
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, &sbxError{Provider: c.M.Provider, Refusal: "unavailable", Msg: "a garbled answer: " + err.Error()}
	}
	return &out, nil
}

// ListDir lists a directory (limit 0: the manager's 1000).
func (c *sbxConn) ListDir(ctx context.Context, id, path string, limit int) (*sbxListing, error) {
	q := map[string]string{"path": path}
	if limit > 0 {
		q["limit"] = strconv.Itoa(limit)
	}
	var out sbxListing
	return &out, c.call(ctx, "GET", sbxPath(id, "files", "list"), q, nil, &out, sbxCallTimeout)
}

func (c *sbxConn) Mkdir(ctx context.Context, id, path string, parents bool) error {
	return c.call(ctx, "POST", sbxPath(id, "files", "mkdir"), nil, map[string]any{"path": path, "parents": parents}, nil, sbxCallTimeout)
}

func (c *sbxConn) Remove(ctx context.Context, id, path string, recursive bool) error {
	return c.call(ctx, "POST", sbxPath(id, "files", "remove"), nil, map[string]any{"path": path, "recursive": recursive}, nil, sbxCallTimeout)
}

func (c *sbxConn) Move(ctx context.Context, id, from, to string, overwrite bool) error {
	return c.call(ctx, "POST", sbxPath(id, "files", "move"), nil, map[string]any{"from": from, "to": to, "overwrite": overwrite}, nil, sbxCallTimeout)
}

// TarGet streams a directory as a tar (names relative to path), leaving out
// the exclude patterns; the caller closes it. Needs the tar capability.
func (c *sbxConn) TarGet(ctx context.Context, id, path string, exclude []string) (io.ReadCloser, error) {
	q := url.Values{"path": {path}}
	for _, x := range exclude {
		q.Add("exclude", x)
	}
	resp, err := c.open(ctx, "GET", sbxPath(id, "tar"), q, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// TarPut extracts a tar stream under path.
func (c *sbxConn) TarPut(ctx context.Context, id, path string, tarball io.Reader, mkdirs bool) error {
	q := url.Values{"path": {path}}
	if mkdirs {
		q.Set("mkdirs", "1")
	}
	resp, err := c.open(ctx, "PUT", sbxPath(id, "tar"), q, tarball, "application/x-tar")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
