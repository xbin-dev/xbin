package xbin

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The product's copies of xbin's mark are the brand's masters (D183), as
// the site's are (make website-guard): /vendor/favicon.svg is the hinted
// favicon and /vendor/logo.svg the lockup with wordmark A, byte for byte.
// The PNGs beside them (favicon.png, favicon-256.png, favicon-32.png,
// apple-touch-icon.png, logo.png) are rendered from the same masters
// (docs/maintenance.md, "Embedded assets"); a drawing of the mark inlined
// elsewhere is checked against logo.svg's paths where it lives.
func TestProductMarksAreTheMasters(t *testing.T) {
	for web, master := range map[string]string{"favicon.svg": "favicon.svg", "logo.svg": "lockup-a.svg"} {
		got, err := fs.ReadFile(WebFS(), web)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("plans", "brand", "marks", master))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("web/%s is not plans/brand/marks/%s: copy the master, never redraw it (D183)", web, master)
		}
	}
}

// The embedded trees ship inside every xbind byte-for-byte and are copied
// into workspaces (`xbind init`, `bx tile import`, template instantiation),
// so their contents are a release decision, not a build accident. Guards:
//
//   - no compiled binaries: a stray `go build` in builtin-tiles/devbox (since
//     retired) once rode along in every xbind (+10 MB) and would have been
//     copied by `bx tile import`;
//   - nothing large outside web/vendor/ — the vendored frontend deps are the
//     only big files by design;
//   - no nested repos / dependency trees (`all:` embeds dotfiles too);
//   - no pointers into plans/: the design records are not embedded, so a
//     `plans/x.md` citation in a served doc, the scaffolded AGENTS.md or an
//     imported tile's comments is a dead path for the reader. Cite the docs
//     and decision IDs instead (docs/maintenance.md, "Embedded assets").
func TestEmbeddedAssets(t *testing.T) {
	const maxSize = 512 << 10
	plansRef := regexp.MustCompile(`(^|[^A-Za-z0-9_./-])plans/`)
	var problems []string
	files := 0
	err := fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, seg := range strings.Split(p, "/") {
			switch seg {
			case ".git", "node_modules", ".claude", "deps":
				problems = append(problems, fmt.Sprintf("%s: %q must not be embedded", p, seg))
				return fs.SkipDir
			}
		}
		if d.IsDir() {
			return nil
		}
		files++
		data, err := fs.ReadFile(assets, p)
		if err != nil {
			return err
		}
		if len(data) >= 4 && data[0] == 0x7f && string(data[1:4]) == "ELF" {
			problems = append(problems, fmt.Sprintf("%s: compiled binary (%d bytes) in an embedded tree — delete it; xbind builds backends into .xbin/build/", p, len(data)))
			return nil
		}
		if len(data) > maxSize && !strings.HasPrefix(p, "web/vendor/") {
			problems = append(problems, fmt.Sprintf("%s: %d bytes exceeds the %d-byte cap for embedded files (only web/vendor/ may be large)", p, len(data), maxSize))
		}
		if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
			return nil // binary asset (png, woff…): no text checks
		}
		if p == "docs/maintenance.md" {
			return nil // the page that states this rule has to name the pattern
		}
		for i, line := range bytes.Split(data, []byte("\n")) {
			if plansRef.Match(line) {
				problems = append(problems, fmt.Sprintf("%s:%d: cites plans/ — not embedded; point at docs/ or a decision ID instead", p, i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 100 {
		t.Fatalf("walked only %d embedded files — is the embed directive intact?", files)
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
