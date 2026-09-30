package manifestmerge

// ownpath.go — the template's own path in an instance's manifest.
// Instantiation at another path rewrote the template's path (apps/agent) to
// the instance's (apps/my-agent) in every text file, the manifest's
// `res:apps/agent/db` uses included; Merge gives the base and upstream the
// same rewrite. It can do that only when ours names the path the driver
// renames to — a copy of an instance (POST /clone rewrites its path again)
// whose driver still names the source's path doesn't — and without it
// upstream's new `res:apps/agent/…` entries would land verbatim: uses of
// another tile's resources, arriving with a clean-looking template update.

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// namesPath reports whether src names the component path p as a whole
// word — `res:apps/x/db` and `/api/apps/x/` do, `apps/x2` and `apps/x-y`
// don't (the boundaries POST /clone's rewrite uses).
func namesPath(src []byte, p string) bool {
	for i := 0; p != ""; {
		j := bytes.Index(src[i:], []byte(p))
		if j < 0 {
			return false
		}
		j += i
		end := j + len(p)
		if (j == 0 || !pathChar(src[j-1])) && (end >= len(src) || !pathChar(src[end])) {
			return true
		}
		i = j + 1
	}
	return false
}

func pathChar(c byte) bool {
	return c == '-' || c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// addsPath reports whether theirs has a string naming p (a key or a value)
// that base doesn't have as often: upstream adds references to p. A
// document that doesn't parse counts as adding (the merge fails on it
// anyway).
func addsPath(base, theirs []byte, p string) bool {
	b, okB := pathStrings(base, p)
	t, okT := pathStrings(theirs, p)
	if !okB || !okT {
		return true
	}
	for s, n := range t {
		if n > b[s] {
			return true
		}
	}
	return false
}

// pathStrings counts the JSON strings of src naming p.
func pathStrings(src []byte, p string) (map[string]int, bool) {
	out := map[string]int{}
	dec := json.NewDecoder(bytes.NewReader(jsonc.Strip(src)))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, true
		}
		if err != nil {
			return nil, false
		}
		if s, ok := tok.(string); ok && namesPath([]byte(s), p) {
			out[s]++
		}
	}
}
