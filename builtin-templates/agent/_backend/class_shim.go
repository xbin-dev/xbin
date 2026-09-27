// class_shim.go — TEMPORARY: replaced by classes.go at integration.
//
// The classes track (D116) defines agent classes in classes.go. This shim
// implements exactly the interface the sandbox binding relies on — agentClass,
// classOf, has, allowsManager, allowsEgress — with the built-in classes only,
// so this branch builds and tests on its own. The integrator deletes it; code
// outside this file must use nothing else from it (not classSet's fields).
package main

import "strings"

// classSet is "all" or a list of names.
type classSet struct {
	all  bool
	list []string
}

func (s classSet) includes(name string) bool {
	if s.all {
		return true
	}
	for _, x := range s.list {
		if x == name {
			return true
		}
	}
	return false
}

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

var shimClasses = map[string]agentClass{
	"internal": {ID: "internal", Name: "Internal", Icon: "🔒",
		Toolsets: []string{"files", "repl", "internal", "subagents", "schedule", "threads", "skills"}, MCP: classSet{all: true}},
	"web": {ID: "web", Name: "Web", Icon: "🌐",
		Toolsets: []string{"files", "repl", "web", "subagents", "schedule", "skills"}},
	"coding": {ID: "coding", Name: "Coding", Icon: "▣",
		Toolsets: []string{"sandbox", "web", "files", "subagents", "skills"}, Managers: classSet{all: true},
		SandboxEgress: []string{"none", "internet"}},
}

// classOf never fails: cfg.Class → the stored class; unknown/"" → from
// cfg.Toolset → a built-in.
func classOf(cfg Config) agentClass {
	if c, ok := shimClasses[cfg.Class]; ok {
		return c
	}
	if cfg.Toolset == "web" {
		return shimClasses["web"]
	}
	return shimClasses["internal"]
}

func (c agentClass) has(toolset string) bool {
	for _, t := range c.Toolsets {
		if t == toolset {
			return true
		}
	}
	return false
}

// allowsManager: a provider "apps/x#inst" is allowed by "apps/x" too.
func (c agentClass) allowsManager(provider string) bool {
	if !c.has("sandbox") {
		return false
	}
	tile, _, _ := strings.Cut(provider, "#")
	return c.Managers.includes(provider) || c.Managers.includes(tile)
}

func (c agentClass) allowsEgress(egress string) bool {
	if !c.has("sandbox") {
		return false
	}
	for _, e := range c.SandboxEgress {
		if e == egress {
			return true
		}
	}
	return false
}
