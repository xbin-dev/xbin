package registry

// SwitchDeletesAll is what a switch between user partitions and
// unpartitioned deletes, as every surface says it (PD-44).
const SwitchDeletesAll = "all data in this tile"

// SwitchDeletes says what switching a tile's partition mode from → to
// deletes (plans/partitions/01 §2.6; owner ruling H1): everything between
// user partitions and unpartitioned; the global instance's data alone when
// "global" goes; nothing when it comes. The broker's acts, the alert, the
// push and the paused tile's page all say it with these words.
func SwitchDeletes(from, to PartitionSpec) string {
	switch {
	case from.User != to.User:
		return SwitchDeletesAll
	case from.Global && !to.Global:
		return "the global instance's data and the tile's shared resources (people's partitions stay)"
	}
	return "nothing (the global instance starts empty)"
}

// SwitchDeleting is SwitchDeletes as a clause: "deleting all its data"
// between user partitions and unpartitioned (PD-44's words), "deleting …"
// for the others.
func SwitchDeleting(from, to PartitionSpec) string {
	if d := SwitchDeletes(from, to); d != SwitchDeletesAll {
		return "deleting " + d
	}
	return "deleting all its data"
}
