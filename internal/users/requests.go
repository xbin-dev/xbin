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

// dismissCooldown is how long a manager's DISMISSAL blocks re-filing the
// same (user, tile) request — "no" should stick for a while, not until the
// requester's next click (withdrawing your own request sets no cooldown).
const dismissCooldown = 24 * time.Hour

// CreateAccessRequest files (or refreshes — same user+tile replaces) a
// request. The requester must exist; the note is clamped. Refused while an
// exact `none` entry excludes the user (the owner already said no, D31) or
// a recent dismissal is cooling down.
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
	u, ok := s.byID[user]
	if !ok {
		return fmt.Errorf("no such user %q", user)
	}
	if u.Tiles[tile] == LevelNone {
		return fmt.Errorf("you've been explicitly excluded from %s — asking again won't change that (talk to the tile's owner)", tile)
	}
	if until, ok := s.dismissed[user+"\x00"+tile]; ok && until > time.Now().Unix() {
		return fmt.Errorf("a request for %s was recently declined — try again later", tile)
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

// DeleteAccessRequest removes one request. dismissed=true is a MANAGER
// saying no — it starts the re-file cooldown; a requester's own withdrawal
// (dismissed=false) doesn't.
func (s *Store) DeleteAccessRequest(user, tile string, dismissed bool) (bool, error) {
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
	if dismissed {
		if s.dismissed == nil {
			s.dismissed = map[string]int64{}
		}
		now := time.Now().Unix()
		for k, until := range s.dismissed { // prune expired while we're here
			if until <= now {
				delete(s.dismissed, k)
			}
		}
		s.dismissed[user+"\x00"+tile] = now + int64(dismissCooldown/time.Second)
	}
	return true, s.persistLocked()
}
