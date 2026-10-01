package main

// logs_partition.go — which log `bx logs` reads on a partitioned tile
// (docs/partitions.md §Logs and status). Without a flag xbind answers the
// caller's own partition's log (a person's session) or, for the root token,
// the global instance's. --global asks for the global instance's
// (?xbin-partition=global, under today's log rule); --user <id> for a
// person's partition's log, which an admin or tile manager reads only while
// that person shares it (?user=<id>). Either one goes to xbind, never to the
// host's log file.

import (
	"net/url"
	"strings"
)

// takePartitionLogFlags takes --global and --user <id> (or --user=<id>) out
// of bx logs' arguments, as the query they ask for.
func takePartitionLogFlags(args []string) (rest []string, q url.Values, err error) {
	q = url.Values{}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--global":
			q.Set("xbin-partition", "global")
		case a == "--user":
			v, e := nextArg(args, &i)
			if e != nil {
				return nil, nil, usageError("logs", "%v", e)
			}
			q.Set("user", v)
		case strings.HasPrefix(a, "--user="):
			q.Set("user", strings.TrimPrefix(a, "--user="))
		default:
			rest = append(rest, a)
		}
	}
	if q.Has("xbin-partition") && q.Has("user") {
		return nil, nil, usageError("logs", "--global and --user name different logs: pick one")
	}
	return rest, q, nil
}
