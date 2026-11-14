package boot

import (
	"fmt"
	"path/filepath"
	"time"

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
	var grace time.Duration
	if v := st.Cfg.LLMCancelGrace; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d > 10*time.Minute {
			return fmt.Errorf("bad XBIN_LLM_CANCEL_GRACE %q: a duration up to 10m, e.g. 5s", v)
		}
		grace = d
	}
	svc, err := llm.New(llm.Options{
		Grace:    grace,
		Dir:      filepath.Join(st.WS, "data", "llm"),
		HostFile: st.Cfg.LLMConfig,
		// upstream keys an admin sets are sealed under the vault barrier;
		// in the clear only in the plaintext vault mode, like every vault file
		Vault: brk.Barrier(), AllowPlaintext: brk.AllowInsecureVault,
		IsAdmin: brk.IsAdmin,
		Bound:   brk.LLMBound, // the binding is the authorization, asked per call
		Edge:    brk.LLMEdge,  // …and a non-primary deployment uses it by the tile's edge policy
		// an LLM key stands while its person exists, is enabled and can
		// still open the tile it was minted for
		KeyOwner: func(user, component string) bool {
			if userStore == nil {
				return false
			}
			u, ok := userStore.Get(user)
			if !ok || u.Disabled {
				return false
			}
			if component == "" || u.IsAdmin() {
				return true
			}
			acc, ok := userStore.Access(user)
			return ok && acc.CanReadTile(component)
		},
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
	// LLM keys end with what they were minted under: a person signed out
	// everywhere, disabled or deleted; a tile's binding to the connector
	prevOut := brk.OnUserSignedOut
	brk.OnUserSignedOut = func(id string, deleted bool) {
		if prevOut != nil {
			prevOut(id, deleted)
		}
		svc.UserGone(id)
	}
	prevGrant := brk.OnGrantChange // a bind or an unbind fires it
	brk.OnGrantChange = func(comp string) {
		if prevGrant != nil {
			prevGrant(comp)
		}
		svc.BindingChanged(comp)
	}
	prevAlerts := brk.AdminAlerts // a single func: chained, never overwritten
	brk.AdminAlerts = func() []broker.Alert {
		var out []broker.Alert
		if prevAlerts != nil {
			out = prevAlerts()
		}
		for _, a := range svc.Alerts() {
			out = append(out, broker.Alert{Level: a.Level, Kind: llm.AlertKind, Message: a.Message})
		}
		return out
	}
	return nil
}
