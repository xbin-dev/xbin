// coord_reader.go — what a research subagent reads code through
// (coord_research.go; D222): a codeReader. The tools a research subagent
// sees are defined here, against the interface — read, ls, glob and grep,
// and a read-only git when the reader's kind declares one — so the same
// subagent works over files in a sandbox with git (Projects:
// projects_coord_reader.go) and over a reader that lists and reads code
// some other way, with no sandbox and no git.
//
// A research subagent's config carries a CodeReadBinding (Config.CodeRead),
// built by the code that starts it, never from a call's arguments; its
// Kind names the reader. Whatever a model calls, a run with a binding gets
// only these tools and the knowledge store's (runCodeReadTool): no bash,
// write, edit, web, coding agents or subagents. What it reads comes back
// redacted; the reader keeps every path inside what the binding names.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// codeReader reads code for a research subagent. Each method takes the
// tool call's arguments as the model gave them (read: path, offset, limit;
// ls: path; glob: pattern, path; grep: pattern, path, glob, ignore_case;
// git: args — an argv, the subcommand first — and path) and answers the
// tool's text. A reader confines every path itself and never writes. Git
// is called only for a kind whose codeCaps declare it.
type codeReader interface {
	Read(ctx context.Context, args map[string]any) (string, error)
	Ls(ctx context.Context, args map[string]any) (string, error)
	Glob(ctx context.Context, args map[string]any) (string, error)
	Grep(ctx context.Context, args map[string]any) (string, error)
	Git(ctx context.Context, args map[string]any) (string, error)
}

// codeCaps is what a kind of reader can do beyond files.
type codeCaps struct {
	// Git is the read-only git subcommands it runs, as the tool lists them
	// (log, show, diff, blame, …); none: its subagents get no git tool.
	Git []string
}

// codeReaderKind is one kind of reader: what it declares, how its file
// tools are described, and how a run's binding is opened — again on every
// call, so a binding that no longer holds (its coordinator gone, its
// source no longer the scope's) reads nothing.
type codeReaderKind struct {
	Kind string // CodeReadBinding.Kind ("": a project's sandbox, the first kind)
	Caps codeCaps
	// What a research subagent of this kind reads, for the refusal of every
	// other tool ("the project's code and its wiki").
	What string
	// Files are its descriptions of the file tools; only read, ls, glob and
	// grep are taken from it.
	Files func() []toolSpec
	Open  func(ctx context.Context, ag *Agent, run *Run, cfg Config) (codeReader, error)
}

// codeReaderKinds is every registered kind, by CodeReadBinding.Kind.
var codeReaderKinds = map[string]*codeReaderKind{}

// registerCodeReader adds a kind (at init).
func registerCodeReader(k *codeReaderKind) {
	if codeReaderKinds[k.Kind] != nil {
		panic("code reader kind " + k.Kind + ": registered twice")
	}
	if k.Open == nil || k.Files == nil {
		panic("code reader kind " + k.Kind + ": Open and Files are needed")
	}
	codeReaderKinds[k.Kind] = k
}

// CodeReadBinding is Config.CodeRead: a research subagent's read-only reach
// into code. Set when it is made, never changed. Kind names its reader; the
// rest is that kind's own (a sandbox reader's: the project, the sandbox,
// where it starts and the roots every path must resolve inside).
type CodeReadBinding struct {
	Kind    string   `json:"kind,omitempty"`  // the reader (codeReaderKinds); "": a project's sandbox
	Project int64    `json:"project"`         // the project whose code it is
	Ref     string   `json:"ref"`             // the sandbox: the project's, or a task's own
	Cwd     string   `json:"cwd"`             // where it starts (paths resolve against it)
	Roots   []string `json:"roots"`           // every path must resolve inside one of these
	Repo    string   `json:"repo,omitempty"`  // owner/name, when it works in one repo
	Task    int64    `json:"task,omitempty"`  // a task's worktree
	Heads   []string `json:"heads,omitempty"` // "<repo> <sha>": what its checkouts are at
	// Checkout is its own directory of research checkouts (removed when it
	// answers), Bases the base repos they are worktrees of.
	Checkout string   `json:"checkout,omitempty"`
	Bases    []string `json:"bases,omitempty"`
	// A reader over a workspace tile's code (kind "tile", ws_research.go):
	// the tile, the org a note about it is stamped with, the revision it was
	// read at, and the notebook page its answer is saved as ("": none).
	Tile string `json:"tile,omitempty"`
	Org  string `json:"org,omitempty"`
	Rev  string `json:"rev,omitempty"`
	Save string `json:"save,omitempty"`
}

// codeFileTools are the file tools of the surface; codeReadTools everything
// a research subagent may call beside the knowledge store's tools.
var (
	codeFileTools = map[string]bool{"read": true, "ls": true, "glob": true, "grep": true}
	codeReadTools = map[string]bool{"read": true, "ls": true, "glob": true, "grep": true, "git": true, "note": true, "message_get": true}
)

// codeWords is words as a list in a sentence: joined by sep, the last one
// after last ("log, show or diff").
func codeWords(words []string, sep, last string) string {
	if len(words) == 0 {
		return ""
	}
	if len(words) == 1 {
		return words[0]
	}
	return strings.Join(words[:len(words)-1], sep) + last + words[len(words)-1]
}

// codeGitSpec is the git tool for a reader that runs subs.
func codeGitSpec(subs []string) toolSpec {
	desc := "Run a read-only git command in the code you are reading: " + codeWords(subs, ", ", " or ") + " — argv only, no shell. " +
		"Each subcommand takes its read-only options by exact name, one flag per argument, a long option's value attached (--date=iso); paths stay relative to the repository."
	var forms []string
	if hasStr(subs, "diff") {
		forms = append(forms, "diff compares commits (A B, A..B) or the index (--cached)")
	}
	if hasStr(subs, "blame") {
		forms = append(forms, "blame names its revision ([\"blame\", \"HEAD\", \"--\", \"file\"])")
	}
	if len(forms) > 0 {
		desc += " " + strings.Join(forms, "; ") + "."
	}
	return toolSpec{Type: "function", Function: funcDef{Name: "git",
		Description: desc,
		Parameters: obj([]string{"args"}, map[string]any{
			"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1,
				"description": "the subcommand and its arguments, e.g. [\"log\", \"--oneline\", \"-20\", \"--\", \"src/\"]"},
			"path": strProp("the repository's directory (default: the working directory)"),
		})}}
}

// codeReadToolSpecs is a research subagent's whole tool list: a note and
// finish, its reader's file tools, git when the reader has one, and the
// knowledge store's tools.
func codeReadToolSpecs(cfg Config, run *Run) []toolSpec {
	specs := []toolSpec{
		{Type: "function", Function: funcDef{Name: "note",
			Description: "Record a short visible note in the run timeline (for the human watching). No effect on control flow.",
			Parameters:  obj([]string{"text"}, map[string]any{"text": strProp("the note")})}},
		finishSpec(run.Depth),
	}
	if kind := codeReaderKinds[cfg.CodeRead.Kind]; kind != nil {
		for _, s := range kind.Files() {
			if codeFileTools[s.Function.Name] {
				specs = append(specs, s)
			}
		}
		if len(kind.Caps.Git) > 0 {
			specs = append(specs, codeGitSpec(kind.Caps.Git))
		}
	}
	specs = append(specs, wikiToolSpecs(true)...)
	kept := specs[:0:0]
	for _, s := range specs {
		if !cfg.denied(s.Function.Name) {
			kept = append(kept, s)
		}
	}
	return kept
}

// runCodeReadTool is runTool for a research subagent: its tools only.
func (ag *Agent) runCodeReadTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	kind := codeReaderKinds[cfg.CodeRead.Kind]
	what := "the code it was given"
	if kind != nil {
		what = kind.What
	}
	if (!codeReadTools[name] && !wikiToolNames[name]) || (name == "git" && kind != nil && len(kind.Caps.Git) == 0) {
		return "", fmt.Errorf("%s is not available to a research subagent: it reads %s, and changes nothing else", name, what)
	}
	if limit := researchTimeoutOf(run); run.Created > 0 && time.Since(time.Unix(run.Created, 0)) > limit {
		return "", fmt.Errorf("your %d minutes are up: answer now with what you found, and what you could not determine", int(limit/time.Minute))
	}
	switch name {
	case "note":
		return "noted", nil
	case "message_get":
		return ag.toolMessageGet(run, args)
	}
	if wikiToolNames[name] {
		return ag.runWikiTool(ctx, run, cfg, name, args)
	}
	if kind == nil {
		return "", fmt.Errorf("this build has no reader for the code you were given (%q): tell the coordinator", clip(cfg.CodeRead.Kind, 40))
	}
	rd, err := kind.Open(ctx, ag, run, cfg)
	if err != nil {
		return "", err
	}
	// a glob is matched under path: one that is absolute, at a home or climbing out would move it
	if why := codeReadPattern(name+"'s pattern", str(args["pattern"])); name == "glob" && why != "" {
		return "", errors.New(why)
	}
	if why := codeReadPattern("glob", str(args["glob"])); name == "grep" && why != "" {
		return "", errors.New(why)
	}
	var out string
	switch name {
	case "read":
		out, err = rd.Read(ctx, args)
	case "ls":
		out, err = rd.Ls(ctx, args)
	case "glob":
		out, err = rd.Glob(ctx, args)
	case "grep":
		out, err = rd.Grep(ctx, args)
	case "git":
		out, err = rd.Git(ctx, args)
	}
	if err != nil {
		return "", err
	}
	return projRedact(out), nil
}

// codeReadPattern says why a glob pattern may not be used ("" = it may):
// one that is absolute, starts at a home or climbs out with .. would look
// outside the directory it is given.
func codeReadPattern(what, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return fmt.Sprintf("%s: an absolute or home pattern — give path for where to look, and a pattern relative to it", what)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Sprintf("%s: .. isn't taken in a pattern — give path for where to look", what)
		}
	}
	return ""
}
