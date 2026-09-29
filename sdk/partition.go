package xbin

// Partitioned tiles (docs/partitions.md; design: plans/partitions/07 §1). A
// tile whose xbin.json declares "partition" runs one backend instance per
// person who uses it — each with its own data, vault and registrations — and,
// when it also declares "global", one more instance that everything not
// acting for a person reaches. xbind names the partition an instance runs in
// XBIN_PARTITION, from its own state, never from anything the tile wrote.
//
// Every function here reads only what xbind injected: on an xbind that
// doesn't know partitions they answer "" (or, for RequirePartition, exit),
// and nothing that works today changes (docs/compat.md rule 8).

import (
	"fmt"
	"os"
	"strings"
)

const (
	partitionGlobal     = "global"
	partitionUserPrefix = "user:"
)

// Partition returns this backend's partition of a partitioned tile:
// "user:<id>" (one person's instance) or "global" (the tile's global
// instance, and the one instance of a non-primary deployment whose code asks
// for partitions — every writer who reaches it shares it); "" when the tile
// isn't partitioned — or when an older xbind runs it, which doesn't know
// partitions (see RequirePartition).
func Partition() string { return os.Getenv("XBIN_PARTITION") }

// PartitionUser is the person of a user partition — the id in "user:<id>" —
// and "" in a global instance or a tile that isn't partitioned.
func PartitionUser() string {
	id, ok := strings.CutPrefix(Partition(), partitionUserPrefix)
	if !ok {
		return ""
	}
	return id
}

// validPartition reports whether p is a partition key xbind hands out:
// "global" or "user:" followed by an id. Anything else — absent, empty, or
// a value this SDK doesn't know — is not a partition.
func validPartition(p string) bool {
	if p == partitionGlobal {
		return true
	}
	id, ok := strings.CutPrefix(p, partitionUserPrefix)
	return ok && id != "" && !strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r == 0x7f })
}

// RequirePartition exits the process (status 3, a line on stderr) unless
// xbind runs it as a partition. A tile whose code expects xbind to keep
// people apart calls it first thing in main: an older xbind, which ignores
// "partition" in xbin.json, then runs no backend at all instead of one shared
// one. It also fails closed on a tile whose managers kept it unpartitioned
// after its code asked for partitions (docs/partitions.md §The mode).
//
// It returns in "global" too, and the global instance is one instance for
// everyone who reaches it: other tiles calling this one, the root token,
// public requests, every person's calls to it (GlobalURL) — and every writer of a
// non-primary deployment, which runs as "global" whenever its code asks for
// partitions, even if the primary isn't partitioned. Code that must keep
// people apart serves per-person data only when PartitionUser() != "", or
// treats "global" explicitly (by Caller(r).User, say).
func RequirePartition() {
	p := Partition()
	if validPartition(p) {
		return
	}
	got := "not set"
	if p != "" {
		got = fmt.Sprintf("%q", p)
	}
	fmt.Fprintf(os.Stderr, "xbin: %s must run as a partition of a partitioned tile, and XBIN_PARTITION is %s: "+
		"this xbind doesn't run partitioned tiles, or the tile is not partitioned — refusing to serve "+
		"everyone from one instance (/docs/partitions.md)\n", Self(), got)
	os.Exit(3)
}

// GlobalURL is the gateway URL of path on this tile's global instance, for a
// user partition's calls to it:
//
//	resp, err := xbin.Client().Get(xbin.GlobalURL("runs/42"))
//	// → http://xbin/api/<self>/runs/42?xbin-partition=global
//
// xbind attributes such a call to the partition's person: global sees
// X-XBin-User (and that person's level and role), never the tile itself.
// Outside a user partition — in the global instance, in a tile that isn't
// partitioned, or under an older xbind — it is the plain URL of path on this
// tile, a self-call as today: the parameter is only ever sent from a user
// partition.
func GlobalURL(path string) string {
	u := "http://xbin/api/" + Self() + "/" + strings.TrimPrefix(path, "/")
	if PartitionUser() == "" {
		return u
	}
	u, frag, hasFrag := strings.Cut(u, "#")
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	u += sep + "xbin-partition=global"
	if hasFrag {
		u += "#" + frag
	}
	return u
}
