package term

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/magik6k/xbin/internal/auth"
)

// Per-user terminal homes (decision D6, amended). $HOME used to be one shared
// <ws>/home for every terminal; now each signed-in user gets
// <ws>/homes/<user>, and the root token gets homes/owner — so agent-CLI config
// (~/.claude, credentials), shell history, and dotfiles are scoped per human.
// This is filesystem hygiene (terminals share one unix user); the API
// credential is separately scoped per session (plans/terminal-tokens.md).

const ownerHomeKey = "owner"

// HomeKey maps the calling principal to its home-directory key: the sanitized
// user id, or "owner" for the root token. (A real user literally named "owner"
// would share the token principal's home — token login is normally disabled
// once users exist, so that collision is theoretical.)
func HomeKey(p auth.Principal) string { return sanitizeHomeKey(p.UserID) }

// sanitizeHomeKey makes a user id safe as a directory name. Anything outside
// [a-zA-Z0-9._-] becomes '_'; empty or dots-only ids fall back to "owner".
func sanitizeHomeKey(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if strings.Trim(s, ".") == "" { // "", ".", ".." and friends
		return ownerHomeKey
	}
	return s
}

// HomeDir is a user's terminal home directory.
func HomeDir(root, key string) string { return filepath.Join(root, "homes", key) }

// MigrateHomes converts a workspace from the legacy shared <root>/home to
// per-user <root>/homes/<user>. Idempotent; run once at startup:
//
//   - only home/ exists  → renamed to homes/<target> (all data preserved; the
//     caller picks target from the users store — the sole user/admin, else owner)
//   - both exist         → home/ is removed iff it is a pristine seeded skeleton
//     (per the pristine callback — e.g. recreated by a downgraded xbind's
//     backfill); otherwise ERROR: real data in both places is never guessed at,
//     the operator merges by hand
//   - neither            → homes/ is created empty (nothing at risk)
//
// It also ensures the workspace .gitignore covers homes/ — user homes hold
// secrets (agent-CLI credentials) and must never land in the workspace repo.
func MigrateHomes(root, target string, pristine func(rel string, data []byte) bool) (moved string, err error) {
	homes := filepath.Join(root, "homes")
	legacy := filepath.Join(root, "home")
	switch {
	case pathIsDir(legacy) && !pathIsDir(homes):
		if err := os.MkdirAll(homes, 0o755); err != nil {
			return "", err
		}
		dst := HomeDir(root, sanitizeHomeKey(target))
		if err := os.Rename(legacy, dst); err != nil {
			return "", err
		}
		moved = dst
	case pathIsDir(legacy) && pathIsDir(homes):
		ok, werr := dirPristine(legacy, pristine)
		if werr != nil {
			return "", werr
		}
		if !ok {
			return "", fmt.Errorf("both %s and %s exist, and home/ holds real data — merge it into homes/<user> by hand (e.g. `mv %s/.claude %s/<user>/`), remove home/, and restart xbind",
				legacy, homes, legacy, homes)
		}
		if err := os.RemoveAll(legacy); err != nil {
			return "", err
		}
	case !pathIsDir(homes):
		if err := os.MkdirAll(homes, 0o755); err != nil {
			return "", err
		}
	}
	return moved, ensureGitignoreLine(root, "homes/")
}

// dirPristine reports whether dir contains nothing beyond files the pristine
// callback recognizes as untouched template skeleton (an empty dir is
// pristine). Any subdirectory (e.g. .claude/) means real data.
func dirPristine(dir string, pristine func(rel string, data []byte) bool) (bool, error) {
	if pristine == nil {
		return false, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() {
			return false, nil
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return false, err
		}
		if !pristine(e.Name(), data) {
			return false, nil
		}
	}
	return true, nil
}

// ensureGitignoreLine appends line to the workspace .gitignore if absent.
func ensureGitignoreLine(root, line string) error {
	p := filepath.Join(root, ".gitignore")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(p, []byte(line+"\n"), 0o644)
		}
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	out := string(b)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(p, []byte(out+line+"\n"), 0o644)
}
