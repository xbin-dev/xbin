// team.go — the `team` resource of a partitioned agent (sqlite, "shared":
// true in scope.json): the one database every partition and the global
// instance read and write (API.md "Partitioned instances"). It is where
// non-secure (hosted) conversations live — the agent's run schema and
// team_hosts (team_runs.go) — beside its schema version, and the tile-wide
// LLM slot locks sit beside it (llmslots.go). Anything in it is readable by
// every partition's code.
//
//   - Only the global instance migrates it, under an exclusive flock on
//     "<team>.migrate" (migrateTeam), so two global generations of a
//     blue/green swap never migrate at once.
//   - A person's partition opens it with openShared: no migration, no
//     adoption, no sweep. When the schema it finds is behind this code's, it
//     wakes the global instance (GET /health, attributed to its person) and
//     waits up to teamWait for the upgrade; until then the hosted features
//     answer 503 "the shared space is being upgraded" (teamUnavailable).
//
// An unpartitioned instance never opens it.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// teamSchema is the team schema this code needs. Each bump adds a step to
// teamMigrations; a person's partition on newer code waits for global to
// apply it.
const teamSchema = 2

// teamMigrations[i] takes the schema from i to i+1. The run schema itself
// is re-applied at every global start (migrateTeamRuns): it grows with the
// agent's own (migrate.go), which a partition checks column by column.
var teamMigrations = []string{
	`CREATE TABLE IF NOT EXISTS team_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
	teamHostsSQL, // hosted conversations (team_runs.go)
}

// teamWait bounds how long a person's partition waits for global to
// upgrade the team schema; teamRecheck how often a later use looks again.
var (
	teamWait    = 30 * time.Second
	teamRecheck = 30 * time.Second
)

// teamDB is the opened team database.
type teamDB struct {
	path  string
	sql   *sql.DB
	runs  *DB // the same database as the agent's run store (team_runs.go)
	ready atomic.Bool

	mu      sync.Mutex // one check at a time
	checked time.Time
}

// teamStore is this process's team database (nil: legacy, or not opened yet).
var teamStore atomic.Pointer[teamDB]

func openTeamSQL(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// teamVersion is the schema the file holds (0: none yet).
func teamVersion(db *sql.DB) int {
	var v string
	if err := db.QueryRow(`SELECT v FROM team_meta WHERE k='schema'`).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}

// migrateTeam opens and migrates team: the global instance's, at start.
func migrateTeam(path string) (*teamDB, error) {
	unlock, err := flockFile(path + ".migrate")
	if err != nil {
		return nil, fmt.Errorf("team migrate lock: %w", err)
	}
	defer unlock()
	db, err := openTeamSQL(path)
	if err != nil {
		return nil, err
	}
	have := teamVersion(db)
	for v := have; v < teamSchema; v++ {
		if _, err := db.Exec(teamMigrations[v]); err != nil {
			db.Close()
			return nil, fmt.Errorf("team schema %d→%d: %w", v, v+1, err)
		}
		if _, err := db.Exec(`INSERT INTO team_meta (k, v) VALUES ('schema', ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
			strconv.Itoa(v+1)); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := migrateTeamRuns(db); err != nil { // hosted conversations (team_runs.go)
		db.Close()
		return nil, fmt.Errorf("team run schema: %w", err)
	}
	t := &teamDB{path: path, sql: db, runs: &DB{sql: db, q: db}, checked: time.Now()}
	t.ready.Store(true)
	return t, nil
}

// openShared opens team in a person's partition: it never migrates. A schema
// behind this code's wakes global and waits (bounded) for the upgrade.
func openShared(ctx context.Context, path string) (*teamDB, error) {
	db, err := openTeamSQL(path)
	if err != nil {
		return nil, err
	}
	t := &teamDB{path: path, sql: db, runs: &DB{sql: db, q: db}}
	t.check(ctx, true)
	return t, nil
}

// wakeGlobal starts the global instance: any call reaching it does, and
// its start migrates team and mirrors conf.
var wakeGlobal = func(ctx context.Context) {
	if r, err := callGlobal(ctx, http.MethodGet, "/health", nil, ""); err != nil || r.Status != http.StatusOK {
		logf("waking the shared instance: %v (HTTP %d)", err, r.Status)
	}
}

// check reads the schema; behind and wait, it wakes global and polls until
// it is current or teamWait passes.
func (t *teamDB) check(ctx context.Context, wait bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.checked = time.Now()
	current := func() bool { return teamVersion(t.sql) >= teamSchema && teamCovers(t.sql) }
	if current() {
		t.ready.Store(true)
		return true
	}
	if !wait {
		return false
	}
	go wakeGlobal(ctx)
	deadline := time.Now().Add(teamWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
		if current() {
			t.ready.Store(true)
			return true
		}
	}
	logf("the team database is at schema %d, this code needs %d: hosted features answer 503 until the shared instance upgrades it",
		teamVersion(t.sql), teamSchema)
	return false
}

// usable: team is open and current — looked at again (without waiting) at
// most every teamRecheck while it isn't.
func (t *teamDB) usable() bool {
	if t == nil {
		return false
	}
	if t.ready.Load() {
		return true
	}
	t.mu.Lock()
	stale := time.Since(t.checked) > teamRecheck
	t.mu.Unlock()
	return stale && t.check(context.Background(), false)
}

// teamUnavailable answers a hosted feature's request 503 while team isn't
// usable here (true: the handler must stop).
func teamUnavailable(w http.ResponseWriter) bool {
	if teamStore.Load().usable() {
		return false
	}
	xbin.WriteError(w, http.StatusServiceUnavailable, "the shared space is being upgraded — try again in a minute")
	return true
}

// flockFile takes an exclusive flock on path (created 0600), blocking; the
// returned func releases it. A process that dies releases it with its fds.
func flockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
