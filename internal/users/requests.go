// Access requests (D36): the HUMAN half of the request/approve loop.
// Elements have had a pending-grant queue since D5; users had nothing — the
// "how do I get access to X" ask ran out of band, and the path of least
// resistance for admins was over-granting. A request is (user, tile, wanted
// level, note); whoever may manage the tile (its user-owner, the owning
// org's admins, or a ws-admin — the same mayManageTile set as sharing)
// approves it into an exact ACL entry or dismisses it.
package users

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// AccessRequest is one pending human request for tile access.
type AccessRequest struct {
	User    string `json:"user"`
	Tile    string `json:"tile"`
	Level   string `json:"level"` // read|write|terminal
	Note    string `json:"note,omitempty"`
	Created int64  `json:"created"`
}

// maxRequestsPerUser bounds one user's pending requests (spam/typo guard).
const maxRequestsPerUser = 20

// CreateAccessRequest files (or refreshes — same user+tile replaces) a
// request. The requester must exist; the note is clamped.
func (s *Store) CreateAccessRequest(user, tile, level, note string) error {
	user = normalizeID(user)
	tile = strings.Trim(tile, "/")
	if tile == "" {
		return fmt.Errorf("tile required")
	}
	if levelRank(level) == 0 {
		return fmt.Errorf("unknown level %q (want read|write|terminal)", level)
	}
	if len(note) > 200 {
		note = note[:200]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[user]; !ok {
		return fmt.Errorf("no such user %q", user)
	}
	mine := 0
	out := make([]AccessRequest, 0, len(s.requests)+1)
	for _, r := range s.requests {
		if r.User == user {
			if r.Tile == tile {
				continue // replaced below
			}
			mine++
		}
		out = append(out, r)
	}
	if mine >= maxRequestsPerUser {
		return fmt.Errorf("you have %d pending requests — withdraw some first", mine)
	}
	out = append(out, AccessRequest{User: user, Tile: tile, Level: level, Note: note, Created: time.Now().Unix()})
	s.requests = out
	return s.persistLocked()
}

// AccessRequests returns all pending requests, oldest first.
func (s *Store) AccessRequests() []AccessRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]AccessRequest(nil), s.requests...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created < out[j].Created
		}
		return out[i].User+out[i].Tile < out[j].User+out[j].Tile
	})
	return out
}

// DeleteAccessRequest removes one request (withdraw / dismiss / approved).
func (s *Store) DeleteAccessRequest(user, tile string) (bool, error) {
	user = normalizeID(user)
	tile = strings.Trim(tile, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.requests[:0]
	found := false
	for _, r := range s.requests {
		if r.User == user && r.Tile == tile {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return false, nil
	}
	s.requests = out
	return true, s.persistLocked()
}
