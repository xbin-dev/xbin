package sbx

import (
	"encoding/json"
	"testing"
	"time"
)

// covers D119c D127h SC-ZERO Z8 PO-11 — the sandbox registry's wire shape for a
// zero-state tile, pinned byte for byte before tile deployments exist: an
// entry that names no deployment marshals exactly as today (no "deployment"
// key, whatever field joins Entry later), and so does a failure row, which
// still coalesces on today's key. The expectations are hand-maintained
// goldens: changing one is a compat change (12-compat), never a
// regeneration.
func TestEntryJSONZeroState(t *testing.T) {
	started := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	entries := []struct {
		name string
		e    Entry
		want string
	}{
		{"zero value", Entry{},
			`{"id":"","kind":"","tile":"","mode":"","started":"0001-01-01T00:00:00Z"}`},
		// the runner's row for a backend generation (internal/runner/sbx.go):
		// ID backend:<CompKey>:g<gen>, the flat cgroup leaf <CompKey>
		{"namespace backend", Entry{ID: "backend:apps~cal-c2360189:g3", Kind: Backend, Tile: "apps/cal",
			Mode: Namespace, PID: 4242, Gen: 3, Started: started, Leaf: "apps~cal-c2360189"},
			`{"id":"backend:apps~cal-c2360189:g3","kind":"backend","tile":"apps/cal","mode":"namespace",` +
				`"pid":4242,"gen":3,"started":"2026-09-27T10:00:00Z","leaf":"apps~cal-c2360189"}`},
		{"host backend", Entry{ID: "backend:apps~cal-c2360189:g1", Kind: Backend, Tile: "apps/cal",
			Mode: Host, PID: 7, Gen: 1, Started: started},
			`{"id":"backend:apps~cal-c2360189:g1","kind":"backend","tile":"apps/cal","mode":"host",` +
				`"pid":7,"gen":1,"started":"2026-09-27T10:00:00Z"}`},
		{"vm backend", Entry{ID: "backend:apps~vm-af7b02dc:g2", Kind: Backend, Tile: "apps/vm", Mode: VM,
			Accel: Emulate, MemMiB: 512, VCPUs: 2, PID: 99, Gen: 2, Started: started, Leaf: "apps~vm-af7b02dc",
			Disk: "/ws/.xbin/vm/apps~vm-af7b02dc.img", Net: "internet"},
			`{"id":"backend:apps~vm-af7b02dc:g2","kind":"backend","tile":"apps/vm","mode":"vm","accel":"emulate",` +
				`"memMiB":512,"vcpus":2,"pid":99,"gen":2,"started":"2026-09-27T10:00:00Z","leaf":"apps~vm-af7b02dc",` +
				`"disk":"/ws/.xbin/vm/apps~vm-af7b02dc.img","net":"internet"}`},
		{"restricted terminal", Entry{ID: "term-1", Kind: Terminal, Tile: "apps/cal", User: "alice",
			Mode: Namespace, PID: 5, Started: started, Restricted: true},
			`{"id":"term-1","kind":"terminal","tile":"apps/cal","user":"alice","mode":"namespace",` +
				`"pid":5,"started":"2026-09-27T10:00:00Z","restricted":true}`},
		{"agent session", Entry{ID: "agent-9", Kind: Agent, Tile: "apps/cal", User: "bob", Label: "claude",
			Mode: VM, Accel: KVM, MemMiB: 2048, VCPUs: 2, Started: started},
			`{"id":"agent-9","kind":"agent","tile":"apps/cal","user":"bob","label":"claude","mode":"vm",` +
				`"accel":"kvm","memMiB":2048,"vcpus":2,` +
				`"started":"2026-09-27T10:00:00Z"}`},
		{"tile-managed child", Entry{ID: "sbx-1", Kind: Tile, Tile: "apps/cal", Parent: "backend:apps~cal-c2360189:g3",
			Mode: Namespace, Started: started},
			`{"id":"sbx-1","kind":"tile","tile":"apps/cal","parent":"backend:apps~cal-c2360189:g3",` +
				`"mode":"namespace","started":"2026-09-27T10:00:00Z"}`},
	}
	for _, c := range entries {
		got, err := json.Marshal(c.e)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.want {
			t.Errorf("%s: an entry naming no deployment must marshal as today\n got: %s\nwant: %s", c.name, got, c.want)
		}
	}

	// A listing round-trips through the registry unchanged (List copies).
	r := New()
	r.Add(entries[1].e)
	list, _ := json.Marshal(r.List(Filter{}))
	if want := "[" + entries[1].want + "]"; string(list) != want {
		t.Errorf("listing:\n got: %s\nwant: %s", list, want)
	}

	// Failures: today's row, and today's coalescing key — the same failure
	// within ten minutes is one row with a count.
	r = New()
	r.now = func() time.Time { return started }
	f := Failure{Kind: Backend, Tile: "apps/cal", Mode: Namespace, Stage: Start, Error: "boom"}
	r.Fail(f)
	r.now = func() time.Time { return started.Add(time.Minute) }
	r.Fail(f)
	r.Fail(Failure{Kind: Terminal, Tile: "apps/cal", User: "alice", Mode: Host, Stage: Refused, Error: "no"})
	got, _ := json.Marshal(r.Failures(Filter{}))
	want := `[{"time":"2026-09-27T10:01:00Z","kind":"terminal","tile":"apps/cal","user":"alice","mode":"host",` +
		`"stage":"refused","error":"no","count":1},` +
		`{"time":"2026-09-27T10:01:00Z","kind":"backend","tile":"apps/cal","mode":"namespace",` +
		`"stage":"start","error":"boom","count":2}]`
	if string(got) != want {
		t.Errorf("failure ring must keep today's rows and coalescing\n got: %s\nwant: %s", got, want)
	}
}
