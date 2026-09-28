//go:build linux

package fusefs

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// traffic is what an export's connection carries, kept for the shim's dump
// (a backend that never listens): requests the guest is waiting on, with the
// guest pid that asked and for how long, and when the guest last asked
// anything at all. Serve feeds it from its pumps.
type traffic struct {
	mu       sync.Mutex
	served   uint64
	last     time.Time
	inflight map[uint64]pending // by the request's unique id
}

type pending struct {
	op   uint32
	node uint64
	pid  uint32
	at   time.Time
}

// request notes a guest → server message (a fuse_in_header: len, opcode,
// unique, nodeid, uid, gid, pid).
func (t *traffic) request(msg []byte, now time.Time) {
	if len(msg) < 40 {
		return
	}
	op := binary.LittleEndian.Uint32(msg[4:8])
	unique := binary.LittleEndian.Uint64(msg[8:16])
	t.mu.Lock()
	defer t.mu.Unlock()
	t.served++
	t.last = now
	switch op {
	case opForget, opInterrupt, opNotifyReply, opBatchForget: // no reply follows
		return
	}
	if t.inflight == nil {
		t.inflight = map[uint64]pending{}
	}
	t.inflight[unique] = pending{op: op, node: binary.LittleEndian.Uint64(msg[16:24]),
		pid: binary.LittleEndian.Uint32(msg[32:36]), at: now}
}

// reply notes a server → guest message (a fuse_out_header: len, error,
// unique; unique 0 is a notification).
func (t *traffic) reply(msg []byte) {
	if len(msg) < 16 {
		return
	}
	if unique := binary.LittleEndian.Uint64(msg[8:16]); unique != 0 {
		t.mu.Lock()
		delete(t.inflight, unique)
		t.mu.Unlock()
	}
}

// Report describes the export's traffic at now: "" when nothing was ever
// asked. names turns a node id into a path.
func (t *traffic) report(now time.Time, names func(uint64) string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.served == 0 {
		return "no requests yet"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d requests, the last %s ago", t.served, now.Sub(t.last).Round(time.Millisecond))
	if len(t.inflight) == 0 {
		b.WriteString(", none in flight")
		return b.String()
	}
	ps := make([]pending, 0, len(t.inflight))
	for _, p := range t.inflight {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].at.Before(ps[j].at) })
	fmt.Fprintf(&b, ", %d in flight:", len(ps))
	for i, p := range ps {
		if i == 16 {
			fmt.Fprintf(&b, "\n    … %d more", len(ps)-i)
			break
		}
		fmt.Fprintf(&b, "\n    %s %s for guest pid %d, %s", opName(p.op), names(p.node), p.pid, now.Sub(p.at).Round(time.Millisecond))
	}
	return b.String()
}

// oldest is how long the oldest request in flight has waited (0: none).
func (t *traffic) oldest(now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	var d time.Duration
	for _, p := range t.inflight {
		d = max(d, now.Sub(p.at))
	}
	return d
}

// FUSE opcodes (linux/fuse.h) that get no reply.
const (
	opForget      = 2
	opInterrupt   = 36
	opNotifyReply = 41
	opBatchForget = 42
)

var opNames = map[uint32]string{
	1: "LOOKUP", 2: "FORGET", 3: "GETATTR", 4: "SETATTR", 5: "READLINK", 6: "SYMLINK", 8: "MKNOD",
	9: "MKDIR", 10: "UNLINK", 11: "RMDIR", 12: "RENAME", 13: "LINK", 14: "OPEN", 15: "READ",
	16: "WRITE", 17: "STATFS", 18: "RELEASE", 20: "FSYNC", 21: "SETXATTR", 22: "GETXATTR",
	23: "LISTXATTR", 24: "REMOVEXATTR", 25: "FLUSH", 26: "INIT", 27: "OPENDIR", 28: "READDIR",
	29: "RELEASEDIR", 30: "FSYNCDIR", 31: "GETLK", 32: "SETLK", 33: "SETLKW", 34: "ACCESS",
	35: "CREATE", 36: "INTERRUPT", 37: "BMAP", 38: "DESTROY", 39: "IOCTL", 40: "POLL",
	41: "NOTIFY_REPLY", 42: "BATCH_FORGET", 43: "FALLOCATE", 44: "READDIRPLUS", 45: "RENAME2",
	46: "LSEEK", 47: "COPY_FILE_RANGE",
}

func opName(op uint32) string {
	if n, ok := opNames[op]; ok {
		return n
	}
	return fmt.Sprintf("op%d", op)
}

// Report describes what the guest asked of this export and what it is still
// waiting on (the shim's dump).
func (fs *FS) Report(now time.Time) string {
	return fs.traffic.report(now, fs.pathOf)
}

// Stalled is how long the oldest request the guest waits on has been in
// flight (0: none).
func (fs *FS) Stalled(now time.Time) time.Duration { return fs.traffic.oldest(now) }

// Root is the export's path.
func (fs *FS) Root() string { return fs.root }

// pathOf names a node for the dump.
func (fs *FS) pathOf(id uint64) string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if n := fs.nodes[id]; n != nil {
		return n.path
	}
	return fmt.Sprintf("node %d", id)
}
