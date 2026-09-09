// Package docscheck keeps the prose honest: every decision id cited anywhere
// resolves to an entry in the decision log, every relative link in docs/ and
// plans/ resolves to a file, and every design record in plans/ says whether
// it is live, implemented, superseded or historical. It is a test so that
// `make test` (and CI) catches drift instead of a reader finding a 404.
package docscheck

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var repo = filepath.Join("..", "..")

// Decision ids: core D-numbers and the per-domain series. A trailing letter
// (D17a, D13b) names a labelled sub-point inside the base entry.
var (
	idCite = regexp.MustCompile(`\b(D|ND|ING|LC|IFACE|VD|RT|ISO|BU|CM|PR)-?(\d+)([a-z]?)\b`)
	// A definition is a bullet or heading that opens with the bold id:
	//   - **D57 — …**      - **IFACE-1** —      ### ND1 — …
	idDef = regexp.MustCompile(`(?m)^(?:\s*- \*\*|#+ )((?:D|ND|ING|LC|IFACE|VD|RT|ISO|BU|CM|PR)-?\d+)\b`)
	// [text](target) links; targets that are URLs, anchors or absolute /docs/
	// paths are checked separately.
	mdLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	// The status line every design record carries near the top.
	statusLine = regexp.MustCompile(`(?im)^>?\s*\**status:?\**\s*:?\s*\**\s*(live|implemented|superseded|historical|IMPLEMENTED)`)
)

func readTree(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(repo, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", "node_modules", "deps", "data", ".git":
					return fs.SkipDir
				}
				return nil
			}
			switch filepath.Ext(p) {
			case ".md", ".go", ".js", ".html", ".json", ".css":
			default:
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repo, p)
			out[filepath.ToSlash(rel)] = string(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestDecisionIDsResolve(t *testing.T) {
	plans := readTree(t, "plans")
	defined := map[string]bool{}
	for _, text := range plans {
		for _, m := range idDef.FindAllStringSubmatch(text, -1) {
			defined[m[1]] = true
		}
	}
	if len(defined) < 80 {
		t.Fatalf("found only %d decision definitions under plans/ — did the format change?", len(defined))
	}
	cited := readTree(t, "docs", "plans", "internal", "web", "workspace-template", "cmd", "sdk", "builtin-tiles", "builtin-templates")
	var problems []string
	seen := map[string]bool{}
	for file, text := range cited {
		for i, line := range strings.Split(text, "\n") {
			for _, m := range idCite.FindAllStringSubmatch(line, -1) {
				series, num := m[1], m[2]
				id := series + num
				if series != "D" && series != "ND" {
					id = series + "-" + num
				}
				if strings.Contains(m[0], "-") && (series == "D" || series == "ND") {
					continue // "D-numbers", "ND-8"-style prose, not an id
				}
				if defined[id] {
					continue
				}
				key := id + " " + file
				if seen[key] {
					continue
				}
				seen[key] = true
				problems = append(problems, fmt.Sprintf("%s:%d: cites %s%s, which no plans/*.md defines (add the entry to plans/DECISIONS.md or fix the id)", file, i+1, id, m[3]))
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

func TestRelativeLinksResolve(t *testing.T) {
	files := readTree(t, "docs", "plans")
	var problems []string
	for file, text := range files {
		if !strings.HasSuffix(file, ".md") {
			continue
		}
		for i, line := range strings.Split(text, "\n") {
			for _, m := range mdLink.FindAllStringSubmatch(line, -1) {
				target := m[1]
				if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				target = strings.SplitN(target, "#", 2)[0]
				target = strings.SplitN(target, "?", 2)[0]
				if target == "" {
					continue
				}
				var full string
				if strings.HasPrefix(target, "/docs/") {
					full = filepath.Join(repo, filepath.FromSlash(strings.TrimPrefix(target, "/")))
				} else if strings.HasPrefix(target, "/") {
					continue // a served route (/c/…, /api/…), not a file
				} else {
					full = filepath.Join(repo, filepath.FromSlash(path.Join(path.Dir(file), target)))
				}
				if st, err := os.Stat(full); err != nil {
					problems = append(problems, fmt.Sprintf("%s:%d: link target %q does not exist", file, i+1, m[1]))
				} else if st.IsDir() && !strings.HasSuffix(target, "/") {
					problems = append(problems, fmt.Sprintf("%s:%d: link target %q is a directory", file, i+1, m[1]))
				}
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

func TestPlansCarryStatus(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(repo, "plans"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repo, "plans", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		head := strings.Join(strings.SplitN(string(b), "\n", 12)[:min(12, strings.Count(string(b), "\n")+1)], "\n")
		if !statusLine.MatchString(head) {
			t.Errorf("plans/%s: no status line in the first 12 lines — add `> Status: live | implemented | superseded | historical`", e.Name())
		}
	}
}
