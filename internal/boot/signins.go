package boot

import (
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/term"
)

// setupSignins starts the coding-agent sign-in store (D230,
// internal/signins, docs/agent-signins.md): each person's saved sign-ins,
// sealed under the vault barrier, put into the agent sessions xbind starts
// for them (internal/term). A workspace nobody saved a sign-in in sees
// nothing of it.
//
// It needs sandboxes: without isolation every terminal runs as xbind's own
// user and could read a coding agent's environment. A development workspace
// (--dev, --no-auth) is excepted, as it is for delegation (D228), and says
// so.
func (st *State) setupSignins(srv *server.Server) {
	brk, userStore := st.Broker, st.Users
	o := signins.Options{
		Dir:            filepath.Join(st.WS, "data", "signins"),
		AllowPlaintext: brk.AllowInsecureVault,
		Provider:       agent.Lookup,
		// a sign-in is usable while its person exists and is enabled, and is
		// theirs while the account is the one that saved it (its stamp)
		Person: func(id string) (int64, bool) {
			if userStore == nil {
				return 0, false
			}
			u, ok := userStore.Get(id)
			if !ok || u.Disabled {
				return 0, false
			}
			return u.Created, true
		},
	}
	if b := brk.Barrier(); b != nil {
		o.Vault = b
	}
	if svc := st.LLM; svc != nil && st.Cfg.LLMConfig != "" { // the host's file: "agentSignIns"
		o.Host = svc.HostAgentSignIns
		o.HostTrust = svc.HostAgentSignInTrust
	}
	store := signins.New(o)
	srv.Signins = store
	srv.SigninFlows = signins.NewFlows(store, signins.Confined{})
	st.Signins, st.signinFlows = store, srv.SigninFlows
	isolate, dev := st.Cfg.Isolate, st.Cfg.Dev || st.Cfg.NoAuth
	srv.SigninsAvail = func() server.SigninsAvailability {
		switch {
		case isolate:
			return server.SigninsAvailability{OK: true}
		case dev:
			return server.SigninsAvailability{OK: true, Unisolated: true}
		}
		return server.SigninsAvailability{Why: "saved sign-ins need sandboxes (xbind --isolate): without them every terminal runs as xbind's own user and could read a coding agent's environment — sign in on the tile, in a terminal"}
	}
	if tm := st.Term; tm != nil { // sessions take a person's sign-in from it (term/signin.go)
		tm.Signins = store
		tm.SigninDev = !isolate && dev
		// where a tile's code comes from for a person: who owns it and who
		// else can change it (broker/signinothers.go) — what decides whether
		// their trust is asked first
		tm.SigninSource = brk.SigninSource
		// …and what tells a tile from one made later at its path: the copy
		// of a person's home on a tile they have not trusted is kept per tile
		// identity (term/home.go, D245). The same source and the same trust
		// decide which home a session mounts: wired whenever sessions exist
		tm.TileIdentity = brk.TileIdentity
		srv.SigninSourcePlanned = brk.SigninSourcePlanned
		tm.SigninsOff = func() bool { return !store.Policy().Enabled }           // the admins' switch, or the host's file
		tm.SigninTrustOff = func() bool { return !store.Policy().Trust.Allowed } // …and the one on trusting a tile's source
		// A trust answer is for a tile as it stood. Whatever changes who
		// owns a tile or who can change it ends at the broker's
		// OnPeopleChanged (usersEvent: accounts, roles, access entries and
		// patterns, permission sets, orgs and their members, an org's
		// shares, owner transfers), and whatever moves, removes or re-makes
		// a tile at the watcher's rescan (signinTiles): at each, every
		// person's live answers are held to their tiles as they stand and
		// the ones whose basis changed are VOIDED — with no session running
		// too, so someone who gains write and loses it again is seen while
		// they have it — and then the sessions that ran on one are ended.
		// (The judgement at every start voids as well: the backstop.)
		revalidate := func() {
			if n := store.Revalidate(brk.SigninSource); n > 0 {
				slog.Info("saved sign-ins: trusted tiles changed and ask again", "voided", n)
			}
			tm.SigninSweep()
		}
		prevPeople := brk.OnPeopleChanged
		brk.OnPeopleChanged = func() {
			if prevPeople != nil {
				prevPeople()
			}
			revalidate()
		}
		var seen string // the registered tiles and their identities, as last judged
		var seenMu sync.Mutex
		st.signinTiles = func() {
			var b strings.Builder
			for _, c := range st.Reg.Components() {
				if !c.Sealed {
					b.WriteString(c.Path + "\x00" + brk.TileIdentity(c.Path) + "\n")
				}
			}
			seenMu.Lock()
			same := b.String() == seen
			seen = b.String()
			seenMu.Unlock()
			if !same {
				revalidate()
				// the copies of people's homes kept for a tile that is gone —
				// its directory no longer there, or another one now — go
				// (term/homecopy.go); an unregistered tile's stay
				if n := tm.SweepHomeCopies(); n > 0 {
					slog.Info("home copies: removed the layers of tiles that are gone", "removed", n)
				}
			}
		}
		st.signinTiles() // the tiles as xbind finds them at its start
	}
	// someone else made a way into a person's account (an admin set their
	// password, minted a sign-in link, bound an SSO email): their saved
	// sign-ins go, so a reset is no way to run sessions on them
	prevMade := brk.OnCredentialMade
	brk.OnCredentialMade = func(id, by string) {
		if prevMade != nil {
			prevMade(id, by)
		}
		srv.SigninFlows.DropUser(id)
		gone := store.Clear(id, "Your saved sign-ins were removed when "+by+" reset your sign-in to this workspace: whoever holds that new sign-in must not get them too. Sign in to your coding agents again.")
		if len(gone) == 0 {
			return
		}
		slog.Info("audit", "event", "signin.clear", "user", id, "by", by, "removed", len(gone))
		if st.Term != nil {
			st.Term.EndSigninSessions(id, "", "Your sign-in to this workspace was reset by "+by+", so this session — which ran on your saved sign-in — was ended.")
		}
	}
	// a person signed out everywhere, disabled or deleted: their guided
	// sign-ins end and their sessions on a saved sign-in with them; a
	// deleted account's sign-ins are deleted at once
	prevOut := brk.OnUserSignedOut
	brk.OnUserSignedOut = func(id string, deleted bool) {
		if prevOut != nil {
			prevOut(id, deleted)
		}
		srv.SigninFlows.DropUser(id)
		if st.Term != nil {
			st.Term.EndSigninSessions(id, "", "Your account was signed out of this workspace, so this session — which ran on your saved sign-in — was ended.")
		}
		if deleted {
			store.PersonGone(id)
			if st.Term != nil { // what tiles wrote to the copies of their home goes with the account (D245)
				st.Term.ForgetHomeCopies(term.HomeKey(auth.Principal{UserID: id}), "Your account was deleted, so this session was ended.")
			}
		}
	}
}
