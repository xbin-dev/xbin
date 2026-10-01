package util

// partitionglobal.go — addressing a partitioned tile's global instance
// (plans/partitions/05 §6, F5): the query parameter a tile's own frames,
// terminals, agent sessions and backends send, and the refusal of a tile
// that has no global instance to address (a 404 on the wire, as an unknown
// deployment is).

import "errors"

// QueryPartition is the query parameter by which a call to a partitioned
// tile addresses its global instance: ?xbin-partition=global. xbind
// consumes it for partitioned targets only; everywhere else it reaches the
// backend as any query parameter, as before.
const QueryPartition = "xbin-partition"

// ErrNoGlobalInstance is the "no global instance" condition: a call that
// addresses a partitioned tile's global instance, which the tile doesn't
// declare. NoGlobalInstance wraps it with the tile.
var ErrNoGlobalInstance = errors.New("no global instance")

// NoGlobalInstance is the error for tile, which has no global instance:
// `<tile> has no global instance`. errors.Is matches ErrNoGlobalInstance.
func NoGlobalInstance(tile string) error { return noGlobalInstance(tile) }

type noGlobalInstance string

func (e noGlobalInstance) Error() string        { return string(e) + " has no global instance" }
func (e noGlobalInstance) Is(target error) bool { return target == ErrNoGlobalInstance }
