package broker

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers PD-23 06§4 — "reviewed code only": an admin's switch, on only with
// the primary and every bound non-partitioned provider protected (409
// naming what isn't); while on, their unprotect is refused (the deployments
// plane's question), binding an unprotected provider into the tile is 409,
// and a gap that opens anyway is a trust warning; off frees them. Not an
// admin: 403.
func TestPartitionReviewedOnly(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	protected := map[string]bool{}
	b.DeploymentSummary = func(tile string) (string, bool, bool, bool) { return "main", true, protected[tile], true }
	bob, alice := personP(t, f.partWS, "bob"), personP(t, f.partWS, "alice")
	on := `{"tile":"apps/pg","on":true}`

	mustCode(t, call(t, b.apiPartitionReviewed, alice, "POST", "/", on, nil), 403, "a person who isn't an admin")
	out := mustCode(t, call(t, b.apiPartitionReviewed, bob, "POST", "/", on, nil), 409, "the primary unprotected")
	if !strings.Contains(out["error"].(string), "apps/pg") {
		t.Errorf("the refusal: %v", out)
	}
	mustCode(t, call(t, b.apiPartitionReviewed, bob, "POST", "/", `{"tile":"apps/x","on":true}`, nil), 409, "an unpartitioned tile")

	// a bound provider that isn't partitioned is in the trust base: protected too
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"apps/pg": {"svc": registry.BindTo("apps/x")}}
	}); err != nil {
		t.Fatal(err)
	}
	protected["apps/pg"] = true
	out = mustCode(t, call(t, b.apiPartitionReviewed, bob, "POST", "/", on, nil), 409, "a bound provider unprotected")
	if gaps, _ := out["unprotected"].([]any); len(gaps) != 1 || gaps[0] != "apps/x" {
		t.Errorf("unprotected %v", out["unprotected"])
	}
	protected["apps/x"] = true
	out = mustCode(t, call(t, b.apiPartitionReviewed, bob, "POST", "/", on, nil), 200, "everything protected")
	if ro, _ := out["reviewedOnly"].(map[string]any); ro["on"] != true || ro["by"] != "bob" {
		t.Errorf("the answer: %v", out)
	}

	// while on: neither primary unprotects; an unprotected provider can't be bound in
	for _, tile := range []string{"apps/pg", "apps/x"} {
		if why := b.ReviewedOnlyRequires(tile); !strings.Contains(why, "reviewed code only") {
			t.Errorf("%s may be unprotected: %q", tile, why)
		}
	}
	if why := b.ReviewedOnlyRequires("apps/q"); why != "" {
		t.Errorf("apps/q, unrelated, held protected: %q", why)
	}
	var c *bindConflict
	if err := b.reviewedOnlyBindRefusal("apps/pg", registry.BindTo("users/alice/mcp")); !errors.As(err, &c) {
		t.Errorf("binding an unprotected provider in: %v", err)
	}
	if err := b.reviewedOnlyBindRefusal("apps/pg", registry.BindTo("apps/x", "apps/pu")); err != nil {
		t.Errorf("a protected and a partitioned provider: %v", err)
	}
	_, list := f.get(alice, "?tile=apps/pg")
	if ro, _ := list["reviewedOnly"].(map[string]any); ro["on"] != true {
		t.Errorf("the listing's reviewedOnly: %v", list["reviewedOnly"])
	}
	if trust, _ := list["trust"].(map[string]any); trust["reviewedOnly"] != true {
		t.Errorf("the trust panel: %v", list["trust"])
	}

	// a gap that opens anyway (a provider's record removed): a warning
	protected["apps/x"] = false
	if w := b.trustWarnings("apps/pg"); len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), "reviewed code only") {
		t.Errorf("no warning for the gap: %q", w)
	}

	// off: the primaries are free again, the record gone
	mustCode(t, call(t, b.apiPartitionReviewed, bob, "POST", "/", `{"tile":"apps/pg","on":false}`, nil), 200, "off")
	if why := b.ReviewedOnlyRequires("apps/pg"); why != "" {
		t.Errorf("still held after off: %q", why)
	}
	if _, err := os.Stat(b.reviewedPath("apps/pg")); !os.IsNotExist(err) {
		t.Errorf("the record stays after off: %v", err)
	}

	// a record this xbind can't read counts as on
	if err := os.WriteFile(b.reviewedPath("apps/pg"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !b.reviewedOnlyOn("apps/pg") {
		t.Error("an unreadable record doesn't hold")
	}
}

// covers 06§7 AR-9 — the untracked files of a partitioned tile are its own
// repository's (a confined git in the tile, never the workspace root's): a
// file left in the tile is listed, a nested component's repository isn't;
// the admins' overview carries them on ?untracked=1 only.
func TestPartitionUntrackedFiles(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	dir := filepath.Join(f.root, "apps", "pg")
	if err := gitInitComponent(dir); err != nil {
		t.Skipf("no git here: %v", err)
	}
	f.write(map[string]string{"apps/pg/leftover.txt": "a person's file\n", "apps/pg/nested/xbin.json": `{"runtime":"go"}`})
	if err := gitInitComponent(filepath.Join(dir, "nested")); err != nil {
		t.Fatal(err)
	}
	files, err := b.untrackedInTile("apps/pg")
	if err != nil || !slices.Equal(files, []string{"leftover.txt"}) {
		t.Fatalf("untracked in apps/pg: %q %v", files, err)
	}
	bob := personP(t, f.partWS, "bob")
	pg := func(q string) map[string]any {
		_, out := f.get(bob, q)
		for _, r := range out["tiles"].([]any) {
			if row := r.(map[string]any); row["tile"] == "apps/pg" {
				return row
			}
		}
		t.Fatalf("no apps/pg in the overview")
		return nil
	}
	if row := pg(""); row["untracked"] != nil {
		t.Errorf("untracked listed without asking: %v", row["untracked"])
	}
	if row := pg("?untracked=1"); row["untrackedCount"] != float64(1) {
		t.Errorf("the overview's untracked: %v %v", row["untracked"], row["untrackedCount"])
	}
}
