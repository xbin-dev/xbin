package checkpoint

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// rec is one record of `git ls-tree -r -t -l -z`.
func rec(mode, typ string, size int64, path string) string {
	sz := "-"
	if typ == "blob" {
		sz = fmt.Sprint(size)
	}
	return fmt.Sprintf("%s %s %s %7s\t%s\x00", mode, typ, strings.Repeat("ab", 20), sz, path)
}

// covers T2 — admission refuses hand-made trees that break tree hygiene: a
// "." or ".." component, .git in any case at any depth, an empty component,
// a gitlink, a mode a checkpoint never holds; it passes a sound tree, and
// applies every cap to what the listing says, reading no blob.
func TestAdmissionRefusesBadTrees(t *testing.T) {
	good := rec("040000", "tree", 0, "src") + rec("100644", "blob", 10, "src/a.js") + rec("100755", "blob", 20, "run.sh") +
		rec("120000", "blob", 11, "link") + rec("040000", "tree", 0, "src/deep") + rec("100644", "blob", 5, "src/deep/b.js")
	st, r := admit("apps/t", []byte(good), DefaultCaps())
	if r != nil {
		t.Fatalf("a sound tree was refused: %v", r)
	}
	if st.Entries != 6 || st.Bytes != 46 || st.Largest != 20 || st.LargestPath != "run.sh" || st.Depth != 2 || st.LongestPath != "src/deep/b.js" {
		t.Errorf("stats: %+v", st)
	}

	for _, c := range []struct{ name, listing, path string }{
		{"dotdot", rec("100644", "blob", 1, "../escape"), "../escape"},
		{"dotdot deep", rec("040000", "tree", 0, "a") + rec("100644", "blob", 1, "a/../../x"), "a/../../x"},
		{"dot", rec("100644", "blob", 1, "a/./b"), "a/./b"},
		{".GIT", rec("040000", "tree", 0, ".GIT") + rec("100644", "blob", 1, ".GIT/config"), ".GIT"},
		{".git deep", rec("100644", "blob", 1, "vendor/x/.Git/hooks/post-checkout"), "vendor/x/.Git/hooks/post-checkout"},
		{".git file", rec("100644", "blob", 1, "sub/.git"), "sub/.git"},
		{"empty component", rec("100644", "blob", 1, "a//b"), "a//b"},
		{"absolute", rec("100644", "blob", 1, "/etc/passwd"), "/etc/passwd"},
		{"gitlink", rec("160000", "commit", 0, "emb"), "emb"},
		{"group-writable mode", rec("100664", "blob", 1, "old"), "old"},
		{"mode and type disagree", rec("040000", "blob", 1, "odd"), "odd"},
		{"blob as tree", rec("100644", "tree", 0, "odd"), "odd"},
		{"malformed", "100644 blob nothex 1\tx\x00", "100644 blob nothex 1\tx"},
		{"no tab", "100644 blob " + strings.Repeat("ab", 20) + " 1 x\x00", ""},
	} {
		_, r := admit("apps/t", []byte(good+c.listing), DefaultCaps())
		if r == nil || r.Rule != RuleHygiene || !errors.Is(r, ErrRefused) {
			t.Errorf("%s: %v, want a tree-hygiene refusal", c.name, r)
			continue
		}
		if c.path != "" && (len(r.Paths) != 1 || r.Paths[0] != c.path) {
			t.Errorf("%s: names %q, want %q", c.name, r.Paths, c.path)
		}
		if !strings.Contains(r.Error(), "checkpoint of apps/t refused") {
			t.Errorf("%s: %v", c.name, r)
		}
	}

	// the caps, on the listing alone
	var many strings.Builder
	many.WriteString(rec("040000", "tree", 0, "node_modules"))
	for i := 0; i < 30; i++ {
		many.WriteString(rec("100644", "blob", 1000, fmt.Sprintf("node_modules/f%02d", i)))
	}
	many.WriteString(rec("100644", "blob", 5, "app.js"))
	deep := strings.Repeat("d/", 70)
	for _, c := range []struct {
		rule    string
		caps    func(*Caps)
		listing string
	}{
		{RuleEntries, func(c *Caps) { c.Entries = 10 }, many.String()},
		{RuleBytes, func(c *Caps) { c.Bytes = 10_000 }, many.String()},
		{RuleLargestFile, func(c *Caps) { c.LargestFile = 999 }, many.String()},
		{RulePathLen, func(c *Caps) {}, rec("100644", "blob", 1, strings.Repeat("p", 1025))},
		{RuleDepth, func(c *Caps) {}, rec("100644", "blob", 1, deep+"f")},
	} {
		caps := DefaultCaps()
		c.caps(&caps)
		_, r := admit("apps/t", []byte(c.listing), caps)
		if r == nil || r.Rule != c.rule {
			t.Errorf("%s: %v", c.rule, r)
			continue
		}
		if !strings.Contains(r.Error(), "over the cap") {
			t.Errorf("%s: the refusal doesn't name the cap: %v", c.rule, r)
		}
		if (c.rule == RuleEntries || c.rule == RuleBytes || c.rule == RuleLargestFile) &&
			(len(r.Paths) == 0 || !strings.HasPrefix(r.Paths[0], "node_modules/ (") || r.Hint != sizeHint) {
			t.Errorf("%s: the largest top-level directory and the way out: %v", c.rule, r)
		}
	}
	// at the caps exactly: admitted
	caps := DefaultCaps()
	caps.Entries, caps.LargestFile = 32, 1000
	if _, r := admit("apps/t", []byte(many.String()), caps); r != nil {
		t.Errorf("a tree at the caps: %v", r)
	}
	if _, r := admit("apps/t", []byte(rec("100644", "blob", 1, strings.Repeat("d/", 64)+"f")), DefaultCaps()); r != nil {
		t.Errorf("a file in a directory 64 deep: %v", r)
	}
}
