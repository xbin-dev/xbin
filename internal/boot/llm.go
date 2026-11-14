package boot

import (
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/llm"
	"github.com/xbin-dev/xbin/internal/server"
)

// setupLLM starts the LLM connector (D226, internal/llm, docs/llm.md): its
// routes answer "not configured" until an admin adds an upstream or the
// host's file (XBIN_LLM_CONFIG) brings one, so a workspace that never sets
// it up sees nothing new. A host file that can't be read stops the boot:
// xbind doesn't run without the limits its host wrote.
func (st *State) setupLLM(srv *server.Server) error {
	// (agent sessions and terminals reach it through term.Manager.LLM, tile
	// sandboxes through the LLM-only listener serve() opens: llmsessions.go)
	brk, userStore := st.Broker, st.Users
	svc, err := llm.New(llm.Options{
		Dir:      filepath.Join(st.WS, "data", "llm"),
		HostFile: st.Cfg.LLMConfig,
		// upstream keys an admin sets are sealed under the vault barrier;
		// in the clear only in the plaintext vault mode, like every vault file
		Vault: brk.Barrier(), AllowPlaintext: brk.AllowInsecureVault,
		IsAdmin: brk.IsAdmin,
		Bound:   brk.LLMBound, // the binding is the authorization, asked per call
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
	if st.Term != nil { // agent sessions and terminals on the connector (llmsessions.go)
		st.Term.LLM = llmSessions{svc}
	}
	brk.LLMOffered = svc.Configured // pickers offer xbin:llm once an upstream exists
	return nil
}
