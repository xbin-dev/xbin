// partition_start.go — what a partitioned agent sets up at start, by mode
// (mode.go; API.md "Partitioned instances"). Legacy mode sets up nothing.
//
//   - Both: the `team` directory's LLM slots (llmslots.go), and the mailbox
//     is pulled once (mailbox.go).
//   - Global: team is migrated (team.go) and the tile-wide settings are
//     mirrored into conf (conf.go).
//   - A person's partition: its db numbers conversations from 2^40
//     (seedPartitionIDs, in openDB), settings are read from conf (read once
//     here, before the engine starts; until conf answers the brake is on —
//     brake.go), and team is opened without migrating it.
package main

import (
	"context"
	"path/filepath"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// partitionIDBase is where a person's partition starts numbering its runs:
// ids from different homes never collide — in sandbox labels, links or
// addresses — and a conversation's id says its home (below 2^40: the global
// instance's db, or an unpartitioned instance's). Safe in JS numbers (2^53).
const partitionIDBase = int64(1) << 40

// seedPartitionIDs raises the runs table's AUTOINCREMENT counter to just
// under partitionIDBase (a fresh partition db has none yet). Idempotent: a
// counter already past it is left alone.
func (d *DB) seedPartitionIDs() error {
	if _, err := d.q.Exec(`INSERT INTO sqlite_sequence (name, seq) SELECT 'runs', ?
		WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='runs')`, partitionIDBase-1); err != nil {
		return err
	}
	_, err := d.q.Exec(`UPDATE sqlite_sequence SET seq=? WHERE name='runs' AND seq < ?`, partitionIDBase-1, partitionIDBase-1)
	return err
}

// startMode wires the mode's plumbing once the db is open.
func startMode(db *DB) {
	if !partitioned() {
		return
	}
	if err := db.addHandoffSchema(); err != nil { // channels and triggers through partition mail (handoff.go)
		logf("handoff tables: %v", err)
	}
	if err := db.addMoveSchema(); err != nil { // un-shared conversations move to their owner's partition (homes_move.go)
		logf("move tables: %v", err)
	}
	limit := func() int { return parseConfig(db.getSetting("config")).maxActiveRuns() }
	conf, team := xbin.Resource("conf"), xbin.Resource("team")
	if conf == "" || team == "" {
		logf("partitioned (%s) without the conf and team resources in xbin.json's uses: update the agent from its template — "+
			"meanwhile people's partitions do no model work (conf) and model calls have no tile-wide cap (team)", partitionKey())
	}
	switch runMode {
	case modeGlobal:
		if team != "" {
			if t, err := migrateTeam(team); err != nil {
				logf("team: %v", err)
			} else {
				teamStore.Store(t)
			}
		}
		if conf != "" {
			confOut = newConfMirror(gatewayKV{res: conf}, db)
			go confOut.kick()
		}
	case modeUser:
		var kv kvStore = emptyKV{}
		if conf != "" {
			kv = gatewayKV{res: conf}
		}
		confIn = newConfReader(kv, func() { wakeGlobal(context.Background()) })
		confIn.parked = db.brakeParked
		confIn.onHaltOff = func() { // brake.go: the runs parked on the brake move again
			if agent != nil && agent.eng != nil {
				agent.eng.recover()
			}
		}
		confIn.refresh() // before the engine starts: its first passes know the settings
		if team != "" {
			go func() {
				t, err := openShared(context.Background(), team)
				if err != nil {
					logf("team: %v", err)
					return
				}
				teamStore.Store(t)
			}()
		}
	}
	if team != "" {
		slots = newLLMSlots(filepath.Dir(team), limit)
	}
}

// emptyKV is a kv resource that holds nothing (conf not granted).
type emptyKV struct{}

func (emptyKV) Get(context.Context, string) ([]byte, bool, error) { return nil, false, nil }
func (emptyKV) Put(context.Context, string, []byte) error         { return errSharedSetting }
func (emptyKV) Delete(context.Context, string) error              { return nil }
func (emptyKV) List(context.Context, string) ([]string, error)    { return nil, nil }
