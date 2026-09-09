// Package sizebudget is the size ratchet: no source file grows past the
// budget hack/size-budget.txt gives it, no unlisted file passes the global
// cap, and a listed file that shrank well below its budget makes the test
// ask for the number to come down — so the two 3,000-line files that made
// the admin console and the shell hard to work in cannot come back once
// they are split.
package sizebudget

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	goCap = 800 // an unlisted non-test Go file
	jsCap = 900 // an unlisted shipped .js / .mjs / .html
)

var repo = filepath.Join("..", "..")

func loadBudget(t *testing.T) map[string]int {
	t.Helper()
	f, err := os.Open(filepath.Join(repo, "hack", "size-budget.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("hack/size-budget.txt: bad line %q (want: <path> <max-lines>)", line)
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("hack/size-budget.txt: bad number in %q", line)
		}
		out[fields[0]] = n
	}
	return out
}

func countLines(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n := strings.Count(string(b), "\n")
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n, nil
}

func TestSizeBudget(t *testing.T) {
	budget := loadBudget(t)
	seen := map[string]bool{}
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	check := func(rel string, cap int) {
		n, err := countLines(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if max, ok := budget[rel]; ok {
			seen[rel] = true
			switch {
			case n > max:
				add("%s: %d lines, over its budget of %d — split it rather than raising the number (docs/maintenance.md → \"Size budget\")", rel, n, max)
			case n < max*9/10:
				add("%s: %d lines, well under its budget of %d — lower the budget in hack/size-budget.txt to %d so the gain sticks", rel, n, max, n+n/20)
			}
			return
		}
		if n > cap {
			add("%s: %d lines, over the %d-line cap for an unlisted file — split it, or list it in hack/size-budget.txt with a reason", rel, n, cap)
		}
	}
	walk := func(dirs []string, cap int, want func(string) bool) {
		for _, dir := range dirs {
			err := filepath.WalkDir(filepath.Join(repo, dir), func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					switch d.Name() {
					case "vendor", "node_modules", "deps", "data", ".git", "out":
						return fs.SkipDir
					}
					return nil
				}
				rel, _ := filepath.Rel(repo, p)
				rel = filepath.ToSlash(rel)
				if want(rel) {
					check(rel, cap)
				}
				return nil
			})
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
	}
	walk([]string{"cmd", "internal", "sdk"}, goCap, func(rel string) bool {
		return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
	})
	walk([]string{"web", "workspace-template", "builtin-tiles", "builtin-templates", "hack"}, jsCap, func(rel string) bool {
		return strings.HasSuffix(rel, ".js") || strings.HasSuffix(rel, ".mjs") || strings.HasSuffix(rel, ".html")
	})
	for rel := range budget {
		if !seen[rel] {
			add("hack/size-budget.txt lists %s, which does not exist — remove the line", rel)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
