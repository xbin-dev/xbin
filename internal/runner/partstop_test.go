package runner

// covers S13 PD-20 PD-26 — stops that land while a person's partition
// starts (plans/partitions/03 §A.8, 02 §2, 01 §6): nothing spawns after a
// stop, no token registered mid-spawn outlives it, and a waiting stop
// returns only once the spawn in flight stopped what it spawned; a
// recreated person's old instance never serves again; a mode change
// revokes the retired primary's token before the publish and wakes an
// alwaysOn global instance after it; a stopped partition's run dir goes.

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// tokenWorld is a partWorld with real instance tokens: the fake's start
// registers each generation's through the runner (registerGen), as
// startDeployment does, and the partition hook binds it in auth.
type tokenWorld struct {
	*partWorld
	a *auth.Auth
}

func newTokenWorld(t *testing.T, spec registry.PartitionSpec, m registry.Manifest) *tokenWorld {
	t.Helper()
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	w := &tokenWorld{partWorld: newPartWorld(t, spec, m), a: a}
	w.r.Auth = a
	hook := w.r.RegisterPartitionInstance
	w.r.RegisterPartitionInstance = func(token, tile, dep, part, uid string) {
		a.RegisterInstance(token, tile)
		hook(token, tile, dep, part, uid)
	}
	e := w.r.engine
	start := e.start
	var n atomic.Int64
	e.start = func(c *registry.Component, bin string, gen int) (*instance, error) {
		inst, err := start(c, bin, gen)
		if err == nil {
			inst.token = fmt.Sprintf("tok-%d", n.Add(1))
			w.r.registerGen(inst.token, c, "main")
		}
		return inst, err
	}
	return w
}

func (w *tokenWorld) authn(tok string) bool {
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	_, ok := w.a.FromRequest(req)
	return ok
}

// lastToken is the last token RegisterPartitionInstance registered.
func (w *tokenWorld) lastToken() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.tokens) == 0 {
		return ""
	}
	return strings.Fields(w.tokens[len(w.tokens)-1])[1]
}

func (w *tokenWorld) tokenCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.tokens)
}

// waitParked waits until the fake parked a build.
func (w *partWorld) waitParked() {
	w.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		w.f.mu.Lock()
		parked := w.f.parked != ""
		w.f.mu.Unlock()
		if parked {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatal("no build parked")
		}
		time.Sleep(time.Millisecond)
	}
}

// covers S13 — TestPartitionStopDuringBuild: a stop that lands while a
// person's first build runs — a mode change, StopPartition — spawns
// nothing and registers no token once the build lands; the request is
// refused (ErrPartitionRefused), never "no deployment".
func TestPartitionStopDuringBuild(t *testing.T) {
	for name, stop := range map[string]func(w *tokenWorld){
		"a mode change":    func(w *tokenWorld) { w.r.PartitionsChanged(w.c, userGlobal, registry.PartitionSpec{}) },
		"StopPartition":    func(w *tokenWorld) { w.r.StopPartition("apps/x", "main", "user:alice") },
		"eviction-like":    func(w *tokenWorld) { w.r.stopPart(w.state("user:alice"), false) },
		"StopPartitionsOf": func(w *tokenWorld) { w.r.StopPartitionsOf("alice") },
	} {
		t.Run(name, func(t *testing.T) {
			w := newTokenWorld(t, userGlobal, registry.Manifest{})
			w.f.holdNextBuild()
			errc := make(chan error, 1)
			go func() {
				_, err := w.r.EnsurePartition(context.Background(), w.c, "main", "user:alice", StartInteractive)
				errc <- err
			}()
			w.waitParked()
			stop(w)
			w.f.releaseBuild()
			err := <-errc
			if !errors.Is(err, ErrPartitionRefused) {
				t.Errorf("the request: %v, want a refusal", err)
			}
			if log := w.f.takeLog(); count(log, "start apps/x user:alice") != 0 {
				t.Errorf("a stopped partition spawned after its build: %q", log)
			}
			if n := w.tokenCount(); n != 0 {
				t.Errorf("a stopped partition registered %d tokens", n)
			}
		})
	}
}

// covers S13 — TestPartitionStopDuringSpawn: a stop that lands after the
// spawn registered its token but before the state holds the generation
// revokes that token at once, and a waiting stop returns only once the
// spawned generation stopped.
func TestPartitionStopDuringSpawn(t *testing.T) {
	w := newTokenWorld(t, userGlobal, registry.Manifest{})
	spawned, proceed := make(chan struct{}), make(chan struct{})
	e := w.r.engine
	start := e.start
	e.start = func(c *registry.Component, bin string, gen int) (*instance, error) {
		inst, err := start(c, bin, gen)
		if c.Partition != "" {
			close(spawned)
			<-proceed
		}
		return inst, err
	}
	errc := make(chan error, 1)
	go func() {
		_, err := w.r.EnsurePartition(context.Background(), w.c, "main", "user:alice", StartInteractive)
		errc <- err
	}()
	<-spawned
	tok := w.lastToken()
	if tok == "" || !w.authn(tok) {
		t.Fatalf("the spawning generation's token %q doesn't authenticate", tok)
	}
	stopped := make(chan struct{})
	go func() { w.r.StopPartition("apps/x", "main", "user:alice"); close(stopped) }()
	for deadline := time.Now().Add(2 * time.Second); w.authn(tok); {
		if time.Now().After(deadline) {
			t.Fatal("the token registered mid-spawn outlived the stop")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-stopped:
		t.Error("the waiting stop returned before the spawn in flight stopped")
	case <-time.After(20 * time.Millisecond):
	}
	close(proceed)
	<-stopped
	if log := w.f.takeLog(); count(log, "stop apps/x user:alice main g1") != 1 {
		t.Errorf("the spawned generation wasn't stopped by the time the stop returned: %q", log)
	}
	if err := <-errc; !errors.Is(err, ErrPartitionRefused) || !strings.Contains(err.Error(), "stopped while it started") {
		t.Errorf("the request: %v", err)
	}
	if w.state("user:alice") != nil {
		t.Error("the stopped state is still kept")
	}
}

// covers PD-20 S13 — TestPartitionRecreatedPerson: a person deleted and
// made again has a new uid and pkey; their old instance stops on the next
// change (never restarts) and on their next request, which starts the new
// incarnation, registered with the new uid.
func TestPartitionRecreatedPerson(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.ensure("user:alice")
	w.mu.Lock()
	w.uids["user:alice"] = "uid-alice-2"
	w.mu.Unlock()
	w.f.takeLog()
	w.r.Changed(w.c)
	w.settleParts()
	if log := w.f.takeLog(); count(log, "start apps/x user:alice") != 0 || count(log, "stop apps/x user:alice") != 1 {
		t.Errorf("the old incarnation after a change: %q, want stopped, not restarted", log)
	}
	if w.state("user:alice") != nil {
		t.Error("the old incarnation's state is kept")
	}
	if got := w.ensure("user:alice"); got != "g1" {
		t.Fatalf("the new incarnation: %s", got)
	}

	// recreated again, no change: the request stops the old one first
	pk2 := w.pkeyOf("user:alice")
	w.mu.Lock()
	w.uids["user:alice"] = "uid-alice-3"
	w.mu.Unlock()
	w.f.takeLog()
	if got := w.ensure("user:alice"); got != "g1" {
		t.Fatalf("the third incarnation: %s", got)
	}
	w.settleParts()
	if w.r.existingPart(partStateKey("apps/x", "main", pk2)) != nil {
		t.Error("the second incarnation still runs beside the third")
	}
	if log := w.f.takeLog(); count(log, "stop apps/x user:alice") != 1 || count(log, "start apps/x user:alice") != 1 {
		t.Errorf("the third incarnation's start: %q", log)
	}
	s := w.r.existingPart(partStateKey("apps/x", "main", w.pkeyOf("user:alice")))
	if s == nil || s.pt.uid != "uid-alice-3" {
		t.Errorf("the new state's uid: %+v", s)
	}
}

// covers S13 01 §6 — TestPartitionsChangedRetiresPrimary: a mode change
// that retires the primary's generation revokes its token before it
// returns (before the publish), and wakes an alwaysOn global instance once
// the scan is out, with no watcher event.
func TestPartitionsChangedRetiresPrimary(t *testing.T) {
	w := newTokenWorld(t, registry.PartitionSpec{}, registry.Manifest{AlwaysOn: true})
	w.ensure("")
	s := w.r.existingStateOf("apps/x", "main")
	s.mu.Lock()
	s.cur.token = "tok-primary"
	s.mu.Unlock()
	w.a.RegisterInstance("tok-primary", "apps/x")
	w.f.takeLog()
	w.r.PartitionsChanged(w.c, registry.PartitionSpec{}, userGlobal) // today's instance becomes the global instance
	if w.authn("tok-primary") {
		t.Error("the retired primary's token still authenticates after PartitionsChanged returned")
	}
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	if cur != nil {
		t.Error("the retired generation is still current")
	}
	deadline := time.Now().Add(3 * time.Second)
	for count(w.f.takeLogPeek(), "start apps/x main") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the alwaysOn global instance never came back: %q", w.f.takeLogPeek())
		}
		time.Sleep(5 * time.Millisecond)
	}
	settle(t, w.r, w.f)
}

// covers 03 §A.8 — a stopped partition's run dir goes once it drained,
// unless the partition started again meanwhile.
func TestPartitionRunDirRemoved(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.r.RunDir = t.TempDir()
	w.ensure("user:alice")
	dir := filepath.Join(w.r.RunDir, partSockDir("apps/x", "main", fakePkey("user:alice")))
	if err := os.MkdirAll(dir, 0o755); err != nil { // the fake spawns nothing: what spawnSetup makes
		t.Fatal(err)
	}
	w.r.StopPartition("apps/x", "main", "user:alice")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the run dir after the stop: %v", err)
	}
	w.ensure("user:alice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := w.state("user:alice")
	w.r.stopPart(old, true)
	w.ensure("user:alice") // started again, which made its run dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	w.r.removePartDir(old) // a late drain of the old state
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("a late drain removed the new state's run dir: %v", err)
	}
}
