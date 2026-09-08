package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reserved capability targets (cap:*, xbin, xbin:*) are never component
// paths — the import warning must not report them as "no such component"
// (it did for cap:containers / cap:net-admin); a missing resource still warns.
func TestUnresolvedUsesCapTargets(t *testing.T) {
	b := testBroker(t)
	dir := filepath.Join(b.Reg.Root, "apps", "linky")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mf := `{"uses":[{"target":"cap:open-links","role":"writer"},{"target":"cap:containers","role":"writer"},
		{"target":"xbin:users","role":"writer"},{"target":"res:nope/x","role":"reader"}]}`
	if err := os.WriteFile(filepath.Join(dir, "xbin.json"), []byte(mf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	warns := b.unresolvedUses("apps/linky")
	if len(warns) != 1 || !strings.Contains(warns[0], "res:nope/x") {
		t.Fatalf("want exactly the missing-resource warning, got %v", warns)
	}
}

func TestValidGitURL(t *testing.T) {
	ok := []string{
		"https://github.com/foo/bar", "https://github.com/foo/bar.git",
		"http://gitlab.local/x/y.git", "git://host/repo", "ssh://git@host/repo",
		"git@github.com:foo/bar.git",
	}
	bad := []string{
		"", "-oProxyCommand=evil", "file:///etc/passwd", "/local/path",
		"./relative", "ext::sh -c whoami", "fd::0", "just-text",
		"https://x\nhttps://y", // newline injection
	}
	for _, u := range ok {
		if !validGitURL(u) {
			t.Errorf("should accept %q", u)
		}
	}
	for _, u := range bad {
		if validGitURL(u) {
			t.Errorf("should REJECT %q", u)
		}
	}
}

func TestRepoNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/foo/my-tile.git": "my-tile",
		"https://github.com/foo/My_Tile":     "my-tile",
		"git@github.com:foo/bar.git":         "bar",
		"https://host/a/b/c/":                "c",
	}
	for in, want := range cases {
		if got := repoNameFromURL(in); got != want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupSortTags(t *testing.T) {
	// Annotated tags appear twice (name and name^{}); newest first, version-aware.
	in := []string{"v1.9.0", "v1.10.0", "v1.9.0", "v1.2.0", "v1.10.0"}
	got := dedupSortTags(in)
	want := []string{"v1.10.0", "v1.9.0", "v1.2.0"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
