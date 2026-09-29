package acp

import (
	"encoding/json"
	"testing"
)

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
