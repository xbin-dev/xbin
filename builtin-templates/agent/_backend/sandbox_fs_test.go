package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// put writes a file into a fake sandbox (its paths are host paths).
func put(t *testing.T, box *sbxSandbox, rel, content string) string {
	t.Helper()
	p := filepath.Join(box.Workdir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func lines(n int, f string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, f+"\n", i)
	}
	return b.String()
}

func TestSandboxRead(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "files", "none")
	read := func(args map[string]any) string { return mustTool(t, ag, r, cfg, "r", "read", args) }
	put(t, box, "ten.txt", lines(10, "line %d"))
	if out := read(map[string]any{"path": "ten.txt"}); !strings.HasPrefix(out, "     1\tline 1\n     2\tline 2\n") || !strings.HasSuffix(out, "    10\tline 10") {
		t.Fatalf("whole: %q", out)
	}
	if out := read(map[string]any{"path": filepath.Join(box.Workdir, "ten.txt"), "offset": 3, "limit": 2}); out != "     3\tline 3\n     4\tline 4\n… [lines 5–10 of 10 not shown — read with offset=5]" {
		t.Fatalf("a range: %q", out)
	}
	if out := read(map[string]any{"path": "ten.txt", "offset": 11}); !strings.Contains(out, "past the end") {
		t.Fatalf("past the end: %q", out)
	}
	put(t, box, "../home/.profile", "export X=1\n")
	if out := read(map[string]any{"path": "~/.profile"}); out != "     1\texport X=1" {
		t.Fatalf("~: %q", out)
	}
	put(t, box, "empty", "")
	if out := read(map[string]any{"path": "empty"}); !strings.Contains(out, "is empty") {
		t.Fatalf("empty: %q", out)
	}
	put(t, box, "blob.bin", "PK\x03\x04\x00\x00binary")
	if out := read(map[string]any{"path": "blob.bin"}); !strings.Contains(out, "is a binary file") {
		t.Fatalf("binary: %q", out)
	}
	put(t, box, "long.txt", strings.Repeat("é", 3000)+"\n")
	if out := read(map[string]any{"path": "long.txt"}); !strings.HasSuffix(out, " …[line clipped]") || len(out) > 2*readLineMax+40 {
		t.Fatalf("a long line: %d bytes", len(out))
	}
	if _, err := tool(t, ag, r, cfg, "r", "read", map[string]any{"path": "."}); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("a directory: %v", err)
	}
	if _, err := tool(t, ag, r, cfg, "r", "read", map[string]any{"path": "nope.txt"}); sbxRefusal(err) != "not-found" {
		t.Fatalf("missing: %v", err)
	}
	// a large file: a range via sed; the budget cuts a long answer
	put(t, box, "big.log", lines(40000, "entry number %06d"))
	out := read(map[string]any{"path": "big.log", "offset": 39990, "limit": 3})
	if !strings.HasPrefix(out, " 39990\tentry number 039990\n 39991\tentry number 039991\n 39992\tentry number 039992\n[") || !strings.Contains(out, "big.log is ") {
		t.Fatalf("a range of a large file: %q", out)
	}
	if out := read(map[string]any{"path": "big.log"}); len(out) > readBudget+200 || !strings.Contains(out, "to keep the result short — read with offset=") {
		t.Fatalf("the budget: %d bytes, %q", len(out), out[len(out)-120:])
	}
	put(t, box, "big.bin", strings.Repeat("\x00\x01", 200<<10))
	if out := read(map[string]any{"path": "big.bin"}); !strings.Contains(out, "is a binary file") {
		t.Fatalf("a large binary: %q", out)
	}
}

func TestSandboxWriteAndEdit(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	m := bindSbx(t, "apps/cs")["apps/cs"]
	r, cfg, box := sbxRun(t, ag, "edits", "none")
	if out := mustTool(t, ag, r, cfg, "w", "write", map[string]any{"path": "a/b/c.go", "content": "package c\n\n// x x x\nfunc A() int { return 1 }\n"}); !strings.HasPrefix(out, "wrote "+box.Workdir+"/a/b/c.go (") {
		t.Fatalf("write: %q", out)
	}
	if _, err := tool(t, ag, r, cfg, "w", "write", map[string]any{"path": "x"}); err == nil || !strings.Contains(err.Error(), "needs content") {
		t.Fatalf("no content: %v", err)
	}
	edit := func(args map[string]any) (string, error) {
		args["path"] = "a/b/c.go"
		return tool(t, ag, r, cfg, "e", "edit", args)
	}
	out, err := edit(map[string]any{"old_string": "return 1", "new_string": "return 2"})
	if err != nil || !strings.HasPrefix(out, "edited "+box.Workdir+"/a/b/c.go (1 replacement)\n     1\tpackage c\n") || !strings.Contains(out, "     3\t// x x x\n     4\tfunc A() int { return 2 }") {
		t.Fatalf("edit: %q %v", out, err)
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"old_string": "return 9", "new_string": "x"}, "old_string not found"},
		{map[string]any{"old_string": "func  A()", "new_string": "x"}, "whitespace differs; read it"},
		{map[string]any{"old_string": "", "new_string": "x"}, "use write to create"},
		{map[string]any{"old_string": "c", "new_string": "c"}, "identical"},
		{map[string]any{"old_string": "x", "new_string": "y"}, "appears 3 times"},
	} {
		if _, err := edit(c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("edit %v: %v, want %q", c.args, err, c.want)
		}
	}
	if out, err := edit(map[string]any{"old_string": "x", "new_string": "y", "replace_all": true}); err != nil || !strings.Contains(out, "(3 replacements)") {
		t.Fatalf("replace_all: %q %v", out, err)
	}
	// a concurrent change between the read and the write: once more, then refused
	puts := func() int { return m.count("PUT", "/sbx/sandboxes/"+box.ID+"/files/content") }
	before := puts()
	m.Fail412(1)
	if _, err := edit(map[string]any{"old_string": "return 2", "new_string": "return 3"}); err != nil || puts()-before != 2 {
		t.Fatalf("one 412 is retried: %v (%d writes)", err, puts()-before)
	}
	m.Fail412(2)
	if _, err := edit(map[string]any{"old_string": "return 3", "new_string": "return 4"}); sbxRefusal(err) != "precondition" || !strings.Contains(err.Error(), "changed since it was read") {
		t.Fatalf("two 412s: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(box.Workdir, "a/b/c.go"))
	if string(got) != "package c\n\n// y y y\nfunc A() int { return 3 }\n" {
		t.Fatalf("the file: %q", got)
	}
	for _, c := range m.Calls() {
		if c.Method == "PUT" && strings.Contains(c.Query, "ifMatch=") == false && strings.Contains(c.Query, "c.go") && !strings.Contains(c.Query, "mkdirs") {
			t.Fatalf("an edit wrote without ifMatch: %+v", c)
		}
	}
	put(t, box, "img.png", "\x89PNG\r\n\x1a\n\x00\x00")
	if _, err := tool(t, ag, r, cfg, "e", "edit", map[string]any{"path": "img.png", "old_string": "PNG", "new_string": "GIF"}); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("editing a binary: %v", err)
	}
}

// withoutRg puts the tools glob and grep use, but not rg, on the PATH the
// fake runs commands with: the find/grep fallback.
func withoutRg(t *testing.T) {
	dir := t.TempDir()
	for _, tool := range []string{"sh", "find", "sed", "head", "grep"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestSandboxLsGlobGrep(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "tree", "none")
	put(t, box, "src/main.go", "package main\n\nfunc main() {}\n")
	put(t, box, "src/util/x.go", "package util\n\n// Main helpers\nfunc X() {}\n")
	put(t, box, "README.md", "# Tree\nsee func main\n")
	put(t, box, ".git/config", "func main in git\n")
	put(t, box, "bin/tool", "\x00\x01func main\x00")
	if out := mustTool(t, ag, r, cfg, "l", "ls", nil); out != box.Workdir+":\n.git/\nbin/\nsrc/\nREADME.md  (21 B)" {
		t.Fatalf("ls: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "l", "ls", map[string]any{"path": "src"}); out != box.Workdir+"/src:\nutil/\nmain.go  (29 B)" {
		t.Fatalf("ls src: %q", out)
	}
	check := func(label string) {
		t.Helper()
		for _, c := range []struct {
			args map[string]any
			want string
		}{
			{map[string]any{"pattern": "*.go"}, "src/main.go\nsrc/util/x.go"},
			{map[string]any{"pattern": "src/**/*.go"}, "src/main.go\nsrc/util/x.go"},
			{map[string]any{"pattern": "src/*.go"}, "src/main.go"},
			{map[string]any{"pattern": "**/*.{md,go}"}, "README.md\nsrc/main.go\nsrc/util/x.go"},
			{map[string]any{"pattern": "*.go", "path": "src/util"}, "src/util/x.go"},
			{map[string]any{"pattern": box.Workdir + "/src/**/x.go"}, "src/util/x.go"},
			{map[string]any{"pattern": "config"}, `no files match "config"`},
		} {
			if out := mustTool(t, ag, r, cfg, "g", "glob", c.args); out != c.want && !strings.HasPrefix(out, c.want) {
				t.Errorf("%s: glob %v = %q, want %q", label, c.args, out, c.want)
			}
		}
		for _, c := range []struct {
			args map[string]any
			want string
		}{
			{map[string]any{"pattern": "func main"}, "README.md:2: see func main\nsrc/main.go:3: func main() {}"},
			{map[string]any{"pattern": "func main", "glob": "*.go"}, "src/main.go:3: func main() {}"},
			{map[string]any{"pattern": "MAIN", "ignore_case": true, "path": "src"}, "src/main.go:1: package main\nsrc/main.go:3: func main() {}\nsrc/util/x.go:3: // Main helpers"},
			{map[string]any{"pattern": "^package", "path": "src/util/x.go"}, "src/util/x.go:1: package util"},
			{map[string]any{"pattern": "nothing here"}, `no matches for "nothing here"`},
		} {
			out := mustTool(t, ag, r, cfg, "g", "grep", c.args)
			if lines := strings.Split(out, "\n"); len(lines) > 1 {
				// rg and grep walk in different orders
				sortStrings(lines)
				out = strings.Join(lines, "\n")
			}
			if out != c.want && !strings.HasPrefix(out, c.want) {
				t.Errorf("%s: grep %v = %q, want %q", label, c.args, out, c.want)
			}
		}
		if _, err := tool(t, ag, r, cfg, "g", "grep", map[string]any{"pattern": "a(b"}); err == nil {
			t.Errorf("%s: a bad pattern is an error", label)
		}
	}
	if _, err := exec.LookPath("rg"); err == nil {
		check("rg")
	}
	// the caps
	for i := 0; i < 250; i++ {
		put(t, box, fmt.Sprintf("many/f%03d.txt", i), "needle\nneedle\n")
	}
	if out := mustTool(t, ag, r, cfg, "g", "glob", map[string]any{"pattern": "many/*.txt"}); strings.Count(out, "\n") != globMax || !strings.HasSuffix(out, "… and 50 more — narrow the pattern or the path") {
		t.Fatalf("glob's cap: %q", out[len(out)-80:])
	}
	if out := mustTool(t, ag, r, cfg, "g", "grep", map[string]any{"pattern": "needle", "path": "many"}); strings.Count(out, "\n") != grepMax || !strings.HasSuffix(out, "… [400 more matching lines — narrow the pattern, the path or the glob]") {
		t.Fatalf("grep's cap: %q", out[len(out)-120:])
	}
	if out := mustTool(t, ag, r, cfg, "g", "grep", map[string]any{"pattern": "needle"}); !strings.Contains(out, "… [2000+ matching lines") && !strings.Contains(out, "… [400 more") {
		t.Fatalf("grep's scan cap: %q", out[len(out)-120:])
	}
	if err := os.RemoveAll(filepath.Join(box.Workdir, "many")); err != nil {
		t.Fatal(err)
	}
	withoutRg(t)
	check("find and grep")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pat, name string
		ok        bool
	}{
		{"*.go", "a.go", true},
		{"*.go", "x/y/a.go", true},
		{"x/*.go", "x/y/a.go", false},
		{"x/**/*.go", "x/a.go", true},
		{"x/**/*.go", "x/y/z/a.go", true},
		{"**", "a/b", true},
		{"**/b", "b", true},
		{"a/**/b/**/c", "a/x/b/y/z/c", true},
		{"a/?.go", "a/bb.go", false},
	} {
		if got := globMatch(c.pat, c.name); got != c.ok {
			t.Errorf("globMatch(%q, %q) = %v", c.pat, c.name, got)
		}
	}
	if got := strings.Join(expandBraces("a{b,c{d,e}}f{1,2}"), " "); got != "abf1 abf2 acdf1 acdf2 acef1 acef2" {
		t.Fatalf("braces: %s", got)
	}
	if got := expandBraces("a{b"); len(got) != 1 || got[0] != "a{b" {
		t.Fatalf("unbalanced: %v", got)
	}
}
