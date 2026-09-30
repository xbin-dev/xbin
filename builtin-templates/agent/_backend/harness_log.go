// harness_log.go — a coding agent's stderr (D-harness §4.2.7): the tail
// GET /runs/{id}/harness/log serves, wherever the pipe (harness_pipe.go)
// left it — a split exec's own stream, or the baseline wrapper's log file
// in the sandbox's HOME.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
)

// harnessTail prints the last $1 bytes of the log $2 the wrapper wrote,
// found the way the wrapper found it; exit 3: there is none.
const harnessTail = `f="${HOME:-/tmp}/.cache/xbin-harness/$2.log"; [ -f "$f" ] || exit 3; exec tail -c "$1" "$f"`

const hpLogMax = 64 << 10

var errNoHarnessLog = errors.New("no log yet")

// harnessLogTail is the tail, at most limit bytes (≤ 64 KiB), of generation
// gen's adapter stderr in run's harness (§4.2.7), read as conn's person: a
// split exec's own stream, else the log file the wrapper wrote — through
// the contract's run (`tail -c`), where the wrapper wrote it whatever the
// manager reports as home. errNoHarnessLog when there is none yet.
func harnessLogTail(ctx context.Context, conn *sbxConn, sandboxID, execID string, run int64, gen, limit int) ([]byte, error) {
	if limit <= 0 || limit > hpLogMax {
		limit = hpLogMax
	}
	if execID != "" {
		ex, err := conn.ExecGet(ctx, sandboxID, execID)
		switch {
		case err == nil && ex.Split:
			ch, err := conn.execStderr(ctx, sandboxID, execID, max(0, ex.ErrTotal-int64(limit)), limit)
			if err != nil {
				return nil, err
			}
			data, derr := base64.StdEncoding.DecodeString(ch.Data)
			if derr != nil || ch.Encoding == "text" {
				data = []byte(ch.Data)
			}
			return data, nil
		case err != nil && !gone(err):
			return nil, err
		}
	}
	res, err := conn.Run(ctx, sandboxID, sbxRunReq{Argv: []string{"sh", "-c", harnessTail, "h", strconv.Itoa(limit), harnessLogName(run, gen)},
		TimeoutMs: 10000, MaxOutput: limit + 4096})
	if err != nil {
		return nil, err
	}
	switch {
	case res.ExitCode != nil && *res.ExitCode == 3:
		return nil, errNoHarnessLog
	case res.ExitCode == nil || *res.ExitCode != 0:
		why := "tail failed"
		if res.Stderr != nil && res.Stderr.Head != "" {
			why = res.Stderr.Head
		}
		return nil, fmt.Errorf("reading the coding agent's log: %s", clip(why, 300))
	}
	if res.Stdout == nil {
		return []byte{}, nil
	}
	return []byte(res.Stdout.Head + res.Stdout.Tail), nil
}

// execStderr reads a split exec's stderr from since (…/output?stream=stderr).
func (c *sbxConn) execStderr(ctx context.Context, id, eid string, since int64, limit int) (*sbxChunk, error) {
	var out sbxChunk
	q := map[string]string{"since": strconv.FormatInt(since, 10), "max": strconv.Itoa(limit), "encoding": "base64", "stream": "stderr"}
	return &out, c.call(ctx, "GET", sbxPath(id, "execs", eid, "output"), q, nil, &out, sbxCallTimeout)
}
