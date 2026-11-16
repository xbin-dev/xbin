package delegation

// hostfile.go — the host's deployment file for the workspace agent's web
// and MCP policy (XBIN_WSAGENT_CONFIG; D236), as XBIN_LLM_CONFIG is the
// connector's (internal/llm/host.go): JSON with comments, read at boot — a
// file that can't be read or is invalid stops the boot, so a host never
// runs without the limits it wrote — and looked at again, at most every
// hostCheckGap, when the policy is asked; a change is reloaded, and a
// change that is invalid is refused (the last good file stays in force,
// the admin's view says so).
//
// What it sets is the host's: a mode is a preset and a ceiling (the admin
// narrows, never widens), a list always holds, and lockAll makes every web
// and MCP setting the host's alone.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// HostNet is the host's file as it reads.
type HostNet struct {
	Web     *WebPolicy `json:"web,omitempty"`
	MCP     *MCPPolicy `json:"mcp,omitempty"`
	LockAll bool       `json:"lockAll,omitempty"`
	// WorkspaceAgent is the host's switch and who-gets-one for workspace
	// agents, read by its own code where it is built; this file's reader
	// takes the key so that one host file can hold both (it judges only
	// the web and MCP keys).
	WorkspaceAgent json.RawMessage `json:"workspaceAgent,omitempty"`
}

// hostCheckGap is how often, at most, the file is looked at.
const hostCheckGap = 2 * time.Second

// HostFile is the host's file in force, reloaded when it changes.
type HostFile struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	cur     HostNet
	size    int64
	mod     time.Time
	err     string // why the latest change was refused
	checked time.Time
}

// LoadHostFile reads the file at path: an error when it can't be read or
// isn't valid — the boot stops on it.
func LoadHostFile(path string) (*HostFile, error) {
	h := &HostFile{path: path, now: time.Now}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("XBIN_WSAGENT_CONFIG: %w", err)
	}
	cur, err := readHostNet(path)
	if err != nil {
		return nil, err
	}
	h.cur, h.size, h.mod, h.checked = cur, fi.Size(), fi.ModTime(), h.now()
	return h, nil
}

func readHostNet(path string) (HostNet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return HostNet{}, fmt.Errorf("XBIN_WSAGENT_CONFIG: %w", err)
	}
	var h HostNet
	dec := json.NewDecoder(bytes.NewReader(jsonc.Strip(raw)))
	dec.DisallowUnknownFields() // a misspelled setting is an error, never a limit silently not applied
	if err := dec.Decode(&h); err != nil {
		return HostNet{}, fmt.Errorf("XBIN_WSAGENT_CONFIG %s: %w", path, err)
	}
	if h.Web != nil {
		w, err := h.Web.Normalized()
		if err != nil {
			return HostNet{}, fmt.Errorf("XBIN_WSAGENT_CONFIG %s: %w", path, err)
		}
		h.Web = &w
	}
	if h.MCP != nil {
		m, err := h.MCP.Normalized()
		if err != nil {
			return HostNet{}, fmt.Errorf("XBIN_WSAGENT_CONFIG %s: %w", path, err)
		}
		h.MCP = &m
	}
	return h, nil
}

// Current is the file in force, looked at again when that is due, and why
// the latest change was refused ("" when it wasn't).
func (h *HostFile) Current() (HostNet, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if now := h.now(); now.Sub(h.checked) >= hostCheckGap {
		h.checked = now
		fi, err := os.Stat(h.path)
		switch {
		case err != nil:
			h.err = "the host's configuration (XBIN_WSAGENT_CONFIG) can't be read; the earlier one stays in force (see xbind's log)"
		case fi.Size() != h.size || !fi.ModTime().Equal(h.mod):
			if next, err := readHostNet(h.path); err != nil {
				slog.Error("delegation: the host's changed XBIN_WSAGENT_CONFIG is refused; the earlier one stays in force", "err", err)
				h.err = "the host's configuration (XBIN_WSAGENT_CONFIG) changed and could not be read; the earlier one stays in force (see xbind's log)"
			} else {
				h.cur, h.err = next, ""
			}
			h.size, h.mod = fi.Size(), fi.ModTime() // the same broken file isn't read again until it changes once more
		}
	}
	return h.cur, h.err
}

// SetClock replaces the file's clock (tests).
func (h *HostFile) SetClock(now func() time.Time) {
	h.mu.Lock()
	h.now = now
	h.mu.Unlock()
}
