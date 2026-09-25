// migrate_conv.go — per-user conversations and automations (D83): who owns a
// run, who else may see it, where it came from, and the per-user state of the
// list (pinned, archived, read). Additive like the rest of migrate.go.
//
// The run columns default to what every run was before: owner ” and
// visibility 'team' with team_role 'participant' — everyone who can open the
// tile sees it and may post. An older binary sharing the file during a
// blue/green overlap writes exactly that, so nothing it creates disappears;
// the new binary always stamps runs explicitly.
package main

import (
	"regexp"
	"strconv"
	"time"
)

const convSchemaSQL = `
CREATE TABLE IF NOT EXISTS run_members (
  run_id INTEGER NOT NULL, user TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'participant',
  added_by TEXT NOT NULL DEFAULT '', via TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL, PRIMARY KEY (run_id, user));
CREATE TABLE IF NOT EXISTS share_links (
  id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL,
  token_hash TEXT NOT NULL UNIQUE, role TEXT NOT NULL DEFAULT 'participant',
  created_by TEXT NOT NULL, created INTEGER NOT NULL,
  expires INTEGER NOT NULL DEFAULT 0, max_uses INTEGER NOT NULL DEFAULT 0,
  uses INTEGER NOT NULL DEFAULT 0, revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS run_user_state (
  run_id INTEGER NOT NULL, user TEXT NOT NULL,
  pinned_at INTEGER NOT NULL DEFAULT 0, archived_at INTEGER NOT NULL DEFAULT 0,
  read_ms INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (run_id, user));
CREATE TABLE IF NOT EXISTS sessions (
  key TEXT PRIMARY KEY, origin TEXT NOT NULL, origin_id INTEGER NOT NULL DEFAULT 0,
  run_id INTEGER NOT NULL DEFAULT 0,
  owner TEXT NOT NULL DEFAULT '', visibility TEXT NOT NULL DEFAULT 'private',
  reset_policy TEXT NOT NULL DEFAULT '', resets INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL, last_in INTEGER NOT NULL DEFAULT 0,
  address TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS idx_runs_conv ON runs(parent_id, activity_ms, id);
CREATE INDEX IF NOT EXISTS idx_runs_owner ON runs(owner, parent_id);
CREATE INDEX IF NOT EXISTS idx_runs_origin ON runs(origin, origin_id, id);
CREATE INDEX IF NOT EXISTS idx_runs_session ON runs(session_key) WHERE session_key<>'';
CREATE INDEX IF NOT EXISTS idx_members_user ON run_members(user);
CREATE INDEX IF NOT EXISTS idx_rus_user ON run_user_state(user, pinned_at);
CREATE INDEX IF NOT EXISTS idx_sessions_origin ON sessions(origin, origin_id);
`

// addConvSchema adds the columns and tables. It runs before any other
// migration step, since those read runs through runCols.
func (d *DB) addConvSchema() error {
	for _, q := range []string{
		`ALTER TABLE runs ADD COLUMN owner TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN visibility TEXT NOT NULL DEFAULT 'team'`,
		`ALTER TABLE runs ADD COLUMN team_role TEXT NOT NULL DEFAULT 'participant'`,
		`ALTER TABLE runs ADD COLUMN origin TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN origin_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN session_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN title_src TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN activity_ms INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE schedules ADD COLUMN owner TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE schedules ADD COLUMN visibility TEXT NOT NULL DEFAULT 'team'`,
		`ALTER TABLE schedules ADD COLUMN mode TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE schedules ADD COLUMN target_run INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE schedules ADD COLUMN created_by_run INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE schedules ADD COLUMN last_run_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE schedules ADD COLUMN last_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE skills ADD COLUMN owner TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE skills ADD COLUMN lane TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = d.q.Exec(q)
	}
	_, err := d.q.Exec(convSchemaSQL)
	return err
}

// migrateConv classifies what nobody stamped; it runs last.
func (d *DB) migrateConv() error {
	d.backfillConversations()
	// Unread is "activity after you last looked" — but nobody has looked at
	// anything yet, so the upgrade instant is everyone's floor.
	if d.getSetting("conv_epoch_ms") == "" {
		if err := d.putSetting("conv_epoch_ms", strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
			return err
		}
	}
	return nil
}

var schedNoteRe = regexp.MustCompile(`started by schedule #(\d+)`)

// backfillConversations classifies runs nobody stamped: everything before the
// upgrade, and whatever an old binary writes during the overlap. Each step
// touches only rows still at their defaults, so it is idempotent and nearly
// free after the first boot.
func (d *DB) backfillConversations() {
	// A watcher's one persistent run.
	_, _ = d.q.Exec(`UPDATE runs SET origin='watcher', title_src='origin',
		origin_id=(SELECT s.id FROM schedules s WHERE s.run_id=runs.id AND s.watcher=1),
		session_key='watch:'||(SELECT s.id FROM schedules s WHERE s.run_id=runs.id AND s.watcher=1)
		WHERE origin='' AND parent_id=0 AND EXISTS (SELECT 1 FROM schedules s WHERE s.run_id=runs.id AND s.watcher=1)`)
	// Runs a schedule fired: the creation note names it.
	type hit struct {
		run   int64
		sched int64
	}
	var hits []hit
	if rows, err := d.q.Query(`SELECT r.id, st.detail FROM runs r JOIN steps st ON st.run_id=r.id
		WHERE r.origin='' AND r.parent_id=0 AND st.kind='note' AND st.detail LIKE '%started by schedule #%'`); err == nil {
		for rows.Next() {
			var id int64
			var detail string
			if rows.Scan(&id, &detail) == nil {
				if m := schedNoteRe.FindStringSubmatch(detail); m != nil {
					sid, _ := strconv.ParseInt(m[1], 10, 64)
					hits = append(hits, hit{id, sid})
				}
			}
		}
		rows.Close()
	}
	for _, h := range hits {
		_, _ = d.q.Exec(`UPDATE runs SET origin='schedule', origin_id=?, session_key=?, title_src='origin'
			WHERE id=? AND origin=''`, h.sched, "sched:"+strconv.FormatInt(h.sched, 10), h.run)
	}
	// The title prefixes the old engine gave them, for what the notes missed.
	_, _ = d.q.Exec(`UPDATE runs SET origin='schedule', title_src='origin' WHERE origin='' AND parent_id=0 AND title LIKE '⏱ %'`)
	_, _ = d.q.Exec(`UPDATE runs SET origin='watcher', title_src='origin' WHERE origin='' AND parent_id=0 AND title LIKE '👁 %'`)
	// Every other top-level run is a conversation; subagents follow their root.
	_, _ = d.q.Exec(`UPDATE runs SET origin='chat' WHERE origin='' AND parent_id=0`)
	_, _ = d.q.Exec(`UPDATE runs SET
		origin=COALESCE((SELECT r.origin FROM runs r WHERE r.id=runs.root_id), ''),
		origin_id=COALESCE((SELECT r.origin_id FROM runs r WHERE r.id=runs.root_id), 0)
		WHERE origin='' AND parent_id<>0`)
	// Last meaningful activity: the last thing a person or the agent said.
	_, _ = d.q.Exec(`UPDATE runs SET activity_ms = 1000*COALESCE((SELECT MAX(m.created) FROM messages m
		WHERE m.run_id=runs.id AND m.role IN ('user','assistant') AND m.content<>''), created)
		WHERE activity_ms=0 AND parent_id=0`)
	// Watchers resolve their run through a session; schedules.run_id stays the
	// authoritative copy (an old binary writes it), so the session follows it.
	_, _ = d.q.Exec(`INSERT OR IGNORE INTO sessions (key, origin, origin_id, run_id, owner, visibility, created)
		SELECT 'watch:'||id, 'watcher', id, run_id, owner, visibility, created FROM schedules WHERE watcher=1 AND run_id<>0`)
	_, _ = d.q.Exec(`UPDATE sessions SET run_id=(SELECT s.run_id FROM schedules s WHERE s.id=sessions.origin_id)
		WHERE origin='watcher' AND EXISTS (SELECT 1 FROM schedules s WHERE s.id=sessions.origin_id AND s.run_id<>0 AND s.run_id<>sessions.run_id)`)
	_, _ = d.q.Exec(`UPDATE schedules SET last_run_id=COALESCE((SELECT MAX(r.id) FROM runs r
		WHERE r.origin='schedule' AND r.origin_id=schedules.id AND r.parent_id=0), 0)
		WHERE last_run_id=0 AND watcher=0`)
}

// bumpActivity marks a conversation as having something new: a person wrote,
// the agent answered, or it is waiting for someone. On the ROOT of runID's
// tree — the list shows roots, and a subagent that needs an answer is news in
// its root's chat.
func (d *DB) bumpActivity(runID int64) {
	_, _ = d.q.Exec(`UPDATE runs SET activity_ms=?
		WHERE id=(SELECT CASE WHEN root_id<>0 THEN root_id ELSE id END FROM runs WHERE id=?)`,
		time.Now().UnixMilli(), runID)
}
