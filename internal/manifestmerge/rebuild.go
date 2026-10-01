package manifestmerge

// rebuild.go — a container whose entries come and go. Each entry of a
// container written one entry per line owns its lines (the comments and
// blank lines above it included): an entry dropped takes them along, an
// entry added is a block of lines of its own, and the commas are put right
// afterwards — every entry but the last has one, and the last has one only
// when ours ended in a trailing comma.

import (
	"encoding/json"
	"strings"
)

// chunk is one entry's lines: text, with its value at [v0, v1) and its
// comma at comma (-1 when none), relative to text.
type chunk struct {
	text   string
	v0, v1 int
	comma  int
}

// chunks cuts n's lines into its entries' chunks, with the head (the
// opening bracket's line) and the tail (what follows the last entry: the
// closing bracket's line); ok is false when an entry doesn't start its own
// line or shares its last line with what follows.
func chunks(src []byte, n *node) (head, tail string, cs []chunk, ok bool) {
	start := restOfLine(src, n.start+1)
	if start < 0 {
		return "", "", nil, false
	}
	at := start
	for _, it := range n.items {
		if _, own := indentAt(src, it.start); !own || it.start < at {
			return "", "", nil, false
		}
		after := it.val.end
		if it.comma >= 0 {
			after = it.comma + 1
		}
		end := restOfLine(src, after)
		if end < 0 {
			return "", "", nil, false
		}
		c := chunk{text: string(src[at:end]), v0: it.val.start - at, v1: it.val.end - at, comma: -1}
		if it.comma >= 0 {
			c.comma = it.comma - at
		}
		cs = append(cs, c)
		at = end
	}
	return string(src[n.start:start]), string(src[at:n.end]), cs, true
}

// with is c with its value replaced by v.
func (c chunk) with(v string) chunk {
	d := len(v) - (c.v1 - c.v0)
	out := chunk{text: c.text[:c.v0] + v + c.text[c.v1:], v0: c.v0, v1: c.v1 + d, comma: c.comma}
	if c.comma >= 0 {
		out.comma += d
	}
	return out
}

// reindented is c with its lines moved from indent from to indent to.
func (c chunk) reindented(from, to string) chunk {
	pre := reindent(c.text[:c.v0], from, to, true)
	v := reindent(c.text[c.v0:c.v1], from, to, false)
	post := c.text[c.v1:] // the value's own line: nothing starts a line before its newline
	out := chunk{text: pre + v + post, v0: len(pre), v1: len(pre) + len(v), comma: -1}
	if c.comma >= 0 {
		out.comma = out.v1 + (c.comma - c.v1)
	}
	return out
}

// commaed is c with a comma after its value, or without one.
func (c chunk) commaed(want bool) chunk {
	switch {
	case want && c.comma < 0:
		return chunk{text: c.text[:c.v1] + "," + c.text[c.v1:], v0: c.v0, v1: c.v1, comma: c.v1}
	case !want && c.comma >= 0:
		return chunk{text: c.text[:c.comma] + c.text[c.comma+1:], v0: c.v0, v1: c.v1, comma: -1}
	}
	return c
}

// rebuild is ours' container o with plans applied and upstream's items
// (theirs' container t) added after the entries groups names.
func (m *merger) rebuild(o, t *node, plans []plan, groups map[int][]int) (string, bool) {
	if len(o.items) == 0 {
		return m.fill(o, t, groups), true
	}
	head, tail, own, ok := chunks(m.o.src, o)
	if !ok {
		return "", false
	}
	indent := m.itemIndent(m.o, o, 0)
	_, _, theirs, tLines := chunks(m.t.src, t)
	added := func(ti int) chunk {
		it := t.items[ti]
		tIndent := m.itemIndent(m.t, t, ti)
		if tLines && !m.norm {
			return theirs[ti].reindented(tIndent, indent) // upstream's lines, its comments with them
		}
		key := ""
		if o.kind == '{' {
			k, _ := json.Marshal(it.key)
			key = string(k) + ": "
		}
		v := m.render(it, indent, tIndent)
		return chunk{text: indent + key + v + "\n", v0: len(indent + key), v1: len(indent+key) + len(v), comma: -1}
	}
	var out []chunk
	for _, ti := range groups[-1] {
		out = append(out, added(ti))
	}
	for i := range o.items {
		if p := plans[i]; !p.drop {
			c := own[i]
			if p.set {
				c = c.with(p.text)
			}
			out = append(out, c)
		}
		for _, ti := range groups[i] {
			out = append(out, added(ti))
		}
	}
	trailing := o.items[len(o.items)-1].comma >= 0
	var b strings.Builder
	b.WriteString(head)
	for k, c := range out {
		b.WriteString(c.commaed(k < len(out)-1 || trailing).text)
	}
	b.WriteString(tail)
	return b.String(), true
}

// fill is ours' empty container o holding upstream's items: one per line
// below the line it opens on, two spaces in from it.
func (m *merger) fill(o, t *node, groups map[int][]int) string {
	src := m.o.src
	ls := lineStart(src, o.start)
	outer := string(src[ls:ls])
	for i := ls; i < o.start && (src[i] == ' ' || src[i] == '\t'); i++ {
		outer = string(src[ls : i+1])
	}
	indent := outer + "  "
	var entries []string
	for _, ti := range groups[-1] {
		it := t.items[ti]
		key := ""
		if o.kind == '{' {
			k, _ := json.Marshal(it.key)
			key = string(k) + ": "
		}
		entries = append(entries, indent+key+m.render(it, indent, m.itemIndent(m.t, t, ti)))
	}
	closing := "}"
	if o.kind == '[' {
		closing = "]"
	}
	return string(src[o.start:o.start+1]) + "\n" + strings.Join(entries, ",\n") + "\n" + outer + closing
}
