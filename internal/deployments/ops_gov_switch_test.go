package deployments

import (
	"net/http"
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers T5 D127h 05-model-§5 — alwaysOn for a non-primary deployment is a
// tile manager's switch in a person's own session, off by default: a
// terminal-level person, the tile's terminal tokens (a manager's included)
// and its frame are refused, changing nothing; never on the primary, whose
// own code decides it; only for a deployment whose own code declares
// alwaysOn (its checkpoint's xbin.json, not the work tree's). Turning it on
// wakes the runner's alwaysOn and AlwaysOnSwitched names it; off takes it
// back, waking nothing.
func TestAlwaysOnNonPrimaryManagerOnly(t *testing.T) {
	f := newGovFx(t, false)
	f.p.MayManage = func(pr auth.Principal, tile string) bool {
		return pr.Component == "" && (pr.IsAdmin() || pr.UserID == "mia")
	}
	f.write(opSite+"/xbin.json", `{"alwaysOn":true}`)
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "bot"})
	f.write(opSite+"/xbin.json", `{}`)
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "plain"})
	f.write(opSite+"/xbin.json", `{"alwaysOn":true}`) // the work tree says it; plain's code doesn't
	f.took()
	on := &SwitchRequest{Tile: opSite, Deployment: "bot", On: ptr(true)}
	frame := userP("dev", opSite, users.LevelTerminal)
	frame.Component, frame.Via = opSite, "frame"
	for _, c := range []struct {
		name   string
		p      auth.Principal
		reason string
	}{
		{"a terminal-level person", userP("dev", opSite, users.LevelTerminal), azManager},
		{"the tile's terminal token", terminalP("dev", opSite, users.LevelTerminal), azSession},
		{"a manager's terminal token", terminalP("mia", opSite, users.LevelTerminal), azSession},
		{"the tile's frame", frame, azSession},
	} {
		_, err := f.do(c.p, OpAlwaysOn, on)
		if bad := azCheckCode(err, c.reason, opSite); bad != "" {
			t.Errorf("%s: %s", c.name, bad)
		}
	}
	if f.rec(opSite).Deployments["bot"].AlwaysOn || len(f.took()) != 0 {
		t.Fatal("a refusal switched alwaysOn or woke the runner")
	}
	_, err := f.do(ownerP, OpAlwaysOn, &SwitchRequest{Tile: opSite, Deployment: "main", On: ptr(true)})
	wantErr(t, "the primary", err, http.StatusConflict, `main is the primary of apps/site — its own code's "alwaysOn" applies to it`)
	_, err = f.do(ownerP, OpAlwaysOn, &SwitchRequest{Tile: opSite, Deployment: "plain", On: ptr(true)})
	wantErr(t, "undeclared", err, http.StatusConflict, `plain's code doesn't declare "alwaysOn"`)

	mia := userP("mia", opSite, users.LevelWrite)
	answerOf(t)(f.do(mia, OpAlwaysOn, on))
	if !f.rec(opSite).Deployments["bot"].AlwaysOn || !slices.Equal(f.p.AlwaysOnSwitched(opSite), []string{"bot"}) {
		t.Errorf("alwaysOn on: %v", f.p.AlwaysOnSwitched(opSite))
	}
	if calls := f.took(); !slices.Equal(calls, []string{"wake"}) {
		t.Errorf("calls = %q", calls)
	}
	answerOf(t)(f.do(mia, OpAlwaysOn, &SwitchRequest{Tile: opSite, Deployment: "bot", On: ptr(false)}))
	if len(f.p.AlwaysOnSwitched(opSite)) != 0 || len(f.took()) != 0 {
		t.Error("alwaysOn off: still switched, or woke the runner")
	}
}

// covers D127n D127q T10 — a deployment's limits default to the tile's (its
// cgroup caps, LimitsFor, every deployment alike); a manager's override
// lowers one for that deployment alone, and null removes it; a value above
// the tile's ceiling, or not a positive integer, is refused with 400; the
// manager gate alone sets them (terminal tokens, a manager's included, and
// element principals are refused). diskGiB is its (scope, name)
// namespace's quota: set on the tile that roots the scope, never above the
// per-scope quota; 501 until the quota is wired. The primary keeps the
// higher CPU weight (D127q).
func TestDeploymentLimitsDefaultToTile(t *testing.T) {
	f := newGovFx(t, false)
	f.p.TileLimits = cgroup.Limits{MemMax: 2 << 30, PidsMax: 512}
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	for _, dep := range []string{"main", "dev", "nope"} {
		if l := f.p.LimitsFor(opSite, dep); l != f.p.TileLimits {
			t.Errorf("%s's default limits = %+v", dep, l)
		}
	}
	patch := func(mem, pids *int64) LimitsPatch {
		var l LimitsPatch
		if mem != nil {
			l.MemMiB = Override{Set: true, Value: mem}
		}
		if pids != nil {
			l.Pids = Override{Set: true, Value: pids}
		}
		return l
	}
	set := &LimitsRequest{Tile: opSite, Deployment: "dev", Limits: patch(ptr(int64(1024)), ptr(int64(128)))}
	for _, pr := range []auth.Principal{terminalP("dev", opSite, users.LevelTerminal), auth.Principal{Owner: false, Component: opSite, Via: "terminal"},
		auth.Principal{Component: opSite, Via: "instance"}, userP("dev", opSite, users.LevelTerminal)} {
		if _, err := f.do(pr, OpLimits, set); err == nil {
			t.Errorf("%s set limits", pr.From())
		}
	}
	answerOf(t)(f.do(ownerP, OpLimits, set))
	if dev, main := f.p.LimitsFor(opSite, "dev"), f.p.LimitsFor(opSite, "main"); dev.MemMax != 1024<<20 || dev.PidsMax != 128 || main != f.p.TileLimits {
		t.Errorf("after the override: dev %+v, main %+v", dev, main)
	}
	for _, c := range []struct {
		l    LimitsPatch
		want string
	}{
		{patch(ptr(int64(4096)), nil), "memMiB can't exceed the tile's ceiling (2048)"},
		{patch(nil, ptr(int64(513))), "pids can't exceed the tile's ceiling (512)"},
		{patch(ptr(int64(0)), nil), "memMiB takes a positive integer, or null to remove the override"},
		{patch(nil, ptr(int64(-1))), "pids takes a positive integer"},
	} {
		_, err := f.do(ownerP, OpLimits, &LimitsRequest{Tile: opSite, Deployment: "dev", Limits: c.l})
		wantErr(t, c.want, err, http.StatusBadRequest, c.want)
	}
	answerOf(t)(f.do(ownerP, OpLimits, &LimitsRequest{Tile: opSite, Deployment: "dev", Limits: LimitsPatch{MemMiB: Override{Set: true}}}))
	if l := f.rec(opSite).Deployments["dev"].Limits; len(l) != 1 || l[LimitPids] != 128 || f.p.LimitsFor(opSite, "dev").MemMax != 2<<30 {
		t.Errorf("after null: %v", l)
	}
	if a := answerOf(t)(f.do(ownerP, OpLimits, &LimitsRequest{Tile: opSite, Deployment: "dev", Limits: patch(nil, ptr(int64(128)))})); !a.Unchanged {
		t.Error("the same limit again changed something")
	}

	disk := func(tile string, v int64) error {
		_, err := f.do(ownerP, OpLimits, &LimitsRequest{Tile: tile, Deployment: "dev", Limits: LimitsPatch{DiskGiB: Override{Set: true, Value: &v}}})
		return err
	}
	wantErr(t, "diskGiB in the workspace scope", disk(opSite, 5), http.StatusConflict, "apps/site keeps its data in the workspace scope")
	f.write("apps/shop/scope.json", "{}")
	f.write("apps/shop/xbin.json", "{}")
	f.write("apps/shop/cart/xbin.json", "{}")
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	f.add(ownerP, &AddRequest{Tile: "apps/shop", Deployment: "dev"})
	f.add(ownerP, &AddRequest{Tile: "apps/shop/cart", Deployment: "dev"})
	wantErr(t, "diskGiB off the scope root", disk("apps/shop/cart", 5), http.StatusConflict, `the quota of apps/shop's "dev" data is set on apps/shop`)
	wantErr(t, "diskGiB above the quota", disk("apps/shop", 51), http.StatusBadRequest, "diskGiB can't exceed the tile's ceiling (50)")
	if err := disk("apps/shop", 20); err != nil || f.rec("apps/shop").Deployments["dev"].Limits[LimitDiskGiB] != 20 {
		t.Errorf("diskGiB on the scope root: %v", err)
	}
	f.p.SetGovHooks(func(h *GovHooks) { h.DiskCeiling = nil })
	wantErr(t, "without the quota", disk("apps/shop", 5), http.StatusNotImplemented, "a deployment's disk limit isn't built")

	if cgroup.PrimaryWeight <= cgroup.NonPrimaryWeight {
		t.Errorf("the primary's CPU weight %d isn't above a non-primary one's %d", cgroup.PrimaryWeight, cgroup.NonPrimaryWeight)
	}
}
