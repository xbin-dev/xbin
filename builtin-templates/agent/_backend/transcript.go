// transcript.go — keeping a run's message list valid for the provider.
//
// The API contract is strict: every assistant message carrying tool_calls must
// be followed by one role:"tool" message per call id. The loop used to persist
// the assistant message BEFORE running the tools and write results only after
// the whole batch finished, so a save/swap/crash in between left tool_calls
// with no replies — and the next request shipped that unanswered block to the
// provider. Two mechanisms live here:
//
//   - placeholders, written before a tool runs and rewritten after, so the
//     transcript is valid at every instant going forward;
//   - repairTranscript, which heals rows that are ALREADY broken in the live
//     database — something a write-ordering fix alone can never do.
package main

import (
	"encoding/json"
	"fmt"
)

// toolRunning is the placeholder content written before a tool executes. It is
// deliberately recognisable: a row still carrying it after a restart means the
// process died mid-tool, and repairTranscript rewrites it.
const toolRunning = "(running…)"

const toolLostToRestart = "(no result: the backend restarted while this tool was running)"

// toolAwaitingApproval answers a call the approval gate parked. It is NOT
// toolRunning: repairTranscript rewrites only that sentinel, so a parked call
// survives a restart or a /resume untouched until the human decides.
const toolAwaitingApproval = "(awaiting your approval)"

// placeholderResults marks each call as running, so the assistant's tool_calls
// block is answered from the moment work starts; runToolBatch settles them with
// updateToolResult.
//
// It UPSERTS: a call the approval gate already answered with
// toolAwaitingApproval is flipped to toolRunning rather than given a second
// row. Appending here is what double-answered every approved call. Flipping
// (rather than leaving "awaiting approval") matters too: if the process dies
// after approval but mid-tool, repairTranscript must recognise the row as lost.
func (ag *Agent) placeholderResults(runID int64, calls []toolCall) {
	for _, tc := range calls {
		ag.settleToolResult(runID, tc, toolRunning)
	}
}

// repairTranscript makes a run's live transcript API-valid before it is sent
// anywhere: it fills in missing tool results and settles placeholders left
// behind by a dead process. Returns how many messages it touched.
//
// Called at the top of every drive. The cost is one pass over messages the
// drive is about to load anyway.
func (ag *Agent) repairTranscript(runID int64) int {
	msgs, err := ag.db.messages(runID, true)
	if err != nil || len(msgs) == 0 {
		return 0
	}
	fixed := 0
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role != "assistant" || m.ToolCalls == "" {
			continue
		}
		var calls []toolCall
		if json.Unmarshal([]byte(m.ToolCalls), &calls) != nil || len(calls) == 0 {
			continue
		}
		// Collect the replies that already follow this block. They are always
		// contiguous: the loop writes them before anything else can be added.
		seen := map[string]bool{}
		lastSeq := m.Seq
		j := i + 1
		for ; j < len(msgs) && msgs[j].Role == "tool"; j++ {
			seen[msgs[j].ToolCallID] = true
			lastSeq = msgs[j].Seq
			// A placeholder still here means the tool never finished.
			if msgs[j].Content == toolRunning {
				if ag.db.updateToolResult(runID, msgs[j].ToolCallID, toolLostToRestart) == nil {
					fixed++
				}
			}
		}
		var missing []*Message
		for _, tc := range calls {
			if tc.ID == "" || seen[tc.ID] {
				continue
			}
			missing = append(missing, &Message{
				RunID: runID, Role: "tool", Name: tc.Function.Name,
				ToolCallID: tc.ID, Content: toolLostToRestart,
			})
		}
		if len(missing) > 0 {
			// Insert directly after the block's existing replies, shifting the
			// rest down — appending at the end would put results out of order
			// behind whatever came later, which is its own protocol violation.
			if ag.db.insertMessagesAfter(runID, lastSeq, missing) == nil {
				fixed += len(missing)
			}
		}
		i = j - 1
	}
	if fixed > 0 {
		ag.db.journal(runID, "note", map[string]string{
			"text": fmt.Sprintf("repaired %d unanswered tool call(s) left by a restart", fixed)})
	}
	return fixed
}
