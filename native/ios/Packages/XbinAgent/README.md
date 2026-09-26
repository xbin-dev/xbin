# XbinAgent

The native client's model of **ACP agent sessions** (plans/native.md §13; D74,
D75, D77; docs/protocol.md "Agent session events" and the
`/api/xbin/term/sessions` + `/agent/*` routes). Foundation only — it builds and
tests on Linux; the SwiftUI agent screen (shell surface) binds to it.

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"
cd native/ios/Packages/XbinAgent && swift build && swift test
```

## What is here

| File | What |
|---|---|
| `JSON.swift` | `JSONValue` (order-preserving objects, bools never numbers) and its parser/serializer; `JSONBacked` = Codable through the wire form |
| `Models.swift` | REST models: `AgentProvider`, `SessionInfo`, `SessionSnapshot`, `PendingPermission`, `EventsPage`, `HistoryEntry`, `HistoryTranscript`; request bodies `CreateAgentSession`, `RestartOptions`, `PermissionAnswer`, `QuestionAction` |
| `Events.swift`, `EventEnums.swift`, `EventPayloads.swift` | `AgentEvent {seq, ts, type, data}` and its typed `payload` for every event type; open enums (unknown values kept); `SessionHubEvent` (a `/ws/events` `session` frame) |
| `Transcript.swift`, `TranscriptItems.swift` | `AgentTranscript` — the reducer (the web's `_blocks()`, incremental) and the items it produces; seq discipline (`receiveLive`, `receiveStream`, `apply(page:since:)` → `SyncAction.refetch(since:)`) |
| `SessionState.swift` | `AgentSessionState` — status, modes, settings pickers, slash commands, sign-in, usage, title |
| `Headline.swift` | D77 headlines: `ToolReading.headline/command/describeCommand/rawText/…` (port of web/agent-tools.js) |
| `Permissions.swift` | D77 permission-card rules: `PermissionRules` (order, scope, heading, plan card, settlement) |
| `Elicitation.swift` | question forms: `FormField.fields/content/missingRequired` |
| `Slash.swift` | slash completion: `SlashCompletion.query/matches/menu/hint` |
| `ANSI.swift` | shell output: `ANSIText.parse` (styled spans), `OutputView` (tail + full), `stripANSI` (the web's) |
| `Diff.swift` | `LineDiff` (the web's LCS, hunks with context, unified text, stats), `GitPatch.parse` |
| `Client.swift` | `AgentClient` over an injected `AgentTransport`; `NDJSONLines`; `AgentAPIError`; `PromptAttachment` (a prompt's files: xbind's limits, the byte-built body, image sniffing and `imagePlan` for photos) |
| `Feed.swift` | `AgentSessionFeed` — one session kept current: replay, follow with reconnect, live frames, refetch on gaps, actions, plan-feedback follow-up |

## How the screen uses it

```swift
let client = AgentClient(transport: workspaceTransport)          // URLSession + device session, the app's
let info = try await client.create(CreateAgentSession(cwd: tile, provider: "claude"))  // eager: pickers load
let feed = AgentSessionFeed(client: client, sessionID: info.id)
Task { await feed.run() }                                         // follow + reconnect until the session ends
for await t in await feed.updates() {                             // AgentTranscript
    // t.items (or t.turns), t.state.pickers(), t.state.commands, t.activity(),
    // t.pendingPermissions / t.pendingQuestions, t.state.signIn(provider:lastError:)
}
// /ws/events frames: if let h = SessionHubEvent(json: frame) { await feed.receive(h) }
// foreground / socket reconnect: await feed.catchUp()
// actions: feed.send(text), feed.send(text, attachments: [PromptAttachment]), feed.cancel(), feed.answer(card, choice:), feed.keepPlanning(card, choice:, feedback:),
//          feed.answer(questionCard, action:, values:), feed.set(picker.id, to: value)
```

## Tests and fixtures

- `Fixtures/<scenario>.json` are **real session logs**: the scripted agent
  (hack/fakeacp) driven through a real `internal/term` session — seq
  numbering, delta coalescing, permission rules, snapshot diffs, history.
  Regenerate:
  `XBIN_AGENT_CAPTURE=$PWD/native/ios/Packages/XbinAgent/Tests/XbinAgentTests/Fixtures go test ./internal/term -run TestAgentCaptureFixtures -count=1`
- `Fixtures/js-parity.json` is **the web's own output** (web/agent-tools.js,
  web/agent-slash.js, and bx-agent.js `_blocks()` over every captured
  session) — `ParityTests` holds the port to it. Regenerate after the
  fixtures or those modules change:
  `node native/tools/agent-parity.mjs . native/ios/Packages/XbinAgent/Tests/XbinAgentTests/Fixtures`

## Where it differs from the web (on purpose)

- **Sign-in prompt is sticky across partial statuses.** The driver's partial
  status updates (`commands`, `usage`, `title`, `options` alone) never carry
  `login`; the web reads only the last status, so its banner can vanish right
  after a failed turn. Here a partial update keeps it; a full status without
  `login`, or a turn that ends other than `error`, clears it.
- **Gaps sit where they happened.** A truncated page (`?since=N`) or a follow
  stream's `gap` line puts a `GapMarker` after seq N, not a banner at the top.
- **Pages merge out of order.** An unseen seq older than the cursor re-folds
  the log (the web re-folds on every render anyway).
- **Turn dividers read humanly** (`StopReason.label`: "done", "cancelled", …)
  where the web prints the raw stop reason.
- **ANSI is rendered**, not stripped: SGR → styled spans; a lone `\r`
  overwrites its line; other escapes are dropped. `stripANSI` is the web's,
  exactly, for plain-text uses.
