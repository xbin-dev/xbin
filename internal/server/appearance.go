package server

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/prefsfile"
)

// The person's appearance at first paint (D184): the theme and density
// they chose in the shell's settings ride the D4 injection as two metas,
//
//	<meta name="xbin-theme" content="light|dark">     absent: follow the system
//	<meta name="xbin-density" content="comfortable">  absent: compact
//
// which /vendor/theme.css reads with :has() in a document that opted in
// (<html data-bx-theme="auto">), so the first painted frame is already
// right; frames hear later changes as xbin:appearance (docs/protocol.md).
// They are added only when the person chose something other than the
// default: a person who never chose gets byte for byte the injection of
// before. The choice is two keys of the person's shell bucket — `theme`
// ("light" | "dark"; absent or "system": the system's) and `density`
// ("comfortable"; absent: compact) — which only chrome writes (a tile's
// PUT /prefs lands in the tile's own bucket). Any other stored value is
// ignored, and only the two constant strings above are ever written into a
// page.

// appearance is a person's choice: theme "light" | "dark" ("" = the
// system's), density "comfortable" ("" = compact).
type appearance struct{ theme, density string }

// metas is the injection's lines for a: nothing for the defaults.
func (a appearance) metas() string {
	out := ""
	if a.theme != "" {
		out += `<meta name="xbin-theme" content="` + a.theme + "\">\n"
	}
	if a.density != "" {
		out += `<meta name="xbin-density" content="` + a.density + "\">\n"
	}
	return out
}

// appearanceFrom reads the two keys of a shell bucket; anything but the
// values the theme defines reads as the default.
func appearanceFrom(m map[string]json.RawMessage) appearance {
	var a appearance
	var theme, density string
	if json.Unmarshal(m["theme"], &theme) == nil && (theme == "light" || theme == "dark") {
		a.theme = theme
	}
	if json.Unmarshal(m["density"], &density) == nil && density == "comfortable" {
		a.density = density
	}
	return a
}

// appearancePerson is whose appearance a document served to p shows: the
// principal's user — a person's session or device, a frame or terminal
// token they drive, and while an admin views as someone (D64) the viewed
// person, whom the session reads as; prefsfile.Root for the owner token,
// --no-auth and the owner's own frames and terminals (the bucket /prefs
// keeps for them). No one for a tile's backend (backendPrincipal) or a
// credential-less subresource load (the empty principal).
func appearancePerson(p auth.Principal) (string, bool) {
	switch {
	case backendPrincipal(p):
		return "", false
	case p.UserID != "":
		return p.UserID, true
	case p.Owner, p.Via == "frame", p.Via == "terminal":
		return prefsfile.Root, true
	}
	return "", false
}

// appearanceOf is the appearance a document served for r carries.
func (s *Server) appearanceOf(r *http.Request) appearance {
	user, ok := appearancePerson(auth.PrincipalOf(r))
	if !ok || s.Reg == nil {
		return appearance{}
	}
	return bucketAppearance(prefsfile.Path(s.Reg.Root, user, prefsfile.Root))
}

// appearanceMetas is the injection's appearance lines for r.
func (s *Server) appearanceMetas(r *http.Request) string { return s.appearanceOf(r).metas() }

// The shell bucket is read on every document load, so its two keys are
// cached per bucket file by the file's identity, size and modification
// time: a load costs a stat. A write replaces the file (prefsfile.Write
// renames a new one into place), which changes all three.
type appearanceEntry struct {
	fi os.FileInfo
	a  appearance
}

const appearanceCacheMax = 4096 // bucket files: one per person who opened a document

var appearanceCache = struct {
	sync.Mutex
	m map[string]appearanceEntry
}{m: map[string]appearanceEntry{}}

func bucketAppearance(path string) appearance {
	fi, err := os.Stat(path)
	if err != nil {
		return appearance{} // no bucket: the defaults
	}
	appearanceCache.Lock()
	e, ok := appearanceCache.m[path]
	appearanceCache.Unlock()
	if ok && sameFileVersion(e.fi, fi) {
		return e.a
	}
	var a appearance
	if m, err := prefsfile.Read(path); err == nil {
		a = appearanceFrom(m)
	}
	appearanceCache.Lock()
	if len(appearanceCache.m) >= appearanceCacheMax {
		appearanceCache.m = map[string]appearanceEntry{}
	}
	appearanceCache.m[path] = appearanceEntry{fi: fi, a: a}
	appearanceCache.Unlock()
	return a
}

// sameFileVersion: b is the very file a was, unchanged since — the same
// file (device and inode), size and modification time.
func sameFileVersion(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
