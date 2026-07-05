// Package util holds small shared helpers: safe path joining, token
// generation, and the ignore rules applied when walking workspace trees.
package util

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Slugify turns arbitrary text into a URL-safe component name (lowercase,
// non-alphanumeric runs collapsed to '-', trimmed).
func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// VersionLess compares version-like tags (e.g. "v1.10.0" > "v1.9.0") field by
// field, numerically where both fields are numbers. Good enough to sort a tag
// list newest-first; not a full semver prerelease ordering.
func VersionLess(a, b string) bool {
	fa, fb := verFields(a), verFields(b)
	for i := 0; i < len(fa) && i < len(fb); i++ {
		if fa[i] == fb[i] {
			continue
		}
		na, ea := strconv.Atoi(fa[i])
		nb, eb := strconv.Atoi(fb[i])
		if ea == nil && eb == nil {
			return na < nb
		}
		return fa[i] < fb[i]
	}
	return len(fa) < len(fb)
}

func verFields(v string) []string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	return strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
}

var ErrUnsafePath = errors.New("path escapes workspace")

// ReservedTop are workspace top-level names that cannot be components.
var ReservedTop = map[string]bool{
	".xbin": true, "vendor": true, "data": true, "home": true, "xbin": true,
}

// IgnoredDirs are never watched, scanned, or served as component internals.
var IgnoredDirs = map[string]bool{
	".git": true, ".xbin": true, "node_modules": true, "deps": true,
	"__pycache__": true,
}

// SafeJoin resolves rel (slash-separated, from a URL or manifest) under root,
// rejecting anything that escapes root. Returns the joined OS path and the
// cleaned relative path.
func SafeJoin(root, rel string) (string, string, error) {
	rel = strings.TrimPrefix(rel, "/")
	// Reject any ".." element outright — even ones Clean would resolve
	// harmlessly against the root. Requests carrying ".." are hostile or
	// broken; there is no legitimate use through these APIs.
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", "", ErrUnsafePath
		}
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+rel), "/")
	return filepath.Join(root, filepath.FromSlash(cleaned)), cleaned, nil
}

// ComponentPathOK reports whether p is an acceptable component path: relative,
// clean, non-reserved, and not inside an ignored dir.
func ComponentPathOK(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	parts := strings.Split(p, "/")
	if ReservedTop[parts[0]] {
		return false
	}
	for _, part := range parts {
		if part == ".." || part == "." || IgnoredDirs[part] || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// RandomToken returns n random bytes hex-encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not recoverable
	}
	return hex.EncodeToString(b)
}

// ScopeKey converts a scope path to its on-disk resource key ("apps/cal" →
// "apps~cal"), used under data/resources and data/vault.
func ScopeKey(scopePath string) string {
	if scopePath == "" {
		return "workspace"
	}
	return strings.ReplaceAll(scopePath, "/", "~")
}

// CompKey hashes a component path into a short filesystem-safe name, shared
// by the runner (sockets, build dirs, logs) and bx (log tailing). Keeps unix
// socket paths under the 108-byte limit.
func CompKey(comp string) string {
	h := sha256.Sum256([]byte(comp))
	base := strings.ReplaceAll(comp, "/", "~")
	if len(base) > 24 {
		base = base[:24]
	}
	return base + "-" + hex.EncodeToString(h[:4])
}
