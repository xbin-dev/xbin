package delegation

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lock screen's rule in rendered terms (D241, review finding 1 and 6):
// a body whose tail iOS would not draw in the collapsed notification never
// gets Allow once — line separators iOS draws as breaks, runs of spaces
// that push the tail out of sight, sizes that wrap past the lines shown,
// characters whose width or direction can't be counted.
func TestLockScreenOfferRefusesWhatIsNotSeen(t *testing.T) {
	base := Request{Kind: KindCall, State: Pending, Tile: "apps/note", Method: "PUT", Target: "/api/apps/note/note",
		ContentType: "application/json", Body: `{"note":"ok"}`, BodyShown: true}
	base.BodySize = len(base.Body)
	if ok, why := LockScreenOffer(base); !ok {
		t.Fatalf("the plain call: %s", why)
	}
	for name, body := range map[string]string{
		"line separators":         `{"note":"ok` + strings.Repeat(" ", 5) + `","all":true}`,
		"paragraph separators":    `{"note":"ok` + strings.Repeat(" ", 5) + `","all":true}`,
		"next line":               "{\"note\":\"ok\u0085\u0085\u0085\u0085\",\"all\":true}",
		"vertical tab":            "{\"note\":\"ok\v\v\v\v\",\"all\":true}",
		"form feed":               "{\"note\":\"ok\f\f\f\f\",\"all\":true}",
		"a carriage return":       "{\"note\":\"ok\r\r\r\r\",\"all\":true}",
		"a tab":                   "{\"note\":\"ok\t\",\"all\":true}",
		"spaces pushing the tail": `{"note":"ok"` + strings.Repeat(" ", 150) + `,"all":true}`,
		"two spaces":              `{"note":"ok  then"}`,
		"a no-break space":        "{\"note\":\"ok then\"}",
		"an ideographic space":    "{\"note\":\"ok　then\"}",
		"right-to-left letters":   `["א",1,2,"ב"]`,
		"arabic":                  `{"note":"مرحبا"}`,
		"wide characters":         `{"note":"請款單"}`,
		"an emoji":                `{"note":"ok 👍"}`,
		"a combining mark":        "{\"note\":\"oḱ\"}",
		"at the byte limits":      `{"note":"` + strings.Repeat("abcdefgh ", 21) + `"}`,
		"four short lines":        "a\nb\nc\nd",
	} {
		r := base
		r.Body, r.BodySize = body, len(body)
		if ok, _ := LockScreenOffer(r); ok {
			t.Errorf("%s: offered Allow once for %q", name, body)
		}
	}
	long := base
	long.Target = "/api/apps/note/" + strings.Repeat("segment/", 13)
	if ok, _ := LockScreenOffer(long); ok {
		t.Errorf("a 120-byte path that wraps past the lines shown: offered")
	}
}

// Generated bodies: a body that fits holds only countable characters and
// no two spaces, and its lines come to at most LockScreenLines; one that
// doesn't never fits with anything appended (the rule is monotone, so
// nothing a request adds at its end can bring an offer back).
func TestLockScreenFitsProperties(t *testing.T) {
	rng := rand.New(rand.NewPCG(41, 241))
	alphabet := []rune("abcxyz ABC MW019{}[]\":,./-_ il \n  \tא́　👍éß–…")
	for i := 0; i < 20000; i++ {
		b := make([]rune, rng.IntN(120))
		for j := range b {
			b[j] = alphabet[rng.IntN(len(alphabet))]
		}
		body := string(b)
		fits := LockScreenFits("PUT", "/api/apps/note/note", body)
		if fits {
			if strings.Contains(body, "  ") || !lockScreenCountable(body) {
				t.Fatalf("fits but not countable: %q", body)
			}
			n := LockScreenRenderedLines("PUT /api/apps/note/note")
			for _, l := range strings.Split(body, "\n") {
				n += LockScreenRenderedLines(l)
			}
			if body != "" && n > LockScreenLines {
				t.Fatalf("fits in %d lines: %q", n, body)
			}
			continue
		}
		for _, c := range alphabet {
			if LockScreenFits("PUT", "/api/apps/note/note", body+string(c)) {
				t.Fatalf("%q doesn't fit, but with %q appended it does", body, c)
			}
		}
	}
}

// lockScreenVectors is what the app's copy of the rule (XbinCore
// LockScreenFit) is held to: these cases and xbind's answer for each.
// XBIN_UPDATE_VECTORS=1 writes the file; otherwise it must match.
func TestLockScreenFitVectors(t *testing.T) {
	type vector struct {
		Method string `json:"method"`
		Target string `json:"target"`
		Body   string `json:"body"`
		Fits   bool   `json:"fits"`
		Lines  int    `json:"lines"`
	}
	var cases []vector
	add := func(m, tg, b string) {
		n := LockScreenRenderedLines(m + " " + tg)
		if b == "" {
			n++
		}
		for _, l := range strings.Split(b, "\n") {
			if b != "" {
				n += LockScreenRenderedLines(l)
			}
		}
		cases = append(cases, vector{m, tg, b, LockScreenFits(m, tg, b), n})
	}
	for _, b := range []string{"", `{"note":"ok"}`, "a\nb\nc", `{"note":"allowed from a notification"}`,
		`{"note":"ok` + strings.Repeat(" ", 5) + `","all":true}`, `{"note":"ok"` + strings.Repeat(" ", 150) + `,"all":true}`,
		`["א",1,2,"ב"]`, `{"note":"請款單"}`, "{\"note\":\"oḱ\"}", `{"note":"Grüße – “so” …"}`,
		strings.Repeat("W", 15), strings.Repeat("W", 12), strings.Repeat("i", 40), strings.Repeat("i", 41), "x \n y"} {
		add("PUT", "/api/apps/note/note", b)
		add("POST", "/api/apps/invoices/links/rebuild?force=1", b)
	}
	add("PUT", "/api/apps/note/"+strings.Repeat("segment/", 13), `{}`)
	rng := rand.New(rand.NewPCG(7, 241))
	alphabet := []rune("abcxyz ABC MW019{}[]\":,./-_ il \n  \tא́éß–…%@&mw")
	for i := 0; i < 400; i++ {
		b := make([]rune, rng.IntN(90))
		for j := range b {
			b[j] = alphabet[rng.IntN(len(alphabet))]
		}
		add("PATCH", "/api/apps/tasks/item?id="+fmt.Sprint(i), string(b))
	}
	got, err := json.MarshalIndent(cases, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "native", "ios", "Packages", "XbinCore", "Tests", "XbinCoreTests", "Resources", "lockscreen-fit.json")
	if os.Getenv("XBIN_UPDATE_VECTORS") == "1" {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (XBIN_UPDATE_VECTORS=1 go test -run TestLockScreenFitVectors ./internal/delegation/ writes it)", err)
	}
	if string(want) != string(got)+"\n" {
		t.Fatalf("%s is not xbind's rule any more: XBIN_UPDATE_VECTORS=1 go test -run TestLockScreenFitVectors ./internal/delegation/", path)
	}
}
