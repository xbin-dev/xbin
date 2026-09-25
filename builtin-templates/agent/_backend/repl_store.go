// repl_store.go — the durable half of the REPL: its replay log, a plain sqlite
// table (schema in db.go). The live goja VM is in repl.go and is pure cache —
// everything that has to survive a backend swap is here or in the session
// files it reads (files_store.go).
package main

import "time"

// Replay-log states. A row is written `running` before the statement executes
// and updated after, so a row left `running` across a restart identifies the
// statement that killed the process (see db.go and replMarkKilled).
const (
	replRunning = "running"
	replOK      = "ok"
	replThrew   = "threw"
	replKilled  = "killed"
)

type ReplEntry struct {
	Seq     int    `json:"seq"`
	Kind    string `json:"kind"` // eval | load
	Code    string `json:"code"` // source for eval; a file path for load
	State   string `json:"state"`
	MS      int    `json:"ms"`
	Created int64  `json:"created"`
}

// --- replay log ---------------------------------------------------------

// replAppend records a statement as `running` BEFORE it executes and returns
// its seq plus the timestamp to pin Date to. If the process dies mid-statement
// the row stays `running`, which is how the next rebuild spots (and refuses to
// replay) the killer.
//
// created is UNIX MILLISECONDS here, not seconds like the other tables: the
// caller pins the sandbox's clock to it for the statement's whole execution,
// both now and on every replay, and second granularity would let Date.now()
// come back different after a rebuild.
func (d *DB) replAppend(runID int64, kind, code string) (seq int, created int64, err error) {
	created = time.Now().UnixMilli()
	// seq allocated inside the INSERT: parallel tool calls on one run must
	// never draw the same one.
	err = d.q.QueryRow(
		`INSERT INTO repl_log (run_id, seq, kind, code, state, ms, created)
		 SELECT ?1, COALESCE(MAX(seq), -1) + 1, ?2, ?3, ?4, 0, ?5 FROM repl_log WHERE run_id=?1 RETURNING seq`,
		runID, kind, code, replRunning, created).Scan(&seq)
	return seq, created, err
}

func (d *DB) replFinish(runID int64, seq int, state string, ms int) {
	_, _ = d.q.Exec(`UPDATE repl_log SET state=?, ms=? WHERE run_id=? AND seq=?`, state, ms, runID, seq)
}

// replDrop removes a statement from the log entirely — used when a statement
// was interrupted (timeout / memory / cancel), since its partial effects are
// not reproducible and replaying it would only burn the budget again.
func (d *DB) replDrop(runID int64, seq int) {
	_, _ = d.q.Exec(`DELETE FROM repl_log WHERE run_id=? AND seq=?`, runID, seq)
}

// replMarkKilled converts every still-`running` row into `killed`. Called once
// per session build: those rows can only be statements that took the process
// down with them. Returns how many it found.
func (d *DB) replMarkKilled(runID int64) int {
	res, err := d.q.Exec(`UPDATE repl_log SET state=? WHERE run_id=? AND state=?`, replKilled, runID, replRunning)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

func (d *DB) replKilledCount(runID int64) int {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM repl_log WHERE run_id=? AND state=?`, runID, replKilled).Scan(&n)
	return n
}

// replayable returns the entries a rebuild should re-execute, oldest first:
// `ok` and `threw` only. A `threw` statement is included deliberately — it
// reproduces the same partial mutation, so the rebuilt scope matches what the
// model last saw.
func (d *DB) replayable(runID int64) ([]*ReplEntry, error) {
	rows, err := d.q.Query(
		`SELECT seq, kind, code, state, ms, created FROM repl_log
		 WHERE run_id=? AND state IN (?, ?) ORDER BY seq`, runID, replOK, replThrew)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReplEntry
	for rows.Next() {
		e := &ReplEntry{}
		if err := rows.Scan(&e.Seq, &e.Kind, &e.Code, &e.State, &e.MS, &e.Created); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// replPrune keeps the log inside its budget. `load` rows are preserved: they
// hold only a path, they are cheap to replay, and they are what makes files
// the stable substrate — so pruning costs ad-hoc scratch work, never the
// definitions the model deliberately wrote to a file. Returns rows dropped.
func (d *DB) replPrune(runID int64, maxEntries, maxBytes int) int {
	rows, err := d.q.Query(
		`SELECT seq, length(code) FROM repl_log WHERE run_id=? AND kind='eval' AND state IN (?, ?) ORDER BY seq DESC`,
		runID, replOK, replThrew)
	if err != nil {
		return 0
	}
	type row struct {
		seq, n int
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.seq, &r.n); err == nil {
			all = append(all, r)
		}
	}
	rows.Close()

	total, keep := 0, len(all)
	for i, r := range all { // newest first: find where the budget runs out
		total += r.n
		if i+1 > maxEntries || total > maxBytes {
			keep = i
			break
		}
	}
	dropped := 0
	for _, r := range all[keep:] {
		if _, err := d.q.Exec(`DELETE FROM repl_log WHERE run_id=? AND seq=?`, runID, r.seq); err == nil {
			dropped++
		}
	}
	return dropped
}

func (d *DB) replClearLog(runID int64) error {
	_, err := d.q.Exec(`DELETE FROM repl_log WHERE run_id=?`, runID)
	return err
}
