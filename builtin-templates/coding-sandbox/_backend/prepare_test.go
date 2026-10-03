package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepareScriptAccounts runs prepareScript itself (sh, awk) in a fake
// sandbox root (the directory the run starts in) with its own etc: the
// layout's user becomes the account of its uid as usermod -l and groupmod
// -n would leave it, an image's other names stay, and a second run changes
// nothing; a root without etc (the fake backend's host directory) is left
// alone — the host's /etc is never the one edited. The uid is the test's
// own, so the chown is a no-op.
func TestPrepareScriptAccounts(t *testing.T) {
	for _, tool := range []string{"sh", "awk", "stat"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s here", tool)
		}
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		t.Skip("as root the layout's user would be root, which the script leaves alone")
	}
	u, g, u1, g1 := fmt.Sprint(uid), fmt.Sprint(gid), fmt.Sprint(uid+1), fmt.Sprint(gid+1)
	sub := func(s string) string {
		return strings.NewReplacer("{U}", u, "{G}", g, "{U1}", u1, "{G1}", g1).Replace(s)
	}
	type files map[string]string
	run := func(t *testing.T, in files, user string) files {
		t.Helper()
		dir := t.TempDir()
		etc := filepath.Join(dir, "etc")
		if err := os.Mkdir(etc, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range in {
			mode := os.FileMode(0o644)
			if strings.Contains(name, "shadow") {
				mode = 0o640
			}
			if err := os.WriteFile(filepath.Join(etc, name), []byte(sub(body)), mode); err != nil {
				t.Fatal(err)
			}
		}
		out := files{}
		for pass := 1; pass <= 2; pass++ {
			cmd := exec.Command("sh", "-c", prepareScript, "prepare", filepath.Join(dir, "work"), filepath.Join(dir, "home/dev"),
				u+":"+g, user, "/bin/bash")
			cmd.Dir = dir // the sandbox's root: its etc is the one edited
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("pass %d: %v\n%s", pass, err, b)
			}
			got := files{}
			for name := range in {
				b, err := os.ReadFile(filepath.Join(etc, name))
				if err != nil {
					t.Fatal(err)
				}
				got[name] = strings.ReplaceAll(string(b), filepath.Join(dir, "home/dev"), "/home/dev")
				want := os.FileMode(0o644)
				if strings.Contains(name, "shadow") {
					want = 0o640
				}
				if fi, _ := os.Stat(filepath.Join(etc, name)); fi.Mode().Perm() != want {
					t.Errorf("pass %d: %s's mode is %v now (was %v)", pass, name, fi.Mode().Perm(), want)
				}
			}
			if pass == 2 {
				for name := range in {
					if got[name] != out[name] {
						t.Errorf("a second run changed %s:\n%s\nwas\n%s", name, got[name], out[name])
					}
				}
			}
			out = got
		}
		if _, err := os.Stat(filepath.Join(etc, "passwd.xbin-new")); err == nil {
			t.Error("a temporary file was left")
		}
		return out
	}
	check := func(t *testing.T, got files, want files) {
		t.Helper()
		for name, w := range want {
			if got[name] != sub(w) {
				t.Errorf("%s:\n%s\nwant\n%s", name, got[name], sub(w))
			}
		}
	}

	t.Run("a root without etc: nothing edited, the host's /etc untouched", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command("sh", "-c", prepareScript, "prepare", filepath.Join(dir, "work"), filepath.Join(dir, "home/dev"),
			u+":"+g, "dev", "/bin/bash")
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		if _, err := os.Stat(filepath.Join(dir, "etc")); !os.IsNotExist(err) {
			t.Errorf("an etc appeared in the root: %v", err)
		}
		for _, d := range []string{"work", "home/dev"} {
			if fi, err := os.Stat(filepath.Join(dir, d)); err != nil || !fi.IsDir() {
				t.Errorf("%s wasn't made: %v", d, err)
			}
		}
	})
	t.Run("an image's user of that uid is renamed", func(t *testing.T) {
		got := run(t, files{
			"passwd":  "root:x:0:0:root:/root:/bin/bash\nubuntu:x:{U}:{G}:Ubuntu:/home/ubuntu:/bin/sh\n",
			"group":   "root:x:0:\nadm:x:4:syslog,ubuntu\nsudo:x:27:ubuntu\nubuntu:x:{G}:\n",
			"shadow":  "root:*:19000:0:99999:7:::\nubuntu:!:19000:0:99999:7:::\n",
			"gshadow": "adm:*::syslog,ubuntu\nsudo:*:ubuntu:ubuntu\nubuntu:!::\n",
		}, "dev")
		check(t, got, files{
			"passwd":  "root:x:0:0:root:/root:/bin/bash\ndev:x:{U}:{G}:Ubuntu:/home/dev:/bin/bash\n",
			"group":   "root:x:0:\nadm:x:4:syslog,dev\nsudo:x:27:dev\ndev:x:{G}:\n",
			"shadow":  "root:*:19000:0:99999:7:::\ndev:!:19000:0:99999:7:::\n",
			"gshadow": "adm:*::syslog,dev\nsudo:*:dev:dev\ndev:!::\n",
		})
	})
	t.Run("no account of that uid: one is added", func(t *testing.T) {
		got := run(t, files{
			"passwd":  "root:x:0:0:root:/root:/bin/bash\n",
			"group":   "root:x:0:\n",
			"shadow":  "root:*:19000:0:99999:7:::\n",
			"gshadow": "root:*::\n",
		}, "dev")
		check(t, got, files{
			"passwd":  "root:x:0:0:root:/root:/bin/bash\ndev:x:{U}:{G}::/home/dev:/bin/bash\n",
			"group":   "root:x:0:\ndev:x:{G}:\n",
			"shadow":  "root:*:19000:0:99999:7:::\ndev:!:1::::::\n",
			"gshadow": "root:*::\ndev:!::\n",
		})
	})
	t.Run("the name is another uid's: nothing changes", func(t *testing.T) {
		in := files{
			"passwd": "dev:x:{U1}:{G1}::/home/dev:/bin/bash\nubuntu:x:{U}:{G}::/home/ubuntu:/bin/bash\n",
			"group":  "dev:x:{G1}:\nubuntu:x:{G}:\n",
		}
		check(t, run(t, in, "dev"), in)
	})
	t.Run("the group name is another gid's: the group stays", func(t *testing.T) {
		got := run(t, files{
			"passwd": "ubuntu:x:{U}:{G}::/home/ubuntu:/bin/bash\n",
			"group":  "dev:x:{G1}:\nubuntu:x:{G}:\nsudo:x:27:ubuntu\n",
		}, "dev")
		check(t, got, files{
			"passwd": "dev:x:{U}:{G}::/home/dev:/bin/bash\n",
			"group":  "dev:x:{G1}:\nubuntu:x:{G}:\nsudo:x:27:dev\n",
		})
	})
}

// TestPrepareScriptSudo: with "sudo" the account the script settled gets
// /etc/sudoers.d/<user> (0440, naming it: the guest drops supplementary
// groups) and /etc/xbin-vm-devices; a second run changes nothing and leaves
// no temporary file. Without it, neither; nor for a name another uid has.
func TestPrepareScriptSudo(t *testing.T) {
	for _, tool := range []string{"sh", "awk", "stat", "mv", "chmod"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s here", tool)
		}
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		t.Skip("as root the layout's user would be root, which the script leaves alone")
	}
	u, g := fmt.Sprint(uid), fmt.Sprint(gid)
	prep := func(t *testing.T, passwd, sudo string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "etc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "etc/passwd"), []byte(passwd), 0o644); err != nil {
			t.Fatal(err)
		}
		for pass := 1; pass <= 2; pass++ {
			cmd := exec.Command("sh", "-c", prepareScript, "prepare", filepath.Join(dir, "work"), filepath.Join(dir, "home/dev"),
				u+":"+g, "dev", "/bin/bash", sudo)
			cmd.Dir = dir
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("pass %d: %v\n%s", pass, err, b)
			}
		}
		return dir
	}
	file := func(t *testing.T, p string, mode os.FileMode) string {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("%s is %v, want %v", p, fi.Mode().Perm(), mode)
		}
		b, _ := os.ReadFile(p)
		return string(b)
	}

	dir := prep(t, "root:x:0:0:root:/root:/bin/bash\nubuntu:x:"+u+":"+g+"::/home/ubuntu:/bin/sh\n", "sudo")
	if got := file(t, filepath.Join(dir, "etc/sudoers.d/dev"), 0o440); got != "dev ALL=(ALL:ALL) NOPASSWD: ALL\n" {
		t.Errorf("the sudoers file: %q", got)
	}
	if got := file(t, filepath.Join(dir, "etc/xbin-vm-devices"), 0o644); !strings.HasPrefix(got, "# ") ||
		!strings.HasSuffix(got, "\n0666 /dev/fuse\n0666 /dev/net/tun\n") {
		t.Errorf("the device list: %q", got)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "etc/sudoers.d")); len(ents) != 1 {
		t.Errorf("sudoers.d holds %v", ents)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc/xbin-vm-devices.xbin-new")); err == nil {
		t.Error("a temporary file was left")
	}
	for name, c := range map[string]struct{ passwd, sudo string }{
		"without sudo":              {"ubuntu:x:" + u + ":" + g + "::/home/ubuntu:/bin/sh\n", ""},
		"the name is another uid's": {"dev:x:" + fmt.Sprint(uid+1) + ":" + g + "::/home/dev:/bin/sh\n", "sudo"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := prep(t, c.passwd, c.sudo)
			for _, p := range []string{"etc/sudoers.d", "etc/xbin-vm-devices"} {
				if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
					t.Errorf("%s is there: %v", p, err)
				}
			}
		})
	}
}
