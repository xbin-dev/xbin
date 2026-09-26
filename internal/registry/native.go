package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// NativeConvention is the native-app UI entry a tile opts in with by simply
// shipping the file next to its xbin.json (docs/elements.md §Native app UI).
const NativeConvention = "native.js"

// NativeOpt is a manifest's "native" key: the tile's native-app UI entry — a
// tile-relative module path ("./mobile/main.js") — or false to opt out of
// the native.js convention. Old xbinds ignore the key; this one never turns
// it into a manifest error either: any other value is ignored (Invalid says
// why), so a tile carrying "native" for reasons of its own keeps loading.
type NativeOpt struct {
	Entry   string // the declared path, as written ("" = the convention)
	Off     bool   // "native": false
	Invalid string // why a declared value was ignored
}

// UnmarshalJSON accepts a path string or a boolean (false opts out, true is
// the convention); anything else is recorded in Invalid, never an error.
func (n *NativeOpt) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*n = NativeOpt{Entry: s}
		return nil
	}
	var on bool
	if json.Unmarshal(b, &on) == nil {
		*n = NativeOpt{Off: !on}
		return nil
	}
	*n = NativeOpt{Invalid: `"native" must be a module path (e.g. "./mobile/main.js") or false; ignored`}
	return nil
}

// nativeSeg is one path segment a native entry may use: URL- and
// JS-string-safe characters only, since the runtime document imports it by
// relative URL. Dot-segments and hidden names are refused separately.
var nativeSeg = regexp.MustCompile(`^[A-Za-z0-9_~+@-][A-Za-z0-9._~+@-]*$`)

// CleanNativeEntry normalises a declared native entry to a clean
// tile-relative path ("./mobile/main.js" → "mobile/main.js"), or explains
// why it can't be one: absolute, escaping the tile, hidden segments, odd
// characters, or not a .js/.mjs module.
func CleanNativeEntry(decl string) (string, error) {
	p := strings.TrimSpace(decl)
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	switch {
	case p == "":
		return "", fmt.Errorf("native entry %q: empty path", decl)
	case strings.HasPrefix(p, "/"), strings.Contains(p, `\`):
		return "", fmt.Errorf("native entry %q: must be a path relative to the tile", decl)
	case !strings.HasSuffix(p, ".js") && !strings.HasSuffix(p, ".mjs"):
		return "", fmt.Errorf("native entry %q: must be a .js or .mjs module", decl)
	}
	for _, seg := range strings.Split(p, "/") {
		if !nativeSeg.MatchString(seg) {
			return "", fmt.Errorf("native entry %q: segment %q not allowed (letters, digits, . _ ~ + @ -; no dot-segments or hidden names)", decl, seg)
		}
	}
	return p, nil
}

// NativeEntryName is the tile-relative file the component's native UI
// entry is — declared, or the convention — whether or not it exists; ""
// when the manifest opts out or declares an unusable path. The watcher
// keys on the name (a deleted entry is still an entry edit).
func (c *Component) NativeEntryName() string {
	n := c.Manifest.Native
	if n != nil && n.Off {
		return ""
	}
	if !c.nativeDeclared() {
		return NativeConvention
	}
	p, err := CleanNativeEntry(n.Entry)
	if err != nil {
		return ""
	}
	return p
}

// nativeDeclared reports whether the manifest names the entry explicitly
// (as opposed to the native.js convention).
func (c *Component) nativeDeclared() bool {
	n := c.Manifest.Native
	return n != nil && n.Invalid == "" && !n.Off && n.Entry != ""
}

// NativeOnlyChange reports whether a change to rel (component-relative)
// touches only the component's native UI entry — which no backend reads, so
// the change reloads the tile's views without restarting its backend.
// Conservative where a backend might read the file after all: an entry that
// is only the native.js convention (not declared) under a node backend (it
// could import it) or a go backend built from the tile root (it could embed
// it) still restarts, as every file edit always has.
func (c *Component) NativeOnlyChange(rel string) bool {
	name := c.NativeEntryName()
	if name == "" || path.Clean(rel) != name || name == c.backendEntryFile() {
		return false
	}
	if !c.nativeDeclared() {
		switch c.Manifest.Runtime {
		case "node":
			return false
		case "go":
			if e := path.Clean(strings.TrimPrefix(c.Manifest.Entry, "./")); c.Manifest.Entry != "" && (e == "." || e == "") {
				return false
			}
		}
	}
	return true
}

// backendEntryFile is the backend entry as a clean tile-relative path, when
// the manifest names one ("" otherwise) — never also a native UI entry.
func (c *Component) backendEntryFile() string {
	if c.Manifest.Entry == "" {
		return ""
	}
	return path.Clean(strings.TrimPrefix(c.Manifest.Entry, "./"))
}

// resolveNative decides the component's native UI entry at scan time: the
// declared file if the manifest names a usable one that exists, else the
// native.js convention when that file exists (unless the manifest opts out
// or the file is the backend's own entry). problem explains a declared
// entry that could not be used — surfaced, never a manifest error.
func (c *Component) resolveNative() (entry, problem string) {
	n := c.Manifest.Native
	if n != nil && n.Invalid != "" {
		problem = n.Invalid
	}
	if c.nativeDeclared() {
		p, err := CleanNativeEntry(n.Entry)
		switch {
		case err != nil:
			return "", err.Error()
		case p == c.backendEntryFile():
			return "", fmt.Sprintf("native entry %q is the backend entry", n.Entry)
		case !regularFile(filepath.Join(c.Dir, filepath.FromSlash(p))):
			return "", fmt.Sprintf("native entry %q: no such file", n.Entry)
		}
		return p, ""
	}
	name := c.NativeEntryName()
	if name == "" || name == c.backendEntryFile() || !regularFile(filepath.Join(c.Dir, name)) {
		return "", problem
	}
	return name, problem
}

func regularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
