package agent

import (
	"github.com/xbin-dev/xbin/sdk/acp"
)

// A session's permission requests and its "allow for session" rules are the
// ACP client's (sdk/acp permissions.go): first answer wins, a rule is
// recorded only when the agent's allow_always option was chosen, and a mode
// switch (plan approval) is never scoped.
type (
	// PermissionOption is one way to answer a request (ACP: optionId + kind).
	PermissionOption = acp.PermissionOption
	// ToolCallRef is what a permission request is about (ACP ToolCallUpdate,
	// the fields worth showing): only ID is guaranteed.
	ToolCallRef = acp.ToolCallRef
	// Pending is a permission request the agent is waiting on.
	Pending = acp.Pending
	// Resolution is a settled request: the option chosen and by whom
	// ("user:<id>", "auto", "cancel").
	Resolution = acp.Resolution
	// Permissions holds a session's pending requests and its "allow for
	// session" rules.
	Permissions = acp.Permissions
)

// Permission option kinds.
const (
	AllowOnce    = acp.AllowOnce
	AllowAlways  = acp.AllowAlways
	RejectOnce   = acp.RejectOnce
	RejectAlways = acp.RejectAlways
)

// KindSwitchMode is the ACP tool kind of a mode switch — Claude's and
// Codex's plan approval, never scoped to a session rule.
const KindSwitchMode = acp.KindSwitchMode

// NewPermissions is an empty set: nothing pending, no rules.
func NewPermissions() *Permissions { return acp.NewPermissions() }
