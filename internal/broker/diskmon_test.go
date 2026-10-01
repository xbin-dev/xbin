package broker

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

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

// covers D166's upgrade check — an admin-only alert (the Go build versions
// alert names tiles and their dependency versions) reaches admins, whatever
// tile it names, and nobody else: not a reader of the tile, not the tile.
func TestAdminAlertsAdminsOnly(t *testing.T) {
	b := testBroker(t)
	b.AdminAlerts = func() []Alert {
		return []Alert{{Level: "warn", Kind: "go-build-versions", Tile: "apps/calendar", Message: "apps/calendar builds with older dependency versions", Dismiss: "/go-build-versions/dismiss"}}
	}
	is := func(a Alert) bool { return a.Kind == "go-build-versions" }
	if a := findAlert(alertsFor(t, b, auth.Principal{Owner: true}), is); a == nil || a.Dismiss != "/go-build-versions/dismiss" {
		t.Errorf("the owner's alerts: %+v", a)
	}
	for _, p := range []auth.Principal{{Component: "apps/calendar", Via: "instance"}, {UserID: "alice", Via: "session"}} {
		if a := findAlert(alertsFor(t, b, p), is); a != nil {
			t.Errorf("%+v sees the admin-only alert", p)
		}
	}
}
