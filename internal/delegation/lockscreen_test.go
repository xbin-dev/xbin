package delegation

import (
	"strings"
	"testing"
)

// The lock screen's rule (D241): only the plainest call, shown whole.
func TestLockScreenOffer(t *testing.T) {
	base := Request{Kind: KindCall, State: Pending, Tile: "apps/notes", Method: "POST", Target: "/api/apps/notes/note",
		ContentType: "application/json", Body: `{"text":"hi"}`, BodyShown: true}
	base.BodySize = len(base.Body)
	if ok, why := LockScreenOffer(base); !ok {
		t.Fatalf("the plain call: %s", why)
	}
	for name, mod := range map[string]func(*Request){
		"a session start":     func(r *Request) { r.Kind = KindSession },
		"a create":            func(r *Request) { r.Kind = KindTemplate },
		"a web page":          func(r *Request) { r.Kind = KindWeb },
		"an MCP tool":         func(r *Request) { r.Kind = KindMCP },
		"decided":             func(r *Request) { r.State = Approved },
		"no tile":             func(r *Request) { r.Tile = "" },
		"a GET":               func(r *Request) { r.Method = "GET" },
		"binary":              func(r *Request) { r.BodyBinary = true },
		"not all there":       func(r *Request) { r.BodySize = 9999 },
		"a long path":         func(r *Request) { r.Target = "/api/apps/notes/" + strings.Repeat("p", 120) },
		"a long body":         func(r *Request) { r.Body = strings.Repeat("b", 201); r.BodySize = len(r.Body) },
		"four lines":          func(r *Request) { r.Body = "a\nb\nc\nd"; r.BodySize = len(r.Body) },
		"a bidi override":     func(r *Request) { r.Body = "pay ‮evil"; r.BodySize = len(r.Body) },
		"a zero-width space":  func(r *Request) { r.Target = "/api/apps/notes/a​b" },
		"a carriage return":   func(r *Request) { r.Body = "a\r\nb"; r.BodySize = len(r.Body) },
		"a form content type": func(r *Request) { r.ContentType = "application/x-www-form-urlencoded" },
		"after a web page":    func(r *Request) { r.Preview = map[string]any{"readWeb": true} },
		"a warning":           func(r *Request) { r.Preview = map[string]any{"warnings": []any{"PUBLIC"}} },
		"a binary preview":    func(r *Request) { r.Preview = map[string]any{"body": map[string]any{"binary": true}} },
	} {
		r := base
		mod(&r)
		if ok, _ := LockScreenOffer(r); ok {
			t.Errorf("%s: offered Allow once", name)
		}
	}
	empty := base
	empty.Body, empty.BodySize, empty.ContentType = "", 0, ""
	if ok, why := LockScreenOffer(empty); !ok {
		t.Fatalf("no body: %s", why)
	}
	three := base // a one-line call and three short lines: the four a collapsed notification shows
	three.Target = "/api/apps/n/x"
	three.Body = "a\nb\nc"
	three.BodySize = len(three.Body)
	if ok, why := LockScreenOffer(three); !ok {
		t.Fatalf("three lines: %s", why)
	}
}
