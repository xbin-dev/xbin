// sessions.go — "a key names the current run" (D83): a persistent schedule
// thread (sched:<id>), a watcher (watch:<id>), later a channel's DM or thread
// (chan:…) and a trigger (trig:…). deliverInbound puts one input where it
// belongs — a new run, the session's current run, or a given conversation —
// in ONE transaction, so two inputs for the same new key never make two runs.
//
// A session never resets on its own by default (compaction bounds its
// context); an optional idle or daily policy is checked when the next input
// arrives — never by a timer.
package main

import (
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// inbound is one input for an automation's conversation.
type inbound struct {
	Mode   string // new | session | run
	Key    string // session: its key (also stamped on a new run)
	RunID  int64  // run: the conversation it goes to
	Stamp  runStamp
	Title  string
	Cfg    Config
	Reset  string // session: its reset policy ("" never | idle:<seconds> | daily:<hour>)
	Source string // inbox source: schedule | watch | channel | trigger
	Sender string
	Label  string
	Text   string
	Files  []string
	Watch  bool   // a watcher round (inboxWatch) instead of a message
	Addr   string // session: where replies go (a channel's route)
	Client string // idempotency key
	// Adopt, when set, moves files into the run once it is known (a chat
	// message's attachments, staged by the adapter); their paths join Files.
	Adopt func(t *DB, runID int64) ([]string, error)
}

// deliverInbound delivers in; it returns the run it went to and whether that
// run was created for it.
func (ag *Agent) deliverInbound(in inbound) (runID int64, created bool, err error) {
	err = ag.db.Tx(func(t *DB) error {
		runID, created, _, err = ag.deliverInboundTx(t, in)
		return err
	})
	if err == nil && ag.eng != nil {
		ag.eng.Poke(runID)
	}
	return runID, created, err
}

// deliverInboundTx is deliverInbound inside the caller's transaction (the
// caller pokes the run after it commits); it also returns the inbox row.
func (ag *Agent) deliverInboundTx(t *DB, in inbound) (runID int64, created bool, inboxID int64, err error) {
	body := inboxBody{Text: in.Text, Files: in.Files, Source: in.Source, Sender: in.Sender, OriginID: in.Stamp.OriginID, Label: in.Label}
	if in.Mode == "session" {
		body.Addr = in.Addr
	}
	kind := inboxUser
	if in.Watch {
		kind = inboxWatch
	}
	err = func() error {
		switch in.Mode {
		case "run":
			runID = in.RunID
		case "session":
			cur, ok := t.sessionRun(in.Key)
			if ok && !sessionStale(t, in.Key, time.Now()) {
				runID = cur
				break
			}
			id, err := ag.startRunTx(t, runOpts{Title: in.Title, Cfg: in.Cfg, Hold: true, Stamp: withKey(in.Stamp, in.Key)})
			if err != nil {
				return err
			}
			runID, created = id, true
			if err := t.putSession(in.Key, in.Stamp, id, in.Reset, ok); err != nil {
				return err
			}
		default: // new
			id, err := ag.startRunTx(t, runOpts{Title: in.Title, Cfg: in.Cfg, Hold: true, Stamp: withKey(in.Stamp, in.Key)})
			if err != nil {
				return err
			}
			runID, created = id, true
		}
		if in.Mode == "session" {
			_, _ = t.q.Exec(`UPDATE sessions SET last_in=?, address=CASE WHEN ?<>'' THEN ? ELSE address END WHERE key=?`,
				now(), in.Addr, in.Addr, in.Key)
		}
		if in.Adopt != nil {
			paths, err := in.Adopt(t, runID)
			if err != nil {
				return err
			}
			body.Files = append(body.Files, paths...)
		}
		var err error
		if inboxID, _, err = t.enqueue(runID, kind, body, in.Client); err != nil {
			return err
		}
		t.bumpActivity(runID)
		if ag.eng != nil {
			if r, err := t.getRun(runID); err == nil {
				ag.eng.emitInbox(t, rootOf(r), runID)
			}
		}
		return nil
	}()
	return runID, created, inboxID, err
}

func withKey(st runStamp, key string) runStamp {
	if key != "" {
		st.SessionKey = key
	}
	if st.TitleSrc == "" {
		st.TitleSrc = "origin"
	}
	return st
}

// sessionRun is the session's current run, if it still exists.
func (d *DB) sessionRun(key string) (int64, bool) {
	var id int64
	if err := d.q.QueryRow(`SELECT s.run_id FROM sessions s JOIN runs r ON r.id=s.run_id WHERE s.key=? AND s.run_id<>0`, key).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

// putSession points key at run; rotated says a previous run is replaced.
func (d *DB) putSession(key string, st runStamp, run int64, reset string, rotated bool) error {
	_, err := d.q.Exec(`INSERT INTO sessions (key, origin, origin_id, run_id, owner, visibility, reset_policy, created, last_in)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET run_id=excluded.run_id, resets=sessions.resets+?,
		  reset_policy=CASE WHEN excluded.reset_policy<>'' THEN excluded.reset_policy ELSE sessions.reset_policy END`,
		key, st.Origin, st.OriginID, run, st.Owner, orStr(st.Visibility, visPrivate), reset, now(), now(), b2i(rotated))
	return err
}

// resetSession starts the session afresh at its next input (its past runs
// stay listed, by their session key). The previous run is returned.
func (d *DB) resetSession(key string) int64 {
	var prev int64
	_ = d.q.QueryRow(`SELECT run_id FROM sessions WHERE key=?`, key).Scan(&prev)
	_, _ = d.q.Exec(`UPDATE sessions SET run_id=0, resets=resets+1 WHERE key=?`, key)
	return prev
}

// sessionStale applies the session's reset policy to the input arriving now.
func sessionStale(d *DB, key string, at time.Time) bool {
	var policy string
	var lastIn int64
	if err := d.q.QueryRow(`SELECT reset_policy, last_in FROM sessions WHERE key=?`, key).Scan(&policy, &lastIn); err != nil {
		return err != sql.ErrNoRows
	}
	return policyStale(policy, lastIn, at)
}

func policyStale(policy string, lastIn int64, at time.Time) bool {
	kind, arg, _ := strings.Cut(policy, ":")
	n, _ := strconv.ParseInt(arg, 10, 64)
	switch kind {
	case "idle":
		return n > 0 && lastIn > 0 && at.Unix()-lastIn > n
	case "daily":
		// the latest boundary (today at hh, or yesterday's when it is earlier)
		b := time.Date(at.Year(), at.Month(), at.Day(), int(n), 0, 0, 0, at.Location())
		if b.After(at) {
			b = b.AddDate(0, 0, -1)
		}
		return lastIn > 0 && lastIn < b.Unix()
	}
	return false
}
