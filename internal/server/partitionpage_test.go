package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers PD-44 01§2.4 — while a tile's partition mode switch is pending,
// its documents (the directory, an .html file, a frame's load, the native
// runtime document) are xbind's own page — 409, no-store, a sandbox CSP
// without scripts — naming the switch (R → Q), that all data will be
// deleted, the tile's partitionNote escaped, and who decides where; the
// tile's other files, other tiles and a reader-less caller (the D36 page)
// are as before, and the page goes when the request is decided. The page
// is self-contained: no script, no external reference of any kind, so it
// renders the same inside any shell, old ones and the app included.
func TestPartitionSwitchPage(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens} {
		t.Run(mode, func(t *testing.T) {
			w := newAssetWS(t, mode)
			note := `Your notes live here. <script>alert("x")</script> & "all" of them`
			p := filepath.Join(w.root, "apps", "a", "xbin.json")
			if err := os.WriteFile(p, []byte(`{"partition":["user"],"partitionNote":`+jsonString(note)+`}`), 0o644); err != nil {
				t.Fatal(err)
			}
			pending := registry.PartitionMode{State: registry.PartitionPending,
				Request: &registry.PartitionRequest{Spec: &registry.PartitionSpec{User: true}}}
			mode := &pending
			w.s.Reg.PartitionModes = func(a registry.PartitionAsk) registry.PartitionMode {
				if a.Tile == "apps/a" {
					return *mode
				}
				return registry.PartitionMode{}
			}
			if err := w.s.Reg.Rescan(); err != nil {
				t.Fatal(err)
			}
			ana := w.session("ana")
			for _, u := range []string{"/c/apps/a/", "/c/apps/a/sub/page.html", "/c/apps/a/?native=1", "/c/apps/a"} {
				rec := w.do(u, ana, hdr("Sec-Fetch-Dest", "iframe"))
				body := rec.Body.String()
				if rec.Code != 409 || !strings.Contains(body, "Partition mode switch requested") {
					t.Fatalf("%s: %d, want the switch page:\n%s", u, rec.Code, body)
				}
				for _, want := range []string{
					"A partition mode switch is requested for <code>apps/a</code> (<b>unpartitioned</b> → <b>user</b>)",
					"<strong>All data in this tile will be deleted for the switch to happen.</strong>",
					`<code>apps/a</code> says: Your notes live here. &lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt; &amp; &quot;all&quot; of them`,
					"Until a manager decides, <code>apps/a</code> doesn't run.",
					"a workspace admin", "<code>http://xbin.localhost:9260/xbin/partitions</code>",
					"<code>bx partition switch apps/a</code>", "<code>bx partition keep apps/a</code>",
				} {
					if !strings.Contains(body, want) {
						t.Errorf("%s: the page lacks %q:\n%s", u, want, body)
					}
				}
				if h := rec.Header(); h.Get("Cache-Control") != "no-store" || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") ||
					!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("X-Content-Type-Options") != "nosniff" {
					t.Errorf("%s: headers %v", u, h)
				}
				// self-contained: nothing loads, nothing runs
				if bad := regexp.MustCompile(`(?i)<script|<link|<img|<iframe|src=|href=|@import|url\(|<form|\son[a-z]+=`).FindString(body); bad != "" {
					t.Errorf("%s: the page references or runs something (%q)", u, bad)
				}
				if strings.Contains(body, "data-xbin") || strings.Contains(body, "xbin-client") {
					t.Errorf("%s: the page was injected", u)
				}
			}
			// a subresource is served as before; other tiles too
			if rec := w.do("/c/apps/a/app.js", append([]reqOpt{ana}, hdr("Sec-Fetch-Dest", "script"))...); rec.Code != 200 || strings.Contains(rec.Body.String(), "Partition mode") {
				t.Errorf("a subresource of a pending tile: %d", rec.Code)
			}
			if rec := w.do("/c/apps/b/", ana, hdr("Sec-Fetch-Dest", "iframe")); rec.Code != 200 || strings.Contains(rec.Body.String(), "Partition mode") {
				t.Errorf("another tile: %d", rec.Code)
			}
			// someone who can't read the tile gets today's answer, not the page
			if rec := w.do("/c/apps/a/", w.session("bob"), hdr("Sec-Fetch-Dest", "iframe")); rec.Code == 409 || strings.Contains(rec.Body.String(), "partitionNote") || strings.Contains(rec.Body.String(), "notes live here") {
				t.Errorf("a non-reader: %d %s", rec.Code, rec.Body.String())
			}
			// decided: the tile's own document again
			mode = &registry.PartitionMode{State: registry.PartitionUnpartitioned, Request: &registry.PartitionRequest{Spec: &registry.PartitionSpec{User: true}, Declined: true}}
			if err := w.s.Reg.Rescan(); err != nil {
				t.Fatal(err)
			}
			if rec := w.do("/c/apps/a/", ana, hdr("Sec-Fetch-Dest", "iframe")); rec.Code != 200 || strings.Contains(rec.Body.String(), "Partition mode") {
				t.Errorf("a declined tile: %d", rec.Code)
			}
		})
	}
}

func jsonString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
