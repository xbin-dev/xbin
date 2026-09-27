package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// File operations (plans/tile-sandbox-runtime.md §2.2): one connection each,
// opened by Hello{Kind: "file", File: &FileOp{…}}. They run inside the
// sandbox, as its root: every path resolves there, so a symlink planted by
// sandbox code can only ever lead to the sandbox's own files.
//
// Framing. Data travels in frames — a u32 big-endian length, then that many
// bytes, at most MaxFrame — and a zero-length frame ends it, so a dropped
// connection never passes for the end of the data.
//
//   - read, tar-get: the agent sends a FileResult line (the stat, or the
//     refusal — then nothing follows), the data frames, the terminator, and a
//     final FileResult line (OK, or an error that happened mid-stream);
//   - write, tar-put: the sender sends the frames and the terminator; the
//     agent answers one FileResult line. A write commits (a temporary file,
//     fsync, rename) only once the terminator has arrived. An agent that
//     refuses before then (a precondition, too-large) answers at once and
//     closes: the sender reads the answer even when its writes fail;
//   - stat, list, mkdir, remove, move: one FileResult line.
//
// Result lines are bounded by MaxResult on the reading side.

// FileOp is one file operation.
type FileOp struct {
	Op     string `json:"op"`           // stat | read | write | list | mkdir | remove | move | tar-get | tar-put
	Path   string `json:"path"`         // absolute, inside the sandbox
	To     string `json:"to,omitempty"` // move: the destination
	Offset int64  `json:"offset,omitempty"`
	Length int64  `json:"length,omitempty"` // read: 0 = to EOF
	// Mode is the permission bits of what write and mkdir create (0: 0644 /
	// 0755; a write that replaces a regular file keeps that file's mode).
	Mode uint32 `json:"mode,omitempty"`
	// Mkdirs creates write's and tar-put's missing parent directories;
	// Parents is mkdir -p; Recursive removes a directory's contents too;
	// Overwrite lets move replace an existing destination.
	Mkdirs    bool `json:"mkdirs,omitempty"`
	Parents   bool `json:"parents,omitempty"`
	Recursive bool `json:"recursive,omitempty"`
	Overwrite bool `json:"overwrite,omitempty"`
	// IfMatch makes a write conditional on the current etag; Create makes
	// it create-only (ifNoneMatch=*). Either failing is "precondition", with
	// the current entry's stat.
	IfMatch string `json:"ifMatch,omitempty"`
	Create  bool   `json:"create,omitempty"`
	// Owner chowns what the operation creates (the definition's default
	// uid/gid); nil leaves it the agent's. A write that replaces a regular
	// file keeps that file's owner.
	Owner *[2]uint32 `json:"owner,omitempty"`
	Limit int        `json:"limit,omitempty"` // list: at most this many entries (default 1000)
	// Exclude: tar-get's globs, matched against names relative to Path and
	// against each base name. A tar-get of the sandbox's root also leaves
	// out /proc, /sys and /dev, whatever Exclude says.
	Exclude []string `json:"exclude,omitempty"`
	// Max refuses past this many bytes (too-large): a read's range, a
	// write's content, a tar stream's bytes either way. 0: no bound.
	Max int64 `json:"max,omitempty"`
}

// Refusals: what a program acts on (the contract's refusal of the same
// name). A FileResult with an Error and no Refusal is a failure.
const (
	RefuseInvalid      = "invalid"
	RefuseNotFound     = "not-found"
	RefusePrecondition = "precondition"
	RefuseTooLarge     = "too-large"
)

// FileResult answers a file operation.
type FileResult struct {
	OK        bool       `json:"ok,omitempty"`
	Refusal   string     `json:"refusal,omitempty"`
	Error     string     `json:"error,omitempty"`
	Stat      *FileStat  `json:"stat,omitempty"`    // stat, read, write; precondition: the current entry
	Entries   []FileStat `json:"entries,omitempty"` // list
	Truncated bool       `json:"truncated,omitempty"`
}

// FileStat describes one entry. A list's entries carry Name and no Path or
// ETag.
type FileStat struct {
	Path    string `json:"path,omitempty"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"`             // file | dir | symlink | other
	Target  string `json:"target,omitempty"` // symlink: where it points
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"` // permission bits (with setuid/setgid/sticky)
	MTimeMs int64  `json:"mtimeMs"`
	// ETag is "<ino>-<size>-<mtime ns>" in hex: an atomic replace makes a
	// new inode, so its etag changes.
	ETag string `json:"etag,omitempty"`
}

// MaxFrame bounds one data frame.
const MaxFrame = 1 << 20

// ErrFrameTooLarge is a frame header past MaxFrame.
var ErrFrameTooLarge = errors.New("proto: frame too large")

// WriteFrame writes b as one frame (len(b) ≤ MaxFrame); an empty b is the
// terminator.
func WriteFrame(w io.Writer, b []byte) error {
	if len(b) > MaxFrame {
		return ErrFrameTooLarge
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if len(b) == 0 {
		_, err := w.Write(hdr[:])
		return err
	}
	// one write for small frames, two for large ones (no copy)
	if len(b) <= 64<<10 {
		buf := make([]byte, 4+len(b))
		copy(buf, hdr[:])
		copy(buf[4:], b)
		_, err := w.Write(buf)
		return err
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// ReadFrame reads one frame into buf (grown as needed) and returns it; the
// terminator is io.EOF, and a connection that ends anywhere else is
// io.ErrUnexpectedEOF.
func ReadFrame(r io.Reader, buf []byte) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, io.EOF
	}
	if n > MaxFrame {
		return nil, ErrFrameTooLarge
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf, nil
}

// FrameReader reads the data of a framed stream: io.EOF at the terminator,
// io.ErrUnexpectedEOF when the connection ends before it.
type FrameReader struct {
	r    io.Reader
	buf  []byte
	rest []byte
	err  error
}

// NewFrameReader reads frames from r.
func NewFrameReader(r io.Reader) *FrameReader { return &FrameReader{r: r} }

func (f *FrameReader) Read(p []byte) (int, error) {
	for len(f.rest) == 0 {
		if f.err != nil {
			return 0, f.err
		}
		b, err := ReadFrame(f.r, f.buf)
		if err != nil {
			f.err = err
			return 0, err
		}
		f.buf, f.rest = b[:0], b
	}
	n := copy(p, f.rest)
	f.rest = f.rest[n:]
	return n, nil
}

// FrameWriter frames what is written to it, one frame per MaxFrame; Close
// writes the terminator (and doesn't close the connection).
type FrameWriter struct {
	w      io.Writer
	closed bool
}

// NewFrameWriter frames writes onto w.
func NewFrameWriter(w io.Writer) *FrameWriter { return &FrameWriter{w: w} }

func (f *FrameWriter) Write(p []byte) (int, error) {
	if f.closed {
		return 0, fmt.Errorf("proto: write after the terminator")
	}
	n := 0
	for len(p) > 0 {
		k := min(len(p), MaxFrame)
		if err := WriteFrame(f.w, p[:k]); err != nil {
			return n, err
		}
		n += k
		p = p[k:]
	}
	return n, nil
}

// Close writes the terminator.
func (f *FrameWriter) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	return WriteFrame(f.w, nil)
}
