package util

import "testing"

// Every spelling of a path in the sealed namespace is seen as one: a
// test that looked at the exact prefix let "/xbin/x" through to code that
// trims, joins or resolves it into xbin/x.
func TestSealedPathSeesEverySpelling(t *testing.T) {
	sealed := []string{
		"xbin/x", "/xbin/x", "//xbin/x", "xbin//x", "xbin/x/", "./xbin/x", "/./xbin/x/", " xbin/x ", "\txbin/x\n",
		"apps/../xbin/x", "xbin/./x", "xbin/x/sub/file.js", "xbin/y/../x", "xbin\\x", "/xbin\\x/", "../xbin/x",
	}
	for _, p := range sealed {
		if !SealedPath(p) {
			t.Errorf("SealedPath(%q) = false", p)
		}
		if !SealedTarget(p) || !SealedTarget("res:"+p) || !SealedTarget("code:"+p) || !SealedTarget(" res:"+p) {
			t.Errorf("SealedTarget misses a form of %q", p)
		}
		if (p != "xbin/x" && p != "xbin/x/sub/file.js") != SealedSpelledOddly(p) {
			t.Errorf("SealedSpelledOddly(%q) = %v", p, SealedSpelledOddly(p))
		}
	}
	// other paths — and other BYTES: nothing in xbind folds case, escapes or
	// look-alike letters, so these name no sealed component anywhere
	for _, p := range []string{
		"", "xbin", "/xbin/", "xbin:llm", "xbin~x", "apps/xbin/x", "apps/x", "XBIN/x", "Xbin/x", "xbin%2Fx", "%78bin/x",
		"xbіn/x" /* Cyrillic і */, "x​bin/x" /* zero-width space */, "ｘbin/x" /* fullwidth x */, "xbin∕x", /* division slash */
	} {
		if SealedPath(p) || SealedSpelledOddly(p) {
			t.Errorf("SealedPath(%q) = true", p)
		}
	}
	for in, want := range map[string]string{
		"apps/a": "apps/a", "/apps/a/": "apps/a", "apps//a": "apps/a", "./apps/a": "apps/a", "apps/b/../a": "apps/a",
		" apps/a ": "apps/a", "": "", "/": "", ".": "", "../x": "x", "a/../../x": "x",
	} {
		if got := CanonPath(in); got != want {
			t.Errorf("CanonPath(%q) = %q, want %q", in, got, want)
		}
	}
}
