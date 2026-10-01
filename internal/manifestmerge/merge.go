// Package manifestmerge merges a component manifest (xbin.json, JSONC)
// three ways by its keys rather than its lines — the merge driver xbind
// configures in every builtin template instance's repository, so a
// builder's `git merge template/main` takes the template's manifest changes
// (docs/overview/03-components.md §Templates; bx template merge-manifest).
//
// Why: an instance's xbin.json is the template's, rewritten when it was
// made — by xbinds before this one re-marshalled (json.MarshalIndent: every
// comment gone, keys sorted, each entry spread over lines), and with the
// template's own path renamed to the instance's. Next to the template's
// JSONC every line differs, so git's line merge conflicts on *any* change
// upstream makes to the manifest, however small. By keys, the same merge is
// plain: a value upstream changed and the instance didn't is upstream's; a
// value the instance changed and upstream didn't is the instance's; only a
// value both changed differently is a conflict.
//
// The rules (Merge):
//   - top-level "template" and "partition" (any case) are never taken from
//     upstream: an instance never carries the template block, and its
//     partition mode changes only by a deliberate edit (docs/partitions.md).
//     Upstream changing one is a conflict — unless it is a block ours
//     doesn't carry (the base had it and ours dropped it, as instances do),
//     or upstream adds a "partition" where neither the base nor ours names
//     one: the served repo of a template whose update requests its
//     instances' mode (D177) — taken, as git's line merge would;
//   - an object merges member by member, an array element by element — an
//     element is known by its "target" when it is an object with one (a
//     `uses` entry), else by its whole value — and a value both sides
//     changed merges inside when both are objects or both arrays;
//   - the result is ours, edited in place: every byte the merge doesn't
//     change stays as it is; what comes from upstream is written the way
//     ours is — re-indented from upstream's text (its comments with it), or
//     in the two-space form when ours is in that form (a new key then goes
//     where json.MarshalIndent would put it), so an unedited instance merges
//     to exactly what instantiation would have written;
//   - what a merge by keys can't carry is a conflict, never dropped:
//     upstream's change to the comments of a manifest that carries comments
//     (comments.go), and upstream's new references to the template's own
//     path when ours doesn't name the path they'd be renamed to (Options).
//
// Merge doesn't know which side is the template: its caller (bx template
// merge-manifest) asks it only when "theirs" is — a merge of the
// template's own versions, never a builder's branch, a rebase or a stash.
package manifestmerge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// Options is how the instance was made from the template.
type Options struct {
	// From and To are instantiation's own-path rewrite (the template's
	// default path, e.g. apps/agent, and the instance's — the same for an
	// instance at the template's path): the base's and upstream's text get
	// it too, when ours names To, before they are compared with ours. When
	// ours doesn't name To (a copy of an instance whose driver still names
	// the source's path) and upstream adds references to From, the merge is
	// a conflict: they would land naming another tile. Both empty: no
	// rename is known, none is checked.
	From, To string
}

// Result is a merge's manifest and the places upstream's change was taken.
type Result struct {
	Out  []byte
	Took []string
}

// ConflictError is a merge where both sides changed the same values
// differently (Paths), or where upstream's change can't be written by keys
// (Notes: a kept key, comments, the template's own path).
type ConflictError struct {
	Paths []string
	Notes []string
}

func (e *ConflictError) Error() string {
	var parts []string
	if len(e.Paths) > 0 {
		parts = append(parts, "both sides changed "+strings.Join(e.Paths, ", "))
	}
	return strings.Join(append(parts, e.Notes...), "; ")
}

// kept are the top-level keys whose upstream change a merge never takes.
var kept = []string{"template", "partition"}

// Merge merges theirs' change from base into ours by keys (the package
// comment). A *ConflictError says what both sides changed; any other error
// is a document that doesn't parse.
func Merge(base, ours, theirs []byte, o Options) (Result, error) {
	if o.From != "" && o.To != "" {
		switch {
		case namesPath(ours, o.To):
			if o.From != o.To {
				base = bytes.ReplaceAll(base, []byte(o.From), []byte(o.To))
				theirs = bytes.ReplaceAll(theirs, []byte(o.From), []byte(o.To))
			}
		case addsPath(base, theirs, o.From):
			return Result{}, &ConflictError{Notes: []string{fmt.Sprintf(
				"upstream adds references to the template's own path %s, to be renamed to %s — a path this manifest doesn't name (a copy of another instance? xbind names the copy's own path in its driver at start)",
				o.From, o.To)}}
		}
	}
	var docs [3]*doc
	for i, side := range []struct {
		name string
		src  []byte
	}{{"the base", base}, {"ours", ours}, {"theirs", theirs}} {
		d, err := parse(side.src)
		if err != nil || d.root.kind != '{' {
			return Result{}, fmt.Errorf("%s isn't a JSONC object", side.name)
		}
		docs[i] = d
	}
	m := &merger{b: docs[0], o: docs[1], t: docs[2], norm: normalized(ours)}
	text, changed := m.merge("", docs[0].root, docs[1].root, docs[2].root, true)
	if len(m.conflicts) > 0 || len(m.notes) > 0 {
		return Result{}, &ConflictError{Paths: m.conflicts, Notes: m.notes}
	}
	out := ours
	if changed {
		root := docs[1].root
		out = append(append(append([]byte(nil), ours[:root.start]...), text...), ours[root.end:]...)
		if !json.Valid(jsonc.Strip(out)) {
			return Result{}, errors.New("manifestmerge: the merged manifest doesn't parse")
		}
	}
	if !m.commentsCarried(out) {
		return Result{}, &ConflictError{Notes: []string{"upstream changed comments, which a merge by keys doesn't carry"}}
	}
	return Result{Out: out, Took: m.took}, nil
}

type merger struct {
	b, o, t   *doc
	norm      bool // ours is in the two-space form
	took      []string
	conflicts []string
	notes     []string // upstream's changes that can't be taken (ConflictError.Notes)
}

// plan is what becomes of one of ours' entries.
type plan struct {
	drop bool
	set  bool   // text replaces the value
	text string //
}

// merge is the text of ours' container o with upstream's change from b to
// t merged in, and whether it changed; conflicts are recorded on m.
func (m *merger) merge(path string, b, o, t *node, root bool) (string, bool) {
	bIDs, bIdx, bOK := m.ids(m.b, b)
	tIDs, tIdx, tOK := m.ids(m.t, t)
	_, oIdx, oOK := m.ids(m.o, o)
	if !bOK || !tOK || !oOK {
		m.conflicts = append(m.conflicts, orRoot(path)+" (an entry appears twice)")
		return "", false
	}
	union := append([]string(nil), bIDs...)
	for _, id := range tIDs {
		if _, ok := bIdx[id]; !ok {
			union = append(union, id)
		}
	}
	plans := make([]plan, len(o.items))
	var inserts []int // theirs' items to add
	changed := false
	for _, id := range union {
		bi, bok := bIdx[id]
		ti, tok := tIdx[id]
		oi, ook := oIdx[id]
		cb, ct, co := m.b.canon(val(b, bi, bok)), m.t.canon(val(t, ti, tok)), m.o.canon(val(o, oi, ook))
		if cb == ct || co == ct {
			continue
		}
		p := label(path, id)
		if root && isKept(id) {
			if strings.EqualFold(id, "k:"+kept[1]) && tok && !hasKey(bIdx, kept[1]) && !hasKey(oIdx, kept[1]) {
				// upstream asks every instance for a mode, and neither the
				// base nor ours names one: a template whose update requests
				// its instances' partition (D177) — taken, as git's line
				// merge takes it. Once ours names a mode (or the base did,
				// and ours took it out) it is ours alone again.
				m.took = append(m.took, p)
				changed = true
				inserts = append(inserts, ti)
				continue
			}
			// upstream's change to a block ours doesn't carry is nothing to ours
			if !(strings.EqualFold(id, "k:"+kept[0]) && bok && !ook) {
				m.notes = append(m.notes, "upstream changes "+p+", which a merge never takes from upstream (only your own edit changes it)")
			}
			continue
		}
		if co == cb {
			m.took = append(m.took, p)
			changed = true
			switch {
			case !tok:
				plans[oi].drop = true
			case !ook:
				inserts = append(inserts, ti)
			default:
				plans[oi] = plan{set: true, text: m.render(t.items[ti], m.itemIndent(m.o, o, oi), m.itemIndent(m.t, t, ti))}
			}
			continue
		}
		bv, ov, tv := val(b, bi, bok), val(o, oi, ook), val(t, ti, tok)
		if bv != nil && ov != nil && tv != nil && bv.kind != 0 && bv.kind == ov.kind && ov.kind == tv.kind {
			if text, ch := m.merge(p, bv, ov, tv, false); ch {
				plans[oi] = plan{set: true, text: text}
				changed = true
			}
			continue
		}
		m.conflicts = append(m.conflicts, p)
	}
	if !changed || len(m.conflicts) > 0 {
		return "", false
	}
	if len(inserts) == 0 && !anyDrop(plans) {
		return m.splice(o, plans), true
	}
	text, ok := m.rebuild(o, t, plans, m.anchors(o, t, plans, inserts, oIdx))
	if !ok {
		m.conflicts = append(m.conflicts, orRoot(path)+" (entries added or removed where its entries don't each sit on lines of their own)")
		return "", false
	}
	return text, true
}

func orRoot(path string) string {
	if path == "" {
		return "the manifest"
	}
	return path
}

func isKept(id string) bool {
	k, ok := strings.CutPrefix(id, "k:")
	if !ok {
		return false
	}
	for _, key := range kept {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// hasKey reports whether a container's ids name key, in any case (as xbind
// reads a manifest's keys).
func hasKey(idx map[string]int, key string) bool {
	for id := range idx {
		if strings.EqualFold(id, "k:"+key) {
			return true
		}
	}
	return false
}

func val(n *node, i int, ok bool) *node {
	if !ok {
		return nil
	}
	return n.items[i].val
}

func anyDrop(plans []plan) bool {
	for _, p := range plans {
		if p.drop {
			return true
		}
	}
	return false
}

// ids are the identities of n's entries in order and where each is: an
// object's by key (the last of duplicates, as encoding/json reads it), an
// array's by elementID; ok is false for an array holding one twice.
func (m *merger) ids(d *doc, n *node) (order []string, at map[string]int, ok bool) {
	at = make(map[string]int, len(n.items))
	for i, it := range n.items {
		id := "k:" + it.key
		if n.kind == '[' {
			id = elementID(d, it.val)
			if _, dup := at[id]; dup {
				return nil, nil, false
			}
		}
		if _, seen := at[id]; !seen {
			order = append(order, id)
		}
		at[id] = i
	}
	return order, at, true
}

// elementID is how an array element is known across versions: a `uses`
// entry (an object with a string "target") by its target, anything else by
// its whole value.
func elementID(d *doc, v *node) string {
	if v.kind == '{' {
		for _, it := range v.items {
			if it.key == "target" && it.val.kind == 0 {
				var s string
				if json.Unmarshal(d.flat[it.val.start:it.val.end], &s) == nil {
					return "t:" + s
				}
			}
		}
	}
	return "v:" + d.canon(v)
}

// label names an entry for a note or a conflict: uses[res:apps/x/db].
func label(path, id string) string {
	switch {
	case strings.HasPrefix(id, "k:"):
		if path == "" {
			return id[2:]
		}
		return path + "." + id[2:]
	case strings.HasPrefix(id, "t:"):
		return path + "[" + id[2:] + "]"
	}
	v := id[2:]
	if len(v) > 40 {
		v = v[:37] + "…"
	}
	return path + "[" + v + "]"
}

// itemIndent is the blanks before n's entry i on its line ("" when it
// doesn't start its line).
func (m *merger) itemIndent(d *doc, n *node, i int) string {
	ind, own := indentAt(d.src, n.items[i].start)
	if !own {
		return ""
	}
	return ind
}

// render is upstream's value of it written for ours at indent (upstream's
// entry sat at tIndent): in the two-space form when ours is in it,
// else upstream's own text re-indented.
func (m *merger) render(it item, indent, tIndent string) string {
	v := it.val
	if m.norm {
		c, err := json.Marshal(json.RawMessage(m.t.flat[v.start:v.end]))
		if err == nil {
			var buf bytes.Buffer
			if json.Indent(&buf, c, indent, "  ") == nil {
				return buf.String()
			}
		}
	}
	return reindent(string(m.t.src[v.start:v.end]), tIndent, indent, false)
}

// reindent moves every line of s that starts with from to start with to
// instead; the first line only when s starts a line.
func reindent(s, from, to string, first bool) string {
	if from == to {
		return s
	}
	lines := strings.SplitAfter(s, "\n")
	for i, ln := range lines {
		if (i > 0 || first) && strings.HasPrefix(ln, from) {
			lines[i] = to + ln[len(from):]
		}
	}
	return strings.Join(lines, "")
}

// splice is o's text with the planned values replaced (nothing added or
// removed).
func (m *merger) splice(o *node, plans []plan) string {
	var out strings.Builder
	last := o.start
	for i, p := range plans {
		if !p.set {
			continue
		}
		v := o.items[i].val
		out.Write(m.o.src[last:v.start])
		out.WriteString(p.text)
		last = v.end
	}
	out.Write(m.o.src[last:o.end])
	return out.String()
}

// anchors groups the items upstream added by the entry of ours each goes
// after (-1: before the first): after the nearest entry before it upstream
// that ours keeps — or, in the two-space form with ours' keys sorted, where
// json.MarshalIndent sorts it.
func (m *merger) anchors(o, t *node, plans []plan, inserts []int, oIdx map[string]int) map[int][]int {
	sort.Ints(inserts)
	groups := map[int][]int{}
	if m.norm && o.kind == '{' && sortedKeys(o) {
		for _, ti := range inserts {
			key, at := t.items[ti].key, -1
			for i, it := range o.items {
				if !plans[i].drop && it.key < key {
					at = i
				}
			}
			groups[at] = append(groups[at], ti)
		}
		for at := range groups {
			g := groups[at]
			sort.Slice(g, func(i, j int) bool { return t.items[g[i]].key < t.items[g[j]].key })
		}
		return groups
	}
	for _, ti := range inserts {
		at := -1
		for j := ti - 1; j >= 0; j-- {
			id := "k:" + t.items[j].key
			if t.kind == '[' {
				id = elementID(m.t, t.items[j].val)
			}
			if oi, ok := oIdx[id]; ok && !plans[oi].drop {
				at = oi
				break
			}
		}
		groups[at] = append(groups[at], ti)
	}
	return groups
}

func sortedKeys(o *node) bool {
	for i := 1; i < len(o.items); i++ {
		if o.items[i-1].key >= o.items[i].key {
			return false
		}
	}
	return len(o.items) > 1
}
