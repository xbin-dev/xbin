package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers P7 P9 P20 — /components rows keep their keys: runtime, hasIndex
// and native describe the primary's code (its checkpoint while pinned),
// chrome and template follow its inbound surface, and manifestError, roles,
// uses and deps describe the work tree; a tile whose work tree has no valid
// manifest keeps its row from the checkpoint with the error surfaced. A
// tile with a record gains the primary summary, the same for every caller
// who sees the row; a zero-state tile's row has no deployments key, and no
// deployment is ever a row. /components/{path} answers alike.
func TestComponentsDeploymentFields(t *testing.T) {
	w := newPinned(newAssetWS(t, TileAssetsLegacy))
	page := `<!doctype html><html><head></head><body>p</body></html>`
	if err := w.st.SetChromeApproved("apps/c", true); err != nil {
		t.Fatal(err)
	}
	if err := w.st.SetChromeApproved("apps/u", true); err != nil {
		t.Fatal(err)
	}
	w.edit(map[string]string{
		// apps/a: the work tree moved on after the pause.
		"apps/a/xbin.json": `{"runtime":"node","expose":{"roles":{"editor":"edits"}},` +
			`"uses":[{"target":"apps/b","role":"reader"}],"deps":["apps/b"],"exposes":{"web":{"kind":"bogus"}}}`,
		// apps/c: chrome only in the checkpoint; apps/u: only in the work tree.
		"apps/c/xbin.json": `{}`, "apps/c/index.html": page,
		"apps/u/xbin.json": `{"chrome":true}`, "apps/u/index.html": page,
		// apps/t: a template only in the checkpoint.
		"apps/t/xbin.json": `{}`, "apps/t/index.html": page,
		// apps/k: its work tree's manifest broke.
		"apps/k/xbin.json": `{"runtime":`, "apps/k/index.html": page,
	})
	if err := os.Remove(filepath.Join(w.root, "apps/a/index.html")); err != nil {
		t.Fatal(err)
	}
	w.rescan()
	w.pin("apps/a", map[string]string{
		"xbin.json": `{"runtime":"go","expose":{"roles":{"reader":"reads"}},` +
			`"uses":[{"target":"apps/secret","role":"reader"}],"deps":["apps/secret"],"native":"m.js"}`,
		"index.html": page, "m.js": `export {}`,
	}, nil)
	w.pin("apps/c", map[string]string{"xbin.json": `{"chrome":true}`, "index.html": page}, nil)
	w.pin("apps/u", map[string]string{"xbin.json": `{}`, "index.html": page}, nil)
	w.pin("apps/t", map[string]string{"xbin.json": `{"template":{"title":"T"}}`, "index.html": page}, nil)
	w.pin("apps/k", map[string]string{"xbin.json": `{"runtime":"python","deps":["apps/b"],"expose":{"roles":{"reader":"reads"}}}`,
		"index.html": page}, nil)
	w.pol.summary["apps/c"] = deploymentsSummary{Primary: util.MainDeployment, Pinned: true, Protected: true}
	// apps/b has a record whose primary follows the work tree (not pinned).
	w.pol.summary["apps/b"] = deploymentsSummary{Primary: util.MainDeployment}

	owner := componentsView(t, w.s)
	a := owner["apps/a"]
	if a.Runtime != "go" || !a.HasIndex || a.Native == nil || a.Native.Entry != "m.js" {
		t.Errorf("apps/a's primary's code: runtime %q hasIndex %v native %+v, want the checkpoint's go, true, m.js", a.Runtime, a.HasIndex, a.Native)
	}
	if roles, _ := json.Marshal(a.Roles); string(roles) != `{"editor":"edits"}` {
		t.Errorf("apps/a roles %s, want the work tree's", roles)
	}
	if uses, _ := json.Marshal(a.Uses); !strings.Contains(string(uses), `"apps/b"`) || strings.Contains(string(uses), "secret") {
		t.Errorf("apps/a uses %s, want the work tree's", uses)
	}
	if !reflect.DeepEqual(a.Deps, []string{"apps/b"}) {
		t.Errorf("apps/a deps %v, want the work tree's", a.Deps)
	}
	if !strings.Contains(a.ManifestErr, "web") {
		t.Errorf("apps/a manifestError %q, want the work tree's exposes error", a.ManifestErr)
	}
	if a.Deployments == nil || *a.Deployments != (deploymentsSummary{Primary: "main", Pinned: true}) {
		t.Errorf("apps/a deployments %+v, want {main pinned}", a.Deployments)
	}
	if c := owner["apps/c"]; !c.Chrome || c.ChromeRequested || c.Sandbox != nil {
		t.Errorf("apps/c (chrome in its checkpoint): chrome %v requested %v", c.Chrome, c.ChromeRequested)
	}
	if u := owner["apps/u"]; u.Chrome || u.ChromeRequested {
		t.Errorf("apps/u (chrome only in its work tree): chrome %v requested %v", u.Chrome, u.ChromeRequested)
	}
	if !owner["apps/t"].Template {
		t.Error("apps/t (a template in its checkpoint) isn't reported as one")
	}
	k := owner["apps/k"]
	if k.Runtime != "python" || !strings.Contains(k.ManifestErr, "serving the pinned primary") || k.Roles != nil {
		t.Errorf("apps/k (broken work tree): runtime %q manifestError %q roles %v", k.Runtime, k.ManifestErr, k.Roles)
	}
	if b := owner["apps/b"]; b.Deployments == nil || *b.Deployments != (deploymentsSummary{Primary: "main"}) {
		t.Errorf("apps/b deployments %+v, want {main}", b.Deployments)
	}
	if owner["apps/secret"].Deployments != nil || owner["apps/raw"].Deployments != nil {
		t.Error("a zero-state tile gained a summary")
	}
	for path := range owner {
		if strings.Contains(path, "+") {
			t.Errorf("a deployment row: %s", path)
		}
	}

	// The same summary for every caller who sees the row; a zero-state
	// row's bytes carry no deployments key at all.
	ana := w.do("/api/xbin/components", w.session("ana"))
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(ana.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		var path string
		_ = json.Unmarshal(row["path"], &path)
		seen[path] = true
		want, _ := json.Marshal(owner[path].Deployments)
		got, ok := row["deployments"]
		switch {
		case owner[path].Deployments == nil && ok:
			t.Errorf("%s: a zero-state row carries deployments %s", path, got)
		case owner[path].Deployments != nil && string(got) != string(want):
			t.Errorf("%s as ana: deployments %s, the owner sees %s", path, got, want)
		}
	}
	if !seen["apps/a"] || !seen["apps/b"] || seen["apps/secret"] {
		t.Errorf("ana's rows: %v", seen)
	}

	// One component's detail answers alike.
	var one struct {
		Component componentInfo `json:"component"`
	}
	rec := w.do("/api/xbin/components/apps/a", w.zsOwner())
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	d := one.Component
	if d.Runtime != "go" || !d.HasIndex || d.Native == nil || d.Deployments == nil || !d.Deployments.Pinned ||
		!reflect.DeepEqual(d.Deps, []string{"apps/b"}) {
		t.Errorf("/components/apps/a: %+v", d)
	}
	if roles, _ := json.Marshal(d.Roles); string(roles) != `{"editor":"edits"}` {
		t.Errorf("/components/apps/a roles %s, want the work tree's", roles)
	}
	if rec := w.do("/api/xbin/components/apps/secret", w.zsOwner()); strings.Contains(rec.Body.String(), `"deployments"`) {
		t.Errorf("a zero-state tile's detail carries deployments: %s", rec.Body.String())
	}
}
