//go:build !sealedfixture

package sealed

import (
	"io/fs"

	xbin "github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/registry"
)

// WorkspaceAgent is the name of the workspace agent's row: people's
// workspace agent is the sealed component xbin/wsagent (D231, D227).
const WorkspaceAgent = "wsagent"

// table is the compiled-in table.
//
// xbin/wsagent — a person's workspace agent (docs/workspace-agent.md): the
// agent template's own tree (builtin-templates/agent), embedded once and
// read here as a release reads it, started in workspace mode. What the row
// says, and why:
//
//   - one backend per person and no shared instance: a person's agent is
//     their partition, and nothing of it is anyone else's;
//   - XBIN_AGENT_MODE=workspace: the same source is a different product in
//     that mode (the template's ws_mode.go) — one profile, one fixed class,
//     a closed route list;
//   - it holds delegation and is THE agent xbin's chrome frames; xbind's
//     notices go to /delegate/notify on a person's partition of it;
//   - of the template's interface slots it has one, the models' — bound to
//     xbind's own LLM connector by default and to nothing else, ever: the
//     provider of that slot reads every person's conversation. No MCP
//     server, sandbox manager, scm provider or network slot exists for it;
//   - its page reads one thing of xbind's beside a viewer's list: the
//     connection view of its own session (the names a model must not see);
//   - cap:open-links: its transcript renders links that open in a new tab.
//
// It is inert until an admin has connected a model (the slot's default
// applies only then), turned workspace agents on (the delegation switch,
// off by default) and given people read access to it (nobody has any
// until granted: users.SealedExplicit). No backend is built or started
// before someone who may open it does.
func table() []Spec {
	src, err := fs.Sub(xbin.BuiltinTemplatesFS(), "agent")
	if err != nil {
		panic(err)
	}
	return []Spec{{
		Name: WorkspaceAgent, Title: "Workspace agent", Source: src, Authored: "apps/agent", Template: true,
		Partition: []string{"user"},
		Slots:     []string{"llm"}, Defaults: map[string]string{"llm": "xbin:llm"}, Providers: map[string][]string{"llm": {"xbin:llm"}},
		Env:          map[string]string{"XBIN_AGENT_MODE": "workspace"},
		Notify:       "/delegate/notify",
		FrameReads:   []string{"/api/xbin/agents/connection"},
		Grants:       []registry.Use{{Target: "cap:open-links", Role: "writer"}},
		Capabilities: Capabilities{Delegate: true, Agent: true},
		Data:         1,
	}}
}

// fixtureTable says this build's table is the integration fixture's
// (table_fixture.go): never, in a build without that tag.
const fixtureTable = false
