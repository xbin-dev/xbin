//go:build linux && integration

package isolated

// updates_tile_test.go — D248 (plans/wsagent/updates.md §3): the brief a
// workspace agent hands a tile agent for an update is true. Each test runs
// the exact commands xbind's brief names (server.TemplateUpdateBrief,
// server.BuiltinProposalBrief), in order, in a terminal session on the
// tile of a real `xbind --isolate` — the sandbox, environment and
// tile-scoped token a tile agent's session has (term.sessionEnv is one
// function for both) — as the person a tile agent opens as.
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^Test(TemplateUpdate|BuiltinProposal)InATileSession$' ./test/isolated/

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

func updAddrs() []string {
	var addrs []string
	for p := 19540; p <= 19569; p++ {
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", p))
	}
	return addrs
}

// updBackend is the template's new backend: what the instance has once it
// merged the template, and answers once xbind rebuilt it.
const updBackend = `package main

import (
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("merged from the template\n")) })
	xbin.Serve(mux)
}
`

// covers D248 — a template instance one commit behind its template, with
// a commit of its own, merged by the commands of xbind's brief in a
// terminal session on it: the `template` remote fetches with the session's
// own credential, the merge (xbin.json by the manifest driver, bx on PATH)
// is clean, and xbind rebuilds the backend the template added, which then
// answers.
func TestTemplateUpdateInATileSession(t *testing.T) {
	t.Parallel()
	a := xbindtest.Require(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, TryAddrs: updAddrs()})
	const tile = "apps/crm"
	d.Must(t, "POST", "/api/xbin/templates/new", map[string]any{"source": "starter", "path": tile}, 200)
	// a person whose sessions reach xbin: the terminal grants (D17) — without
	// them the read sends the update to someone whose do (SessionReachesXbind)
	d.AddUser(t, "ana", "pw-ana-248a", "user", map[string]string{tile: "terminal"}, map[string]any{"termApi": true, "termNet": true})
	d.Must(t, "POST", "/api/xbin/owner", map[string]any{"tile": tile, "to": "user:ana"}, 200)
	ana := xbindtest.H("Authorization", "Bearer "+d.Login(t, "ana", "pw-ana-248a"))
	dir := filepath.Join(d.WS, filepath.FromSlash(tile))
	xbindtest.Eventually(t, time.Minute, tile+"'s repository and its template remote", func() (bool, string) {
		out, err := tmGitIn(t, dir, "", "remote", "get-url", "template")
		return err == nil && strings.TrimSpace(out) == "http://xbin/api/xbin/templates/starter.git", out
	})

	// the template gains a commit the instance doesn't have: a Go backend,
	// and its manifest says so (a line the instance's manifest merges by keys)
	repo := filepath.Join(d.WS, ".xbin", "template-repos", "starter")
	manifest := string(readT(t, filepath.Join(repo, "xbin.json")))
	upstream := strings.Replace(manifest, "{\n", "{\n  \"runtime\": \"go\",\n", 1)
	if upstream == manifest {
		t.Fatalf("the template's manifest: %q", manifest)
	}
	writeT(t, filepath.Join(repo, "xbin.json"), upstream)
	writeT(t, filepath.Join(repo, "go.mod"), "module crm\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n")
	writeT(t, filepath.Join(repo, "backend", "main.go"), updBackend)
	tmGitMust(t, repo, "add", "-A")
	tmGitMust(t, repo, "-c", "user.email=xbin@localhost", "-c", "user.name=xbin", "commit", "-q", "-m", "template snapshot")
	tmGitMust(t, repo, "update-server-info")

	// ana's own change, committed in her terminal: the merge is a real one
	sess := w2OpenTerm(t, d, tile, ana)
	if out, rc := sess.run(t, `printf '<p>ours</p>\n' >> index.html && git add index.html && git commit -q -m "our change"`, time.Minute); rc != 0 {
		t.Fatalf("ana's own commit: exit %d\n%s", rc, out)
	}

	// what the person's own listing says, and the agent's read's counts
	var listed struct {
		Instances []struct {
			Path, Template string
			Legacy         bool
		}
	}
	if r := d.Call(t, "GET", "/api/xbin/templates/updates", nil, ana); r.Status != 200 || json.Unmarshal(r.Body, &listed) != nil || len(listed.Instances) != 1 || listed.Instances[0].Path != tile {
		t.Fatalf("the person's listing: %d %s", r.Status, r)
	}

	head := strings.TrimSpace(tmGitMust(t, repo, "rev-parse", "main"))
	brief := server.TemplateUpdateBrief(tile, "starter", head, 1, false)

	// the tile's own config redirects xbind's URL to another history (here
	// the agent template, served by the same xbind): the fetch goes there —
	// no -c takes url.*.insteadOf away — and the check says STOP before
	// anything is merged (D248 re-review, P2b)
	redirect := `url."$XBIN_URL/api/xbin/templates/agent.git".insteadOf`
	if out, rc := sess.run(t, `git config `+redirect+` http://xbin/api/xbin/templates/starter.git`, time.Minute); rc != 0 {
		t.Fatalf("the redirect: %d %s", rc, out)
	}
	for _, cmd := range brief.Commands[1:3] {
		out, rc := sess.run(t, cmd, 5*time.Minute)
		t.Logf("$ %s → %d\n%s", cmd, rc, out)
		if strings.Contains(cmd, " fetch ") && (rc != 0 || !strings.Contains(out, "/templates/agent")) {
			t.Fatalf("the tile's redirect wasn't taken (the check below would prove nothing): %d %q", rc, out)
		}
		if strings.HasPrefix(cmd, "test ") && !strings.Contains(out, "STOP") {
			t.Fatalf("a redirected fetch passed the check: %q", out)
		}
	}
	if out, rc := sess.run(t, `git config --unset `+redirect, time.Minute); rc != 0 {
		t.Fatalf("unset the redirect: %d %s", rc, out)
	}

	outs := map[int]string{}
	for i, cmd := range brief.Commands {
		if strings.HasPrefix(cmd, "curl ") {
			continue // after the build, below
		}
		out, rc := sess.run(t, cmd, 5*time.Minute)
		t.Logf("$ %s → %d\n%s", cmd, rc, out)
		if rc != 0 {
			t.Fatalf("the brief's command %d, %q, failed (exit %d):\n%s\nmanifest:\n%s", i+1, cmd, rc, out, readT(t, filepath.Join(dir, "xbin.json")))
		}
		outs[i] = out
	}
	if outs[0] != "" {
		t.Errorf("git status --short: %q", outs[0])
	}
	if !strings.Contains(outs[2], "verified: xbin's template "+head[:12]) {
		t.Errorf("the check: %q", outs[2])
	}
	if !strings.Contains(outs[3], "template snapshot") {
		t.Errorf("what the template changed: %q", outs[3])
	}
	// merged: both parents, nothing uncommitted, the template's files and ours
	if out, _ := sess.run(t, "git log -1 --format=%P", time.Minute); len(strings.Fields(out)) != 2 {
		t.Errorf("the merge commit's parents: %q", out)
	}
	if out, _ := sess.run(t, "git status --short", time.Minute); out != "" {
		t.Errorf("after the merge: %q", out)
	}
	if got := string(readT(t, filepath.Join(dir, "xbin.json"))); !strings.Contains(got, `"runtime": "go"`) || strings.Contains(got, "<<<<<<<") || strings.Contains(got, `"template"`) {
		t.Errorf("the merged manifest:\n%s", got)
	}
	if got := string(readT(t, filepath.Join(dir, "index.html"))); !strings.Contains(got, "<p>ours</p>") {
		t.Errorf("our change is gone: %s", got)
	}
	// the backend the template added: xbind builds it; the brief's call from
	// the session starts it, and it answers
	curl := brief.Commands[len(brief.Commands)-1]
	xbindtest.Eventually(t, 8*time.Minute, "the merged backend answers the session's call", func() (bool, string) {
		out, rc := sess.run(t, curl, 3*time.Minute)
		return rc == 0 && strings.TrimSpace(out) == "200", out
	})
	if r := d.Call(t, "GET", "/api/"+tile+"/", nil); r.Status != 200 || !strings.Contains(string(r.Body), "merged from the template") {
		t.Errorf("the tile's API after the merge: %d %s", r.Status, r)
	}
	if out, rc := sess.run(t, "bx status", time.Minute); rc != 0 {
		t.Errorf("bx status: %d %s", rc, out)
	}
	// nothing behind any more
	if r := d.Call(t, "GET", "/api/xbin/templates/updates", nil, ana); r.Status != 200 || strings.Contains(string(r.Body), tile) {
		t.Errorf("still listed: %d %s", r.Status, r)
	}
}

// covers D248 — a builtin whose update conflicts with the tile's own
// change: mode safe writes nothing and files the update as a proposal
// on the tile; the commands of xbind's brief, in a terminal session on the
// tile as its person, fetch it, `git am --3way` it in a clone, resolve,
// bring it in and close it merged — a person's terminal session may
// (never the tile's own code) — which records what the tile then holds:
// upstream's line, with the person's own right after it, holds the update,
// so it is no longer listed.
func TestBuiltinProposalInATileSession(t *testing.T) {
	t.Parallel()
	a := xbindtest.Require(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, TryAddrs: updAddrs()})
	const name, tile = "prometheus-viewer", "apps/prometheus-viewer"
	const id = "tile:" + name
	d.Must(t, "POST", "/api/xbin/builtins/import", map[string]any{"name": name}, 200)
	d.AddUser(t, "ana", "pw-ana-248b", "user", map[string]string{tile: "terminal"}, map[string]any{"termApi": true, "termNet": true})
	d.Must(t, "POST", "/api/xbin/owner", map[string]any{"tile": tile, "to": "user:ana"}, 200)
	ana := xbindtest.H("Authorization", "Bearer "+d.Login(t, "ana", "pw-ana-248b"))
	dir := filepath.Join(d.WS, filepath.FromSlash(tile))
	xbindtest.Eventually(t, time.Minute, tile+"'s repository", func() (bool, string) {
		out, err := tmGitIn(t, dir, "", "log", "--oneline", "-1")
		return err == nil, out
	})

	// the workspace installed an older version (its base), and ana changed
	// the very line this xbin's version changes
	const file = "index.html"
	shipped := string(readT(t, filepath.Join(dir, file)))
	lines := strings.Split(shipped, "\n")
	at := -1
	for i, l := range lines {
		if strings.TrimSpace(l) != "" && i > 0 {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("no line to change in %s", file)
	}
	older := append([]string{}, lines...)
	older[at] = "<!-- the older version's line -->"
	ours := append([]string{}, older...)
	ours[at] = "<!-- ana line -->"
	base := strings.Join(older, "\n")
	mine := strings.Join(ours, "\n")
	snap := filepath.Join(d.WS, ".xbin", "builtins", "tile~"+name)
	writeT(t, filepath.Join(snap, file), base)
	markerPath := filepath.Join(d.WS, ".xbin", "builtins.json")
	var marker struct {
		Units map[string]*struct {
			Source      string            `json:"source"`
			InstallPath string            `json:"installPath"`
			Version     int               `json:"version,omitempty"`
			Hash        string            `json:"hash"`
			Files       map[string]string `json:"files"`
			Adopted     bool              `json:"adopted,omitempty"`
			Pinned      bool              `json:"pinned,omitempty"`
		} `json:"units"`
	}
	if err := json.Unmarshal(readT(t, markerPath), &marker); err != nil || marker.Units[id] == nil {
		t.Fatalf("the builtins marker: %v %s", err, readT(t, markerPath))
	}
	u := marker.Units[id]
	u.Files[file] = shaT(base)
	u.Version--
	u.Hash = rollupT(u.Files)
	raw, _ := json.MarshalIndent(marker, "", "  ")
	writeT(t, markerPath, string(raw)+"\n")
	// the tile's history as the workspace has it: the older install, then ana's change
	writeT(t, filepath.Join(dir, file), base)
	tmGitMust(t, dir, "add", "-A")
	tmGitMust(t, dir, "commit", "-q", "-m", "the older install")
	writeT(t, filepath.Join(dir, file), mine)
	tmGitMust(t, dir, "commit", "-qam", "ana's change")

	// the safe update: nothing written, filed as a proposal
	var safe struct {
		Applied   string   `json:"applied"`
		Conflicts []string `json:"conflicts"`
		PR        *struct {
			Target string `json:"target"`
			Number int    `json:"number"`
		} `json:"pr"`
	}
	r := d.Call(t, "POST", "/api/xbin/builtins/update", map[string]any{"id": id, "mode": "safe"})
	if r.Status != 200 || json.Unmarshal(r.Body, &safe) != nil || safe.Applied != "proposed" || safe.PR == nil || safe.PR.Target != tile {
		t.Fatalf("the safe update: %d %s", r.Status, r)
	}
	if got := string(readT(t, filepath.Join(dir, file))); got != mine {
		t.Fatalf("a conflicting safe update wrote %s:\n%s", file, got)
	}

	brief := server.BuiltinProposalBrief(tile, id, safe.PR.Number)
	sess := w2OpenTerm(t, d, tile, ana)
	for i, cmd := range brief.Commands {
		out, rc := sess.run(t, cmd, 5*time.Minute)
		t.Logf("$ %s → %d\n%s", cmd, rc, out)
		if strings.Contains(cmd, " am --3way ") {
			if rc == 0 {
				t.Fatalf("the proposal applied without the conflict it was filed for:\n%s", out)
			}
			// resolved as a tile agent would, in the copy: upstream's file, with
			// ana's line kept below the one upstream changed
			work := `"$XBIN_WORKSPACE/` + tile + `/.git/xbin-update"`
			for _, c := range []string{
				"git -C " + work + " checkout --theirs " + file,
				fmt.Sprintf("sed -i '%da <!-- ana line -->' %s/%s", at+1, work, file),
				"git -C " + work + " add " + file,
				"git -C " + work + " am --continue",
			} {
				if out, rc := sess.run(t, c, time.Minute); rc != 0 {
					t.Fatalf("%s (the brief's resolution steps): exit %d\n%s", c, rc, out)
				}
			}
			continue
		}
		if rc != 0 {
			t.Fatalf("the brief's command %d, %q, failed (exit %d):\n%s", i+1, cmd, rc, out)
		}
	}
	got := string(readT(t, filepath.Join(dir, file)))
	if !strings.Contains(got, lines[at]+"\n<!-- ana line -->") || strings.Contains(got, "<<<<<<<") || strings.Contains(got, "older version") {
		t.Errorf("the tile after the merge:\n%s", got)
	}
	var pr struct{ State string }
	if r := d.Call(t, "GET", fmt.Sprintf("/api/xbin/code/pr?target=%s&n=%d", tile, safe.PR.Number), nil, ana); r.Status != 200 || json.Unmarshal(r.Body, &pr) != nil || pr.State != "merged" {
		t.Errorf("the proposal: %d %s", r.Status, r)
	}
	var ups []struct{ ID string }
	if r := d.Call(t, "GET", "/api/xbin/builtins/updates", nil); r.Status != 200 || json.Unmarshal(r.Body, &ups) != nil {
		t.Fatalf("the listing: %d %s", r.Status, r)
	}
	for _, up := range ups {
		if up.ID == id {
			t.Errorf("%s is still offered after its proposal closed merged", id)
		}
	}
}

func readT(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeT(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func shaT(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// rollupT is the builtins marker's rollup of a file set (internal/builtins).
func rollupT(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\x00", k, files[k])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
