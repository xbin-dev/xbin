//go:build linux && integration

package isolated

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// hcTree is a directory on disk, entry by entry: path, mode, content.
func hcTree(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		line := fmt.Sprintf("%s %v", rel, fi.Mode())
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			line += fmt.Sprintf(" %d %s", len(b), hex.EncodeToString(sum[:8]))
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// covers D245, end to end on an `xbind --isolate` with accounts — the rule
// through the broker's own word on who owns a tile and who can change it,
// the routes a person uses, real sandboxes: on bob's tile alice's session
// reads her home and writes to that tile's copy; her real home on disk is
// byte-identical after the tile's code planted ~/.claude/settings.json,
// ~/.gitconfig and ~/.bashrc; her own tile has none of it; the row says
// which home; an admin can attach to her session on the copy and not to one
// with her real home; "I trust this tile's source" gives a new session her
// real home and merges nothing; taking the trust back ends that session
// and the next is on the copy as it was; forget removes it.
func TestHomeOnATileThatIsntOnlyYours(t *testing.T) {
	a := xbindtest.Require(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true})
	for _, tile := range []string{"apps/hers", "apps/his"} {
		for rel, body := range map[string]string{"index.html": "<html><body>" + tile + "</body></html>", "xbin.json": `{"title":"` + tile + `"}`} {
			p := filepath.Join(d.WS, filepath.FromSlash(tile), rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	both := map[string]string{"apps/hers": "terminal", "apps/his": "terminal"}
	d.AddUser(t, "alice", "pw-alice-7d1f02", "user", both)
	d.AddUser(t, "bob", "pw-bob-2a9c44", "user", map[string]string{"apps/his": "terminal"})
	d.AddUser(t, "carol", "pw-carol-5c2e81", "admin", nil)
	alice := xbindtest.H("Authorization", "Bearer "+d.Login(t, "alice", "pw-alice-7d1f02"))
	carol := xbindtest.H("Authorization", "Bearer "+d.Login(t, "carol", "pw-carol-5c2e81"))

	type homeView struct {
		Mode  string
		Why   string
		Kept  bool
		Trust *struct {
			Basis   string
			Allowed bool
		}
		Copy *struct {
			ID     string
			Unused bool
		}
	}
	type signinsView struct {
		Home  *homeView
		Homes []struct {
			ID, Tile string
			Bytes    int64
			Sessions int
			Unused   bool
		}
		Trusted []struct {
			ID, Tile string
			Holds    bool
		}
	}
	view := func(tile string) signinsView {
		t.Helper()
		var v signinsView
		r := d.Call(t, "GET", "/api/xbin/agent/signins?tile="+tile, nil, alice)
		if r.Status != 200 {
			t.Fatalf("GET /agent/signins?tile=%s: %d %s", tile, r.Status, r)
		}
		r.Decode(t, &v)
		return v
	}
	// the tiles register, and take their owners
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ok := true
		for tile, to := range map[string]string{"apps/hers": "user:alice", "apps/his": "user:bob"} {
			if r := d.Call(t, "POST", "/api/xbin/owner", map[string]any{"tile": tile, "to": to}); r.Status != 200 {
				ok = false
			}
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tiles never registered")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// (an owner can be recorded for a path before the watcher registers its tile: wait for the tiles themselves)
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
		hers, his := view("apps/hers").Home, view("apps/his").Home
		if hers != nil && hers.Mode == "own" && his != nil && his.Trust != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the tiles never registered: her home on hers %+v, on his %+v", hers, his)
		}
	}
	if v := view("apps/his"); v.Home == nil || v.Home.Mode != "copy" || v.Home.Trust == nil || !v.Home.Trust.Allowed || !strings.Contains(v.Home.Why, "it belongs to bob") {
		t.Fatalf("alice's home on bob's tile: %+v", v.Home)
	}
	rows := func() map[string]homeView {
		t.Helper()
		var list []struct {
			ID   string
			Cwd  string
			Home *homeView
		}
		d.Call(t, "GET", "/api/xbin/term/sessions", nil, alice).Decode(t, &list)
		out := map[string]homeView{}
		for _, s := range list {
			if s.Home != nil {
				out[s.ID] = *s.Home
			}
		}
		return out
	}
	end := func(s *w2Term) {
		t.Helper()
		s.conn.Close()
		d.Call(t, "DELETE", "/ws/term?session="+s.id, nil, alice)
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if _, there := rows()[s.id]; !there {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %s did not end", s.id)
			}
		}
	}

	// her real home, made on her own tile
	home := filepath.Join(d.WS, "homes", "alice")
	own := w2OpenTerm(t, d, "apps/hers", alice)
	if out, rc := own.run(t, `printf '[user]\n\tname = alice-REAL\n' > ~/.gitconfig && mkdir -p ~/.ssh && echo KEY-OF-alice > ~/.ssh/id && echo 'export REAL_RC=1' >> ~/.bashrc && echo seeded`, time.Minute); rc != 0 {
		t.Fatalf("seeding her home: %s", out)
	}
	if h := rows()[own.id]; h.Mode != "own" {
		t.Fatalf("her own tile's session row: %+v", h)
	}
	end(own)
	before := hcTree(t, home)
	if !strings.Contains(before, ".gitconfig") {
		t.Fatalf("her real home on disk:\n%s", before)
	}

	// on bob's tile: reads work, the tile's code plants what would run later
	his := w2OpenTerm(t, d, "apps/his", alice)
	out, _ := his.run(t, `cat ~/.gitconfig ~/.ssh/id; mkdir -p ~/.claude && echo '{"hooks":"curl evil"}' > ~/.claude/settings.json && printf '[core]\n\tfsmonitor = /tmp/evil\n' >> ~/.gitconfig && echo 'echo PW''NED' >> ~/.bashrc && echo planted; ls `+d.WS+`/data 2>&1 | head -2`, time.Minute)
	for _, want := range []string{"alice-REAL", "KEY-OF-alice", "planted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("alice on bob's tile: no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "home-copies") {
		t.Fatalf("data/ is visible in the session:\n%s", out)
	}
	his.mu.Lock()
	said := string(his.out)
	his.mu.Unlock()
	if !strings.Contains(said, "Your home here is this tile's copy") {
		t.Errorf("the first session on the copy did not say so:\n%s", said)
	}
	if h := rows()[his.id]; h.Mode != "copy" || h.Trust == nil || h.Trust.Basis == "" {
		t.Fatalf("the session's row on bob's tile: %+v", h)
	}
	// an admin may attach to her session on the copy, as before
	if conn, r, err := d.Dial(t, "/ws/term?session="+his.id, carol); err != nil {
		t.Errorf("an admin attaching to a session on a copy: %v (%d %s)", err, r.Status, r)
	} else {
		conn.Close()
	}
	end(his)
	if after := hcTree(t, home); after != before {
		t.Fatalf("alice's real home changed on disk:\n--- before\n%s\n--- after\n%s", before, after)
	}

	// her own tile again: none of it; and no admin pass into a session with her real home
	own = w2OpenTerm(t, d, "apps/hers", alice)
	out, _ = own.run(t, `cat ~/.claude/settings.json 2>&1; cat ~/.gitconfig ~/.bashrc`, time.Minute)
	if strings.Contains(out, "curl evil") || strings.Contains(out, "fsmonitor") || strings.Contains(out, "PWNED") || !strings.Contains(out, "alice-REAL") {
		t.Fatalf("alice's own tile after the plant:\n%s", out)
	}
	if conn, r, err := d.Dial(t, "/ws/term?session="+own.id, carol); err == nil {
		conn.Close()
		t.Error("an admin attached to a session that has alice's real home")
	} else if r.Status != 403 || !strings.Contains(r.String(), "their own home") {
		t.Errorf("an admin attaching to a real-home session: %d %s", r.Status, r)
	}
	end(own)

	// "I trust this tile's source": a NEW session has her real home; nothing was merged
	v := view("apps/his")
	if v.Home.Copy == nil || len(v.Homes) != 1 || v.Homes[0].Tile != "apps/his" || v.Homes[0].Unused {
		t.Fatalf("her copies before trusting: %+v here %+v", v.Homes, v.Home.Copy)
	}
	copyID := v.Homes[0].ID
	if r := d.Call(t, "POST", "/api/xbin/agent/signins/trust", map[string]string{"tile": "apps/his", "basis": v.Home.Trust.Basis}, alice); r.Status != 201 {
		t.Fatalf("trusting bob's tile: %d %s", r.Status, r)
	}
	v = view("apps/his")
	if v.Home.Mode != "own" || len(v.Homes) != 1 || !v.Homes[0].Unused || len(v.Trusted) != 1 {
		t.Fatalf("after trusting: home %+v homes %+v trusted %+v", v.Home, v.Homes, v.Trusted)
	}
	trusted := w2OpenTerm(t, d, "apps/his", alice)
	out, _ = trusted.run(t, `cat ~/.claude/settings.json 2>&1; cat ~/.bashrc; echo TRUSTED-WROTE > ~/trusted-wrote`, time.Minute)
	if strings.Contains(out, "curl evil") || strings.Contains(out, "PWNED") {
		t.Fatalf("trusting merged the copy into her home:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "trusted-wrote")); !strings.Contains(string(b), "TRUSTED-WROTE") {
		t.Fatalf("on a trusted tile a write did not reach her real home: %q", b)
	}
	if h := rows()[trusted.id]; h.Mode != "own" {
		t.Fatalf("the trusted session's row: %+v", h)
	}

	// she takes the trust back: the session with her real home there ends
	var un struct{ Ended int }
	r := d.Call(t, "DELETE", "/api/xbin/agent/signins/trust/"+v.Trusted[0].ID, nil, alice)
	r.Decode(t, &un)
	if r.Status != 200 || un.Ended != 1 {
		t.Fatalf("untrust: %d %s", r.Status, r)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, there := rows()[trusted.id]; !there {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the real-home session on the no-longer-trusted tile still runs")
		}
	}
	// the next one there: the copy, as it was left — and it reads what her home gained
	back := w2OpenTerm(t, d, "apps/his", alice)
	out, _ = back.run(t, `cat ~/.claude/settings.json; cat ~/trusted-wrote`, time.Minute)
	if !strings.Contains(out, "curl evil") || !strings.Contains(out, "TRUSTED-WROTE") {
		t.Fatalf("back on the copy:\n%s", out)
	}

	// forget what this tile wrote: her session on it ends, the copy is gone from disk
	var fg struct {
		Tile  string
		Ended int
	}
	r = d.Call(t, "DELETE", "/api/xbin/agent/signins/homes/"+copyID, nil, alice)
	r.Decode(t, &fg)
	if r.Status != 200 || fg.Tile != "apps/his" || fg.Ended != 1 {
		t.Fatalf("forget: %d %s", r.Status, r)
	}
	if _, err := os.Stat(filepath.Join(d.WS, "data", "home-copies", "alice", copyID)); !os.IsNotExist(err) {
		t.Fatalf("the forgotten copy is still on disk: %v", err)
	}
	if v = view("apps/his"); len(v.Homes) != 0 || v.Home.Copy != nil {
		t.Fatalf("her copies after the forget: %+v", v.Homes)
	}
	// bob's own session on his tile: his real home, nothing of alice's
	bob := xbindtest.H("Authorization", "Bearer "+d.Login(t, "bob", "pw-bob-2a9c44"))
	bs := w2OpenTerm(t, d, "apps/his", bob)
	out, _ = bs.run(t, `echo HOME=$HOME; ls -a ~ | head -20; ls `+d.WS+`/homes`, time.Minute)
	if !strings.Contains(out, "HOME="+filepath.Join(d.WS, "homes", "bob")) || strings.Contains(out, "alice") || strings.Contains(out, ".claude") {
		t.Fatalf("bob's session on his own tile:\n%s", out)
	}
}
