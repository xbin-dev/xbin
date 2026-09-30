//go:build linux && integration

package isolated

// partitions_tplmerge_test.go — work pack T1 of the partitioned-tiles plan:
// a template instance takes its template's manifest changes without a
// conflict (docs/overview/03-components.md §Templates), end to end on a
// real `xbind --isolate`.
//
//   - Two agent instances as an xbind before B2a made them — the builtin
//     agent template of bf5a56b1 (this repository's history), instantiated
//     the old way (xbin.json re-marshalled) at apps/agent and at
//     apps/my-agent, their repositories seeded from a template repository
//     written the old way (the files mirrored, block and all) — and xbind
//     restarted on them, as an upgrade does: the served repository gains
//     today's snapshot (keeping its old block), and each instance's
//     repository gets the manifest's merge driver.
//   - The builder's `git fetch template && git merge template/main` (the
//     served repository over HTTP, bx on PATH as in a terminal) is clean in
//     both: the manifest is today's, merged by keys, with no partition and no
//     template block; the tile stays unpartitioned, and apps/agent's backend
//     — today's code now — builds and answers, in its legacy mode.
//   - A new instance keeps the template's comments, starts partitioned, has
//     the driver, and is up to date with its template.
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsTemplateMerge$' ./test/isolated/

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

// tmPreB2a is the agent template before B2a changed its manifest (master's
// head when the partitioned-tiles branch forked; 7b54e7fb has the same
// template).
const tmPreB2a = "bf5a56b1"

// tmGitIn runs git in dir as a builder would (no system or global config),
// with PATH path when set.
func tmGitIn(t *testing.T, dir, path string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=b@b", "-c", "user.name=builder"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if path != "" {
		cmd.Env = append(cmd.Env, "PATH="+path)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tmGitMust(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := tmGitIn(t, dir, "", args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

// tmOldManifest is a template manifest as xbinds before T1 wrote an
// instance's (builtins.stripTemplateBlock at bf5a56b1).
func tmOldManifest(t *testing.T, tpl []byte, tile string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(jsonc.Strip(tpl), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "template")
	out, _ := json.MarshalIndent(m, "", "  ")
	return []byte(strings.ReplaceAll(string(out)+"\n", "apps/agent", tile))
}

func TestPartitionsTemplateMerge(t *testing.T) {
	t.Parallel()
	a := xbindtest.Require(t)
	if out, err := exec.Command("git", "-C", a.Repo, "cat-file", "-e", tmPreB2a+"^{commit}").CombinedOutput(); err != nil {
		t.Skipf("needs this repository's history (%s): %v %s", tmPreB2a, err, out)
	}
	old := t.TempDir()
	if out, err := exec.Command("sh", "-c", `git -C "$1" archive --format=tar "$2" builtin-templates/agent | tar -x -C "$3"`,
		"sh", a.Repo, tmPreB2a, old).CombinedOutput(); err != nil {
		t.Fatalf("the pre-B2a template: %v %s", err, out)
	}
	oldTpl := filepath.Join(old, "builtin-templates", "agent")
	oldManifest, err := os.ReadFile(filepath.Join(oldTpl, "xbin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(oldManifest), "partitionMail") {
		t.Fatalf("%s's agent template already has B2a's manifest", tmPreB2a)
	}

	d := xbindtest.Start(t, a, xbindtest.Options{Env: []string{"XBIN_VAULT_PASSPHRASE=xbindtest-vault"}}) // the agent's db is encrypted
	e := &psEnv{d: d}
	d.Stop(t)

	// the template repository an older xbind wrote: the files, one commit
	repo := filepath.Join(d.WS, ".xbin", "template-repos", "agent")
	if !strings.HasPrefix(repo, d.WS+string(filepath.Separator)) {
		t.Fatalf("template repo %s outside the workspace", repo)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-a", oldTpl+"/.", repo+"/").CombinedOutput(); err != nil {
		t.Fatalf("mirror the old template: %v %s", err, out)
	}
	tmGitMust(t, repo, "init", "-q", "-b", "main")
	tmGitMust(t, repo, "add", "-A")
	tmGitMust(t, repo, "-c", "user.email=xbin@localhost", "-c", "user.name=xbin", "commit", "-q", "-m", "template snapshot")
	tmGitMust(t, repo, "update-server-info")

	// the instances an older xbind made: rendered, the manifest re-marshalled,
	// the repository seeded from the snapshot, the `template` remote
	for _, tile := range []string{"apps/agent", "apps/my-agent"} {
		files, err := builtins.RenderTree(os.DirFS(filepath.Join(old, "builtin-templates")), "agent", tile, "apps/agent", nil)
		if err != nil {
			t.Fatal(err)
		}
		files["xbin.json"] = tmOldManifest(t, oldManifest, tile)
		dir := filepath.Join(d.WS, filepath.FromSlash(tile))
		if _, err := builtins.WriteTree(dir, tile, files); err != nil {
			t.Fatal(err)
		}
		tmGitMust(t, dir, "init", "-q", "-b", "main")
		tmGitMust(t, dir, "fetch", "-q", repo, "main")
		tmGitMust(t, dir, "update-ref", "refs/heads/main", "FETCH_HEAD")
		tmGitMust(t, dir, "reset", "-q", "--mixed", "HEAD")
		tmGitMust(t, dir, "add", "-A")
		tmGitMust(t, dir, "-c", "user.email=xbin@localhost", "-c", "user.name=xbin", "commit", "-q", "--allow-empty", "-m", "instantiate agent as "+tile)
		tmGitMust(t, dir, "remote", "add", "template", "http://xbin/api/xbin/templates/agent.git")
	}

	// the upgrade
	d.Restart(t)
	if log := tmGitMust(t, repo, "log", "--format=%s%n%b", "-1"); !strings.Contains(log, `"template" block changed`) {
		t.Errorf("the served repository's new snapshot: %s", log)
	}
	if kept, _ := os.ReadFile(filepath.Join(repo, "xbin.json")); !strings.Contains(string(kept), `"defaultName": "agent"`+"\n  },") ||
		!strings.Contains(string(kept), "partitionMail") {
		t.Errorf("the served manifest keeps its old block and has today's keys:\n%s", cut(string(kept), 1500))
	}
	for _, tile := range []string{"apps/agent", "apps/my-agent"} {
		dir := filepath.Join(d.WS, filepath.FromSlash(tile))
		drv := strings.TrimSpace(tmGitMust(t, dir, "config", "merge.xbin-manifest.driver"))
		if !strings.HasPrefix(drv, "bx template merge-manifest") || strings.Contains(drv, "--rename") != (tile != "apps/agent") {
			t.Errorf("%s's driver: %q", tile, drv)
		}
		if got := strings.TrimSpace(tmGitMust(t, dir, "check-attr", "merge", "xbin.json")); got != "xbin.json: merge: xbin-manifest" {
			t.Errorf("%s: %q", tile, got)
		}
		e.waitState(t, tile, "")
	}
	// the old code runs, as before the upgrade
	xbindtest.Eventually(t, 8*time.Minute, "the old instance's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/apps/agent/config", nil)
		return r.Status == 200 && strings.Contains(string(r.Body), `"system"`), fmt.Sprint(r.Status, " ", cut(string(r.Body), 200))
	})

	// the builder merges the template: clean, by keys
	path := a.Bin + string(os.PathListSeparator) + os.Getenv("PATH")
	for _, tile := range []string{"apps/agent", "apps/my-agent"} {
		dir := filepath.Join(d.WS, filepath.FromSlash(tile))
		if out, err := tmGitIn(t, dir, path, "fetch", "-q", d.URL+"/api/xbin/templates/agent.git", "main"); err != nil {
			t.Fatalf("%s: fetch the template: %v\n%s", tile, err, out)
		}
		out, err := tmGitIn(t, dir, path, "merge", "--no-edit", "FETCH_HEAD")
		if err != nil {
			t.Fatalf("%s: the merge conflicts (%v):\n%s\n%s", tile, err, out, tmGitMust(t, dir, "diff"))
		}
		if !strings.Contains(out, "xbin.json merged by keys") {
			t.Errorf("%s: bx didn't merge the manifest:\n%s", tile, out)
		}
		man, _ := os.ReadFile(filepath.Join(dir, "xbin.json"))
		if _, has, _ := jsonc.TopLevel(man, "partition"); has {
			t.Errorf("%s: the merge made it partitioned:\n%s", tile, man)
		}
		if _, has, _ := jsonc.TopLevel(man, "template"); has {
			t.Errorf("%s: the merge brought the template block:\n%s", tile, man)
		}
		for _, s := range []string{`"partitionMail": "/mailbox"`, `"target": "res:` + tile + `/conf"`, `"target": "res:` + tile + `/team"`} {
			if !strings.Contains(string(man), s) {
				t.Errorf("%s: the merged manifest lacks %s:\n%s", tile, s, man)
			}
		}
		if strings.Contains(string(man), "res:apps/agent/") != (tile == "apps/agent") {
			t.Errorf("%s: another tile's resources:\n%s", tile, man)
		}
		if st := tmGitMust(t, dir, "status", "--porcelain"); st != "" {
			t.Errorf("%s: after the merge: %s", tile, st)
		}
		e.waitState(t, tile, "")
	}
	// today's code builds and runs, unpartitioned (/health is today's)
	xbindtest.Eventually(t, 8*time.Minute, "the merged instance's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/apps/agent/health", nil)
		return r.Status == 200 && strings.Contains(string(r.Body), `"mode":"legacy"`), fmt.Sprint(r.Status, " ", cut(string(r.Body), 200))
	})
	if r := d.Call(t, "GET", "/api/apps/agent/config", nil); r.Status != 200 || !strings.Contains(string(r.Body), `"system"`) {
		t.Errorf("the merged instance's config: %d %s", r.Status, cut(string(r.Body), 300))
	}

	// a new instance: the template's comments, partitioned, the driver, up to date
	d.Must(t, "POST", "/api/xbin/templates/new", map[string]string{"source": "agent", "path": "apps/agent-new"}, 200)
	d.WaitComponent(t, "apps/agent-new")
	dir := filepath.Join(d.WS, "apps", "agent-new")
	man, _ := os.ReadFile(filepath.Join(dir, "xbin.json"))
	if !strings.HasPrefix(string(man), "{\n  \"partition\": [\"user\", \"global\"],\n") || !strings.Contains(string(man), "// Where xbind rings") ||
		strings.Contains(string(man), "A TEMPLATE component") {
		t.Errorf("the new instance's manifest:\n%s", cut(string(man), 1200))
	}
	e.waitState(t, "apps/agent-new", "partitioned")
	if drv := tmGitMust(t, dir, "config", "merge.xbin-manifest.driver"); !strings.Contains(drv, "--rename apps/agent=apps/agent-new") {
		t.Errorf("the new instance's driver: %q", drv)
	}
	tmGitMust(t, dir, "fetch", "-q", d.URL+"/api/xbin/templates/agent.git", "main")
	if out := tmGitMust(t, dir, "merge", "--no-edit", "FETCH_HEAD"); !strings.Contains(out, "Already up to date") {
		t.Errorf("the new instance merging its template: %s", out)
	}
}
