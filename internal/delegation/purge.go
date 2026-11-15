package delegation

// purge.go — a person's delegation state ends with their account.
//
// Everything here is kept by user id: approval requests and tickets,
// standing approvals, agent sessions (handles), change sets, the state
// (pause, the admin opt-in) and the audit log. An account deleted and one
// made later under the same id are two people: the second must inherit
// none of it — not a standing approval, not an approved ticket, not a
// handle, not the first one's log. So the directory is taken out of use
// when the account goes (Purge), and it records which account it belongs
// to (Claim: the account's creation stamp), so a deletion xbind didn't see
// — the users file edited while it was down — is caught too.
//
// Taken out of use, not destroyed: the directory moves to deleted/, where
// no id reaches it, and stays for the audit log's own retention
// (KeepMonths) — what an agent did for a person is still there for
// whoever must look, after the person is gone.

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

const (
	deletedDir  = "deleted" // not hex: never a person's directory (userOfDir)
	accountFile = "account" // the creation stamp of the account the directory belongs to
)

// Purge takes everything the store keeps about user out of use. Nothing
// kept before is read again under that id.
func (s *Store) Purge(user string) error {
	if s == nil || user == "" {
		return nil
	}
	// every lock a file of theirs is written under, in the one order
	p := s.lockSets(user)
	defer p.setsMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	u := s.Audit.lock(user)
	defer u.mu.Unlock()
	return s.purgeLocked(p, u, user)
}

func (s *Store) purgeLocked(p *person, u *logUser, user string) error {
	p.sessUsed, p.sets = nil, map[string]*cachedSet{}
	p.state.loaded, p.reqs.loaded, p.aps.loaded, p.sess.loaded = false, false, false, false
	u.month = ""
	dir := userDir(s.dir, user)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	gone := filepath.Join(s.dir, deletedDir)
	if err := os.MkdirAll(gone, 0o700); err != nil {
		return err
	}
	now := s.now()
	oldest := now.AddDate(0, -KeepMonths, 0)
	if ents, err := os.ReadDir(gone); err == nil { // what was kept for the log's retention has had it
		for _, e := range ents {
			if _, at, ok := strings.Cut(e.Name(), "."); ok {
				if ns, err := strconv.ParseInt(at, 10, 64); err == nil && time.Unix(0, ns).Before(oldest) {
					_ = os.RemoveAll(filepath.Join(gone, e.Name()))
				}
			}
		}
	}
	// a name of its own: two purges of one id in the same instant (a clock
	// that stands still) must not meet
	to := filepath.Join(gone, hex.EncodeToString([]byte(user))+"."+strconv.FormatInt(now.UnixNano(), 10))
	for i := 1; ; i++ {
		if _, err := os.Lstat(to); errors.Is(err, fs.ErrNotExist) {
			break
		}
		to = filepath.Join(gone, hex.EncodeToString([]byte(user))+"."+strconv.FormatInt(now.UnixNano()+int64(i), 10))
	}
	return os.Rename(dir, to)
}

// Claim says the account that holds user's id now was created at created
// (users.User.Created). What the store keeps under that id for ANOTHER
// account — one deleted since, the id reused — is purged (purged true);
// what it keeps with no stamp yet is this account's from now on. Nothing
// kept: nothing written.
func (s *Store) Claim(user string, created int64) (purged bool, err error) {
	if s == nil || user == "" {
		return false, nil
	}
	p := s.lockSets(user)
	defer p.setsMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := userDir(s.dir, user)
	if _, err := os.Lstat(dir); err != nil {
		return false, nil // nothing kept (or unreadable: every read of it fails closed anyway)
	}
	want := strconv.FormatInt(created, 10)
	b, err := os.ReadFile(filepath.Join(dir, accountFile))
	switch {
	case err == nil && strings.TrimSpace(string(b)) == want:
		return false, nil
	case err == nil: // another account's
		u := s.Audit.lock(user)
		defer u.mu.Unlock()
		return true, s.purgeLocked(p, u, user)
	case errors.Is(err, fs.ErrNotExist):
		return false, fsutil.WriteFileAtomic(filepath.Join(dir, accountFile), []byte(want+"\n"), 0o600)
	}
	return false, err
}
