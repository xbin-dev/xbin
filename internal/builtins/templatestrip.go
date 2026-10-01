package builtins

// templatestrip.go — a template's manifest as its instance gets it: the
// "template" block taken out (with the comment introducing it, which is
// about the template) and the instance's partition mode put in
// (partition.go), as splices into the template's own JSONC — its other
// comments, key order and layout stay. The instance's xbin.json is then the
// served template repo's (templaterepo_block.go) plus one line, so a later
// `git merge template/main` meets upstream's manifest change line for line
// (docs/overview/03-components.md §Templates).
//
// xbinds before this one re-marshalled the manifest (json.MarshalIndent:
// comments gone, keys sorted), which made every upstream manifest change a
// conflict in every instance; the merge driver xbind names in an
// instance's repository (manifestmerge) takes those by keys. A manifest the
// splices can't handle still gets that old form (marshalStrip).

import (
	"bytes"
	"encoding/json"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// templateKey is the manifest's template block.
const templateKey = "template"

// stripTemplateBlock removes the top-level "template" key from a xbin.json so
// an instantiated copy is a normal, plugged-in component, and writes the
// block's "partition" as the copy's own when opts allows (instancePartition's
// rules) — on the line after the opening brace, where no upstream change is
// next to it. Best-effort: a manifest that doesn't parse, or has no block, is
// returned unchanged.
func stripTemplateBlock(data []byte, opts InstanceOpts) []byte {
	tpl, has, err := jsonc.TopLevel(data, templateKey)
	if err != nil || !has {
		return data
	}
	out := data
	if def := templatePartition(tpl); !opts.Partition || def != nil {
		out, err = jsonc.DeleteTopLevel(out, partitionKey)
		if err == nil && opts.Partition {
			out, err = partitionOnTop(out, def)
		}
	}
	if err == nil {
		out, err = jsonc.DeleteTopLevelNoted(out, templateKey)
	}
	if err != nil || !json.Valid(jsonc.Strip(out)) {
		return marshalStrip(data, opts)
	}
	return out
}

// PartitionOnTop is partitionOnTop: the served template repo puts the mode a
// template's update requests of its instances where instantiation puts an
// instance's own (D177; internal/broker templaterepo_block.go), so the two
// lines meet as one in a merge.
func PartitionOnTop(doc, def []byte) ([]byte, error) { return partitionOnTop(doc, def) }

// partitionOnTop is doc with `"partition": def` as its first member, on a
// line of its own right after the opening brace at the next line's indent —
// or, when the brace's line holds more than a comment, before the first
// member (jsonc.SetTopLevel).
func partitionOnTop(doc, def []byte) ([]byte, error) {
	open := firstToken(doc)
	if open < 0 || doc[open] != '{' {
		return jsonc.SetTopLevel(doc, partitionKey, def)
	}
	eol := bytes.IndexByte(doc[open:], '\n')
	if eol < 0 || len(bytes.TrimSpace(jsonc.Strip(doc[open+1:open+eol]))) > 0 {
		return jsonc.SetTopLevel(doc, partitionKey, def)
	}
	at := open + eol + 1
	next := doc[at:]
	indent := next[:len(next)-len(bytes.TrimLeft(next, " \t"))]
	if len(indent) == 0 || bytes.HasPrefix(bytes.TrimLeft(next, " \t"), []byte("}")) {
		indent = []byte("  ")
	}
	line := string(indent) + `"` + partitionKey + `": ` + string(def) + ",\n"
	out := append(append(append([]byte(nil), doc[:at]...), line...), doc[at:]...)
	return out, nil
}

// firstToken is the offset of doc's first byte that is neither blank nor
// in a comment, -1 when there is none.
func firstToken(doc []byte) int {
	flat := jsonc.Strip(doc)
	for i, c := range flat {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return i
		}
	}
	return -1
}

// marshalStrip is the strip as xbinds before T1 wrote it: the block
// deleted, the partition set, the rest re-marshalled.
func marshalStrip(data []byte, opts InstanceOpts) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(jsonc.Strip(data), &m) != nil {
		return data
	}
	tpl, ok := m[templateKey]
	if !ok {
		return data
	}
	delete(m, templateKey)
	instancePartition(m, tpl, opts)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return data
	}
	return append(out, '\n')
}
