// harness_view.go — how a coding agent's run looks to the tile (D-harness
// §4.3): the `harness` summary on runSummary and the `harness` stream event
// (§4.3.2/§4.3.3), the park's card data in pendingState.harness (§4.3.4),
// and the `acp` a tool row's Meta.harness is lifted to by messageView
// (§4.3.5).
package main

import (
	"encoding/json"
	"strings"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// evHarness is the stream event carrying a harness run's whole summary.
const evHarness = "harness"

// harnessMeta is msgMeta.harness: on a tool row the call as the adapter
// described it (lifted to `acp`), on an assistant row only Parent (its text
// belongs to a harness-internal subagent). RawInput and Text are the
// engine's own (merging updates), never lifted.
type harnessMeta struct {
	Kind            string          `json:"kind,omitempty"`
	Title           string          `json:"title,omitempty"`
	Label           string          `json:"label,omitempty"`
	Tool            string          `json:"tool,omitempty"`
	Status          string          `json:"status,omitempty"`
	Parent          string          `json:"parent,omitempty"`
	Subagent        bool            `json:"subagent,omitempty"`
	PlanReview      bool            `json:"planReview,omitempty"`
	Locations       []hLocation     `json:"locations,omitempty"`
	Files           []string        `json:"files,omitempty"`
	ExitCode        *int            `json:"exitCode,omitempty"`
	Output          string          `json:"output,omitempty"`
	OutputTruncated int64           `json:"outputTruncated,omitempty"`
	Diffs           []hDiff         `json:"diffs,omitempty"`
	RawInput        json.RawMessage `json:"rawInput,omitempty"`
	Text            string          `json:"text,omitempty"`
}

type hLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

type hDiff struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // added | modified | deleted
	Add       int    `json:"add"`
	Del       int    `json:"del"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated"`
}

// acpView is the `acp` of a message (§4.3.5): the call on a tool row,
// {parent} on an assistant row of a subagent's, nil otherwise.
func (h *harnessMeta) acpView(role string) map[string]any {
	if h == nil {
		return nil
	}
	if role != "tool" {
		if h.Parent == "" {
			return nil
		}
		return map[string]any{"parent": h.Parent}
	}
	v := map[string]any{"kind": orStr(h.Kind, "other"), "title": h.Title, "status": h.Status,
		"subagent": h.Subagent, "planReview": h.PlanReview}
	for k, s := range map[string]string{"label": h.Label, "tool": h.Tool, "parent": h.Parent} {
		if s != "" {
			v[k] = s
		}
	}
	if len(h.Locations) > 0 {
		v["locations"] = h.Locations
	}
	if len(h.Files) > 0 {
		v["files"] = h.Files
	}
	if h.ExitCode != nil {
		v["exitCode"] = *h.ExitCode
	}
	if h.Output != "" || h.ExitCode != nil {
		v["output"], v["outputTruncated"] = h.Output, h.OutputTruncated
	}
	if len(h.Diffs) > 0 {
		v["diffs"] = h.Diffs
	}
	return v
}

// --- pendingState.harness (§4.3.4) ------------------------------------------------

// hPark is the card data of a harness park: a permission (approval), a
// question (a form elicitation) or a sign-in (login). PID, RPCID and EID
// are the engine's (clients ignore them): the request to answer.
type hPark struct {
	CallID       string          `json:"callId,omitempty"`
	Options      []hOption       `json:"options,omitempty"`
	Tool         *hParkTool      `json:"tool,omitempty"`
	Rule         *acp.Rule       `json:"rule,omitempty"`
	DefaultToNo  bool            `json:"defaultToNo,omitempty"`
	Description  string          `json:"description,omitempty"`
	PlanApproval bool            `json:"planApproval,omitempty"`
	Plan         string          `json:"plan,omitempty"`
	PID          string          `json:"pid,omitempty"`
	RPCID        string          `json:"rpcId,omitempty"`
	EID          string          `json:"eid,omitempty"`
	Message      string          `json:"message,omitempty"`
	Schema       json.RawMessage `json:"schema,omitempty"`
	Login        json.RawMessage `json:"login,omitempty"`
}

type hOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Explicit bool   `json:"explicit,omitempty"`
}

type hParkTool struct {
	Title    string          `json:"title"`
	Kind     string          `json:"kind"`
	Name     string          `json:"name,omitempty"`
	Label    string          `json:"label,omitempty"`
	Command  string          `json:"command,omitempty"`
	RawInput json.RawMessage `json:"rawInput,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
}

// pending is the permission request as acp.Permissions restores it.
func (p *hPark) pending() harnessPending {
	pd := acp.Pending{PID: p.PID}
	if p.Tool != nil {
		pd.ToolCall = acp.ToolCallRef{ID: acpCallID(p.CallID), Name: p.Tool.Name, Title: p.Tool.Title, Kind: p.Tool.Kind,
			RawInput: p.Tool.RawInput, Content: p.Tool.Content}
	}
	for _, o := range p.Options {
		pd.Options = append(pd.Options, acp.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	return harnessPending{Pending: pd, RPC: json.RawMessage(p.RPCID)}
}

// hQueued is a request waiting behind the park (harness_sessions.queue).
type hQueued struct {
	Kind string `json:"kind"` // approval | question
	Perm *hPark `json:"perm,omitempty"`
}

// acpCallID is the adapter's id of a stored call id ("h<gen>:<id>").
func acpCallID(stored string) string {
	if i := strings.IndexByte(stored, ':'); i > 0 && strings.HasPrefix(stored, "h") {
		return stored[i+1:]
	}
	return stored
}

// --- the summary (§4.3.2) ------------------------------------------------------------

// hCounts is harness_sessions.counts: this conversation's calls, distinct
// files edited (Paths, the engine's own), lines added and deleted.
type hCounts struct {
	Tools int      `json:"tools"`
	Files int      `json:"files"`
	Add   int      `json:"add"`
	Del   int      `json:"del"`
	Paths []string `json:"paths,omitempty"`
}

func (c *hCounts) addFile(path string) {
	if path == "" || hasStr(c.Paths, path) {
		return
	}
	c.Files++
	if len(c.Paths) < 1000 {
		c.Paths = append(c.Paths, path)
	}
}

// harnessSummaryOf is runSummary's `harness` for a harness run (nil for a
// built-in one, or before the agent has a database).
func harnessSummaryOf(r *Run) map[string]any {
	if r == nil || r.Engine != engineHarness || agent == nil || agent.db == nil {
		return nil
	}
	return harnessSummary(agent.db, r)
}

// harnessSummary is §4.3.2 for run r: its config, its session row, the
// park and — while this process drives it — what it is doing.
func harnessSummary(d *DB, r *Run) map[string]any {
	cfg, err := d.runConfig(r.ID)
	if err != nil || cfg.Harness == nil {
		return nil
	}
	h := cfg.Harness
	hs, _ := d.harnessSession(r.ID)
	if hs == nil {
		hs = &harnessSession{State: hsNone}
	}
	var st acp.SessionState
	_ = json.Unmarshal([]byte(hs.Snapshot), &st)
	prov := harnessProvider(h.Provider, nil)
	name := prov.Name
	if h.Provider == fakeHarness || name == h.Provider {
		name = harnessName(h.Provider)
	}
	state := map[string]string{hsNone: "stopped", hsStopped: "stopped", hsStarting: "starting", hsLogin: "login",
		hsLost: "lost", hsFailed: "failed"}[hs.State]
	if hs.State == hsLive {
		state = "ready"
		if hs.PromptState != "" || r.Status == statusRunning || r.Status == statusWaiting {
			state = "working"
		}
	}
	if state == "" {
		state = "stopped"
	}
	errText := ""
	if state == "lost" || state == "failed" {
		errText = hs.Error
	}
	out := map[string]any{"provider": h.Provider, "name": name, "state": state, "error": errText,
		"mode": harnessModes(prov, st, h.Mode), "options": harnessOptionsView(st.Options),
		"commands": orCommands(st.Commands), "steering": st.Steering || hs.Steering, "title": hs.Title, "gen": hs.Gen}
	var c hCounts
	_ = json.Unmarshal([]byte(hs.Counts), &c)
	c.Paths = nil
	out["counts"] = c
	if hs.Usage != "" && json.Valid([]byte(hs.Usage)) {
		out["usage"] = json.RawMessage(hs.Usage)
	} else if st.Usage != nil {
		out["usage"] = st.Usage
	}
	if hs.Plan != "" && json.Valid([]byte(hs.Plan)) {
		out["plan"] = json.RawMessage(hs.Plan)
	}
	sb := map[string]any{"ref": h.Ref, "cwd": orStr(hs.Cwd, h.Cwd), "shared": hs.Shared}
	if b, ok := cfg.sandboxBinding(h.Ref); ok {
		sb["name"] = orStr(b.Name, h.Ref)
		if sb["cwd"] == "" {
			sb["cwd"] = b.Cwd
		}
	}
	out["sandbox"] = sb
	if p := parsePending(r.Pending); r.Status == statusWaiting && p.Harness != nil {
		title := ""
		switch p.Kind {
		case "approval":
			if p.Harness.Tool != nil {
				title = orStr(p.Harness.Tool.Title, p.Harness.Tool.Label)
			}
		case "question":
			title = p.Harness.Message
		case "login":
			title = "Sign in to " + name
		}
		out["pending"] = map[string]any{"park": p.Park, "kind": p.Kind, "title": title}
	}
	if hs.State == hsLogin && hs.Login != "" && json.Valid([]byte(hs.Login)) {
		out["login"] = json.RawMessage(hs.Login)
	}
	if agent != nil && agent.eng != nil {
		if s := agent.eng.harnessOf(r.ID); s != nil {
			a := s.activityNow()
			if p := parsePending(r.Pending); r.Status == statusWaiting && p.Harness != nil {
				a = hActivity{Kind: "waiting", At: a.At}
			}
			out["activity"] = a
		}
	}
	return out
}

// harnessModes is the summary's mode: the session's modes, else its config
// option of category mode (an adapter that speaks its modes only as one —
// opencode's build/plan agents: the mode picker covers it, §4.2.4), else
// the catalog's; `explicit` from the catalog.
func harnessModes(prov acp.Provider, st acp.SessionState, want string) map[string]any {
	explicit := map[string]bool{}
	for _, m := range prov.Modes {
		explicit[m.ID] = m.Explicit
	}
	var avail []map[string]any
	add := func(id, name, desc string) {
		e := map[string]any{"id": id, "name": orStr(name, id)}
		if desc != "" {
			e["description"] = desc
		}
		if explicit[id] {
			e["explicit"] = true
		}
		avail = append(avail, e)
	}
	cur := want
	if st.Modes != nil && len(st.Modes.AvailableModes) > 0 {
		cur = orStr(st.Modes.CurrentModeID, want)
		for _, m := range st.Modes.AvailableModes {
			add(m.ID, m.Name, m.Description)
		}
	} else if o := modeOption(st.Options); o != nil {
		cur = orStr(o.CurrentValue, want)
		for _, v := range o.Options {
			add(v.Value, v.Name, v.Description)
		}
	} else {
		for _, m := range prov.Modes {
			e := map[string]any{"id": m.ID, "name": m.Name}
			if m.Explicit {
				e["explicit"] = true
			}
			avail = append(avail, e)
		}
	}
	if avail == nil {
		avail = []map[string]any{}
	}
	return map[string]any{"current": orStr(cur, prov.DefaultMode), "available": avail}
}

// modeOption is the adapter's config option of category mode, if any.
func modeOption(opts []acp.ConfigOption) *acp.ConfigOption {
	for i := range opts {
		if opts[i].Category == "mode" && len(opts[i].Options) > 0 {
			return &opts[i]
		}
	}
	return nil
}

// harnessOptionsView is the adapter's config options but a mode one (the
// mode picker covers it).
func harnessOptionsView(opts []acp.ConfigOption) []acp.ConfigOption {
	out := []acp.ConfigOption{}
	for _, o := range opts {
		if o.Category != "mode" {
			out = append(out, o)
		}
	}
	return out
}

func orCommands(c []acp.Command) []acp.Command {
	if c == nil {
		return []acp.Command{}
	}
	return c
}
