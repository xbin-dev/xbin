package resenc

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeManager is a Manager over a stand-in gocryptfs: a script that logs
// each run's arguments and, on -init, waits (up to 3 s) until as many inits
// as want are under way at once before it writes gocryptfs.conf — so two
// volumes' inits succeed only if they run concurrently. Nothing mounts.
func fakeManager(t *testing.T, want int) (*Manager, func() string) {
	t.Helper()
	dir := t.TempDir()
	logf, inflight := filepath.Join(dir, "runs.log"), filepath.Join(dir, "inflight")
	if err := os.MkdirAll(inflight, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$@" >> ` + logf + `
for a; do last=$a; done
case "$1" in
-init)
  touch ` + inflight + `/$$
  i=0
  while [ "$(ls ` + inflight + ` | wc -l)" -lt ` + string(rune('0'+want)) + ` ] && [ $i -lt 60 ]; do sleep 0.05; i=$((i+1)); done
  [ "$(ls ` + inflight + ` | wc -l)" -ge ` + string(rune('0'+want)) + ` ] || exit 1
  echo '{}' > "$last/gocryptfs.conf" ;;
esac
exit 0
`
	bin := filepath.Join(dir, "gocryptfs")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(t.TempDir(), bin, func(label string) ([]byte, error) {
		h := sha256.Sum256([]byte(label))
		return h[:], nil
	})
	return m, func() string { b, _ := os.ReadFile(logf); return string(b) }
}

const partKey = ".partitions/apps~docs/main/u-0123456789abcdef0123456789abcdef/fs"

// covers PD-48 — volumes lock per (directory key, name), not manager-wide:
// two people's volumes initialize at the same time (the stand-in's inits
// each wait for the other); a new user partition's volume is initialized
// with -scryptn 10, and main's and a deployment's with gocryptfs's default.
func TestPartitionVolumesInitConcurrently(t *testing.T) {
	m, log := fakeManager(t, 2)
	other := strings.Replace(partKey, "u-0123", "u-9999", 1)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, key := range []string{partKey, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = m.Ensure(key+"/db", key, "db", false)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("volume %d: %v (inits didn't overlap: one lock for every volume?)", i, err)
		}
	}
	runs := log()
	if n := strings.Count(runs, "-init -q -passfile /dev/stdin -scryptn 10 "); n != 2 {
		t.Errorf("partition inits with -scryptn 10: %d\n%s", n, runs)
	}

	m2, log2 := fakeManager(t, 1)
	for _, key := range []string{"apps~docs", ".deployments/apps~docs/dev/fs"} {
		if _, err := m2.Ensure(key+"/db", key, "db", false); err != nil {
			t.Fatal(err)
		}
	}
	if runs := log2(); strings.Contains(runs, "-scryptn") || strings.Count(runs, "-init") != 2 {
		t.Errorf("main's and a deployment's inits:\n%s", runs)
	}
	// An initialized volume isn't initialized again.
	if _, err := m2.Ensure("apps~docs/db", "apps~docs", "db", false); err != nil || strings.Count(log2(), "-init") != 2 {
		t.Errorf("a second Ensure re-inits: %v\n%s", err, log2())
	}
}

// covers PD-48 — the refcounted idle unmount: a user partition's view is
// unmounted once nobody has held it for the idle period and the caller's
// keep doesn't claim it; a hold keeps it; main's and a deployment's views
// are never unmounted for idleness.
func TestPartitionVolumeIdleUnmount(t *testing.T) {
	m, _ := fakeManager(t, 1)
	for _, key := range []string{partKey, "apps~docs", ".deployments/apps~docs/dev/fs"} {
		if _, err := m.Ensure(key+"/db", key, "db", false); err != nil {
			t.Fatal(err)
		}
	}
	mounted := func() string {
		var out []string
		for _, mt := range m.Mounts() {
			out = append(out, mt.ScopeKey)
		}
		return strings.Join(out, ",")
	}
	all := mounted()
	now := time.Now()
	release := m.Hold(partKey, "db")
	if got := m.UnmountIdle(now.Add(2*time.Hour), time.Hour, nil); len(got) != 0 {
		t.Fatalf("unmounted while held: %+v", got)
	}
	release()
	release() // idempotent
	if got := m.UnmountIdle(time.Now().Add(30*time.Minute), time.Hour, nil); len(got) != 0 {
		t.Fatalf("unmounted before the idle period: %+v", got)
	}
	keep := func(scopeKey, _ string) bool { return scopeKey == partKey }
	if got := m.UnmountIdle(time.Now().Add(2*time.Hour), time.Hour, keep); len(got) != 0 {
		t.Fatalf("unmounted what keep claims: %+v", got)
	}
	got := m.UnmountIdle(time.Now().Add(2*time.Hour), time.Hour, nil)
	if len(got) != 1 || got[0].ScopeKey != partKey {
		t.Fatalf("idle unmount: %+v (mounted before: %s)", got, all)
	}
	if left := mounted(); strings.Contains(left, ".partitions") || !strings.Contains(left, "apps~docs") || !strings.Contains(left, ".deployments") {
		t.Errorf("left mounted: %s", left)
	}
	// It mounts again on its next use.
	if _, err := m.Ensure(partKey+"/db", partKey, "db", false); err != nil || !strings.Contains(mounted(), ".partitions") {
		t.Errorf("remount: %v (%s)", err, mounted())
	}
}

// covers PD-48 — the directory keys resenc takes: main's (one segment), a
// deployment's, and now a user partition's; nothing else.
func TestPartitionDirKeys(t *testing.T) {
	for key, want := range map[string]bool{
		"apps~docs": true, ".deployments/apps~docs/dev/fs": true, partKey: true,
		".partitions/apps~docs/main/fs":               false,
		".partitions/apps~docs/main/u-1/x":            false,
		".partitions/.x/main/u-1/fs":                  false,
		".partitions/apps~docs/../u-1/fs":             false,
		".deployments/apps~docs/dev/u-1/fs":           false,
		".partitions/apps~docs/main/u-1/fs/extra":     false,
		".partitions/apps~docs/main/u-1\x00/fs":       false,
		".other/apps~docs/main/u-0123456789abcdef/fs": false,
	} {
		if dirKeyOK(key) != want {
			t.Errorf("dirKeyOK(%q) = %v", key, !want)
		}
		if PartitionVolume(key) != (want && strings.HasPrefix(key, ".partitions/")) {
			t.Errorf("PartitionVolume(%q) = %v", key, !want)
		}
	}
}

// covers PD-48 — a real user partition's volume (FUSE; skipped without it):
// initialized with the cheap scrypt cost, it round-trips its data across an
// idle unmount and the next mount.
func TestPartitionVolumeRoundTrip(t *testing.T) {
	m, _ := testManager(t)
	t.Cleanup(m.UnmountAll)
	mnt, err := m.Ensure(partKey+"/files", partKey, "files", false)
	if err != nil {
		t.Skipf("gocryptfs mount failed (no userns/FUSE perms here?): %v", err)
	}
	conf, err := os.ReadFile(filepath.Join(m.CipherDir(partKey, "files"), "gocryptfs.conf"))
	if err != nil || !strings.Contains(string(conf), `"N": 1024`) {
		t.Errorf("the volume's scrypt cost isn't 2^10: %s %v", conf, err)
	}
	if err := os.WriteFile(filepath.Join(mnt, "note"), []byte("alice's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := m.UnmountIdle(time.Now().Add(2*time.Hour), time.Hour, nil); len(got) != 1 || m.Mounted(partKey, "files") {
		t.Fatalf("idle unmount: %+v, mounted %v", got, m.Mounted(partKey, "files"))
	}
	if _, err := m.Ensure(partKey+"/files", partKey, "files", false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(mnt, "note")); err != nil || string(b) != "alice's" {
		t.Errorf("after the remount: %q %v", b, err)
	}
}
