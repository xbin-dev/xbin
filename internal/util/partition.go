package util

// partition.go — the partition a partitioned tile's call or instance acts in
// (plans/partitions/02 §1; PD-01, PD-43). Two spellings, never mixed:
//
//   - the wire key, Partition: "global" or "user:<id>" — carried in
//     X-XBin-Partition, XBIN_PARTITION, the xbin-partition meta and API JSON;
//     a display name, never a storage key and never in a URL path;
//   - the partition id (pkey), PartitionKey: "u-" + 32 hex, a hash of the
//     person's id and their uid (users.User.UID) — the storage key under
//     data/partitions and the wire X-XBin-Partition-Id. A person deleted and
//     recreated under the same id gets a new uid, so a new pkey, and never
//     inherits the old incarnation's partitions.
//
// The empty Partition is "no partition": every principal of a tile that
// isn't partitioned, and every answer of a workspace that never used
// partitions.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Partition is a partition key: "" (none), "global" or "user:<id>".
type Partition string

// PartitionGlobal is the global partition: the tile's optional background
// instance, at today's keys (PD-04).
const PartitionGlobal Partition = "global"

const partitionUserPrefix = "user:"

// maxPartitionUserID bounds the id in a user key. Today's store creates ids
// of at most 32 bytes; the bound leaves room for older ids and matches what
// providers parse (docs/sandbox-manager.md: a printable id of at most 128).
const maxPartitionUserID = 128

// UserPartition is the key of user id's partition. It doesn't validate id:
// ParsePartition does.
func UserPartition(id string) Partition { return Partition(partitionUserPrefix + id) }

// User is the id of a user partition's person; ok is false for "", global,
// or any other key.
func (p Partition) User() (id string, ok bool) {
	id, ok = strings.CutPrefix(string(p), partitionUserPrefix)
	return id, ok && id != ""
}

// IsUser reports a user partition.
func (p Partition) IsUser() bool {
	_, ok := p.User()
	return ok
}

// ErrPartition is ParsePartition's refusal.
var ErrPartition = errors.New("not a partition key")

// ParsePartition is the strict parser of a wire key: "global", or "user:"
// plus an id a users store can hold — 1 to 128 bytes of printable ASCII
// without spaces or upper case (the store keeps ids lower-cased, so one
// person has one key). Whether that person exists is the caller's question.
// Anything else — "", an unknown kind ("org:…"), a malformed id — is an
// error: a partition xbind can't name is never a partition.
func ParsePartition(s string) (Partition, error) {
	if s == string(PartitionGlobal) {
		return PartitionGlobal, nil
	}
	id, ok := strings.CutPrefix(s, partitionUserPrefix)
	if !ok || !PartitionUserIDOK(id) {
		return "", fmt.Errorf("%q: %w (want \"global\" or \"user:<id>\")", s, ErrPartition)
	}
	return Partition(s), nil
}

// PartitionUserIDOK reports whether id can be the id of a user partition's
// key.
func PartitionUserIDOK(id string) bool {
	if id == "" || len(id) > maxPartitionUserID {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c <= ' ' || c > '~' || c >= 'A' && c <= 'Z' {
			return false
		}
	}
	return true
}

// PartitionKey is the partition id (pkey) of user id's partition for the
// person's incarnation uid: "u-" + hex(SHA-256("xbin-partition-v1" ‖ 0 ‖ id
// ‖ 0 ‖ uid)[:16]). Stable while the person exists; opaque to everyone.
func PartitionKey(userID, uid string) string {
	h := sha256.Sum256([]byte("xbin-partition-v1\x00" + userID + "\x00" + uid))
	return "u-" + hex.EncodeToString(h[:16])
}

// PartitionKeyOK reports a string PartitionKey produces: "u-" and 32 lower
// hex digits.
func PartitionKeyOK(s string) bool {
	hexPart, ok := strings.CutPrefix(s, "u-")
	if !ok || len(hexPart) != 32 {
		return false
	}
	for i := 0; i < len(hexPart); i++ {
		if c := hexPart[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
