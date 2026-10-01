package builtins

// partition.go — templates and updates never switch a partition mode
// (plans/partitions/01-manifest-registry.md §2.7; PD-35, PD-52;
// docs/partitions.md). A tile's top-level "partition" is a request that,
// once the tile holds data, pauses it for a manager's decision, so nothing
// but a deliberate edit may add, remove or change it:
//
//   - a template names the mode its NEW instances start in inside its
//     "template" block ("template": {"partition": [...]}), which
//     instantiation strips; the copy gets it as its top-level key unless the
//     request opted out or xbind runs without isolation (InstanceOpts). A
//     template's top level never carries it (TestTemplatesNoTopLevelPartition),
//     so `git merge template/main` can't bring one in — unless the block
//     sets "partitionOnUpdate" (the agent template's, D177): then, under
//     isolation, the served repo asks every instance for the block's mode
//     (internal/broker templaterepo_block.go);
//   - a builtin replace writes each manifest with the installed copy's
//     "partition" — present, absent or its list — as one JSONC-aware splice
//     (jsonc.SetTopLevelLike/DeleteTopLevel), read from the file, from its
//     own side of the conflict markers an earlier merge left, or, when the
//     file can't be read, from the mode xbind last read from the tile's code
//     (SetRecordedPartition); when none of those can tell, the manifest is
//     left as it is and the update stays offered;
//   - a merge and a proposal diff upstream from the recorded base with
//     upstream's own change to the key undone (undoPartition), so git never
//     brings upstream's value in and carries the builder's line untouched.
//
// Every one says so (Notes) when upstream asks otherwise. Keys match in any
// case, as xbind reads a manifest ("Partition" is the key too).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// partitionKey is the manifest's partition request (docs/partitions.md).
const partitionKey = "partition"

// InstanceOpts is how a template is copied into an instance.
type InstanceOpts struct {
	// Partition writes the template's "template.partition" as the
	// instance's top-level "partition": the mode it starts in (PD-35). The
	// caller leaves it off when the request opted out or xbind runs without
	// isolation (PD-19); the instance then asks for no mode at all — a
	// top-level "partition" the template itself carries goes too.
	Partition bool
}

// instancePartition sets m (a template manifest's top level, its template
// block tpl already taken out) up as the instance asks under opts. Keys
// match in any case, as the registry reads them.
func instancePartition(m map[string]json.RawMessage, tpl json.RawMessage, opts InstanceOpts) {
	def := templatePartition(tpl)
	if opts.Partition && def == nil {
		return // the template names no default: its manifest stands as written
	}
	for k := range m {
		if strings.EqualFold(k, partitionKey) {
			delete(m, k)
		}
	}
	if opts.Partition {
		m[partitionKey] = def
	}
}

// templatePartition is a template block's "partition" as written, nil when
// it has none (or null).
func templatePartition(tpl json.RawMessage) json.RawMessage {
	raw, has, err := jsonc.TopLevel(tpl, partitionKey)
	if err != nil || !has || string(bytes.TrimSpace(jsonc.Strip(raw))) == "null" {
		return nil
	}
	return raw
}

// partitionWords is a template block's "partition" value as a list of
// words for the catalog (nil when it has none or it isn't a list of strings:
// an instance still carries it as written, and the registry judges it).
func partitionWords(raw json.RawMessage) []string {
	var words []string
	if json.Unmarshal(raw, &words) != nil || len(words) == 0 {
		return nil
	}
	return words
}

// Applied is what a builtin update wrote, and what it kept of the installed
// copy (Notes: a manifest's "partition" kept as installed, PD-52).
type Applied struct {
	Files []string
	Notes []string
}

// ApplyReplace takes upstream wholesale (applyReplace), each manifest
// keeping its installed "partition".
func (u *Updater) ApplyReplace(id string) (Applied, error) {
	var a Applied
	var err error
	a.Files, err = u.applyReplace(id, &a.Notes)
	return a, err
}

// ApplyMerge merges upstream file by file (applyMerge), never taking
// upstream's change to a manifest's "partition".
func (u *Updater) ApplyMerge(id string) (Applied, error) {
	var a Applied
	var err error
	a.Files, err = u.applyMerge(id, &a.Notes)
	return a, err
}

// SetRecordedPartition installs how the updater learns the "partition" xbind
// last read from a tile's code (the broker's mode store): raw is the value
// as a manifest writes it, has whether there is one, ok false when xbind
// can't tell. It stands in for an installed manifest that doesn't parse.
func (u *Updater) SetRecordedPartition(f func(tile string) (raw []byte, has, ok bool)) {
	u.recorded = f
}

// pval is a manifest's top-level "partition": its value when present.
type pval struct {
	raw []byte
	has bool
}

func (p pval) same(q pval) bool { return p.has == q.has && canonical(p.raw) == canonical(q.raw) }

func (p pval) String() string {
	if !p.has {
		return "none"
	}
	return canonical(p.raw)
}

// readPartition is doc's partition; ok is false when doc doesn't parse.
func readPartition(doc []byte) (pval, bool) {
	raw, has, err := jsonc.TopLevel(doc, partitionKey)
	return pval{raw, has}, err == nil
}

// setPartition is doc carrying v — spliced where like has it when doc
// lacks the key — and doc itself when it already does.
func setPartition(doc []byte, v pval, like []byte) ([]byte, error) {
	if cur, ok := readPartition(doc); ok && cur.same(v) {
		return doc, nil
	}
	if v.has {
		return jsonc.SetTopLevelLike(doc, partitionKey, v.raw, like)
	}
	return jsonc.DeleteTopLevel(doc, partitionKey)
}

func isManifest(rel string) bool { return path.Base(rel) == "xbin.json" }

// How an installed manifest's partition was read (installedPartition).
const (
	readFile    = "file"
	readMarkers = "markers"
	readRecord  = "record"
)

// installedPartition is the "partition" of ours, the installed manifest at
// installPath/rel; how it was read: from the file, from its own side of the
// conflict markers a merge left, or — the file can't be read — from the
// mode xbind last read from the tile's code; and the document it was read
// from (nil for the record). how is "" when none of them can tell.
func (u *Updater) installedPartition(installPath, rel string, ours []byte) (v pval, how string, from []byte) {
	if v, ok := readPartition(ours); ok {
		return v, readFile, ours
	}
	if side, ok := conflictSide(ours, sideOurs); ok {
		if v, ok := readPartition(side); ok {
			return v, readMarkers, side
		}
	}
	if u.recorded != nil {
		if raw, has, ok := u.recorded(path.Join(installPath, path.Dir(rel))); ok {
			return pval{raw, has}, readRecord, nil
		}
	}
	return pval{}, "", nil
}

// keptNote says an update kept the installed partition inst (read as how)
// where upstream asks up.
func keptNote(installPath, rel string, inst pval, how string, up pval) string {
	was := inst.String()
	switch how {
	case readMarkers:
		was += ", read from its own side of the conflict markers"
	case readRecord:
		was += ", as xbind last read it: the file doesn't parse"
	case "":
		was = "unknown: the file doesn't parse"
	}
	return fmt.Sprintf("%s/%s: partition kept as installed (%s; upstream asks %s): edit it deliberately to request a switch",
		installPath, rel, was, up)
}

// keepPartition is theirs — a unit's rendered upstream files — for a
// replace: every manifest carrying the installed copy's "partition", spliced
// where the installed copy has it; notes gains a line where upstream asks
// otherwise. A manifest with no installed copy is new, and upstream's
// stands. whole is false when a manifest was left out — its installed
// partition can't be told — so the caller keeps the update offered.
func (u *Updater) keepPartition(installPath string, theirs map[string][]byte, notes *[]string) (out map[string][]byte, whole bool) {
	dir := filepath.Join(u.root, filepath.FromSlash(installPath))
	out, whole = make(map[string][]byte, len(theirs)), true
	var n []string
	for rel, th := range theirs {
		out[rel] = th
		if !isManifest(rel) {
			continue
		}
		ours, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		up, upOK := readPartition(th)
		if err != nil || !upOK {
			continue // no installed copy (an install), or upstream's own can't be read
		}
		inst, how, like := u.installedPartition(installPath, rel, ours)
		kept, err := setPartition(th, inst, like)
		if how == "" || err != nil {
			delete(out, rel)
			whole = false
			n = append(n, installPath+"/"+rel+": the installed xbin.json doesn't parse and xbind has no record of its partition, "+
				"so it was left as it is and the update stays offered: fix it, then update again")
			continue
		}
		out[rel] = kept
		if !inst.same(up) {
			n = append(n, keptNote(installPath, rel, inst, how, up))
		}
	}
	sort.Strings(n)
	*notes = append(*notes, n...)
	return out, whole
}

// undoPartition is theirs (a manifest) with upstream's change to its
// "partition" undone against base, the manifest the change is diffed from:
// base's value, put where base has it, or no key when base has none — so a
// 3-way merge or a patch from base never brings upstream's value in, and
// the installed line (the builder's own edit included) merges untouched.
// When base can't be read the installed value stands in (inst); ok is false
// when that can't be told either. changed says upstream changed the key.
func undoPartition(base, theirs []byte, inst func() (pval, string, []byte)) (out []byte, changed, ok bool) {
	up, upOK := readPartition(theirs)
	if !upOK {
		return theirs, false, true // upstream's own manifest doesn't parse: nothing to undo
	}
	like := base
	b, bOK := readPartition(base)
	if !bOK {
		v, how, from := inst()
		if how == "" {
			return nil, true, false
		}
		b, like = v, from
	}
	if b.same(up) {
		return theirs, false, true
	}
	out, err := setPartition(theirs, b, like)
	return out, true, err == nil
}

// mergeTheirs is theirs for a merge (applyMerge): every manifest that has an
// installed copy with upstream's change to "partition" undone against the
// recorded base (snap), and notes where upstream asked another value than
// the installed one. A manifest whose change can't be undone (neither its
// base nor its installed copy can be read) is the installed copy itself —
// left as it is — and whole is false, so the caller keeps the update
// offered.
func (u *Updater) mergeTheirs(installPath, snap string, theirs map[string][]byte, notes *[]string) (out map[string][]byte, whole bool) {
	dir := filepath.Join(u.root, filepath.FromSlash(installPath))
	out, whole = make(map[string][]byte, len(theirs)), true
	var n []string
	for rel, th := range theirs {
		out[rel] = th
		if !isManifest(rel) {
			continue
		}
		ours, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			continue // no installed copy: an install, upstream's stands
		}
		base, _ := os.ReadFile(filepath.Join(snap, filepath.FromSlash(rel)))
		inst := func() (pval, string, []byte) { return u.installedPartition(installPath, rel, ours) }
		undone, changed, ok := undoPartition(base, th, inst)
		if !ok {
			out[rel], whole = ours, false
			n = append(n, installPath+"/"+rel+": neither the installed xbin.json nor its recorded base can be read, "+
				"so it was left as it is and the update stays offered: fix it, then update again")
			continue
		}
		out[rel] = undone
		if up, _ := readPartition(th); changed {
			if v, how, _ := inst(); !v.same(up) || how == "" {
				n = append(n, keptNote(installPath, rel, v, how, up))
			}
		}
	}
	sort.Strings(n)
	*notes = append(*notes, n...)
	return out, whole
}

// proposalPartition is the "to" side of a proposal's series (Propose):
// theirs with upstream's change to each manifest's "partition" undone
// against base, the series' "from" side, so the patch never touches the key
// (a manifest whose change can't be undone keeps base's bytes); and the
// notes, against the installed copy.
func (u *Updater) proposalPartition(installPath string, base, theirs map[string][]byte) (map[string][]byte, []string) {
	dir := filepath.Join(u.root, filepath.FromSlash(installPath))
	to := make(map[string][]byte, len(theirs))
	var notes []string
	for rel, th := range theirs {
		to[rel] = th
		b, inBase := base[rel]
		if !isManifest(rel) || !inBase {
			continue
		}
		ours, oursErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		inst := func() (pval, string, []byte) {
			if oursErr != nil {
				return pval{}, "", nil
			}
			return u.installedPartition(installPath, rel, ours)
		}
		undone, changed, ok := undoPartition(b, th, inst)
		if !ok {
			undone = b
		}
		to[rel] = undone
		if up, _ := readPartition(th); changed && oursErr == nil {
			if v, how, _ := inst(); !v.same(up) || how == "" {
				notes = append(notes, keptNote(installPath, rel, v, how, up))
			}
		}
	}
	sort.Strings(notes)
	return to, notes
}

// proposalNote is the paragraph a proposal's message gains for notes.
func proposalNote(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "\n\nThis proposal leaves the tile's partition mode as it is:\n  " + strings.Join(notes, "\n  ")
}

// sameFiles reports whether two file sets are byte-identical.
func sameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for rel, x := range a {
		if y, ok := b[rel]; !ok || !bytes.Equal(x, y) {
			return false
		}
	}
	return true
}

// canonical is a JSONC value as compact JSON ("" when absent or unreadable).
func canonical(raw []byte) string {
	var v any
	if raw == nil || json.Unmarshal(jsonc.Strip(raw), &v) != nil {
		return string(bytes.TrimSpace(raw))
	}
	b, _ := json.Marshal(v)
	return string(b)
}
