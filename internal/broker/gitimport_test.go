package broker

import "testing"

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
