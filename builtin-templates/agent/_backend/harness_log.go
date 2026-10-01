// harness_log.go — a coding agent's stderr (D147 §4.2.7): the tail
// GET /runs/{id}/harness/log serves, wherever the pipe (harness_pipe.go)
// left it — a split exec's own stream, or the baseline wrapper's log file
// in the sandbox's HOME.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
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

// handleHarnessLog is the tail of a coding agent's stderr, read as the
// caller (Sbx-User) — a person who may use its sandbox themself, checked
// first (the file lives in a HOME the sandbox's users can write, and a
// manager doesn't police an asserted person). Its current generation's.
//
//	GET /runs/{id}/harness/log?max=<bytes ≤ 65536> → text/plain
func handleHarnessLog(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	cfg, err := harnessRunOf(id)
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	limit := hpLogMax
	if v := r.URL.Query().Get("max"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			xbin.WriteError(w, http.StatusBadRequest, fmt.Sprintf("max: a number of bytes, at most %d", hpLogMax))
			return
		}
		limit = min(n, hpLogMax)
	}
	denied := func(name string) string { return "only a person who may use " + name + " can read its log" }
	c := callerOf(r)
	if sbxUserOf(c) == "" {
		name := cfg.Harness.Ref
		if b, ok := cfg.sandboxBinding(name); ok && b.Name != "" {
			name = b.Name
		} else if _, sid, ok := splitSandboxRef(name); ok {
			name = sid
		}
		xbin.WriteError(w, http.StatusForbidden, denied(name))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	conn, sid, _, err := personSandbox(ctx, c, cfg.Harness.Ref, func(b *sbxSandbox) string { return denied(sbxLabel(b)) })
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	s, err := agent.db.harnessSession(id)
	if err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s == nil || (s.ExecID == "" && s.Gen == 0) { // never started
		xbin.WriteError(w, http.StatusNotFound, errNoHarnessLog.Error())
		return
	}
	data, err := harnessLogTail(ctx, conn, sid, s.ExecID, id, s.Gen, limit)
	var se *sbxError
	switch {
	case errors.Is(err, errNoHarnessLog):
		xbin.WriteError(w, http.StatusNotFound, errNoHarnessLog.Error())
		return
	case errors.As(err, &se):
		writeSbxErr(w, err)
		return
	case err != nil:
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	// the saved sign-in it started with, and anything token-shaped, masked
	// (a hook or a tool may print the environment: harness_redact.go)
	var secrets []string
	if s.Cred != "" {
		secrets = append(secrets, credSecret(agent.db.signin(s.Cred)))
	}
	data = newRedactor(secrets...).apply(data)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
