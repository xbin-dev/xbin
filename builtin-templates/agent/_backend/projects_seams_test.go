package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// A feature's /adapter/* route is mounted with adapterGuard: a bound
// provider calls it with the channel role (never admin) and gets through;
// a caller without that role doesn't.
func TestAdapterRouteTables(t *testing.T) {
	old := adapterRouteTables
	t.Cleanup(func() { adapterRouteTables = old })
	adapterRouteTables = append(adapterRouteTables, func() map[string]http.HandlerFunc {
		return map[string]http.HandlerFunc{"POST /adapter/seam-probe": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) }}
	})
	_, mux := accessFixture(t)
	adapterRoutes(mux)
	call := func(role string) int {
		r := httptest.NewRequest("POST", "/adapter/seam-probe", strings.NewReader("{}"))
		r.Header.Set("X-XBin-From", "apps/scm-github")
		r.Header.Set("X-XBin-Role", role)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if c := call("channel"); c != 299 {
		t.Fatalf("a provider with the channel role: %d", c)
	}
	if c := call("reader"); c != http.StatusForbidden {
		t.Fatalf("a caller without the channel role: %d", c)
	}
}

// schemaAdds run on the agent's own database, never on team: team's schema
// stays the run store's (teamCovers compares it across versions).
func TestFeatureSchemasNotOnTeam(t *testing.T) {
	old := schemaAdds
	t.Cleanup(func() { schemaAdds = old })
	schemaAdds = append(schemaAdds, func(d *DB) error {
		_, err := d.q.Exec(`CREATE TABLE IF NOT EXISTS seam_probe (id INTEGER PRIMARY KEY)`)
		return err
	})
	db, err := openTeamSQL(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateTeamRuns(db); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='seam_probe'`).Scan(&n)
	if n != 0 {
		t.Fatal("a feature's table was made in team")
	}
	d := newTestDB(t)
	if _, err := d.q.Exec(`INSERT INTO seam_probe (id) VALUES (1)`); err != nil {
		t.Fatalf("the agent's own database lacks the feature's table: %v", err)
	}
	// Hooks that read a feature's tables run only where they are in: the
	// agent's own database and its transactions, never team's or a
	// database migrate() is still rewriting.
	if !d.features {
		t.Fatal("the agent's own database doesn't say its feature schemas are in")
	}
	_ = d.Tx(func(tx *DB) error {
		if !tx.features {
			t.Error("a transaction lost the features flag")
		}
		return nil
	})
	if (&DB{sql: db, q: db}).features {
		t.Fatal("team's run store says it has feature schemas")
	}
}

// A token never shows: not in a log line (%v, %+v, %#v, %s), not in JSON
// of anything holding one; the provider's answer still decodes, and Reveal
// is the one way to the value.
func TestSCMSecretNeverShows(t *testing.T) {
	const tok = "ghs_" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789.-_x"
	var got scmToken
	if err := json.Unmarshal([]byte(`{"host":"github.com","username":"x-access-token","token":"`+tok+`"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Token.Reveal() != tok {
		t.Fatalf("decoded %q", got.Token.Reveal())
	}
	rv := scmRevokeReq{Token: got.Token, Purpose: "proj:k3x9qa:x"}
	b1, _ := json.Marshal(got)
	b2, _ := json.Marshal(rv)
	b3, _ := json.Marshal(map[string]any{"row": got, "req": rv})
	for _, out := range []string{fmt.Sprintf("%v", got), fmt.Sprintf("%+v", got), fmt.Sprintf("%#v", got), fmt.Sprintf("%s", got.Token),
		fmt.Sprintf("%#v", rv), fmt.Sprintf("%+v", rv), string(b1), string(b2), string(b3)} {
		if strings.Contains(out, "AbCdEf") {
			t.Errorf("the token shows: %s", out)
		}
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
		`"membersAsBot":false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("policy JSON lacks %s: %s", want, b)
		}
	}
	if k := coordSessionKey(1<<40, "alice"); k != "proj:1099511627776:coord:alice" {
		t.Errorf("coordSessionKey: %s", k)
	}
}
