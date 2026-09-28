//go:build linux && integration

// Run with: go test -tags=integration ./internal/broker/
// The seed's confined copies in real sandboxes (06-security ledger L10,
// L11): they need user namespaces and an unpacked rootfs with rsync and
// python3 (XBIN_TEST_ROOTFS, or the repo's .rootfs from `make rootfs`), and
// run over real gocryptfs volumes when XBIN_GOCRYPTFS names the binary and
// FUSE works, over deploy_seed_test.go's plain-directory stand-ins
// otherwise. Each skips with the reason. TestMain, the sandbox's re-exec
// init, is backup_deploy_linux_test.go's.
package broker

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// seedIsolated turns confinement on over the test rootfs, or skips.
func seedIsolated(t *testing.T) {
	t.Helper()
	rootfs := os.Getenv("XBIN_TEST_ROOTFS")
	if rootfs == "" {
		rootfs, _ = filepath.Abs("../../.rootfs")
	}
	for _, tool := range []string{"rsync", "python3", "cp"} {
		if _, err := os.Stat(filepath.Join(rootfs, "usr", "bin", tool)); err != nil {
			t.Skipf("no rootfs with %s (%s)", tool, rootfs)
		}
	}
	if !sandbox.Available() {
		t.Skip("no user namespaces")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no host python3 to write and check the databases")
	}
	confine.Configure(rootfs)
	t.Cleanup(func() { confine.Configure("") })
	if !confine.Isolated() {
		t.Fatal("not isolated")
	}
}

// newConfinedSeedFx is newSeedFx over real gocryptfs volumes when it can be
// (real true), the plain-directory stand-ins otherwise.
func newConfinedSeedFx(t *testing.T) (f *seedFx, real bool) {
	t.Helper()
	f = newSeedFx(t)
	bin := os.Getenv("XBIN_GOCRYPTFS")
	if fi, err := os.Stat(bin); bin == "" || err != nil || fi.Mode()&0o111 == 0 || !exists("/dev/fuse") {
		t.Log("XBIN_GOCRYPTFS names no gocryptfs, or no FUSE: over plain directories")
		return f, false
	}
	b := f.b
	prev := volumeMounted
	volumeMounted = func(m *resenc.Manager, k resKeys) bool { return m.Mounted(k.DirKey, k.Name) }
	t.Cleanup(func() { volumeMounted = prev })
	for _, dir := range []string{filepath.Join(f.root, "data", "resources-enc"), filepath.Join(f.root, ".xbin", "resenc")} {
		if err := os.RemoveAll(dir); err != nil { // the stand-ins' volumes
			t.Fatal(err)
		}
	}
	b.resenc = resenc.New(b.Reg.Root, bin, func(label string) ([]byte, error) { return b.barrier.DeriveKey(label) })
	t.Cleanup(func() { b.resenc.UnmountAll() })
	b.MountEncrypted()
	if k := f.keys(fxCalendar, util.MainDeployment, "files"); !b.resenc.Mounted(k.DirKey, k.Name) {
		t.Skip("gocryptfs didn't mount main's volumes here")
	}
	return f, true
}

// pyRun runs a host python3 script with args.
func pyRun(t *testing.T, script string, args ...string) string {
	t.Helper()
	out, err := exec.Command("python3", append([]string{"-c", script}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("python3: %v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

const pyCheck = `import sqlite3, sys
c = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
print(c.execute("pragma integrity_check").fetchone()[0], c.execute("select count(*) from t").fetchone()[0])`

// covers T11 T20 L11 Q9 08-data §8.3 — a sqlite seed in confine, online: a
// WAL database the primary keeps writing (a host writer holds it open, its
// -wal and -shm live) is copied through the backup API from a mode=ro
// connection with the primary's volume bound read-write, into a copy that
// passes integrity_check and holds a point in time; a database kept inside a
// filesystem resource is copied the same way, and a symlink named like one
// is kept as a link, never opened. Over gocryptfs when XBIN_GOCRYPTFS is set.
func TestSeedSqliteConfined(t *testing.T) {
	seedIsolated(t)
	f, real := newConfinedSeedFx(t)
	token := filepath.Join(f.root, ".xbin", "token")
	nsWrite(t, filepath.Dir(token), map[string]string{"token": "SECRET-TOKEN"})
	db := filepath.Join(f.mount(fxCalendar, util.MainDeployment, "db"), "db.sqlite")
	pyRun(t, `import sqlite3, sys
c = sqlite3.connect(sys.argv[1]); c.execute("pragma journal_mode=wal"); c.execute("create table t(x)")
c.executemany("insert into t values(?)", [(i,) for i in range(200)]); c.commit()`, db)
	files := f.mount(fxCalendar, util.MainDeployment, "files")
	inner := filepath.Join(files, "inner", "app.db")
	if err := os.MkdirAll(filepath.Dir(inner), 0o755); err != nil {
		t.Fatal(err)
	}
	pyRun(t, `import sqlite3, sys
c = sqlite3.connect(sys.argv[1]); c.execute("create table t(x)")
c.executemany("insert into t values(?)", [(i,) for i in range(33)]); c.commit()`, inner)
	if err := os.Symlink(token, filepath.Join(files, "tok.db")); err != nil {
		t.Fatal(err)
	}

	stop := filepath.Join(t.TempDir(), "stop")
	writer := exec.Command("python3", "-c", `import os, sqlite3, sys, time
c = sqlite3.connect(sys.argv[1], isolation_level=None); c.execute("pragma journal_mode=wal")
i = 1000
while not os.path.exists(sys.argv[2]):
    c.execute("insert into t values(?)", (i,)); i += 1; time.sleep(0.002)
print(i - 1000)`, db, stop)
	var wout bytes.Buffer
	writer.Stdout, writer.Stderr = &wout, &wout
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}
	defer writer.Process.Kill()
	for deadline := time.Now().Add(10 * time.Second); !exists(db + "-wal"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the writer never opened the WAL")
		}
	}

	_, err := f.seed(deployPerson(t, f.b, fxAdmin), seedReq(fxCalendar, "dev"))
	if werr := os.WriteFile(stop, nil, 0o600); werr != nil {
		t.Fatal(werr)
	}
	if werr := writer.Wait(); werr != nil {
		t.Fatalf("the writer: %v %s", werr, wout.String())
	}
	if err != nil {
		t.Fatal(err)
	}
	if m := f.meta(fxCalendar, "dev"); m.State != nsSeeded || m.Consistency != "online" {
		t.Fatalf("dev's ns.json: %+v", m)
	}
	written, _ := strconv.Atoi(strings.TrimSpace(wout.String()))
	devDB := filepath.Join(f.mount(fxCalendar, "dev", "db"), "db.sqlite")
	check, rows := "", 0
	if _, err := fmt.Sscan(pyRun(t, pyCheck, devDB), &check, &rows); err != nil || check != "ok" || rows < 200 || rows > 200+written {
		t.Errorf("dev's db.sqlite: %q, %d rows (%v): want ok and between 200 and %d", check, rows, err, 200+written)
	}
	if got := pyRun(t, pyCheck, db); !strings.HasPrefix(got, "ok ") {
		t.Errorf("main's database after the seed: %q", got)
	}
	if got := pyRun(t, pyCheck, filepath.Join(f.mount(fxCalendar, "dev", "files"), "inner", "app.db")); got != "ok 33" {
		t.Errorf("dev's files/inner/app.db: %q, want ok 33", got)
	}
	if l, err := os.Readlink(filepath.Join(f.mount(fxCalendar, "dev", "files"), "tok.db")); err != nil || l != token {
		t.Errorf("tok.db in dev: %q %v, want the symlink itself", l, err)
	}
	var tools []string
	for _, c := range f.runs {
		tools = append(tools, c.Argv[0])
	}
	if !slices.Equal(tools, []string{"rsync", "python3", "rsync", "python3"}) {
		t.Errorf("confined runs %v", tools)
	}
	if real {
		k := f.keys(fxCalendar, "dev", "db")
		ents, err := os.ReadDir(f.b.resenc.CipherDir(k.DirKey, k.Name))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if strings.Contains(e.Name(), "sqlite") {
				t.Errorf("dev's ciphertext holds a plaintext name %q", e.Name())
			}
		}
	}
}

// covers T8 T2 C5 L10 08-data §8.3 — the filesystem copy in confine: rsync
// in a sandbox that sees only the two volumes copies a symlink planted
// toward .xbin/token as the link (inside, its target doesn't even exist),
// keeps hard links and xattrs, leaves a FIFO out (the sandbox refuses
// mknod, so a special file would fail the copy), and none of the token
// reaches dev.
func TestSeedFilesystemConfined(t *testing.T) {
	seedIsolated(t)
	f, _ := newConfinedSeedFx(t)
	token := filepath.Join(f.root, ".xbin", "token")
	nsWrite(t, filepath.Dir(token), map[string]string{"token": "SECRET-TOKEN"})
	from := f.mount(fxCalendar, util.MainDeployment, "files")
	nsWrite(t, from, map[string]string{"a.txt": "alpha", "h1": "hard", "sub/c.txt": "gamma"})
	for _, err := range []error{os.Symlink(token, filepath.Join(from, "token-link")),
		os.Link(filepath.Join(from, "h1"), filepath.Join(from, "h2")), syscall.Mkfifo(filepath.Join(from, "fifo"), 0o600)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	xattr := unix.Setxattr(filepath.Join(from, "a.txt"), "user.seed", []byte("kept"), 0) == nil

	if _, err := f.seed(deployPerson(t, f.b, fxAdmin), seedReq(fxCalendar, "dev")); err != nil {
		t.Fatal(err)
	}
	if m := f.meta(fxCalendar, "dev"); m.State != nsSeeded {
		t.Fatalf("dev's ns.json: %+v", m)
	}
	to := f.mount(fxCalendar, "dev", "files")
	if l, err := os.Readlink(filepath.Join(to, "token-link")); err != nil || l != token {
		t.Errorf("token-link in dev: %q %v", l, err)
	}
	h1, err1 := os.Lstat(filepath.Join(to, "h1"))
	h2, err2 := os.Lstat(filepath.Join(to, "h2"))
	if err1 != nil || err2 != nil || !os.SameFile(h1, h2) {
		t.Errorf("hard links not kept: %v %v", err1, err2)
	}
	if _, err := os.Lstat(filepath.Join(to, "fifo")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the FIFO was copied (%v): the sandbox makes no special file", err)
	}
	buf := make([]byte, 16)
	if n, err := unix.Getxattr(filepath.Join(to, "a.txt"), "user.seed", buf); xattr && (err != nil || string(buf[:n]) != "kept") {
		t.Errorf("the xattr: %q %v", buf[:n], err)
	}
	for rel, want := range map[string]string{"a.txt": "alpha", "sub/c.txt": "gamma"} {
		if got, err := os.ReadFile(filepath.Join(to, rel)); err != nil || string(got) != want {
			t.Errorf("dev's %s: %q %v", rel, got, err)
		}
	}
	_ = filepath.WalkDir(to, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if data, _ := os.ReadFile(p); bytes.Contains(data, []byte("SECRET-TOKEN")) {
				t.Errorf("the token's content reached %s", p)
			}
		}
		return nil
	})
}

// covers T8 08-data §8.3 §14.2 (single-tenant seed) — the ciphertext copy
// of a single-tenant volume over real gocryptfs: the primary's cipher
// directory is copied in confine and its config re-wrapped, so the copy
// opens under dev's label with the primary's files, never under the
// primary's label, and the primary's volume is mounted again, untouched.
// (It drives the copy directly: a single-tenant mount needs
// user_allow_other, which the copy itself doesn't.)
func TestSeedCipherRewrap(t *testing.T) {
	seedIsolated(t)
	f, real := newConfinedSeedFx(t)
	if !real {
		t.Skip("the re-wrap needs real gocryptfs volumes (XBIN_GOCRYPTFS)")
	}
	b := f.b
	from := f.mount(fxCalendar, util.MainDeployment, "files")
	nsWrite(t, from, map[string]string{"layer/a": "alpha", "b": "beta"})
	r := seedRes{name: "files", typ: "filesystem", single: true,
		from: f.keys(fxCalendar, util.MainDeployment, "files"), to: f.keys(fxCalendar, "dev", "files")}
	if _, err := b.seedCipher(fxCalendar, r); err != nil {
		t.Fatal(err)
	}
	to := f.mount(fxCalendar, "dev", "files")
	for rel, want := range map[string]string{"layer/a": "alpha", "b": "beta"} {
		if got, err := os.ReadFile(filepath.Join(to, rel)); err != nil || string(got) != want {
			t.Errorf("dev's %s: %q %v", rel, got, err)
		}
		if got, err := os.ReadFile(filepath.Join(from, rel)); err != nil || string(got) != want {
			t.Errorf("main's %s after the copy: %q %v", rel, got, err)
		}
	}
	if !b.resenc.Mounted(r.from.DirKey, r.from.Name) {
		t.Error("the primary's volume wasn't mounted again")
	}
	// the copy's config opens under dev's label only
	cipher, probe := b.resenc.CipherDir(r.to.DirKey, r.to.Name), t.TempDir()
	k, err := b.barrier.DeriveKey("fs:" + r.from.FSLabel)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Getenv("XBIN_GOCRYPTFS"), "-q", "-passfile", "/dev/stdin", cipher, probe)
	cmd.Stdin = strings.NewReader(base64.RawStdEncoding.EncodeToString(k) + "\n")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || !strings.Contains(string(out), "Password incorrect") {
		_ = exec.Command("fusermount3", "-u", probe).Run()
		t.Errorf("dev's copy opened under the primary's label: %v %s", err, out)
	}
}
