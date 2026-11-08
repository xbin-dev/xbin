package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// research's git: a subcommand's read-only options by exact name, one flag
// an argument, values that can't pass for options; revisions and paths
// inside the repository; diff between commits or against the index, blame
// at a revision. The review's bypasses: a pattern of "--" that let the
// option check stop early, -O and --open-files-in-pager after it, an
// outside file to diff (git's implicit --no-index), abbreviations.
func TestResearchGitArgs(t *testing.T) {
	roots := []string{"/w/web"}
	dir := "/w/web/.xbin/research/r-1/web"
	for _, bad := range [][]string{
		{"grep", "-e", "--", "--open-files-in-pager=touch ../pwned;true"},
		{"grep", "-e", "--", "-e", "web", "--open-files-in-pager=touch /x/pwned;true"},
		{"grep", "-e", "--", "-Otouch ../pwned;true"},
		{"grep", "-Otouch", "x"}, {"grep", "-O"}, {"grep", "--open-files-in-pager=x"}, {"grep", "--open=x"}, {"grep", "-iO", "x"},
		{"grep", "-f", "/etc/passwd"}, {"grep", "-e", "-Ox"}, {"grep", "--no-index", "x"},
		{"diff", "../outside.txt", "/dev/null"}, {"diff", "/home/user/.git-credentials", "/dev/null"}, {"diff", "--no-index", "a", "b"},
		{"diff"}, {"diff", "HEAD"}, {"diff", "--", "README.md"}, {"diff", "HEAD", "HEAD~1", "--", "/etc/passwd"},
		{"log", "-c", "x"}, {"log", "-C"}, {"log", "--output=/tmp/x"}, {"log", "--out=/tmp/x"}, {"log", "--exec=sh"},
		{"log", "--show-signature"}, {"log", "--format", "x"}, {"log", "--", "../../../../.."}, {"grep", "-e", "--"}, {"log", ":(top)x"}, {"show", ":/fix"},
		{"show", "~/x"}, {"show", "--ext-diff"}, {"show", "--textconv"}, {"push"}, {"config", "x"}, {"status"}, {"-c", "x=y", "log"},
		{"blame", "--contents", "/etc/passwd", "x"}, {"blame", "README.md"}, {"blame", "-S", "revs", "HEAD", "--", "x"},
		{"ls-files", "-X", "/etc/passwd"}, {"ls-files", "-m"}, {"log", "-in"}, {}} {
		if _, err := gitArgv(bad, dir, roots); err == nil {
			t.Errorf("git %q was taken", bad)
		}
	}
	for _, ok := range [][]string{{"log", "--oneline", "-5"}, {"log", "-n", "5", "--", "src/"}, {"log", "--date=iso", "--format=%h %s"},
		{"show", "HEAD:README.md"}, {"show", "--stat", "HEAD~2"}, {"grep", "-n", "hello"}, {"grep", "-n", "-e-x", "HEAD", "--", "src"},
		{"grep", "-e--open-files-in-pager=x"}, {"log", "--", "../.."}, {"ls-files", "--others"}, {"diff", "--stat", "HEAD~1", "HEAD"}, {"diff", "HEAD~3..HEAD"}, {"diff", "--cached"},
		{"blame", "-L", "1,5", "HEAD", "--", "README.md"}, {"log", "-L1,5:README.md"}} {
		argv, err := gitArgv(ok, dir, roots)
		if err != nil {
			t.Errorf("git %q was refused: %v", ok, err)
			continue
		}
		line := strings.Join(argv, " ")
		for _, want := range []string{"--no-pager", "core.fsmonitor=false", "core.hooksPath=/dev/null", "core.attributesFile=/dev/null", "log.showSignature=false"} {
			if !strings.Contains(line, want) {
				t.Errorf("git %q runs without %s: %s", ok, want, line)
			}
		}
		if (ok[0] == "log" || ok[0] == "show" || ok[0] == "diff") && !strings.Contains(line, "--no-ext-diff --no-textconv") {
			t.Errorf("git %q runs with external diffs or textconv: %s", ok, line)
		}
	}
}

// The coordinator's research: a subagent in the coordinator's class and
// lane that reads the project's code (a checkout of origin's default
// branch) and writes the wiki — and nothing else, whatever it calls:
// write, edit and bash are refused, git's -c and --output too, a path
// outside the project's directory, the web, subagents — and the review's
// escapes: git grep opening "files in a pager" (a program) behind a
// pattern of "--", git diff of a file outside (an implicit --no-index), a
// glob or grep's glob reaching outside. Its own checkout goes when it
// answers.
func TestResearchReadOnly(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	pwned := filepath.Join(t.TempDir(), "pwned")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("TOPSECRET-OUTSIDE-ROOTS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonArgs := func(a ...string) string { b, _ := json.Marshal(a); return string(b) }
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
		tc("e1", "git", `{"args":`+jsonArgs("grep", "-e", "--", "-e", "web", "--open-files-in-pager=touch "+pwned+";true")+`}`),
		tc("e1b", "git", `{"args":`+jsonArgs("grep", "-e", "--", "-Otouch "+pwned+";true")+`}`),
		tc("e2", "git", `{"args":`+jsonArgs("diff", outside, "/dev/null")+`}`),
		tc("e3", "glob", fmt.Sprintf(`{"pattern":"%s/*.txt"}`, filepath.Dir(outside))),
		tc("e4", "grep", fmt.Sprintf(`{"pattern":"TOPSECRET","glob":"../../../../%s"}`, filepath.Base(outside))),
		tc("e5", "glob", `{"pattern":"../../**/*.txt"}`),
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
	if !strings.HasPrefix(ccfg.CodeRead.Cwd, researchDir(pp)+"/r-") || !strings.HasSuffix(ccfg.CodeRead.Cwd, "/web") ||
		ccfg.CodeRead.Checkout == "" || !strings.HasPrefix(ccfg.CodeRead.Cwd, ccfg.CodeRead.Checkout+"/") {
		t.Fatalf("its cwd %q (its own checkout %q)", ccfg.CodeRead.Cwd, ccfg.CodeRead.Checkout)
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
		"c3": "not available to a research subagent", "c4": "isn't one of research's read-only options", "c5": "isn't one of research's read-only options", "c6": "# web", "c7": "first",
		"c8": "outside the project's code", "c9": "error", "c10": "error", "c11": "created readme", "c12": "README.md",
		"e1": "-e needs a value", "e1b": "-e needs a value", "e2": "absolute path",
		"e3": "absolute or home pattern", "e4": ".. isn't taken", "e5": ".. isn't taken"} {
		if !strings.Contains(results[id], want) {
			t.Errorf("%s: %q, want %q", id, results[id], want)
		}
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("git wrote a file outside the repository, or ran a program")
	}
	for id, r := range results {
		if strings.Contains(r, "TOPSECRET-OUTSIDE") || (id != "e2" && strings.Contains(r, "secret.txt")) {
			t.Errorf("%s read outside the roots: %q", id, clip(r, 300))
		}
	}
	if _, err := os.Stat(filepath.Join(ccfg.CodeRead.Cwd, "x.txt")); err == nil {
		t.Fatal("the child wrote a file")
	}
	hwait(t, "its own checkout removed after it answered", func() bool {
		_, err := os.Stat(ccfg.CodeRead.Checkout)
		return os.IsNotExist(err)
	})
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

// End to end: the coordinator's research reads the repo and writes a wiki
// page; a coding agent's task started later finds it in its sandbox
// (.xbin/wiki/<slug>.md, the copy written before its first prompt) and its
// brief points there.
func TestResearchWikiReachesTask(t *testing.T) {
	fx := newProjFix(t)
	fx.m.Caps = []string{"exec", "files", "tar"}
	argv := acptest.Command()
	fx.m.Harnesses = []fsbHarness{{ID: "fake", Title: "Fake agent (tests)", Argv: argv, Login: argv[0] + " acptest login"}}
	forgetHarnessProbes()
	t.Cleanup(func() { settleHarnesses(fx.ag.eng) })
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	f := fakeOf(fx.ag)
	isResearch := func(r LLMRequest) bool {
		return len(r.Msgs) > 0 && strings.Contains(asString(r.Msgs[0].Content), "research subagent of the project")
	}
	f.on(func(r LLMRequest) bool { return isResearch(r) && lastIs("user", "")(r) }, callTools(tc("c1", "read", `{"path":"README.md"}`))).once()
	f.on(func(r LLMRequest) bool { return isResearch(r) && lastIs("tool", "# web")(r) }, callTools(
		tc("c2", "wiki_write", `{"slug":"index","body":"- [[layout]]: what is where","note":"start the map"}`),
		tc("c3", "wiki_write", `{"slug":"layout","title":"Layout","body":"README.md holds one heading, `+"`# web`"+`.","note":"read it","sources":["README.md"]}`))).once()
	f.on(func(r LLMRequest) bool { return isResearch(r) && lastIs("tool", "")(r) }, say("README.md is `# web`; noted in the wiki")).once()
	f.on(func(r LLMRequest) bool { return !isResearch(r) && lastUser("map the repo")(r) && lastIs("user", "")(r) },
		callTools(tc("r1", "research", `{"question":"What is in the repo?"}`))).once()
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/coordinator", p.ID), map[string]any{"text": "map the repo"}); w.Code != 200 {
		t.Fatalf("POST coordinator: %d %s", w.Code, w.Body)
	}
	coord := fx.ag.db.coordRunOf(p.ID, "alice")
	hwait(t, "the coordinator's turn", func() bool {
		r, _ := fx.ag.db.getRun(coord)
		return r != nil && r.Status == statusIdle && len(f.callsFor(coord)) >= 2
	})
	if pg, err := fx.ag.db.wikiPage(p.ID, "layout"); err != nil || pg.Version != 1 {
		t.Fatalf("research's page: %+v %v", pg, err)
	}
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "fix the header", "agent": map[string]any{"provider": "fake"}})
	hwait(t, "the coding agent's first turn", func() bool {
		return turnOver(fx.ag, runID)() && strings.Contains(fullText(fx.ag.db, runID), "echo: ")
	})
	pp, _ := fx.ag.db.getProject(p.ID)
	b, err := os.ReadFile(filepath.Join(wikiDir(pp), "layout.md"))
	if err != nil || !strings.HasPrefix(string(b), "<!-- wiki: layout version 1 -->\n# Layout\n") || !strings.Contains(string(b), "`# web`") {
		t.Fatalf("the task's copy of the page: %v %q", err, b)
	}
	text := fullText(fx.ag.db, runID)
	if !strings.Contains(text, wikiDir(pp)+"/index.md") || !strings.Contains(text, "[[layout]]: what is where") {
		t.Fatalf("the task's brief doesn't point at the wiki:\n%s", clip(text, 3000))
	}
}
