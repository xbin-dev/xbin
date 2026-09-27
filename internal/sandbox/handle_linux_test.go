//go:build linux

package sandbox

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// covers the WP-19 verifier's race (run with -race): a range-mode
// sandbox's teardown — Handle.Cleanup on the watcher's goroutine — may
// overtake a SetupUserns still running on the start's, when the init exits
// before its uid maps are written. Both close the sync pipe's write end;
// neither may touch state the other writes, the pipe closes once, and the
// init sees at most the one release byte, then EOF.
func TestHandleCleanupOvertakesSetup(t *testing.T) {
	arm := func(apply func() error) (*Handle, *os.File, string) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		spec := filepath.Join(t.TempDir(), "spec.json")
		if err := os.WriteFile(spec, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := &Handle{}
		h.arm(spec, w, apply)
		return h, r, spec
	}
	initSees := func(r *os.File) []byte {
		t.Helper()
		defer r.Close()
		b, err := io.ReadAll(r) // EOF only once every write end is closed
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// at once, unordered: what -race checks
	for i := 0; i < 50; i++ {
		failed := i%2 == 1 // newuidmap failing, or the maps written
		h, r, spec := arm(func() error {
			if failed {
				return errors.New("newuidmap: no")
			}
			return nil
		})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = h.SetupUserns() }()
		go func() { defer wg.Done(); h.Cleanup() }()
		wg.Wait()
		if b := initSees(r); len(b) > 1 || failed && len(b) != 0 {
			t.Fatalf("round %d: the init read %q", i, b)
		}
		if _, err := os.Stat(spec); !os.IsNotExist(err) {
			t.Fatalf("round %d: the spec is left: %v", i, err)
		}
	}

	// the teardown first: the late setup releases nothing
	inApply, cont := make(chan struct{}), make(chan struct{})
	h, r, _ := arm(func() error { close(inApply); <-cont; return nil })
	done := make(chan error, 1)
	go func() { done <- h.SetupUserns() }()
	<-inApply
	h.Cleanup()
	close(cont)
	if err := <-done; err == nil {
		t.Error("SetupUserns released an init its teardown had already let go")
	}
	if b := initSees(r); len(b) != 0 {
		t.Errorf("the init read %q after its teardown", b)
	}

	// the usual order: the byte, then the teardown's close is a no-op
	h, r, _ = arm(func() error { return nil })
	if err := h.SetupUserns(); err != nil {
		t.Fatal(err)
	}
	h.Cleanup()
	if b := initSees(r); string(b) != "\x01" {
		t.Errorf("the init read %q, want the release byte", b)
	}
}
