package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Config.Project is a conversation's own (projects_types.go): it survives
// a round trip through the stored JSON, and PUT /config never keeps it in
// the global defaults.
func TestConfigProjectSeam(t *testing.T) {
	cfg := defaultConfig()
	cfg.Project = &ProjectRef{ID: 1 << 40, Role: projRoleTask, N: 3}
	b, _ := json.Marshal(cfg)
	if !strings.Contains(string(b), `"project":{"id":1099511627776,"role":"task","n":3}`) {
		t.Fatalf("Config JSON: %s", b)
	}
	back := parseConfig(string(b))
	if !back.Project.isTask() || back.Project.isCoordinator() || back.Project.N != 3 {
		t.Fatalf("round trip: %+v", back.Project)
	}
	var none *ProjectRef
	if none.isTask() || none.isCoordinator() {
		t.Fatal("a nil ref is neither")
	}

	_, mux := accessFixture(t)
	w := callAs(t, mux, asMgr, "PUT", "/config", map[string]any{"project": map[string]any{"id": 7, "role": "coordinator"}})
	if w.Code != 200 {
		t.Fatalf("PUT /config: %d %s", w.Code, w.Body)
	}
	if g := parseConfig(agent.db.getSetting("config")); g.Project != nil {
		t.Fatalf("the global config kept a project: %s", jsonOf(g))
	}
}

// The registration points are wired: a route table and a schema a feature
// registers from its init() are mounted and run like the built-in ones.
func TestProjectSeamsRegistration(t *testing.T) {
	oldR, oldS := routeTables, schemaAdds
	t.Cleanup(func() { routeTables, schemaAdds = oldR, oldS })
	ran := 0
	schemaAdds = append(schemaAdds, func(d *DB) error {
		ran++
		_, err := d.q.Exec(`CREATE TABLE IF NOT EXISTS seam_probe (id INTEGER PRIMARY KEY)`)
		return err
	})
	routeTables = append(routeTables, func() []routeDef {
		return []routeDef{{"GET /seam-probe", needAny, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) }}}
	})
	_, mux := accessFixture(t)
	if ran == 0 {
		t.Fatal("migrate() did not run schemaAdds")
	}
	if _, err := agent.db.q.Exec(`INSERT INTO seam_probe (id) VALUES (1)`); err != nil {
		t.Fatalf("the registered table: %v", err)
	}
	if w := callAs(t, mux, asMgr, "GET", "/seam-probe", nil); w.Code != 299 {
		t.Fatalf("the registered route: %d %s", w.Code, w.Body)
	}
}

// The policy's JSON names are the contract (API.md §Projects): every key
// with its default.
func TestProjectPolicyDefaults(t *testing.T) {
	b, _ := json.Marshal(defaultProjectPolicy())
	for _, want := range []string{`"taskClass":"coding"`, `"engine":"auto"`, `"maxTasks":3`, `"maxOpenTasks":20`,
		`"maxTaskCreatesPerDay":50`, `"autoPR":"off"`, `"checkout":"worktree"`, `"setupTimeoutSec":600`, `"setupBlocking":true`,
		`"ports":{"base":20000,"span":10,"slots":100}`, `"fetchEveryMin":10`, `"protection":"warn"`, `"workflows":false`,
		`"ci":{"autoFix":true,"maxPerDay":5,"delaySec":60,"logBytes":8192}`, `"reviews":{"forward":"trusted","batchSec":120}`,
		`"bigTasks":{"mode":"fork","keepFork":false}`, `"cleanup":{"onMerge":true,"onClose":true}`, `"coordinator":{"web":false}`,
		`"botForPeople":false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("policy JSON lacks %s: %s", want, b)
		}
	}
	if k := coordSessionKey(1<<40, "alice"); k != "proj:1099511627776:coord:alice" {
		t.Errorf("coordSessionKey: %s", k)
	}
}
