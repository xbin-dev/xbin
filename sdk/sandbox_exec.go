package xbin

// sandbox_exec.go — commands in a tile sandbox: a blocking Run, background
// execs, their output read by byte offset, stdin, signals and resizes. The
// shapes are the sandbox-manager contract's (docs/sandbox-manager.md
// §Running commands), plus uid, gid and forUser.

import (
	"context"
	"encoding/base64"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// RunRequest is a command run to its end (POST …/run): Cmd (run by the
// sandbox's shell, `<shell> -lc`) or Argv. Cwd defaults to the sandbox's
// defaults.cwd and must exist. TimeoutMs (default 60000) and MaxOutput are
// clamped to the runtime's limits.
type RunRequest struct {
	Cmd       string            `json:"cmd,omitempty"`
	Argv      []string          `json:"argv,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Stdin     string            `json:"stdin,omitempty"`
	TimeoutMs int64             `json:"timeoutMs,omitempty"`
	MaxOutput int64             `json:"maxOutput,omitempty"` // per stream
	Merge     bool              `json:"merge,omitempty"`     // one Output, stdout and stderr together
	UID       *int              `json:"uid,omitempty"`
	GID       *int              `json:"gid,omitempty"`
	ForUser   string            `json:"forUser,omitempty"` // a claim: the person it runs for
}

// RunResult is a run's end. ExitCode is nil when a signal ended it (Signal
// names it); TimedOut says the timeout sent it.
type RunResult struct {
	ExitCode *int       `json:"exitCode"`
	Signal   string     `json:"signal,omitempty"`
	TimedOut bool       `json:"timedOut"`
	Ms       int64      `json:"ms"`
	Stdout   *RunOutput `json:"stdout,omitempty"`
	Stderr   *RunOutput `json:"stderr,omitempty"`
	Output   *RunOutput `json:"output,omitempty"` // with Merge
}

// RunOutput is one stream of a run: all of it in Head when it fit, else its
// first quarter in Head and its last three quarters in Tail, Elided bytes
// between. Bytes is the stream's whole length. Text is UTF-8, invalid bytes
// replaced.
type RunOutput struct {
	Head   string `json:"head"`
	Tail   string `json:"tail,omitempty"`
	Elided int64  `json:"elided,omitempty"`
	Bytes  int64  `json:"bytes"`
}

// Run runs a command and waits for its end. Cancelling ctx hangs up, and
// the runtime kills the command's process group.
func (b *Sandbox) Run(ctx context.Context, r RunRequest) (*RunResult, error) {
	path, err := b.route("run")
	if err != nil {
		return nil, err
	}
	var res RunResult
	if err := b.s.call(ctx, http.MethodPost, path, nil, r, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ExecRequest starts a background exec (POST …/execs). A non-tty exec's
// stdout and stderr are one stream — unless Split, which keeps stderr its
// own (Output with Stream "stderr", and the stdio socket: DialStdio); a tty
// exec's is its terminal. Stdin true keeps its stdin open for Stdin (and
// the stdio socket). TimeoutMs 0 is none. Split needs the runtime's stdio
// capability (SandboxRuntime.Caps): an xbind without it ignores the field,
// and the exec answers Split false.
type ExecRequest struct {
	Cmd       string            `json:"cmd,omitempty"`
	Argv      []string          `json:"argv,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TTY       bool              `json:"tty,omitempty"`
	Rows      int               `json:"rows,omitempty"`
	Cols      int               `json:"cols,omitempty"`
	Stdin     bool              `json:"stdin,omitempty"`
	Split     bool              `json:"split,omitempty"` // stderr apart from stdout (not with TTY)
	TimeoutMs int64             `json:"timeoutMs,omitempty"`
	Label     string            `json:"label,omitempty"`
	ClientID  string            `json:"clientId,omitempty"` // per sandbox: a repeat answers the same exec
	UID       *int              `json:"uid,omitempty"`
	GID       *int              `json:"gid,omitempty"`
	ForUser   string            `json:"forUser,omitempty"` // a claim: the person (a tty exec is refused for one with noTerminal)
}

// ExecInfo is an exec: State is running, exited or killed (a signal, its
// timeout, a stop or a Kill ended it; ExitCode nil when a signal did). An
// exec of an earlier xbind start isn't answered at all: its calls are
// ErrSandboxLost. Total is the bytes its output stream has had — a Split
// exec's stdout, ErrTotal its stderr's.
type ExecInfo struct {
	ID       string   `json:"id"`
	Label    string   `json:"label,omitempty"`
	Cmd      string   `json:"cmd,omitempty"`
	Argv     []string `json:"argv,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	TTY      bool     `json:"tty"`
	State    string   `json:"state"`
	ExitCode *int     `json:"exitCode"`
	Signal   string   `json:"signal,omitempty"`
	Started  int64    `json:"started"` // unix ms
	Ended    int64    `json:"ended,omitempty"`
	Total    int64    `json:"total"`
	Split    bool     `json:"split,omitempty"`
	ErrTotal int64    `json:"errTotal,omitempty"`
	ClientID string   `json:"clientId,omitempty"`
	ForUser  string   `json:"forUser,omitempty"`
	UID      *int     `json:"uid,omitempty"`
}

// Exec starts a background exec. The command's output is read with Output
// or Follow (or, a non-tty one's, over the stdio socket: RelayStdio,
// DialStdio); a tty exec is attached with RelayTTY or DialTTY.
func (b *Sandbox) Exec(ctx context.Context, r ExecRequest) (*ExecInfo, error) {
	path, err := b.route("execs")
	if err != nil {
		return nil, err
	}
	var e ExecInfo
	if err := b.s.call(ctx, http.MethodPost, path, nil, r, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// Execs lists the sandbox's execs: the running ones and those that ended
// recently.
func (b *Sandbox) Execs(ctx context.Context) ([]ExecInfo, error) {
	path, err := b.route("execs")
	if err != nil {
		return nil, err
	}
	var out struct {
		Execs []ExecInfo `json:"execs"`
	}
	if err := b.s.call(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Execs, nil
}

// GetExec is one exec. One from before xbind restarted is ErrSandboxLost;
// one that was running when its sandbox stopped is killed (signal KILL),
// its output kept.
func (b *Sandbox) GetExec(ctx context.Context, id string) (*ExecInfo, error) {
	path, err := b.execRoute(id, "")
	if err != nil {
		return nil, err
	}
	var e ExecInfo
	if err := b.s.call(ctx, http.MethodGet, path, nil, nil, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// Kill kills an exec's process group and forgets it (DELETE the exec).
func (b *Sandbox) Kill(ctx context.Context, id string) error {
	path, err := b.execRoute(id, "")
	if err != nil {
		return err
	}
	return b.s.call(ctx, http.MethodDelete, path, nil, nil, nil)
}

// OutputQuery reads an exec's output from byte offset Since, at most Max
// bytes, waiting up to WaitMs for more while it runs. Encoding is "text"
// (the default: UTF-8, invalid bytes replaced) or "base64" (exact). Stream
// "stderr" reads a Split exec's stderr (its own offsets); "" or "stdout" is
// the exec's output stream. The runtime clamps Max and WaitMs to its
// limits.
type OutputQuery struct {
	Since    int64
	Max      int64
	WaitMs   int64
	Encoding string
	Stream   string
}

func (q OutputQuery) values() url.Values {
	v := url.Values{}
	if q.Since > 0 {
		v.Set("since", strconv.FormatInt(q.Since, 10))
	}
	if q.Max > 0 {
		v.Set("max", strconv.FormatInt(q.Max, 10))
	}
	if q.WaitMs > 0 {
		v.Set("waitMs", strconv.FormatInt(q.WaitMs, 10))
	}
	if q.Encoding != "" {
		v.Set("encoding", q.Encoding)
	}
	if q.Stream != "" {
		v.Set("stream", q.Stream)
	}
	return v
}

// OutputChunk is a read of an exec's output: the bytes from Start to End
// of its stream (Start past the offset asked for means the ring dropped the
// gap), Total the stream's length so far, RingStart the oldest byte kept,
// and the exec's State, ExitCode and Signal.
type OutputChunk struct {
	Start     int64  `json:"start"`
	End       int64  `json:"end"`
	Total     int64  `json:"total"`
	RingStart int64  `json:"ringStart"`
	Data      string `json:"data"`
	Encoding  string `json:"encoding"`
	State     string `json:"state"`
	ExitCode  *int   `json:"exitCode"`
	Signal    string `json:"signal,omitempty"`
}

// Bytes is the chunk's data, decoded when it came as base64.
func (c OutputChunk) Bytes() ([]byte, error) {
	if c.Encoding == "base64" {
		return base64.StdEncoding.DecodeString(c.Data)
	}
	return []byte(c.Data), nil
}

// Output reads a chunk of an exec's output.
func (b *Sandbox) Output(ctx context.Context, id string, q OutputQuery) (*OutputChunk, error) {
	path, err := b.execRoute(id, "output")
	if err != nil {
		return nil, err
	}
	var c OutputChunk
	if err := b.s.call(ctx, http.MethodGet, path, q.values(), nil, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// followWaitMs is how long each of Follow's reads waits for more output
// (the runtime clamps it to its own limit).
const followWaitMs = 30000

// Follow reads an exec's output from byte offset since to its end: each
// chunk that carries bytes, then the last one (the exec ended, and nothing
// is left past it). Chunks come base64-encoded — exact bytes, a multi-byte
// character split between two reads intact — so read them with Bytes. A
// chunk whose Start is past the previous End (or past since) means the ring
// dropped that gap. An error ends the sequence: ErrSandboxLost when the exec
// is gone, or ctx's.
//
//	for c, err := range sb.Follow(ctx, id, 0) {
//		if err != nil { return err }
//		data, _ := c.Bytes()
//		os.Stdout.Write(data)
//	}
func (b *Sandbox) Follow(ctx context.Context, id string, since int64) iter.Seq2[OutputChunk, error] {
	return func(yield func(OutputChunk, error) bool) {
		for {
			t0 := time.Now()
			c, err := b.Output(ctx, id, OutputQuery{Since: since, WaitMs: followWaitMs, Encoding: "base64"})
			if err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				yield(OutputChunk{}, err)
				return
			}
			done := c.State != "running" && c.End >= c.Total
			if (c.End > c.Start || c.Start > since || done) && !yield(*c, nil) {
				return
			}
			if done {
				return
			}
			if c.End > since {
				since = c.End
			} else if time.Since(t0) < time.Second {
				// nothing new, answered at once (a runtime that doesn't
				// long-poll, or an offset past the end): don't spin
				select {
				case <-ctx.Done():
					yield(OutputChunk{}, ctx.Err())
					return
				case <-time.After(250 * time.Millisecond):
				}
			}
		}
	}
}

// Stdin sends r to a stdin: true (or tty) exec's stdin, streamed; eof closes
// its stdin after. r may be nil (to close it only).
func (b *Sandbox) Stdin(ctx context.Context, id string, r io.Reader, eof bool) error {
	path, err := b.execRoute(id, "stdin")
	if err != nil {
		return err
	}
	var q url.Values
	if eof {
		q = url.Values{"eof": {"1"}}
	}
	if r == nil {
		r = http.NoBody
	}
	resp, err := b.s.open(ctx, http.MethodPost, path, q, r, "application/octet-stream")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Signal sends sig (INT, TERM, KILL or HUP) to the exec — its process group
// when group is true, as the contract's default is.
func (b *Sandbox) Signal(ctx context.Context, id, sig string, group bool) error {
	path, err := b.execRoute(id, "signal")
	if err != nil {
		return err
	}
	body := struct {
		Signal string `json:"signal"`
		Group  bool   `json:"group"`
	}{sig, group}
	return b.s.call(ctx, http.MethodPost, path, nil, body, nil)
}

// Resize sets a tty exec's terminal size.
func (b *Sandbox) Resize(ctx context.Context, id string, rows, cols int) error {
	path, err := b.execRoute(id, "resize")
	if err != nil {
		return err
	}
	body := struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}{rows, cols}
	return b.s.call(ctx, http.MethodPost, path, nil, body, nil)
}
