//go:build linux && integration

package isolated

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

// TestNamespace drives examples/sandbox-go's namespace sandboxes through
// xbind's proxy: definitions, commands, output through Forward, stdin,
// files and tar, relayed terminals, snapshots, two consumers' partitions
// against crafted ids, egress, an xbind restart — and, where this host
// delegates a sub-uid range, range mode.
func TestNamespace(t *testing.T) {
	owner, cons := setup(t, xbindtest.Options{})
	runSuite(t, owner, cons, "namespace", 1)
}

// TestVM is the same with VM sandboxes (KVM, or QEMU's emulation with
// XBIN_VM_ACCEL=emulate); it skips where VMs can't run.
func TestVM(t *testing.T) {
	d := xbindtest.StartOrConnect(t, xbindtest.Options{})
	slow := time.Duration(1)
	if d.RequireVM(t) == "emulate" {
		slow = 6
	}
	importManager(t, d)
	owner, cons := managers(t, d)
	runSuite(t, owner, cons, "vm", slow)
}

func runSuite(t *testing.T, owner, cons *manager, mode string, slow time.Duration) {
	d := owner.d
	var rt runtimeInfo
	owner.must(t, "GET", "/runtime", nil, 200, &rt)
	if !rt.Enabled || !rt.Isolation || !hasMode(rt, mode) {
		t.Fatalf("the runtime doesn't offer %s: %+v", mode, rt)
	}
	for _, c := range []string{"exec", "tty", "files", "tar", "snapshots", "clone"} {
		if !slices.Contains(rt.Caps, c) {
			t.Errorf("runtime caps %v lack %s", rt.Caps, c)
		}
	}
	if !slices.ContainsFunc(rt.Egress, func(e struct{ Class, Slot, Reach string }) bool {
		return e.Class == "class:internet" && e.Slot == "internet" && e.Reach == "internet"
	}) {
		t.Errorf("the bound internet class isn't offered: %+v", rt.Egress)
	}
	if rt.Limits.Flows.TCP <= 0 || rt.Limits.Flows.UDP <= 0 {
		t.Errorf("limits.flows didn't come through the SDK: %+v", rt.Limits.Flows)
	}

	box := owner.create(t, "box", mode, "internet")
	if box.Mode != mode || box.Net.Reach != "internet" || box.Labels["sandbox-go.home"] != "owner" || box.For != "" {
		t.Errorf("box: %+v", box)
	}
	if d.IsRemote() { // its rootfs is on its host
		if box.Base.Version == "" || box.Base.Outdated {
			t.Errorf("box's base: %+v", box.Base)
		}
	} else if want := baseVersion(t, d.Rootfs()); box.Base.Version != want || box.Base.Outdated {
		t.Errorf("box's base: %+v, want %s", box.Base, want)
	}
	if mode == "vm" && box.Accel == "" {
		t.Errorf("a VM sandbox without its accel: %+v", box)
	}

	t.Run("run", func(t *testing.T) {
		res := owner.run(t, "box", `echo out; echo err >&2; echo "$IN_SANDBOX $SANDBOX_NAME"; id -u; id -G`)
		// no supplementary groups (id -G is 0) — except in a namespace
		// mapping a single uid (users: root), which can't drop xbind's
		// user's: they read as nogroup there
		ok := res.Stdout.Head == "out\n1 box\n0\n0\n"
		if mode == "namespace" && rt.Users != "any" {
			ok = strings.HasPrefix(res.Stdout.Head, "out\n1 box\n0\n0") && strings.Count(res.Stdout.Head, "\n") == 4
		}
		if !ok || res.Stderr.Head != "err\n" {
			t.Errorf("run: stdout %q stderr %q (want no supplementary groups: id -G is 0)", res.Stdout.Head, res.Stderr.Head)
		}
		// nothing of xbind's reaches a command: no XBIN_ variable, no
		// token, none of the manager's own identity
		env := owner.run(t, "box", "env; cat /proc/1/environ 2>/dev/null | tr '\\0' '\\n'")
		if strings.Contains(env.Stdout.Head+env.Stdout.Tail, "XBIN_") {
			t.Errorf("an xbin variable inside:\n%s", env.Stdout.Head)
		}
		var bad runResult
		owner.must(t, "POST", "/sandboxes/box/run", map[string]any{"argv": []string{"sh", "-c", "exit 3"}}, 200, &bad)
		if bad.ExitCode == nil || *bad.ExitCode != 3 {
			t.Errorf("exit 3: %+v", bad)
		}
	})

	t.Run("exec output through Forward", func(t *testing.T) {
		ex := owner.exec(t, "box", map[string]any{"cmd": "for i in 1 2 3; do echo line$i; sleep 0.3; done", "label": "lines"})
		out, end := owner.follow(t, "box", ex.ID, "", 60*time.Second*slow)
		if out != "line1\nline2\nline3\n" || end.State != "exited" || end.ExitCode == nil || *end.ExitCode != 0 {
			t.Errorf("follow: %q %+v", out, end)
		}
		// stdin, forwarded as a raw body
		cat := owner.exec(t, "box", map[string]any{"cmd": "cat", "stdin": true})
		owner.must(t, "POST", "/sandboxes/box/execs/"+cat.ID+"/stdin?eof=1", "piped in\n", 204, nil)
		if out, _ := owner.follow(t, "box", cat.ID, "", 30*time.Second*slow); out != "piped in\n" {
			t.Errorf("stdin: %q", out)
		}
		var list struct{ Execs []execInfo }
		owner.must(t, "GET", "/sandboxes/box/execs", nil, 200, &list)
		if len(list.Execs) < 2 {
			t.Errorf("execs: %+v", list.Execs)
		}
		// a signal forwarded as {"signal":"TERM"} reaches the group
		sl := owner.exec(t, "box", map[string]any{"cmd": "sleep 600 & wait"})
		owner.must(t, "POST", "/sandboxes/box/execs/"+sl.ID+"/signal", map[string]string{"signal": "TERM"}, 204, nil)
		if _, end := owner.follow(t, "box", sl.ID, "", 30*time.Second*slow); end.State != "killed" {
			t.Errorf("after TERM: %+v", end)
		}
	})

	t.Run("files and tar through Forward", func(t *testing.T) {
		var st struct{ Path, Type, Etag string }
		owner.must(t, "PUT", "/sandboxes/box/files/content?path=/root/dir/hello.txt&mkdirs=1", "hello, sandbox\n", 200, &st)
		if st.Type != "file" || st.Etag == "" {
			t.Fatalf("write: %+v", st)
		}
		r := owner.must(t, "GET", "/sandboxes/box/files/content?path=/root/dir/hello.txt", nil, 200, nil)
		if string(r.Body) != "hello, sandbox\n" || strings.Trim(r.Header.Get("ETag"), `"`) != st.Etag {
			t.Errorf("read: %q etag %q (stat %q)", r.Body, r.Header.Get("ETag"), st.Etag)
		}
		owner.must(t, "PUT", "/sandboxes/box/files/content?path=/root/dir/hello.txt&ifMatch=nope", "x", 412, nil)
		owner.must(t, "POST", "/sandboxes/box/files/move", map[string]any{"from": "/root/dir/hello.txt", "to": "/root/dir/moved.txt"}, 204, nil)
		var ls struct {
			Entries []struct{ Name, Type string }
		}
		owner.must(t, "GET", "/sandboxes/box/files/list?path=/root/dir", nil, 200, &ls)
		if len(ls.Entries) != 1 || ls.Entries[0].Name != "moved.txt" {
			t.Errorf("list: %+v", ls.Entries)
		}
		owner.must(t, "GET", "/sandboxes/box/files/nope?path=/root", nil, 400, nil) // FilesRoute refuses an op that isn't one
		tr := owner.must(t, "GET", "/sandboxes/box/tar?path=/root/dir", nil, 200, nil)
		names := tarNames(t, tr.Body)
		if !slices.Contains(names, "moved.txt") {
			t.Errorf("tar: %v", names)
		}
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: "up/a.txt", Mode: 0o644, Size: 3})
		_, _ = tw.Write([]byte("abc"))
		_ = tw.Close()
		owner.must(t, "PUT", "/sandboxes/box/tar?path=/root/in&mkdirs=1", buf.Bytes(), 204, nil)
		if r := owner.must(t, "GET", "/sandboxes/box/files/content?path=/root/in/up/a.txt", nil, 200, nil); string(r.Body) != "abc" {
			t.Errorf("tar put: %q", r.Body)
		}
	})

	t.Run("terminals relayed", func(t *testing.T) {
		// a new terminal: RelayNewTTY
		c, r, err := d.Dial(t, "/api/"+mgrTile+"/sandboxes/box/tty?rows=24&cols=80")
		if err != nil {
			t.Fatalf("dial tty: %v (%d %s)", err, r.Status, r)
		}
		sess := readSession(t, c)
		if sess.Sandbox != "box" || sess.ID == "" || !sess.EchoAck {
			t.Errorf("session frame: %+v", sess)
		}
		send(t, c, "echo tty-$((6*7))\r")
		readUntil(t, c, "tty-42", 30*time.Second*slow)
		send(t, c, "exit\r")
		if code := readExit(t, c, 30*time.Second*slow); code == nil || *code != 0 {
			t.Errorf("exit frame: %v", code)
		}
		c.Close()

		// an attach to a tty exec: RelayTTY
		ex := owner.exec(t, "box", map[string]any{"cmd": "cat", "tty": true, "rows": 24, "cols": 80})
		c, r, err = d.Dial(t, "/api/"+mgrTile+"/sandboxes/box/execs/"+ex.ID+"/tty")
		if err != nil {
			t.Fatalf("attach: %v (%d %s)", err, r.Status, r)
		}
		if sess := readSession(t, c); sess.ID != ex.ID || sess.Sandbox != "box" {
			t.Errorf("attach session frame: %+v", sess)
		}
		owner.must(t, "POST", "/sandboxes/box/execs/"+ex.ID+"/resize", map[string]int{"rows": 30, "cols": 100}, 204, nil)
		send(t, c, "typed-through\r")
		readUntil(t, c, "typed-through", 30*time.Second*slow)
		c.Close()
		time.Sleep(200 * time.Millisecond)
		var e execInfo
		owner.must(t, "GET", "/sandboxes/box/execs/"+ex.ID, nil, 200, &e)
		if e.State != "running" || !e.TTY {
			t.Errorf("a detached tty exec: %+v", e)
		}
		owner.must(t, "DELETE", "/sandboxes/box/execs/"+ex.ID, nil, 204, nil)
		owner.must(t, "GET", "/sandboxes/box/execs/"+ex.ID, nil, 404, nil)

		// the second consumer's page, its frame token on the upgrade
		if in := cons.create(t, "cterm", mode, ""); in.For != consTile || in.Labels["sandbox-go.home"] != consTile {
			t.Errorf("the consumer's sandbox: %+v", in)
		}
		c, r, err = d.Dial(t, "/api/"+mgrTile+"/sandboxes/cterm/tty?frame="+cons.frame(t))
		if err != nil {
			t.Fatalf("consumer tty: %v (%d %s)", err, r.Status, r)
		}
		readSession(t, c)
		send(t, c, "echo from-$((1+1))-cons\r")
		readUntil(t, c, "from-2-cons", 30*time.Second*slow)
		c.Close()
		cons.must(t, "DELETE", "/sandboxes/cterm", nil, 204, nil)
	})

	t.Run("snapshots", func(t *testing.T) {
		owner.run(t, "box", "echo before > /root/snap.txt")
		var sn snapshotInfo
		owner.must(t, "POST", "/sandboxes/box/snapshots", map[string]string{"name": "first", "clientId": "c1"}, 201, &sn)
		if sn.ID != "s-1" || sn.Pending {
			t.Fatalf("snapshot: %+v", sn)
		}
		owner.run(t, "box", "echo after > /root/snap.txt")
		var in sandboxInfo
		owner.must(t, "POST", "/sandboxes/box/snapshots/s-1/restore", nil, 200, &in)
		if in.State != "running" {
			t.Errorf("restored: %+v", in)
		}
		if res := owner.run(t, "box", "cat /root/snap.txt"); res.Stdout.Head != "before\n" {
			t.Errorf("after the restore: %q", res.Stdout.Head)
		}
		owner.must(t, "POST", "/sandboxes/box/snapshots/..%252Fx/restore", nil, 404, nil)
		owner.must(t, "DELETE", "/sandboxes/box/snapshots/s-1", nil, 204, nil)
	})

	t.Run("partitions against crafted ids", func(t *testing.T) { testPartitions(t, owner, cons, mode, slow) })

	t.Run("egress none", func(t *testing.T) {
		cons.create(t, "quiet", mode, "")
		var res runResult
		t0 := time.Now()
		cons.must(t, "POST", "/sandboxes/quiet/run", map[string]any{"argv": []string{"bash", "-c", "exec 3<>/dev/tcp/1.1.1.1/53"}, "timeoutMs": 20000}, 200, &res)
		if res.ExitCode == nil || *res.ExitCode == 0 || res.TimedOut || time.Since(t0) > 15*time.Second*slow {
			t.Errorf("a connection under egress none: %+v after %s", res, time.Since(t0))
		}
	})

	if mode == "namespace" && rt.Users == "any" {
		t.Run("range mode", func(t *testing.T) { testRangeMode(t, owner) })
	}

	t.Run("an xbind restart", func(t *testing.T) {
		owner.must(t, "PUT", "/sandboxes/box/files/content?path=/root/keep.txt", "kept\n", 200, nil)
		ex := owner.exec(t, "box", map[string]any{"cmd": "sleep 600"})
		d.Restart(t)
		waitManager(t, d)
		in := owner.get(t, "box")
		if in.State != "stopped" {
			t.Errorf("after the restart: %+v", in)
		}
		if r := owner.call(t, "GET", "/sandboxes/box/execs/"+ex.ID, nil); r.Status != 410 || refusedAs(t, r).Refusal != "lost" {
			t.Errorf("an exec of the old boot: %d %s", r.Status, r)
		}
		if r := owner.call(t, "GET", "/sandboxes/box/execs/"+ex.ID+"/output", nil); r.Status != 410 {
			t.Errorf("its output through Forward: %d %s", r.Status, r)
		}
		// the state is kept: a file read starts it again (autoStart)
		if r := owner.must(t, "GET", "/sandboxes/box/files/content?path=/root/keep.txt", nil, 200, nil); string(r.Body) != "kept\n" {
			t.Errorf("after the restart: %q", r.Body)
		}
		if in := owner.get(t, "box"); in.State != "running" {
			t.Errorf("auto-started: %+v", in)
		}
		// the consumer's sandbox, stopped and kept too
		if in := cons.get(t, "quiet"); in.State != "stopped" {
			t.Errorf("the consumer's sandbox after the restart: %+v", in)
		}
	})
}

// frame is the consumer's frame token, for a WebSocket's ?frame=.
func (m *manager) frame(t *testing.T) string {
	for _, h := range m.as {
		if h.K == "X-XBin-Frame-Token" {
			return h.V
		}
	}
	t.Fatal("not a page")
	return ""
}

// testPartitions: each consumer sees only its own sandboxes, and no id it
// crafts — an encoded "/" and "..", once or twice encoded, in the sandbox,
// exec, file-op or snapshot segment — reaches the other's.
func testPartitions(t *testing.T, owner, cons *manager, mode string, slow time.Duration) {
	d := owner.d
	cons.create(t, "cbox", mode, "")
	secret := owner.exec(t, "box", map[string]any{"cmd": "echo OWNER-SECRET; sleep 600"})
	owner.follow(t, "box", secret.ID, "OWNER-SECRET", 30*time.Second*slow)
	owner.must(t, "PUT", "/sandboxes/box/files/content?path=/root/secret.txt", "OWNER-SECRET\n", 200, nil)
	tty := owner.exec(t, "box", map[string]any{"cmd": "echo OWNER-SECRET; cat", "tty": true})
	cmine := cons.exec(t, "cbox", map[string]any{"cmd": "echo CONS-SECRET; sleep 600"})
	cons.follow(t, "cbox", cmine.ID, "CONS-SECRET", 30*time.Second*slow)

	lists := func(m *manager) []string {
		var out struct{ Sandboxes []sandboxInfo }
		m.must(t, "GET", "/sandboxes", nil, 200, &out)
		var names []string
		for _, s := range out.Sandboxes {
			names = append(names, s.Name)
		}
		return names
	}
	if l := lists(cons); slices.Contains(l, "box") || !slices.Contains(l, "cbox") {
		t.Errorf("the consumer lists %v", l)
	}
	if l := lists(owner); slices.Contains(l, "cbox") || !slices.Contains(l, "box") {
		t.Errorf("the owner lists %v", l)
	}
	cons.must(t, "GET", "/sandboxes/box", nil, 404, nil)
	cons.must(t, "GET", "/sandboxes/box/execs/"+secret.ID+"/output", nil, 404, nil)

	for _, c := range []struct {
		m           *manager
		mine, other string // the caller's sandbox, the other's
		eid         string // an exec of the other's
		marker      string
	}{
		{cons, "cbox", "box", secret.ID, "OWNER-SECRET"},
		{owner, "box", "cbox", cmine.ID, "CONS-SECRET"},
	} {
		var paths []string
		for _, sl := range []string{"%2F", "%252F", "%2f"} {
			up := "..%s..%s"
			paths = append(paths,
				fmt.Sprintf("/sandboxes/%s/execs/"+up+"%s%sexecs%s%s/output", c.mine, sl, sl, c.other, sl, sl, c.eid),
				fmt.Sprintf("/sandboxes/%s/execs/"+up+"..%ssandboxes%s%s%sexecs%s%s/output", c.mine, sl, sl, sl, sl, c.other, sl, sl, c.eid),
				fmt.Sprintf("/sandboxes/%s%s..%s%s/execs/%s/output", c.mine, sl, sl, c.other, c.eid),
				fmt.Sprintf("/sandboxes/..%s%s/execs/%s/output", sl, c.other, c.eid),
				fmt.Sprintf("/sandboxes/%s/execs/%s%s..%s..%s..%s%s%sexecs%s%s/output", c.mine, c.eid, sl, sl, sl, sl, c.other, sl, sl, c.eid),
				fmt.Sprintf("/sandboxes/%s/files/..%s..%s%s%sfiles%scontent?path=/root/secret.txt", c.mine, sl, sl, c.other, sl, sl),
				fmt.Sprintf("/sandboxes/%s/execs/%s", c.mine, strings.ReplaceAll("../../"+c.other+"/execs/"+c.eid, "/", sl)),
				fmt.Sprintf("/sandboxes/%s/snapshots/..%s..%s%s%ssnapshots/restore", c.mine, sl, sl, c.other, sl),
			)
		}
		paths = append(paths,
			"/sandboxes/"+c.mine+"/execs/../../"+c.other+"/execs/"+c.eid+"/output", // a plain dot segment
			"/sandboxes/"+c.mine+"/execs/%2E%2E/%2E%2E/"+c.other+"/execs/"+c.eid+"/output",
		)
		for _, p := range paths {
			for _, method := range []string{"GET", "DELETE", "POST"} {
				r := d.Call(t, method, "/api/"+mgrTile+p, nil, c.m.as...)
				if r.Status/100 == 2 || strings.Contains(string(r.Body), c.marker) {
					t.Errorf("%s %s %s reached the other's sandbox: %d %s", c.m.who, method, p, r.Status, r)
				}
				if r.Status/100 == 3 { // a redirect never leads there either
					if loc := r.Header.Get("Location"); strings.Contains(loc, c.other) {
						rr := d.Call(t, method, loc, nil, c.m.as...)
						if rr.Status/100 == 2 || strings.Contains(string(rr.Body), c.marker) {
							t.Errorf("%s %s %s → %s reached the other's sandbox: %d %s", c.m.who, method, p, loc, rr.Status, rr)
						}
					}
				}
			}
		}
	}
	// a crafted terminal attach is refused before the upgrade
	for _, p := range []string{
		"/sandboxes/cbox/execs/..%2F..%2Fbox%2Fexecs%2F" + tty.ID + "/tty",
		"/sandboxes/cbox%2F..%2Fbox/execs/" + tty.ID + "/tty",
		"/sandboxes/cbox/execs/..%252F..%252Fbox%252Fexecs%252F" + tty.ID + "/tty",
		"/sandboxes/box/execs/" + tty.ID + "/tty",
	} {
		c, r, err := d.Dial(t, "/api/"+mgrTile+p+"?frame="+cons.frame(t))
		if err == nil {
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, b, _ := c.ReadMessage()
			c.Close()
			t.Errorf("the consumer attached to %s: %s", p, b)
		} else if r.Status == 101 || strings.Contains(string(r.Body), "OWNER-SECRET") {
			t.Errorf("the consumer's attach %s: %d %s", p, r.Status, r)
		}
	}
	// the owner's exec never saw the crafted calls: it still runs, untouched
	var e execInfo
	owner.must(t, "GET", "/sandboxes/box/execs/"+secret.ID, nil, 200, &e)
	if e.State != "running" {
		t.Errorf("the owner's exec after the crafted calls: %+v", e)
	}
	cons.must(t, "DELETE", "/sandboxes/cbox", nil, 204, nil)
	owner.must(t, "DELETE", "/sandboxes/box/execs/"+tty.ID, nil, 204, nil)
	owner.must(t, "DELETE", "/sandboxes/box/execs/"+secret.ID, nil, 204, nil)
}

// testRangeMode: a sandbox's commands run as a sub-uid of the host's
// delegated range; what they write is owned by it on the host, measured,
// snapshotted (confined, exactly) and removed with the sandbox.
func testRangeMode(t *testing.T, owner *manager) {
	d := owner.d
	in := owner.create(t, "ranged", "namespace", "")
	if in.Users != "any" {
		t.Fatalf("range mode: users %q", in.Users)
	}
	owner.run(t, "ranged", "mkdir -p /srv/r && chown 1000:1000 /srv/r")
	owner.run(t, "ranged", "head -c 4194304 /dev/urandom > /srv/r/blob && chmod 600 /srv/r/blob && ln -s /etc/shadow /srv/r/link",
		map[string]any{"uid": 1000, "gid": 1000})
	stateDir := findStateDir(t, d, "ranged", in.UID)
	blob := filepath.Join(stateDir, "cur", "upper", "srv", "r", "blob")
	// on xbind's host, as its user: owned by a sub-uid, unreadable
	if got := hostSh(t, d, fmt.Sprintf(`u=$(stat -c %%u '%s') || exit 1; [ "$u" != "$(id -u)" ] && [ "$u" != 0 ] && echo sub-uid || echo "uid $u"; cat '%s' >/dev/null 2>&1 && echo readable || echo denied`, blob, blob)); got != "sub-uid\ndenied\n" {
		t.Errorf("the upper's file on the host: %q (want a sub-uid's, unreadable to xbind's user)", got)
	}
	var stopped sandboxInfo
	owner.must(t, "POST", "/sandboxes/ranged/stop", nil, 200, &stopped)
	if stopped.State != "stopped" {
		t.Errorf("stopped: %+v", stopped)
	}
	// measured (a confined du, after the stop): the sub-uid's 4 MiB count
	xbindtest.Eventually(t, time.Minute, "the upper measured", func() (bool, string) {
		in := owner.get(t, "ranged")
		return in.DiskBytes >= 4<<20, fmt.Sprint(in.DiskBytes)
	})
	var sn snapshotInfo
	owner.must(t, "POST", "/sandboxes/ranged/snapshots", map[string]string{"name": "ranged"}, 201, &sn)
	if sn.Bytes < 4<<20 {
		t.Errorf("the snapshot: %+v", sn)
	}
	owner.run(t, "ranged", "rm -rf /srv/r")
	owner.must(t, "POST", "/sandboxes/ranged/snapshots/"+sn.ID+"/restore", nil, 200, nil)
	if res := owner.run(t, "ranged", "stat -c '%u:%g %a' /srv/r/blob; readlink /srv/r/link; wc -c < /srv/r/blob"); res.Stdout.Head != "1000:1000 600\n/etc/shadow\n4194304\n" {
		t.Errorf("restored: %q", res.Stdout.Head)
	}
	owner.must(t, "DELETE", "/sandboxes/ranged", nil, 204, nil)
	xbindtest.Eventually(t, 2*time.Minute, "the sandbox's state removed", func() (bool, string) {
		left := hostSh(t, d, fmt.Sprintf(`[ -e '%s' ] && echo '%s'; ls -A '%s/.trash' 2>/dev/null; true`, stateDir, stateDir, filepath.Dir(stateDir)))
		return left == "", left
	})
}

// findStateDir is .xbin/sbx/<CK>/<name>.<uid> on xbind's host.
func findStateDir(t *testing.T, d *xbindtest.Daemon, name, uid string) string {
	m := strings.Fields(hostSh(t, d, fmt.Sprintf(`ls -d '%s'/.xbin/sbx/*/'%s.%s' 2>/dev/null; true`, d.Workspace(), name, uid)))
	if len(m) != 1 {
		t.Fatalf("the state dir of %s.%s: %v", name, uid, m)
	}
	return m[0]
}

// hostSh runs script on xbind's host as its user (xbindtest HostSh).
func hostSh(t *testing.T, d *xbindtest.Daemon, script string) string {
	t.Helper()
	out, err := d.HostSh(script)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --- the terminal wire -------------------------------------------------------

type sessionFrame struct {
	Op, ID, Sandbox string
	EchoAck         bool
}

func readSession(t *testing.T, c *websocket.Conn) sessionFrame {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
	for {
		kind, b, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for the session frame: %v", err)
		}
		if kind != websocket.TextMessage {
			continue
		}
		var f sessionFrame
		if json.Unmarshal(b, &f) == nil && f.Op == "session" {
			return f
		}
	}
}

func send(t *testing.T, c *websocket.Conn, s string) {
	t.Helper()
	if err := c.WriteMessage(websocket.BinaryMessage, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

// readUntil reads terminal bytes until they hold want.
func readUntil(t *testing.T, c *websocket.Conn, want string, timeout time.Duration) {
	t.Helper()
	var seen bytes.Buffer
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	for !strings.Contains(seen.String(), want) {
		kind, b, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for %q: %v (saw %q)", want, err, seen.String())
		}
		if kind == websocket.BinaryMessage {
			seen.Write(b)
		}
	}
}

// readExit reads to the exit frame and returns its code.
func readExit(t *testing.T, c *websocket.Conn, timeout time.Duration) *int {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	for {
		kind, b, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for the exit frame: %v", err)
		}
		var f struct {
			Op   string
			Code *int
		}
		if kind == websocket.TextMessage && json.Unmarshal(b, &f) == nil && f.Op == "exit" {
			return f.Code
		}
	}
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		names = append(names, strings.TrimPrefix(h.Name, "./"))
	}
}

func baseVersion(t *testing.T, rootfs string) string {
	b, err := os.ReadFile(filepath.Join(rootfs, "etc", "xbin-base-version"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
