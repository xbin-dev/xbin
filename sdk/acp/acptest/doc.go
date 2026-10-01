// Package acptest is a scripted ACP agent for testing ACP clients: it
// speaks ACP v1 over a reader/writer pair (or stdio) the way the real
// adapters (claude-agent-acp, codex-acp, gemini) do and plays a script
// chosen by words in the prompt. It is xbind's test agent (hack/fakeacp is
// `func main() { acptest.Main() }`, provider "fake" when xbind runs with
// XBIN_AGENT_FAKE) and a builder's: drive Serve over io.Pipe, or start a
// test binary as the agent (MainIfAdapter, Command).
//
// Scripts, by the first match in this order (a word matches anywhere in
// the prompt's text, a prefix only at its start):
//
//	perm-edit…  (a prefix) an edit tool_call (a diff for hello.txt) and a
//	            session/request_permission — skipped in modes auto and yolo;
//	            selected → the edit completes, "edited hello.txt"
//	todo…       (a prefix) three `plan` updates 200 ms apart (3 entries:
//	            pending → one in progress → all completed), then "todo done"
//	stall…      (a prefix) a chunk "stalling", then nothing until
//	            session/cancel (handoff and reattach tests)
//	cards…      (a prefix) one completed call of each kind — read, edit (a
//	            diff for hello.txt), delete, move, search, execute (terminal
//	            output + exit 0), fetch, think, other — then "cards done"
//	steer…      (a prefix) a slow turn (ten ticks 200 ms apart) that reports
//	            each steer it received, then "steers: ‹a› | ‹b›" (or "none")
//	fail        pushes _auth/status_update{kind:none}, then the prompt fails
//	            with -32000 (auth required) — a signed-out agent, as Claude does
//	crash       exits 3 mid-turn (Serve returns an *ExitError)
//	paras N     (a prefix) one agent message of N paragraphs, a chunk each,
//	            250 ms apart (a selection in the first survives the rest)
//	chatty N    (a prefix) an execute call whose output streams as N
//	            _meta.terminal_output_delta chunks back to back (the daemon
//	            coalesces them), then completes
//	long N      (a prefix) N units back to back, a long transcript for the
//	            Agent tab's windowing (D124): a markdown message (heading,
//	            list, code fence), an edit tool_call carrying a ~30-line diff,
//	            its completion; every tenth unit starts with a thought
//	burst       fifty one-character chunks back to back (the daemon coalesces them)
//	slow        ten chunks 200 ms apart (cancel lands mid-turn)
//	ask…        (a prefix) Claude's AskUserQuestion: a tool_call, then
//	            elicitation/create (form: a single-select with descriptions, a
//	            multi-select, each with its "Other" box, as claude-agent-acp
//	            builds it); accept → "answers: <content json>", decline →
//	            "skipped", cancel → the turn ends cancelled
//	subagent…   (a prefix) Claude's Task call: a tool_call (name Task, kind
//	            think, _meta.claudeCode.subagent) and, tagged with its id as
//	            _meta.claudeCode.parentToolUseId, the subagent's thought, a Read
//	            call and its text; then the Task completes with its answer
//	think…      (a prefix) six agent_thought_chunks 300 ms apart, then a chunk
//	            "thought it through"
//	plan…       (a prefix) Claude's plan approval: an ExitPlanMode tool_call
//	            (kind switch_mode, the plan as text content + rawInput.plan) and
//	            a request_permission with its mode options (two allow_always)
//	            and _meta.permission.title "Ready to code?"; approve →
//	            "plan approved: <option>", reject → the turn ends cancelled
//	perm2…      (a prefix) two calls that ask at once, as Claude's parallel
//	            tool calls do: tool_calls t1 (run ls, execute) and t2 (rm -rf
//	            build, delete), then both session/request_permission
//	            (once/always/no; ids "perm2-<n>"), the second sent before the
//	            first is answered; the turn waits for both — an allow
//	            completes its call, no fails it, then "perm2: ‹t1's option›
//	            ‹t2's option›"; a cancelled one fails both and ends the turn
//	            cancelled; mode yolo skips the requests ("perm2: yolo yolo")
//	perm        a tool_call + session/request_permission (once/always/no);
//	            selected → the tool completes, cancelled → the turn ends
//	            cancelled; mode yolo skips the request
//	run: <cmd>  terminal/create `sh -c '<cmd>'` → "run: <output>", wrapped
//	            like a real adapter's shell call: an execute tool_call
//	            (rawInput.command) completed with _meta.terminal_output +
//	            terminal_exit (failed on a non-zero exit)
//	term        terminal/create `sh -c 'echo hi; printenv FAKE_API_KEY | wc -c'`,
//	            wait, output → a chunk "term: <output>"
//	whoami…     (a prefix) a chunk "account: <token …last4 | home | none>":
//	            the sign-in a turn uses (--require-login's rules below)
//	env         a chunk "HOME=<home> key=<yes|no> settings=<~/.claude/settings.json
//	            via fs/read_text_file> model=<the model option>"
//	write       fs/write_text_file <cwd>/fake-wrote.txt
//	(default)   one agent_message_chunk "echo: <text>" — plus, for a prompt
//	            with attachments, a description of each non-text block
//	            ([image <type> <n>B], [resource <name> <n>B], [file <name>
//	            <n>B] read from the linked path) — usage, end_turn
//
// A turn that ends normally reports usage and, the first time, a
// session_info_update title. After session/new the agent advertises three
// slash commands (review, compact, init). session/cancel ends the turn with
// stopReason cancelled. The session advertises modes ask and yolo and one
// config option, `model` (fake-default | fake-fast), settable with
// session/set_config_option (the response carries the refreshed list, and a
// config_option_update follows). It advertises promptCapabilities image +
// embeddedContext and loadSession: session/load replays one canned earlier
// turn ("resumed <id>" and the agent's echo) before answering.
//
// The flags (Options; unknown flags are ignored) change only what they
// name; without them every script above plays exactly as hack/fakeacp's did
// (testdata/golden):
//
//	--steer          advertises _meta.steering.supported and serves
//	                 _session/steering: during a turn → injected (the turn's
//	                 next chunk is "steered: ‹text›"); idle with
//	                 idleBehavior promptRequired → promptRequired; idle
//	                 otherwise → startedNewTurn (a turn answering "echo: ‹text›")
//	--auto-mode      the modes gain auto (between ask and yolo): auto skips an
//	                 edit's permission request and still asks for others
//	--require-login  signed in only while $HOME/.fakeacp/credentials exists
//	                 (read at start, after authenticate, and by a prompt while
//	                 signed out); signed out, a prompt pushes
//	                 _auth/status_update{kind:none} and fails -32000. The auth
//	                 methods become fake-login (terminal, args ["login"]),
//	                 fake-api-key (_meta["api-key"]) and fake-device (a device
//	                 code through URL elicitation). As Claude Code does, a
//	                 credential in the environment outranks $HOME's:
//	                 CLAUDE_CODE_OAUTH_TOKEN (else ANTHROPIC_API_KEY) signs
//	                 every prompt in, and one holding "refused" fails every
//	                 prompt (-32000, the sign-out status first); with an
//	                 OAuth token, session/new and session/load are followed by
//	                 _auth/status_update{kind:none}, as claude-agent-acp
//	                 0.81's `claude auth status` probe reports one
//	--persist       sessions get their own ids; every session's updates (and
//	                 its prompts, as user_message_chunk) are appended to
//	                 $HOME/.fakeacp/sessions/<id>.jsonl, and session/load
//	                 replays exactly those instead of the canned turn
//	--device-ms=N    the device-code sign-in completes N ms after the client
//	                 accepts the URL (default 1000)
//
// authenticate (with --require-login): fake-api-key takes
// _meta["api-key"].apiKey, codex-acp's shape, or _meta["api-key"] as the key
// itself, gemini-cli's (empty → -32602 "no key", "bad" → "invalid API
// key", else it writes the credentials file — the method and which shape
// came, never the key); fake-device
// needs the client's elicitation.url, sends elicitation/create {mode:"url"}
// and, N ms after the client accepts, writes the file, sends
// elicitation/complete and answers; any other method (and, without the
// flag, every method) answers {} as before.
//
// `fakeacp login` (Login) is the terminal sign-in: it asks for a code and
// writes the credentials file for "fake-code".
package acptest
