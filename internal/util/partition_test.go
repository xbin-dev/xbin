package util

import (
	"errors"
	"strings"
	"testing"
)

// covers PD-01 — the wire key's strict grammar: "global" or "user:" plus an
// id a users store can hold; everything else, an unknown kind included, is
// no partition (fail closed).
func TestParsePartition(t *testing.T) {
	for _, s := range []string{"global", "user:alice", "user:a", "user:dev.1_x-y", "user:" + strings.Repeat("a", 128), "user:old@id"} {
		p, err := ParsePartition(s)
		if err != nil || string(p) != s {
			t.Errorf("ParsePartition(%q) = %q, %v", s, p, err)
		}
	}
	for _, s := range []string{"", "Global", "user:", "user", "org:x", "user:Alice", "user:a b", "user:a\tb", "user:\x00",
		"user:é", "user:" + strings.Repeat("a", 129), " user:a", "user:a\n"} {
		if p, err := ParsePartition(s); err == nil || !errors.Is(err, ErrPartition) {
			t.Errorf("ParsePartition(%q) = %q, %v: want ErrPartition", s, p, err)
		}
	}
	if id, ok := UserPartition("bob").User(); !ok || id != "bob" {
		t.Errorf("UserPartition(bob).User() = %q, %v", id, ok)
	}
	for _, p := range []Partition{"", PartitionGlobal, "user:", "org:x"} {
		if p.IsUser() {
			t.Errorf("%q.IsUser()", p)
		}
	}
}

// covers PD-43 — the partition id: stable per (id, uid), different for a
// recreated person (a new uid) and between ids, never the raw id, and of a
// shape PartitionKeyOK accepts.
func TestPartitionKey(t *testing.T) {
	a := PartitionKey("alice", "0123456789abcdef0123456789abcdef")
	if a != PartitionKey("alice", "0123456789abcdef0123456789abcdef") || !PartitionKeyOK(a) || strings.Contains(a, "alice") {
		t.Errorf("PartitionKey = %q", a)
	}
	for _, other := range []string{PartitionKey("alice", "ffffffffffffffffffffffffffffffff"), PartitionKey("bob", "0123456789abcdef0123456789abcdef"),
		PartitionKey("alic", "e0123456789abcdef0123456789abcdef")} {
		if other == a {
			t.Errorf("two people share the partition id %q", a)
		}
	}
	for _, s := range []string{"", "u-", "u-0123", "x-" + a[2:], strings.ToUpper(a), a + "0"} {
		if PartitionKeyOK(s) {
			t.Errorf("PartitionKeyOK(%q)", s)
		}
	}
}
