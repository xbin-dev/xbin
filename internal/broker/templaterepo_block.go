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

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// templateBlockKey is the manifest's template block (docs/overview/03-components.md §Templates).
const templateBlockKey = "template"

// templateBlockTrailer names, in a snapshot commit's message, the hash of
// the block the snapshot was made from.
const templateBlockTrailer = "Xbin-Template-Block: "

// repoManifest is a template's embedded manifest as its repo carries it: its
// "template" member replaced by the one carried (the repo's current
// xbin.json, nil for a new repo) or, when that has none, removed. A manifest
// that doesn't parse, or has no block, is taken as it is.
func repoManifest(embedded, carried []byte) []byte {
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
// block of embedded (the template's manifest) changed.
func templateBlockNote(embedded []byte) string {
	blk, _, _ := jsonc.TopLevel(embedded, templateBlockKey)
	mode := "none (unpartitioned)"
	if raw, has, err := jsonc.TopLevel(blk, "partition"); err == nil && has {
		var v any
		if json.Unmarshal(jsonc.Strip(raw), &v) == nil {
			b, _ := json.Marshal(v)
			mode = string(b)
		}
	}
	return "The template's \"template\" block changed — what new instances are made from; " +
		"they now start with partition " + mode + ". An instance never carries that block " +
		"(instantiation strips it), so this repository leaves it as it was and a merge never " +
		"touches your xbin.json for it: your instance keeps its own mode (/docs/partitions.md)."
}
