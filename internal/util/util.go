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
// Several are reserved because a component path becomes an injected
// X-XBin-From identity: a top-level component named "owner" would make the
// proxy inject `X-XBin-From: owner`, so callees' SDK would see it as the human
// owner (Caller().Owner) — an impersonation across the identity spine. Same
// for "ingress" (the anonymous public-caller identity, plans/ingress.md) and
// "runtime" (the builtin ingress source). "xbin/cron" and "xbin/bus" can't
// collide (xbin is reserved, so the "/" form is unreachable).
var ReservedTop = map[string]bool{
	".xbin": true, "vendor": true, "data": true, "home": true, "homes": true, "xbin": true,
	"ingress": true, "runtime": true, "owner": true,
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

// MainDeployment is the deployment every tile has: the one a tile without a
// deployment record runs, and the one storage keys leave unnamed.
const MainDeployment = "main"

// DeploymentNameOK reports whether s is a tile deployment name: a lowercase
// letter, then up to 23 lowercase letters, digits or '-' (the glossary
// grammar, ^[a-z][a-z0-9-]{0,23}$). A name never holds '+' or '/', so a tile
// ref "<tile>+<name>" splits one way only.
func DeploymentNameOK(s string) bool {
	if len(s) == 0 || len(s) > 24 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// TileKey is the 128-bit key of a tile path that names the stores tile
// deployments add (data/deployments, data/checkpoints, .xbin/deploy): 32
// lowercase hex digits of SHA-256("xbin-tile-key-v1" ‖ 0x00 ‖ path). Unlike
// CompKey's 32 bits it can't be ground into a collision; existing stores keep
// their CompKey and ScopeKey names. Each new store also records the full path
// and refuses a load whose path differs.
func TileKey(path string) string {
	h := sha256.Sum256([]byte("xbin-tile-key-v1\x00" + path))
	return hex.EncodeToString(h[:16])
}

// ErrNoDeployment is the "unknown deployment" condition (a 404 on the wire).
// NoDeployment wraps it with the tile and the name.
var ErrNoDeployment = errors.New("no such deployment")

// NoDeployment is the error for a tile that has no deployment called name:
// `<tile> has no deployment "<name>"`. errors.Is matches ErrNoDeployment.
func NoDeployment(tile, name string) error { return noDeployment{tile, name} }

type noDeployment struct{ tile, name string }

func (e noDeployment) Error() string        { return e.tile + " has no deployment " + strconv.Quote(e.name) }
func (e noDeployment) Is(target error) bool { return target == ErrNoDeployment }

// PlusNameRefusal is the name rule every NEW tile meets, whoever creates it,
// admins included, on every creation path (P17): no '+' in any segment of
// its path, since "<tile>+<name>" is a tile deployment's URL. "" = allowed.
// A directory whose name already holds '+' keeps resolving (an exact match
// wins) but can't get deployments.
func PlusNameRefusal(path string) string {
	path = strings.Trim(path, "/")
	if !strings.Contains(path, "+") {
		return ""
	}
	return "can't create " + path + `: '+' isn't allowed in tile names (it names a tile deployment in URLs, /c/<tile>+<name>/) — pick another path`
}

// QueryRefMsg is the 400 of a query parameter that names a tile by a
// qualified ref (P17).
const QueryRefMsg = "a deployment is named with deployment=, not tile+name (a '+' in a query string reads as a space)"

// QueryTileQualified reports whether v, a query parameter that names a tile
// (tile=, component=), is a qualified ref instead of a tile's path (P17): a
// '+' in it that doesn't name a tile (isTile), or a space the client's
// unescaped '+' decoded to, splitting it into a tile and a deployment name.
// A query string never carries the qualifier: its callers answer 400
// QueryRefMsg, and a tile whose own name holds '+' passes (isTile).
func QueryTileQualified(v string, isTile func(string) bool) bool {
	if !strings.ContainsAny(v, "+ ") || isTile(v) {
		return false
	}
	if strings.Contains(v, "+") {
		return true
	}
	i := strings.LastIndexByte(v, ' ')
	return i > 0 && DeploymentNameOK(v[i+1:]) && isTile(v[:i])
}
