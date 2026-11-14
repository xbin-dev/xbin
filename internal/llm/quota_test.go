package llm

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// covers D226 — whose quota applies: a user's override, else the most
// generous of their orgs' per limit, else the default; each limit on its
// own; -1 lifts a limit.
func TestQuotaResolution(t *testing.T) {
	rules := QuotaRules{
		Default: &Quota{TokensPerDay: 1000, TokensPerMonth: 20000, CostPerMonth: 10},
		Users: map[string]Quota{
			"low":  {TokensPerDay: 100},                 // below everything: the user override wins downwards too
			"free": {TokensPerDay: Unlimited},           // lifts the daily limit, keeps the rest
			"warn": {TokensPerDay: 500, Action: "warn"}, // their action reaches the limits they inherit
		},
		Orgs: map[string]Quota{
			"eng":     {TokensPerDay: 5000, CostPerDay: 3},
			"interns": {TokensPerDay: 200, TokensPerMonth: 1000, Action: "warn"},
			"labs":    {TokensPerMonth: Unlimited},
		},
	}
	type lim struct {
		name         string
		value        float64
		source, actn string
	}
	for _, c := range []struct {
		what string
		user string
		orgs []string
		want []lim
	}{
		{"nobody special: the default", "ann", nil, []lim{{"tokensPerDay", 1000, "default", "stop"}, {"tokensPerMonth", 20000, "default", "stop"}, {"costPerMonth", 10, "default", "stop"}}},
		{"one org: its limits replace the default's, limit by limit", "ann", []string{"interns"},
			[]lim{{"tokensPerDay", 200, "org:interns", "warn"}, {"tokensPerMonth", 1000, "org:interns", "warn"}, {"costPerMonth", 10, "default", "warn"}}},
		{"two orgs: the most generous per limit", "ann", []string{"eng", "interns"},
			[]lim{{"tokensPerDay", 5000, "org:eng", "warn"}, {"tokensPerMonth", 1000, "org:interns", "warn"}, {"costPerDay", 3, "org:eng", "warn"}, {"costPerMonth", 10, "default", "warn"}}},
		{"an org that lifts a limit beats every number", "ann", []string{"interns", "labs"},
			[]lim{{"tokensPerDay", 200, "org:interns", "warn"}, {"costPerMonth", 10, "default", "warn"}}},
		{"the user override wins over their orgs, downwards too", "low", []string{"eng"},
			[]lim{{"tokensPerDay", 100, "user", "stop"}, {"tokensPerMonth", 20000, "default", "stop"}, {"costPerDay", 3, "org:eng", "stop"}, {"costPerMonth", 10, "default", "stop"}}},
		{"a user override that lifts one limit", "free", nil, []lim{{"tokensPerMonth", 20000, "default", "stop"}, {"costPerMonth", 10, "default", "stop"}}},
		{"a user override's action", "warn", []string{"eng"},
			[]lim{{"tokensPerDay", 500, "user", "warn"}, {"tokensPerMonth", 20000, "default", "warn"}, {"costPerDay", 3, "org:eng", "warn"}, {"costPerMonth", 10, "default", "warn"}}},
		{"an org without an override changes nothing", "ann", []string{"sales"}, []lim{{"tokensPerDay", 1000, "default", "stop"}, {"tokensPerMonth", 20000, "default", "stop"}, {"costPerMonth", 10, "default", "stop"}}},
	} {
		got := rules.resolve(c.user, c.orgs)
		ok := len(got) == len(c.want)
		for i := 0; ok && i < len(got); i++ {
			ok = got[i].Name == c.want[i].name && got[i].Value == c.want[i].value && got[i].Source == c.want[i].source && got[i].Action == c.want[i].actn
		}
		if !ok {
			t.Errorf("%s:\n got  %+v\n want %+v", c.what, got, c.want)
		}
	}
	if l := (QuotaRules{}).resolve("ann", []string{"eng"}); len(l) != 0 {
		t.Errorf("no rules: %+v", l)
	}
	if l := rules.workspaceLimits(); len(l) != 0 {
		t.Errorf("no workspace quota: %+v", l)
	}
}

func TestQuotaWindowsAndVerdict(t *testing.T) {
	now := time.Date(2026, 12, 31, 23, 30, 0, 0, time.FixedZone("CET", 3600)) // 22:30 UTC on the 31st
	if got := resetOf("tokensPerDay", now).Format(time.RFC3339); got != "2027-01-01T00:00:00Z" {
		t.Errorf("the day's reset: %s", got)
	}
	if got := resetOf("costPerMonth", now).Format(time.RFC3339); got != "2027-01-01T00:00:00Z" {
		t.Errorf("the month's reset: %s", got)
	}
	if got := resetOf("tokensPerMonth", time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC)).Format(time.RFC3339); got != "2026-03-01T00:00:00Z" {
		t.Errorf("February's reset: %s", got)
	}
	w := window{dayTokens: 1000, monthTokens: 5000, dayCost: 0.5, monthCost: 10}
	limits := judge([]Limit{
		{Name: "tokensPerDay", Value: 1001, Action: "stop", Source: "user"}, // one token left
		{Name: "tokensPerMonth", Value: 5000, Action: "warn", Source: "default"},
		{Name: "costPerDay", Value: 0.5, Action: "stop", Source: "org:eng"},
		{Name: "costPerMonth", Value: 9.99, Action: "stop", Source: "workspace"},
	}, w, now)
	states := []string{limits[0].State, limits[1].State, limits[2].State, limits[3].State}
	if strings.Join(states, ",") != "ok,warn,stop,stop" {
		t.Errorf("states: %v", states)
	}
	v := verdictOf(limits)
	if v.Stop == nil || v.Stop.quotaName() != "org:eng.costPerDay" || v.Warn == nil || v.Warn.quotaName() != "default.tokensPerMonth" {
		t.Errorf("verdict: %+v", v)
	}
	if d := limits[2].describe(); d != "your daily LLM cost quota of $0.50 is used up ($0.50 used); it resets at 2027-01-01T00:00:00Z" {
		t.Errorf("describe: %s", d)
	}
	if d := limits[3].describe(); !strings.HasPrefix(d, "the workspace's monthly LLM cost quota of $9.99 is used up ($10.00 used)") {
		t.Errorf("describe: %s", d)
	}
	for _, bad := range []Quota{{TokensPerDay: -2}, {CostPerMonth: -0.5}, {Action: "explode"}} {
		if bad.validate("q") == nil {
			t.Errorf("%+v validated", bad)
		}
	}
	if (Quota{TokensPerDay: Unlimited, CostPerDay: Unlimited, Action: "warn"}).validate("q") != nil {
		t.Error("unlimited is valid")
	}
}

// covers D226 — the meter: counters per day, person, component and model,
// written to one file per month and read back; a month's file this xbind
// can't read is never written over.
func TestMeterPersistsAndRolls(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.DiscardHandler)
	m := newMeter(dir, log)
	u := sdkllm.Usage{InputTokens: 100, OutputTokens: 50, CachedTokens: 40, ReasoningTokens: 10, TotalTokens: 150}
	d1 := time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC)
	m.record(d1, "ann", "apps/agent", "main/gpt-5", u, 0.25, true, false)
	m.record(d1, "ann", "apps/agent", "main/gpt-5", u, 0.25, true, false)
	m.record(d1, "ann", "", "main/local", u, 0, false, false)                   // no price: unpriced
	m.record(d1, "", "apps/cron", "main/gpt-5", sdkllm.Usage{}, 0, false, true) // failed, nobody acting
	person, ws := m.windows(d1, "ann")
	if person != (window{dayTokens: 450, monthTokens: 450, dayCost: 0.5, monthCost: 0.5}) || ws != person {
		t.Errorf("windows: %+v, %+v", person, ws)
	}
	// the next day, which is the next month: the windows start afresh, the rows stay
	d2 := d1.Add(2 * time.Hour)
	if person, ws := m.windows(d2, "ann"); person != (window{}) || ws != (window{}) {
		t.Errorf("after the month rolled: %+v, %+v", person, ws)
	}
	m.record(d2, "ann", "", "main/gpt-5", u, 1, true, false)
	m.close()
	// a call that ends while xbind stops is still saved
	m.record(d2, "late", "", "main/gpt-5", u, 0, false, false)
	if raw, _ := os.ReadFile(filepath.Join(dir, "2026-12.json")); !strings.Contains(string(raw), `"late"`) {
		t.Errorf("a count after close was not written: %s", raw)
	}

	for _, month := range []string{"2026-11", "2026-12"} {
		raw, err := os.ReadFile(filepath.Join(dir, month+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var f usageFile
		if err := json.Unmarshal(raw, &f); err != nil || f.Schema != 1 || f.Month != month {
			t.Fatalf("%s: %v %+v", month, err, f)
		}
		if fi, _ := os.Stat(filepath.Join(dir, month+".json")); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v", month, fi.Mode().Perm())
		}
	}

	m2 := newMeter(dir, log)
	rows := m2.query("2026-11-01", "2026-12-31", false, func(user, _, _ string) bool { return user != "late" })
	if len(rows) != 4 {
		t.Fatalf("rows read back: %+v", rows)
	}
	want := UsageRow{Day: "2026-11-30", User: "ann", Component: "apps/agent", Model: "main/gpt-5",
		Counters: Counters{Requests: 2, InputTokens: 200, OutputTokens: 100, CachedTokens: 80, ReasoningTokens: 20, Cost: 0.5}}
	if rows[2] != want {
		t.Errorf("row: %+v\nwant %+v", rows[2], want)
	}
	if rows[0].User != "" || rows[0].Failed != 1 || rows[0].Requests != 1 || rows[1].Unpriced != 1 || rows[3].Day != "2026-12-01" {
		t.Errorf("rows: %+v", rows)
	}
	// by month, filtered
	months := m2.query("2026-01-01", "2026-12-31", true, func(user, _, _ string) bool { return user == "ann" })
	if len(months) != 3 || months[0].Month != "2026-11" || months[0].Day != "" || months[2].Month != "2026-12" || months[2].Cost != 1 {
		t.Errorf("by month: %+v", months)
	}
	// the windows are rebuilt from the files
	if person, _ := m2.windows(d2.Add(time.Hour), "ann"); person.monthTokens != 150 || person.dayTokens != 150 || person.monthCost != 1 {
		t.Errorf("the windows after a restart: %+v", person)
	}
	m2.close()

	// a month file that can't be read: counted in memory, left as it is
	broken := filepath.Join(dir, "2027-01.json")
	if err := os.WriteFile(broken, []byte(`{"schema": 99, "month": "2027-01", "rows": "from the future"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m3 := newMeter(dir, log)
	jan := time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC)
	m3.record(jan, "ann", "", "main/gpt-5", u, 0, false, false)
	if person, _ := m3.windows(jan, "ann"); person.dayTokens != 150 {
		t.Errorf("a broken month still counts in memory: %+v", person)
	}
	m3.close()
	if raw, _ := os.ReadFile(broken); !strings.Contains(string(raw), "from the future") {
		t.Errorf("the unreadable file was written over: %s", raw)
	}
	// old months go when a new one starts
	if err := os.WriteFile(filepath.Join(dir, "2020-01.json"), []byte(`{"schema":1,"month":"2020-01","rows":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m4 := newMeter(dir, log)
	m4.record(time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), "ann", "", "main/gpt-5", u, 0, false, false)
	m4.close()
	if _, err := os.Stat(filepath.Join(dir, "2020-01.json")); err == nil {
		t.Error("a month older than the retention stayed")
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-11.json")); err != nil {
		t.Error("a month within the retention went")
	}
}

// covers D226 — who reads usage: a person their own rows, an admin all of
// them, a tile's credentials none.
func TestUsageScopes(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone"})
	r.bound["apps/agent"] = true
	r.must(200, pUser("bob"), "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
	r.must(200, pUser("cy"), "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
	r.must(200, pFrame("apps/agent", "bob"), "POST", "/llm/v1/responses", chatBody("gpt-5", true))
	r.must(200, pInstance("apps/agent"), "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))

	type usage struct {
		Scope  string     `json:"scope"`
		Rows   []UsageRow `json:"rows"`
		Totals Counters   `json:"totals"`
	}
	read := func(a answer) usage {
		t.Helper()
		var u usage
		if err := json.Unmarshal(a.body, &u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	own := read(r.must(200, pUser("bob"), "GET", "/llm/usage", nil))
	if own.Scope != "own" || len(own.Rows) != 2 || own.Totals.Requests != 2 || own.Totals.InputTokens != 200 {
		t.Errorf("bob's own usage: %+v", own)
	}
	for _, row := range own.Rows {
		if row.User != "bob" {
			t.Errorf("bob read a row of %q", row.User)
		}
	}
	if a := r.do(pUser("bob"), "GET", "/llm/usage?user=cy", nil); a.code != 403 {
		t.Errorf("bob reading cy's usage: %d", a.code)
	}
	all := read(r.must(200, pAdmin, "GET", "/llm/usage", nil))
	if all.Scope != "all" || len(all.Rows) != 4 || all.Totals.Requests != 4 {
		t.Errorf("an admin's view: %+v", all)
	}
	if u := read(r.must(200, pAdmin, "GET", "/llm/usage?user=cy", nil)); len(u.Rows) != 1 || u.Rows[0].User != "cy" {
		t.Errorf("an admin filtering by person: %+v", u)
	}
	if u := read(r.must(200, pAdmin, "GET", "/llm/usage?component=apps/agent&period=month", nil)); len(u.Rows) != 2 || u.Rows[0].Month != "2026-11" || u.Rows[0].Day != "" {
		t.Errorf("an admin filtering by component, by month: %+v", u)
	}
	if u := read(r.must(200, pAdmin, "GET", "/llm/usage?user=", nil)); len(u.Rows) != 1 || u.Rows[0].Component != "apps/agent" || u.Rows[0].User != "" {
		t.Errorf("the calls nobody acted in: %+v", u)
	}
	// a tile's credentials read no usage, whoever drives them — the admin console (a tile holding xbin:admin) does
	if a := r.do(pFrame("apps/agent", "bob"), "GET", "/llm/usage", nil); a.code != 403 {
		t.Errorf("a tile's frame reading usage: %d", a.code)
	}
	if a := r.do(pInstance("apps/agent"), "GET", "/llm/usage", nil); a.code != 403 {
		t.Errorf("a tile's backend reading usage: %d", a.code)
	}
	if a := r.do(pFrame("apps/admin", "ada"), "GET", "/llm/usage", nil); a.code != 200 {
		t.Errorf("the admin console reading usage: %d", a.code)
	}
	for _, bad := range []string{"?period=week", "?from=yesterday", "?from=2026-12-01&to=2026-11-01"} {
		if a := r.do(pAdmin, "GET", "/llm/usage"+bad, nil); a.code != 400 {
			t.Errorf("GET /llm/usage%s: %d", bad, a.code)
		}
	}
	// the counters survive a restart
	r.s.Close()
	r2 := newRigAt(t, r.dir, "", r.vault)
	if u := read(r2.must(200, pAdmin, "GET", "/llm/usage", nil)); u.Totals.Requests != 4 || u.Totals.OutputTokens != 80 {
		t.Errorf("after a restart: %+v", u.Totals)
	}
	// quota rules: admins only, with a person's limits on request
	if a := r2.do(pUser("bob"), "GET", "/llm/quotas", nil); a.code != 403 {
		t.Errorf("GET /llm/quotas as a person: %d", a.code)
	}
	r2.must(200, pAdmin, "PUT", "/llm/quotas", map[string]any{"default": map[string]any{"tokensPerMonth": 1000}})
	q := r2.must(200, pAdmin, "GET", "/llm/quotas?user=bob", nil).json(t)
	eff := q["effective"].(map[string]any)["limits"].([]any)[0].(map[string]any)
	if eff["used"] != 240.0 || eff["value"] != 1000.0 || eff["state"] != "ok" || eff["resets"] != "2026-12-01T00:00:00Z" {
		t.Errorf("bob's limits as an admin reads them: %v", eff)
	}
	for _, bad := range []any{map[string]any{"default": map[string]any{"tokensPerDay": -7}}, map[string]any{"default": map[string]any{"tokens": 5}}, map[string]any{"defaults": nil}, "[]"} {
		if a := r2.do(pAdmin, "PUT", "/llm/quotas", bad); a.code != 400 {
			t.Errorf("PUT /llm/quotas %v: %d %s", bad, a.code, a.body)
		}
	}
}
