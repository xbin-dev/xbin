package broker

import "testing"

func TestDiskMonQuota(t *testing.T) {
	usage := map[string]int64{"apps~big": 60 << 30, "apps~small": 1 << 30}
	d := newDiskMon(t.TempDir(), 0, func() map[string]int64 { return usage }) // quota → 50 GiB default
	d.scan()

	if _, blocked := d.Blocked("apps~big"); !blocked {
		t.Error("a scope over its 50 GiB quota must be write-blocked")
	}
	if _, blocked := d.Blocked("apps~small"); blocked {
		t.Error("a small scope must not be blocked")
	}
	// One crit quota alert, tile-scoped, naming the offender.
	var got *Alert
	for i := range d.Alerts() {
		if a := d.Alerts()[i]; a.Kind == "quota" && a.Tile == "apps~big" {
			got = &a
		}
	}
	if got == nil || got.Level != "crit" {
		t.Fatalf("expected a crit quota alert for apps~big, got %+v", d.Alerts())
	}
	// extra alert sources (cgroup at-limit) are folded in.
	d.extra = func() []Alert { return []Alert{{Level: "warn", Kind: "oom", Tile: "apps/x", Message: "oom"}} }
	d.scan()
	found := false
	for _, a := range d.Alerts() {
		if a.Kind == "oom" {
			found = true
		}
	}
	if !found {
		t.Error("extra (cgroup) alerts must be folded into Alerts()")
	}
}
