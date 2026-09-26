# XbinTerm

The terminal's non-drawing half for the xbin app (plans/native.md §12):
the `/ws/term` codec, the session state machine, predictive echo and the
keyboard model. Foundation only — it builds and tests on Linux:

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"
cd native/ios/Packages/XbinTerm && swift test
```

The emulator (SwiftTerm, decision 8) stays in the app behind a thin adapter;
this package never imports it.

| File | What |
|---|---|
| `TermProtocol.swift` | `TermCodec` (server frames ↔ `TermServerFrame`, client frames → `TermWireMessage`), `TermSessionInfo`, `TermScope`, `TermPaths` (socket/kill/env paths), `TermEnvState` |
| `TermSession.swift` | `TermSession` (main actor): connect → session frame → replay → live, reattach with backoff, pings/RTT, predictions and acks; the injected `TermEmulator`, `TermTransport`/`TermConnect`, `TermClock` |
| `TilePty.swift` | `TilePtySession` (main actor): a native tile's `terminal` element on its own backend's pty socket (binary + `resize`/`exit`, live on open; a clean close — 1000 or no code — ends it like `exit`, any other reconnects with backoff that starts over only after 5 s open; 401 → the app renews the frame token) |
| `Predictor.swift` | `Predictor`: a port of `web/term-predict.js` (D70/D71) over `TermFramebuffer` |
| `Keyboard.swift` | `TermKeyboard` (accessory row with sticky ctrl/alt, soft-keyboard text, hardware keys → bytes or ⌘ shortcuts), `TermKeyEncoder` (xterm sequences, DECCKM) |

## Gluing SwiftTerm to it

The app implements three small things and forwards a few emulator events.
(SwiftTerm member names below are from its public API as of writing; check
them against the version the app pins.)

```swift
// 1. The emulator adapter
@MainActor final class SwiftTermEmulator: TermEmulator {
    let view: TerminalView
    func write(_ b: [UInt8]) { view.feed(byteArray: b[...]) }      // parses synchronously…
    func afterParsed(_ body: @escaping @MainActor () -> Void) { body() } // …so acks apply at once
    func reset() { view.getTerminal().resetToInitialState() }         // RIS + scrollback
    var size: TermSize { let t = view.getTerminal(); return TermSize(cols: t.cols, rows: t.rows) }
    var framebuffer: (any TermFramebuffer)? { SwiftTermScreen(view.getTerminal()) }
}
// TermFramebuffer over the VISIBLE screen (row 0 = top of the viewport at the
// bottom of the scrollback): cursor = (buffer.y, buffer.x); charAt → the cell's
// character ("" never written); widthAt → the cell's width (0 for a wide
// glyph's second cell); lineAt → one Character per cell.

// 2. The transport: URLSessionWebSocketTask on wss://<workspace><path>, with the
// device session's credentials; `start()` resumes it. Report the upgrade's HTTP
// status on failure (`.refused(status:body:)`), `.unreachable` for network
// errors, `.dropped` when an open socket closes. Deliver events on the main
// actor, never from inside start/send/close.

// 3. The session
let session = TermSession(target: .new(TermNewSession(cwd: tile)), emulator: emu,
                          clock: TermSystemClock(), connect: { path, events in WebSocketTransport(path, events) })
session.delegate = self   // phase, attached(info), netNote, overlay, rtt, notice
session.start()
```

Forward from SwiftTerm's delegates:

| SwiftTerm | XbinTerm |
|---|---|
| `send(source:data:)` (typed keys, the emulator's own replies) | `session.send(Array(data))` |
| `sizeChanged(source:newCols:newRows:)` | `session.resized(TermSize(…))` |
| cursor shown/hidden (DECTCEM `?25h/l`; a full reset shows it) | `session.cursorVisibilityChanged(hidden:)` |
| alternate screen switched | `session.bufferChanged()` |
| scrolled, or anything else that moves the screen | `session.redraw()` |
| scene phase active/background | `session.visible = …`; on return, `reconnect()` if `.failed(.disconnected)` |

Draw `overlay` (from `termSession(_:overlay:lagging:)`) in a layer above the
terminal, in its default colours: each `PredictedRun` at its cell (underlined
when `underline`), and the predicted cursor as a block. Hide it while the user
has scrolled back. Show the RTT badge while `lagging`.

Keys: SwiftTerm's `TerminalView` already turns key presses into bytes and
hands them to `send(source:data:)`. Either subclass it and take input over
(`insertText`, `deleteBackward`, `pressesBegan` → `TermKeyboard`: one tested
mapping, sticky modifiers everywhere), or keep SwiftTerm's input and use
`TermKeyboard` only for the accessory row and the ⌘ shortcuts. Keep one
`TermKeyboard` per terminal. The accessory row calls
`accessory(_:applicationCursor:)` (nil = a modifier toggled: re-render the
caps from `sticky`); the text input path calls `text(_:)` and
`deleteBackward()`; `pressesBegan` calls `hardware(_:applicationCursor:)` and
consumes the press unless it returns `.passthrough`. `applicationCursor` is
the emulator's DECCKM mode. Send the bytes with `session.send`.

## Conformance

- `PredictorTests` ports every case of `hack/term-predict.test.mjs`.
- `PredictorTraceTests` replays `Tests/XbinTermTests/Resources/term-predict-trace.json`,
  thousands of steps recorded from the JS engine by `hack/term-predict-trace.mjs`;
  `make js-test` fails when `web/term-predict.js` changes without the trace
  (and so this port) following.
- `native/tools/term-live` drives `TermSession` against a running xbind's
  `/ws/term` (session frame, acks, pongs, drop → reattach → replay, resize,
  DELETE → exit, 404/403 refusals); instructions at the top of its
  `main.swift`. Not in CI — it needs a live xbind.
