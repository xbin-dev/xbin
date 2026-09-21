package agent

import (
	"encoding/json"
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

func TestPermissionsFirstAnswerWins(t *testing.T) {
	p := NewPermissions()
	opts := []PermissionOption{{OptionID: "once", Kind: AllowOnce}, {OptionID: "always", Kind: AllowAlways}, {OptionID: "no", Kind: RejectOnce}}
	pd, auto := p.Request(ToolCallRef{ID: "t1", Title: "run ls", Kind: "execute"}, opts, json.RawMessage(`7`))
	if auto != nil || pd.PID != "p1" || p.Count() != 1 {
		t.Fatalf("request: %+v %+v", pd, auto)
	}
	if _, err := p.Resolve("p1", "", "reject_always", "user:a"); err == nil {
		t.Fatal("a decision the agent did not offer must fail, leaving the request pending")
	}
	res, err := p.Resolve("p1", "", AllowOnce, "user:a")
	if err != nil || res.OptionID != "once" || res.By != "user:a" || string(res.RPCID) != "7" {
		t.Fatalf("resolve: %+v %v", res, err)
	}
	if _, err := p.Resolve("p1", "once", "", "user:b"); err == nil {
		t.Fatal("second answer must fail")
	}
	if p.Count() != 0 {
		t.Fatal("still pending")
	}
	// allow_always records a rule: the same kind+title auto-resolves next time
	p.Request(ToolCallRef{ID: "t2", Title: "run ls", Kind: "execute"}, opts, json.RawMessage(`8`))
	if _, err := p.Resolve("p2", "always", "", "user:a"); err != nil {
		t.Fatal(err)
	}
	pd, auto = p.Request(ToolCallRef{ID: "t3", Title: "run ls", Kind: "execute"}, opts, json.RawMessage(`9`))
	if auto == nil || auto.By != "auto" || auto.OptionID != "always" || p.Count() != 0 {
		t.Fatalf("auto-allow: %+v %+v", pd, auto)
	}
	if _, auto = p.Request(ToolCallRef{ID: "t4", Title: "rm -rf", Kind: "execute"}, opts, json.RawMessage(`10`)); auto != nil {
		t.Fatal("a different title is not covered")
	}
	// the "always" decision on an agent that offers no allow_always allows
	// once and remembers NOTHING: the agent granted a one-shot approval, and
	// xbin must not turn it into a standing rule the agent never gave
	q := NewPermissions()
	onceOnly := []PermissionOption{{OptionID: "y", Kind: AllowOnce}, {OptionID: "n", Kind: RejectOnce}}
	q.Request(ToolCallRef{ID: "t", Title: "x", Kind: "edit"}, onceOnly, json.RawMessage(`1`))
	if res, err := q.Resolve("p1", "", AllowAlways, "user:a"); err != nil || res.OptionID != "y" {
		t.Fatalf("always without an always option: %+v %v", res, err)
	}
	if _, auto := q.Request(ToolCallRef{ID: "t", Title: "x", Kind: "edit"}, onceOnly, json.RawMessage(`2`)); auto != nil {
		t.Fatal("a fallback to allow_once must not record a rule")
	}
	// cancel settles everything, by rpc id or all
	c := NewPermissions()
	c.Request(ToolCallRef{ID: "a"}, opts, json.RawMessage(`"r1"`))
	c.Request(ToolCallRef{ID: "b"}, opts, json.RawMessage(`"r2"`))
	if r := c.CancelByRPC(json.RawMessage(`"r1"`)); r == nil || r.PID != "p1" || !r.Cancel {
		t.Fatalf("cancel by rpc: %+v", r)
	}
	if all := c.CancelAll(); len(all) != 1 || all[0].PID != "p2" || c.Count() != 0 {
		t.Fatalf("cancel all: %+v", all)
	}
	if l := p.List(); len(l) != 1 || l[0].PID != "p4" {
		t.Fatalf("list: %+v", l)
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
	if h := c.LoginHint("apps/x"); !strings.Contains(h, "claude /login") || !strings.Contains(h, "apps/x") || strings.Contains(h, "vault") {
		t.Fatalf("claude login hint: %q", h)
	}
	if h := o.LoginHint("apps/x"); !strings.Contains(h, "opencode auth login") {
		t.Fatalf("opencode login hint: %q", h)
	}
}
