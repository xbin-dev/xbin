package obs

// partitionlogs.go — backend logs on partitioned tiles (plans/partitions/06
// §5; PD-24, S12). A person's partition logs to its own file,
// .xbin/partition/<TileKey>/<dep>/<pkey>/backend.log; which log a request
// reads is the broker's answer (PartitionLog), never a parameter the caller
// controls: its own partition's, at any level; a log its person shares, for
// an admin or a tile manager; the global instance's under today's rule. The
// answer names the partition it serves (X-XBin-Partition). A tile that
// isn't partitioned is answered as before.

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionHeader names the partition whose log an answer serves.
const partitionHeader = "X-XBin-Partition"

// PartitionLogFunc answers, for GET /logs on tile, which log a request by p
// reads (the broker's PartitionLog): partitioned false leaves the request
// to today's rules; otherwise rel is the partition's log, workspace-relative
// ("" for the global instance's: today's file and gate), part the partition
// it names, or status and err refuse.
type PartitionLogFunc func(p auth.Principal, tile string, q url.Values) (rel string, part util.Partition, partitioned bool, status int, err error)

// partitionLogs answers GET /logs for a partitioned tile's people's logs;
// false leaves the request to apiLogs (the tile isn't partitioned, or the
// global instance's log is asked for).
func (o *Plane) partitionLogs(w http.ResponseWriter, r *http.Request, p auth.Principal, comp string) bool {
	if o.PartitionLog == nil {
		return false
	}
	rel, part, on, status, err := o.PartitionLog(p, comp, r.URL.Query())
	switch {
	case !on:
		return false
	case err != nil:
		server.WriteError(w, status, err.Error(), "/docs/partitions.md")
		return true
	case rel == "":
		if part != "" {
			w.Header().Set(partitionHeader, string(part))
		}
		return false // the global instance's: today's file and gate
	}
	dir, file := path.Split(rel)
	if file != "backend.log" || !strings.HasPrefix(dir, ".xbin/partition/") {
		server.WriteError(w, http.StatusInternalServerError, "no such partition log")
		return true
	}
	tail := logTailDefault
	if t, err := strconv.ParseInt(r.URL.Query().Get("tail"), 10, 64); err == nil && t >= 0 {
		tail = min(t, logTailMax)
	}
	sub := strings.TrimSuffix(strings.TrimPrefix(dir, ".xbin/partition/"), "/")
	root := filepath.Join(o.Root, ".xbin", "partition")
	open := func() (*os.File, error) { return fsutil.OpenIn(root, sub, file) } // never through a symlink
	// a follow keeps asking: the same log, still this reader's (a share that
	// ends or is revoked, a person disabled, a reset, end the stream)
	q := r.URL.Query()
	still := func() bool {
		again, againPart, on, _, err := o.PartitionLog(p, comp, q)
		return on && err == nil && again == rel && againPart == part
	}
	o.streamLog(w, r, open, q.Get("follow") == "1", tail, map[string]string{partitionHeader: string(part)}, still)
	return true
}
