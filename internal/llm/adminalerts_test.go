package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

func alertsOf(r *rig, kind string) []AdminAlert {
	var out []AdminAlert
	for _, a := range r.s.AdminAlerts() {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// covers D226 — the setup alert: while nothing is configured and no admin
// has touched the connector, admins are told where it is set up; a
// dismissal is the workspace's and stays; an upstream ends it too, and
// removing that upstream doesn't bring it back; a host that locks every
// setting leaves nothing to set up.
func TestSetupAlert(t *testing.T) {
	r := newRig(t, "")
	setup := alertsOf(r, AlertSetup)
	if len(setup) != 1 || setup[0].Level != "warn" || setup[0].Dismiss != SetupDismissRoute ||
		!strings.Contains(setup[0].Message, PagePath) || !strings.Contains(setup[0].Message, "bx llm upstream add") {
		t.Fatalf("an unconfigured connector's alerts: %+v", r.s.AdminAlerts())
	}
	for _, p := range []auth.Principal{pUser("bob"), pInstance("apps/agent")} {
		if a := r.do(p, "POST", SetupDismissRoute, nil); a.code != 403 {
			t.Errorf("dismiss as %+v: %d", p, a.code)
		}
	}
	if len(alertsOf(r, AlertSetup)) != 1 {
		t.Fatal("a refused dismissal dismissed it")
	}
	r.must(200, pAdmin, "POST", SetupDismissRoute, nil)
	if got := r.s.AdminAlerts(); len(got) != 0 {
		t.Fatalf("after the dismissal: %+v", got)
	}
	// the shell an admin drives shows the alert with its Dismiss, and posts with its own token
	if err := os.Remove(filepath.Join(r.dir, setupFile)); err != nil {
		t.Fatal(err)
	}
	r.s.opt.AdminShell = func(p auth.Principal) bool { return p.Component == "shell" && p.UserID == "ada" }
	if a := r.do(pFrame("shell", "bob"), "POST", SetupDismissRoute, nil); a.code != 403 {
		t.Errorf("dismiss from another person's shell: %d", a.code)
	}
	r.must(200, pFrame("shell", "ada"), "POST", SetupDismissRoute, nil)
	if got := r.s.AdminAlerts(); len(got) != 0 {
		t.Fatalf("after an admin's shell dismissed it: %+v", got)
	}
	if a := r.do(pFrame("shell", "ada"), "GET", "/llm/config", nil); a.code != 403 {
		t.Errorf("an admin's shell reads the settings: %d (it dismisses the alert, nothing else)", a.code)
	}
	// it is the workspace's, and kept: another xbind on the same data
	if got := newRigAt(t, r.dir, "", r.vault).s.AdminAlerts(); len(got) != 0 {
		t.Errorf("after a restart: %+v", got)
	}
	// the file is the state: without it the alert is back (the harness re-arms it so)
	if err := os.Remove(filepath.Join(r.dir, setupFile)); err != nil {
		t.Fatal(err)
	}
	if len(alertsOf(r, AlertSetup)) != 1 {
		t.Error("the marker removed: no alert")
	}

	// an upstream ends it, and an admin who has changed anything is not asked again
	r.addUpstream("main", newUpstream(t), nil)
	if got := r.s.AdminAlerts(); len(got) != 0 {
		t.Errorf("configured: %+v", got)
	}
	r.must(200, pAdmin, "DELETE", "/llm/upstreams/main", nil)
	if got := r.s.AdminAlerts(); len(got) != 0 {
		t.Errorf("after its one upstream was removed: %+v", got)
	}

	// a host that locks every setting: nothing an admin could set up
	locked := newRig(t, writeHost(t, t.TempDir(), `{"lockAll": true}`, ""))
	if got := locked.s.AdminAlerts(); len(got) != 0 {
		t.Errorf("lockAll without upstreams: %+v", got)
	}
	// a host that brings the upstream: configured from the first boot
	hosted := newRig(t, writeHost(t, t.TempDir(), `{"upstreams": {"house": {"baseURL": "http://127.0.0.1:9/v1", "keyFile": "upstream.key"}}}`, "k"))
	if got := hosted.s.AdminAlerts(); len(got) != 0 {
		t.Errorf("a host's upstream: %+v", got)
	}
}

// covers D226 — quota alerts: a person at a limit is named to the admins
// with the limit, where it comes from (their own, an org's, the default),
// what it does (refuses, or only warns), when it resets and where it is
// changed; the workspace's own quota is critical; many people at once are
// one alert; a reset or a raise ends it. The person reads their own limits
// in GET /llm whether or not the people switch admits their direct calls.
func TestQuotaAlerts(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t) // each call: 120 tokens
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone"})
	r.orgs["ivy"] = []string{"interns"}
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{
		"default": map[string]any{"tokensPerDay": 200},
		"orgs":    map[string]any{"interns": map[string]any{"tokensPerDay": 100, "action": "warn"}},
		"users":   map[string]any{"cy": map[string]any{"costPerMonth": 5, "tokensPerDay": -1}},
	})
	call := func(p auth.Principal) answer {
		return r.do(p, "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
	}
	call(pUser("bob"))
	if got := alertsOf(r, AlertQuota); len(got) != 0 {
		t.Fatalf("under every limit: %+v", got)
	}
	// alerts.go's own notice rides along under its kind: cy's cost quota can't count a model without a price
	if got := alertsOf(r, AlertKind); len(got) != 1 || !strings.Contains(got[0].Message, "have no price") {
		t.Errorf("the unpriced-calls alert: %+v", r.s.AdminAlerts())
	}
	call(pUser("bob")) // 240 of 200: stopped
	call(pUser("ivy")) // 120 of 100: warned
	call(pUser("cy"))  // no price, no cost: nothing reached
	got := alertsOf(r, AlertQuota)
	if len(got) != 2 {
		t.Fatalf("alerts: %+v", r.s.AdminAlerts())
	}
	bob, ivy := got[0], got[1]
	if bob.Level != "warn" || bob.Dismiss != "" || bob.Message != "LLM quota: bob reached their daily limit of 200 tokens (the default quota): their calls are refused until 2026-11-15 00:00 UTC"+
		" — raise or lift it at /xbin/llm (the admin console's workspace → LLM), or `bx llm quota set user:bob …`" {
		t.Errorf("bob's alert: %+v", bob)
	}
	if !strings.Contains(ivy.Message, "ivy reached their daily limit of 100 tokens (the quota of org interns)") ||
		!strings.Contains(ivy.Message, "their calls still go through (this quota only warns)") || !strings.Contains(ivy.Message, PagePath) {
		t.Errorf("ivy's alert: %+v", ivy)
	}

	// the person reads their own limits, admitted or not
	for _, people := range []string{"everyone", "admins", "nobody"} {
		r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": people})
		st := r.must(200, pUser("bob"), "GET", "/llm", nil).json(t)
		q, _ := st["quota"].(map[string]any)
		if q == nil || st["user"] != "bob" || q["state"] != "stop" || q["user"] != "bob" || st["callable"] != (people == "everyone") {
			t.Errorf("people=%s: bob's status %v", people, st)
		}
	}
	// a tile's own credential reads nobody's limits
	r.bound["apps/agent"] = true
	if st := r.must(200, pInstance("apps/other"), "GET", "/llm", nil).json(t); st["quota"] != nil || st["user"] != nil {
		t.Errorf("an unbound component's status: %v", st)
	}

	// a user override ends bob's; the workspace's own quota is critical
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"users": map[string]any{"bob": map[string]any{"tokensPerDay": 1000000}},
		"workspace": map[string]any{"tokensPerMonth": 400, "costPerMonth": 100}})
	got = alertsOf(r, AlertQuota)
	if len(got) != 2 || got[0].Level != "crit" || !strings.HasPrefix(got[0].Message, "LLM quota: the workspace reached its monthly limit of 400 tokens: every call is refused until 2026-12-01 00:00 UTC") ||
		!strings.Contains(got[0].Message, "bx llm quota set workspace") || !strings.Contains(got[1].Message, "ivy") {
		t.Errorf("with the workspace's quota reached: %+v", got)
	}
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"workspace": nil})

	// many people at a limit: one alert
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone"})
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"default": map[string]any{"tokensPerDay": 100}})
	for i := range 7 {
		call(pUser(fmt.Sprintf("u%d", i)))
	}
	got = alertsOf(r, AlertQuota)
	if len(got) != 1 || !strings.HasPrefix(got[0].Message, "LLM quota: 8 people have reached a limit (ivy, u0, u1, u2, u3 and 3 more); the calls of 7 are refused") {
		t.Errorf("eight people at a limit: %+v", got)
	}

	// the next UTC day: nothing is reached
	r.clock.set(time.Date(2026, 11, 15, 0, 0, 1, 0, time.UTC))
	if got := alertsOf(r, AlertQuota); len(got) != 0 {
		t.Errorf("after the reset: %+v", got)
	}
}

// covers D226 — a workspace quota the host's file holds says so, and names
// no command that would be refused.
func TestQuotaAlertHostLocked(t *testing.T) {
	up := newUpstream(t)
	r := newRig(t, writeHost(t, t.TempDir(), `{"quotas": {"workspace": {"tokensPerDay": 100, "action": "warn"}}}`, ""))
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.do(pOwner, "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
	got := alertsOf(r, AlertQuota)
	if len(got) != 1 || got[0].Level != "warn" || !strings.Contains(got[0].Message, "the workspace reached its daily limit of 100 tokens (set by your host): calls still go through") ||
		!strings.Contains(got[0].Message, "your host holds this quota") || strings.Contains(got[0].Message, "bx llm quota set") {
		t.Errorf("a host's workspace quota: %+v", got)
	}
}

func TestTokenWords(t *testing.T) {
	for n, want := range map[int64]string{2_000_000: "2M", 200_000: "200k", 1500: "1500", 1000: "1k", 999: "999", 2_500_000: "2500k"} {
		if got := tokenWords(n); got != want {
			t.Errorf("tokenWords(%d) = %q, want %q", n, got, want)
		}
	}
}
