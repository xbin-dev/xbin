package host

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent/acp"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// signin.go — a person's saved sign-in in the agent's environment (D230,
// docs/agent-signins.md). The daemon sends the credential's line in the
// spawn frame; the host sets it on the agent's process alone. Its own
// environment, the sandbox's spec and every terminal the agent opens
// through the host stay without it (terminal.go builds a terminal's
// environment from the agent's request, which names what it wants).
//
// A CLI that writes a key handed to it into a file (codex: auth.json under
// CODEX_HOME) gets a key directory of its own: made under the sandbox's
// /tmp — a tmpfs no other session mounts, gone with the sandbox — holding
// links to everything in the person's own directory but that file, so the
// CLI finds its config and its past sessions, and writes the key where
// nothing else reads. The host removes it when the agent is gone.

// Outcomes the host reports (HelloParams.Signin).
const (
	signinSaved = "saved"
	signinHome  = "home"
)

// applySignin answers the environment the agent starts with and what
// became of the saved sign-in ("" when there was none).
func (h *Host) applySignin(env []string, s *acp.SpawnSignin) ([]string, string) {
	if s == nil || s.Env == "" {
		return env, ""
	}
	name, _, ok := strings.Cut(s.Env, "=")
	if !ok || name == "" {
		return env, ""
	}
	getenv := func(n string) string {
		for i := len(env) - 1; i >= 0; i-- {
			if v, ok := strings.CutPrefix(env[i], n+"="); ok {
				return v
			}
		}
		return ""
	}
	var extra []string
	drop := map[string]bool{name: true}
	if k := s.KeyHome; k != nil && k.Env != "" && k.File != "" {
		real := getenv(k.Env)
		if real == "" {
			real = sdkacp.ExpandWord(k.Dir, getenv)
		}
		if fi, err := os.Stat(filepath.Join(real, k.File)); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return env, signinHome // the CLI is signed in on its own here: it would start on that
		}
		dir, err := h.makeKeyHome(real, k.File, k.At)
		if err != nil {
			h.note("no private key directory for the saved sign-in (" + err.Error() + "): it is left out")
			return env, signinHome
		}
		extra = append(extra, k.Env+"="+dir)
		drop[k.Env] = true
	}
	for _, n := range s.Unset {
		drop[n] = true
	}
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if n, _, _ := strings.Cut(kv, "="); !drop[n] {
			out = append(out, kv)
		}
	}
	out = append(out, extra...)
	return append(out, s.Env), signinSaved
}

// makeKeyHome makes the agent's private key directory: links to what the
// person's own (real; it may not exist yet) holds, but for skip.
func (h *Host) makeKeyHome(real, skip, at string) (string, error) {
	dir := at
	if dir == "" {
		d, err := os.MkdirTemp("", "xbin-signin-")
		if err != nil {
			return "", err
		}
		dir, h.keyHome = d, d // the host's own: removed when the agent is gone
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		h.dropKeyHome()
		return "", err
	}
	if real == "" || !filepath.IsAbs(real) {
		return dir, nil
	}
	// what the CLI keeps across sessions lives in the person's directory:
	// made now, so its past sessions land there and not in the private one
	for _, sub := range []string{"sessions", "log"} {
		_ = os.MkdirAll(filepath.Join(real, sub), 0o700)
	}
	ents, _ := os.ReadDir(real)
	for _, e := range ents {
		if e.Name() == skip {
			continue
		}
		_ = os.Symlink(filepath.Join(real, e.Name()), filepath.Join(dir, e.Name()))
	}
	return dir, nil
}

// homeLogin reports whether one of the provider's login files (shell words)
// is in the session's $HOME.
func homeLogin(env, files []string) bool {
	getenv := func(n string) string {
		for i := len(env) - 1; i >= 0; i-- {
			if v, ok := strings.CutPrefix(env[i], n+"="); ok {
				return v
			}
		}
		return ""
	}
	for _, f := range files {
		p := sdkacp.ExpandWord(f, getenv)
		if fi, err := os.Stat(p); p != "" && filepath.IsAbs(p) && err == nil && !fi.IsDir() && fi.Size() > 0 {
			return true
		}
	}
	return false
}

// dropKeyHome removes the private key directory (the links, and the key
// file the CLI wrote there) — never what the links point at.
func (h *Host) dropKeyHome() {
	if h.keyHome != "" {
		_ = os.RemoveAll(h.keyHome) // RemoveAll unlinks a symlink; it never follows one
		h.keyHome = ""
	}
}
