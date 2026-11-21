package delegation

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The lock screen's rule (plans/wsagent/ios-stage2.md §3.3, D241): when a
// request's notification may offer Allow once — an approval given from the
// lock screen, after device authentication, without the app's sheet. It
// may only where the whole call is shown in the notification as iOS draws
// it collapsed (a line for the call, three for its body), so the bounds are
// small on purpose, and only for the plainest shape: one call to a tile's
// API. Everything else — a tile agent's start, a tile's creation, a web
// page, an MCP tool, any standing scope — is reviewed in the app.
//
// The push names the result (`offer: "once"`), the app checks the same
// fields again itself, and a decision sent `via: "notification"` is held
// to it once more when it arrives (delegateapi.go).

// The clip rule's bounds.
const (
	LockScreenTarget    = 120 // bytes of the call's path (under /api/, with its query)
	LockScreenBody      = 200 // bytes of its body
	LockScreenBodyLines = 3
)

// LockScreenOffer reports whether r's notification may offer Allow once,
// and if not, why (for tests and logs).
func LockScreenOffer(r Request) (bool, string) {
	switch {
	case r.Kind != KindCall:
		return false, "only a tile API call is allowed from the lock screen"
	case r.State != Pending:
		return false, "not pending"
	case r.Tile == "":
		return false, "the call names no tile"
	case r.Method == "" || r.Method == "GET" || r.Method == "HEAD":
		return false, "not a call that changes something"
	case r.BodyBinary || !r.BodyShown:
		return false, "its body can't be shown"
	case r.BodySize != len(r.Body):
		return false, "its body isn't all there to show"
	case len(r.Target) > LockScreenTarget:
		return false, "the path is too long for a notification"
	case len(r.Body) > LockScreenBody:
		return false, "the body is too long for a notification"
	case strings.Count(r.Body, "\n") >= LockScreenBodyLines:
		return false, "the body has too many lines for a notification"
	case strings.ContainsAny(r.Target, "\r\n\t"):
		return false, "the path holds a line break"
	case HidesText(r.Target) || HidesText(r.Body):
		return false, "it holds characters that hide or reorder text"
	case !LockScreenFits(r.Method, r.Target, r.Body):
		return false, "the notification would not show all of it"
	}
	switch ct := strings.ToLower(strings.TrimSpace(strings.SplitN(r.ContentType, ";", 2)[0])); ct {
	case "", "application/json", "text/plain":
	default:
		return false, "its content type is reviewed in the app"
	}
	if r.Preview != nil {
		// a tile call's preview, as xbind writes one (delegateapi.go): anything
		// else it says — an outside server, a host, a sign-in, a waiting line, a
		// shape that came later — is reviewed in the app
		for k := range r.Preview {
			if !lockScreenPreviewKeys[k] {
				return false, "its preview says more than a tile call's: reviewed in the app"
			}
		}
		if c, _ := r.Preview["class"].(string); c != "" && !strings.HasPrefix(c, "tile-") {
			return false, "not a write to a tile's API"
		}
		if web, _ := r.Preview["readWeb"].(bool); web {
			return false, "the conversation read a web page: what it asks may come from it"
		}
		if _, binary := r.Preview["body"]; binary {
			return false, "its body can't be shown"
		}
		if w, ok := r.Preview["warnings"].([]any); ok && len(w) > 0 {
			return false, "it carries a warning"
		}
	}
	return true, ""
}

// lockScreenPreviewKeys are what a tile call's preview holds: anything else
// refuses the offer.
var lockScreenPreviewKeys = map[string]bool{"class": true, "reversible": true, "tile": true, "owner": true, "level": true,
	"role": true, "standing": true, "readWeb": true, "warnings": true}

// HidesText reports a string holding a character the approval chrome draws
// as its escape (agent-chrome-words.js reveal, DelegationWords.reveal):
// a control character but newline and tab, or a format character (bidi
// embeddings, overrides and isolates, the zero-width ones, the word joiner,
// the byte order mark — Unicode category Cf), or a line or paragraph
// separator (U+2028, U+2029). Invalid UTF-8 counts.
func HidesText(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, c := range s {
		switch {
		case c == '\n' || c == '\t':
		case c < 0x20 || c == 0x7f || (c >= 0x80 && c < 0xa0):
			return true
		case unicode.Is(unicode.Cf, c), c == 0x2028 || c == 0x2029: // a format character; a line or paragraph separator (D252)
			return true
		}
	}
	return false
}

// ---- what the collapsed notification certainly shows (review finding 1) ----
//
// iOS draws the notification's body — the call line, then the body, or
// "(no body)" — in at most LockScreenLines lines collapsed, wrapping at
// word ends and breaking a word longer than a line. A body is offered only
// where the whole of it certainly shows there, at the largest default
// Dynamic Type on the narrowest iPhone: about 281 pt of text at 23 pt, so
// LockScreenLineWidth is 12 em, and every character's advance is counted
// at or above SF Pro's (lockScreenAdvance). Only characters whose width
// and direction are known are counted at all — printable Latin, a few
// typographic marks — so any other (a line or paragraph separator iOS
// draws as a break, a tab, a space but U+0020, a right-to-left letter, a
// wide, combining or emoji character) and any run of two spaces refuse the
// offer. The app runs the same function (XbinCore LockScreenFit), held to
// this one by shared vectors (lockscreen-fit.json).

const (
	LockScreenLines     = 4    // lines of body a collapsed notification shows
	LockScreenLineWidth = 1200 // hundredths of an em in a line
)

// LockScreenFits reports whether the body the app draws for this call —
// "<method> <target>", a line break, then body or "(no body)" — certainly
// shows whole in the collapsed notification.
func LockScreenFits(method, target, body string) bool {
	if strings.Contains(target, "\n") || !lockScreenCountable(method) || !lockScreenCountable(target) || !lockScreenCountable(body) {
		return false
	}
	if body == "" {
		body = "(no body)"
	}
	lines := LockScreenRenderedLines(method + " " + target)
	for _, l := range strings.Split(body, "\n") {
		lines += LockScreenRenderedLines(l)
		if lines > LockScreenLines {
			return false
		}
	}
	return lines <= LockScreenLines
}

// lockScreenCountable: every character's width is known (lockScreenAdvance
// > 0) or it is a line feed, and no two spaces stand together.
func lockScreenCountable(s string) bool {
	if !utf8.ValidString(s) || strings.Contains(s, "  ") {
		return false
	}
	for _, c := range s {
		if c != '\n' && lockScreenAdvance(c) == 0 {
			return false
		}
	}
	return true
}

// LockScreenRenderedLines is how many lines one line of text (no line
// feed) takes at LockScreenLineWidth: greedy, at spaces; a word wider than
// a line is broken between its characters.
func LockScreenRenderedLines(s string) int {
	space := lockScreenAdvance(' ')
	lines, cur, started := 1, 0, false
	for _, w := range strings.Split(s, " ") {
		ww := 0
		for _, c := range w {
			ww += lockScreenAdvance(c)
		}
		if started {
			if cur+space+ww <= LockScreenLineWidth {
				cur += space + ww
				continue
			}
			lines, cur = lines+1, 0
		}
		started = true
		if ww <= LockScreenLineWidth {
			cur = ww
			continue
		}
		for _, c := range w {
			cw := lockScreenAdvance(c)
			if cur+cw > LockScreenLineWidth {
				lines, cur = lines+1, cw
			} else {
				cur += cw
			}
		}
	}
	return lines
}

// lockScreenAdvance is a character's advance in hundredths of an em, at or
// above SF Pro Text's; 0 for a character whose width or direction isn't
// known here (it refuses the offer).
func lockScreenAdvance(c rune) int {
	switch {
	case c == ' ':
		return 30
	case strings.ContainsRune("',.:;ijl|!", c):
		return 30
	case strings.ContainsRune("frt\"()[]{}/\\`-I", c):
		return 45
	case c == 'm' || c == 'w' || c == 'M' || c == 'W' || c == '@' || c == '%' || c == '&':
		return 100
	case c >= 'A' && c <= 'Z':
		return 80
	case c > ' ' && c < 0x7f:
		return 65
	case c >= 0xa1 && c <= 0x24f && c != 0xad:
		return 100 // Latin-1 and Latin Extended letters and signs (not the soft hyphen)
	}
	switch c {
	case 0x2013, 0x2014, 0x2018, 0x2019, 0x201c, 0x201d, 0x2026, 0x20ac: // – — ‘ ’ “ ” … €
		return 100
	}
	return 0
}
