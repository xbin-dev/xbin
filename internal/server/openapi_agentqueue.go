package server

// openapi_agentqueue.go — an agent session's messages while a turn runs
// (D197, internal/term/agentqueue.go): endpoints() appends them.

func agentQueueEndpoints() []ep {
	return []ep{
		{"POST", "/term/sessions/{id}/queue", "Agent sessions", "Send a message whatever the agent is doing (D197)", "creator or admin; not an agent sandbox's token, the session's own or another's (403)",
			"{text, attachments?, interrupt?} → {outcome, turn?, id?}. Idle, it starts a turn as /prompt does (outcome prompted, turn). While a turn runs the message goes to that turn where the agent takes messages mid-turn (_session/steering: claude, codex — taken at the agent's next step; outcome steered, and the log's user message.delta carries steered:true), else it is held and sent as the next prompt when the turn ends (outcome queued, id) — held messages go in together, in order, as one prompt, and once one is held later ones queue behind it. interrupt: hold it and cancel the running turn, so it goes in once that turn has ended. The held messages are the session's: a `queue` event ({items:[{id, text (a preview), files?, at}], sent?, dropped?, reason?}) follows every change, the tail events page's state.queue has them now, and DELETE …/queue takes them back. A cancel keeps them; the session's end drops them (a notice says so). 409 while another message for the session is being taken (before this body is read) or 16 are held; 413 past one prompt's attachment limits or 40 MiB of held files. Attachments as for /prompt. Not audited per call (the data plane).", []oapi{pathParam("id", "session id")},
			jsonBody("message", oapi{"text": str("the user's message"), "interrupt": oapi{"type": "boolean", "description": "hold it and cancel the running turn"}, "attachments": oapi{"type": "array", "description": "files, as for /prompt", "items": oapi{"type": "object"}}}), "{outcome, turn?, id?}"},
		{"DELETE", "/term/sessions/{id}/queue", "Agent sessions", "Take the held messages back (D197)", "creator or admin; not an agent sandbox's token (403)",
			"→ {items:[{id, text, files?, at}]}: every message held for the turn's end, whole (their files are not returned) — a client puts the text back where it was typed. A queue event follows. Not audited per call.", []oapi{pathParam("id", "session id")}, nil, "{items}"},
		{"DELETE", "/term/sessions/{id}/queue/{qid}", "Agent sessions", "Take one held message back (D197)", "creator or admin; not an agent sandbox's token (403)",
			"→ {items:[{id, text, files?, at}]}: that message, whole. 404 when it is no longer held (sent, or taken back elsewhere).", []oapi{pathParam("id", "session id"), pathParam("qid", "a held message's id")}, nil, "{items}"},
	}
}
