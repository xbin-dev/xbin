package runner

// covers PD-17 PD-18 PD-19 PD-20 PD-35 — people's partitions in the runner
// (plans/partitions/03 §A, §Tests "runner"): state keys, the gates of
// EnsurePartition, one build per change, admission (caps from memory,
// eviction, background limits), the 10-minute idle stop, alwaysOn for the
// global instance alone, stops and mode transitions, events to the person
// only. The fake engine drives the real runner; only the effects are fake.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// fakePkey is a person's partition id in these tests: "u-" and 32 hex
// digits of SHA-256(part).
func fakePkey(part string) string {
	h := sha256.Sum256([]byte(part))
	return "u-" + hex.EncodeToString(h[:16])
}

var (
	userGlobal = registry.PartitionSpec{User: true, Global: true}
	userOnly   = registry.PartitionSpec{User: true}
)

// partWorld is a fake-engine runner over apps/x, partitioned as spec, with
// every partition hook wired to a recorder.
type partWorld struct {
	t      *testing.T
	r      *Runner
	f      *fakeEngine
	tp     *tape
	c      *registry.Component
	mu     sync.Mutex
	denied map[string]bool   // ShouldRunPartition refuses these partitions
	events []string          // PartitionEvent: "<typ> <part>"
	tokens []string          // RegisterPartitionInstance: "<part> <token> <uid>"
	uids   map[string]string // a person's uid, when not "uid-<part>" (a recreated person's)
}

// uidOf is part's person's uid now; callers hold w.mu.
func (w *partWorld) uidOf(part string) string {
	if u, ok := w.uids[part]; ok {
		return u
	}
	return "uid-" + part
}

// pkeyOf is part's pkey now: fakePkey for the first incarnation, the
// SHA-256 of the uid for any other.
func (w *partWorld) pkeyOf(part string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if u, ok := w.uids[part]; ok {
		return fakePkey(u)
	}
	return fakePkey(part)
}

func newPartWorld(t *testing.T, spec registry.PartitionSpec, m registry.Manifest) *partWorld {
	t.Helper()
	if m.Runtime == "" {
		m.Runtime = "go"
	}
	c := &registry.Component{Path: "apps/x", Manifest: m}
	r, f, tp := newSeamRunner(t, c)
	r.Isolate = true
	r.parts.adm.memTotal.Store(64 << 30) // 32 per tile, 128 in the workspace
	w := &partWorld{t: t, r: r, f: f, tp: tp, c: c, denied: map[string]bool{}, uids: map[string]string{}}
	r.PartitionIdent = func(tile, part string) (string, string, error) {
		pkey := w.pkeyOf(part)
		w.mu.Lock()
		defer w.mu.Unlock()
		return pkey, w.uidOf(part), nil
	}
	r.ShouldRunPartition = func(tile, dep, part, uid string) bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.denied[part] && uid == w.uidOf(part) // PD-20: the same uid
	}
	r.PartitionEnv = func(c *registry.Component, dep, part string) ([]string, map[string]ResBind) {
		return nil, map[string]ResBind{}
	}
	r.RegisterPartitionInstance = func(token, tile, dep, part, uid string) {
		w.mu.Lock()
		w.tokens = append(w.tokens, part+" "+token+" "+uid)
		w.mu.Unlock()
	}
	r.PartitionEvent = func(tile, dep, part, typ, text string) {
		w.mu.Lock()
		w.events = append(w.events, typ+" "+part)
		w.mu.Unlock()
	}
	setMode(r, "apps/x", spec)
	return w
}

// setMode is what PartitionsChanged records, without its restarts.
func setMode(r *Runner, tile string, spec registry.PartitionSpec) {
	r.mu.Lock()
	if r.parts.mode == nil {
		r.parts.mode = map[string]registry.PartitionSpec{}
	}
	r.parts.mode[tile] = spec
	r.mu.Unlock()
}

func (w *partWorld) ensureDep(dep, part string, class StartClass) string {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return answer(w.r.EnsurePartition(ctx, w.c, dep, part, class))
}

func (w *partWorld) ensure(part string) string { return w.ensureDep("main", part, StartInteractive) }

func (w *partWorld) state(part string) *state {
	return w.r.existingPart(partStateKey("apps/x", "main", fakePkey(part)))
}

// running reports whether part has a current generation.
func (w *partWorld) running(part string) bool {
	s := w.state(part)
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil
}

// settleParts waits until no state — a partition's or the global
// instance's — builds or waits for a restart, and every generation not
// current is stopped.
func (w *partWorld) settleParts() {
	w.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		busy := ""
		for _, s := range append(w.r.allStates(""), w.r.partStates("")...) {
			s.mu.Lock()
			if s.building || s.dirty && s.cur != nil {
				busy = s.comp + " " + s.dep
			}
			s.mu.Unlock()
		}
		if busy == "" {
			busy = w.strayGen()
		}
		if busy == "" {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("partitions never settled: %s", busy)
		}
		time.Sleep(time.Millisecond)
	}
}

// strayGen names a generation the fake started that is neither any
// state's current one nor stopped.
func (w *partWorld) strayGen() string {
	curs := map[*instance]bool{}
	for _, s := range append(w.r.allStates(""), w.r.partStates("")...) {
		s.mu.Lock()
		if s.cur != nil {
			curs[s.cur] = true
		}
		s.mu.Unlock()
	}
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	for inst, g := range w.f.gens {
		if !g.closed && !curs[inst] {
			return fmt.Sprintf("%s g%d is neither current nor stopped", g.tile, g.gen)
		}
	}
	return ""
}

func count(log []string, prefix string) int {
	n := 0
	for _, l := range log {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// covers PD-01 — TestPartStateKey: a partition's key is distinct from
// every deployment's key and from every other partition's; its run dir
// ("p-" + 16 hex, a hand-computed golden) collides with no CompKey or
// deployment dir, and its log and cgroup leaf live under its own names.
func TestPartStateKey(t *testing.T) {
	const pk = "u-0123456789abcdef0123456789abcdef"
	tiles, deps := []string{"apps/x", "apps/x/y", "apps", "p"}, []string{"main", "dev", "p", "global"}
	seen := map[string]string{}
	add := func(k, what string) {
		if prev, ok := seen[k]; ok {
			t.Errorf("%s and %s share the key %q", prev, what, k)
		}
		seen[k] = what
	}
	for _, tile := range tiles {
		for _, dep := range deps {
			add(stateKey(tile, dep), "stateKey("+tile+", "+dep+")")
			for _, pkey := range []string{pk, fakePkey("user:alice"), fakePkey("user:bob")} {
				add(partStateKey(tile, dep, pkey), "partStateKey("+tile+", "+dep+", "+pkey+")")
			}
		}
	}
	if got, want := partSockDir("apps/x", "main", pk), "p-2db3f2a97388013b"; got != want {
		t.Errorf("partSockDir = %q, want %q (sha256(apps/x\\0main\\0<pkey>)[:8])", got, want)
	}
	if d := partSockDir("apps/x", "main", pk); d == sockDir("apps/x", "main") || d == sockDir("apps/x", "dev") || len(d) != 18 {
		t.Errorf("partSockDir %q collides or has the wrong shape", d)
	}
	if got, want := partitionLog("apps/x", "main", pk), ".xbin/partition/"+tkX+"/main/"+pk+"/backend.log"; got != want {
		t.Errorf("partitionLog = %q, want %q", got, want)
	}
	if got, want := partitionLeaf("apps/x", "main", pk), "tile-"+ckX+"/p-2db3f2a97388013b/backend"; got != want {
		t.Errorf("partitionLeaf = %q, want %q", got, want)
	}
}

// covers PD-17 PD-19 PD-20 G3 — TestEnsurePartitionNeedsIsolate,
// TestEnsurePartitionPrimaryOnly and the other gates: every refusal
// creates no state, and "" and "global" are today's instance.
func TestEnsurePartitionGates(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	must := func(part, want string) {
		t.Helper()
		if got := w.ensure(part); !strings.Contains(got, want) {
			t.Errorf("ensure %q = %q, want it to say %q", part, got, want)
		}
	}
	w.r.Isolate = false
	must("user:alice", "only in a sandbox (--isolate)")
	w.r.Isolate = true
	if got := w.ensureDep("dev", "user:alice", StartInteractive); !strings.Contains(got, "primary deployment only") {
		t.Errorf("a partition on dev: %s", got)
	}
	must("user:", "is not a partition key")
	must("org:sales", "is not a partition key")
	w.mu.Lock()
	w.denied["user:alice"] = true
	w.mu.Unlock()
	must("user:alice", "may not run now")
	ident := w.r.PartitionIdent
	w.r.PartitionIdent = func(string, string) (string, string, error) { return "../../etc", "uid", nil }
	must("user:bob", "no valid partition id")
	w.r.PartitionIdent = nil
	must("user:bob", "own identity and data yet")
	w.r.PartitionIdent = ident
	env := w.r.PartitionEnv
	w.r.PartitionEnv = nil
	must("user:bob", "own identity and data yet")
	w.r.PartitionEnv = env
	w.f.seal("apps/x", true)
	must("user:bob", "is not enabled")
	w.f.seal("apps/x", false)
	setMode(w.r, "apps/x", registry.PartitionSpec{})
	must("user:bob", "doesn't run people's partitions")
	if n := len(w.r.partStates("")); n != 0 {
		t.Fatalf("refused partitions left %d states", n)
	}
	for _, sk := range w.r.allStates("") {
		t.Fatalf("a refusal made a state for %s %s", sk.comp, sk.dep)
	}

	setMode(w.r, "apps/x", userGlobal)
	if got := w.ensure("user:bob"); got != "g1" {
		t.Fatalf("bob's partition: %s", got)
	}
	if got := w.ensure("global"); got != "g1" || w.r.existingStateOf("apps/x", "main") == nil {
		t.Errorf("the global instance: %s, want the primary's state at today's key", got)
	}
	if got := w.ensure(""); got != "g1" {
		t.Errorf("the tile's one instance: %s", got)
	}
	_, err := w.r.EnsurePartition(context.Background(), w.c, "main", "user:", StartInteractive)
	if !errors.Is(err, ErrPartitionRefused) {
		t.Errorf("a refusal isn't ErrPartitionRefused: %v", err)
	}

	t.Run("no global instance", func(t *testing.T) {
		w := newPartWorld(t, userOnly, registry.Manifest{})
		_, err := w.r.EnsurePartition(context.Background(), w.c, "main", "global", StartInteractive)
		if !errors.Is(err, ErrNoPartition) {
			t.Errorf("global without one: %v, want ErrNoPartition", err)
		}
		if _, err := w.r.EnsureDeployment(context.Background(), w.c, "main"); !errors.Is(err, ErrNoPartition) {
			t.Errorf("the primary of a user-only tile: %v, want ErrNoPartition", err)
		}
		if got := w.ensure("user:alice"); got != "g1" {
			t.Errorf("alice's partition: %s", got)
		}
		if log := w.f.takeLog(); count(log, "start apps/x main") != 0 {
			t.Errorf("the global instance of a user-only tile started: %q", log)
		}
	})
}

// covers PD-18 PD-35 C4 C5 S20 — TestPartitionAdmission (03 §A.5).
func TestPartitionAdmission(t *testing.T) {
	t.Run("caps from memory", func(t *testing.T) {
		for _, tc := range []struct {
			mem       int64
			tile, all int
		}{
			{4 << 30, 6, 12}, // the 4 GiB macOS VM
			{1 << 30, 4, 8},
			{64 << 30, 32, 128},
			{0, 4, 8},
		} {
			if tl, ws := partitionCapsFrom(tc.mem); tl != tc.tile || ws != tc.all {
				t.Errorf("caps(%d MiB) = %d/%d, want %d/%d", tc.mem>>20, tl, ws, tc.tile, tc.all)
			}
		}
		w := newPartWorld(t, userGlobal, registry.Manifest{})
		w.r.parts.adm.memTotal.Store(4 << 30)
		if tl, ws := w.r.partitionCaps("apps/x"); tl != 6 || ws != 12 {
			t.Errorf("the runner's caps on 4 GiB: %d/%d", tl, ws)
		}
		w.r.PartitionCapsFor = func(string) (int, int) { return 9, 0 } // an admin's tile cap
		if tl, ws := w.r.partitionCaps("apps/x"); tl != 9 || ws != 12 {
			t.Errorf("with an override: %d/%d, want 9/12", tl, ws)
		}
	})

	t.Run("interactive evicts only what isn't in use; passive streams don't count", func(t *testing.T) {
		w := newPartWorld(t, userGlobal, registry.Manifest{})
		w.r.PartitionCapsFor = func(string) (int, int) { return 2, 0 }
		for _, p := range []string{"user:alice", "user:bob"} {
			if got := w.ensure(p); got != "g1" {
				t.Fatalf("%s: %s", p, got)
			}
		}
		_, err := w.r.EnsurePartition(context.Background(), w.c, "main", "user:carol", StartInteractive)
		if !errors.Is(err, ErrPartitionBusy) || !errors.Is(err, sbx.ErrRefused) ||
			!strings.Contains(err.Error(), "too many people's instances of apps/x are running; try again shortly") {
			t.Errorf("carol past the cap, both in use: %v", err)
		}
		if w.state("user:carol") != nil {
			t.Error("a refused start made a state")
		}
		w.f.advance(3 * time.Minute)
		w.ensure("user:bob") // bob's request: bob is in use again
		if got := w.ensure("user:carol"); got != "g1" {
			t.Fatalf("carol, alice idle for 3 min: %s", got)
		}
		if w.running("user:alice") || !w.running("user:bob") {
			t.Errorf("the LRU partition not in use (alice) should be evicted, bob kept: alice %v bob %v", w.running("user:alice"), w.running("user:bob"))
		}

		w.f.advance(3 * time.Minute)
		stream := w.r.TrackPartition("apps/x", "main", "user:bob", true) // a visible tab's SSE
		hold := w.r.TrackPartition("apps/x", "main", "user:carol", false)
		if got := w.ensure("user:dave"); got != "g1" {
			t.Fatalf("dave: %s", got)
		}
		if w.running("user:bob") || !w.running("user:carol") {
			t.Errorf("a passive stream kept bob (%v), or a hold didn't keep carol (%v)", w.running("user:bob"), w.running("user:carol"))
		}
		stream()

		w.f.advance(3 * time.Minute)
		davesStream := w.r.TrackPartition("apps/x", "main", "user:dave", true)
		_, err = w.r.EnsurePartition(context.Background(), w.c, "main", "user:erin", StartBackground)
		if !errors.Is(err, ErrPartitionDeferred) {
			t.Errorf("a background start with only streaming and held partitions: %v, want deferred", err)
		}
		if got := w.ensure("user:erin"); got != "g1" || w.running("user:dave") {
			t.Errorf("an interactive start evicts the streaming one: %s (dave running %v)", got, w.running("user:dave"))
		}
		davesStream()
		hold()
	})

	t.Run("the workspace cap", func(t *testing.T) {
		w := newPartWorld(t, userGlobal, registry.Manifest{})
		w.r.PartitionCapsFor = func(string) (int, int) { return 10, 2 }
		w.ensure("user:alice")
		w.ensure("user:bob")
		if got := w.ensure("user:carol"); !strings.Contains(got, "too many people's instances") {
			t.Errorf("carol past the workspace cap: %s", got)
		}
	})

	t.Run("background starts: 4 at once, mail 6 a minute per tile", func(t *testing.T) {
		w := newPartWorld(t, userGlobal, registry.Manifest{})
		var rel []func()
		for i := range maxBackgroundStarts {
			f, err := w.r.admitPartition("apps/x", fmt.Sprintf("k%d", i), StartBackground)
			if err != nil {
				t.Fatalf("background start %d: %v", i, err)
			}
			rel = append(rel, f)
		}
		if _, err := w.r.admitPartition("apps/x", "k9", StartBackground); !errors.Is(err, ErrPartitionDeferred) {
			t.Errorf("a 5th background start at once: %v", err)
		}
		if f, err := w.r.admitPartition("apps/x", "k10", StartInteractive); err != nil {
			t.Errorf("an interactive start beside 4 background ones: %v", err)
		} else {
			f()
		}
		for _, f := range rel {
			f()
		}
		for i := range mailStartsPerMinute {
			f, err := w.r.admitPartition("apps/x", fmt.Sprintf("m%d", i), StartMail)
			if err != nil {
				t.Fatalf("mail start %d: %v", i, err)
			}
			f()
		}
		if _, err := w.r.admitPartition("apps/x", "m9", StartMail); !errors.Is(err, ErrPartitionDeferred) {
			t.Errorf("a 7th mail start in a minute: %v", err)
		}
		if f, err := w.r.admitPartition("apps/y", "m9", StartMail); err != nil {
			t.Errorf("another tile's mail start: %v", err)
		} else {
			f()
		}
		w.f.advance(61 * time.Second)
		if f, err := w.r.admitPartition("apps/x", "m9", StartMail); err != nil {
			t.Errorf("a mail start a minute later: %v", err)
		} else {
			f()
		}
	})

	t.Run("a refused person is an admin's metadata", func(t *testing.T) {
		w := newPartWorld(t, userGlobal, registry.Manifest{})
		w.r.Sandboxes = sbx.New()
		w.r.PartitionCapsFor = func(string) (int, int) { return 1, 0 }
		w.ensure("user:alice")
		w.ensure("user:bob")
		fl := w.r.Sandboxes.Failures(sbx.Filter{})
		if len(fl) != 1 || fl[0].Stage != sbx.Refused || fl[0].Partition != "user:bob" || fl[0].Tile != "apps/x" {
			t.Errorf("failure ring %+v, want bob's refusal", fl)
		}
		if _, err := w.r.EnsurePartition(context.Background(), w.c, "main", "user:carol", StartBackground); !errors.Is(err, ErrPartitionDeferred) {
			t.Errorf("carol's background start: %v", err)
		}
		if n := len(w.r.Sandboxes.Failures(sbx.Filter{})); n != 1 {
			t.Errorf("a deferred delivery reached the failure ring (%d rows)", n)
		}
	})
}

// covers PD-18 PD-35 — TestPartitionReap10m (03 §A.6): a user partition
// stops 10 minutes after its last non-passive use; a held connection keeps
// it, a passive stream doesn't; the global instance keeps today's 30.
func TestPartitionReap10m(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.ensure("user:alice")
	w.ensure("global")
	w.f.advance(9 * time.Minute)
	w.r.reapOnce()
	if !w.running("user:alice") {
		t.Fatal("reaped after 9 minutes")
	}
	w.f.advance(2 * time.Minute)
	w.r.reapOnce()
	w.settleParts()
	if w.running("user:alice") {
		t.Error("alice's partition still runs after 11 idle minutes")
	}
	if s := w.r.existingStateOf("apps/x", "main"); s == nil || s.cur == nil {
		t.Error("the global instance was reaped after 11 minutes")
	}
	if got := w.ensure("user:alice"); got != "g2" {
		t.Errorf("alice after the reap: %s, want g2", got)
	}

	hold := w.r.TrackPartition("apps/x", "main", "user:alice", false)
	w.f.advance(11 * time.Minute)
	w.r.reapOnce()
	if !w.running("user:alice") {
		t.Error("a held connection (the agent's /engine/hold) didn't keep alice's partition")
	}
	hold()
	stream := w.r.TrackPartition("apps/x", "main", "user:alice", true)
	w.f.advance(11 * time.Minute)
	w.r.reapOnce()
	w.settleParts()
	if w.running("user:alice") {
		t.Error("a passive stream kept alice's partition past 10 idle minutes")
	}
	stream()
}

// covers PD-35 — TestAlwaysOnGlobalOnly (03 §A.6): alwaysOn keeps up the
// global instance only; no person's partition starts at boot or after an
// exit, and a tile without a global instance keeps nothing up.
func TestAlwaysOnGlobalOnly(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{AlwaysOn: true})
	w.r.WakeAlwaysOn()
	deadline := time.Now().Add(2 * time.Second)
	for count(w.f.takeLogPeek(), "start apps/x main") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the global instance never started")
		}
		time.Sleep(time.Millisecond)
	}
	settle(t, w.r, w.f)
	if n := len(w.r.partStates("")); n != 0 {
		t.Errorf("a boot wake made %d partition states", n)
	}
	w.ensure("user:alice")
	w.f.crash("apps/x user:alice", "main")
	w.settleParts()
	w.r.ao.mu.Lock()
	pending := len(w.r.ao.pending)
	w.r.ao.mu.Unlock()
	if pending != 0 {
		t.Error("a partition's exit scheduled an alwaysOn restart")
	}
	w.f.advance(11 * time.Minute)
	w.r.reapOnce()
	if s := w.r.existingStateOf("apps/x", "main"); s == nil || s.cur == nil {
		t.Error("the alwaysOn global instance was reaped")
	}

	t.Run("no global instance", func(t *testing.T) {
		w := newPartWorld(t, userOnly, registry.Manifest{AlwaysOn: true})
		w.r.WakeAlwaysOn()
		time.Sleep(20 * time.Millisecond)
		if log := w.f.takeLog(); len(log) != 0 {
			t.Errorf("a user-only alwaysOn tile started something at boot: %q", log)
		}
	})
}

// covers PD-24 03§E — the crash watch tells PartitionExit of an exit of a
// person's partition that nothing asked for (its pkey, and whether the
// breaker holds it), so its record keeps it across a restart of xbind; the
// global instance's exit tells nothing.
func TestPartitionExitHook(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	got := make(chan string, 8)
	w.r.PartitionExit = func(tile, dep, part, pkey string, crashLoop bool) {
		got <- fmt.Sprintf("%s %s %s %s %v", tile, dep, part, pkey, crashLoop)
	}
	w.ensure("user:alice")
	w.ensure("global")
	w.f.crash("apps/x user:alice", "main")
	select {
	case e := <-got:
		if want := "apps/x main user:alice " + fakePkey("user:alice") + " false"; e != want {
			t.Errorf("the exit: %q, want %q", e, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a partition's exit wasn't told")
	}
	w.f.crash("apps/x", "main")
	select {
	case e := <-got:
		t.Errorf("the global instance's exit was told: %q", e)
	case <-time.After(100 * time.Millisecond):
	}
}

// takeLogPeek is the fake's log so far, left in place.
func (f *fakeEngine) takeLogPeek() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

// covers S12 C15 — a person's partition's runner events reach its person
// only (PartitionEvent), never the tile-wide hub; its crash loop names the
// partition, never a log path.
func TestPartitionEventsPersonOnly(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.tp.take()
	w.ensure("user:alice")
	if got := w.tp.take(); len(got) != 0 {
		t.Errorf("a partition's start reached the hub: %q", got)
	}
	w.mu.Lock()
	ev := slices.Clone(w.events)
	w.mu.Unlock()
	if want := []string{"build-start user:alice", "build-ok user:alice"}; !equalStrings(ev, want) {
		t.Errorf("the person's events %q, want %q", ev, want)
	}
	w.ensure("global")
	if got := w.tp.take(); !slices.Contains(got, "build-ok apps/x") {
		t.Errorf("the global instance's events %q, want today's", got)
	}
	// a `setup` manifest's env layer: announced to its person only
	w.r.emitSetupStart(partitionView(w.c, "user:alice", fakePkey("user:alice")))
	w.mu.Lock()
	ev = slices.Clone(w.events)
	w.mu.Unlock()
	if got := w.tp.take(); len(got) != 0 || ev[len(ev)-1] != "build-start user:alice" {
		t.Errorf("a partition's setup run: hub %q, person %q", got, ev)
	}
	w.r.emitSetupStart(w.c)
	if got := w.tp.take(); !slices.Equal(got, []string{"build-start apps/x"}) {
		t.Errorf("the global instance's setup run %q, want today's", got)
	}
	s := w.state("user:alice")
	if e := w.r.crashLoop(s, Code{WorkTree: true}, 3).Error(); !strings.Contains(e, "user:alice's instance") || strings.Contains(e, ".xbin") {
		t.Errorf("crash loop: %q", e)
	}
	w.r.PartitionEvent = nil
	w.r.stopState(w.r.existingStateOf("apps/x", "main")) // the global instance: no rebuild of its own
	w.tp.take()
	w.r.Changed(w.c)
	w.settleParts()
	if got := w.tp.take(); slices.ContainsFunc(got, func(e string) bool { return strings.HasPrefix(e, "build") }) {
		t.Errorf("without the event hook a partition's restart reached the hub: %q", got)
	}
}

// covers S13 PD-26 — stops: StopPartition, StopPartitionsOf,
// StopDeployment of the primary and PartitionsChanged all revoke the
// partition's instance token before they return, and never touch data.
func TestPartitionStops(t *testing.T) {
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.r.Auth = a
	token := func(part string) string {
		s := w.state(part)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cur.token = "tok-" + part // the fake starts generations without one
		a.RegisterInstance(s.cur.token, "apps/x")
		return s.cur.token
	}
	authn := func(tok string) bool {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		_, ok := a.FromRequest(req)
		return ok
	}
	for _, p := range []string{"user:alice", "user:bob", "user:carol"} {
		w.ensure(p)
	}
	ta, tb, tc := token("user:alice"), token("user:bob"), token("user:carol")
	w.r.StopPartition("apps/x", "main", "user:alice")
	if w.state("user:alice") != nil || authn(ta) || !authn(tb) {
		t.Errorf("StopPartition(alice): state %v, alice's token %v, bob's %v", w.state("user:alice") != nil, authn(ta), authn(tb))
	}
	w.r.StopPartitionsOf("bob")
	if w.state("user:bob") != nil || authn(tb) || !authn(tc) {
		t.Error("StopPartitionsOf(bob) stopped the wrong partitions, or left bob's token")
	}
	w.ensure("global")
	w.r.StopDeployment("apps/x", "main") // a pending mode stops every primary instance
	if w.state("user:carol") != nil || authn(tc) {
		t.Error("the primary's stop left carol's partition")
	}

	w.ensure("user:alice")
	ta = token("user:alice")
	w.r.PartitionsChanged(w.c, userGlobal, registry.PartitionSpec{}) // a switch to unpartitioned
	if w.state("user:alice") != nil || authn(ta) {
		t.Error("a mode change left alice's partition, or its token")
	}
	if got := w.ensure("user:alice"); !strings.Contains(got, "doesn't run people's partitions") {
		t.Errorf("a partition after the switch: %s", got)
	}

	w.ensure("") // today's instance, unpartitioned
	w.f.takeLog()
	w.r.PartitionsChanged(w.c, registry.PartitionSpec{}, userGlobal) // auto: it becomes the global instance
	for deadline := time.Now().Add(2 * time.Second); count(w.f.takeLogPeek(), "stop apps/x main") == 0; {
		if time.Now().After(deadline) {
			t.Fatal("today's instance never stopped")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // nothing may start meanwhile
	if log := w.f.takeLog(); count(log, "stop apps/x main") != 1 || count(log, "start ") != 0 {
		t.Errorf("turning partitioned: %q, want today's instance stopped (its next start says global) and nothing started", log)
	}
	w.ensure("global")
	w.r.PartitionsChanged(w.c, userGlobal, userOnly) // global removed (H1)
	deadline := time.Now().Add(2 * time.Second)
	for s := w.r.existingStateOf("apps/x", "main"); ; {
		s.mu.Lock()
		gone := s.cur == nil
		s.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the global instance still runs after global was removed")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := w.r.EnsurePartition(context.Background(), w.c, "main", "global", StartInteractive); !errors.Is(err, ErrNoPartition) {
		t.Errorf("global after its removal: %v", err)
	}
}

// covers PD-46 — the admin's metadata: Inspect keeps one row per tile (the
// primary's), InspectPartitions has one per partition instance, naming it;
// a partition's sandbox entry names its partition under an id of its own.
func TestInspectPartitions(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.ensure("global")
	w.ensure("user:alice")
	w.ensure("user:bob")
	if rows := w.r.Inspect(); len(rows) != 1 || rows[0].Partition != "" {
		t.Errorf("Inspect = %+v, want the global instance's row alone", rows)
	}
	rows := w.r.InspectPartitions()
	if len(rows) != 2 || rows[0].Partition != "user:alice" || rows[1].Partition != "user:bob" || rows[0].Deployment != "" || rows[0].State != "healthy" {
		t.Errorf("InspectPartitions = %+v", rows)
	}
	if st := w.r.Status(); len(st) != 1 {
		t.Errorf("Status = %v, want the primary's row alone", st)
	}
	v := partitionView(w.c, "user:alice", fakePkey("user:alice"))
	if got, want := sbxPartID(v, "", 3), "backend@"+fakePkey("user:alice")+":"+ckX+":g3"; got != want {
		t.Errorf("sbx id %q, want %q", got, want)
	}
	if got := sbxPartID(w.c, "", 3); got != sbxID("apps/x", "", 3) {
		t.Errorf("the global instance's sbx id changed: %q", got)
	}
	w.r.Sandboxes = sbx.New()
	remove := w.r.sbxAddLeaf(v, 3, "sock", 42, "")
	defer remove()
	if es := w.r.Sandboxes.List(sbx.Filter{}); len(es) != 1 || es[0].Partition != "user:alice" || es[0].Deployment != "" {
		t.Errorf("entries %+v", es)
	}
}

// covers PD-18 — a tracked connection turned passive (the proxy learns a
// response is a stream) stops counting as use.
func TestTrackPartitionConn(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.ensure("user:alice")
	s := w.state("user:alice")
	c := w.r.TrackPartitionConn("apps/x", "main", "user:alice")
	read := func() (int, int) { s.mu.Lock(); defer s.mu.Unlock(); return s.active, s.pt.passive }
	if a, p := read(); a != 1 || p != 0 {
		t.Errorf("tracked: active %d passive %d", a, p)
	}
	c.Passive()
	c.Passive()
	if a, p := read(); a != 0 || p != 1 {
		t.Errorf("passive: active %d passive %d", a, p)
	}
	c.Release()
	c.Release()
	if a, p := read(); a != 0 || p != 0 {
		t.Errorf("released: active %d passive %d", a, p)
	}
	// "" and "global" are tracked by the primary's state, as today
	g := w.r.TrackPartitionConn("apps/x", "main", "global")
	active := func() int {
		ps := w.r.existingStateOf("apps/x", "main")
		if ps == nil {
			return -1
		}
		ps.mu.Lock()
		defer ps.mu.Unlock()
		return ps.active
	}
	if active() != 1 {
		t.Error("global isn't tracked by the primary's state")
	}
	g.Passive() // no passive for today's instance
	g.Release()
	if active() != 0 {
		t.Error("global's release")
	}
	w.r.TrackPartitionConn("apps/x", "main", "user:nobody").Release() // no state: nothing
}

// covers PD-19 — TestEnsurePartitionNeedsIsolate (03 §Tests): without
// --isolate no person's partition starts, and nothing is left of the try.
func TestEnsurePartitionNeedsIsolate(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	w.r.Isolate = false
	_, err := w.r.EnsurePartition(context.Background(), w.c, "main", "user:alice", StartInteractive)
	if !errors.Is(err, ErrPartitionRefused) || !strings.Contains(err.Error(), "--isolate") {
		t.Errorf("without isolation: %v", err)
	}
	if n := len(w.r.partStates("")); n != 0 || len(w.f.takeLog()) != 0 {
		t.Errorf("a refused partition left %d states or ran something", n)
	}
}

// covers PD-17 — TestEnsurePartitionPrimaryOnly (03 §Tests): a person's
// partition runs on the tile's primary deployment only.
func TestEnsurePartitionPrimaryOnly(t *testing.T) {
	w := newPartWorld(t, userGlobal, registry.Manifest{})
	for _, dep := range []string{"dev", "global"} {
		_, err := w.r.EnsurePartition(context.Background(), w.c, dep, "user:alice", StartInteractive)
		if !errors.Is(err, ErrPartitionRefused) || !strings.Contains(err.Error(), "primary deployment only") {
			t.Errorf("a partition on %s: %v", dep, err)
		}
	}
	if n := len(w.r.partStates("")); n != 0 {
		t.Errorf("refused partitions left %d states", n)
	}
	if got := w.ensure("user:alice"); got != "g1" {
		t.Errorf("on the primary: %s", got)
	}
}
