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
//     so `git merge template/main` can't bring one in;
//   - a builtin update (replace, merge or a proposal) writes each manifest
//     with the installed copy's "partition" — present, absent or its list —
//     as a single JSONC-aware splice (jsonc.SetTopLevel/DeleteTopLevel), and
//     says so when upstream asks otherwise.

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
// block tpl already taken out) up as the instance asks under opts.
func instancePartition(m map[string]json.RawMessage, tpl json.RawMessage, opts InstanceOpts) {
	if !opts.Partition {
		delete(m, partitionKey)
		return
	}
	if def := templatePartition(tpl); def != nil {
		m[partitionKey] = def
	}
}

// templatePartition is a template block's "partition" as written, nil when
// it has none (or null).
func templatePartition(tpl json.RawMessage) json.RawMessage {
	var t map[string]json.RawMessage
	if json.Unmarshal(jsonc.Strip(tpl), &t) != nil {
		return nil
	}
	if raw := t[partitionKey]; raw != nil && string(bytes.TrimSpace(raw)) != "null" {
		return raw
	}
	return nil
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

// ApplyMerge merges upstream file by file (applyMerge), each manifest
// keeping its installed "partition".
func (u *Updater) ApplyMerge(id string) (Applied, error) {
	var a Applied
	var err error
	a.Files, err = u.applyMerge(id, &a.Notes)
	return a, err
}

// keepPartition is theirs — a unit's rendered upstream files — with every
// manifest carrying the installed copy's "partition"; notes gains a line
// where upstream asks otherwise. A manifest with no installed copy is new,
// and upstream's stands.
func (u *Updater) keepPartition(installPath string, theirs map[string][]byte, notes *[]string) map[string][]byte {
	dir := filepath.Join(u.root, filepath.FromSlash(installPath))
	out, n := keepPartitionOf(installPath, theirs, func(rel string) ([]byte, bool) {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		return b, err == nil
	})
	*notes = append(*notes, n...)
	return out
}

// proposalPartition is the "to" side of a proposal's series (Propose): theirs
// with each manifest's "partition" as in base, the series' "from" side, so
// the patch never touches the key; and the notes, against the installed copy.
func (u *Updater) proposalPartition(installPath string, base, theirs map[string][]byte) (map[string][]byte, string) {
	to, _ := keepPartitionOf(installPath, theirs, func(rel string) ([]byte, bool) {
		b, ok := base[rel]
		return b, ok
	})
	var notes []string
	u.keepPartition(installPath, theirs, &notes)
	if len(notes) == 0 {
		return to, ""
	}
	return to, "\n\nThis proposal leaves the tile's partition mode as it is:\n  " + strings.Join(notes, "\n  ")
}

func keepPartitionOf(installPath string, theirs map[string][]byte, installed func(rel string) ([]byte, bool)) (map[string][]byte, []string) {
	out := make(map[string][]byte, len(theirs))
	var notes []string
	for rel, th := range theirs {
		out[rel] = th
		if path.Base(rel) != "xbin.json" {
			continue
		}
		ours, ok := installed(rel)
		if !ok {
			continue
		}
		kept, note := withPartitionOf(ours, th)
		out[rel] = kept
		if note != "" {
			notes = append(notes, installPath+"/"+rel+": "+note)
		}
	}
	sort.Strings(notes)
	return out, notes
}

// mergeKeeping is mergeFile for one of a unit's files. A manifest whose three
// sides don't agree on "partition" is merged without the key — so its lines
// never conflict, and upstream's never ride in — and gets ours' back
// afterwards, also beside conflict markers (the builder resolves those).
func mergeKeeping(rel string, ours, base, theirs []byte) ([]byte, error) {
	oRaw, oHas, err := jsonc.TopLevel(ours, partitionKey)
	if path.Base(rel) != "xbin.json" || err != nil { // unreadable: keepPartition's note says so
		return mergeFile(ours, base, theirs)
	}
	strip := !agrees(oRaw, oHas, base) || !agrees(oRaw, oHas, theirs)
	o := ours
	if strip {
		o, base, theirs = withoutPartition(ours), withoutPartition(base), withoutPartition(theirs)
	}
	merged, err := mergeFile(o, base, theirs)
	if err != nil {
		return merged, err
	}
	if _, _, perr := jsonc.TopLevel(merged, partitionKey); perr != nil { // conflict markers
		if strip && oHas {
			return insertFirst(merged, oRaw), nil
		}
		return merged, nil // no side's partition in it, or all three sides' same one
	}
	kept, _ := withPartitionOf(ours, merged)
	return kept, nil
}

// agrees reports whether doc's top-level "partition" is (has, raw) — present
// alike and equal as JSON.
func agrees(raw []byte, has bool, doc []byte) bool {
	r, h, err := jsonc.TopLevel(doc, partitionKey)
	return err == nil && h == has && canonical(r) == canonical(raw)
}

// withoutPartition is doc without its top-level "partition" (doc itself
// when it doesn't parse).
func withoutPartition(doc []byte) []byte {
	if out, err := jsonc.DeleteTopLevel(doc, partitionKey); err == nil {
		return out
	}
	return doc
}

// insertFirst puts `"partition": raw,` right after a manifest's opening
// brace, on its own line — for a merge result that doesn't parse (conflict
// markers), where no JSONC-aware splice can.
func insertFirst(doc, raw []byte) []byte {
	i := bytes.IndexByte(doc, '{')
	if i < 0 {
		return doc
	}
	indent := "  "
	if rest := doc[i+1:]; bytes.HasPrefix(rest, []byte("\n")) {
		line := rest[1:]
		if j := bytes.IndexByte(line, '\n'); j >= 0 {
			line = line[:j]
		}
		if w := len(line) - len(bytes.TrimLeft(line, " \t")); w > 0 {
			indent = string(line[:w])
		}
	}
	return []byte(string(doc[:i+1]) + "\n" + indent + `"` + partitionKey + `": ` + string(raw) + "," + string(doc[i+1:]))
}

// withPartitionOf is theirs with ours' top-level "partition" (present,
// absent or its list), and a note when they differed or ours can't be read.
// theirs is returned as it is when the two already agree.
func withPartitionOf(ours, theirs []byte) ([]byte, string) {
	tRaw, tHas, err := jsonc.TopLevel(theirs, partitionKey)
	if err != nil {
		return theirs, "" // upstream's own manifest doesn't parse (a merge's markers): nothing to keep it against
	}
	oRaw, oHas, err := jsonc.TopLevel(ours, partitionKey)
	if err != nil {
		return theirs, "the installed xbin.json doesn't parse, so its partition couldn't be carried over (upstream asks " +
			showPartition(tRaw, tHas) + "): check the key"
	}
	if oHas == tHas && canonical(oRaw) == canonical(tRaw) {
		return theirs, ""
	}
	var kept []byte
	if oHas {
		kept, err = jsonc.SetTopLevel(theirs, partitionKey, oRaw)
	} else {
		kept, err = jsonc.DeleteTopLevel(theirs, partitionKey)
	}
	if err != nil {
		return theirs, "the installed partition couldn't be carried over (" + err.Error() + "): check the key"
	}
	return kept, fmt.Sprintf("partition kept as installed (%s; upstream asks %s): edit it deliberately to request a switch",
		showPartition(oRaw, oHas), showPartition(tRaw, tHas))
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

func showPartition(raw []byte, has bool) string {
	if !has {
		return "none"
	}
	return canonical(raw)
}
