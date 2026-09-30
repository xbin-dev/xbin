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
//     it), marked `shared`. A conversation in a person's partition never
//     works in one (partitionBoxRefusal) — its terminal can still be opened.
//   - Every sandbox a partitioned agent makes is labelled xbin.agent/home
//     (mode.go agentHome) beside xbin.agent/conversation: ids of different
//     homes never collide.
package main

import "fmt"

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

// partitionBoxRefusal: in a person's partition, a sandbox that isn't homed
// there (the manager marks it `shared`) is never bound to a conversation.
func partitionBoxRefusal(box *sbxSandbox) string {
	if !userMode() || box == nil || !box.Shared {
		return ""
	}
	return fmt.Sprintf("%s is a shared sandbox (the team's, or shared with this agent): a conversation in your own space "+
		"works only in sandboxes of your own — create one, or open this one's terminal instead", box.Name)
}
