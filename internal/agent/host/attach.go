package host

// A prompt's ATTACHMENTS, dropped inside the sandbox (_xbin/attach, from
// the daemon): the daemon's ACP client hands each file of a prompt to the
// host before the prompt, and names the path in the prompt (a resource
// link) so the agent can read it with its own tools — a PDF, a log, the
// screenshot it should put in the docs. The files go to a private
// directory under the sandbox's own /tmp (a tmpfs that dies with the
// sandbox), never into the tile. With isolation off there is no sandbox:
// the daemon makes the directory and names it in _xbin/spawn (attachDir),
// and removes it when the session ends — the host is SIGKILLed then, so
// nothing of its own would run. The directory holds at most attachBudget
// bytes: past it the oldest files are removed (the agent has read them by
// then, or can ask for them again).

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/agent/acp"
)

const attachBudget = 64 << 20

type attachments struct {
	mu    sync.Mutex
	dir   string
	files []dropped // oldest first
	total int64
}

// setDir adopts the daemon's directory for the session's files (_xbin/spawn
// attachDir; "" = make one on first use).
func (a *attachments) setDir(dir string) {
	if dir == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dir = dir
}

type dropped struct {
	path string
	size int64
}

// attach serves _xbin/attach: writes the file, answers its path.
func (h *Host) attach(p acp.AttachParams) (any, *acp.Error) {
	name := plainName(p.Name)
	if name == "" {
		return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: "attachment name must be a plain file name"}
	}
	size := int64(len(p.Data))
	if size > attachBudget {
		return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: "attachment larger than the session's attachment budget"}
	}
	a := &h.att
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dir == "" {
		d, err := os.MkdirTemp("", "xbin-attachments-")
		if err != nil {
			return nil, &acp.Error{Code: acp.ErrInternal, Message: err.Error()}
		}
		a.dir = d
	}
	for a.total+size > attachBudget && len(a.files) > 0 {
		_ = os.Remove(a.files[0].path)
		a.total -= a.files[0].size
		a.files = a.files[1:]
	}
	path, err := createUnique(a.dir, name, p.Data)
	if err != nil {
		return nil, &acp.Error{Code: acp.ErrInternal, Message: err.Error()}
	}
	a.files = append(a.files, dropped{path, size})
	a.total += size
	return acp.AttachResult{Path: path}, nil
}

// createUnique writes data to dir/name, or dir/<stem>-<n><ext> when that
// exists (a second screenshot.png of the session). O_EXCL: never through a
// file that is already there.
func createUnique(dir, name string, data []byte) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 1; n < 10000; n++ {
		path := filepath.Join(dir, name)
		if n > 1 {
			path = filepath.Join(dir, stem+"-"+strconv.Itoa(n)+ext)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(path)
			return "", werr
		}
		return path, nil
	}
	return "", errors.New("too many attachments with that name")
}

// plainName is name when it is a plain file name (the daemon sends them so;
// checked again here), else "".
func plainName(name string) string {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return ""
	}
	return name
}

// dropAttachments removes the session's attachment directory on a clean
// host exit (the daemon closed our stdin: the session ended, or xbind
// stopped) — the daemon's too, which it also removes itself when the
// session ends: a SIGKILLed host runs nothing.
func (h *Host) dropAttachments() {
	h.att.mu.Lock()
	defer h.att.mu.Unlock()
	if h.att.dir != "" {
		_ = os.RemoveAll(h.att.dir)
		h.att.dir, h.att.files, h.att.total = "", nil, 0
	}
}
