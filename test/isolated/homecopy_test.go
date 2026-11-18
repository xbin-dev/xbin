//go:build linux && integration

package isolated

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	if r := d.Call(t, "POST", "/api/xbin/agent/signins/trust", map[string]string{"tile": "apps/his", "basis": v.Home.Trust.Basis, "shown": "signin+home"}, alice); r.Status != 201 {
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

// covers D245 in a VM sandbox: a VM terminal's jail mounts the session's
// home the way a namespace terminal does and the shim exports the jail's
// view, so on bob's tile alice's guest reads her home and writes to the
// tile's copy — her real home on disk is unchanged. Skips where VMs can't
// run.
func TestHomeOnATileThatIsntOnlyYoursVM(t *testing.T) {
	a := xbindtest.Require(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true})
	var st struct{ Status xbindtest.VMStatus }
	d.Must(t, "GET", "/api/xbin/vm", nil, 200).Decode(t, &st)
	if !st.Status.Available {
		t.Skipf("VM sandboxes unavailable here: %s", st.Status.Reason)
	}
	d.Must(t, "PUT", "/api/xbin/vm/policy", map[string]any{"terminals": true, "budgetMiB": 8192}, 200)
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
	d.AddUser(t, "alice", "pw-alice-7d1f02", "user", map[string]string{"apps/hers": "terminal", "apps/his": "terminal"})
	d.AddUser(t, "bob", "pw-bob-2a9c44", "user", map[string]string{"apps/his": "terminal"})
	alice := xbindtest.H("Authorization", "Bearer "+d.Login(t, "alice", "pw-alice-7d1f02"))
	homeOn := func(tile string) (mode string, asks bool) {
		var v struct {
			Home *struct {
				Mode  string
				Trust *struct{ Basis string }
			}
		}
		d.Call(t, "GET", "/api/xbin/agent/signins?tile="+tile, nil, alice).Decode(t, &v)
		if v.Home == nil {
			return "", false
		}
		return v.Home.Mode, v.Home.Trust != nil
	}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
		d.Call(t, "POST", "/api/xbin/owner", map[string]any{"tile": "apps/hers", "to": "user:alice"})
		d.Call(t, "POST", "/api/xbin/owner", map[string]any{"tile": "apps/his", "to": "user:bob"})
		hers, _ := homeOn("apps/hers")
		his, asks := homeOn("apps/his")
		if hers == "own" && his == "copy" && asks {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the tiles never registered (her home on hers %q, on his %q)", hers, his)
		}
	}
	home := filepath.Join(d.WS, "homes", "alice")
	own := w2OpenTerm(t, d, "apps/hers", alice)
	if out, rc := own.run(t, `printf '[user]\n\tname = alice-REAL\n' > ~/.gitconfig && echo seeded`, time.Minute); rc != 0 {
		t.Fatalf("seeding her home: %s", out)
	}
	own.conn.Close()
	d.Call(t, "DELETE", "/ws/term?session="+own.id, nil, alice)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var list []struct{ ID string }
		d.Call(t, "GET", "/api/xbin/term/sessions", nil, alice).Decode(t, &list)
		if len(list) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("her session did not end")
		}
	}
	before := hcTree(t, home)

	vmS := w2OpenTerm(t, d, "apps/his&vm=1", alice)
	out, _ := vmS.run(t, `uname -r; cat ~/.gitconfig; mkdir -p ~/.claude && echo '{"hooks":"curl evil"}' > ~/.claude/settings.json && echo 'echo PW''NED' >> ~/.bashrc && cat ~/.claude/settings.json && echo planted`, 3*time.Minute)
	if !strings.Contains(out, "alice-REAL") || !strings.Contains(out, "planted") || !strings.Contains(out, "curl evil") {
		t.Fatalf("alice's VM terminal on bob's tile:\n%s", out)
	}
	var list []struct {
		ID   string
		VM   bool
		Home *struct{ Mode string }
	}
	d.Call(t, "GET", "/api/xbin/term/sessions", nil, alice).Decode(t, &list)
	if len(list) != 1 || !list[0].VM || list[0].Home == nil || list[0].Home.Mode != "copy" {
		t.Fatalf("the VM session's row: %+v", list)
	}
	vmS.conn.Close()
	d.Call(t, "DELETE", "/ws/term?session="+vmS.id, nil, alice)
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		d.Call(t, "GET", "/api/xbin/term/sessions", nil, alice).Decode(t, &list)
		if len(list) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the VM session did not end")
		}
	}
	if after := hcTree(t, home); after != before {
		t.Fatalf("the VM terminal changed alice's real home on disk:\n--- before\n%s\n--- after\n%s", before, after)
	}
	ents, _ := filepath.Glob(filepath.Join(d.WS, "data", "home-copies", "alice", "*", "upper", ".claude", "settings.json"))
	if len(ents) != 1 {
		t.Fatalf("the planted file is not in the tile's copy: %v", ents)
	}
}

// covers the review of D245, end to end on an `xbind --isolate`.
//
// A one-person install first (the root token, no accounts): `bx agent run`
// typed in a shell on the workspace's tile — a session opened with that
// terminal's token — has the root token's REAL home (the coding agent's
// own files land in homes/owner), no copy is made, and xbind keeps no
// record of who could change tiles.
//
// Then a shared workspace: on bob's tile a workspace admin's session — and
// the root token's — is on the tile's copy and CONFINED like a member's.
// The tile's code tries the two ways an admin's sandbox would have had
// around the copy: bind the workspace again without its masks, remount it
// read-write and write the real home; remount the read-only workspace
// read-write and edit the admin's own tile. Both fail; the real home and
// the own tile are byte-identical on disk; the row says "confined".
func TestHomeCopyConfinesAdminsAndBxAgentInAShell(t *testing.T) {
	a := xbindtest.Require(t)
	dir, err := os.MkdirTemp("", "xbindtest-homecopy-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { xbindtest.RemoveTree(dir) }) // after the daemon's own cleanup
	ws := filepath.Join(dir, "ws")
	const own, his = "apps/ws", "apps/his" // the workspace's tile; bob's (once he exists)
	acp := filepath.Join(ws, own, ".bin", "fakeacp")
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, WS: ws, Env: []string{"XBIN_AGENT_FAKE=" + acp + " --persist"}})
	for _, tile := range []string{own, his} {
		d.WriteTile(t, tile, map[string]string{"xbin.json": `{"title":"` + tile + `"}`, "index.html": "<!doctype html><title>" + tile + "</title>"})
	}
	build := exec.Command("go", "build", "-o", acp, "./hack/fakeacp")
	build.Dir, build.Env = a.Repo, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hack/fakeacp: %v\n%s", err, out)
	}
	d.WaitComponent(t, own)
	d.WaitComponent(t, his)
	root := xbindtest.H("Authorization", "Bearer "+d.Token())

	type row struct {
		ID, Cwd, Kind string
		Home          *struct {
			Mode     string
			Confined bool
		}
	}
	rows := func(cred xbindtest.Header) []row {
		t.Helper()
		var list []row
		d.Call(t, "GET", "/api/xbin/term/sessions", nil, cred).Decode(t, &list)
		return list
	}
	end := func(id string, cred xbindtest.Header) {
		t.Helper()
		d.Call(t, "DELETE", "/ws/term?session="+id, nil, cred)
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			there := false
			for _, r := range rows(cred) {
				there = there || r.ID == id
			}
			if !there {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %s did not end", id)
			}
		}
	}

	// ---- a one-person install: bx agent in the root token's shell ----
	shell := w2OpenTerm(t, d, own, root)
	out, rc := shell.run(t, `timeout 120 bx agent run --provider fake --tile `+own+` "say hello" 2>&1 | tail -n 20`, 3*time.Minute)
	if rc != 0 || !strings.Contains(out, "session ") {
		t.Fatalf("bx agent run in the root token's shell (rc %d):\n%s", rc, out)
	}
	var agentRow *row
	for _, r := range rows(root) {
		if r.Kind == "agent" {
			agentRow = &r
		}
	}
	if agentRow == nil || agentRow.Home == nil || agentRow.Home.Mode != "own" || agentRow.Home.Confined {
		t.Fatalf("the session bx agent opened in the root token's shell: %+v (%s)", agentRow, out)
	}
	ownerHome := filepath.Join(d.WS, "homes", "owner")
	if ents, _ := os.ReadDir(filepath.Join(ownerHome, ".fakeacp", "sessions")); len(ents) == 0 {
		t.Fatalf("the coding agent's own files are not in the root token's real home:\n%s", hcTree(t, ownerHome))
	}
	for _, p := range []string{filepath.Join(d.WS, "data", "home-copies"), filepath.Join(d.WS, "data", "signins", "tiles.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("a one-person install has %s: %v", p, err)
		}
	}
	end(agentRow.ID, root)
	shell.conn.Close()
	end(shell.id, root)

	// ---- a shared workspace: bob's tile, an admin, the root token ----
	d.AddUser(t, "bob", "pw-bob-2a9c44", "user", map[string]string{his: "terminal"})
	d.AddUser(t, "carol", "pw-carol-5c2e81", "admin", nil)
	carol := xbindtest.H("Authorization", "Bearer "+d.Login(t, "carol", "pw-carol-5c2e81"))
	d.Must(t, "POST", "/api/xbin/owner", map[string]any{"tile": his, "to": "user:bob"}, 200)
	if err := os.WriteFile(filepath.Join(d.WS, own, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		who  string
		key  string
		cred xbindtest.Header
	}{{"an admin", "carol", carol}, {"the root token", "owner", root}} {
		home := filepath.Join(d.WS, "homes", c.key)
		// their real home, seeded on their own tile — where their sandbox is an admin's
		mine := w2OpenTerm(t, d, own, c.cred)
		if out, rc := mine.run(t, `echo 'export REAL_RC=1' >> ~/.bashrc && echo seeded; grep CapEff /proc/self/status`, time.Minute); rc != 0 || !hcSysAdmin(t, out) {
			t.Fatalf("%s on the workspace's tile (rc %d): not an admin's sandbox, or no home:\n%s", c.who, rc, out)
		}
		for _, r := range rows(c.cred) {
			if r.ID == mine.id && (r.Home == nil || r.Home.Mode != "own" || r.Home.Confined) {
				t.Fatalf("%s's row on the workspace's tile: %+v", c.who, r.Home)
			}
		}
		mine.conn.Close()
		end(mine.id, c.cred)
		before, ownBefore := hcTree(t, home), hcTree(t, filepath.Join(d.WS, own))

		s := w2OpenTerm(t, d, his, c.cred)
		out, _ := s.run(t, `mkdir -p /mnt/x; mount --bind `+d.WS+` /mnt/x 2>&1; mount -o remount,rw,bind /mnt/x 2>&1; `+
			`(echo 'echo PW''NED-VIA-MOUNT' >> /mnt/x/homes/`+c.key+`/.bashrc) 2>&1; `+
			`mount -o remount,rw,bind `+d.WS+` 2>&1; (echo planted > `+d.WS+`/`+own+`/planted-by-tile) 2>&1; `+
			`unshare -Urm true 2>&1; grep CapEff /proc/self/status; cat ~/.bashrc | tail -n 2; echo y > ~/in-copy && echo HOME-''WRITE-OK`, time.Minute)
		if hcSysAdmin(t, out) {
			t.Errorf("%s's session on bob's tile can mount (CAP_SYS_ADMIN):\n%s", c.who, out)
		}
		if !strings.Contains(out, "REAL_RC") || !strings.Contains(out, "HOME-WRITE-OK") {
			t.Errorf("%s's session on bob's tile does not read the real home and write its copy:\n%s", c.who, out)
		}
		confined := false
		for _, r := range rows(c.cred) {
			confined = confined || (r.ID == s.id && r.Home != nil && r.Home.Mode == "copy" && r.Home.Confined)
		}
		if !confined {
			t.Errorf("%s's row on bob's tile does not say copy, confined: %+v", c.who, rows(c.cred))
		}
		s.mu.Lock()
		said := string(s.out)
		s.mu.Unlock()
		if !strings.Contains(said, "confined like a member's") {
			t.Errorf("%s was not told the session is confined:\n%s", c.who, said)
		}
		s.conn.Close()
		end(s.id, c.cred)
		if after := hcTree(t, home); after != before {
			t.Fatalf("%s's REAL home was written from a session on bob's tile:\n--- before\n%s\n--- after\n%s\n--- the session\n%s", c.who, before, after, out)
		}
		if after := hcTree(t, filepath.Join(d.WS, own)); after != ownBefore {
			t.Fatalf("%s's own tile was written from a session on bob's tile:\n--- before\n%s\n--- after\n%s\n--- the session\n%s", c.who, ownBefore, after, out)
		}
	}
}

// covers the root token's own trust answers (D245's review: someone who
// lives on the root token must not lose the real home with no way back),
// end to end on an `xbind --isolate`: on bob's tile the token's session is
// on the copy, confined, and its row carries the question; the token
// answers it through the same route a person uses (the basis and what was
// shown with it); a new session there has the token's real home and an
// admin's sandbox; taking the answer back ends that session.
func TestHomeRootTokenTrusts(t *testing.T) {
	a := xbindtest.Require(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true})
	const his = "apps/his"
	d.WriteTile(t, his, map[string]string{"xbin.json": `{"title":"His"}`, "index.html": "<!doctype html><title>his</title>"})
	d.WaitComponent(t, his)
	d.AddUser(t, "bob", "pw-bob-2a9c44", "user", map[string]string{his: "terminal"})
	d.Must(t, "POST", "/api/xbin/owner", map[string]any{"tile": his, "to": "user:bob"}, 200)
	root := xbindtest.H("Authorization", "Bearer "+d.Token())
	type view struct {
		Home *struct {
			Mode     string
			Confined bool
			Trust    *struct {
				Basis   string
				Allowed bool
			}
		}
		Trusted []struct {
			ID, Tile string
			Holds    bool
		}
	}
	get := func() view {
		t.Helper()
		var v view
		d.Must(t, "GET", "/api/xbin/agent/signins?tile="+his, nil, 200, root).Decode(t, &v)
		return v
	}
	v := get()
	if v.Home == nil || v.Home.Mode != "copy" || !v.Home.Confined || v.Home.Trust == nil || !v.Home.Trust.Allowed || v.Home.Trust.Basis == "" {
		t.Fatalf("the root token's home on bob's tile, and its question: %+v", v.Home)
	}
	onCopy := w2OpenTerm(t, d, his, root)
	if out, _ := onCopy.run(t, `grep CapEff /proc/self/status`, time.Minute); hcSysAdmin(t, out) {
		t.Fatalf("the root token's session on a tile it has not trusted can mount:\n%s", out)
	}
	// an answer that doesn't say what it showed is an old page's: refused
	if r := d.Call(t, "POST", "/api/xbin/agent/signins/trust", map[string]string{"tile": his, "basis": v.Home.Trust.Basis}, root); r.Status != 409 || !strings.Contains(r.String(), "Reload this page") {
		t.Fatalf("an answer without what was shown: %d %s", r.Status, r)
	}
	d.Must(t, "POST", "/api/xbin/agent/signins/trust", map[string]string{"tile": his, "basis": v.Home.Trust.Basis, "shown": "signin+home"}, 201, root)
	if v = get(); v.Home.Mode != "own" || len(v.Trusted) != 1 || !v.Trusted[0].Holds {
		t.Fatalf("after the root token's answer: home %+v trusted %+v", v.Home, v.Trusted)
	}
	real := w2OpenTerm(t, d, his, root)
	out, _ := real.run(t, `echo ROOT-''WROTE > ~/root-wrote; grep CapEff /proc/self/status`, time.Minute)
	if !hcSysAdmin(t, out) {
		t.Fatalf("the root token's session on a tile it trusted is still confined:\n%s", out)
	}
	if b, err := os.ReadFile(filepath.Join(d.WS, "homes", "owner", "root-wrote")); err != nil || !strings.Contains(string(b), "ROOT-WROTE") {
		t.Fatalf("the root token's real home was not written: %q %v", b, err)
	}
	// taken back: the real-home session ends; the one on the copy stays
	var un struct {
		OK    bool
		Ended int
	}
	d.Must(t, "DELETE", "/api/xbin/agent/signins/trust/"+v.Trusted[0].ID, nil, 200, root).Decode(t, &un)
	if !un.OK || un.Ended != 1 {
		t.Fatalf("taking the root token's answer back: %+v", un)
	}
	xbindtest.Eventually(t, 30*time.Second, "the root token's real-home session ended", func() (bool, string) {
		var list []struct{ ID string }
		d.Call(t, "GET", "/api/xbin/term/sessions", nil, root).Decode(t, &list)
		for _, s := range list {
			if s.ID == real.id {
				return false, "still listed"
			}
		}
		return len(list) == 1 && list[0].ID == onCopy.id, fmt.Sprintf("%+v", list)
	})
	onCopy.conn.Close()
	d.Call(t, "DELETE", "/ws/term?session="+onCopy.id, nil, root)
}

// hcSysAdmin reports whether the CapEff line in out has CAP_SYS_ADMIN (bit 21): the capability to mount.
func hcSysAdmin(t *testing.T, out string) bool {
	t.Helper()
	_, hex, ok := strings.Cut(out, "CapEff:")
	if !ok || len(strings.Fields(hex)) == 0 {
		t.Fatalf("no CapEff in:\n%s", out)
	}
	caps, err := strconv.ParseUint(strings.Fields(hex)[0], 16, 64)
	if err != nil {
		t.Fatalf("CapEff: %v in:\n%s", err, out)
	}
	return caps&(1<<21) != 0
}
