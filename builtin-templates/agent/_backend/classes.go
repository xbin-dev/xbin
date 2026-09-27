// classes.go — agent classes (D116): which toolsets a conversation gets.
//
// A class names toolsets (files, repl, web, internal, sandbox, subagents,
// schedule, threads, skills — the core tools are always there), which MCP
// servers, sandbox managers and sandbox egress it may use, and optionally a
// model and a system addendum. The tile's managers define them (PUT
// /classes); three are built in — internal 🔒 (the old private lane), web 🌐
// (the old web lane) and coding ▣ — and a deleted built-in comes back as its
// default, since old conversations and APIs name it.
//
// A conversation's class is fixed at its start (Config.Class, inherited by
// subagents and by the schedules it makes) and read every step: toolSpecs
// offers only the class's toolsets and runTool refuses the rest. The toolset
// firewall is the class's property: one holding internal reach together with
// any egress (web, or a sandbox with internet) "can move internal data out" —
// saving one takes confirmMixed, and its conversations say so. Config.Toolset
// keeps the lane ("private" | "web") the class had when the conversation
// started; an edit to a class applies from the next step but never carries a
// conversation across the firewall (clampTo).
//
// Classes live in settings k='classes'; classStore caches them so classOf is
// an atomic load and a scan of a short list.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// The toolsets a class may hold.
const (
	tsFiles     = "files"
	tsRepl      = "repl"
	tsWeb       = "web"
	tsInternal  = "internal"
	tsSandbox   = "sandbox"
	tsSubagents = "subagents"
	tsSchedule  = "schedule"
	tsThreads   = "threads"
	tsSkills    = "skills"
)

var classToolsets = []string{tsFiles, tsRepl, tsWeb, tsInternal, tsSandbox, tsSubagents, tsSchedule, tsThreads, tsSkills}

// Sandbox egress values (docs/sandbox-manager.md).
var sandboxEgressKinds = []string{"none", "internet", "open"}

// The built-in class ids.
const (
	classInternal = "internal"
	classWeb      = "web"
	classCoding   = "coding"
)

// classSet is "all" or a list of names. set records whether a request named
// it at all (a class with internal and no mcp gets "all").
type classSet struct {
	All   bool
	Names []string
	set   bool
}

func (s classSet) MarshalJSON() ([]byte, error) {
	if s.All {
		return []byte(`"all"`), nil
	}
	if s.Names == nil {
		return []byte(`[]`), nil
	}
	return json.Marshal(s.Names)
}

func (s *classSet) UnmarshalJSON(b []byte) error {
	*s = classSet{set: true}
	switch t := strings.TrimSpace(string(b)); {
	case t == "null":
		s.set = false
		return nil
	case strings.HasPrefix(t, `"`):
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		switch v {
		case "all":
			s.All = true
		case "", "none":
		default:
			s.Names = []string{v}
		}
		return nil
	}
	return json.Unmarshal(b, &s.Names)
}

func (s classSet) allows(name string) bool { return s.All || hasStr(s.Names, name) }

// an agent class (D116): which toolsets a conversation of it gets.
type agentClass struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Icon          string   `json:"icon,omitempty"`
	Toolsets      []string `json:"toolsets"`                // files, repl, web, internal, sandbox, subagents, schedule, threads, skills
	MCP           classSet `json:"mcp"`                     // "all" or a list of MCP server names (only meaningful with internal)
	Managers      classSet `json:"managers"`                // "all" or a list of sandbox-manager providers (tile paths)
	SandboxEgress []string `json:"sandboxEgress,omitempty"` // egress values a sandbox may have: none, internet, open
	Model         string   `json:"model,omitempty"`
	System        string   `json:"system,omitempty"`
	Who           string   `json:"who,omitempty"` // "everyone" (default) | "managers"
}

func (c agentClass) has(toolset string) bool { return hasStr(c.Toolsets, toolset) }

// allowsManager: may a conversation of this class bind a sandbox of provider?
// A class naming a tile allows every instance of it ("apps/cs" allows
// "apps/cs#eu"); naming an instance allows only that one.
func (c agentClass) allowsManager(provider string) bool {
	tile, _, _ := strings.Cut(provider, "#")
	return c.has(tsSandbox) && (c.Managers.allows(provider) || c.Managers.allows(tile))
}

// allowsEgress: may a conversation of this class bind a sandbox with egress?
func (c agentClass) allowsEgress(egress string) bool {
	return c.has(tsSandbox) && hasStr(c.SandboxEgress, egress)
}

// allowsMCP: may a conversation of this class use the MCP server?
func (c agentClass) allowsMCP(server string) bool {
	return c.has(tsInternal) && c.MCP.allows(server)
}

// egress: can a conversation of this class reach outside the workspace —
// the web tools, or a sandbox that may have a network?
func (c agentClass) egress() bool {
	if c.has(tsWeb) {
		return true
	}
	if c.has(tsSandbox) {
		for _, e := range c.SandboxEgress {
			if e != "none" {
				return true
			}
		}
	}
	return false
}

// mixed: internal reach together with egress — the class "can move
// internal data out". Saving one takes confirmMixed; the built-ins never are.
func (c agentClass) mixed() bool { return c.has(tsInternal) && c.egress() }

// lane is the class as the old two lanes (Config.toolset()): "web" when it
// reaches outside and has no internal reach, else "private". Everything keyed
// by the lane (skills, the channel policy, the thread tools' scope, older
// tiles reading config.toolset) keeps working from it.
func (c agentClass) lane() string {
	if c.egress() && !c.has(tsInternal) {
		return "web"
	}
	return "private"
}

// usableBy: may w start a conversation (or an automation) in this class?
func (c agentClass) usableBy(w who) bool { return c.Who != "managers" || w.manager() }

// clampTo keeps a conversation on its side of the firewall whatever an edit
// did to its class since it started: lane is the lane it started in
// (Config.Toolset; "" = unknown, nothing to hold it to). One that started
// reaching outside never gains internal reach; one that started without
// egress gains it only as a (confirmed) mixed class.
func (c agentClass) clampTo(lane string) agentClass {
	switch {
	case lane == "web" && c.has(tsInternal):
		c.Toolsets = without(c.Toolsets, tsInternal)
		c.MCP = classSet{}
	case lane == "private" && c.lane() == "web":
		c.Toolsets = without(c.Toolsets, tsWeb)
		if c.has(tsSandbox) {
			c.SandboxEgress = []string{"none"}
		}
	}
	return c
}

func without(list []string, s string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// builtinClasses are the three every agent has.
func builtinClasses() []agentClass {
	return []agentClass{
		{ID: classInternal, Name: "Internal", Icon: "🔒",
			Description: "Your workspace's systems and data (xbin_call, MCP servers) — no web.",
			Toolsets:    []string{tsFiles, tsRepl, tsInternal, tsSubagents, tsSchedule, tsThreads, tsSkills},
			MCP:         classSet{All: true}},
		// threads too: the web lane always had the thread tools (its own
		// automations only — threads_tools.go refuses "all" in the web lane)
		{ID: classWeb, Name: "Web", Icon: "🌐",
			Description: "Searches and reads the web — no internal systems.",
			Toolsets:    []string{tsFiles, tsRepl, tsWeb, tsSubagents, tsSchedule, tsThreads, tsSkills}},
		{ID: classCoding, Name: "Coding", Icon: "▣",
			Description:   "Works in a coding sandbox, with the web — no internal systems.",
			Toolsets:      []string{tsSandbox, tsWeb, tsFiles, tsSubagents, tsSkills},
			Managers:      classSet{All: true},
			SandboxEgress: []string{"none", "internet"}},
	}
}

func isBuiltinClass(id string) bool {
	return id == classInternal || id == classWeb || id == classCoding
}

// laneClass is the built-in a legacy toolset names.
func laneClass(toolset string) string {
	if normalizeToolset(toolset) == "web" {
		return classWeb
	}
	return classInternal
}

// --- the stored set ----------------------------------------------------------------

// classSettings is settings k='classes'.
type classSettings struct {
	Classes []agentClass `json:"classes"`
	Default string       `json:"default,omitempty"` // a new conversation's class when none is named
}

// classState is the classes in force: the built-ins first, in their order
// (each as stored, or its default), then the others as stored.
type classState struct {
	list   []agentClass
	def    string
	stored map[string]bool // the ids in the stored settings (a missing built-in is not)
}

var (
	classStore   atomic.Pointer[classState]
	builtinState = newClassState(classSettings{})
)

func newClassState(s classSettings) *classState {
	st := &classState{def: s.Default, stored: map[string]bool{}}
	edited := map[string]agentClass{}
	var custom []agentClass
	for _, c := range s.Classes {
		if c.ID == "" || st.stored[c.ID] {
			continue
		}
		st.stored[c.ID] = true
		if isBuiltinClass(c.ID) {
			edited[c.ID] = c
		} else {
			custom = append(custom, c)
		}
	}
	for _, b := range builtinClasses() {
		if c, ok := edited[b.ID]; ok {
			b = c
		}
		st.list = append(st.list, b)
	}
	st.list = append(st.list, custom...)
	if _, ok := st.find(st.def); !ok {
		st.def = classInternal
	}
	return st
}

func (st *classState) find(id string) (agentClass, bool) {
	for _, c := range st.list {
		if c.ID == id {
			return c, true
		}
	}
	return agentClass{}, false
}

// defaultFor is the class a new conversation of w's gets when it names none:
// the tile's default when w may use it, else the first one w may.
func (st *classState) defaultFor(w who) agentClass {
	if c, ok := st.find(st.def); ok && c.usableBy(w) {
		return c
	}
	for _, c := range st.list {
		if c.usableBy(w) {
			return c
		}
	}
	c, _ := st.find(classInternal)
	return c
}

// currentClasses is the cached set (the built-ins until loadClasses ran).
func currentClasses() *classState {
	if st := classStore.Load(); st != nil {
		return st
	}
	return builtinState
}

// loadClasses (re)reads the stored classes into the cache.
func loadClasses(d *DB) *classState {
	var s classSettings
	if raw := d.getSetting("classes"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	st := newClassState(s)
	classStore.Store(st)
	return st
}

// classOf is a conversation's class. Never fails: cfg.Class names a stored
// class; unknown or "" resolves from cfg.Toolset to a built-in. Held to the
// lane the conversation started in (clampTo).
func classOf(cfg Config) agentClass {
	st := currentClasses()
	c, ok := st.find(cfg.Class)
	if !ok {
		c, _ = st.find(laneClass(cfg.Toolset))
	}
	if cfg.Toolset == "" {
		return c
	}
	return c.clampTo(normalizeToolset(cfg.Toolset))
}

// fixedLane is the lane the conversation started in — what what it starts
// (subagents, schedules) is held to, even when an edit to its class has since
// clamped it into another.
func (c Config) fixedLane() string {
	if c.Toolset != "" {
		return normalizeToolset(c.Toolset)
	}
	return c.toolset()
}

// setClass fixes a new conversation's class: its id, and its lane in Toolset
// (what clampTo holds it to, and what older tiles and the lane-keyed
// features read). Without an explicit system prompt the class's addendum
// joins the agent's.
func (c *Config) setClass(cls agentClass, explicitSystem bool) {
	c.Class, c.Toolset = cls.ID, cls.lane()
	if !explicitSystem && strings.TrimSpace(cls.System) != "" {
		c.System = strings.TrimRight(c.System, "\n") + "\n\n" + strings.TrimSpace(cls.System)
	}
}

// errClass is a class a request may not name; code is the HTTP status.
type errClass struct {
	code int
	msg  string
}

func (e *errClass) Error() string { return e.msg }

// requestedClass is the class a new conversation or automation asks for:
// class by id; else a legacy toolset's built-in; else w's default.
func requestedClass(w who, class, toolset string) (agentClass, error) {
	st := currentClasses()
	var c agentClass
	switch {
	case strings.TrimSpace(class) != "":
		var ok bool
		if c, ok = st.find(strings.TrimSpace(class)); !ok {
			return c, &errClass{400, fmt.Sprintf("class: no class %q (GET /classes lists them)", class)}
		}
	case strings.TrimSpace(toolset) != "":
		c, _ = st.find(laneClass(toolset))
	default:
		return st.defaultFor(w), nil
	}
	if !c.usableBy(w) {
		return c, &errClass{403, fmt.Sprintf("the %s class is for the agent's managers", c.Name)}
	}
	return c, nil
}

// writeClassErr answers a requestedClass error.
func writeClassErr(w http.ResponseWriter, err error) {
	code := 400
	if e, ok := err.(*errClass); ok {
		code = e.code
	}
	xbin.WriteError(w, code, err.Error())
}

// classView is a class as the tile shows it: with what it adds up to.
func classView(c agentClass) map[string]any {
	return map[string]any{
		"id": c.ID, "name": c.Name, "description": c.Description, "icon": c.Icon, "toolsets": c.Toolsets,
		"mcp": c.MCP, "managers": c.Managers, "sandboxEgress": orList(c.SandboxEgress), "model": c.Model,
		"system": c.System, "who": orStr(c.Who, "everyone"), "builtin": isBuiltinClass(c.ID),
		"lane": c.lane(), "egress": c.egress(), "mixed": c.mixed(),
	}
}

func orList(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// --- validation --------------------------------------------------------------------

var classIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

const maxClasses = 32

// normalize checks one class as a manager saved it and fills its defaults;
// "" = fine.
func (c *agentClass) normalize() string {
	c.ID, c.Name = strings.TrimSpace(c.ID), strings.TrimSpace(c.Name)
	c.Icon, c.Description = strings.TrimSpace(c.Icon), strings.TrimSpace(c.Description)
	c.Model, c.Who = strings.TrimSpace(c.Model), strings.TrimSpace(c.Who)
	if !classIDRe.MatchString(c.ID) {
		return fmt.Sprintf("class id %q: a–z, 0–9 and -, starting with a letter, up to 32", c.ID)
	}
	if c.Name == "" {
		c.Name = c.ID
	}
	var ts []string
	for _, t := range c.Toolsets {
		t = strings.TrimSpace(t)
		if !hasStr(classToolsets, t) {
			return fmt.Sprintf("class %s: unknown toolset %q (one of %s)", c.ID, t, strings.Join(classToolsets, ", "))
		}
		if !hasStr(ts, t) {
			ts = append(ts, t)
		}
	}
	c.Toolsets = orList(ts)
	var eg []string
	for _, e := range c.SandboxEgress {
		if !hasStr(sandboxEgressKinds, e) {
			return fmt.Sprintf("class %s: sandboxEgress is none, internet or open (not %q)", c.ID, e)
		}
		if !hasStr(eg, e) {
			eg = append(eg, e)
		}
	}
	c.SandboxEgress = eg
	if c.has(tsSandbox) && len(c.SandboxEgress) == 0 {
		c.SandboxEgress = []string{"none"}
	}
	if !c.MCP.set && c.has(tsInternal) {
		c.MCP.All = true
	}
	if !c.Managers.set && c.has(tsSandbox) {
		c.Managers.All = true
	}
	for _, s := range [][]string{c.MCP.Names, c.Managers.Names} {
		for _, n := range s {
			if n = strings.TrimSpace(n); n == "" || len(n) > 200 || strings.ContainsAny(n, " \t\n") {
				return fmt.Sprintf("class %s: %q isn't a server or tile name", c.ID, n)
			}
		}
	}
	switch {
	case len(c.Name) > 60:
		return fmt.Sprintf("class %s: the name is up to 60 characters", c.ID)
	case len(c.Description) > 400:
		return fmt.Sprintf("class %s: the description is up to 400 characters", c.ID)
	case len(c.Icon) > 32:
		return fmt.Sprintf("class %s: the icon is a short symbol", c.ID)
	case len(c.System) > 16000:
		return fmt.Sprintf("class %s: the system addendum is up to 16000 characters", c.ID)
	case !validPick(c.Model):
		return fmt.Sprintf("class %s: model is a model id from GET /models", c.ID)
	case c.Who != "" && c.Who != "everyone" && c.Who != "managers":
		return fmt.Sprintf("class %s: who is everyone or managers", c.ID)
	}
	if c.Who == "everyone" {
		c.Who = ""
	}
	return ""
}

// --- routes ---------------------------------------------------------------------------

// handleGetClasses lists the classes the caller may start conversations in
// (a manager sees them all) and the one a new conversation of theirs gets.
// stored says a class is in the saved set — a built-in that is not is its
// default (the editor sends back only the stored ones, so a built-in nobody
// edited keeps following the template's default).
//
//	GET /classes → {classes: [classView… + stored], default: "<id>"}
func handleGetClasses(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	st := loadClasses(agent.db) // fresh: another process may have saved
	out := []map[string]any{}
	for _, cls := range st.list {
		if cls.usableBy(c) {
			v := classView(cls)
			v["stored"] = st.stored[cls.ID]
			out = append(out, v)
		}
	}
	xbin.WriteJSON(w, 200, map[string]any{"classes": out, "default": st.defaultFor(c).ID})
}

// handlePutClasses replaces the classes (the tile's managers).
//
//	PUT /classes {classes: [agentClass…], default?, confirmMixed?}
//
// A class that mixes internal reach with egress is refused (409, naming them
// in mixed) unless confirmMixed is true. A built-in left out comes back as its
// default. A class a channel runs strangers in stays one (webClassGuard, 400).
func handlePutClasses(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Classes      []agentClass `json:"classes"`
		Default      string       `json:"default"`
		ConfirmMixed bool         `json:"confirmMixed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		xbin.WriteError(w, 400, "need {classes: [...], default?, confirmMixed?}")
		return
	}
	if len(body.Classes) > maxClasses {
		xbin.WriteError(w, 400, fmt.Sprintf("at most %d classes", maxClasses))
		return
	}
	seen := map[string]bool{}
	var mixed []string
	for i := range body.Classes {
		c := &body.Classes[i]
		if msg := c.normalize(); msg != "" {
			xbin.WriteError(w, 400, msg)
			return
		}
		if seen[c.ID] {
			xbin.WriteError(w, 400, "two classes named "+c.ID)
			return
		}
		seen[c.ID] = true
		if c.mixed() {
			mixed = append(mixed, c.ID)
		}
	}
	next := newClassState(classSettings{Classes: body.Classes})
	if msg := webClassGuard(loadClasses(agent.db), next, agent.db.listChannels()); msg != "" {
		xbin.WriteError(w, 400, msg)
		return
	}
	if len(mixed) > 0 && !body.ConfirmMixed {
		xbin.WriteJSON(w, http.StatusConflict, map[string]any{
			"error": "these classes hold internal reach together with egress, so their conversations can move internal data out: " +
				strings.Join(mixed, ", ") + " — save again with confirmMixed to allow it",
			"mixed": mixed,
		})
		return
	}
	body.Default = strings.TrimSpace(body.Default)
	if body.Default != "" {
		if _, ok := next.find(body.Default); !ok {
			xbin.WriteError(w, 400, "default: one of the classes")
			return
		}
	}
	raw, _ := json.Marshal(classSettings{Classes: body.Classes, Default: body.Default})
	if err := agent.db.putSetting("classes", string(raw)); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	loadClasses(agent.db)
	handleGetClasses(w, r)
}

// webClassGuard keeps what the channels run strangers in: a class a
// channel's policy names as everyone else's (webClass; unset: the built-in
// web) must stay in the web lane — reaching outside, with no internal reach —
// and can't be deleted while it is named (a built-in left out comes back as
// its default, which is). cur is the set in force, next the one being saved;
// "" = fine. A class not in force as a web-lane class now (a name gone stale,
// or one already moved) holds nothing up: the channel falls back to web.
func webClassGuard(cur, next *classState, chans []*Channel) string {
	var ids []string
	users := map[string][]string{}
	for _, ch := range chans {
		id := orStr(ch.Policy.WebClass, classWeb)
		if _, ok := users[id]; !ok {
			ids = append(ids, id)
		}
		users[id] = append(users[id], strconv.Quote(ch.title()))
	}
	for _, id := range ids {
		if was, ok := cur.find(id); !ok || was.lane() != "web" {
			continue
		}
		on := "channel " + users[id][0]
		if n := len(users[id]); n > 1 {
			on = fmt.Sprintf("channels %s and %d more", users[id][0], n-1)
		}
		now, ok := next.find(id)
		switch {
		case !ok:
			return fmt.Sprintf("class %s is everyone else's class (webClass) on %s: pick another in its rules before deleting it", id, on)
		case now.lane() != "web":
			return fmt.Sprintf("class %s is everyone else's class (webClass) on %s, so it must keep reaching outside with no internal reach "+
				"(a reply is an egress) — pick another in its rules first", id, on)
		}
	}
	return ""
}
