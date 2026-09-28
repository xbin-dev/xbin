package tilesbx

// files.go — files, trees and copies (plans/tile-sandbox-runtime.md §3.8).
// Each operation is one "file" connection to the sandbox's agent, which
// resolves every path inside the sandbox (agentcore: openat2
// RESOLVE_IN_ROOT, the kernel enforcing its read-only mounts), so xbind
// never resolves one on the host (§8.3). Data moves in the agent's frames
// (proto/file.go), which xbind turns into HTTP bodies and back reading only
// their lengths; a copy between two sandboxes splices one agent's frames
// into the other's.
//
// An operation goes to a running sandbox (acquire, §3.4): a stopped one
// with autoStart is started first, one starting is waited for, one
// stopping is waited out and started again. While it is under way it holds
// the sandbox's idle timer off (§7), however long its stream runs, and its
// start and end are activity.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// fileCall is one file operation under way: the run it goes to, its
// connection to the agent, and its hold on the idle timer.
type fileCall struct {
	m    *Manager
	r    *run
	name string
	c    *proto.Conn
	end  func() // closes the connection and releases the hold (idempotent)
}

// begin opens op on k's sandbox name: the run (started when it may be), the
// hold, the connection — closed when ctx ends, so a caller hanging up
// never leaves one open.
func (m *Manager) begin(ctx context.Context, k Key, name string, op proto.FileOp) (*fileCall, error) {
	if err := helloFits(op); err != nil {
		return nil, err
	}
	r, release, err := m.acquire(k, name, waitMaxSec*time.Second) // started when it may be; the hold is the operation's
	if err != nil {
		return nil, err
	}
	a := r.client()
	if a == nil {
		release()
		return nil, &Error{Refusal: RefState, State: StateStopping, Msg: fmt.Sprintf("sandbox %q stopped", name)}
	}
	c, err := a.File(op)
	if err != nil {
		release()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	return &fileCall{m: m, r: r, name: name, c: c, end: onceFunc(func() {
		stop()
		c.Close()
		release()
	})}, nil
}

// result reads one FileResult line: a refusal is its *Error, a connection
// that ended is the sandbox stopping (or its agent failing).
func (fc *fileCall) result() (proto.FileResult, error) {
	var res proto.FileResult
	if err := fc.c.RecvMax(&res, proto.MaxResult); err != nil {
		return res, fc.broken(err)
	}
	return res, resultErr(res)
}

// broken is what a connection that ended part-way means: the sandbox
// stopping under the operation (409 state), or its agent not answering.
func (fc *fileCall) broken(err error) error {
	r := fc.r
	r.mu.Lock()
	ending := r.asked || r.closed
	r.mu.Unlock()
	select {
	case <-r.exited:
		ending = true
	default:
	}
	if ending {
		fc.m.mu.Lock()
		st := r.b.state
		fc.m.mu.Unlock()
		return &Error{Refusal: RefState, State: st, Msg: fmt.Sprintf("sandbox %q stopped during the operation", fc.name)}
	}
	return &Error{Refusal: RefUnavailable, Msg: "the sandbox's agent broke off the operation: " + err.Error(), RetryAfter: time.Second}
}

// resultErr maps the agent's answer onto the contract's refusals; an
// answer with an error and no refusal (an I/O error inside the sandbox) is
// a failure (500).
func resultErr(res proto.FileResult) error {
	if res.OK {
		return nil
	}
	msg := res.Error
	if msg == "" {
		msg = "the operation failed"
	}
	switch res.Refusal {
	case proto.RefuseInvalid:
		return &Error{Refusal: RefInvalid, Msg: msg}
	case proto.RefuseNotFound:
		return &Error{Refusal: RefNotFound, Msg: msg}
	case proto.RefusePrecondition:
		e := &Error{Refusal: RefPrecondition, Msg: msg}
		if res.Stat != nil {
			e.ETag = safeETag(res.Stat.ETag)
		}
		return e
	case proto.RefuseTooLarge:
		return &Error{Refusal: RefTooLarge, Msg: msg}
	}
	return errors.New("inside the sandbox: " + msg)
}

// oneLine runs an operation whose answer is one line (stat, list, mkdir,
// remove, move).
func (m *Manager) oneLine(ctx context.Context, k Key, name string, op proto.FileOp) (proto.FileResult, error) {
	fc, err := m.begin(ctx, k, name, op)
	if err != nil {
		return proto.FileResult{}, err
	}
	defer fc.end()
	return fc.result()
}

// answer is the agent's one line after a stream xbind sent.
type answer struct {
	res proto.FileResult
	err error
}

// await reads the agent's answer beside a stream being sent: an agent that
// refuses part-way (a precondition, too-large) answers and closes, and its
// answer then says why the stream's writes failed. answered is closed once
// it is in.
func (fc *fileCall) await() (got *answer, answered chan struct{}) {
	got, answered = &answer{}, make(chan struct{})
	go func() {
		got.res, got.err = fc.result()
		close(answered)
	}()
	return got, answered
}

// sendBody streams body to the agent in frames, then the terminator, and
// returns its answer. Past max bytes nothing more is sent and no
// terminator follows, so the agent commits nothing (413). A body that
// fails to arrive is ended the same way (400).
func (fc *fileCall) sendBody(body io.Reader, max int64, limit string) (proto.FileResult, error) {
	got, answered := fc.await()
	early := func() (proto.FileResult, error) { // an answer before the terminator: a refusal
		<-answered
		if got.err == nil {
			return got.res, refuse(RefUnavailable, "the sandbox's agent answered before the body ended")
		}
		return got.res, got.err
	}
	fw := proto.NewFrameWriter(fc.c.Writer())
	buf := make([]byte, 256<<10)
	var sent int64
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if sent += int64(n); sent > max {
				fc.c.Close()
				return proto.FileResult{}, refuse(RefTooLarge, "the body is over %d bytes (limits.%s)", max, limit)
			}
			if _, err := fw.Write(buf[:n]); err != nil {
				return early() // the agent stopped reading: its answer says why
			}
		}
		if rerr == io.EOF {
			if err := fw.Close(); err != nil {
				return early()
			}
			<-answered
			return got.res, got.err
		}
		if rerr != nil {
			fc.c.Close()
			return proto.FileResult{}, refuse(RefInvalid, "reading the body: %v", rerr)
		}
		select {
		case <-answered:
			return early()
		default:
		}
	}
}

// pipeOut copies the data frames to w up to the terminator, then reads the
// last line, which says whether they were whole.
func (fc *fileCall) pipeOut(w io.Writer) (int64, error) {
	n, err := io.CopyBuffer(w, proto.NewFrameReader(fc.c.Reader()), make([]byte, 256<<10))
	if err != nil {
		return n, err
	}
	_, err = fc.result()
	return n, err
}

// abort cuts a response whose status is already out: the client sees the
// connection end before the body did, never a clean end of a partial file
// or tree.
func abort() { panic(http.ErrAbortHandler) }

// dstError is a copy's destination failing a write (its agent stopped
// reading): its answer says why.
type dstError struct{ error }

// relayFrames copies src's data frames to dst unchanged — their lengths
// are read, their bytes never looked at — up to src's terminator, which it
// doesn't forward: the caller does once the source's last line says the
// data was whole. stop ends it early (the destination answered).
func relayFrames(dst io.Writer, src io.Reader, stop <-chan struct{}) error {
	var hdr [4]byte
	for {
		select {
		case <-stop:
			return dstError{errors.New("the destination answered before the end")}
		default:
		}
		if _, err := io.ReadFull(src, hdr[:]); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 {
			return nil
		}
		if n > proto.MaxFrame {
			return proto.ErrFrameTooLarge
		}
		if _, err := dst.Write(hdr[:]); err != nil {
			return dstError{err}
		}
		dw := &errWriter{w: dst}
		if _, err := io.CopyN(dw, src, int64(n)); err != nil {
			if dw.err != nil {
				return dstError{dw.err}
			}
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
	}
}

// errWriter remembers its writer's failure (io.CopyN's error may be the
// reader's or the writer's).
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil {
		e.err = err
	}
	return n, err
}

// The shape of what file operations answer (the contract's): a mode is
// its permission bits in octal.
type (
	fileStat struct {
		Path    string `json:"path"`
		Type    string `json:"type"`
		Size    int64  `json:"size"`
		Mode    string `json:"mode"`
		MtimeMs int64  `json:"mtimeMs"`
		ETag    string `json:"etag"`
		Target  string `json:"target,omitempty"`
	}
	fileEntry struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Size    int64  `json:"size"`
		MtimeMs int64  `json:"mtimeMs"`
		Mode    string `json:"mode"`
		Target  string `json:"target,omitempty"`
	}
	fileList struct {
		Path      string      `json:"path"`
		Entries   []fileEntry `json:"entries"`
		Truncated bool        `json:"truncated"`
	}
)

func octal(mode uint32) string { return fmt.Sprintf("%04o", mode&0o7777) }

func statOf(p string, st *proto.FileStat) fileStat {
	return fileStat{Path: p, Type: st.Type, Size: st.Size, Mode: octal(st.Mode), MtimeMs: st.MTimeMs,
		ETag: safeETag(st.ETag), Target: st.Target}
}

// etagRE is what an etag may carry: the agent's is hex and dashes, and
// nothing the agent says reaches a header unchecked (§2.6).
var etagRE = regexp.MustCompile(`^[0-9A-Za-z._-]{1,128}$`)

func safeETag(e string) string {
	if etagRE.MatchString(e) {
		return e
	}
	return ""
}

// ownerOf is who owns what an operation creates: the definition's default
// uid and gid (the agent's own, root, for one it doesn't set); nil when
// neither is set.
func ownerOf(d *Def) *[2]uint32 {
	if d.Defaults.UID == nil && d.Defaults.GID == nil {
		return nil
	}
	var o [2]uint32
	if d.Defaults.UID != nil {
		o[0] = *d.Defaults.UID
	}
	if d.Defaults.GID != nil {
		o[1] = *d.Defaults.GID
	}
	return &o
}

// sandboxPath checks a path the caller names inside a sandbox: absolute
// and clean, UTF-8 (the agent's wire is JSON, which would change other
// bytes), no NUL. It is only ever sent to the agent (§8.3).
func sandboxPath(field, p string) (string, error) {
	switch {
	case p == "":
		return "", refuse(RefInvalid, "%s is required: an absolute path inside the sandbox", field)
	case !strings.HasPrefix(p, "/"):
		return "", refuse(RefInvalid, "%s %.64q must be absolute (a path inside the sandbox)", field, p)
	case strings.IndexByte(p, 0) >= 0 || !utf8.ValidString(p):
		return "", refuse(RefInvalid, "%s must be UTF-8 with no NUL", field)
	case path.Clean(p) != p:
		return "", refuse(RefInvalid, "%s %.64q must be clean: no empty, . or .. segment and no trailing /", field, p)
	}
	return p, nil
}

// helloFits refuses an operation whose Hello, as sent, would pass the
// agent's line bound (proto.MaxHello): long paths, many or long exclude
// patterns, characters JSON escapes. The agent would drop it unanswered.
func helloFits(op proto.FileOp) error {
	b, err := json.Marshal(proto.Hello{Kind: "file", File: &op})
	if err != nil {
		return refuse(RefInvalid, "the operation: %v", err)
	}
	if len(b)+1 > proto.MaxHello {
		return refuse(RefInvalid, "the paths and patterns are too long: the operation must fit the agent's %d-byte request line (it is %d bytes)", proto.MaxHello, len(b)+1)
	}
	return nil
}

// flagParam reads a boolean query flag: "1" or "true" sets it.
func flagParam(q map[string][]string, name string) (bool, error) {
	v := ""
	if vs := q[name]; len(vs) > 0 {
		v = vs[0]
	}
	switch v {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	}
	return false, refuse(RefInvalid, "%s is 1 or 0", name)
}
