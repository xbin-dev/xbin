package term

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// HoldEnv (WP-9): a restore takes the layer only once the sessions holding it
// have let go, and while it holds the layer no new session mounts it.
func TestHoldEnv(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	key := termKey("apps/x")
	if !m.acquireEnv(key) {
		t.Fatal("acquire")
	}
	s := stub(m, "s1", "alice", "apps/x", time.Now())
	s.envKey, s.cmd = key, &exec.Cmd{} // no process: kill is a no-op, teardown below
	// The killed session's pump tearing down.
	go func() {
		time.Sleep(200 * time.Millisecond)
		m.remove(s.ID)
		m.releaseEnv(key)
	}()
	t0 := time.Now()
	release, err := m.HoldEnv("apps/x")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(t0) < 150*time.Millisecond {
		t.Fatal("held the layer while a session still had it")
	}
	if m.acquireEnv(key) {
		t.Fatal("a new session got the held layer")
	}
	release()
	release() // idempotent
	if !m.acquireEnv(key) {
		t.Fatal("the layer stayed held after release")
	}

	// A session that never lets go: HoldEnv gives up rather than swap under it.
	old := holdEnvWait
	holdEnvWait = 300 * time.Millisecond
	defer func() { holdEnvWait = old }()
	if _, err := m.HoldEnv("apps/x"); err == nil {
		t.Fatal("held a layer a session still has")
	}
}

// ResetEnv (WP-9b): the sessions holding the layer are killed and the layer
// held before it is removed — by the confined removal, never xbind's own —
// and released after; a session that won't let go fails the reset with the
// layer untouched.
func TestResetEnvHoldsThenRemovesConfined(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	key := termKey("apps/x")
	layer := filepath.Join(m.Root, ".xbin", "term", key)
	if err := os.MkdirAll(filepath.Join(layer, "upper", "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	var removed []string
	m.rmTree = func(dir string) error {
		removed = append(removed, dir)
		m.mu.Lock()
		live, held := len(m.sessions), m.envHeld[key]
		m.mu.Unlock()
		if live != 0 || !held {
			t.Errorf("removed with %d sessions live, held=%v", live, held)
		}
		return os.RemoveAll(dir)
	}
	live := func() *Session {
		if !m.acquireEnv(key) {
			t.Fatal("acquire")
		}
		s := stub(m, "s1", "alice", "apps/x", time.Now())
		s.envKey, s.cmd = key, &exec.Cmd{}
		return s
	}

	s := live()
	go func() { // the killed session's pump tearing down
		time.Sleep(200 * time.Millisecond)
		m.remove(s.ID)
		m.releaseEnv(key)
	}()
	if err := m.ResetEnv("apps/x"); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != layer {
		t.Fatalf("removed %v, want the layer %s", removed, layer)
	}
	if _, err := os.Lstat(layer); !os.IsNotExist(err) {
		t.Fatalf("the layer survived: %v", err)
	}
	if !m.acquireEnv(key) {
		t.Fatal("the layer stayed held after the reset")
	}
	m.releaseEnv(key)

	old := holdEnvWait
	holdEnvWait = 300 * time.Millisecond
	defer func() { holdEnvWait = old }()
	if err := os.MkdirAll(layer, 0o755); err != nil {
		t.Fatal(err)
	}
	removed = nil
	live() // never lets go
	if err := m.ResetEnv("apps/x"); err == nil {
		t.Fatal("reset a layer a session still holds")
	}
	if len(removed) != 0 {
		t.Fatalf("removed %v under a live session", removed)
	}
	if _, err := os.Lstat(layer); err != nil {
		t.Fatalf("the layer went: %v", err)
	}
}
