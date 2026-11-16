//go:build linux && integration

package isolated

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// A terminal or agent session may open in a directory inside a tile; it is
// decided on that TILE. It was decided on the directory's literal path, and
// levels are looked up exactly: an exact entry, the tile's owner and its org
// never matched apps/crm/src, only patterns did. So on v0.3.79 a person with
// `apps/*: terminal` beside an exact `apps/crm: none` (or `read`) got a
// read-write shell in apps/crm/src, and a personal pattern reached into an
// org's tile past D31 the same way. A session inside a tile the person has
// terminal on works as before: it starts in that directory, which alone is
// writable.
func TestTerminalInsideATileIsTheTiles(t *testing.T) {
	a := xbindtest.Require(t)
	var addrs []string
	for p := 19510; p <= 19529; p++ {
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", p))
	}
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, TryAddrs: addrs})
	for _, tile := range []string{"apps/crm", "apps/ledger", "apps/open"} {
		d.WriteTile(t, tile, map[string]string{"index.html": "<p>" + tile + "</p>\n", "src/keep.txt": "kept\n"})
	}
	d.Must(t, "POST", "/api/xbin/orgs", map[string]any{"id": "sales", "name": "Sales"}, 200)
	d.Must(t, "POST", "/api/xbin/owner", map[string]any{"tile": "apps/ledger", "to": "org:sales"}, 200)
	d.AddUser(t, "pat", "pw-pat-71c0aa", "user", map[string]string{"apps/*": "terminal", "apps/crm": "none"})
	d.AddUser(t, "rhea", "pw-rhea-0d9e13", "user", map[string]string{"apps/*": "terminal", "apps/crm": "read"})
	pat := xbindtest.H("Authorization", "Bearer "+d.Login(t, "pat", "pw-pat-71c0aa"))
	rhea := xbindtest.H("Authorization", "Bearer "+d.Login(t, "rhea", "pw-rhea-0d9e13"))
	if err := os.Symlink("../crm", filepath.Join(d.WS, "apps", "open", "crm")); err != nil {
		t.Fatal(err)
	}
	keep := func(tile string) string {
		b, _ := os.ReadFile(filepath.Join(d.WS, tile, "src", "keep.txt"))
		return string(b)
	}

	for _, c := range []struct {
		who  xbindtest.Header
		name string
		cwd  string
	}{
		{pat, "pat", "apps/crm/src"},        // (a) an exact none, a terminal pattern
		{pat, "pat", "apps/crm/src/"},       // a trailing slash
		{pat, "pat", "/apps/crm/src"},       // a leading one
		{pat, "pat", "apps/crm"},            // the tile itself, as before
		{rhea, "rhea", "apps/crm/src"},      // read on the tile
		{pat, "pat", "apps/ledger/src"},     // (b) an org's tile (D31)
		{pat, "pat", "apps/open/../crm"},    // .. never climbs into another tile
		{pat, "pat", "apps/open/crm"},       // a link inside a tile pat may open, to one they may not
		{pat, "pat", "apps/open/crm/src"},   // …and a directory through it
		{pat, "pat", "apps/crm+main/src"},   // a deployment-qualified path is no directory
		{rhea, "rhea", "apps/open+dev/src"}, // nor for a tile rhea may open
	} {
		conn, r, err := d.Dial(t, "/ws/term?cwd="+url.QueryEscape(c.cwd), c.who)
		if err == nil {
			conn.Close()
			t.Errorf("%s's terminal opened at %s", c.name, c.cwd)
		} else if r.Status != 403 && r.Status != 400 {
			t.Errorf("%s's terminal at %s: %d %s (%v), want refused", c.name, c.cwd, r.Status, r, err)
		}
		if r := d.Call(t, "POST", "/api/xbin/term/sessions", map[string]any{"cwd": c.cwd, "kind": "agent", "provider": "claude"}, c.who); r.Status != 403 && r.Status != 400 {
			t.Errorf("%s's agent session at %s: %d %s, want refused", c.name, c.cwd, r.Status, r)
		}
		inTile := strings.Contains(c.cwd, "apps/crm") && !strings.Contains(c.cwd, "+") && !strings.Contains(c.cwd, "open") || strings.Contains(c.cwd, "apps/ledger")
		if r := d.Call(t, "GET", "/ws/term/env?cwd="+url.QueryEscape(c.cwd), nil, c.who); inTile && r.Status != 403 { // the layer of a directory in a tile closed to them
			t.Errorf("%s reads the terminal layer at %s: %d", c.name, c.cwd, r.Status)
		}
	}

	// never break users: a directory inside a tile pat has terminal on opens,
	// starts there, and is the one place in the tile it writes
	s := w2OpenTerm(t, d, "apps/open/src", pat)
	out, code := s.run(t, "pwd; echo comp=$XBIN_COMPONENT; touch made-here && echo wrote-dir; "+
		"(touch "+d.WS+"/apps/open/outside-dir) 2>/dev/null && echo wrote-tile; "+
		"(echo planted > "+d.WS+"/apps/crm/src/keep.txt) 2>/dev/null && echo wrote-crm; "+
		"(echo planted > "+d.WS+"/apps/ledger/src/keep.txt) 2>/dev/null && echo wrote-ledger; true", time.Minute)
	if code != 0 || !strings.Contains(out, d.WS+"/apps/open/src") || !strings.Contains(out, "wrote-dir") {
		t.Errorf("a session inside a tile pat may open: %d %s", code, out)
	}
	if !strings.Contains(out, "comp=apps/open") || strings.Contains(out, "comp=apps/open/src") {
		t.Errorf("XBIN_COMPONENT in a session inside apps/open: %s, want the tile", out)
	}
	for _, bad := range []string{"wrote-tile", "wrote-crm", "wrote-ledger"} {
		if strings.Contains(out, bad) {
			t.Errorf("a session in apps/open/src: %s (%s)", bad, out)
		}
	}
	if _, err := os.Stat(filepath.Join(d.WS, "apps", "open", "src", "made-here")); err != nil {
		t.Errorf("the session's write in its own directory did not land: %v", err)
	}
	if keep("apps/crm") != "kept\n" || keep("apps/ledger") != "kept\n" {
		t.Errorf("another tile's file changed: crm %q ledger %q", keep("apps/crm"), keep("apps/ledger"))
	}
}
