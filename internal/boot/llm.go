package boot

import (
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/llm"
	"github.com/xbin-dev/xbin/internal/server"
)

// setupLLM starts the LLM connector (D226, internal/llm, docs/llm.md): its
// routes answer "not configured" until an admin adds an upstream or the
// host's file (XBIN_LLM_CONFIG) brings one, so a workspace that never sets
// it up sees nothing new. A host file that can't be read stops the boot:
// xbind doesn't run without the limits its host wrote.
func (st *State) setupLLM(srv *server.Server) error {
	brk, userStore := st.Broker, st.Users
	svc, err := llm.New(llm.Options{
		Dir:      filepath.Join(st.WS, "data", "llm"),
		HostFile: st.Cfg.LLMConfig,
		// upstream keys an admin sets are sealed under the vault barrier;
		// in the clear only in the plaintext vault mode, like every vault file
		Vault: brk.Barrier(), AllowPlaintext: brk.AllowInsecureVault,
		IsAdmin: brk.IsAdmin, AdminShell: brk.AdminShell,
		Bound: brk.LLMBound, // the binding is the authorization, asked per call
		// a person's orgs, for the orgs' quota overrides: active memberships only
		OrgsOf: func(user string) []string {
			if userStore == nil {
				return nil
			}
			var out []string
			for _, o := range userStore.Orgs() {
				if m, ok := o.Member(user); ok && !m.Suspended {
					out = append(out, o.ID)
				}
			}
			return out
		},
	})
	if err != nil {
		return err
	}
	st.LLM = svc
	svc.Register(srv)
	brk.LLMOffered = svc.Configured // pickers offer xbin:llm once an upstream exists
	// its notices for admins — a quota reached, the setup card (llm/alerts.go)
	// — reach an admin's own session and the admin console (AdminAlerts) and
	// the shell an admin drives (AdminShellAlerts); each a single func:
	// chained after the earlier sources, never overwritten
	chain := func(prev func() []broker.Alert) func() []broker.Alert {
		return func() []broker.Alert {
			var out []broker.Alert
			if prev != nil {
				out = prev()
			}
			for _, a := range svc.Alerts() {
				out = append(out, broker.Alert{Level: a.Level, Kind: a.Kind, Message: a.Message, Dismiss: a.Dismiss})
			}
			return out
		}
	}
	brk.AdminAlerts, brk.AdminShellAlerts = chain(brk.AdminAlerts), chain(brk.AdminShellAlerts)
	return nil
}
