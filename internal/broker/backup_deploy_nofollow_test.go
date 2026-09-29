package broker

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/util"
)

// backupTripwire is a FIFO outside every tree under test, polled with
// open(O_WRONLY|O_NONBLOCK), which succeeds only while something holds it
// open for reading: a following open of a symlink to it trips it, and the
// probe's open lets that reader through, so nothing hangs. The detector of
// internal/confine's TestNoFollowingHostWalks, for this package's case.
type backupTripwire struct {
	path    string
	tripped atomic.Bool
}

func newBackupTripwire(t *testing.T) *backupTripwire {
	t.Helper()
	w := &backupTripwire{path: filepath.Join(t.TempDir(), "tripwire")}
	if err := syscall.Mkfifo(w.path, 0o600); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			if fd, err := syscall.Open(w.path, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err == nil {
				w.tripped.Store(true)
				syscall.Close(fd)
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
	return w
}

// covers D119g T2 T11 — rule C5 for a tile's backup of its deployment state,
// behaviourally: a checkpoint store whose packed-refs, a ref, a loose
// object and the pack directory are symlinks out of it (to the FIFO, to a
// directory holding a pack), and whose deploy-log ref is a FIFO of its own,
// is archived without anything opening them — only its one regular object
// goes in — and a record file that is a symlink to the FIFO leaves the
// tile's archive today's. The hostile-tree case the integrator wires into
// internal/confine's TestNoFollowingHostWalks.
func TestBackupDeploymentsNoFollow(t *testing.T) {
	w := newBackupTripwire(t)
	b := plaintextVault(zeroDataBroker(t))
	root := b.Reg.Root
	arch := &zeroDataArchiver{}
	b.ProxyHandler = arch
	backupMembers := func() []string {
		t.Helper()
		done := make(chan error, 1)
		go func() { _, err := b.doBackup("apps/cal"); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("backup: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the backup hung: it opened a FIFO")
		}
		arch.mu.Lock()
		defer arch.mu.Unlock()
		var out []string
		for _, m := range readArchive(t, arch.body) {
			if strings.HasPrefix(m.name, backup.DeploymentsPrefix) {
				out = append(out, m.name)
			}
		}
		return out
	}

	outside := t.TempDir()
	pack := "pack-" + strings.Repeat("1", 40) + ".pack"
	if err := os.WriteFile(filepath.Join(outside, pack), []byte("PACK"), 0o444); err != nil {
		t.Fatal(err)
	}
	store := checkpointStoreDir(root, "apps/cal")
	regular := "objects/cd/" + strings.Repeat("2", 38)
	for rel, body := range map[string]string{"HEAD": "ref: refs/heads/deploy/main\n", regular: "x", "config": "[core]\n"} {
		p := filepath.Join(store, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	for rel, target := range map[string]string{
		"packed-refs": w.path,
		"refs/xbin/checkpoints/" + strings.Repeat("3", 40): w.path,
		"objects/ab/" + strings.Repeat("4", 38):            w.path,
		"objects/pack":                                     outside,
	} {
		p := filepath.Join(store, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(store, "refs", "xbin", "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(store, "refs", "xbin", "log", "main"), 0o600); err != nil {
		t.Fatal(err)
	}

	// the record is a symlink to the FIFO: no record, today's archive
	recDir := filepath.Join(root, "data", "deployments")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recFile := filepath.Join(recDir, util.TileKey("apps/cal")+".json")
	if err := os.Symlink(w.path, recFile); err != nil {
		t.Fatal(err)
	}
	if got := backupMembers(); len(got) != 0 {
		t.Errorf("a symlinked record put deployment state in the archive: %v", got)
	}

	// the tile's record: the store's one regular object goes in, nothing else
	if err := os.Remove(recFile); err != nil {
		t.Fatal(err)
	}
	writeTestRecord(t, root, "apps/cal", testRecord("apps/cal", "", ""))
	got := backupMembers()
	sort.Strings(got)
	want := []string{backup.CheckpointsPrefix + regular, backup.RecordName}
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the hostile store archived %v, want %v", got, want)
	}
	if w.tripped.Load() {
		t.Fatal("the backup opened the FIFO outside the store")
	}
}
