package xbin

import (
	"bytes"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The embedded trees ship inside every xbind byte-for-byte and are copied
// into workspaces (`xbind init`, `bx tile import`, template instantiation),
// so their contents are a release decision, not a build accident. Guards:
//
//   - no compiled binaries: a stray `go build` in builtin-tiles/devbox once
//     rode along in every xbind (+10 MB) and would have been copied by
//     `bx tile import`;
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
