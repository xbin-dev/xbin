package broker

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
)

// A line is judged by BOTH of its ends (review finding 3): a binding lets
// the consumer's code — and a tile agent started in the consumer — reach
// the provider, and a grant reaches into its target. So a tile that turns
// agents away ("agent": "off") is no provider of a planned binding and no
// target of a planned grant, and a provider or a target outside the
// conversation's scope is refused like a consumer outside it.
func TestPlanLinesAreJudgedByBothEnds(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	if code, _ := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
		t.Fatal("opt in")
	}
	// bob's own assistant, and a tile of his that keeps agents out
	done := w.approve(t, w.plan(t, "bob", `[{"kind":"tile.create","args":{"template":"assistant","path":"apps/asst"}}]`).ID, `{"items":[1]}`, bob)
	if done.State != delegation.SetApplied {
		t.Fatalf("the assistant: %s", results(done))
	}
	w.write(map[string]string{"apps/vault/xbin.json": `{"runtime":"go","agent":"off","provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"}}}`, "apps/vault/backend/main.go": "package main\n"})
	w.rescan()
	if err := w.b.Users.SetOwner("apps/vault", "user:bob"); err != nil {
		t.Fatal(err)
	}
	cs := w.plan(t, "bob", `[
		{"kind":"binding.set","args":{"component":"apps/asst","slot":"mcp","provider":"apps/vault"}},
		{"kind":"grant.add","args":{"from":"apps/x","target":"apps/vault","role":"reader"}},
		{"kind":"tile.create","args":{"template":"assistant","path":"apps/asst2"}},
		{"kind":"binding.set","args":{"component":"apps/asst2","slot":"mcp","provider":"apps/vault"}}]`)
	for _, i := range []int{0, 1, 3} {
		if !strings.Contains(cs.Items[i].Blocked, `apps/vault doesn't let a person's agent use it (its agent level is "off")`) {
			t.Errorf("line %d (%s) reaches a tile that turns agents away: blocked %q", i+1, cs.Items[i].Line, cs.Items[i].Blocked)
		}
	}
	// the other end, outside the conversation's scope
	narrow := w.scoped(t, "bob", "apps/x", "apps/asst")
	cs = w.planAs(t, "bob", narrow, `[
		{"kind":"grant.add","args":{"from":"apps/x","target":"apps/pg","role":"writer"}},
		{"kind":"binding.set","args":{"component":"apps/asst","slot":"mcp","provider":"users/alice/mcp"}},
		{"kind":"grant.add","args":{"from":"apps/x","target":"apps/asst","role":"reader"}}]`)
	for i, want := range []bool{true, true, false} {
		if got := strings.Contains(cs.Items[i].Blocked, "hidden by you"); got != want {
			t.Errorf("line %d (%s): blocked %q", i+1, cs.Items[i].Line, cs.Items[i].Blocked)
		}
	}
}

// The same property on tiles that are still to be made (review finding 10):
// a line xbind calls personal while the tiles it names exist only as
// earlier lines of the plan (described from their templates) must be one
// its person can do by hand, with no admin right, once those tiles exist —
// every kind, a planned or an existing tile on either side, every owner.
func TestPersonalLinesOnPlannedTilesNeedNoAdmin(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	const asst = `{"runtime":"go","interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true},"llm":{"kind":"http","service":"openai","multi":true,"required":true},"net":{"kind":"net"}}}`
	const tool = `{"runtime":"go","provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"}}}`
	// tiles that exist already: one of each person's, and the world's own
	for path, owner := range map[string]string{"apps/e-alice-tool": "user:alice", "apps/e-bob-tool": "user:bob", "apps/e-alice-asst": "user:alice", "apps/e-bob-asst": "user:bob"} {
		m := tool
		if strings.HasSuffix(path, "asst") {
			m = asst
		}
		w.write(map[string]string{path + "/xbin.json": m, path + "/backend/main.go": "package main\n"})
		w.rescan()
		if err := w.b.Users.SetOwner(path, owner); err != nil {
			t.Fatal(err)
		}
	}
	type planned struct{ path, manifest, owner string }
	var tiles []planned
	for _, owner := range []string{"user:alice", "user:bob", "org:fin", ""} {
		tag := strings.NewReplacer(":", "-", "user-", "").Replace(owner)
		if tag == "" {
			tag = "ws"
		}
		tiles = append(tiles, planned{"apps/p-" + tag + "-asst", asst, owner}, planned{"apps/p-" + tag + "-tool", tool, owner})
	}
	var pl []delegation.PlannedTile
	names := []string{"apps/x", "apps/pg", "users/alice/mcp", "apps/q", "apps/nope", "apps/e-alice-tool", "apps/e-bob-tool", "apps/e-alice-asst", "apps/e-bob-asst"}
	for i, p := range tiles {
		pl = append(pl, delegation.PlannedTile{N: i + 1, Path: p.path, Template: "t", Owner: p.owner, Manifest: []byte(p.manifest)})
		names = append(names, p.path)
	}
	var ops []delegation.ChangeOp
	op := func(kind string, args map[string]any) {
		ops = append(ops, delegation.ChangeOp{Kind: kind, Args: mustJSON(args)})
	}
	for _, a := range names {
		for _, b := range append([]string{"xbin:llm", "internet", "none", "set:corp"}, names...) {
			for _, slot := range []string{"mcp", "llm", "net", "nope"} {
				op(ChangeBindingSet, map[string]any{"component": a, "slot": slot, "provider": b})
			}
			op(ChangeBindingSet, map[string]any{"component": a, "slot": "mcp", "providers": []string{b, "apps/e-alice-tool"}})
			op(ChangeGrantAdd, map[string]any{"from": a, "target": b, "role": "reader"})
			op(ChangeGrantRevoke, map[string]any{"from": a, "target": b, "role": "reader"})
		}
		op(ChangeBindingRemove, map[string]any{"component": a, "slot": "mcp"})
		op(ChangeOrgShare, map[string]any{"org": "fin", "tile": a, "level": "read"})
		op(ChangeLimitsSet, map[string]any{"tile": a, "kind": "memMiB", "value": 4096})
		op(ChangeTileCreate, map[string]any{"template": "tool", "path": a + "/in"})
		op(ChangeTileCreate, map[string]any{"template": "tool", "path": a})
	}
	// described while the planned tiles are only lines of a plan
	personal := map[string][]delegation.ChangeOp{}
	deferred := map[string]int{}
	for _, user := range []string{"bob", "alice"} {
		p := personP(t, w.partWS, user)
		for _, o := range ops {
			pv, err := w.b.PreviewPlanned(p, o, pl)
			if err != nil || pv.Admin || pv.Blocked != "" {
				continue
			}
			personal[user] = append(personal[user], o)
			if pv.Deferred {
				deferred[user+" "+o.Kind]++
				// never the LLM connector, a named set, or a tile that is another's, an org's or the workspace's
				for _, bad := range []string{"xbin:llm", "set:", "p-fin-", "p-ws-", map[string]string{"bob": "alice", "alice": "bob"}[user]} {
					if strings.Contains(string(o.Args), bad) {
						t.Errorf("%s: a line on a planned tile is called personal though it names %q: %s", user, bad, o.Args)
					}
				}
			}
		}
	}
	for _, k := range []string{"bob binding.set", "alice binding.set"} {
		if deferred[k] == 0 {
			t.Fatalf("no personal %s line on a planned tile in the corpus: the property is checked on nothing (%v)", k, deferred)
		}
	}
	// the tiles are made, as the plan's creations would make them
	for _, p := range tiles {
		w.write(map[string]string{p.path + "/xbin.json": p.manifest, p.path + "/backend/main.go": "package main\n"})
		w.rescan()
		if err := w.b.Users.SetOwner(p.path, p.owner); err != nil {
			t.Fatal(err)
		}
	}
	// …and every line that was called personal is one the person does by hand with no admin right
	for _, user := range []string{"bob", "alice"} {
		p := personP(t, w.partWS, user)
		plainUser := *p.User
		plainUser.Role = users.RoleUser
		plain := p
		plain.User = &plainUser
		for _, o := range personal[user] {
			if o.Kind == ChangeTileCreate {
				continue // (the place is taken now: creations are TestPersonalLinesNeedNoAdmin's)
			}
			got, err := w.b.PreviewPlanned(plain, o, nil)
			if err != nil || got.Blocked != "" || got.Admin {
				t.Errorf("%s: %s %s was called personal while its tiles were planned; with them made, by hand and no admin: err %v, blocked %q, admin %v", user, o.Kind, o.Args, err, got.Blocked, got.Admin)
			}
		}
	}
}
