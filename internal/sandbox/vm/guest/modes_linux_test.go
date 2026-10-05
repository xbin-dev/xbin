//go:build linux

package guest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseModes(t *testing.T) {
	in := strings.Join([]string{
		"# the image's special modes",
		"4755 0 0 f /usr/bin/sudo.ws",
		"",
		"2755 0 42 f /usr/bin/chage",
		"1777 0 0 d /var/tmp",
		"2775 0 50 d /var/local",
		"4755 0 0 f /opt/my tool/helper", // a space in the path: the path is the rest of the line
		"0755 0 0 f /usr/bin/plain",      // no special bit
		"14755 0 0 f /usr/bin/x",         // over 07777
		"4755 0 0 l /usr/bin/link",       // a type the list doesn't carry
		"4755 0 0 f usr/bin/rel",         // relative
		"4755 0 0 f /usr/bin/../sbin/su", // not clean
		"1777 0 0 d /",                   // the root
		"4755 0 0 f",                     // too few fields
		"4755 -1 0 f /usr/bin/neg",       // not a uid
		"4755 0 0 f /usr/bin/sudo.ws",    // again
		"rwsr 0 0 f /usr/bin/word",       // not octal
	}, "\n")
	es, bad, err := parseModes(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []modeEntry{
		{mode: 0o4755, path: "/usr/bin/sudo.ws"},
		{mode: 0o2755, gid: 42, path: "/usr/bin/chage"},
		{mode: 0o1777, dir: true, path: "/var/tmp"},
		{mode: 0o2775, gid: 50, dir: true, path: "/var/local"},
		{mode: 0o4755, path: "/opt/my tool/helper"},
	}
	if fmt.Sprint(es) != fmt.Sprint(want) {
		t.Errorf("entries %v, want %v", es, want)
	}
	if bad != 10 {
		t.Errorf("bad %d, want 10", bad)
	}

	t.Run("too many entries: none", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i <= modesMaxEntries; i++ {
			fmt.Fprintf(&b, "4755 0 0 f /usr/bin/f%d\n", i)
		}
		if es, _, err := parseModes(strings.NewReader(b.String())); err == nil || es != nil {
			t.Errorf("a list over the entry bound: %d entries, %v", len(es), err)
		}
	})
	t.Run("too large: none", func(t *testing.T) {
		b := []byte("4755 0 0 f /usr/bin/su\n" + strings.Repeat("# padding\n", modesMaxBytes/10+1))
		if es, _, err := parseModes(bytes.NewReader(b)); err == nil || es != nil {
			t.Errorf("a list over the byte bound: %d entries, %v", len(es), err)
		}
	})
	t.Run("a line too long: none", func(t *testing.T) {
		b := "4755 0 0 f /" + strings.Repeat("x", 20<<10) + "\n4755 0 0 f /usr/bin/su\n"
		if es, _, err := parseModes(strings.NewReader(b)); err == nil || es != nil {
			t.Errorf("a list with a line over 16 KiB: %d entries, %v", len(es), err)
		}
	})
}

// modesTree makes an image under a temporary lower: files and directories
// as an unprivileged unpack leaves them (no special bits), its list, and a
// symbolic link or two. The owners are the test's own (a chown to them is
// allowed unprivileged).
func modesTree(t *testing.T, list string) (lower string, uid, gid int) {
	t.Helper()
	lower = t.TempDir()
	uid, gid = os.Getuid(), os.Getgid()
	for _, d := range []string{"usr/bin", "usr/lib", "var/tmp", "var/spool", "etc"} {
		if err := os.MkdirAll(filepath.Join(lower, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"usr/bin/su": "the su program", "usr/bin/plain": "plain", "usr/bin/already": "already"} {
		if err := os.WriteFile(filepath.Join(lower, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Chmod(filepath.Join(lower, "usr/bin/already"), 0o2755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"usr/bin/link": "su", "sbin": "usr/bin"} {
		if err := os.Symlink(target, filepath.Join(lower, link)); err != nil {
			t.Fatal(err)
		}
	}
	list = strings.NewReplacer("{U}", fmt.Sprint(uid), "{G}", fmt.Sprint(gid)).Replace(list)
	if err := os.WriteFile(filepath.Join(lower, modesManifest), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	return lower, uid, gid
}

func lstat(t *testing.T, p string) *unix.Stat_t {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return &st
}

// TestStageModes: a listed file the image carries without its modes is
// copied into the layer with them, its parents mirrored from the image; one
// the image carries as listed, a symbolic link, a path behind one, a missing
// path and one of the other type are left alone; a directory is planned for
// the assembled root; the layer has nothing else; a file past the budget is
// skipped whole.
func TestStageModes(t *testing.T) {
	lower, uid, gid := modesTree(t, `# test
4755 {U} {G} f /usr/bin/su
2755 {U} {G} f /usr/bin/already
1777 {U} {G} d /var/tmp
4755 {U} {G} f /usr/bin/link
4755 {U} {G} f /sbin/su
4755 {U} {G} f /usr/bin/missing
1777 {U} {G} d /usr/bin/plain
`)
	es := readModes(lower)
	if len(es) != 7 {
		t.Fatalf("read %d entries, want 7", len(es))
	}
	lfd, err := unix.Open(lower, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(lfd)
	files, dirs, skipped := planModes(lfd, es)
	if len(files) != 1 || files[0].path != "/usr/bin/su" || files[0].was.mode != 0o755 {
		t.Errorf("files to stage: %+v", files)
	}
	if len(dirs) != 1 || dirs[0].path != "/var/tmp" || dirs[0].was != (attrs{mode: 0o755, uid: uid, gid: gid}) {
		t.Errorf("directories to fix: %+v", dirs)
	}
	if skipped != 4 { // the link, the path behind one, the missing one, the file listed as a directory
		t.Errorf("skipped %d, want 4", skipped)
	}

	fix := t.TempDir()
	staged, n, sk := stageFiles(lower, fix, files, fixupMaxBytes)
	if staged != 1 || n != int64(len("the su program")) || sk != 0 {
		t.Errorf("staged %d (%d bytes), skipped %d", staged, n, sk)
	}
	st := lstat(t, filepath.Join(fix, "usr/bin/su"))
	if st.Mode&0o7777 != 0o4755 || int(st.Uid) != uid || int(st.Gid) != gid {
		t.Errorf("the staged su is %o %d:%d, want 4755 %d:%d", st.Mode&0o7777, st.Uid, st.Gid, uid, gid)
	}
	if b, _ := os.ReadFile(filepath.Join(fix, "usr/bin/su")); string(b) != "the su program" {
		t.Errorf("the staged su holds %q", b)
	}
	if ist := lstat(t, filepath.Join(lower, "usr/bin/su")); st.Mtim != ist.Mtim {
		t.Errorf("the staged su's mtime %v, the image's %v", st.Mtim, ist.Mtim)
	}
	for _, d := range []string{"usr", "usr/bin"} {
		got, img := lstat(t, filepath.Join(fix, d)), lstat(t, filepath.Join(lower, d))
		if attrsOf(got) != attrsOf(img) || got.Mtim != img.Mtim {
			t.Errorf("the layer's %s is %+v %v, the image's %+v %v", d, attrsOf(got), got.Mtim, attrsOf(img), img.Mtim)
		}
	}
	var all []string
	_ = filepath.Walk(fix, func(p string, _ os.FileInfo, _ error) error {
		all = append(all, strings.TrimPrefix(p, fix))
		return nil
	})
	if strings.Join(all, " ") != " /usr /usr/bin /usr/bin/su" {
		t.Errorf("the layer holds %q", all)
	}
	if ist := lstat(t, filepath.Join(lower, "usr/bin/su")); ist.Mode&0o7777 != 0o755 {
		t.Errorf("the image's su changed: %o", ist.Mode&0o7777)
	}

	t.Run("over the budget: skipped whole", func(t *testing.T) {
		fix := t.TempDir()
		staged, n, sk := stageFiles(lower, fix, files, 4)
		if staged != 0 || n != 0 || sk != 1 {
			t.Errorf("staged %d (%d bytes), skipped %d", staged, n, sk)
		}
		if ents, _ := os.ReadDir(fix); len(ents) != 0 {
			t.Errorf("the layer isn't empty: %v", ents)
		}
	})
	t.Run("a file the image changed since the plan: nothing staged", func(t *testing.T) {
		fix := t.TempDir()
		f := files[0]
		f.size++
		if staged, _, sk := stageFiles(lower, fix, []modeFix{f}, fixupMaxBytes); staged != 0 || sk != 1 {
			t.Errorf("staged %d, skipped %d", staged, sk)
		}
		if _, err := os.Lstat(filepath.Join(fix, "usr/bin/su")); !os.IsNotExist(err) {
			t.Errorf("a partial copy was left: %v", err)
		}
	})
	t.Run("no list: nothing", func(t *testing.T) {
		if es := readModes(t.TempDir()); es != nil {
			t.Errorf("entries without a list: %v", es)
		}
	})
	t.Run("a list behind a symbolic link: nothing", func(t *testing.T) {
		img := t.TempDir()
		if err := os.Symlink(filepath.Join(lower, "etc"), filepath.Join(img, "etc")); err != nil {
			t.Fatal(err)
		}
		if es := readModes(img); es != nil {
			t.Errorf("read through a symbolic link: %v", es)
		}
	})
}

// TestFixDirs: in the assembled root, a directory showing the image's
// unpacked mode and owner gets the listed ones; one the sandbox changed, one
// already right, a symbolic link and a missing one are left as they are.
func TestFixDirs(t *testing.T) {
	root := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	for d, mode := range map[string]os.FileMode{"var/tmp": 0o755, "var/spool": 0o700, "var/mail": 0o755, "real": 0o755} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, d), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Chmod(filepath.Join(root, "var/mail"), 0o2775); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	was := attrs{mode: 0o755, uid: uid, gid: gid}
	fix := func(p string, mode uint32) modeFix {
		return modeFix{modeEntry: modeEntry{mode: mode, uid: uid, gid: gid, dir: true, path: p}, was: was}
	}
	fixed, skipped := fixDirs(root, []modeFix{
		fix("/var/tmp", 0o1777),   // as unpacked: set
		fix("/var/spool", 0o1777), // the sandbox's own 0700: left
		fix("/var/mail", 0o2775),  // already right
		fix("/link", 0o1777),      // a symbolic link: never followed
		fix("/missing", 0o1777),
	})
	if fixed != 1 || skipped != 3 {
		t.Errorf("fixed %d, skipped %d; want 1, 3", fixed, skipped)
	}
	for d, want := range map[string]uint32{"var/tmp": 0o1777, "var/spool": 0o700, "var/mail": 0o2775, "real": 0o755} {
		if got := lstat(t, filepath.Join(root, d)).Mode & 0o7777; got != want {
			t.Errorf("%s is %o, want %o", d, got, want)
		}
	}
	if uid != 0 { // an owner the test can't give: refused, counted, the mode left
		if err := os.Mkdir(filepath.Join(root, "var/other"), 0o755); err != nil {
			t.Fatal(err)
		}
		other := fix("/var/other", 0o1777)
		other.uid = uid + 1
		if fixed, skipped := fixDirs(root, []modeFix{other}); fixed != 0 || skipped != 1 {
			t.Errorf("a chown refused: fixed %d, skipped %d", fixed, skipped)
		}
		if got := lstat(t, filepath.Join(root, "var/other")).Mode & 0o7777; got != 0o755 {
			t.Errorf("var/other is %o after a refused chown", got)
		}
	}
}

// recordModes is docker/rootfs.Dockerfile's recording step with its root
// as $1: keep the two the same.
const recordModes = `nl="$(printf '\n.')" && nl="${nl%.}" && find "$1" -xdev -name "*${nl}*" -prune -o \( -type f -o -type d \) -perm /7000 -printf '%m %U %G %y %p\n'`

// TestRecordModes pins the list's format to what the Dockerfile's step
// writes: GNU find's %m carries the special bits, a path with a space is
// whole, and a name with a newline (which would split its line) is left
// out.
func TestRecordModes(t *testing.T) {
	if _, err := exec.LookPath("find"); err != nil {
		t.Skip("no find here")
	}
	root := t.TempDir()
	for _, d := range []string{"sticky", "plain dir"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"setuid tool", "plain dir/setgid", "plain", "new\nline"} {
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, mode := range map[string]uint32{"sticky": 0o1777, "setuid tool": 0o4755, "plain dir/setgid": 0o2755, "new\nline": 0o4755} {
		if err := unix.Chmod(filepath.Join(root, p), mode); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("sh", "-c", recordModes, "record", root).Output()
	if err != nil {
		t.Skipf("the recording step: %v (not GNU find?)", err)
	}
	es, bad, err := parseModes(bytes.NewReader(out))
	if err != nil || bad != 0 {
		t.Fatalf("the step's output %q: bad %d, %v", out, bad, err)
	}
	got := map[string]uint32{}
	for _, e := range es {
		got[strings.TrimPrefix(e.path, root)] = e.mode
	}
	want := map[string]uint32{"/sticky": 0o1777, "/setuid tool": 0o4755, "/plain dir/setgid": 0o2755}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("recorded %v, want %v", got, want)
	}
}
