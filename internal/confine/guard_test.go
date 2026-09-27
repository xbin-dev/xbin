package confine

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// execCall matches every way Go code starts a program.
var execCall = regexp.MustCompile(`exec\.Command(Context)?\(|exec\.Cmd\{|os\.StartProcess\(|syscall\.Exec\(|unix\.Exec\(`)

// daemonGoFiles calls fn for every non-test Go file of daemon code
// (internal/, cmd/xbind) with its repo-relative, slash-separated path. cmd/bx
// is the user's CLI and runs with the user's privileges, not the daemon's.
func daemonGoFiles(t *testing.T, fn func(rel, abs string) error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, top := range []string{"internal", "cmd/xbind"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			return fn(filepath.ToSlash(rel), p)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The daemon may not start a program except through this package (a
// confined run) or where the call site says why a direct run is safe:
// `exec-ok: <reason>` on the line or the two above (D78). Exempt: this
// package, the sandbox itself (it IS the confinement), and the agent host
// (it runs inside the sandbox).
func TestNoDirectExec(t *testing.T) {
	exempt := []string{"internal/confine/", "internal/sandbox/", "internal/agent/host/"}
	var bad []string
	daemonGoFiles(t, func(rel, abs string) error {
		for _, e := range exempt {
			if strings.HasPrefix(rel, e) {
				return nil
			}
		}
		f, err := os.Open(abs)
		if err != nil {
			return err
		}
		defer f.Close()
		var prev [2]string
		sc := bufio.NewScanner(f)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			code, _, _ := strings.Cut(line, "//")
			if execCall.MatchString(code) && !strings.Contains(line+prev[0]+prev[1], "exec-ok:") {
				bad = append(bad, rel+":"+strconv.Itoa(n)+": "+strings.TrimSpace(line))
			}
			prev[1], prev[0] = prev[0], line
		}
		return sc.Err()
	})
	if len(bad) > 0 {
		t.Fatalf("the daemon starts programs directly — run tools on workspace data through internal/confine "+
			"(a sandbox), or say why the call is safe with `// exec-ok: <reason>` (D78, AGENTS.md):\n  %s", strings.Join(bad, "\n  "))
	}
}

// net/http/cgi's Handler execs its Path on the host, as xbind — the exec
// guard above cannot see that (the exec happens inside the standard
// library). It is how runtime "cgi" ran a tile's backend/handler outside
// every sandbox until D117 removed it; no daemon code may import it again,
// with no exemption.
func TestNoCGIHandler(t *testing.T) {
	var bad []string
	fset := token.NewFileSet()
	daemonGoFiles(t, func(rel, abs string) error {
		f, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, im := range f.Imports {
			if p, _ := strconv.Unquote(im.Path.Value); p == "net/http/cgi" {
				bad = append(bad, rel+": imports net/http/cgi")
			}
		}
		return nil
	})
	if len(bad) > 0 {
		t.Fatalf("the daemon imports net/http/cgi, which runs programs on the host as xbind — tile code runs only "+
			"in a backend's sandbox (a go/node/python runtime; D117, D78):\n  %s", strings.Join(bad, "\n  "))
	}
}
