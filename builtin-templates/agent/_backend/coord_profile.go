// coord_profile.go — the coordinator kit (API.md §The coordinator,
// "Profiles"; D222's lineage): what a tool-only coordinating conversation
// is, whatever it coordinates. A coordinator is a person's own top-level
// conversation forced into a role: a built-in class it can't leave (its
// firewall lane — never internal reach), a fixed list of tools that start
// and steer work elsewhere, and the rule that it never answers a person's
// question or approval for them — a message it sends something that waits
// for a person is held until that person answered (coord_hold.go). It is
// woken, coalesced, when something it started changes state
// (coord_wake.go), reads code only through research subagents
// (coord_research.go, coord_reader.go) and keeps what it learns in a
// knowledge store whose index rides its prompt (coord_know*.go,
// coord_prompt.go).
//
// A profile is one kind of coordinator: Projects' is the first ("project",
// projects_coord_profile.go); "workspace" is a registered, empty slot
// (coord_workspace.go). The kit owns the mechanisms and the invariants —
// which run is whose coordinator, that a tool is on its profile's
// allowlist, that every call resolves the coordinator again and holds its
// lane, the wake's coalescing, the prompt's assembly — and a profile
// supplies the hooks below. Nothing here names a project.
package main

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// coordProfile is one kind of coordinator. Registered once, at init
// (registerCoordProfile), and never changed after; a nil hook means the
// profile has none of that.
type coordProfile struct {
	// ID names the profile ("project", "workspace"); Origin is runs.origin
	// of its conversations — stamped by the code that makes one, never taken
	// from a request, and one profile's only.
	ID     string
	Origin string
	// Class is the built-in class its conversations are forced into (their
	// lane; a coordinator never holds internal reach, whatever an edit did
	// to the class since); Deny the tools of that class it never has.
	Class string
	Deny  []string
	// Allow is its tool allowlist: the only names a tool of this profile
	// may be registered under (coord_tools.go). The boundary is this list,
	// read in one place — registering a tool never widens it.
	Allow []string
	// Limits bound what it starts.
	Limits coordBounds
	// Words say, in the coordinator's words, where something it started
	// stands (nil: the state's own name).
	Words coordWords

	// Is says whether run — top-level, of this profile's origin, not hosted
	// — is one of its coordinators, from what is stored: no liveness, no
	// side effects. It decides the tool list, the wakes and the updates.
	Is func(d *DB, run *Run) bool
	// Resolve is the coordinator's boundary, called again on every tool
	// call: run is a live coordinator of this profile (what it coordinates
	// still there, its person still allowed) and scope is what its tools
	// act on. The error is what the tool answers.
	Resolve func(ag *Agent, t *DB, run *Run, cfg Config) (scope any, err error)
	// Pending says whether something run started changed state and asks to
	// wake it; the kit coalesces the wakes (coordWakes).
	Pending func(d *DB, run *Run) bool
	// Deliver is what changed since run last heard, as one message at a
	// step boundary (nil: nothing).
	Deliver func(t *DB, run *Run) *coordUpdate
	// Prompt is the block run's system prompt carries (coordAssemble builds
	// one from parts). Projects leaves it nil: projectPromptFor writes its
	// block, with its tasks' briefs.
	Prompt func(ag *Agent, run *Run, cfg Config) string
	// System is the base prompt of the profile's coordinator as this build
	// words it (nil or "": the one its config stored when it was made) — for
	// a conversation that outlives releases.
	System func(ag *Agent, run *Run, cfg Config) string
	// Knowledge is run's role in this profile's knowledge store — the
	// coordinator's, a research subagent's, anything else of the profile's
	// origin that reads or writes it. (nil, nil): run has none here.
	Knowledge func(ag *Agent, run *Run, cfg Config) (*wikiRole, error)
	// Specs is the profile's last word on its coordinator's tool list, after
	// the class's tools and its own were gathered (nil: as gathered) — a
	// core tool worded for what it coordinates. It adds no tool: a name it
	// returns that wasn't offered is dropped.
	Specs func(cfg Config, run *Run, specs []toolSpec) []toolSpec

	tools map[string]*coordTool
	order []string
}

// coordBounds bound what a coordinator starts.
type coordBounds struct {
	Research        int           // research subagents at once (per scope)
	ResearchTimeout time.Duration // a research subagent's time to answer
}

// coordWords is a profile's status words: a state of something the
// coordinator started, as the coordinator reads it.
type coordWords interface {
	Words(state string) string
}

// coordStates is coordWords from a table; a state it lacks reads as itself.
type coordStates map[string]string

func (m coordStates) Words(state string) string { return orStr(m[state], state) }

// coordUpdate is what a profile delivers to its coordinator: the message's
// text, its meta (origin and label), and the mark that records it
// delivered with the message's id.
type coordUpdate struct {
	Text string
	Meta msgMeta
	Mark func(msgID int64)
}

// coordProfiles is every registered profile, by id and by origin.
var coordProfiles = struct {
	byID     map[string]*coordProfile
	byOrigin map[string]*coordProfile
}{byID: map[string]*coordProfile{}, byOrigin: map[string]*coordProfile{}}

// registerCoordProfile adds p (at init: a mistake in a profile is a build
// that doesn't start, never a coordinator with a wider reach).
func registerCoordProfile(p *coordProfile) *coordProfile {
	switch {
	case p.ID == "" || p.Origin == "":
		panic("coordinator profile: an id and an origin are needed")
	case coordProfiles.byID[p.ID] != nil:
		panic("coordinator profile " + p.ID + ": registered twice")
	case coordProfiles.byOrigin[p.Origin] != nil:
		panic("coordinator profile " + p.ID + ": origin " + p.Origin + " is " + coordProfiles.byOrigin[p.Origin].ID + "'s")
	}
	p.tools = map[string]*coordTool{}
	coordProfiles.byID[p.ID] = p
	coordProfiles.byOrigin[p.Origin] = p
	return p
}

// coordProfileIDs lists the registered profiles' ids, sorted.
func coordProfileIDs() []string {
	ids := make([]string, 0, len(coordProfiles.byID))
	for id := range coordProfiles.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// coordOriginOf is the profile whose conversations have run's origin, for
// a top-level run that isn't hosted (nil: none) — a map lookup, before
// anything is read.
func coordOriginOf(run *Run) *coordProfile {
	if run == nil || run.ParentID != 0 || hostedID(run.ID) {
		return nil
	}
	return coordProfiles.byOrigin[run.Origin]
}

// coordProfileOf is the profile run is a coordinator of (nil: it is none).
func coordProfileOf(d *DB, run *Run) *coordProfile {
	p := coordOriginOf(run)
	if p == nil || p.Is == nil || !p.Is(d, run) {
		return nil
	}
	return p
}

// errCoordLane: a coordinator whose class has internal reach acts on
// nothing — it steers work that reaches outside.
var errCoordLane = errors.New("this coordinator's class has internal reach: a coordinator steers work that reaches outside, so it never holds internal reach — a manager should take internal reach off that class")

// errCoordNoClass: the profile's class is gone or has internal reach.
var errCoordNoClass = errors.New("the coordinator's class is missing or has internal reach")

// forcedClass is p's class as it stands now — errCoordNoClass when it is
// gone or an edit gave it internal reach: no coordinator is made in it.
func (p *coordProfile) forcedClass() (agentClass, error) {
	cls, ok := currentClasses().find(p.Class)
	if !ok || cls.has(tsInternal) {
		return agentClass{}, errCoordNoClass
	}
	return cls, nil
}

// forcedConfig is base as a coordinator of p holds it: in cls (its own
// system prompt, never the caller's), p.Deny and more denied on top of
// what base denies.
func (p *coordProfile) forcedConfig(base Config, cls agentClass, more ...string) Config {
	base.setClass(cls, false)
	base.Deny = append(append(append([]string(nil), base.Deny...), p.Deny...), more...)
	return base
}

// resolve is p.Resolve and then the lane, held by the kit whatever the
// profile checked: a coordinator in a class with internal reach acts on
// nothing.
func (p *coordProfile) resolve(ag *Agent, t *DB, run *Run, cfg Config) (any, error) {
	if p.Resolve == nil {
		return nil, fmt.Errorf("the %s coordinator has no tools in this build", p.ID)
	}
	scope, err := p.Resolve(ag, t, run, cfg)
	if err != nil {
		return nil, err
	}
	if classOf(cfg).has(tsInternal) {
		return nil, errCoordLane
	}
	return scope, nil
}

// stateWords is state in p's words.
func (p *coordProfile) stateWords(state string) string {
	if p.Words == nil {
		return state
	}
	return p.Words.Words(state)
}
