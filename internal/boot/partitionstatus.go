package boot

// partitionstatus.go — the runtime reads of people's partitions
// (plans/partitions/06 §5; PD-24, S12):
//
//   - GET /tile-status from a person's partition's own credential (its
//     backend, their frames, terminals and agent sessions — the partition
//     gate stamps it) answers that partition's instance and disk, never the
//     global instance's or another person's, and says which partition
//     (partition);
//   - GET /backends (admins) nests each tile's people's partition instances
//     in its row as partitions: [{partition, state, gen, uptimeSec, rssKb,
//     restarts, crashLoop, lastExit, lastStarted, errorClass}] — metadata,
//     never content. A tile whose global instance isn't running while a
//     person's partition is gets an idle row to hold them (as its
//     deployments do); the row stays one per tile.

import (
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionReads are the sources of the partition rows; the zero value
// adds nothing (a runtime without people's partitions).
type partitionReads struct {
	inspect func() []runner.Backend                                     // runner.InspectPartitions
	meta    func(tile, part string) (map[string]any, bool)              // broker.PartitionMeta
	disk    func(tile string, part util.Partition) (usage, quota int64) // broker.PartitionDisk
}

func (st *State) partitionReads() partitionReads {
	return partitionReads{inspect: st.Run.InspectPartitions, meta: st.Broker.PartitionMeta, disk: st.Broker.PartitionDisk}
}

// status gives a person's partition's own credential its partition's row
// and disk in out (GET /tile-status of its own tile).
func (pr partitionReads) status(p auth.Principal, tile string, out map[string]any) {
	if pr.inspect == nil || !p.Partition.IsUser() || p.Component != tile {
		return
	}
	out["partition"] = string(p.Partition)
	out["backend"] = nil
	for _, b := range pr.inspect() {
		if b.Path == tile && b.Partition == string(p.Partition) {
			row := b
			out["backend"] = &row
			break
		}
	}
	if pr.disk != nil {
		usage, quota := pr.disk(tile, p.Partition)
		out["disk"] = map[string]any{"usageBytes": usage, "quotaBytes": quota, "blocked": quota > 0 && usage >= quota}
	}
}

// nest adds each tile's people's partition instances to its /backends row.
func (pr partitionReads) nest(out map[string]any) {
	if pr.inspect == nil {
		return
	}
	for _, b := range pr.inspect() {
		if b.Partition == "" {
			continue
		}
		row, ok := out[b.Path].(map[string]any)
		if !ok {
			row = map[string]any{"state": "idle", "gen": 0}
			out[b.Path] = row
		}
		p := map[string]any{"partition": b.Partition, "state": b.State, "gen": b.Gen, "uptimeSec": b.UptimeSec,
			"rssKb": b.RSSKB, "restarts": b.Restarts}
		if b.Error != "" {
			p["errorClass"] = errorClass(b.Error)
		}
		if pr.meta != nil {
			if m, ok := pr.meta(b.Path, b.Partition); ok {
				for k, v := range m {
					if k != "restarts" || v.(int) > b.Restarts {
						p[k] = v
					}
				}
			}
		}
		list, _ := row["partitions"].([]map[string]any)
		row["partitions"] = append(list, p)
	}
}

// errorClass is a runner error's class, never its text (which a person's
// code may have written): crash-loop, build, start, exit or other.
func errorClass(err string) string {
	e := strings.ToLower(err)
	switch {
	case strings.Contains(e, "crash"):
		return "crash-loop"
	case strings.Contains(e, "build"):
		return "build"
	case strings.Contains(e, "start") || strings.Contains(e, "spawn"):
		return "start"
	case strings.Contains(e, "exit"):
		return "exit"
	}
	return "other"
}
