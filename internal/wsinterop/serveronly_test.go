package wsinterop

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// gorillaClient matches every way gorilla/websocket opens a connection as
// the client.
var gorillaClient = regexp.MustCompile(`websocket\.(Dialer|DefaultDialer|NewClient)\b`)

// xbind and bx speak gorilla/websocket as the server only: they upgrade,
// they never dial. hack/vulncheck-allow.txt lets GO-2026-6278 ship on that
// ground — gorilla's weak mask key is made for a client's frames alone
// (conn.go: !c.isServer). A dial in shipped code makes the advisory apply:
// take a gorilla with the fix (and drop the allow entry) before adding one.
// Tests may dial; they aren't shipped.
func TestGorillaIsServerSideOnly(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if !strings.Contains(string(src), `"github.com/gorilla/websocket"`) {
				return nil
			}
			for i, line := range strings.Split(string(src), "\n") {
				if code, _, _ := strings.Cut(line, "//"); gorillaClient.MatchString(code) {
					rel, _ := filepath.Rel(root, p)
					bad = append(bad, filepath.ToSlash(rel)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) > 0 {
		t.Errorf("shipped code dials with gorilla/websocket (GO-2026-6278 then applies — see hack/vulncheck-allow.txt):\n  %s", strings.Join(bad, "\n  "))
	}
}
