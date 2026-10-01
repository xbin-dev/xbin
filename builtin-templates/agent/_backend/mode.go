// mode.go — which of the agent's three modes this process runs in
// (docs/partitions.md; API.md "Partitioned instances"). The code is the same
// in all three; xbind names the partition in XBIN_PARTITION (the SDK's
// xbin.Partition()), from its own state, never from anything the tile wrote:
//
//   - legacy ("") — an unpartitioned instance: every instance made before the
//     agent was partitioned by default that keeps its mode, one made with the
//     opt-out, and any instance an older xbind runs. Exactly today's
//     behaviour: nothing in this file, conf.go, team.go, llmslots.go or
//     mailbox.go changes a thing for it.
//   - global ("global") — the global instance of a partitioned agent: what
//     no person's partition holds — shared conversations, channels, the
//     trigger registry, team automations — and the tile-wide settings, which
//     it mirrors into the `conf` resource for people's partitions to read.
//   - user ("user:<id>") — one person's partition: their private
//     conversations, memory, skills and schedules in their own copy of `db`,
//     their sandboxes; the settings read from `conf`.
package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

type agentMode int

const (
	modeLegacy agentMode = iota // XBIN_PARTITION unset: an unpartitioned instance
	modeGlobal                  // "global": a partitioned agent's global instance
	modeUser                    // "user:<id>": one person's partition
)

func (m agentMode) String() string {
	switch m {
	case modeGlobal:
		return "global"
	case modeUser:
		return "user"
	}
	return "legacy"
}

// runMode and runUser are this process's mode and, in user mode, its person.
// Set once in main before anything opens the database; tests set them with
// setMode.
var (
	runMode agentMode
	runUser string
)

// modeOf reads a partition key: "" is legacy, "global" and "user:<id>" are
// the partitioned modes. Anything else is a kind of partition this code
// doesn't know — an error, so the process refuses to serve rather than run
// one instance for everybody (the SDK's RequirePartition rule for unknown
// words).
func modeOf(p string) (agentMode, string, error) {
	switch {
	case p == "":
		return modeLegacy, "", nil
	case p == "global":
		return modeGlobal, "", nil
	case strings.HasPrefix(p, "user:"):
		id := strings.TrimPrefix(p, "user:")
		if id == "" || strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return modeLegacy, "", fmt.Errorf("XBIN_PARTITION %q names no person", p)
		}
		return modeUser, id, nil
	}
	return modeLegacy, "", fmt.Errorf("XBIN_PARTITION %q is a kind of partition this agent doesn't know", p)
}

// detectMode sets runMode from the environment.
func detectMode() error {
	m, u, err := modeOf(xbin.Partition())
	if err != nil {
		return err
	}
	runMode, runUser = m, u
	return nil
}

// partitioned: the instance is one of a partitioned agent's (global or a
// person's partition).
func partitioned() bool { return runMode != modeLegacy }

// userMode: this process is one person's partition.
func userMode() bool { return runMode == modeUser }

// globalMode: this process is a partitioned agent's global instance.
func globalMode() bool { return runMode == modeGlobal }

// partitionKey is XBIN_PARTITION as this process runs it ("" in legacy).
func partitionKey() string {
	switch runMode {
	case modeGlobal:
		return "global"
	case modeUser:
		return "user:" + runUser
	}
	return ""
}

// --- the partition id ------------------------------------------------------------
//
// A person's partition learns its own partition id (the opaque "u-…" key a
// sandbox manager homes its sandboxes under) from the calls that reach it:
// xbind sets X-XBin-Partition-Id beside X-XBin-Partition on every call acting
// in the partition. It is kept in settings, so a restart that resumes work
// before any call arrives still labels sandboxes with it.

var (
	pidMu   sync.Mutex
	pidSeen string
)

const settingPartitionID = "partition_id"

// notePartitionID keeps the partition id a call into this person's partition
// carries. Anything but user mode, a call acting in another partition (an F5
// call reaching global carries the caller's), or an id already known: nothing.
func notePartitionID(r *http.Request) {
	if !userMode() {
		return
	}
	c := xbin.Caller(r)
	if c.Partition != partitionKey() || c.PartitionID == "" {
		return
	}
	pidMu.Lock()
	known := pidSeen == c.PartitionID
	pidSeen = c.PartitionID
	pidMu.Unlock()
	if !known && agent != nil && agent.db != nil && agent.db.getSetting(settingPartitionID) != c.PartitionID {
		_ = agent.db.putSetting(settingPartitionID, c.PartitionID)
	}
}

// partitionID is this person's partition id ("" until one call told it).
func partitionID() string {
	pidMu.Lock()
	id := pidSeen
	pidMu.Unlock()
	if id == "" && agent != nil && agent.db != nil {
		id = agent.db.getSetting(settingPartitionID)
	}
	return id
}

// agentHome is the xbin.agent/home label of a sandbox made here: "global"
// for the global instance, the partition id for a person's (their partition
// key until the id is known), "" for an unpartitioned instance (no label, as
// before).
func agentHome() string {
	switch runMode {
	case modeGlobal:
		return "global"
	case modeUser:
		if id := partitionID(); id != "" {
			return id
		}
		return partitionKey()
	}
	return ""
}
