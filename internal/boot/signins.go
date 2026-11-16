package boot

import (
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
