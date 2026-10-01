// sandbox_partition.go — coding sandboxes in a partitioned agent (API.md
// "Partitioned instances"; docs/sandbox-manager.md §Partitioned consumers).
//
//   - A person's partition calls its managers as itself: xbind adds
//     X-XBin-Partition and X-XBin-Partition-Id, and a manager whose hello
//     offers `partitions` homes what the partition makes there, where no
//     other person's partition — nor the global instance — can list or use
//     it. A manager without the capability would see every person's
//     partition as one consumer, so a person's partition doesn't use it at
//     all: its hello is refused (partitionManagerRefusal), which makes every
//     sandbox tool, the catalog and the Sandboxes dialog say why — with the
//     manager named and how to update it. The global instance (shared
//     conversations) keeps using it.
//   - Isolation stops at the sandbox: a person's partition also sees the
//     team's sandboxes (homed at the agent's global identity, or shared with
//     it), marked `shared`. A conversation in a person's partition works
//     only in a sandbox homed there — the manager's owner.partitionId is
//     this partition's id (its owner.partition the partition key while the
//     id isn't known yet), owner.via this tile, and not `shared` — checked
//     when it is bound and again at every use; anything else, a manager that
//     says nothing of the home included, is refused (partitionBoxRefusal).
//     Its terminal can still be opened.
//   - Every sandbox a partitioned agent makes is labelled xbin.agent/home
//     (mode.go agentHome) — whatever makes it: a conversation's tools, its
//     dialog, POST /sandboxes with or without a conversation. Ids of
//     different homes never collide.
package main

import (
	"fmt"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// capPartitions is the manager capability a person's partition needs.
const capPartitions = "partitions"

// partitionManagerRefusal refuses, in a person's partition, a manager whose
// hello lacks `partitions`.
func partitionManagerRefusal(provider string, h *sbxHello) error {
	if !userMode() || h.has(capPartitions) {
		return nil
	}
	return &sbxError{Provider: provider, Refusal: "partitions", Msg: oldManagerWords(provider, h)}
}

// oldManagerWords says why an old manager isn't used, and what to do.
func oldManagerWords(provider string, h *sbxHello) string {
	return fmt.Sprintf("%s can't keep each person's sandboxes apart (its hello doesn't offer the %q capability), "+
		"so this agent doesn't use it in your own conversations: update %s to a version that does "+
		"(the Tile Manager's Updates, or `bx template updates` for a copy of the coding-sandbox template), "+
		"or ask a workspace admin to", h.title(provider), capPartitions, provider)
}

// homedHere: box is homed in this person's partition, as its manager says.
func homedHere(box *sbxSandbox) bool {
	o := box.Owner
	if box.Shared || o.PartitionID == "" || o.Via != xbin.Self() {
		return false
	}
	if id := partitionID(); id != "" {
		return o.PartitionID == id
	}
	return o.Partition == partitionKey()
}

// homedAtOwnGlobal: in a person's partition, box is homed at this agent's
// own non-personal identity (a sandbox the global instance made: owner.via
// this tile, no partition). The manager shows it there `shared` — the
// partition changes neither who may use it nor deletes it — and only where
// the person may use it at that identity (docs/sandbox-manager.md
// §Partitioned consumers), so it isn't another consumer's share needing
// one for this tile: the person rules apply as at the global instance, and
// its terminal opens (partitionBoxRefusal keeps conversations out of it).
func homedAtOwnGlobal(box *sbxSandbox) bool {
	o := box.Owner
	return userMode() && o.Via == xbin.Self() && o.PartitionID == "" && (o.Partition == "" || o.Partition == "global")
}

// partitionBoxRefusal: in a person's partition, a sandbox that isn't homed
// there is never bound to a conversation, nor used by one.
func partitionBoxRefusal(box *sbxSandbox) string {
	if !userMode() || box == nil || homedHere(box) {
		return ""
	}
	return fmt.Sprintf("%s isn't a sandbox of your own space (the team's, shared with this agent, or one its manager doesn't say is yours): "+
		"a conversation in your own space works only in sandboxes of your own — create one, or open this one's terminal instead", box.Name)
}

// withHomeLabel is labels with xbin.agent/home set in a partitioned agent (a
// copy: the caller's map is left alone).
func withHomeLabel(labels map[string]string) map[string]string {
	home := agentHome()
	if home == "" {
		return labels
	}
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out["xbin.agent/home"] = home
	return out
}
