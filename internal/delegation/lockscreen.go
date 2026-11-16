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
	}
	switch ct := strings.ToLower(strings.TrimSpace(strings.SplitN(r.ContentType, ";", 2)[0])); ct {
	case "", "application/json", "text/plain":
	default:
		return false, "its content type is reviewed in the app"
	}
	if r.Preview != nil {
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

// HidesText reports a string holding a character the approval chrome draws
// as its escape (agent-chrome-words.js reveal, DelegationWords.reveal):
// a control character but newline and tab, or a format character (bidi
// embeddings, overrides and isolates, the zero-width ones, the word joiner,
// the byte order mark — Unicode category Cf). Invalid UTF-8 counts.
func HidesText(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, c := range s {
		switch {
		case c == '\n' || c == '\t':
		case c < 0x20 || c == 0x7f || (c >= 0x80 && c < 0xa0):
			return true
		case unicode.Is(unicode.Cf, c):
			return true
		}
	}
	return false
}
