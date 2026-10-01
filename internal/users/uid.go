package users

// uid.go — a person's incarnation, for partitioned tiles (plans/partitions/03
// §E; PD-43). A user id is reusable: an admin can delete alice and create
// another alice. A partition's data is keyed by a hash of the id and the
// record's uid (util.PartitionKey), so the second alice never reaches the
// first's partitions.
//
// The uid is store-owned: never taken from an API body (Upsert keeps the
// record's), never on the wire (Public drops it). It is minted lazily, at a
// person's first partition, so a workspace that never uses partitions keeps
// today's users.json byte for byte (plans/partitions/10 §A.1). Deleting the
// user deletes it with the record. An older xbind that rewrites the store
// drops the field; the partition records carry the uid, and the caller that
// knows them passes it here to be adopted instead of minted anew.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
)

// UIDOK reports a uid EnsureUID can store: 32 lower hex digits (128 bits).
func UIDOK(uid string) bool {
	if len(uid) != 32 {
		return false
	}
	for i := 0; i < len(uid); i++ {
		if c := uid[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// EnsureUID answers id's uid, giving the record one when it has none:
// adopt when that is a uid (the partition records' of this person, which the
// caller judged theirs — PD-43's re-adoption), a fresh random one
// otherwise. The record is persisted before the uid is answered, so a uid
// is never handed out that a restart would forget.
func (s *Store) EnsureUID(id, adopt string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil {
		return "", fmt.Errorf("no such user %q", id)
	}
	if u.UID != "" {
		return u.UID, nil
	}
	uid := adopt
	if !UIDOK(uid) {
		if uid != "" {
			slog.Warn("users: not adopting a malformed uid; minting one", "user", u.ID)
		}
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		uid = hex.EncodeToString(b)
	}
	nu := *u
	nu.UID = uid
	s.byID[nu.ID] = &nu
	if err := s.persistLocked(); err != nil {
		s.byID[nu.ID] = u
		return "", err
	}
	return uid, nil
}
