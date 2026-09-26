package auth

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// --- persisted frame-token generations ---
//
// Frame tokens are bound to the login that minted them (frametoken.go), and
// logins live in memory — so without this file every xbind restart would
// 401 every tile open anywhere, where before binding those tiles simply
// kept renewing. What is persisted is the generation bookkeeping, never a
// credential:
//
//   - the user-generation epoch and counters (u.<epoch>.<n>), so a
//     sign-out-everywhere stays in force across restarts and u.* tokens
//     survive them;
//   - one record per login session, keyed by its generation handle (the
//     non-secret name frame tokens carry; the session id — the credential —
//     is not written): user, device, view-as admin, created, last activity.
//
// At boot every recorded login becomes an ORPHAN: the session itself is gone
// (the browser or app signs in again, as always after a restart), but tiles
// already open keep renewing under its handle until the login would have
// expired — idle and absolute TTL as before, frame use sliding the idle
// window. Sign-out-everywhere, disabling or deleting the user, and revoking
// the device end orphans too; a logout can't name one (its cookie died with
// the restart). A missing or unreadable file starts fresh: a new epoch, no
// orphans — every bound token dies, the safe direction.
//
// Writes: synchronously whenever liveness is REMOVED (logout, device
// revocation, sign-out-everywhere, end of a view-as), so a crash can't bring
// an ended login back; new logins and activity ride along with the next
// write and the shutdown flush (FlushGens) — a crash loses only those, and
// their tiles then reload like before this file existed.

const gensFileName = "frame-gens.json"

type gensDisk struct {
	V        int                  `json:"v"`
	Epoch    string               `json:"epoch"`
	Users    map[string]uint64    `json:"users,omitempty"`
	Sessions map[string]genRecord `json:"sessions,omitempty"` // generation handle → the login behind it
}

type genRecord struct {
	User     string `json:"u"`
	Device   string `json:"d,omitempty"`
	Imp      string `json:"i,omitempty"` // the view-as admin (D64): its tiles stay read-only
	Bearer   bool   `json:"b,omitempty"`
	Created  int64  `json:"c"`
	Active   int64  `json:"a"`
	NotAfter int64  `json:"n,omitempty"`
}

// loadGens reads the persisted generations (from Load), turning every
// recorded login into an orphan and reaping the expired ones.
func (a *Auth) loadGens(path string) {
	a.gens.path = path
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("auth: frame generations unreadable — starting fresh; open tiles reload", "err", err)
		}
		a.saveGens() // pin the new epoch
		return
	}
	var d gensDisk
	if err := json.Unmarshal(b, &d); err != nil || d.V != 1 || !validGen(d.Epoch) {
		slog.Warn("auth: frame generations malformed — starting fresh; open tiles reload", "err", err)
		a.saveGens()
		return
	}
	now := time.Now()
	a.mu.Lock()
	a.gens.epoch = d.Epoch
	for u, n := range d.Users {
		a.gens.users[u] = n
	}
	for h, r := range d.Sessions {
		if !validGen(h) || r.User == "" {
			continue
		}
		s := &session{userID: r.User, deviceID: r.Device, impersonator: r.Imp, bearer: r.Bearer, gen: h,
			created: time.Unix(r.Created, 0), lastActive: time.Unix(r.Active, 0)}
		if r.NotAfter != 0 {
			s.notAfter = time.Unix(r.NotAfter, 0)
		}
		if !a.expiredLocked(s, now) {
			a.gens.orphans[h] = s
		}
	}
	a.mu.Unlock()
}

// saveGens writes the generation state (atomic, 0600). Serialized by saveMu
// so an older snapshot never overwrites a newer one; never called with a.mu
// held.
func (a *Auth) saveGens() {
	if a.gens.path == "" {
		return
	}
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	a.mu.RLock()
	d := gensDisk{V: 1, Epoch: a.gens.epoch, Users: make(map[string]uint64, len(a.gens.users)),
		Sessions: make(map[string]genRecord, len(a.sessions)+len(a.gens.orphans))}
	for u, n := range a.gens.users {
		d.Users[u] = n
	}
	now := time.Now()
	rec := func(s *session) {
		if s.gen == "" || a.expiredLocked(s, now) {
			return
		}
		r := genRecord{User: s.userID, Device: s.deviceID, Imp: s.impersonator, Bearer: s.bearer,
			Created: s.created.Unix(), Active: s.lastActive.Unix()}
		if !s.notAfter.IsZero() {
			r.NotAfter = s.notAfter.Unix()
		}
		d.Sessions[s.gen] = r
	}
	for _, s := range a.sessions {
		rec(s)
	}
	for _, s := range a.gens.orphans {
		rec(s)
	}
	a.mu.RUnlock()
	b, err := json.Marshal(d)
	if err == nil {
		err = fsutil.WriteFileAtomic(a.gens.path, b, 0o600)
	}
	if err != nil {
		slog.Warn("auth: persisting frame generations failed", "err", err)
	}
}

// FlushGens persists the generation state — new logins and recent activity
// included — at shutdown, so the tiles open now keep working after the
// restart (internal/boot/serve.go).
func (a *Auth) FlushGens() { a.saveGens() }
