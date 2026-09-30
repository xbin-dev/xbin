package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/manifestmerge"
)

// tmGit runs git in dir with PATH path ("" = the test's own).
func tmGit(t *testing.T, dir, path string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if path != "" {
		cmd.Env = append(cmd.Env, "PATH="+path)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tmMust(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := tmGit(t, dir, "", args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func tmWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, s := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// tmMarshalled is a template manifest the way xbinds before T1 wrote an
// instance's: the block deleted, the rest json.MarshalIndent-ed.
func tmMarshalled(t *testing.T, tpl []byte, from, to string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(jsonc.Strip(tpl), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "template")
	out, _ := json.MarshalIndent(m, "", "  ")
	return strings.ReplaceAll(string(out)+"\n", from, to)
}

// covers T1 — the merge driver as git runs it, with the config xbind writes
// (templaterepo_driver.go) and bx on PATH: an instance an older xbind made
// from the pre-B2a agent template (its manifest re-marshalled, at
// apps/agent and renamed at apps/my-agent) merges today's template cleanly,
// to exactly today's manifest instantiated the old way, unpartitioned; one
// its builder edited keeps the edit; a value both sides changed stays a
// conflict with git's markers; and without bx the merge is git's own.
func TestTemplateMergeManifestDriver(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// bx on PATH is this test binary running main() (TestMain); git beside it
	shim, bare := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "bx"), []byte("#!/bin/sh\n"+zsBxMain+"=1 exec '"+self+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{shim, bare} {
		if err := os.Symlink(gitPath, filepath.Join(d, "git")); err != nil {
			t.Fatal(err)
		}
	}
	withBx, withoutBx := shim+":/usr/bin:/bin", bare+":/usr/bin:/bin"
	if _, err := os.Stat("/usr/bin/bx"); err == nil {
		withoutBx = bare // a bx installed system-wide must not stand in
	}

	old, err := os.ReadFile(filepath.Join("..", "..", "internal", "manifestmerge", "testdata", "agent-bf5a56b1.json"))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := os.ReadFile(filepath.Join("..", "..", "builtin-templates", "agent", "xbin.json"))
	if err != nil {
		t.Fatal(err)
	}
	// today's manifest as an older xbind's served repo carries it: its old block
	blk, _, _ := jsonc.TopLevel(old, "template")
	served, err := jsonc.SetTopLevel(cur, "template", blk)
	if err != nil {
		t.Fatal(err)
	}

	// the template repo an older xbind wrote: v1 as it was, then today's
	tpl := t.TempDir()
	tmMust(t, tpl, "init", "-q")
	tmWrite(t, tpl, map[string]string{"xbin.json": string(old), "API.md": "see apps/agent\n\n# agent\n"})
	tmMust(t, tpl, "add", "-A")
	tmMust(t, tpl, "commit", "-qm", "template snapshot")
	snap1 := strings.TrimSpace(tmMust(t, tpl, "rev-parse", "HEAD"))
	tmWrite(t, tpl, map[string]string{"xbin.json": string(served), "API.md": "see apps/agent\n\n# agent\n\nPartitioned instances.\n"})
	tmMust(t, tpl, "commit", "-qam", "template snapshot")

	// instance: seeded from snapshot 1, the old instantiation on top, the
	// driver as xbind names it
	instance := func(tile, manifest string) string {
		t.Helper()
		dir := t.TempDir()
		tmMust(t, dir, "init", "-q")
		tmMust(t, dir, "fetch", "-q", tpl, snap1)
		tmMust(t, dir, "reset", "-q", "--hard", "FETCH_HEAD")
		tmWrite(t, dir, map[string]string{"xbin.json": manifest, "API.md": strings.ReplaceAll("see apps/agent\n\n# agent\n", "apps/agent", tile)})
		tmMust(t, dir, "commit", "-qam", "instantiate agent as "+tile)
		tmMust(t, dir, "config", "merge."+manifestmerge.DriverName+".driver", manifestmerge.Driver("apps/agent", tile))
		if err := os.WriteFile(filepath.Join(dir, ".git", "info", "attributes"), []byte(manifestmerge.Attribute+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		tmMust(t, dir, "fetch", "-q", tpl, "HEAD")
		return dir
	}
	merge := func(dir, path string) (string, error) {
		t.Helper()
		return tmGit(t, dir, path, "merge", "--no-edit", "FETCH_HEAD")
	}
	read := func(dir string) string {
		b, _ := os.ReadFile(filepath.Join(dir, "xbin.json"))
		return string(b)
	}

	for _, tile := range []string{"apps/agent", "apps/my-agent"} {
		t.Run(tile, func(t *testing.T) {
			dir := instance(tile, tmMarshalled(t, old, "apps/agent", tile))
			out, err := merge(dir, withBx)
			if err != nil {
				t.Fatalf("the merge conflicts (%v):\n%s\n%s", err, out, read(dir))
			}
			if !strings.Contains(out, "xbin.json merged by keys") {
				t.Errorf("bx didn't say what it did:\n%s", out)
			}
			if got, want := read(dir), tmMarshalled(t, cur, "apps/agent", tile); got != want {
				t.Fatalf("merged:\n%s\nwant:\n%s", got, want)
			}
			if raw, has, _ := jsonc.TopLevel([]byte(read(dir)), "partition"); has {
				t.Errorf("the merge made the instance partitioned: %s", raw)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "API.md")); !strings.Contains(string(b), "Partitioned instances.") || !strings.Contains(string(b), tile) {
				t.Errorf("the other file: %q", b)
			}
		})
	}

	t.Run("edited", func(t *testing.T) {
		ours := strings.Replace(tmMarshalled(t, old, "", ""), "  \"runtime\": \"go\",\n", "  \"runtime\": \"go\",\n  \"title\": \"Sales bot\",\n", 1)
		dir := instance("apps/agent", ours)
		if out, err := merge(dir, withBx); err != nil {
			t.Fatalf("the merge conflicts (%v):\n%s", err, out)
		}
		if got := read(dir); !strings.Contains(got, `"title": "Sales bot"`) || !strings.Contains(got, `"partitionMail": "/mailbox"`) {
			t.Errorf("merged:\n%s", got)
		}
	})

	t.Run("both changed", func(t *testing.T) {
		ours := strings.Replace(tmMarshalled(t, old, "", ""), `"entry": "./_backend"`, `"entry": "./backend"`, 1)
		dir := instance("apps/agent", ours)
		// upstream moves the entry too
		tmWrite(t, tpl, map[string]string{"xbin.json": strings.Replace(string(served), `"entry": "./_backend"`, `"entry": "./cmd"`, 1)})
		tmMust(t, tpl, "commit", "-qam", "template snapshot")
		defer tmMust(t, tpl, "reset", "-q", "--hard", "HEAD~1")
		tmMust(t, dir, "fetch", "-q", tpl, "HEAD")
		out, err := merge(dir, withBx)
		if err == nil {
			t.Fatalf("a value both changed merged:\n%s", read(dir))
		}
		if got := read(dir); !strings.Contains(got, "<<<<<<< ours") || !strings.Contains(got, ">>>>>>> theirs") || !strings.Contains(out, "can't be merged by keys") {
			t.Errorf("no conflict markers (or no word from bx):\n%s\n%s", out, got)
		}
	})

	t.Run("no bx", func(t *testing.T) {
		dir := instance("apps/agent", tmMarshalled(t, old, "", ""))
		if _, err := merge(dir, withoutBx); err == nil {
			t.Fatalf("merged without bx: the line merge must conflict here\n%s", read(dir))
		}
		if got := read(dir); !strings.Contains(got, "<<<<<<< ours") {
			t.Errorf("without bx, git's own conflict markers:\n%s", got)
		}
	})
}
