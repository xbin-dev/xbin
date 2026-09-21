package host

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent/acp"
)

// The fs/* half of the ACP client capabilities, served inside the sandbox:
// the kernel's mount view (the allow-list, the secret masks, the read-only
// tiles) is the authority on what the agent may read, so the host reads
// what the agent could read from a shell — and writes only under the
// session's cwd (the tile), whatever the mounts would allow.

const maxReadBytes = 8 << 20

// readTextFile serves fs/read_text_file: the whole file, or `limit` lines
// from 1-based `line`.
func (h *Host) readTextFile(p acp.FsReadParams) (any, *acp.Error) {
	if !filepath.IsAbs(p.Path) {
		return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: "path must be absolute"}
	}
	fi, err := os.Stat(p.Path)
	if err != nil {
		return nil, &acp.Error{Code: acp.ErrNoResource, Message: err.Error()}
	}
	if fi.IsDir() {
		return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: p.Path + " is a directory"}
	}
	if fi.Size() > maxReadBytes {
		return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: "file larger than 8 MiB"}
	}
	b, err := os.ReadFile(p.Path)
	if err != nil {
		return nil, &acp.Error{Code: acp.ErrNoResource, Message: err.Error()}
	}
	s := string(b)
	if p.Line != nil || p.Limit != nil {
		lines := strings.SplitAfter(s, "\n")
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
		start := 0
		if p.Line != nil && *p.Line > 1 {
			start = min(*p.Line-1, len(lines))
		}
		end := len(lines)
		if p.Limit != nil && *p.Limit >= 0 {
			end = min(start+*p.Limit, len(lines))
		}
		s = strings.Join(lines[start:end], "")
	}
	return acp.FsReadResult{Content: s}, nil
}

// writeTextFile serves fs/write_text_file: under the session cwd only;
// parents are created.
func (h *Host) writeTextFile(p acp.FsWriteParams) (any, *acp.Error) {
	if !filepath.IsAbs(p.Path) {
		return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: "path must be absolute"}
	}
	if !underDir(p.Path, h.cwd) {
		return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: "writes are confined to the session's tile: " + h.cwd}
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return nil, &acp.Error{Code: acp.ErrInternal, Message: err.Error()}
	}
	if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
		return nil, &acp.Error{Code: acp.ErrInternal, Message: err.Error()}
	}
	return map[string]any{}, nil
}

// underDir reports whether path (cleaned) is dir or below it.
func underDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
