// Package agentpolicy is what orgs and the workspace say about agents
// (D229): whether the workspace agent may work on an org's tiles, which
// coding-agent harnesses may run there, and which models may see their
// data. It is pure — no store, no HTTP, no clock: the users store keeps
// the rules (users.Org.Agents, the workspace's outer bounds), the broker
// gathers the layers a tile falls under and asks Check, and Connection
// turns the same answers into the list a person reads ("14 tiles · 3 orgs
// connected · 6 not connected").
//
// The rule of evaluation: a call is filtered by the TILE's orgs — the org
// that owns it and every org it is shared into — plus the workspace's
// bounds. Every layer must allow it, so a tile under several orgs gets the
// strictest of them and a tile in no org follows the workspace bounds
// alone. Nothing is set by default: with no rules anywhere, every answer
// is yes.
package agentpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Switch is the workspace agent's on/off rule. The zero value is on.
type Switch string

const (
	On  Switch = "on"
	Off Switch = "off"
)

// IsOff reports an explicit off.
func (s Switch) IsOff() bool { return s == Off }

// MarshalJSON writes "on" for the zero value: a reader never sees "".
func (s Switch) MarshalJSON() ([]byte, error) {
	if s.IsOff() {
		return []byte(`"off"`), nil
	}
	return []byte(`"on"`), nil
}

// UnmarshalJSON takes "on", "off" or null (on).
func (s *Switch) UnmarshalJSON(b []byte) error {
	var v *string
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf(`workspaceAgent: "on" or "off"`)
	}
	switch {
	case v == nil, *v == string(On), *v == "":
		*s = ""
	case *v == string(Off):
		*s = Off
	default:
		return fmt.Errorf(`workspaceAgent: "on" or "off", not %q`, *v)
	}
	return nil
}

// Allow is one allow rule: any, private (models only: a model of an
// upstream the LLM connector marks private), or an allowlist of ids. The
// zero value is any. On the wire: "any" | "private" | ["id", …] — an empty
// list allows nothing.
type Allow struct {
	private bool
	listed  bool
	list    []string
}

// Any allows everything.
func Any() Allow { return Allow{} }

// Private allows private models only.
func Private() Allow { return Allow{private: true} }

// Only allows the listed ids (none, for an empty list).
func Only(ids ...string) Allow { return Allow{listed: true, list: normList(ids)} }

func normList(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// IsAny reports the rule that narrows nothing.
func (a Allow) IsAny() bool { return !a.private && !a.listed }

// IsPrivate reports the private-models rule.
func (a Allow) IsPrivate() bool { return a.private }

// List is the allowlist (a copy) and whether the rule is one.
func (a Allow) List() ([]string, bool) { return slices.Clone(a.list), a.listed }

// String is the rule as a person reads it: any, private, or a, b, c.
func (a Allow) String() string {
	switch {
	case a.private:
		return "private"
	case a.listed && len(a.list) == 0:
		return "none"
	case a.listed:
		return strings.Join(a.list, ", ")
	}
	return "any"
}

func (a Allow) MarshalJSON() ([]byte, error) {
	switch {
	case a.private:
		return []byte(`"private"`), nil
	case a.listed:
		if a.list == nil {
			return []byte(`[]`), nil
		}
		return json.Marshal(a.list)
	}
	return []byte(`"any"`), nil
}

func (a *Allow) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*a = Allow{}
		return nil
	}
	if len(b) > 0 && b[0] == '[' {
		var ids []string
		if err := json.Unmarshal(b, &ids); err != nil {
			return fmt.Errorf(`a list of ids, like ["claude", "codex"]`)
		}
		*a = Only(ids...)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf(`"any", "private" or a list of ids`)
	}
	switch s {
	case "any", "":
		*a = Allow{}
	case "private":
		*a = Private()
	default:
		return fmt.Errorf(`"any", "private" or a list of ids, not %q`, s)
	}
	return nil
}

// Rules is one layer's constraints on agents: an org's (users.Org.Agents)
// or the workspace's outer bounds. The zero Rules narrows nothing.
type Rules struct {
	// WorkspaceAgent: off keeps every person's workspace agent away from
	// the layer's tiles.
	WorkspaceAgent Switch `json:"workspaceAgent"`
	// Harnesses are the coding-agent harnesses (sdk/acp provider ids) an
	// agent session on the layer's tiles may run.
	Harnesses Allow `json:"harnesses"`
	// Models are the models that may see the layer's tiles' data: any,
	// private, or LLM connector ids — an upstream ("local") for all of its
	// models, or "upstream/model" for one.
	Models Allow `json:"models"`
	// Web and MCP say what a workspace agent's conversation that holds the
	// layer's data may do on the web and with MCP servers (netrules.go;
	// D239). Unset (omitted): no rule of the layer's own.
	Web WebRule `json:"web,omitempty"`
	MCP MCPRule `json:"mcp,omitempty"`
}

// IsZero reports rules that narrow nothing (stored as no rules at all).
func (r Rules) IsZero() bool {
	return !r.WorkspaceAgent.IsOff() && r.Harnesses.IsAny() && r.Models.IsAny() && r.Web == "" && r.MCP == ""
}

const (
	maxEntries = 64
	maxIDLen   = 200
)

// Validate checks the rules' shape; knownHarness, when given, names the
// harness ids this xbind can run.
func (r Rules) Validate(knownHarness func(id string) bool) error {
	if r.Harnesses.IsPrivate() {
		return fmt.Errorf(`harnesses: "any" or a list of harness ids ("private" is a rule for models)`)
	}
	if ids, ok := r.Harnesses.List(); ok {
		if len(ids) > maxEntries {
			return fmt.Errorf("harnesses: at most %d ids", maxEntries)
		}
		for _, id := range ids {
			if !validID(id, false) {
				return fmt.Errorf("harnesses: %q isn't a harness id", id)
			}
			if knownHarness != nil && !knownHarness(id) {
				return fmt.Errorf("harnesses: no harness %q (GET /api/xbin/agent/providers lists them)", id)
			}
		}
	}
	if ids, ok := r.Models.List(); ok {
		if len(ids) > maxEntries {
			return fmt.Errorf("models: at most %d ids", maxEntries)
		}
		for _, id := range ids {
			if !validID(id, true) {
				return fmt.Errorf("models: %q isn't an upstream or upstream/model id", id)
			}
		}
	}
	return nil
}

// validID: a printable id without spaces; a model id may hold slashes.
func validID(id string, slashes bool) bool {
	if id == "" || len(id) > maxIDLen || strings.HasPrefix(id, "/") || strings.HasSuffix(id, "/") {
		return false
	}
	for _, c := range id {
		switch {
		case c <= ' ' || c == 0x7f || c == ',' || c == '"' || c == '\\':
			return false
		case c == '/' && !slashes:
			return false
		}
	}
	return true
}

// Within reports why r reaches past bounds — an org narrows what the
// workspace allows and never widens it — or nil. "any" is always within:
// it adds nothing of the org's own, and the bounds still apply to its
// tiles on every call.
func (r Rules) Within(bounds Rules) error {
	if err := webWithin(r.Web, bounds.Web); err != nil {
		return err
	}
	if err := mcpWithin(r.MCP, bounds.MCP); err != nil {
		return err
	}
	if ids, ok := r.Harnesses.List(); ok {
		if allowed, bounded := bounds.Harnesses.List(); bounded {
			for _, id := range ids {
				if !slices.Contains(allowed, id) {
					return fmt.Errorf("harnesses: %s is outside what the workspace allows (%s)", id, bounds.Harnesses)
				}
			}
		}
	}
	if ids, ok := r.Models.List(); ok {
		if allowed, bounded := bounds.Models.List(); bounded {
			for _, id := range ids {
				if !listCovers(allowed, id) {
					return fmt.Errorf("models: %s is outside what the workspace allows (%s)", id, bounds.Models)
				}
			}
		}
	}
	return nil
}

// listCovers: id is in list, or id is "upstream/model" and list holds its
// upstream.
func listCovers(list []string, id string) bool {
	if slices.Contains(list, id) {
		return true
	}
	for _, e := range list {
		if strings.HasPrefix(id, e+"/") {
			return true
		}
	}
	return false
}

// Patch is a partial change of a Rules: a nil field keeps its rule.
type Patch struct {
	WorkspaceAgent *Switch
	Harnesses      *Allow
	Models         *Allow
	Web            *WebRule
	MCP            *MCPRule
}

// PatchKeys are the keys ParsePatch reads.
var PatchKeys = []string{"workspaceAgent", "harnesses", "models", "web", "mcp"}

// ParsePatch reads a patch from a decoded JSON object: each key present
// sets its rule, null puts it back to the default (on, any); keys it
// doesn't know are an error unless ignore names them.
func ParsePatch(body map[string]json.RawMessage, ignore ...string) (Patch, error) {
	var p Patch
	for k, raw := range body {
		switch k {
		case "workspaceAgent":
			var s Switch
			if err := json.Unmarshal(raw, &s); err != nil {
				return p, err
			}
			p.WorkspaceAgent = &s
		case "harnesses":
			var a Allow
			if err := json.Unmarshal(raw, &a); err != nil {
				return p, fmt.Errorf("harnesses: %v", err)
			}
			p.Harnesses = &a
		case "models":
			var a Allow
			if err := json.Unmarshal(raw, &a); err != nil {
				return p, fmt.Errorf("models: %v", err)
			}
			p.Models = &a
		case "web":
			var w WebRule
			if err := json.Unmarshal(raw, &w); err != nil {
				return p, err
			}
			p.Web = &w
		case "mcp":
			var m MCPRule
			if err := json.Unmarshal(raw, &m); err != nil {
				return p, err
			}
			p.MCP = &m
		default:
			if !slices.Contains(ignore, k) {
				return p, fmt.Errorf("unknown key %q (workspaceAgent, harnesses, models, web, mcp)", k)
			}
		}
	}
	return p, nil
}

// Apply is r with p's rules set.
func (r Rules) Apply(p Patch) Rules {
	if p.WorkspaceAgent != nil {
		r.WorkspaceAgent = *p.WorkspaceAgent
		if !r.WorkspaceAgent.IsOff() {
			r.WorkspaceAgent = ""
		}
	}
	if p.Harnesses != nil {
		r.Harnesses = *p.Harnesses
	}
	if p.Models != nil {
		r.Models = *p.Models
	}
	if p.Web != nil {
		r.Web = *p.Web
	}
	if p.MCP != nil {
		r.MCP = *p.MCP
	}
	return r
}

// ModelRef names one model of the LLM connector: its upstream, the model's
// id there, and whether the upstream is marked private.
type ModelRef struct {
	Upstream string `json:"upstream"`
	Model    string `json:"model,omitempty"`
	Private  bool   `json:"private"`
}

// ID is "upstream/model" (the upstream alone for a ref that names no model).
func (m ModelRef) ID() string {
	if m.Model == "" {
		return m.Upstream
	}
	return m.Upstream + "/" + m.Model
}

// ParseModelID splits "upstream/model" at its first slash (a model's own
// id may hold more); Private is false — only the connector knows.
func ParseModelID(id string) ModelRef {
	up, model, _ := strings.Cut(strings.TrimSpace(id), "/")
	return ModelRef{Upstream: up, Model: model}
}

// Whose credentials serve a coding agent's model calls (plans/wsagent
// s4-connector.md §1.5). The workspace sets a mode; a person and a session
// may state a choice, which the mode may or may not honour.
const (
	// CredentialsOwnFirst (the default): the person's own sign-in when they
	// have one, else the LLM connector; a choice of either is honoured.
	CredentialsOwnFirst = "own-first"
	// CredentialsConnectorFirst: the connector, unless the person
	// explicitly chose their own sign-in (for the harness, or the session).
	CredentialsConnectorFirst = "connector-first"
	// CredentialsConnectorOnly: the connector; own sign-ins are not used,
	// whatever anyone chose.
	CredentialsConnectorOnly = "connector-only"

	// A person's or a session's choice.
	CredentialsOwn       = "own"       // my own sign-in
	CredentialsConnector = "connector" // the connector
)

// CredentialMode is a workspace mode as stored: "" and anything unknown
// read as own-first, and "connector" — what connector-only was called
// before the three modes — as connector-only. ok is false for a value
// that is no mode at all.
func CredentialMode(s string) (mode string, ok bool) {
	switch s {
	case "", CredentialsOwnFirst:
		return CredentialsOwnFirst, true
	case CredentialsConnectorFirst:
		return CredentialsConnectorFirst, true
	case CredentialsConnectorOnly, CredentialsConnector:
		return CredentialsConnectorOnly, true
	}
	return CredentialsOwnFirst, false
}

// ResolveCredentials is the precedence rule under a mode: the session's
// own choice, then the person's saved choice for the harness, then the
// mode. It answers "own", "connector" or "own-first" (their own sign-in
// when they have one, else the connector — only the host that starts the
// CLI can see which), and whether the mode forced it past any choice.
func ResolveCredentials(session, saved, mode string) (use string, forced bool) {
	mode, _ = CredentialMode(mode)
	if mode == CredentialsConnectorOnly {
		return CredentialsConnector, true
	}
	for _, c := range []string{session, saved} {
		if c == CredentialsOwn || c == CredentialsConnector {
			return c, false
		}
	}
	if mode == CredentialsConnectorFirst {
		return CredentialsConnector, false
	}
	return CredentialsOwnFirst, false
}
