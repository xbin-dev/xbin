package term

import (
	"os/exec"
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
