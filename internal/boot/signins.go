package boot

import (
	"log/slog"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/signins"
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
		// who else could change what a session on a tile runs: the other
		// enabled people with write level or more on the tile the path lies
		// in (broker/signinothers.go). Workspace admins aren't counted.
		tm.SigninOthers = brk.SigninOthers
		// people or their access changed: a session on a saved sign-in whose
		// tile someone else can now open a terminal on is ended
		prevPeople := brk.OnPeopleChanged
		brk.OnPeopleChanged = func() {
			if prevPeople != nil {
				prevPeople()
			}
			tm.SigninSweep()
		}
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
		}
	}
}
