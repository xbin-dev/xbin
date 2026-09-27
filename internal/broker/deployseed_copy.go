package broker

// deployseed_copy.go — a seed's copies of file-backed resources (08-data
// §8.3; 06-security ledger L10, L11, L12), for deployseed.go's runSeed:
// sqlite and filesystem through rsync and python3's backup API in confine,
// seeing only the two volumes; a single-tenant volume as its ciphertext,
// copied in confine and re-wrapped under the target's label; blob in
// process, each file opened beneath its volume. Each answers the bytes the
// target's ciphertext takes, and verifies what it copied.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// seedTimeout bounds each confined copy.
const seedTimeout = 4 * time.Hour

// seedRun runs one confined copy (confine.Run); tests watch what it gets.
var seedRun = confine.Run

// ---- sqlite and filesystem: confined (L10, L11) ----

// seedPy is the confined sqlite step (python3 - <from> <to> <strict>): each
// regular file under <from> that starts with SQLite's header, and <strict>
// (a sqlite resource's own database), is read through the backup API from
// a mode=ro connection — one point in time while the primary keeps writing —
// into a fresh file that must pass integrity_check, which then replaces
// rsync's copy and its sidecars. No link is followed. One JSON line per
// database; nothing of the data is printed.
const seedPy = `import contextlib, json, os, sqlite3, stat, sys, urllib.parse
src, dst, strict = sys.argv[1], sys.argv[2], sys.argv[3]
def is_db(p):
    try:
        fd = os.open(p, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError:
        return False
    with contextlib.closing(os.fdopen(fd, "rb")) as f:
        return stat.S_ISREG(os.fstat(fd).st_mode) and f.read(16) == b"SQLite format 3\x00"
def backup(rel):
    s, d = os.path.join(src, rel), os.path.join(dst, rel)
    st = os.lstat(s)
    if not stat.S_ISREG(st.st_mode):
        return None
    os.makedirs(os.path.dirname(d), exist_ok=True)
    tmp = d + ".xbin-seed-" + os.urandom(6).hex()
    try:
        with contextlib.closing(sqlite3.connect("file:" + urllib.parse.quote(s) + "?mode=ro", uri=True)) as sc, \
             contextlib.closing(sqlite3.connect(tmp)) as dc:
            sc.backup(dc, pages=-1)
            res = [str(r[0]) for r in dc.execute("PRAGMA integrity_check")]
    except BaseException:
        res = None
        raise
    finally:
        if res != ["ok"]:
            with contextlib.suppress(FileNotFoundError):
                os.unlink(tmp)
    if res != ["ok"]:
        return "integrity_check: " + "; ".join(res[:3])
    for side in (d + "-wal", d + "-shm", d + "-journal"):
        with contextlib.suppress(FileNotFoundError):
            os.unlink(side)
    os.replace(tmp, d)
    os.chmod(d, stat.S_IMODE(st.st_mode))
    os.utime(d, ns=(st.st_atime_ns, st.st_mtime_ns))
    return ""
for root, dirs, files in os.walk(src):
    for name in files:
        rel = os.path.relpath(os.path.join(root, name), src)
        if rel != strict and not is_db(os.path.join(src, rel)):
            continue
        try:
            err = backup(rel)
        except Exception as e:
            err = str(e) or type(e).__name__
        if err is not None:
            print(json.dumps({"rel": rel, "ok": not err, "error": err, "strict": rel == strict}), flush=True)
`

// seedFiles copies a sqlite or filesystem resource (§8.3) into a fresh
// volume of the target's, initialized under its label: rsync -aHX with the
// primary's volume bound read-only, then the databases through the backup
// API with it bound read-write, for a WAL reader's shared memory. Only the
// two volumes are visible, each at its own path. Special files are left
// out: the sandbox refuses mknod, and a backend's can make none but a
// socket, which is its own runtime state.
func (b *Broker) seedFiles(scope string, r seedRes) (int64, error) {
	from, to, err := b.seedVolumes(scope, r)
	if err != nil || from == "" {
		return 0, err
	}
	strict, args := "", []string{"rsync", "-aHX", "--no-D", "--numeric-ids"} // no FIFO, socket or device: confine refuses mknod
	if r.typ == "sqlite" {
		strict = r.name + ".sqlite"
		for _, s := range []string{"", "-wal", "-shm", "-journal"} {
			args = append(args, "--exclude=/"+strict+s)
		}
	}
	_, err = seedRun(context.Background(), confine.Cmd{Argv: append(args, from+"/", to+"/"), Dir: to,
		Binds: []sandbox.Bind{confine.RO(from)}, Timeout: seedTimeout, MaxOutput: 1 << 20})
	if code, ok := confine.ExitCode(err); err != nil && (!ok || code != 24) { // 24: files vanished mid-copy
		return 0, fmt.Errorf("rsync: %w", err)
	}
	out, err := seedRun(context.Background(), confine.Cmd{Argv: []string{"python3", "-I", "-", from, to, strict}, Dir: to,
		Binds: []sandbox.Bind{confine.RW(from)}, Stdin: strings.NewReader(seedPy), Timeout: seedTimeout, MaxOutput: 4 << 20})
	if err != nil {
		return 0, fmt.Errorf("sqlite backup: %w", err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(out.Stdout), []byte("\n")) {
		var db struct {
			Rel, Error string
			OK, Strict bool
		}
		switch {
		case len(line) == 0:
		case json.Unmarshal(line, &db) != nil:
			return 0, fmt.Errorf("sqlite backup: unreadable report %q", line)
		case !db.OK && db.Strict:
			return 0, fmt.Errorf("%s: %s", db.Rel, db.Error)
		case !db.OK:
			slog.Warn("seed: a database in a filesystem resource was copied file by file, not at one point in time",
				"resource", r.name, "file", db.Rel, "err", db.Error)
		}
	}
	n, _ := dirUsage(b.resenc.CipherDir(r.to.DirKey, r.to.Name)) // cipher dirs are xbind's own
	return n, nil
}

// seedVolumes mounts r's volume in the primary's namespace and a fresh one
// in the target's, answering both mount points; "" when the primary's was
// never written, and then the target starts empty and nothing is made.
func (b *Broker) seedVolumes(scope string, r seedRes) (from, to string, err error) {
	switch {
	case b.resenc == nil || !b.resenc.Encrypted(r.from.DirKey, r.from.Name):
		return "", "", nil
	case !b.ensureVolume(r.from, scope, r.typ):
		return "", "", fmt.Errorf("the primary's %s isn't mounted", r.name)
	case !b.ensureVolume(r.to, scope, r.typ):
		return "", "", fmt.Errorf("the new volume of %s didn't mount", r.name)
	}
	return b.resMount(r.from, false), b.resMount(r.to, false), nil
}

// seedCipher copies a single-tenant volume, whose virtual owners, modes and
// whiteouts no copy through the mount keeps (§8.3; stopped only): the
// primary's is unmounted so every cached write reaches its ciphertext, the
// cipher directory is copied with cp -a in confine, and its config
// re-wrapped under the target's label. The copy shares the primary volume's
// master key, as documented for this mode.
func (b *Broker) seedCipher(scope string, r seedRes) (int64, error) {
	if b.resenc == nil || !b.resenc.Encrypted(r.from.DirKey, r.from.Name) {
		return 0, nil
	}
	src, dst := b.resenc.CipherDir(r.from.DirKey, r.from.Name), b.resenc.CipherDir(r.to.DirKey, r.to.Name)
	if err := b.unmountUnder(b.resenc.MountDir(r.from.DirKey, r.from.Name), r.from.DirKey); err != nil {
		return 0, err
	}
	defer b.ensureVolume(r.from, scope, r.typ) // the primary's, for its restart
	err := b.unmountUnder(b.resenc.MountDir(r.to.DirKey, r.to.Name), r.to.DirKey)
	if err == nil {
		err = os.RemoveAll(dst) // xbind's own, and empty after the wipe
	}
	if err == nil {
		err = os.MkdirAll(dst, 0o700)
	}
	if err != nil {
		return 0, err
	}
	if _, err := seedRun(context.Background(), confine.Cmd{Argv: []string{"cp", "-a", "--reflink=auto", src + "/.", dst + "/"},
		Dir: dst, Binds: []sandbox.Bind{confine.RO(src)}, Timeout: seedTimeout, MaxOutput: 1 << 20}); err != nil {
		return 0, fmt.Errorf("cp: %w", err)
	}
	if err := b.rewrap(dst, r.from.FSLabel, r.to.FSLabel); err != nil {
		return 0, err
	}
	if !b.ensureVolume(r.to, scope, r.typ) {
		return 0, fmt.Errorf("%s doesn't open under its new key after the re-wrap", r.name)
	}
	n, _ := dirUsage(dst)
	return n, nil
}

// rewrap re-encrypts cipher's gocryptfs.conf (the master key) from label
// from's password to label to's: gocryptfs -passwd reads both on stdin, fed
// as resenc feeds its runs.
func (b *Broker) rewrap(cipher, from, to string) error {
	bin := resenc.Resolve()
	if bin == "" || b.barrier == nil {
		return errors.New("gocryptfs isn't available")
	}
	var pw [2]string
	for i, label := range []string{from, to} {
		k, err := b.barrier.DeriveKey("fs:" + label)
		if err != nil {
			return err
		}
		pw[i] = base64.RawStdEncoding.EncodeToString(k) // resenc's password encoding
		clear(k)
	}
	cmd := exec.Command(bin, "-passwd", "-q", cipher) // exec-ok: gocryptfs -passwd on a cipher dir under data/, xbind's own (L12)
	cmd.Stdin = strings.NewReader(pw[0] + "\n" + pw[1] + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gocryptfs -passwd: %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// ---- blob: in process, beneath the volume ----

// seedBlob copies a blob volume's regular files and directories (§8.3),
// which only the API writes and no sandbox is bound to, each opened beneath
// the primary's mount, links and special files skipped; then counts what
// the target holds: file counts and bytes must match.
func (b *Broker) seedBlob(scope string, r seedRes) (int64, error) {
	from, to, err := b.seedVolumes(scope, r)
	if err != nil || from == "" {
		return 0, err
	}
	var files, got [2]int64 // count, bytes
	if err := walkBeneath(from, func(rel string, f *os.File, fi fs.FileInfo) error {
		dst := filepath.Join(to, filepath.FromSlash(rel))
		if f == nil {
			return os.MkdirAll(dst, 0o755)
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, fi.Mode().Perm()) // walk-ok: the target's fresh volume, held
		if err != nil {
			return err
		}
		n, err := io.Copy(out, f)
		files[0], files[1] = files[0]+1, files[1]+n
		return cmp.Or(err, out.Close())
	}); err != nil {
		return 0, err
	}
	err = walkBeneath(to, func(_ string, f *os.File, fi fs.FileInfo) error {
		if f != nil {
			got[0], got[1] = got[0]+1, got[1]+fi.Size()
		}
		return nil
	})
	if err != nil || got != files {
		return 0, cmp.Or(err, fmt.Errorf("verifying: copied %d files (%d bytes), the copy holds %d (%d bytes)", files[0], files[1], got[0], got[1]))
	}
	n, _ := dirUsage(b.resenc.CipherDir(r.to.DirKey, r.to.Name)) // cipher dirs are xbind's own
	return n, nil
}

// walkBeneath visits root's directories (f nil) and regular files, parents
// first, each opened beneath root (fsutil.OpenBeneath): a symlink, FIFO,
// socket or device is skipped, never followed or opened. rel is
// slash-separated; fi is the open file's own.
func walkBeneath(root string, fn func(rel string, f *os.File, fi fs.FileInfo) error) error {
	for stack := []string{"."}; len(stack) > 0; {
		rel := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		d, err := fsutil.OpenBeneath(root, rel)
		if err != nil {
			return err
		}
		ents, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return err
		}
		for _, e := range ents {
			sub := path.Join(rel, e.Name())
			if e.IsDir() {
				stack = append(stack, sub)
				if err := fn(sub, nil, nil); err != nil {
					return err
				}
				continue
			} else if !e.Type().IsRegular() {
				continue
			}
			f, err := fsutil.OpenBeneath(root, sub)
			if errors.Is(err, fsutil.ErrNotRegular) || errors.Is(err, fsutil.ErrEscapes) || errors.Is(err, fs.ErrNotExist) {
				continue // swapped since the listing
			} else if err != nil {
				return err
			}
			fi, err := f.Stat()
			if err == nil && fi.Mode().IsRegular() {
				err = fn(sub, f, fi)
			}
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
