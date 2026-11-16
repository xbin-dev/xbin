package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeReader is a codeReader with no sandbox and no git behind it: it
// records what it was asked and answers from a map of files.
type fakeReader struct {
	files map[string]string
	calls *[]string
}

func (r fakeReader) note(tool string, args map[string]any) {
	b, _ := json.Marshal(args)
	*r.calls = append(*r.calls, tool+" "+string(b))
}

func (r fakeReader) Read(_ context.Context, args map[string]any) (string, error) {
	r.note("read", args)
	body, ok := r.files[str(args["path"])]
	if !ok {
		return "", fmt.Errorf("%s isn't in the tile's code", str(args["path"]))
	}
	return body, nil
}

func (r fakeReader) Ls(_ context.Context, args map[string]any) (string, error) {
	r.note("ls", args)
	var names []string
	for n := range r.files {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, "\n"), nil
}

func (r fakeReader) Glob(_ context.Context, args map[string]any) (string, error) {
	r.note("glob", args)
	return "main.go", nil
}

func (r fakeReader) Grep(_ context.Context, args map[string]any) (string, error) {
	r.note("grep", args)
	return "main.go:1: package main", nil
}

func (r fakeReader) Git(_ context.Context, args map[string]any) (string, error) {
	r.note("git", args)
	return "abc1234 first", nil
}

// fakeReaderKind registers a reader kind for a test and takes it out again.
func fakeReaderKind(t *testing.T, kind string, git []string, calls *[]string) {
	t.Helper()
	registerCodeReader(&codeReaderKind{Kind: kind, Caps: codeCaps{Git: git}, What: "the tile's code and the notebook",
		Files: func() []toolSpec {
			specs := sandboxFileSpecs() // write and edit are in there: the surface must leave them out
			for i := range specs {
				specs[i].Function.Description = "(remote) " + specs[i].Function.Description
			}
			return specs
		},
		Open: func(context.Context, *Agent, *Run, Config) (codeReader, error) {
			return fakeReader{calls: calls, files: map[string]string{
				"main.go": "package main\n// token sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"}}, nil
		}})
	t.Cleanup(func() { delete(codeReaderKinds, kind) })
}

// researchChild makes a research subagent of coord by hand, reading
// through kind.
func researchChild(t *testing.T, fx *coordFix, coord *Run, kind string) (*Run, Config) {
	t.Helper()
	pcfg, _ := fx.ag.db.runConfig(coord.ID)
	child := childConfig(pcfg, "You are a research subagent.")
	child.CodeRead = &CodeReadBinding{Kind: kind, Project: fx.p.ID, Cwd: "/"}
	child.Project = &ProjectRef{ID: fx.p.ID, Role: projRoleResearch}
	b, _ := json.Marshal(child)
	id, err := fx.ag.db.createRunStamped("research: probe", string(b), coord.ID, statusRunning, runStamp{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := fx.ag.db.getRun(id)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := fx.ag.db.runConfig(id)
	if err != nil || cfg.CodeRead == nil || cfg.CodeRead.Kind != kind {
		t.Fatalf("the child's stored binding: %+v %v", cfg.CodeRead, err)
	}
	return run, cfg
}

// The research subagent is defined against codeReader, not a sandbox: a
// reader that is neither a sandbox nor has git runs the same file tools and
// the knowledge store's, gets no git tool, has its output redacted and its
// glob patterns checked before it is asked, and is held to the profile's
// time — and every other tool is refused whatever the run's class, as for
// the sandbox reader. A kind that declares git gets the git tool for the
// subcommands it names.
func TestCodeReaderContract(t *testing.T) {
	fx := newCoordFix(t)
	coord := fx.coordinator(t, asAlice, fx.p.ID)
	var calls []string
	fakeReaderKind(t, "fake", nil, &calls)
	fakeReaderKind(t, "fake-git", []string{"log", "show"}, &calls)
	run, cfg := researchChild(t, fx, coord, "fake")
	ctx := context.Background()

	// the surface: the file tools, the knowledge store's, note and finish —
	// the same names the sandbox reader's subagent gets, but for git
	names := func(cfg Config, run *Run) string {
		var out []string
		for n := range toolNamesIn(runToolSpecs(cfg, run, nil)) {
			out = append(out, n)
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	sbxRun, sbxCfg := researchChild(t, fx, coord, "")
	withGit, gitCfg := researchChild(t, fx, coord, "fake-git")
	want := "finish glob grep ls note read wiki_archive wiki_edit wiki_history wiki_list wiki_read wiki_search wiki_write"
	if got := names(cfg, run); got != want {
		t.Fatalf("a research subagent of a reader without git is offered:\n%s\nwant:\n%s", got, want)
	}
	wantGit := strings.Replace(want, "glob grep", "git glob grep", 1)
	if got := names(sbxCfg, sbxRun); got != wantGit {
		t.Fatalf("the sandbox reader's subagent is offered:\n%s\nwant:\n%s", got, wantGit)
	}
	if got := names(gitCfg, withGit); got != wantGit {
		t.Fatalf("a reader with git: its subagent is offered:\n%s", got)
	}
	for _, s := range runToolSpecs(gitCfg, withGit, nil) {
		switch s.Function.Name {
		case "git":
			if d := s.Function.Description; !strings.Contains(d, "log or show — argv only") || strings.Contains(d, "blame") || strings.Contains(d, "diff") {
				t.Errorf("git for a reader of log and show: %q", d)
			}
		case "read":
			if !strings.HasPrefix(s.Function.Description, "(remote) ") {
				t.Errorf("read isn't described by its reader's kind: %q", s.Function.Description)
			}
		}
	}

	// the same tools run, through the reader
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"read": {map[string]any{"path": "main.go"}, "package main"},
		"ls":   {map[string]any{}, "main.go"},
		"glob": {map[string]any{"pattern": "**/*.go"}, "main.go"},
		"grep": {map[string]any{"pattern": "package", "glob": "*.go"}, "main.go:1:"},
	} {
		out, err := fx.ag.runTool(ctx, run, cfg, name, c.args)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%s through the fake reader: %q %v", name, out, err)
		}
	}
	if len(calls) != 4 {
		t.Fatalf("the reader was asked %d times: %v", len(calls), calls)
	}
	if out, _ := fx.ag.runTool(ctx, run, cfg, "read", map[string]any{"path": "main.go"}); strings.Contains(out, "sk-ant-api03-AAAA") {
		t.Fatalf("what the reader answered wasn't redacted: %q", out)
	}
	if _, err := fx.ag.runTool(ctx, run, cfg, "read", map[string]any{"path": "nope.go"}); err == nil || !strings.Contains(err.Error(), "isn't in the tile's code") {
		t.Fatalf("the reader's own refusal: %v", err)
	}
	// the kit checks a pattern before any reader sees it
	calls = nil
	if _, err := fx.ag.runTool(ctx, run, cfg, "glob", map[string]any{"pattern": "/etc/*"}); err == nil || !strings.Contains(err.Error(), "absolute or home pattern") {
		t.Fatalf("an absolute glob: %v", err)
	}
	if _, err := fx.ag.runTool(ctx, run, cfg, "grep", map[string]any{"pattern": "x", "glob": "../../*"}); err == nil || !strings.Contains(err.Error(), ".. isn't taken") {
		t.Fatalf("a climbing grep glob: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("the reader was asked for a refused pattern: %v", calls)
	}
	// the knowledge store's tools are the same ones
	if out, err := fx.ag.runTool(ctx, run, cfg, "wiki_write", map[string]any{"slug": "layout", "body": "main.go is the entry (read in main.go:1)", "note": "read it"}); err != nil ||
		!strings.Contains(out, "wrote layout") {
		t.Fatalf("wiki_write from the fake reader's subagent: %q %v", out, err)
	}
	// everything else is refused, git included for a reader without it
	for _, name := range []string{"git", "bash", "write", "edit", "web_fetch", "subagent_spawn", "task_create", "research", "xbin_call", "file_write"} {
		_, err := fx.ag.runTool(ctx, run, cfg, name, map[string]any{"args": []any{"log"}, "path": "main.go"})
		if err == nil || !(strings.Contains(err.Error(), "is not available to a research subagent: it reads the tile's code and the notebook") ||
			strings.Contains(err.Error(), "is not available in this conversation")) {
			t.Errorf("%s from a research subagent: %v", name, err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("a refused tool reached the reader: %v", calls)
	}
	if out, err := fx.ag.runTool(ctx, withGit, gitCfg, "git", map[string]any{"args": []any{"log", "--oneline"}}); err != nil || !strings.Contains(out, "abc1234") {
		t.Fatalf("git through a reader that has it: %q %v", out, err)
	}
	// a kind this build doesn't have reads nothing, and says so
	lost, lostCfg := researchChild(t, fx, coord, "gone")
	if _, err := fx.ag.runTool(ctx, lost, lostCfg, "read", map[string]any{"path": "main.go"}); err == nil || !strings.Contains(err.Error(), "no reader for the code") {
		t.Fatalf("a binding of an unknown kind: %v", err)
	}
	if _, err := fx.ag.runTool(ctx, lost, lostCfg, "bash", map[string]any{"command": "id"}); err == nil || !strings.Contains(err.Error(), "not available to a research subagent") {
		t.Fatalf("bash on a binding of an unknown kind: %v", err)
	}
	if got := toolNamesIn(runToolSpecs(lostCfg, lost, nil)); got["read"] || got["grep"] || got["git"] || got["bash"] {
		t.Fatalf("a binding of an unknown kind is offered: %s", names(lostCfg, lost))
	}
	// the profile's time: after it, the tools stop answering
	late := time.Now().Add(-coordProject.Limits.ResearchTimeout - time.Minute).Unix()
	if _, err := fx.ag.db.q.Exec(`UPDATE runs SET created=? WHERE id=?`, late, run.ID); err != nil {
		t.Fatal(err)
	}
	old, _ := fx.ag.db.getRun(run.ID)
	if _, err := fx.ag.runTool(ctx, old, cfg, "read", map[string]any{"path": "main.go"}); err == nil || !strings.Contains(err.Error(), "your 10 minutes are up") {
		t.Fatalf("a research subagent past its time: %v", err)
	}
}
