package agent

import (
	"strings"
	"testing"
	"time"
)

func TestLogRingAndSince(t *testing.T) {
	l := NewLog(3, 0)
	l.now = func() time.Time { return time.UnixMilli(1000) }
	for i := 0; i < 5; i++ {
		e := l.Append(New(EvThoughtDelta, map[string]string{"text": strings.Repeat("x", i)}))
		if e.Seq != uint64(i+1) || e.TS != 1000 {
			t.Fatalf("append %d → %+v", i, e)
		}
	}
	got, trunc := l.Since(0)
	if len(got) != 3 || got[0].Seq != 3 || !trunc {
		t.Fatalf("Since(0) = %d events from %d, truncated %v; want 3 from 3, truncated", len(got), got[0].Seq, trunc)
	}
	got, trunc = l.Since(2)
	if len(got) != 3 || trunc {
		t.Fatalf("Since(2): %d, truncated %v — the cursor sits exactly before the oldest kept event", len(got), trunc)
	}
	got, trunc = l.Since(4)
	if len(got) != 1 || got[0].Seq != 5 || trunc {
		t.Fatalf("Since(4) = %v", got)
	}
	if got, _ := l.Since(99); got == nil || len(got) != 0 {
		t.Fatal("past the end: an empty, non-nil slice")
	}
	if l.Last() != 5 {
		t.Fatal("Last")
	}
	// the byte bound trims too, but never below one event
	b := NewLog(0, 100)
	b.Append(New(EvMessageDelta, map[string]string{"text": strings.Repeat("y", 200)}))
	b.Append(New(EvMessageDelta, map[string]string{"text": "z"}))
	if got, _ := b.Since(0); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("byte bound: %v", got)
	}
	// Wait wakes on the next append
	w := l.Wait()
	select {
	case <-w:
		t.Fatal("woke early")
	default:
	}
	l.Append(New(EvStatus, nil))
	select {
	case <-w:
	case <-time.After(time.Second):
		t.Fatal("not woken")
	}
}

func TestProviders(t *testing.T) {
	t.Setenv(FakeEnv, "")
	if _, ok := Lookup("fake"); ok {
		t.Fatal("fake registered without the env")
	}
	t.Setenv(FakeEnv, "/tmp/fakeacp --script x")
	f, ok := Lookup("fake")
	if !ok || len(f.Argv) != 3 || f.Argv[2] != "x" {
		t.Fatalf("fake: %+v", f)
	}
	c, _ := Lookup("claude")
	if m, err := c.ResolveMode(""); err != nil || m != "default" {
		t.Fatalf("default mode: %q %v", m, err)
	}
	if m, err := c.ResolveMode("bypassPermissions"); err != nil || m != "bypassPermissions" {
		t.Fatalf("explicit mode by name: %q %v", m, err)
	}
	if _, err := c.ResolveMode("yolo"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	for _, m := range c.Modes {
		if m.Explicit && m.ID == c.DefaultMode {
			t.Fatal("an explicit mode must never be the default")
		}
	}
	o, _ := Lookup("opencode")
	if m, err := o.ResolveMode("whatever"); err != nil || m != "whatever" {
		t.Fatal("a provider without a mode table lets the agent judge")
	}
	// the auth hint points at the home login, per provider — never a vault key
	if h := LoginHint(c, "apps/x"); !strings.Contains(h, "claude auth login") || !strings.Contains(h, "apps/x") || strings.Contains(h, "vault") {
		t.Fatalf("claude login hint: %q", h)
	}
	if h := LoginHint(o, "apps/x"); !strings.Contains(h, "opencode auth login") {
		t.Fatalf("opencode login hint: %q", h)
	}
}
