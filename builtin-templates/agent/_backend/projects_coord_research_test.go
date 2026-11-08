package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// research's git takes read-only subcommands and refuses what writes files,
// runs programs or leaves the repository — abbreviations included.
func TestResearchGitArgs(t *testing.T) {
	for _, bad := range [][]string{{"log", "-c", "x"}, {"log", "-C"}, {"log", "--output=/tmp/x"}, {"log", "--out=/tmp/x"},
		{"diff", "--no-index", "/etc/passwd", "x"}, {"grep", "-O", "x"}, {"grep", "-iO", "x"}, {"grep", "-f", "/etc/passwd"},
		{"push"}, {"config", "x"}, {"-c", "x=y", "log"}, {"blame", "--contents", "/etc/passwd", "x"}, {"show", "--ext-diff"},
		{"show", "--textconv"}, {"log", "--exec=sh"}, {"ls-files", "-X", "/etc/passwd"}, {"diff", "--git-dir=/x"}, {}} {
		if gitArgsRefused(bad) == "" {
			t.Errorf("git %q was taken", bad)
		}
	}
	for _, ok := range [][]string{{"log", "--oneline", "-5"}, {"show", "HEAD:README.md"}, {"grep", "-n", "hello"}, {"ls-files"},
		{"status", "--short"}, {"log", "--", "--output"}, {"diff", "--stat", "HEAD~1"}, {"blame", "-L", "1,5", "README.md"}, {"log", "--no-textconv"}} {
		if why := gitArgsRefused(ok); why != "" {
			t.Errorf("git %q was refused: %s", ok, why)
		}
	}
}

// The coordinator's research: a subagent in the coordinator's class and
// lane that reads the project's code (a checkout of origin's default
// branch) and writes the wiki — and nothing else, whatever it calls:
// write, edit and bash are refused, git's -c and --output too, a path
// outside the project's directory, the web, subagents.
func TestResearchReadOnly(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	pwned := filepath.Join(t.TempDir(), "pwned")
	f := fakeOf(fx.ag)
	isResearch := func(r LLMRequest) bool {
		return len(r.Msgs) > 0 && strings.Contains(asString(r.Msgs[0].Content), "research subagent of the project")
	}
	f.on(func(r LLMRequest) bool { return isResearch(r) && lastIs("user", "")(r) }, callTools(
		tc("c1", "write", `{"path":"x.txt","content":"no"}`),
		tc("c2", "bash", `{"command":"touch y"}`),
		tc("c3", "edit", `{"path":"README.md","old_string":"web","new_string":"pwned"}`),
		tc("c4", "git", fmt.Sprintf(`{"args":["log","--output=%s"]}`, pwned)),
		tc("c5", "git", `{"args":["log","-c","core.pager=sh"]}`),
		tc("c6", "read", `{"path":"README.md"}`),
		tc("c7", "git", `{"args":["log","--oneline"]}`),
		tc("c8", "read", `{"path":"/etc/hostname"}`),
		tc("c9", "web_fetch", `{"url":"https://example.com"}`),
		tc("c10", "subagent_spawn", `{"task":"x"}`),
		tc("c11", "wiki_write", `{"slug":"readme","body":"README.md is one heading: `+"`# web`"+`.","note":"read it"}`),
		tc("c12", "grep", `{"pattern":"web"}`),
	)).once()
	f.on(func(r LLMRequest) bool { return isResearch(r) && lastIs("tool", "")(r) }, say("README.md says: # web")).once()
	f.on(func(r LLMRequest) bool {
		return !isResearch(r) && lastUser("what does the readme say")(r) && lastIs("user", "")(r)
	},
		callTools(tc("r1", "research", `{"question":"What does README.md say?","label":"readme"}`))).once()
	f.on(func(r LLMRequest) bool { return !isResearch(r) && lastIs("tool", "README.md says")(r) }, say("it says # web")).once()
	w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/coordinator", p.ID), map[string]any{"text": "what does the readme say"})
	if w.Code != 200 {
		t.Fatalf("POST coordinator: %d %s", w.Code, w.Body)
	}
	coord := fx.ag.db.coordRunOf(p.ID, "alice")
	t.Cleanup(func() {
		if t.Failed() {
			dumpRuns(t, fx.ag.db)
		}
	})
	hwait(t, "the coordinator's answer", func() bool {
		r, _ := fx.ag.db.getRun(coord)
		return r != nil && r.Status == statusIdle && strings.Contains(fx.ag.db.lastAssistant(coord), "it says # web")
	})
	kids := scanIDs(fx.ag.db.q.Query(`SELECT id FROM runs WHERE parent_id=?`, coord))
	if len(kids) != 1 {
		t.Fatalf("the coordinator's children: %v", kids)
	}
	child, _ := fx.ag.db.getRun(kids[0])
	ccfg, _ := fx.ag.db.runConfig(child.ID)
	pcfg, _ := fx.ag.db.runConfig(coord)
	if ccfg.CodeRead == nil || ccfg.CodeRead.Project != p.ID || classOf(ccfg).ID != classOf(pcfg).ID || ccfg.toolset() != pcfg.toolset() ||
		classOf(ccfg).has(tsInternal) || ccfg.Subagents || ccfg.Sandbox != nil {
		t.Fatalf("the child's config: %+v (class %s, parent's %s)", ccfg.CodeRead, classOf(ccfg).ID, classOf(pcfg).ID)
	}
	pp, _ := fx.ag.db.getProject(p.ID)
	if want := researchDir(pp) + "/web"; ccfg.CodeRead.Cwd != want {
		t.Fatalf("its cwd %q, want %q", ccfg.CodeRead.Cwd, want)
	}
	got := runSpecNames(ccfg, child)
	for _, n := range []string{"bash", "write", "edit", "web_fetch", "web_search", "subagent_spawn", "research", "task_create", "xbin_call"} {
		if got[n] {
			t.Errorf("the child is offered %s", n)
		}
	}
	for _, n := range []string{"read", "grep", "git", "wiki_write", "finish"} {
		if !got[n] {
			t.Errorf("the child lacks %s", n)
		}
	}
	results := map[string]string{}
	msgs, _ := fx.ag.db.messages(child.ID, false)
	for _, m := range msgs {
		if m.Role == "tool" {
			results[m.ToolCallID] = m.Content
		}
	}
	for id, want := range map[string]string{"c1": "not available to a research subagent", "c2": "not available to a research subagent",
		"c3": "not available to a research subagent", "c4": "refused", "c5": "refused", "c6": "# web", "c7": "first",
		"c8": "outside the project's code", "c9": "error", "c10": "error", "c11": "created readme", "c12": "README.md"} {
		if !strings.Contains(results[id], want) {
			t.Errorf("%s: %q, want %q", id, results[id], want)
		}
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("git wrote a file outside the repository")
	}
	if _, err := os.Stat(filepath.Join(ccfg.CodeRead.Cwd, "x.txt")); err == nil {
		t.Fatal("the child wrote a file")
	}
	if b, _ := os.ReadFile(filepath.Join(ccfg.CodeRead.Cwd, "README.md")); string(b) != "# web\n" {
		t.Fatalf("the checkout changed: %q", b)
	}
	pg, err := fx.ag.db.wikiPage(p.ID, "readme")
	if err != nil || !strings.HasPrefix(pg.UpdatedBy, fmt.Sprintf("research #%d", child.ID)) {
		t.Fatalf("the child's page: %+v %v", pg, err)
	}
	// at most researchMax at once
	for i := 0; i < researchMax; i++ {
		if _, err := fx.ag.db.q.Exec(`INSERT INTO runs (title, status, config, parent_id, root_id, depth, created, updated, origin, origin_id)
			VALUES ('r', 'running', ?, ?, ?, 1, 0, 0, ?, ?)`, fmt.Sprintf(`{"codeRead":{"project":%d}}`, p.ID), coord, coord, originProject, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := fx.ag.db.researchRunning(p.ID); n != researchMax {
		t.Fatalf("running: %d", n)
	}
}

func dumpRuns(t *testing.T, d *DB) {
	for _, id := range scanIDs(d.q.Query(`SELECT id FROM runs ORDER BY id`)) {
		r, err := d.getRun(id)
		if err != nil {
			continue
		}
		t.Logf("run #%d parent %d %s %q %q", r.ID, r.ParentID, r.Status, clip(r.Result, 200), r.Title)
		msgs, _ := d.messages(id, false)
		for _, m := range msgs {
			t.Logf("   %s %s %s", m.Role, clip(m.Content, 300), clip(m.ToolCalls, 300))
		}
	}
}
