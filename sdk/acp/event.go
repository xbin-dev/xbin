package acp

import "encoding/json"

// Event types, as a Client emits them (xbind serves them unchanged:
// docs/protocol.md §agent sessions).
const (
	EvMessageDelta       = "message.delta"        // {role, text, messageId?, parent?, attachments?, steered?}
	EvThoughtDelta       = "thought.delta"        // {text, parent?}
	EvPlan               = "plan"                 // {entries:[{content, priority, status}]}
	EvToolCall           = "tool.call"            // {id, title, kind, status, content, locations, rawInput, name?, label?, …}
	EvToolUpdate         = "tool.update"          // {id, …partial}
	EvPermissionRequest  = "permission.request"   // {pid, toolCall, options, rule, meta?}
	EvPermissionResolved = "permission.resolved"  // {pid, optionId, by}
	EvElicitRequest      = "elicitation.request"  // {eid, toolCallId?, message, schema, mode?, url?, elicitationId?}
	EvElicitResolved     = "elicitation.resolved" // {eid, action, by, content?} — action "complete" (by "agent"): a url one's elicitation/complete
	EvTurnEnd            = "turn.end"             // {turn, stopReason, usage?, error?}
	EvStatus             = "status"               // {status, detail?, modes?, currentMode?, options?, commands?, usage?, agent?, title?, login?}
)

// Session statuses (a status event's status).
const (
	StatusStarting   = "starting"
	StatusIdle       = "idle"
	StatusRunning    = "running"
	StatusWaiting    = "waiting_permission" // a permission request or a question waits for an answer
	StatusCancelling = "cancelling"         // session/cancel sent, the turn's end pending
	StatusError      = "error"
	StatusExited     = "exited"
)

// Event is one thing the agent did, typed. Data is the payload for Type.
// Seq and TS are an event log's (xbind numbers them 1, 2, … and stamps unix
// milliseconds as it appends); a Client leaves them zero.
type Event struct {
	Seq  uint64          `json:"seq"`
	TS   int64           `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
	// Wire says where on the wire the event came from, for an embedder that
	// keeps a session across processes (ClientOptions.Attach). Never
	// serialized; nil for an event the client caused itself that names no
	// request (a status it set, a cancel's tool marks).
	Wire *Wire `json:"-"`
}

// Wire is an event's place on the wire.
type Wire struct {
	// Off is the agent's output consumed through the frame that caused the
	// event — where a reader resumes to receive what follows it (Decoder.Offset,
	// from Process.Off). 0: no frame caused it (the client's own doing: a
	// prompt's echo, an answer); an embedder keeping an offset keeps its
	// previous one. Events come in the output's order (a prompt's turn.end
	// before what the agent sent after answering it): the offsets never go
	// back.
	Off int64
	// RPCID is the request the event is about: the agent's (a permission,
	// a question — the id its answer goes to), or the client's (a prompt's
	// echo and its turn.end: the session/prompt; a steer's echo).
	RPCID json.RawMessage
	// Replay: part of a session/load replaying earlier turns, not new work.
	Replay bool
}

// NewEvent builds an event with its payload marshalled (Seq and TS zero).
func NewEvent(typ string, data any) Event {
	b, _ := json.Marshal(data)
	return Event{Type: typ, Data: b}
}
