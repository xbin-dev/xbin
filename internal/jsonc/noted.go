package jsonc

// noted.go — deleting a member together with the comment that introduces it.

import (
	"sort"
	"strings"
)

// DeleteTopLevelNoted is DeleteTopLevel with each deleted member's own
// comment going too: the line comments on the lines directly above it (no
// blank line between) when the member starts its line. A template's
// "template" block is introduced that way — a comment about the template,
// which its instance isn't.
func DeleteTopLevelNoted(src []byte, key string) ([]byte, error) {
	d, err := parseDoc(src)
	if err != nil {
		return nil, err
	}
	type cut struct{ from, to int }
	var cuts []cut
	for _, m := range d.members {
		if !keyIs(m.key, key) {
			continue
		}
		ls := lineStart(src, m.keyStart)
		if strings.TrimSpace(string(src[ls:m.keyStart])) != "" {
			continue
		}
		from := ls
		for from > 0 {
			prev := lineStart(src, from-1)
			if !strings.HasPrefix(strings.TrimSpace(string(src[prev:from])), "//") {
				break
			}
			from = prev
		}
		if from < ls && from > d.open {
			cuts = append(cuts, cut{from, ls})
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].from > cuts[j].from })
	out := append([]byte(nil), src...)
	for _, c := range cuts {
		out = append(out[:c.from], out[c.to:]...)
	}
	return DeleteTopLevel(out, key)
}
