# The native tree wire contract (v1)

What a tile's native runtime (`/vendor/xb-native.js`, running in the app's
hidden WebKit document for the tile) and the app (XbinCore + the SwiftUI
renderer, or a preview host) say to each other. The design is
plans/native.md §7–§11; this file is the precise contract. The vocabulary —
every primitive's props, events, child rules and revision — is
[`vocab.json`](vocab.json), the JSON export of `web/xb/vocab.js`
(`node hack/xbn/vocab-json.mjs > native/spec/vocab.json`; a test keeps them
equal).

**Additive only.** Shipped apps lag by months: messages, ops, node fields,
primitives, props, events, token values and features are only ever added.
Receivers ignore fields and messages they do not know. Changing or removing a
meaning needs a new major `v`, served side by side (docs/compat.md).

Reference implementations: `web/xb/rt-diff.js` (`diff`, `applyOps`,
`normalize`), `web/xb/rt-runtime.js` (the runtime side of every message),
`hack/xbn/node.mjs` (runs a `native.js` in node and returns the tree).

## 1. The runtime document and the transport

The app loads the runtime document for a tile (`/c/<tile>/?native=1`, through
its scheme handler with the tile's frame token — plans/native.md §6.1/§7.2).
Before any page script runs (a `WKUserScript` at document start) the app
evaluates:

```js
window.xbin = { native: { caps: {/* §8 */}, state: /* the saved blob or null */ } };
```

`xbin-client.js` (injected by xbind) carries that `native` object into the
frozen `window.xbin`; `xb-native.js` reads `caps`/`state` from it and adds its
methods (§9). Without an injection the runtime assumes the full vocabulary.

**Runtime → app.** Every message is posted as a **JSON string** to the
`WKScriptMessageHandler` named `xbn`
(`window.webkit.messageHandlers.xbn.postMessage(JSON.stringify(msg))`).
Strings keep JSON booleans distinct from numbers: decode with a real JSON
codec, never through `NSNumber`. Without WebKit (previews, tests) the same
message objects go to a host's `post(msg)` (`attach(post)` or
`globalThis.xbnHost = {post}`); until one exists they queue.

**App → runtime.** `callAsyncJavaScript` on `globalThis.xbn` (§4).

Messages are delivered and processed in order. One render produces at most
one tree message (`mount` or `patch`), preceded by any `error`/`diag` messages
about it (an app that falls back on `unsupported` can do so before drawing).

## 2. Nodes

```json
{"k": "r.0.1", "t": "button", "p": {"label": "+1", "role": "primary"}, "e": ["tap"], "c": [ … ]}
```

| Field | Meaning |
|---|---|
| `k` | the node's key: a string, unique in the whole tree, stable across renders (§3) |
| `t` | the primitive (`vocab.json` `prims`), or `fragment` |
| `p` | props, a JSON object; absent = `{}` |
| `e` | the events the tile listens to, in template order; absent = `[]` |
| `c` | children, in order; absent = `[]` |

- **Absent and empty are the same thing** (`p`, `e`, `c`). The runtime omits
  empty `p` and `e`; it sends `c` (possibly `[]`) for an element whose
  template has child content and omits it for a leaf. Renderers must not
  distinguish absent from empty.
- **`fragment`** has no visual of its own: its children render in its place,
  in order (a `nav` with a `sheet` laid over it). The root is a fragment when a
  render has several top-level nodes, or none.
- **Tabs are materialized lazily**: under a `tabs` with a `selected` prop, a
  `tab` whose `key` prop differs has `"c": []` (its content is not even
  evaluated); selecting it (a `change` the tile applies) inserts it. Without
  `selected` every tab is materialized.
- A node of a type the app does not know (a newer runtime) is drawn as a
  neutral placeholder; the runtime has already sent `unsupported` (§8).

## 3. Keys

The root is `r`. For a template rendered at position `P`:

- a **template child** (element, hole or non-blank static text) at slot `i`
  of an element keyed `K` has position `K.i` — slots count every element,
  hole and text run of the element's content, whatever they render, so keys
  do not shift when a conditional hole renders nothing;
- a **hole** at position `P` renders its value there:
  - a single-root template: its root is keyed `P`;
  - a multi-root template: its slot `i` is at `P.i`;
  - an array (or iterable): item `i` is at `P.i`;
  - `repeat(items, keyFn, tplFn)`: the item with key `x` is at `P:x`
    (`repeat(items, tplFn)` keys by index: `P:0`, `P:1`, …);
  - a string or number: a `text` node keyed `P`;
  - `nothing`, `null`, `undefined`, `false`, `true`, `''`: nothing;
- `key=${x}` on an element at position `P` keys it `P:x` (the attribute is
  not a prop) — except on primitives that declare a `key` prop (`tab`), where
  `key` is that prop and the node keeps `P`;
- the render's own value is at position `r`: when it yields exactly one node
  that node is the root (keyed `r`, or `r:x` with `key=`); otherwise the root
  is a `fragment` keyed `r` holding them (a multi-root template's slots at
  `r.i`).

Keys are opaque strings on the wire: they may contain `.`, `:`, `/`, spaces —
never parse them. The runtime guarantees uniqueness: a key that would repeat
(two repeat items with one key) gets `~2`, `~3`, … appended and a
`duplicate-key` diagnostic.

Examples (plans/native.md §18): `r.1.0:1` is item `1` of a repeat in slot 0
of `r.1`; `r.1.0:3.1` is slot 1 of the multi-root template item `3` renders;
`r.0.0.2.0.1.0.1` is slot 1 of a multi-root template in slot 0 of `r.0.0.2.0.1`.

## 4. App → runtime

| Call | Meaning |
|---|---|
| `xbn.event(k, type, payload, n?)` | the user acted on node `k`. `type` must be in the node's `e`; `payload` is the event's object (§6), `{}` when it has none. `n` (optional, recommended): the `n` of the last `mount`/`patch` the app had applied when the user acted (§5). Returns `true` when a handler ran. |
| `xbn.visibility(state)` | `"visible"` / `"hidden"`: the tile's surface is on or off screen. Drives `document.visibilityState` (tiles slow their polling) and fires `visibilitychange`. |
| `xbn.resolve(id, value, error?)` | answers `{op:"call"}` `id`; a non-null `error` (a string) rejects the tile's promise instead. |
| `xbn.frame()` | the renderer's frame clock: flush a pending render now (§7). Returns whether a tree message was sent. |
| `xbn.remount()` | send the whole tree again as a fresh `mount` (the app lost its copy or failed to apply a patch). |

The tile's handler receives `{type, value, ...payload}` (`value` is
`payload.value`, possibly `undefined`).

## 5. Runtime → app

### `mount`

```json
{"op": "mount", "v": 1, "n": 1, "root": {"k": "r", "t": "screen", "p": {"title": "Counter"}, "c": []}}
```

Replaces the whole tree. Sent for the first render, whenever the root's key or
type changes, and on `xbn.remount()`. `v` is this format's major version (1).

### `patch`

```json
{"op": "patch", "n": 2, "ops": [["remove", "r.0.3"], ["insert", "r.0", 2, {"k": "r.0.2", "t": "notice", "p": {"tone": "ok", "text": "saved"}}], ["set", "r.0.0", {"detail": "43"}], ["move", "r.1:a", "r.1", 0]]}
```

`n` increases by one with every `mount`/`patch`. Ops apply **in order**, each
to the tree as the previous ops left it:

| Op | Effect |
|---|---|
| `["set", k, {prop: value, …}]` | merge: each listed prop takes the value (JSON; replace, never deep-merge) |
| `["unset", k, [prop, …]]` | delete the listed props |
| `["events", k, [type, …]]` | replace the node's `e` (`[]`: listens to nothing) |
| `["insert", parentK, i, node]` | insert the complete subtree `node` into `parentK`'s children so it ends at index `i` |
| `["remove", k]` | remove `k` and its whole subtree |
| `["move", k, parentK, i]` | move `k` within its parent `parentK` so it ends at index `i` (the index counts the list without `k`, i.e. `k`'s final position) |

Guarantees the runtime keeps (XbinCore may assert them):

- every `k` an op names exists when the op applies; `insert` never reuses a
  live key; `move` never changes parents (a key belongs to one parent — keys
  carry their parent's key);
- per node, in tree order: its own `set`/`unset`/`events`, then its children's
  `remove`s, then their `insert`s/`move`s from the last position to the first,
  then the ops of the kept children; a child whose type changed under the same
  key is `remove`d and re-`insert`ed;
- the number of `move`s is minimal (the kept children not moved form a longest
  increasing subsequence);
- an unchanged render sends **no message at all**.

`applyOps` in `web/xb/rt-diff.js` is the reference; the node tests check that
applying `diff(a, b)` to `a` yields `b` over thousands of random trees.

### `meta`

`{"op": "meta", "title"?: string|null, "icon"?: string|null, "badge"?: string|null}` —
`xbin.native.meta()`: the tile's title/icon/badge for the navigator and
switcher. Only the fields given change; `null` clears one.

### `call`

`{"op": "call", "id": "c1", "what": "copy"|"share"|"open", "args": {…}}` — app
UI acting on data the tile hands over; answer with `xbn.resolve(id, …)`:

| `what` | `args` | Resolve with |
|---|---|---|
| `copy` | `{text}` | `true` when copied |
| `share` | `{text?, url?, file?}` — `file` is a tile-relative path the app downloads with the frame token | `true` when shared, `false` when dismissed |
| `open` | `{url}` — `https:` only (the runtime refuses others); the app also requires `cap:open-links` | `true` when opened; reject when not allowed |

### `state`

`{"op": "state", "state": <JSON>}` — `xbin.native.saveState(obj)`: the app
keeps the blob (≤ 64 KiB, per tile per workspace) and injects it as
`xbin.native.state` when it recreates the runtime.

### `error`

`{"op": "error", "kind", "message", "where", "stack"?}`:

| `kind` | When | The app |
|---|---|---|
| `unsupported` | the tree uses a primitive, prop revision or feature the app's caps lack (once per distinct message) | falls back to the web tile ("update the app for the native view") |
| `exception` | the render threw (a template syntax error, a throwing `repeat` callback); the previous tree stays | falls back if no tree was ever mounted; otherwise logs it |
| `module` | the tile's `native.js` failed to load (`boot()`) | falls back to the web tile |
| `uncaught` | a tile event handler, timer or promise threw | logs it (the tile's report) |

### `diag`

`{"op": "diag", "level": "info"|"warn"|"error", "code", "message", "where"}` —
validation findings, once per distinct finding: the Xcode console, `bx lint
--native`, the preview. `where` is `<file>:<line>:<col> <tag>` (the template's
call site) when known. Codes: `unknown-tag`, `unknown-prop`, `unknown-event`,
`runtime-prop`, `bad-type`, `bad-token`, `bad-value`, `bad-handler`,
`bad-children`, `bad-child`, `child-rule`, `duplicate-key`, `lit-template`,
`unvalidated`. A diagnostic never stops a render: an invalid prop is dropped,
an unknown icon is kept (the renderer draws a placeholder), a child-rule
violation keeps the child.

## 6. Events and controlled props

The events of each primitive and their payloads are `vocab.json`
`prims.<name>.events` (`payload`: field → type). The app sends an event only
when its type is in the node's `e`.

**Controlled props.** Some events report the app-side value of a prop
(`reports` in the vocabulary):

| Primitive | Event → prop |
|---|---|
| `field`, `composer` | `input`/`change`/`submit`/`send` `{value}` → `value` |
| `toggle`, `picker` | `change {value}` → `value` |
| `tabs` | `change {key}` → `selected` |
| `section` | `toggle {collapsed}` → `collapsed` |
| `disclosure`, `thinking`, `toolcard` | `toggle {open}` → `open` |
| `sheet` | `dismiss` → `open: false` |
| `screen` | `search {value}` → `search` |

A prop the tile binds is **controlled** (the tile owns it); an unbound one is
the renderer's. The runtime keeps a shadow of what the app shows: a report
for a bound prop updates the shadow first, so

- a re-render with the value the app reported sends nothing (typing costs one
  event per keystroke and no patch back);
- a re-render with a different value sends it as a plain `set` (a reset such as
  `draft = ''`, or the tile refusing a toggle), which the app applies;
- a report for an unbound prop changes nothing.

**Ordering.** If the app passes `n` and a report was produced before the app
applied the patch that last `set` that prop (`n` < that patch's `n`), the
runtime keeps the value it sent (the app shows that one) — so a tile that then
accepts the typed text resends it. What the app does:

- apply a `set` of a controlled prop when the value differs from what the
  control currently shows (a focused field included — that is how resets
  work); while an IME composition is in progress, defer it to the end of the
  composition;
- report each change with the event its type lists, passing `n`.

## 7. Rendering and frames

`render(tpl)` schedules a flush; renders before the flush coalesce (the last
wins). A flush happens at the first of: `xbn.frame()`, the document's
`requestAnimationFrame` (a 50 ms timer backs it up where frames stall, such as
a hidden view), or — with no `requestAnimationFrame` (node) — a 0 ms timer. The
app should call `xbn.frame()` on its display link while the tile is on screen;
calling it with nothing pending is cheap.

## 8. Caps

```json
{"v": 1, "renderer": "ios", "app": "1.0 (42)", "prims": {"screen": 1, "row": 1, "…": 1}, "features": ["chart.area", "markdown.tables"]}
```

- `v`: the tree format / vocabulary major the app speaks.
- `prims`: every primitive the app renders, with its revision (`vocab.json`
  `prims.<name>.rev`). A prop with `since: n` needs revision ≥ `n`.
- `features`: flags for capabilities within a primitive (`vocab.json`
  `features`); an enum value that needs one says so (`chart` `kind: area` →
  `chart.area`). Without `markdown.tables` the runtime turns tables into
  `code` blocks of their source instead of failing.
- The runtime checks every rendered node: a primitive missing from `prims`, a
  prop newer than its revision, or a value needing a missing feature →
  `{op:"error", kind:"unsupported"}` (§5).
- Tiles test with `xbin.native.supports(name[, rev])` (a primitive with at least
  that revision, or a feature flag).

## 9. `xbin.native` (tile side)

| Member | Wire |
|---|---|
| `caps` | the injected caps (normalized) |
| `supports(name[, rev])` | local |
| `meta({title, icon, badge})` | `{op:"meta"}` |
| `copy(text)` / `share({text, url, file})` / `open(url)` | `{op:"call"}` → a promise settled by `xbn.resolve` |
| `state` / `saveState(obj)` | the injected blob / `{op:"state"}` |

## 10. Props and values

Types (`vocab.json`): `string`, `number` (finite), `bool`, `json` (any JSON),
`array` (`of`: the item schema), `object` (`shape`: field schemas; unknown
fields are kept with a `bad-value` diagnostic), or a list of these (the first
that matches). `enum` restricts strings; `token` names a set in `tokens`
(`tone`, `noticeTone`, `type`, `gap`, `height`, `icon`). Coercions the runtime
applies before sending: a number bound to a string prop becomes its decimal
string (`detail=${42}` → `"42"`); static attributes convert to the prop's type
(`lines="3"` → `3`, a bare `nav` → `true`); `?name=${v}` is `!!v`; `null`,
`undefined` and `nothing` leave the prop out. Values are snapshots — mutating
a bound array and re-rendering sends the change.

Text content becomes a prop for `text`, `badge`, `code` (`text`) and `button`
(`label`): static runs collapse whitespace like HTML and trim at the ends
(`code` keeps them verbatim, minus a leading line break and trailing
whitespace); bound values are inserted verbatim.

`text` is always verbatim (`Text(verbatim:)`); markup exists only as markdown
tokens.

## 11. Markdown tokens

`markdown` nodes carry `tokens` instead of `source`; `message` nodes with
`markdown: true` carry `tokens` beside `text`. The runtime lexes with the
vendored marked (gfm, single newlines are breaks) into this subset — raw HTML
dropped, images as the text `[image: alt]`, never loaded:

Blocks:

| Token | Shape |
|---|---|
| heading | `{"t": "heading", "depth": 1…6, "c": [inline…]}` |
| paragraph | `{"t": "paragraph", "c": [inline…]}` |
| list | `{"t": "list", "ordered": bool, "start"?: n (ordered only), "loose": bool, "items": [{"c": [block…], "task"?: true, "checked"?: bool}]}` |
| code | `{"t": "code", "text": "…", "lang"?: "go"}` |
| blockquote | `{"t": "blockquote", "c": [block…]}` |
| table | `{"t": "table", "align": ["left"\|"center"\|"right"\|null…], "header": [[inline…]…], "rows": [[[inline…]…]…]}` |
| hr | `{"t": "hr"}` |

Inline:

| Token | Shape |
|---|---|
| text | `{"t": "text", "text": "…"}` (entities decoded; adjacent runs merged) |
| strong, em, del | `{"t": "strong"\|"em"\|"del", "c": [inline…]}` |
| codespan | `{"t": "codespan", "text": "…"}` (verbatim) |
| link | `{"t": "link", "href": "https:…"\|"http:…"\|"mailto:…", "c": [inline…]}` — other schemes and relative links become their text |
| br | `{"t": "br"}` |

A tapped link fires the node's `link {href}` event (`markdown`, `message`)
when the tile listens; opening it needs `cap:open-links`. While `streaming` is true the
runtime re-lexes only the tail of an append-only source; the render with
`streaming` false is always a full lex.

## 12. A round trip

```js
render(html`<screen title="Counter" style="form"><section>
  <row title="Count" detail=${count}/>
  <button role="primary" @tap=${inc}>+1</button></section></screen>`);
```

1. runtime → `{"op":"mount","v":1,"n":1,"root":{"k":"r","t":"screen","p":{"title":"Counter","style":"form"},"c":[{"k":"r.0","t":"section","c":[{"k":"r.0.0","t":"row","p":{"title":"Count","detail":"42"}},{"k":"r.0.1","t":"button","p":{"role":"primary","label":"+1"},"e":["tap"]}]}]}}`
2. user taps → app calls `xbn.event("r.0.1", "tap", {}, 1)`
3. the tile's `inc` runs, re-renders → runtime → `{"op":"patch","n":2,"ops":[["set","r.0.0",{"detail":"43"}]]}`
