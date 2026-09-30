// team_runs.go — the run schema in `team`, where a partitioned agent's
// non-secure (hosted) conversations live (API.md "Non-secure conversations";
// hosted.go, hosted_global.go).
//
//   - team carries the agent's own run schema (migrate.go's): a host's
//     partition drives a hosted conversation with the same engine as any
//     other (hosted_engine.go), and the global instance reads it for the
//     members with the same code (hosted_serve.go). The global instance
//     re-applies migrate.go to team at every start (the schema grows with
//     the agent's own), under team's migrate lock; a person's partition only
//     checks that team has every column this code needs (teamCovers) and
//     otherwise waits for global, as for team's own schema (team.go).
//   - Hosted conversations are numbered from teamIDBase (2^39): below 2^40,
//     so a person's page reaches them at the global instance (model/homes.js,
//     unchanged), and far above anything the global instance's own db
//     numbers, so global tells a hosted id from its own by the number alone.
//   - team_hosts says who hosts each one and in what state. It is a hint
//     for display and for the members' requests at global — never the
//     authority: a host's engine drives only what its own `hosted` table
//     lists (hosted.go), whatever a row here says.
package main

import (
	"database/sql"
	"sync"
)

// teamIDBase is where team numbers its runs.
const teamIDBase = int64(1) << 39

// hostedID: id names a hosted conversation (a run in team).
func hostedID(id int64) bool { return id >= teamIDBase && id < partitionIDBase }

// Hosting states (team_hosts.state, and a host's own hosted.state).
const (
	hostActive  = "active"  // the host's engine drives it
	hostPaused  = "paused"  // waiting for the host to confirm a wider audience
	hostDropped = "dropped" // the host declined, took their resources back, or didn't answer in time
	hostGone    = "gone"    // the host's partition can't be reached any more (deleted, disabled, lost read)
)

const teamHostsSQL = `CREATE TABLE IF NOT EXISTS team_hosts (
  run_id INTEGER PRIMARY KEY,
  host TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'active',
  reason TEXT NOT NULL DEFAULT '',
  resources TEXT NOT NULL DEFAULT '[]',
  pending TEXT NOT NULL DEFAULT '',
  moved_from INTEGER NOT NULL DEFAULT 0,
  since INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL DEFAULT 0
)`

// migrateTeamRuns gives team the agent's run schema and team_hosts, and
// numbers its runs from teamIDBase. The global instance's, at every start
// (migrateTeam holds the lock).
func migrateTeamRuns(db *sql.DB) error {
	d := &DB{sql: db, q: db}
	if err := d.migrate(); err != nil {
		return err
	}
	if _, err := db.Exec(teamHostsSQL); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO sqlite_sequence (name, seq) SELECT 'runs', ?
		WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='runs')`, teamIDBase-1); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE sqlite_sequence SET seq=? WHERE name='runs' AND seq < ?`, teamIDBase-1, teamIDBase-1)
	return err
}

// columnsOf lists db's tables' columns as "table.column".
func columnsOf(db *sql.DB) map[string]bool {
	out := map[string]bool{}
	rows, err := db.Query(`SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p
		WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var tbl, col string
		if rows.Scan(&tbl, &col) == nil {
			out[tbl+"."+col] = true
		}
	}
	return out
}

var (
	teamRefOnce sync.Once
	teamRefCols map[string]bool
)

// teamReference is every column the run schema of this code has (a fresh
// in-memory database migrated as global migrates team), team_hosts' too.
func teamReference() map[string]bool {
	teamRefOnce.Do(func() {
		db, err := openTeamSQL(":memory:")
		if err != nil {
			return
		}
		defer db.Close()
		if err := migrateTeamRuns(db); err != nil {
			logf("the team reference schema: %v", err)
			return
		}
		teamRefCols = columnsOf(db)
	})
	return teamRefCols
}

// teamCovers: db has every column this code's run schema needs (a global
// instance on older code hasn't migrated it yet otherwise).
func teamCovers(db *sql.DB) bool {
	ref := teamReference()
	if len(ref) == 0 {
		return false
	}
	have := columnsOf(db)
	for c := range ref {
		if !have[c] {
			return false
		}
	}
	return true
}

// teamRuns is team as the agent's run store (nil: not open, or not usable
// here yet — hosted features then answer 503, teamUnavailable).
func teamRuns() *DB {
	t := teamStore.Load()
	if !t.usable() {
		return nil
	}
	return t.runs
}

// teamHost is one team_hosts row.
type teamHost struct {
	RunID     int64    `json:"conversation"`
	Host      string   `json:"host"`
	State     string   `json:"state"`
	Reason    string   `json:"reason,omitempty"`
	Resources []string `json:"resources"`
	Pending   string   `json:"-"`
	MovedFrom int64    `json:"movedFrom,omitempty"`
	Since     int64    `json:"since"`
	Created   int64    `json:"created"`
}

func (d *DB) teamHost(root int64) (*teamHost, error) {
	h := &teamHost{}
	var res string
	err := d.q.QueryRow(`SELECT run_id, host, state, reason, resources, pending, moved_from, since, created FROM team_hosts WHERE run_id=?`, root).
		Scan(&h.RunID, &h.Host, &h.State, &h.Reason, &res, &h.Pending, &h.MovedFrom, &h.Since, &h.Created)
	if err != nil {
		return nil, err
	}
	h.Resources = parseResources(res)
	return h, nil
}

// setTeamHostState records a hosted conversation's state in team (for the
// members' view at global; the host's own table decides what runs).
func (d *DB) setTeamHostState(root int64, state, reason, pending string) error {
	_, err := d.q.Exec(`UPDATE team_hosts SET state=?, reason=?, pending=?, since=? WHERE run_id=?`, state, reason, pending, now(), root)
	return err
}
