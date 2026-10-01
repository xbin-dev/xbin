package broker

// templaterepo_block.go — a builtin template's "template" block in its served
// repo (templaterepo.go; plans/partitions/08 §10; docs/partitions.md).
//
// Instantiation strips xbin.json's "template" block (builtins' CopyTree): an
// instance never carries it. So the served repo — every instance's
// `template` remote — never changes it either: a repo this xbind writes first
// carries none, and one an older xbind wrote keeps the block it has,
// verbatim. A change to the block (the mode new instances start in, their
// default name, the catalog's words) is then no change to any file an
// instance merges: `git merge template/main` never conflicts on a block the
// instance doesn't have, and never brings a "partition" in. The change is
// reported instead, as information: the snapshot commit that first sees it
// says so (an empty commit when nothing else changed), carrying the block's
// hash as a trailer, so it is said once.
//
// One exception, the owner's (D177, amending PD-52 for the agent template):
// a block with "partitionOnUpdate": true asks every instance for its
// partition through the served repo. Under --isolate the repo's manifest
// then carries the block's "partition" as its top-level key, on the line
// after the opening brace — where instantiation puts an instance's own — so
// an instance that merges the update gains it (a new instance already has
// that very line): on a tile holding data that is a mode-switch request a
// manager decides (PD-44: the switch deletes its data, "Keep the current
// mode" declines), on an empty one the mode itself. Once served it stays as
// served, whatever a later template or xbind says — a removal or a change
// would ask partitioned instances for another switch — and without
// --isolate (no person's partition can run) a repo that never served it
// doesn't start: its instances stay one instance, as before.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/jsonc"
)

// templateBlockKey is the manifest's template block (docs/overview/03-components.md §Templates).
const templateBlockKey = "template"

// templateUpdateKey in a template block asks every instance for the
// block's partition through the served repo (D177).
const templateUpdateKey = "partitionOnUpdate"

// templateBlockTrailer names, in a snapshot commit's message, the hash of
// the block the snapshot was made from.
const templateBlockTrailer = "Xbin-Template-Block: "

// repoManifest is a template's embedded manifest as its repo carries it: its
// "template" member replaced by the one carried (the repo's current
// xbin.json, nil for a new repo) or, when that has none, removed; and the
// partition the repo asks every instance for (requestedPartition) as its
// top-level "partition". A manifest that doesn't parse, or has no block, is
// taken as it is.
func repoManifest(embedded, carried []byte) []byte {
	out := servedBlock(embedded, carried)
	if def := requestedPartition(embedded, carried, templatesIsolated()); def != nil {
		if o, err := jsonc.DeleteTopLevel(out, "partition"); err == nil {
			if o, err = builtins.PartitionOnTop(o, def); err == nil {
				out = o
			}
		}
	}
	return out
}

// servedBlock is embedded with the carried block, or none (repoManifest).
func servedBlock(embedded, carried []byte) []byte {
	if _, has, err := jsonc.TopLevel(embedded, templateBlockKey); err != nil || !has {
		return embedded
	}
	if blk, has, err := jsonc.TopLevel(carried, templateBlockKey); err == nil && has {
		if out, err := jsonc.SetTopLevel(embedded, templateBlockKey, blk); err == nil {
			return out
		}
	}
	// as instantiation strips it (builtins' templatestrip.go): the block and
	// the comment introducing it, so an instance is this plus its partition
	if out, err := jsonc.DeleteTopLevelNoted(embedded, templateBlockKey); err == nil {
		return out
	}
	return embedded
}

// requestedPartition is the top-level "partition" a template's served repo
// asks every instance for (D177): the one it carries already, as it is —
// never changed or removed — else, under --isolate (isolated), the
// embedded block's "partition" when the block sets partitionOnUpdate; nil
// for none.
func requestedPartition(embedded, carried []byte, isolated bool) []byte {
	if raw, has, err := jsonc.TopLevel(carried, "partition"); err == nil && has {
		return raw
	}
	if !isolated {
		return nil
	}
	blk, has, err := jsonc.TopLevel(embedded, templateBlockKey)
	if err != nil || !has {
		return nil
	}
	on, has, err := jsonc.TopLevel(blk, templateUpdateKey)
	if err != nil || !has || strings.TrimSpace(string(jsonc.Strip(on))) != "true" {
		return nil
	}
	def, has, err := jsonc.TopLevel(blk, "partition")
	if err != nil || !has || strings.TrimSpace(string(jsonc.Strip(def))) == "null" {
		return nil
	}
	return def
}

// servedPartition is the top-level "partition" a served manifest carries,
// as compact JSON ("" for none).
func servedPartition(manifest []byte) string {
	raw, has, err := jsonc.TopLevel(manifest, "partition")
	if err != nil || !has {
		return ""
	}
	var v any
	if json.Unmarshal(jsonc.Strip(raw), &v) != nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// templateRequestNote is the paragraph of the snapshot that first asks
// every instance for partition mode (compact JSON).
func templateRequestNote(mode string) string {
	return "This snapshot asks every instance of the template for partition " + mode +
		" (D177): merging it adds that \"partition\" to your xbin.json. On an instance that " +
		"holds data that is a partition mode switch request: the tile pauses until a manager " +
		"switches it — which deletes all its data — or keeps the current mode, which deletes " +
		"nothing; an empty instance takes the mode at once (/docs/partitions.md)."
}

// templateBlockHash is a hash of manifest's "template" block as JSON ("" when
// it has none or doesn't parse).
func templateBlockHash(manifest []byte) string {
	blk, has, err := jsonc.TopLevel(manifest, templateBlockKey)
	if err != nil || !has {
		return ""
	}
	var v any
	if json.Unmarshal(jsonc.Strip(blk), &v) != nil {
		return ""
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:12])
}

// lastBlockHash is the block hash the repo's last snapshot was made from:
// its trailer (msg, HEAD's message), else — a repo an older xbind wrote —
// the hash of the block it carries; "" for a new repo.
func lastBlockHash(msg string, carried []byte) string {
	for _, ln := range strings.Split(msg, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(ln), templateBlockTrailer); ok {
			return strings.TrimSpace(h)
		}
	}
	return templateBlockHash(carried)
}

// templateBlockNote is the paragraph a snapshot's message gains when the
// block of embedded (the template's manifest) changed; requested is the
// partition the served repo asks every instance for ("" for none).
func templateBlockNote(embedded []byte, requested string) string {
	blk, _, _ := jsonc.TopLevel(embedded, templateBlockKey)
	mode := "none (unpartitioned)"
	if raw, has, err := jsonc.TopLevel(blk, "partition"); err == nil && has {
		var v any
		if json.Unmarshal(jsonc.Strip(raw), &v) == nil {
			b, _ := json.Marshal(v)
			mode = string(b)
		}
	}
	head := "The template's \"template\" block changed — what new instances are made from; " +
		"they now start with partition " + mode + ". An instance never carries that block " +
		"(instantiation strips it), so this repository leaves it as it was"
	if requested != "" {
		return head + "; it asks every instance for partition " + requested + " on its own (/docs/partitions.md)."
	}
	return head + " and a merge never touches your xbin.json for it: your instance keeps its own mode (/docs/partitions.md)."
}
