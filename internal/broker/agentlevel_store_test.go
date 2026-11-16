package broker

import (
	"bytes"
	"os"
	"testing"
)

// The levels store (data/agent-levels.json) is written only when a level
// is put on record — a first statement or a lowering — never by a rescan
// or an observation that finds what is already there, nor by a raise (a
// pending request, not a change). So a workspace whose levels hold still
// keeps the file byte-identical (TestZeroStateLifecycleFiles' invariant);
// and the bytes are a function of the levels alone (sorted keys).
func TestAgentLevelsStoreWrittenOnlyOnChange(t *testing.T) {
	w := newPartWS(t, map[string]string{
		"xbin.json":        `{"schema":1}`,
		"apps/a/xbin.json": `{"runtime":"go","agent":"read"}`,
		"apps/b/xbin.json": `{"runtime":"go","agent":"off"}`,
		"apps/c/xbin.json": `{"runtime":"go"}`,
	}, nil)
	b := w.b
	b.ObserveAgentLevels()
	path := b.agentLevelsPath()
	snap := func() ([]byte, os.FileInfo) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return raw, fi
	}
	raw0, fi0 := snap()
	unchanged := func(what string) {
		t.Helper()
		raw, fi := snap()
		if !bytes.Equal(raw, raw0) {
			t.Fatalf("%s changed the store:\n%s\nwas\n%s", what, raw, raw0)
		}
		if !os.SameFile(fi, fi0) { // an atomic write is a new file
			t.Fatalf("%s rewrote the store (same bytes)", what)
		}
	}

	for range 3 {
		b.ObserveAgentLevels()
	}
	unchanged("observing again")
	w.write(map[string]string{"apps/a/xbin.json": `{"runtime":"go","agent":"read"}`, "apps/c/xbin.json": `{"runtime":"go"}`})
	w.rescan()
	b.ObserveAgentLevels()
	unchanged("a save that kept the levels, and a rescan")
	w.write(map[string]string{"apps/b/xbin.json": `{"runtime":"go","agent":"full"}`})
	w.rescan()
	b.ObserveAgentLevels()
	if in, asked := b.AgentLevelOf(mustComp(t, b, "apps/b")); in != "off" || asked != "full" {
		t.Fatalf("a raise: in force %q, asked %q", in, asked)
	}
	unchanged("a raise (a request, not a change)")

	// the same levels, whatever order the map holds them in, are the same bytes
	b.agentLv.mu.Lock()
	lv := b.levelsLocked()
	for range 5 {
		if err := b.saveLevelsLocked(lv); err != nil {
			b.agentLv.mu.Unlock()
			t.Fatal(err)
		}
		if raw, _ := os.ReadFile(path); !bytes.Equal(raw, raw0) {
			b.agentLv.mu.Unlock()
			t.Fatalf("the same levels, other bytes:\n%s\nwas\n%s", raw, raw0)
		}
	}
	b.agentLv.mu.Unlock()
	raw0, fi0 = snap()

	// a lowering is a change: written
	w.write(map[string]string{"apps/a/xbin.json": `{"runtime":"go","agent":"off"}`})
	w.rescan()
	b.ObserveAgentLevels()
	if raw, _ := snap(); bytes.Equal(raw, raw0) {
		t.Fatal("a lowering wasn't recorded")
	}
}
