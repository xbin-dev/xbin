package tilesbx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type policyAnswer struct {
	Policy Policy
	Stored Policy
}

func (e *testEnv) putPolicy(body string, status int) policyAnswer {
	e.t.Helper()
	w := e.do(admin, "PUT", "/sandboxes/policy", body)
	e.want(w, status, "")
	var a policyAnswer
	if status == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
			e.t.Fatal(err)
		}
	}
	return a
}

// A PUT merges onto the stored policy: absent fields stay, zero means the
// default, an override replaces that tile's, null removes it.
func TestPolicyMerge(t *testing.T) {
	e := newEnv(t)
	w := e.do(admin, "GET", "/sandboxes/policy", nil)
	var a policyAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if !a.Policy.On() || a.Policy.PerTile.Max != 8 || a.Policy.PerSandbox.Pids != 4096 || a.Policy.OutputBudgetMiB != 64 ||
		a.Stored.PerTile.Max != 0 || a.Stored.Enabled != nil {
		t.Fatalf("defaults %+v", a)
	}

	a = e.putPolicy(`{"perTile":{"max":2}}`, http.StatusOK)
	if a.Stored.PerTile.Max != 2 || a.Stored.PerTile.Running != 0 || a.Policy.PerTile.Running != 4 {
		t.Fatalf("partial %+v", a)
	}
	a = e.putPolicy(`{"idleStopMin":60,"perTile":{"running":1},"unknownField":true}`, http.StatusOK)
	if a.Stored.PerTile.Max != 2 || a.Stored.PerTile.Running != 1 || a.Stored.IdleStopMin != 60 {
		t.Fatalf("merged %+v", a.Stored)
	}
	a = e.putPolicy(`{"enabled":false,"overrides":{"apps/mgr":{"perTile":{"max":32}},"apps/x":{"idleStopMin":5}}}`, http.StatusOK)
	if a.Policy.On() || len(a.Stored.Overrides) != 2 || a.Stored.For("apps/mgr").PerTile.Max != 32 ||
		a.Stored.For("apps/mgr").PerTile.Running != 1 || a.Stored.For("apps/other").PerTile.Max != 2 {
		t.Fatalf("overrides %+v", a.Stored)
	}
	a = e.putPolicy(`{"enabled":null,"overrides":{"apps/x":null,"apps/mgr":{"perSandbox":{"vcpus":4}}}}`, http.StatusOK)
	if !a.Policy.On() || len(a.Stored.Overrides) != 1 || a.Stored.For("apps/mgr").PerTile.Max != 2 || a.Stored.For("apps/mgr").PerSandbox.VCPUs != 4 {
		t.Fatalf("override replaced %+v", a.Stored)
	}

	// Persisted, 0600; a restart reads it back.
	st, err := os.Stat(e.m.policyPath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("policy file %v %v", st, err)
	}
	m2 := New(Options{Root: e.m.root, Deps: testDeps()})
	if p := m2.Policy(); p.PerTile.Max != 2 || p.IdleStopMin != 60 {
		t.Fatalf("reloaded %+v", p)
	}

	// Invalid: out of range, a default over its cap, a bad override key, bad JSON.
	for _, bad := range []string{
		`{"idleStopMin":1441}`, `{"outputRingMiB":9}`, `{"perTile":{"max":-1}}`, `{"perSandbox":{"memMiB":100}}`,
		`{"perSandbox":{"memMiB":16384}}`, `{"perSandbox":{"vcpus":9}}`, `{"perSandbox":{"maxDiskGiB":10}}`,
		`{"outputBudgetMiB":1,"outputRingMiB":2}`, `{"perSandbox":{"pids":10}}`,
		`{"overrides":{"/abs":{}}}`, `{"overrides":{"../x":{}}}`, `{"overrides":{"apps/x":{"perSandbox":{"vcpus":99}}}}`,
		`{"perTile":"x"}`,
	} {
		w := e.do(admin, "PUT", "/sandboxes/policy", bad)
		e.want(w, http.StatusBadRequest, RefInvalid)
	}
	if p := e.m.Policy(); p.PerTile.Max != 2 || p.IdleStopMin != 60 {
		t.Fatalf("a refused PUT changed the policy: %+v", p)
	}
}

func TestPolicyFor(t *testing.T) {
	p := Policy{Limits: Limits{PerTile: PerTile{Max: 3}, IdleStopMin: 10},
		Overrides: map[string]*Limits{"apps/a": {PerTile: PerTile{Running: 9}}}}
	if l := p.For("apps/a"); l.PerTile.Max != 3 || l.PerTile.Running != 9 || l.IdleStopMin != 10 || l.PerSandbox.MemMiB != 2048 {
		t.Fatalf("For(apps/a) %+v", l)
	}
	if l := p.For("apps/b"); l.PerTile.Running != 4 {
		t.Fatalf("For(apps/b) %+v", l)
	}
	if err := (Policy{}).Validate(); err != nil {
		t.Fatalf("the zero policy: %v", err)
	}
}

// total caps every tile sandbox together: validated, pids defaulted, merged
// like the rest, and never per tile. The runtime tells a manager its
// sandboxes' flow caps.
func TestPolicyTotalAndFlows(t *testing.T) {
	e := newEnv(t)
	a := e.putPolicy(`{"idleStopMin":45}`, http.StatusOK)
	if a.Policy.Total.Pids != 32768 || a.Policy.Total.MemMiB != 0 || a.Stored.Total != (Total{}) {
		t.Fatalf("total's defaults %+v / %+v", a.Policy.Total, a.Stored.Total)
	}
	e.putPolicy(`{"total":{"memMiB":65536}}`, http.StatusOK)
	a = e.putPolicy(`{"total":{"pids":100000}}`, http.StatusOK)
	if a.Stored.Total != (Total{MemMiB: 65536, Pids: 100000}) || a.Policy.Total.Pids != 100000 {
		t.Fatalf("total merged %+v", a.Stored.Total)
	}
	// An override can't carry one: the field is ignored (lenient), never applied.
	a = e.putPolicy(`{"overrides":{"apps/mgr":{"total":{"pids":64},"idleStopMin":5}}}`, http.StatusOK)
	if b, _ := json.Marshal(a.Stored.Overrides["apps/mgr"]); contains(string(b), "total") || a.Stored.Total.Pids != 100000 {
		t.Fatalf("an override's total: %s, total %+v", b, a.Stored.Total)
	}
	for _, bad := range []string{`{"total":{"pids":10}}`, `{"total":{"pids":-1}}`, `{"total":{"pids":8388608}}`,
		`{"total":{"memMiB":100}}`, `{"total":{"memMiB":-1}}`, `{"total":{"memMiB":33554432}}`} {
		e.want(e.do(admin, "PUT", "/sandboxes/policy", bad), http.StatusBadRequest, RefInvalid)
	}
	w := e.do(mgr, "GET", "/sandboxes/runtime", nil)
	var rt Runtime
	if err := json.Unmarshal(w.Body.Bytes(), &rt); err != nil || rt.Limits.Flows != (Flows{TCP: 1024, UDP: 256}) ||
		!contains(w.Body.String(), `"flows":{"tcp":1024,"udp":256}`) {
		t.Fatalf("runtime limits: %s", w.Body)
	}
}

// An unreadable policy file fails closed (§3.2): it says so in GET
// /sandboxes/policy (error, and enabled false), the runtime says enabled
// false, every start answers 503; a PUT replaces it and clears the error.
func TestPolicyError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".xbin", "sandboxes", "policy.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// it said enabled, and more: none of it counts while it can't be read
	if err := os.WriteFile(path, []byte(`{"enabled": true, "idleStopMin": 5`), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, func(o *Options) { o.Root = root })
	e.create(ns("sb-1"))
	var v struct {
		Error  string
		Policy Policy
	}
	w := e.do(admin, "GET", "/sandboxes/policy", nil)
	e.want(w, http.StatusOK, "")
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || !contains(v.Error, "policy.json") || v.Policy.On() {
		t.Fatalf("an unreadable policy file: %s", w.Body)
	}
	if e.m.PolicyError() == "" {
		t.Fatal("no policy error for the health view")
	}
	var rt Runtime
	w = e.do(mgr, "GET", "/sandboxes/runtime", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &rt); err != nil || rt.Enabled {
		t.Fatalf("the runtime with an unreadable policy file: %s", w.Body)
	}
	w = e.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
	e.want(w, http.StatusServiceUnavailable, RefUnavailable)
	if !contains(w.Body.String(), "policy file is unreadable") {
		t.Fatalf("the refusal: %s", w.Body)
	}

	a := e.putPolicy(`{"idleStopMin":45}`, http.StatusOK)
	if !a.Policy.On() || a.Stored.IdleStopMin != 45 {
		t.Fatalf("the PUT's policy: %+v", a)
	}
	w = e.do(admin, "GET", "/sandboxes/policy", nil)
	if contains(w.Body.String(), `"error"`) || e.m.PolicyError() != "" {
		t.Fatalf("the error outlived a PUT: %s", w.Body)
	}
	w = e.do(mgr, "GET", "/sandboxes/runtime", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &rt); err != nil || !rt.Enabled {
		t.Fatalf("the runtime after the PUT: %s", w.Body)
	}
}
