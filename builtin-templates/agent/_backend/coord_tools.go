// coord_tools.go — a coordinator's tools, by profile (coord_profile.go):
// each profile registers its tools at init, and only under the names on
// its allowlist. They are offered at depth 0 only, to a run its profile
// says is one of its coordinators (no new toolset: the way cfg.Channel
// gates attach_to_reply), and dispatched from runTool beside the thread
// tools — or, for a tool that waits as a subagent does (research), from
// execTools. Every call resolves the coordinator again (the profile's
// Resolve, then the lane), so a handler never sees a run that isn't a live
// coordinator of its profile: a tool called by anything else answers the
// refusal of the profile that owns it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// coordTool is one tool of a profile: Run for a tool that answers at once,
// Engine for one that needs the turn (it settles its own result). Write: it
// starts or steers work, so approval mode asks the person first.
type coordTool struct {
	Spec   toolSpec
	Write  bool
	Run    func(ctx context.Context, ag *Agent, c coordCall) (string, error)
	Engine func(e *Engine, ctx context.Context, ts *turnState, tc toolCall)
}

// coordCall is one call of a coordinator's tool: the run and its config as
// stored, what its profile resolved it to (Scope), and the arguments.
type coordCall struct {
	Run   *Run
	Cfg   Config
	Scope any
	Args  map[string]any
}

// The tools of every profile, by name: coordToolNames run through runTool,
// coordEngineTools through execTools, coordWrites change something
// (sideEffect). A name two profiles share is one kind of tool in both.
var (
	coordToolNames   = map[string]bool{}
	coordEngineTools = map[string]bool{}
	coordWrites      = map[string]bool{}
	coordToolOwners  = map[string][]*coordProfile{}
)

// register adds tools to p, in the order they are offered. Refused: a
// name that isn't on p's allowlist, one p has already, a tool with no
// handler or with both, and a name another profile registered as another
// kind of tool (a write there and a read here; run by the engine there and
// by runTool here) — what a name means to approval mode and to the loop
// doesn't depend on who calls it.
func (p *coordProfile) register(tools ...coordTool) error {
	for _, t := range tools {
		name := t.Spec.Function.Name
		switch {
		case name == "":
			return fmt.Errorf("coordinator profile %s: a tool needs a name", p.ID)
		case !hasStr(p.Allow, name):
			return fmt.Errorf("coordinator profile %s: %s is not on its allowlist", p.ID, name)
		case p.tools[name] != nil:
			return fmt.Errorf("coordinator profile %s: %s is registered twice", p.ID, name)
		case (t.Run == nil) == (t.Engine == nil):
			return fmt.Errorf("coordinator profile %s: %s needs one handler (Run or Engine)", p.ID, name)
		}
		if len(coordToolOwners[name]) > 0 && (coordWrites[name] != t.Write || coordEngineTools[name] != (t.Engine != nil)) {
			return fmt.Errorf("coordinator profile %s: %s is another kind of tool in profile %s", p.ID, name, coordToolOwners[name][0].ID)
		}
		tool := t
		p.tools[name] = &tool
		p.order = append(p.order, name)
		coordToolOwners[name] = append(coordToolOwners[name], p)
		if t.Engine != nil {
			coordEngineTools[name] = true
		} else {
			coordToolNames[name] = true
		}
		if t.Write {
			coordWrites[name] = true
		}
	}
	return nil
}

// mustRegister is register at init: a tool off the allowlist is a build
// that doesn't start.
func (p *coordProfile) mustRegister(tools ...coordTool) {
	if err := p.register(tools...); err != nil {
		panic(err)
	}
}

// specs are p's tools as offered, in their order.
func (p *coordProfile) specs() []toolSpec {
	out := make([]toolSpec, 0, len(p.order))
	for _, name := range p.order {
		out = append(out, p.tools[name].Spec)
	}
	return out
}

// coordToolsFor is what runToolSpecs adds for run: its profile's tools for
// a coordinator at depth 0, none otherwise.
func coordToolsFor(cfg Config, run *Run) []toolSpec {
	if run == nil || run.Depth != 0 || coordOriginOf(run) == nil {
		return nil
	}
	ag := projAg()
	if ag == nil || !ag.db.features {
		return nil
	}
	p := coordProfileOf(ag.db, run)
	if p == nil {
		return nil
	}
	var out []toolSpec
	for _, s := range p.specs() {
		if !cfg.denied(s.Function.Name) {
			out = append(out, s)
		}
	}
	return out
}

// coordToolOf is the tool name for run and the profile that answers for
// it: run's own when it has the tool; else the profile that owns the name
// — whose Resolve refuses a run that is none of its coordinators, in its
// own words. nil: no profile has that tool.
func coordToolOf(d *DB, run *Run, name string) (*coordProfile, *coordTool) {
	if p := coordProfileOf(d, run); p != nil && p.tools[name] != nil {
		return p, p.tools[name]
	}
	if owners := coordToolOwners[name]; len(owners) > 0 {
		return owners[0], owners[0].tools[name]
	}
	return nil, nil
}

// runCoordTool runs one of a coordinator's tools, after resolving the
// coordinator again.
func (ag *Agent) runCoordTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	p, tool := coordToolOf(ag.db, run, name)
	if tool == nil || tool.Run == nil {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	scope, err := p.resolve(ag, ag.db, run, cfg)
	if err != nil {
		return "", err
	}
	return tool.Run(ctx, ag, coordCall{Run: run, Cfg: cfg, Scope: scope, Args: args})
}

// coordEngineTool runs a coordinator's tool that needs the turn (execTools);
// the tool resolves the coordinator itself and settles its result.
func (e *Engine) coordEngineTool(ctx context.Context, ts *turnState, tc toolCall) {
	_, tool := coordToolOf(e.db, ts.run, tc.Function.Name)
	if tool == nil || tool.Engine == nil {
		e.settleResult(ts, tc, fmt.Sprintf("error: unknown tool %q", tc.Function.Name))
		return
	}
	tool.Engine(e, ctx, ts, tc)
}

// coordArgs decodes a tool call's arguments into v.
func coordArgs(args map[string]any, v any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("the arguments don't fit the tool's parameters: %v", err)
	}
	return nil
}

func coordIntProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func coordIntsProp(desc string, max int) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "minItems": 1, "maxItems": max, "description": desc}
}
