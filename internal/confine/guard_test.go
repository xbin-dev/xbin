package confine

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// execCall matches every way Go code starts a program.
var execCall = regexp.MustCompile(`exec\.Command(Context)?\(|exec\.Cmd\{|os\.StartProcess\(|syscall\.Exec\(|unix\.Exec\(`)

// The daemon may not start a program except through this package (a
// confined run) or where the call site says why a direct run is safe:
// `exec-ok: <reason>` on the line or the two above (D78). Exempt: this
// package, the sandbox itself (it IS the confinement), and the agent host
// (it runs inside the sandbox). cmd/bx is the user's CLI and runs with the
// user's privileges, not the daemon's.
func TestNoDirectExec(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	exempt := []string{"internal/confine/", "internal/sandbox/", "internal/agent/host/"}
	var bad []string
	for _, top := range []string{"internal", "cmd/xbind"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			for _, e := range exempt {
				if strings.HasPrefix(rel, e) {
					return nil
				}
			}
			f, err := os.Open(p)
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
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("the daemon starts programs directly — run tools on workspace data through internal/confine "+
			"(a sandbox), or say why the call is safe with `// exec-ok: <reason>` (D78, AGENTS.md):\n  %s", strings.Join(bad, "\n  "))
	}
}
