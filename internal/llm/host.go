package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// host.go — the deployment file (XBIN_LLM_CONFIG, docs/llm.md "The host's
// file"): the connector's settings as whoever runs this xbind sets them. It
// is Settings as JSON (comments allowed), with two differences: an
// upstream's key is named by file (keyFile, relative to the config file),
// and lockAll makes every setting the host's. Whatever it sets is locked:
// reads mark it, writes through the API are refused.
//
// It is read at boot — a file that can't be read or is invalid stops the
// boot, so a host never runs without the limits it wrote — and again when
// it or one of its key files changes (their size and modification time are
// looked at, at most every hostCheckGap, as the ingress certificate's are).
// A changed file that is invalid is refused: the last good one stays in
// force and the admin's view says why.

// hostConfig is a loaded deployment file.
type hostConfig struct {
	Settings
	// LockAll makes every setting of the connector the host's: what the
	// file leaves out keeps its default, and the admin's own settings and
	// upstreams are not in force.
	LockAll bool `json:"lockAll,omitempty"`
	// WorkspaceAgent is the one key that isn't the connector's: the host's
	// rule for workspace agents (hostagent.go).
	WorkspaceAgent HostAgent `json:"workspaceAgent"`

	keys   map[string]string // upstream → the key read from its keyFile
	stamps map[string]fileStamp
}

// fileStamp is what a file looked like when it was read.
type fileStamp struct {
	size int64
	mod  time.Time
	gone bool
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{gone: true}
	}
	return fileStamp{size: fi.Size(), mod: fi.ModTime()}
}

// maxKeyFile bounds a key file: a key is a line, not a document.
const maxKeyFile = 16 << 10

// loadHost reads the deployment file at path.
func loadHost(path string) (*hostConfig, error) {
	// a file's stamp is taken before it is read: one caught mid-write is
	// then seen as changed once its writer is done, whatever was read
	h := &hostConfig{keys: map[string]string{}, stamps: map[string]fileStamp{path: stampOf(path)}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("XBIN_LLM_CONFIG: %w", err)
	}
	if afterHostRead != nil {
		afterHostRead(path)
	}
	dec := json.NewDecoder(bytes.NewReader(jsonc.Strip(raw)))
	dec.DisallowUnknownFields() // a misspelled setting is an error, never a limit silently not applied
	if err := dec.Decode(h); err != nil {
		return nil, fmt.Errorf("XBIN_LLM_CONFIG %s: %w", path, err)
	}
	if err := h.Settings.validate(); err != nil {
		return nil, fmt.Errorf("XBIN_LLM_CONFIG %s: %w", path, err)
	}
	for name, u := range h.Upstreams {
		if u.HasKey {
			return nil, fmt.Errorf("XBIN_LLM_CONFIG %s: upstream %s: hasKey is the admin store's; name the key's file with keyFile", path, name)
		}
		if u.KeyFile == "" {
			continue // an upstream that takes no key (a model server on the host's own network)
		}
		kf := u.KeyFile
		if !filepath.IsAbs(kf) {
			kf = filepath.Join(filepath.Dir(path), kf)
		}
		h.stamps[kf] = stampOf(kf)
		b, err := os.ReadFile(kf)
		if err != nil {
			return nil, fmt.Errorf("XBIN_LLM_CONFIG %s: upstream %s: its key file: %w", path, name, err)
		}
		key := strings.TrimSpace(string(b))
		if key == "" || len(b) > maxKeyFile || strings.ContainsAny(key, "\r\n") {
			return nil, fmt.Errorf("XBIN_LLM_CONFIG %s: upstream %s: its key file holds no key (one line)", path, name)
		}
		h.keys[name] = key
	}
	if err := hostServerSecrets(h, path); err != nil {
		return nil, fmt.Errorf("XBIN_LLM_CONFIG %s: %w", path, err)
	}
	return h, nil
}

// changed reports whether the file or one of its key files looks different
// from when h was loaded.
func (h *hostConfig) changed() bool {
	for p, was := range h.stamps {
		if now := stampOf(p); now.gone != was.gone || now.size != was.size || !now.mod.Equal(was.mod) {
			return true
		}
	}
	return false
}

// afterHostRead, in tests, runs after the deployment file was read and
// before it is parsed: where a writer finishing the file would land.
var afterHostRead func(path string)

// hostCheckGap is how often, at most, the deployment file is looked at.
const hostCheckGap = 2 * time.Second

// hostWatch holds the deployment file in force and reloads it on change.
type hostWatch struct {
	path string

	mu      sync.Mutex
	cur     *hostConfig // the last file that loaded
	err     string      // why the latest reload was refused
	checked time.Time
}

// poll looks at the file when it is due and reports whether what is in
// force (or the reload error) changed.
func (w *hostWatch) poll(now time.Time) bool {
	if w == nil || w.path == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if now.Sub(w.checked) < hostCheckGap && !w.checked.IsZero() {
		return false
	}
	w.checked = now
	if w.cur != nil && !w.cur.changed() {
		return false
	}
	// what the files look like before they are read: a refused read is
	// remembered as these, so a file still being written is read again
	// when it has changed since — never missed
	before := map[string]fileStamp{}
	if w.cur != nil {
		for p := range w.cur.stamps {
			before[p] = stampOf(p)
		}
	}
	h, err := loadHost(w.path)
	if err != nil {
		// refused: the last good file stays in force, and the same broken
		// file isn't read again until it changes once more
		msg := err.Error()
		same := msg == w.err
		w.err = msg
		if w.cur != nil {
			w.cur.stamps = before
		}
		return !same
	}
	w.cur, w.err = h, ""
	return true
}

func (w *hostWatch) current() (*hostConfig, string) {
	if w == nil || w.path == "" {
		return nil, ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cur, w.err
}
