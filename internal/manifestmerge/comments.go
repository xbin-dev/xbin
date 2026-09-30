package manifestmerge

// comments.go — what a merge by keys can't carry is a conflict, not a loss.
// The merge edits ours' values; a comment outside the values upstream
// replaces or adds stays as ours has it. So when upstream changed comments
// — reworded one, dropped one next to a kept entry — and the result doesn't
// show that change, the merge is a conflict and git's line merge (with its
// markers) stands instead.
//
// A manifest without comments of its own takes none: the two-space form
// xbinds before T1 wrote (json.MarshalIndent can't hold a comment), and any
// manifest that dropped every comment its base had. Upstream's comment
// changes have no place in those — the manifest was rewritten without them.
// Likewise a top-level member ours dropped takes its comments along (the
// comment lines right above it, as instantiation drops the template block's
// with the block): upstream rewording them is nothing to ours.

import (
	"bytes"
	"strings"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// commentsCarried reports whether out — the merged manifest — carries
// upstream's change to the comments: counted comment by comment (text
// trimmed), out has what ours has, plus what upstream added, less what
// upstream removed.
func (m *merger) commentsCarried(out []byte) bool {
	co, cb := comments(m.o.src), comments(m.b.src)
	if m.norm || len(co) == 0 && len(cb) > 0 {
		return true
	}
	base, theirs := m.b.src, m.t.src
	have := map[string]bool{}
	for _, it := range m.o.root.items {
		have[it.key] = true
	}
	for _, it := range m.b.root.items {
		if have[it.key] {
			continue
		}
		if b, err := jsonc.DeleteTopLevelNoted(base, it.key); err == nil {
			base = b
		}
		if t, err := jsonc.DeleteTopLevelNoted(theirs, it.key); err == nil {
			theirs = t
		}
	}
	cb = comments(base)
	ct, cr := comments(theirs), comments(out)
	seen := map[string]bool{}
	for _, set := range []map[string]int{co, cb, ct, cr} {
		for c := range set {
			if seen[c] {
				continue
			}
			seen[c] = true
			if cr[c] != max(0, co[c]+ct[c]-cb[c]) {
				return false
			}
		}
	}
	return true
}

// comments counts src's comments by their text: a line comment trimmed, a
// block comment with each of its lines trimmed (so re-indenting one doesn't
// change it).
func comments(src []byte) map[string]int {
	out := map[string]int{}
	for i := 0; i < len(src); i++ {
		switch {
		case src[i] == '"':
			for i++; i < len(src) && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
			end := bytes.IndexByte(src[i:], '\n')
			if end < 0 {
				end = len(src) - i
			}
			out[strings.TrimSpace(string(src[i:i+end]))]++
			i += end
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			end := len(src)
			if j := bytes.Index(src[i+2:], []byte("*/")); j >= 0 {
				end = i + 2 + j + 2
			}
			lines := strings.Split(string(src[i:end]), "\n")
			for k, ln := range lines {
				lines[k] = strings.TrimSpace(ln)
			}
			out[strings.Join(lines, "\n")]++
			i = end - 1
		}
	}
	return out
}
