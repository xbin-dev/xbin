package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memKV is a kv resource in memory (conf).
type memKV struct {
	mu   sync.Mutex
	m    map[string][]byte
	gets int
}

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) Get(_ context.Context, key string) ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.gets++
	v, ok := k.m[key]
	return append([]byte(nil), v...), ok, nil
}

func (k *memKV) Put(_ context.Context, key string, val []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = append([]byte(nil), val...)
	return nil
}

func (k *memKV) Delete(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
}

func (k *memKV) List(_ context.Context, prefix string) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for key := range k.m {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out, nil
}

// fileDB opens a db file in a temp dir (the mode is the caller's).
func fileDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	d, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.sql.Close() })
	return d, path
}

// TestPartitionIDsStartAt2to40: a person's partition's db numbers its runs
// from 2^40 — from a fresh file, and again after a reopen — so ids of
// different homes never collide; an unpartitioned (or global) db starts at 1.
func TestPartitionIDsStartAt2to40(t *testing.T) {
	legacy, _ := fileDB(t)
	if id, _ := legacy.createRun("a", "", 0); id != 1 {
		t.Fatalf("legacy's first run is #%d", id)
	}
	setMode(t, modeGlobal, "")
	global, _ := fileDB(t)
	if id, _ := global.createRun("a", "", 0); id != 1 {
		t.Fatalf("global's first run is #%d", id)
	}
	setMode(t, modeUser, "alice")
	d, path := fileDB(t)
	id, err := d.createRun("first", "", 0)
	if err != nil || id != partitionIDBase {
		t.Fatalf("the partition's first run is #%d (%v), want 2^40 = %d", id, err, partitionIDBase)
	}
	kid, _ := d.createRun("sub", "", id)
	if kid != partitionIDBase+1 {
		t.Fatalf("its subagent is #%d", kid)
	}
	d.sql.Close()
	d2, err := openDB(path) // a restart: the seed never lowers the counter
	if err != nil {
		t.Fatal(err)
	}
	defer d2.sql.Close()
	if id, _ := d2.createRun("again", "", 0); id != partitionIDBase+2 {
		t.Fatalf("after a reopen: #%d", id)
	}
	if partitionIDBase >= 1<<53 {
		t.Fatal("2^40 must be exact in JS numbers")
	}
}

// TestConfMirror: global writes its tile-wide settings and shared skills
// into conf when they change; a person's partition reads them from there,
// can't write them, and sees the shared skills beside its own.
func TestConfMirror(t *testing.T) {
	kv := newMemKV()
	setMode(t, modeGlobal, "")
	g := newTestDB(t)
	confOut = newConfMirror(kv, g)
	if err := g.putSetting("config", `{"model":"fake/one"}`); err != nil {
		t.Fatal(err)
	}
	if err := g.putSetting("halt", "1"); err != nil {
		t.Fatal(err)
	}
	if err := g.upsertSkill(&Skill{Name: "deploy", Description: "how we deploy", Content: "steps"}); err != nil {
		t.Fatal(err)
	}
	if err := g.upsertSkill(&Skill{Name: "mine", Content: "x", Owner: "bob"}); err != nil { // bob's, never mirrored
		t.Fatal(err)
	}
	_ = g.putSetting("conv_epoch_ms", "5") // not a tile-wide one
	var snap confSnap
	raw, ok, _ := kv.Get(context.Background(), confSettingsKey)
	if !ok || json.Unmarshal(raw, &snap) != nil || snap.Config != `{"model":"fake/one"}` || snap.Halt != "1" || snap.Skills == "" {
		t.Fatalf("conf settings: %s", raw)
	}
	keys, _ := kv.List(context.Background(), "")
	if strings.Join(keys, ",") != "settings,skill:deploy" {
		t.Fatalf("conf keys %v (only the shared skill, never bob's)", keys)
	}

	// a person's partition
	setMode(t, modeUser, "alice")
	shorten(t, &confTTL, 0)
	confOut = nil
	confIn = newConfReader(kv, nil)
	p := newTestDB(t)
	if v := p.getSetting("config"); v != `{"model":"fake/one"}` {
		t.Fatalf("the partition reads config %q", v)
	}
	if parseConfig(p.getSetting("config")).Model != "fake/one" {
		t.Fatal("parsed config")
	}
	e := &Engine{db: p}
	if !e.halted() {
		t.Fatal("the halt switch isn't read from conf")
	}
	if err := p.putSetting("config", "{}"); !errors.Is(err, errSharedSetting) {
		t.Fatalf("the partition wrote config: %v", err)
	}
	if err := p.putSetting("conv_epoch_ms", "7"); err != nil || p.getSetting("conv_epoch_ms") != "7" {
		t.Fatalf("a partition's own setting: %v", err)
	}
	_ = p.upsertSkill(&Skill{Name: "notes", Content: "alice's", Owner: "alice"})
	all, _ := p.listSkills()
	var names []string
	for _, s := range all {
		names = append(names, s.Name+"/"+s.Owner)
	}
	if strings.Join(names, ",") != "deploy/,notes/alice" {
		t.Fatalf("the partition's skills: %v", names)
	}
	if s, err := p.getSkill("deploy"); err != nil || s.Content != "steps" {
		t.Fatalf("a shared skill by name: %+v %v", s, err)
	}
	if _, err := p.getSkill("mine"); err == nil {
		t.Fatal("another person's skill reached the partition")
	}

	// a manager's change at global reaches the partition (the cache is 0 here)
	setMode(t, modeGlobal, "")
	confIn, confOut = nil, newConfMirror(kv, g)
	_ = g.putSetting("halt", "")
	_ = g.deleteSkill("deploy")
	_ = g.putSetting("classes", `{"classes":[{"id":"ops","name":"Ops","toolsets":["internal"]}],"default":"ops"}`)
	setMode(t, modeUser, "alice")
	shorten(t, &confTTL, 0)
	confIn = newConfReader(kv, nil)
	if e.halted() {
		t.Fatal("the halt stayed on in the partition")
	}
	if _, err := p.getSkill("deploy"); err == nil {
		t.Fatal("a deleted shared skill is still offered")
	}
	if _, ok := currentClasses().find("ops"); !ok {
		t.Fatal("the classes from conf aren't in force")
	}
}

// TestConfReaderCaches: settings are read at most once per confTTL, and a
// conf that has nothing wakes the global instance.
func TestConfReaderCaches(t *testing.T) {
	kv := newMemKV()
	woke := make(chan struct{}, 4)
	c := newConfReader(kv, func() { woke <- struct{}{} })
	for range 5 {
		c.setting("config")
	}
	if kv.gets != 1 {
		t.Fatalf("%d reads within the TTL", kv.gets)
	}
	select {
	case <-woke:
	case <-time.After(2 * time.Second):
		t.Fatal("an empty conf didn't wake the global instance")
	}
	c.invalidate()
	c.setting("config")
	if kv.gets != 2 {
		t.Fatalf("invalidate didn't read again (%d)", kv.gets)
	}
}

// TestTeamOpenShared: global migrates team under its flock; a person's
// partition never does — behind, it wakes global and waits (bounded), and
// until the schema is current hosted features answer 503.
func TestTeamOpenShared(t *testing.T) {
	setMode(t, modeUser, "alice")
	dir := t.TempDir()
	path := filepath.Join(dir, "team.sqlite")
	shorten(t, &teamWait, 300*time.Millisecond)
	shorten(t, &teamRecheck, 0)
	var woke atomic.Int32
	old := wakeGlobal
	wakeGlobal = func(context.Context) { woke.Add(1) }
	t.Cleanup(func() { wakeGlobal = old })

	u, err := openShared(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer u.sql.Close()
	if u.usable() || teamVersion(u.sql) != 0 {
		t.Fatal("a partition migrated team, or took a behind schema as current")
	}
	time.Sleep(50 * time.Millisecond)
	if woke.Load() == 0 {
		t.Fatal("a behind team didn't wake the global instance")
	}
	teamStore.Store(u)
	rec := httptest.NewRecorder()
	if !teamUnavailable(rec) || rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "being upgraded") {
		t.Fatalf("hosted features while behind: %d %s", rec.Code, rec.Body)
	}

	g, err := migrateTeam(path) // the global instance starts
	if err != nil {
		t.Fatal(err)
	}
	defer g.sql.Close()
	if teamVersion(g.sql) != teamSchema || !g.usable() {
		t.Fatalf("global's migration: schema %d", teamVersion(g.sql))
	}
	if !u.usable() || teamUnavailable(httptest.NewRecorder()) {
		t.Fatal("the partition didn't see the upgrade")
	}
	if _, err := migrateTeam(path); err != nil { // idempotent, e.g. a blue/green successor
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".migrate"); err != nil {
		t.Fatalf("the migrate lock file: %v", err)
	}
}

// TestLLMSlotsCap: the tile-wide semaphore caps model calls across every
// process of the tile (here: two semaphores on one directory, as two
// partitions would be) and a slot held by a process that dies is free again.
func TestLLMSlotsCap(t *testing.T) {
	dir := t.TempDir()
	limit := 2
	a := newLLMSlots(dir, func() int { return limit })
	b := newLLMSlots(dir, func() int { return limit })
	r1, err := a.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r2 := b.try()
	if r2 == nil {
		t.Fatal("the second slot wasn't free")
	}
	if b.try() != nil || a.try() != nil {
		t.Fatal("a third call got a slot with the cap at 2")
	}
	got := make(chan func(), 1)
	go func() {
		rel, _ := b.acquire(context.Background())
		got <- rel
	}()
	select {
	case <-got:
		t.Fatal("acquire didn't wait for a free slot")
	case <-time.After(100 * time.Millisecond):
	}
	r1()
	select {
	case rel := <-got:
		rel()
	case <-time.After(3 * time.Second):
		t.Fatal("a freed slot wasn't taken")
	}
	r2()
	var held []func()
	for range limit {
		rel := a.try()
		if rel == nil {
			t.Fatal("slots weren't released")
		}
		held = append(held, rel)
	}
	for _, rel := range held {
		rel()
	}

	// another process holds the only slot, then dies
	limit = 1
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLLMSlot$")
	cmd.Env = append(os.Environ(), "AGENT_HOLD_SLOT_DIR="+dir)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(out, buf); err != nil || string(buf) != "held" {
		t.Fatalf("the helper: %q %v", buf, err)
	}
	if a.try() != nil {
		t.Fatal("the other process's slot was taken")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	rel, err := a.acquire(ctx2)
	if err != nil {
		t.Fatalf("a dead process's slot wasn't freed: %v", err)
	}
	rel()
}

// TestHelperHoldLLMSlot is TestLLMSlotsCap's other process.
func TestHelperHoldLLMSlot(t *testing.T) {
	dir := os.Getenv("AGENT_HOLD_SLOT_DIR")
	if dir == "" {
		t.Skip("a helper process")
	}
	s := newLLMSlots(dir, func() int { return 1 })
	if s.try() == nil {
		os.Exit(2)
	}
	fmt.Print("held")
	time.Sleep(time.Minute)
}

// TestAcquireLLMWithSlots: the engine's model calls take a gate place and a
// tile-wide slot; a slot directory that can't hold them costs only the cap.
func TestAcquireLLMWithSlots(t *testing.T) {
	setMode(t, modeUser, "alice")
	e := newEngine(newTestDB(t), nil, nil, "")
	if e.gate.limit != userGateCap {
		t.Fatalf("a partition's gate: %d, want %d", e.gate.limit, userGateCap)
	}
	slots = newLLMSlots(t.TempDir(), func() int { return 1 })
	rel, err := e.acquireLLM(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if e.tryBackgroundLLM() != nil {
		t.Fatal("a title's call took a slot while the only one was held")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := e.acquireLLM(ctx, true); err == nil {
		t.Fatal("a second call passed the tile-wide cap of 1")
	}
	if a, _, _ := e.gate.stats(); a != 1 {
		t.Fatalf("a refused call kept its gate place (%d active)", a)
	}
	rel()
	slots = newLLMSlots(filepath.Join(t.TempDir(), "missing"), func() int { return 1 })
	rel, err = e.acquireLLM(context.Background(), true)
	if err != nil {
		t.Fatalf("an unusable slot directory failed the call: %v", err)
	}
	rel()
}

// TestUserModeWake: a person's partition leaves the resume job only for
// work that moves without them, a wake job at a sleeping run's minute, and
// nothing for a run waiting on a person; unpartitioned keeps today's rule.
func TestUserModeWake(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	d := newTestDB(t)
	id, _ := d.createRun("x", "", 0)
	set := func(status string, wake int64) {
		_, _ = d.q.Exec(`UPDATE runs SET status=?, wake_at=? WHERE id=?`, status, wake, id)
	}
	for _, c := range []struct {
		status   string
		wake     int64
		runnable bool
		at       int64
		legacy   bool
	}{
		{statusRunning, 0, true, 0, true},
		{statusQueued, 0, true, 0, true},
		{statusAwait, 0, false, 0, true},    // waits for subagents that are themselves runs
		{statusBlocked, 0, false, 0, true},  // legacy: resumed every minute; a partition: never
		{statusWaiting, 0, false, 0, false}, // waits for a person
		{statusSleep, now.Unix() + 3600, false, now.Unix() + 3600, true},
		{statusSleep, now.Unix() + 30, true, 0, true}, // due within the minute
		{statusIdle, 0, false, 0, false},
	} {
		set(c.status, c.wake)
		got := d.userWake(now)
		if got.runnable != c.runnable || got.wake != c.at {
			t.Errorf("%s wake=%d: %+v", c.status, c.wake, got)
		}
		if d.hasWork() != c.legacy {
			t.Errorf("%s: the legacy rule says %v", c.status, d.hasWork())
		}
	}
	set(statusIdle, 0)
	_, _ = d.q.Exec(`INSERT INTO inbox (run_id, kind, body, created) VALUES (?, 'message', '{}', 0)`, id)
	if !d.userWake(now).runnable {
		t.Error("an undelivered inbox row isn't runnable work")
	}
	if got := wakeSchedule(time.Date(2030, 3, 5, 14, 7, 30, 0, time.UTC).Unix()); got != "CRON_TZ=UTC 8 14 5 3 *" {
		t.Errorf("wakeSchedule rounds up to the minute: %q", got)
	}
	if got := wakeSchedule(time.Date(2030, 12, 31, 23, 59, 0, 0, time.UTC).Unix()); got != "CRON_TZ=UTC 59 23 31 12 *" {
		t.Errorf("wakeSchedule on the minute: %q", got)
	}
}

// fakeGateway serves h as the xbin gateway for the rest of the test (no
// keep-alive, so the SDK's shared client never reuses a connection to it).
func fakeGateway(t *testing.T, h http.Handler) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	srv.SetKeepAlivesEnabled(false)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("XBIN_GATEWAY", sock)
}

// TestLeaveWakeUpByMode drives Shutdown's registration through a gateway.
func TestLeaveWakeUpByMode(t *testing.T) {
	var mu sync.Mutex
	var jobs []map[string]any
	var deletes []string
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "PUT" && r.URL.Path == "/api/xbin/cron/jobs":
			var j map[string]any
			_ = json.NewDecoder(r.Body).Decode(&j)
			jobs = append(jobs, j)
		case r.Method == "DELETE":
			deletes = append(deletes, strings.TrimPrefix(r.URL.Path, "/api/xbin/cron/jobs/"))
		}
		w.WriteHeader(200)
	}))
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	d := newTestDB(t)
	ag := &Agent{db: d}
	id, _ := d.createRun("x", "", 0)
	wake := time.Now().Add(2 * time.Hour).Unix()
	_, _ = d.q.Exec(`UPDATE runs SET status='sleeping', wake_at=? WHERE id=?`, wake, id)

	take := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := jobs
		jobs = nil
		return out
	}
	ag.leaveWakeUp(d) // legacy: today's resume job
	if j := take(); len(j) != 1 || j[0]["name"] != "resume" || j[0]["schedule"] != "@every 1m" {
		t.Fatalf("legacy: %v", j)
	}
	setMode(t, modeUser, "alice")
	ag.leaveWakeUp(d)
	if j := take(); len(j) != 1 || j[0]["name"] != "wake" || j[0]["schedule"] != wakeSchedule(wake) {
		t.Fatalf("a partition with a sleeping run: %v", j)
	}
	_, _ = d.q.Exec(`UPDATE runs SET status='waiting_input', wake_at=0 WHERE id=?`, id)
	ag.leaveWakeUp(d)
	if j := take(); len(j) != 0 {
		t.Fatalf("a partition waiting for its person registered %v", j)
	}
	ag.clearWakeJobs()
	mu.Lock()
	got := strings.Join(deletes, ",")
	mu.Unlock()
	if got != "resume,heartbeat,wake" {
		t.Fatalf("takeover deletes %q", got)
	}
}
