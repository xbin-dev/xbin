# Native fixtures — the contract every renderer is tested against

Each directory here is a small, realistic tile UI written against
`/vendor/xb-native.js`, a scripted `xbin` for it to talk to, and the tree it
renders once the script has played. The runtime tests produce that tree from
the tile code; the renderers (the SwiftUI app, the Lit reference renderer)
draw it — light and dark, default and large Dynamic Type — for snapshots and
the web-vs-iOS contact sheet (plans/native.md §17, native/AGENTS.md).

```
native/fixtures/<name>/
  native.js       the tile code under test (it may import sibling modules)
  data.json       the scripted xbin: responses, streams, interface values, pinned clock/zone/locale, interactions
  expected.json   the rendered tree {"v":1,"root":…} after the interactions — the contract
```

## Running them

```sh
make native-check                               # xb-native's tests + every fixture (part of make check)
node native/tools/fixture.mjs                   # --check every fixture (the default)
node native/tools/fixture.mjs --check controls  # just one (or a few)
node native/tools/fixture.mjs --update controls # write expected.json, printing what changed
node native/tools/fixture.mjs --print controls  # the tree it renders now
node native/tools/fixture.mjs --messages controls  # every runtime → app message (mount, patch, diag, …)
node native/tools/fixture.mjs --coverage        # which fixture exercises which part of the vocabulary
node native/tools/fixture.mjs --list            # the fixtures and their `about`
```

A fixture fails when its run reports a runtime error, a `warn`/`error`
diagnostic (unknown props, bad tokens, child-rule violations — unless its
code is in `allowDiagnostics`), a request that no route answers, or a tree
that differs from `expected.json`. Differences are printed per node key:

```
FAIL controls
    ~ r.1.0 (picker) p.value: expected "weekly", got "daily"
    + r.2.6 (notice) unexpected in r.2: {"tone":"warn","text":"…"}
```

A full check (no names) also fails when the fixtures together leave any part
of the vocabulary (`web/xb/vocab.js`) unexercised — every primitive, event,
prop, object field, enum value, tone/type/gap token per prop, icon and height
token, restricted child (`list>notice`) and markdown token shape must appear
in some `expected.json` — or when this README's index misses a fixture.

## `data.json`

| Key | What |
|---|---|
| `about` | one line: what the fixture shows (`--list`, and the index below) |
| `self` | `xbin.self` (default `apps/tile`) |
| `now` | the pinned clock: an ISO time or ms since the epoch (default `2026-09-21T14:13:20Z`). `Date`, timers and `requestAnimationFrame` run on a virtual clock that moves only when an interaction waits |
| `tz`, `locale` | the time zone and default locale for `Date` and `Intl` (default `UTC`, `en-US`); the run gets a node process of its own when they differ from the runner's |
| `iface` | `{slot: value}` — what `xbin.iface(slot)` returns |
| `routes` | `xbin.fetch` answers: `{"METHOD /path?query" \| "METHOD /path" \| "/path": response \| [response, …]}` — the most specific key wins; an array answers successive calls in turn, the last one repeating |
| `calls` | how `xbin.native.copy/share/open` resolve: `{"copy": true, …}` (default `null`) |
| `dialog` | what `xbin.dialog()` resolves to |
| `caps` | the app's caps (default: the full vocabulary) — to test an older app |
| `state` | the blob the app kept (`xbin.native.state`, default `null`) |
| `interactions` | the script, below |
| `allowDiagnostics` | diagnostic codes this fixture expects (e.g. `["duplicate-key"]`) |

A response is `{status?, json? | text? | sse?, headers?, delay?, error?}`:
`json` sets `content-type: application/json`; `sse` is a list of frames
`{event?, id?, data, after?}` (`data` is JSON-encoded unless it is a string)
served as `text/event-stream` — a frame with `after` arrives that many virtual
ms after the previous one, and `open: true` leaves the stream open after the
last frame (a turn still streaming); `delay` holds the whole response back
(virtual ms — a request can still be in flight when the tree is taken);
`error` makes the fetch reject.

## Interactions

Played in order after the first render settles; the run settles after each
one (no timers due at the current virtual time, no pending work), and the
final tree is taken after the last.

```json
{"after": 1000, "select": "field[label=Name]", "type": "input", "payload": {"value": "nightly"}, "note": "optional"}
```

- `after` (ms, default 0) moves the virtual clock first — timers, polls and
  stream frames due by then run. An interaction with only `after` just waits.
- Then at most one action:
  - an **event** on a node: `k` (its key) or `select` (below), `type`, and
    `payload` (default `{}`), optionally `n` (native/spec/tree.md §6). The
    node must exist and take the event — listen to it, or the event reports
    one of its controlled props — or the run fails. What the app sends is
    the vocabulary's payload: `tap` `{}`, `input`/`change`/`submit`
    `{value}`, tabs `change` `{key}`, section `toggle` `{collapsed}`,
    `toggle` `{open}`, composer `uploaded` `{name, response}`, …
  - `bus`: `[topic, data]` (or `{topic, data}`) delivered to `xbin.bus.on`
    subscribers and `xbin.events.on` listeners, e.g.
    `["res:apps/mail/bus/inbox/new", {…}]`;
  - `visibility`: `"hidden"` / `"visible"` (`document.visibilityState`);
  - `resolve`: `[callId, value]` answers an `xbin.native` call by hand.

**Selectors** name a node without its key, CSS-style:
`tag[prop=value][prop*=part]`, `*` for any tag, `[k=r.0.1]` for a key,
`[@tap]` for "listens to tap", quotes for values with spaces or brackets
(`button[label="+1"]`), and a space for "inside" (`sheet[title="New event"]
field[label=Title]`). A selector must match exactly one node — none or
several fails the run, naming what it matched.

## `expected.json`

Written by `--update`, one node per line with children indented (markdown
tokens get a line per block), keys in wire order — valid JSON, and a
reviewable diff. The shape is native/spec/tree.md: `{k, t, p?, e?, c?}`,
absent ≡ empty.

**Updating a fixture is a reviewed change, never a blind regenerate:** read
the whole `expected.json` of a new fixture and every line `--update` reports
for a changed one, and fix the tile code or the data — not the expectation —
when the tree is not what a user should see.

## Adding one

1. Pick a tile a user would plausibly have (a list/detail, a form, a
   dashboard, a chat) and the part of the vocabulary it shows off.
2. `native/fixtures/<name>/native.js`: plain tile code — `html`/`render`/
   `repeat`/`nothing` from `/vendor/xb-native.js`, data from
   `xbin.fetch(\`/api/${xbin.self}/…\`)`, state in module variables, a
   `paint()` after every change. Keep logic realistic (formatting with
   `Intl`, derived counts, optimistic updates) — the fixture also tests the
   runtime.
3. `data.json`: the responses the tile asks for, then the interactions that
   bring it to the state worth drawing. Renderers draw only the final tree:
   the top screen of a `nav`, the selected tab, an open sheet — make that
   state show what the fixture is about, with content that fits roughly one
   phone screen.
4. `node native/tools/fixture.mjs --update <name>`, then read
   `expected.json` line by line; `--coverage` shows what it exercises.
5. Add it to the index below; `make native-check`.

Rules: content is realistic and self-consistent (names, times, counts agree
with each other); nothing depends on the machine (no `Math.random`, no real
network — every request has a route; time only through the pinned clock;
`Intl` formats whose output ICU versions disagree on — AM/PM spacing,
abbreviated month names, time-zone names — are avoided, since CI's node may
not be yours); no real secrets or personal data; a vocabulary change updates
the fixtures, `expected.json` and the renderers in one change
(native/AGENTS.md).

## Index

| Fixture | Shows |
|---|---|
| `structure-nav` | `nav`: a list root (large title, refresh, search, a toolbar `menu` with a divider) and a pushed form screen (`appear`) with an open `disclosure`, `code` and confirm buttons |
| `sections-rows` | `section` (title, badge, footer, collapsible, collapsed) and `row` in every shape: icon, subtitle, detail, badge, each tone, each `mono`, `nav`, `selected`, `disabled`, swipe `actions`, content under a row |
| `controls` | a form: all nine `field` kinds (hint, error, disabled, return-key label, typed values), `toggle`s, `picker` in segmented, menu and inline styles with icons and number values |
| `buttons` | `button` in every role, with icons, disabled, busy (a request in flight), `confirm`, `copy`; a `toolbar` holding a picker, a badge, a menu and a button; row actions |
| `text` | `text` in all twelve type roles and five tones, `mono`, `selectable`, `lines`; `Intl` times in a pinned zone and locale |
| `markdown` | a runbook through `markdown`: headings 1–6, emphasis, strikethrough, code spans, links, breaks, bullet/ordered (from 3)/loose/task lists, code with and without a language, a quote, an aligned table, a rule |
| `media` | `image` (tile-relative and `data:`, fill and fit, every height, preview, tap to switch) and `icon` in every tone |
| `icons` | every icon name of the vocabulary, grouped and captioned |
| `charts` | `chart`: line over time (percent) and numbers, area (bytes), bar (categories), sparklines in rows; every height; the source from `xbin.iface` |
| `notices-empty-progress` | `notice` in every tone, `progress` with a value and indeterminate, `badge` in every tone (one pulsing), a `list` mixing progress, notice, rows and a section with load-more, `empty` states |
| `chat-transcript` | an agent turn mid-stream: `message` roles (sender, time, files, actions, queued, streaming markdown over an open event stream), `thinking` (done and live), `step` in every tone, `activity`, a date divider, an image and markdown in the `transcript`, the `composer` (attachment, slash commands, chips) |
| `chat-tools` | `plan`, `toolcard` in every state with chips and each body (code, text, streaming markdown, notice, image, diff, a subagent's nested `transcript`), `diff` with a patch, a settled and a pending `approval`, a settled `question`, a progress line, the composer disabled |
| `escape-hatches` | `terminal` on the tile's pty and `canvas` (a tile page, and static no-script html) inside a native screen |
| `sheet-open` | a `fragment` root: a `nav` plus two `sheet`s — one open (medium and large detents, its own toolbar, a validating field), one closed |
| `tabs` | segmented `tabs` with icons and badges; only the selected tab is materialized |
| `tabs-bar` | a tab bar (`tabs style="bar"`) at the root, a `nav` per tab, the last tab restored from `xbin.native.state` |
| `split` | `split prefer="auto"`: a plain lazy `list` (selected row, load more, a bus-delivered mail) beside the message detail |
| `split-single` | `split prefer="single"`: a sectioned list and the opened contact |
| `stack-layout` | `stack` vertical and horizontal, every gap, start/center/end, wrap; `spacer`, `divider` |
