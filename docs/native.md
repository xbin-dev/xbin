# Native app UI — `native.js`

The xbin app (iOS first) shows every tile of a workspace. By default a tile
opens as its web page — the same `index.html` the browser shell frames —
inside native chrome. A tile may also ship **`native.js`**: a module that
describes its UI in a small semantic vocabulary (screens, sections, rows,
fields, buttons, a chat transcript, …) which the app draws with platform
controls. This page is the builder's reference: when to write one, the
template API, every primitive, the rules, and how to check what you made
without a phone.

> **Status.** The app is in development. Everything on this page ships in
> xbind today — the runtime (`/vendor/xb-native.js`), the vocabulary, the
> runtime document and the checking tools (`bx lint --native`, `bx preview
> --native`, `bx native tree`) — so native UIs can be written and checked
> now. The desktop shell never shows them: browsers keep showing
> `index.html`.

**Related:** [elements.md §Native app UI](/docs/elements.md) (what xbind
serves: discovery, the runtime document, entry paths, live reload) ·
[bx.md §Native UIs](/docs/bx.md) (previews, lint, fixture replays) ·
[compat.md](/docs/compat.md) (the additive contract) ·
[frontend-kit.md](/docs/frontend-kit.md) (the `/vendor/` modules) ·
decisions D91 (the runtime, vocabulary and wire format) and D92 (how the
app and its renderers are verified).

## When to write one

- **Every tile already works in the app**, as its web page. Add `native.js`
  when people use the tile on a phone and a list, a form or a chat would
  serve them better than a shrunken page: approval queues, settings, status
  lists, chat.
- **Visually complex tiles stay web**: custom graphics, many charts,
  canvases, editors, maps, dense dashboards. The page is the most expressive
  surface and remains the tile's UI everywhere else.
- **Native UI is for mobile.** The web page stays the fallback
  (§Fallback), so keep it working and test both.

## Quick start

`native.js` sits next to `xbin.json`. The counter example ships this one
(`examples/counter-go/native.js`):

```js
import { html, render, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let count = null, busy = false, err = '';

async function load() {
  try { count = (await selfApi('/count')).count; err = ''; } catch (e) { err = String(e.message ?? e); }
  paint();
}
async function inc() {
  busy = true; paint();
  try { await selfApi('/count', { method: 'POST' }); await load(); } catch (e) { err = String(e.message ?? e); }
  finally { busy = false; paint(); }
}
const paint = () => render(html`
  <screen title="Counter" style="form">
    <section>
      <row title="Count" detail=${count ?? '…'} mono="detail"/>
      <button role="primary" icon="plus" ?busy=${busy} @tap=${inc}>+1</button>
      ${err ? html`<notice tone="danger" text=${err}/>` : nothing}
    </section>
  </screen>`);
paint();   // at once: the app wants a tree before the backend answers
load();
```

That is the whole contract: import `html` and `render` from
`/vendor/xb-native.js`, call `render()` with a template whenever your state
changes, and talk to your backend with the same calls your page makes
(the kit's `selfApi` calls your own API through `xbin.fetch` and returns
the JSON). What the app draws is a tree of vocabulary nodes:

```json
{"v":1,"root":{"k":"r","t":"screen","p":{"title":"Counter","style":"form"},"c":[
  {"k":"r.0","t":"section","c":[
    {"k":"r.0.0","t":"row","p":{"title":"Count","detail":"42","mono":"detail"}},
    {"k":"r.0.1","t":"button","p":{"role":"primary","icon":"plus","busy":false,"label":"+1"},"e":["tap"]}
  ]}
]}}
```

Look at it before you ship it:

```sh
bx native tree apps/counter                              # the tree above
bx preview --native apps/counter --out /tmp/counter.png  # a picture — open it
bx lint --native apps/counter                            # problems, sizes, app revisions
```

To use another file than `native.js`, name it in the manifest:
`"native": "./mobile/main.js"` ([elements.md](/docs/elements.md)).

## How it runs

The app opens a native tile by loading its **runtime document**,
`/c/<tile>/?native=1`, in a hidden WebKit view. xbind generates it: the same
head injection as your `index.html` gets, plus one module script that
imports `/vendor/xb-native.js` and then loads your entry with its `boot()` —
so a `native.js` that fails to load is reported to the app at once. So:

- **Same identity and sandbox as your page.** `window.xbin` is the object
  your page gets — `xbin.fetch`, `ws`, `url`, `bus`, `events`, `iface`,
  `self`, `status`, `notify`, … — with your tile's frame token, grants and
  opaque-origin sandbox. Nothing in it runs as the signed-in user.
- **The whole web platform**: ES modules, `fetch` with streaming bodies,
  WebSocket, timers, `Intl`, `TextDecoderStream`.
- **Shared code.** `native.js` imports your own modules (`./model.js`,
  `./fmt.js`) — the same files `index.html` imports, resolved against the
  tile directory. A module that imports `lit` still loads (the runtime is a
  real document; it just never shows one). Keep the logic in plain modules
  both views import and keep both views thin — the builtin chat tile's
  `chat-core.js` and the Prometheus viewer's `prom.js` are the pattern.
- **Visibility.** `document.visibilityState` is `visible` while the tile is
  on screen and `hidden` when it is not (with a `visibilitychange` event):
  poll slower when hidden, as the examples do.
- **Live reload.** Saving the entry reloads the native view the way a web
  frame reloads; the backend is not restarted for it
  ([elements.md](/docs/elements.md) has the one exception). The app follows
  the workspace's event stream while it shows the workspace: a change in
  your tile reloads its open native view (or web page) in every window
  that shows it — the most specific open tile, as in the shell.
- **Only the app loads it** (and the previews in §Checking it). The shell
  and `<bx-frame>` never run `native.js`.

## Templates

```js
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
```

The API is lit's shape, but the markup is the vocabulary, not HTML:

- **Tags are primitives** (§The vocabulary). Self-closing tags are
  allowed (`<row title="x"/>`), attribute names keep their case, `&amp;
  &lt; &gt; &quot; &#39; &apos; &nbsp;` and numeric entities decode, and
  `<!-- comments -->` are dropped. A template is parsed once per call site,
  as in lit.
- **Bindings:**

  | Syntax | Meaning |
  |---|---|
  | `name="text"` | a static value, converted to the prop's type (`lines="3"` is 3, `options='[…]'` parses as JSON) |
  | `name` (bare) | `true` (`<row nav>`) |
  | `name=${v}` | the raw JS value — number, bool, array, object. A number bound to a string prop becomes its decimal string (`detail=${42}` → `"42"`); `null`, `undefined` and `nothing` leave the prop out |
  | `name="a ${v} b"` | string interpolation |
  | `?name=${v}` | a bool (`!!v`) |
  | `.name=${v}` | the same as `name=${v}` (a lit habit, accepted) |
  | `@event=${fn}` | an event handler (§Events) |
  | `key=${x}` | the node's key (§Keys) — not a prop, except on `tab`, whose `key` is its prop |

- **Children**: a hole takes a template, an array, `repeat(…)`, `nothing`, or
  a string or number (which becomes a `text` node); `null`, `undefined`,
  `false`, `true` and `''` render nothing.
- **Text content** is a prop for four primitives: `<text>`, `<badge>` and
  `<code>` put theirs in `text`, `<button>` in `label`. Static text collapses
  whitespace like HTML (in `code` it is kept, minus a leading line break and
  trailing whitespace); bound values go in verbatim; elements inside them
  are dropped.
- **Several top-level elements** render side by side — typically a `nav`
  with a `sheet` laid over it.
- **Not supported**: dynamic tag names (`<${tag}>`), bindings outside
  attribute values (`<row ${props}>`), lit directives (`classMap`,
  `styleMap`, `unsafeHTML`, …) and lit's own `html` — a lit template inside
  a native render is dropped with a `lit-template` error. `style` is a
  vocabulary prop (a screen's `list`·`form`·`scroll`, a text's type role),
  never CSS, and there is no `class`.

### `render()`

`render(template)` schedules a render; renders coalesce per frame and the
last one wins. The runtime diffs the new tree against what the app shows and
sends only the changes — **an unchanged render sends nothing**. So keep
state in plain variables and re-render after every change or poll (the
examples' `paint()`); there is no component model to learn.

**Render something at once.** The app shows the web page instead if no tree
arrives within 5 s of opening, and a backend can take seconds to start: a
screen with `<progress label="loading…"/>` is enough.

### Keys

Every node has a key, stable across renders. The app keeps per-node state
by key (a scroll position, a focused field, a disclosure the user opened),
and `bx native tree` steps and fixtures name nodes by it. Keys come from
template positions: the root is `r`, the element in slot 1 of it is `r.1`,
and so on — a conditional hole keeps its slot even when it renders nothing,
so its siblings keep their keys.

Lists need identity: ``repeat(items, (x) => x.id, (x) => html`…`)`` keys each
item by its id (`r.1.0:42`), so a reorder moves nodes instead of rebuilding
them and each row keeps its state. `repeat(items, tplFn)` keys by index;
`key=${x}` keys a single element. A key that would repeat gets `~2`, `~3`
appended and a `duplicate-key` warning.

## Events

A handler receives one object, `{type, value, …payload}`:

```js
const paint = () => render(html`
  <screen title="Notes" style="form">
    <tabs selected=${tab} @change=${(e) => { tab = e.key; paint(); }}>
      <tab key="new" title="New">
        <section>
          <field label="Title" value=${draft} @input=${(e) => { draft = e.value; paint(); }} @submit=${save}/>
        </section>
      </tab>
      <tab key="all" title="All"><section><row title="…"/></section></tab>
    </tabs>
  </screen>`);
```

Each event's payload fields are in the tables of §The vocabulary
(`pop {depth}`, `choose {id, feedback}`, `uploaded {name, response}`, …).
Handlers may be async. A handler that throws — or a rejected promise, or a
failing timer — is reported to the app as an uncaught error; the tile keeps
running.

### Controlled and uncontrolled props

Some props are the user's to change: a field's `value`, a toggle's, a
sheet's `open`, a disclosure's `open`. Bind one and your tile owns it
(**controlled**); leave it unbound and the app owns it (**uncontrolled** — a
`disclosure` without `open` opens and closes by itself). For a controlled
prop the app reports each change with the event in the table below; store
the value and re-render:

- **Typing never fights a render.** A text field is the app's while it has
  focus, and a render with the value the app just reported sends no patch —
  typing costs one event per keystroke and nothing back.
- **Resets work.** A render with a *different* value is applied, focused
  field included (`draft = ''` after sending; a tile refusing a toggle).
- **A bound prop with no listener is read-only**: the user's change is
  undone by your next render.

<!-- generated:controlled (node hack/native-docs.mjs --write) -->
| Primitive | Event → controlled prop |
|---|---|
| `screen` | `search` {value} → `search` |
| `section` | `toggle` {collapsed} → `collapsed` |
| `disclosure` | `toggle` {open} → `open` |
| `tabs` | `change` {key} → `selected` |
| `sheet` | `dismiss` → `open` = false |
| `toggle` | `change` {value} → `value` |
| `field` | `input` {value}, `change` {value}, `submit` {value} → `value` |
| `picker` | `change` {value} → `value` |
| `thinking` | `toggle` {open} → `open` |
| `toolcard` | `toggle` {open} → `open` |
| `composer` | `input` {value}, `send` {value} → `value` |
<!-- /generated:controlled -->

## `xbin.native` — the small app API

In the runtime document `window.xbin` gains `native`; the same object is
exported as `native` from `/vendor/xb-native.js`. None of it is a device
API — each member is app UI acting on data your tile hands it.

| Member | What |
|---|---|
| `xbin.native.caps` | what the app renders: `{v, renderer, app, prims: {name: rev}, features: […]}`. Previews and `bx` report the full vocabulary |
| `xbin.native.supports(name[, rev])` | `true` when the app has primitive `name` at revision `rev` (default 1) or newer, or has the feature flag `name` |
| `xbin.native.meta({title, icon, badge})` | the title, icon and badge the app shows for the tile in its navigator and switcher; only the given fields change, `null` clears one. `icon` is an icon name (§Icons; an unknown one shows nothing there), `badge` a count or a short word (the first 8 characters show). The app remembers the last one it saw, so the badge shows before the tile is opened again |
| `xbin.native.copy(text)` | the app copies `text` to the clipboard → a promise of `true` when copied |
| `xbin.native.share({text, url, file})` | the share sheet; `file` is a tile-relative path the app downloads with your frame token → a promise of `true` when shared, `false` when dismissed |
| `xbin.native.open(url)` | opens an `https:` URL outside the app — anything else rejects at once, and the app refuses unless the tile holds `cap:open-links` (ND11) |
| `xbin.native.state` | the JSON blob you last saved (`null` at first) |
| `xbin.native.saveState(obj)` | keep a small JSON blob (at most 64 KiB — larger throws) that the app hands back as `state` when it recreates the runtime: the open screen, the selected tab. Never secrets |

`xbin.native` exists only in the runtime document (the app, and the
previews `bx` draws); your web page does not have it.

## The vocabulary

Props are JSON values. In the tables a bare `name` is a string; `bool` and
`number` say so; `a·b·c` is one of those strings; *italic* names a token
set (§Tokens); `[{…}]` is an array of objects with those fields and `{…}`
one object. **Children** says what may appear inside (*any* is any
primitive; *text → `label`* means the element's text content becomes that
prop). Every primitive has a revision (1 unless shown); an app lists the
revisions it renders in `caps`. The machine-readable vocabulary is
`/vendor/xb/vocab.js`. A prop of the wrong type, a raw colour or size where a
token belongs, an unknown prop or event: the runtime drops it and reports a
diagnostic (§Checking it) — the render goes on.

### Structure and navigation

- **One screen, or a stack of them.** A tile renders a `screen`, or a `nav`
  whose screens form a stack: the first is the root, each further one is
  pushed on top. Back hides the top screen at once and fires `@pop` with
  `depth`, the number of screens left — drop the pushed screen from your
  state and re-render.
- **`screen style`**: `list` and `form` screens hold `section`s (plus one
  `toolbar`); `scroll` holds anything. `refreshable` with `@refresh` is
  pull-to-refresh, a `search` prop (even `""`) shows a search field, and
  `@appear` fires each time the screen comes on screen.
- **`row`**: `detail` is the trailing value, `nav` shows a disclosure
  chevron (push your detail screen on `@tap`), `mono` picks the monospaced
  text. An `actions` child becomes the row's swipe actions and context menu;
  other children draw under the row (a spark chart, a progress bar).
- **`tabs`** builds only the selected `tab`'s content; a `tab`'s `key` is its
  prop, matched against `selected`.
- **`sheet`** is modal over everything. Bind `open` and clear your state in
  `@dismiss` (the user swiped it away). It usually sits beside the `nav` in
  a two-root template. In the sheet's own `toolbar`, the first `plain`
  button is its cancel: it takes the close button's place at the leading
  end (close the sheet in its `@tap`), and the other items sit at the
  trailing end.
- **`split`** is list/detail — exactly two children, side by side on wide
  screens and stacked when compact. Never for transcripts or terminals.

<!-- generated:prims-structure (node hack/native-docs.mjs --write) -->
| Primitive | What | Props | Events | Children |
|---|---|---|---|---|
| `nav` | a navigation stack; the first screen is the root | — | `pop` {depth} | `screen`, at least 1 |
| `screen` | one screen: a list, a form or a scroll view with a title | `title`, `subtitle`, `style` list·form·scroll, `large` bool, `refreshable` bool, `search` | `refresh`, `search` {value}, `appear` | any |
| `toolbar` | actions in the screen/sheet bar | — | — | `button`, `menu`, `picker`, `badge` |
| `section` | a titled group of rows, controls or content | `title`, `badge`, `footer`, `collapsible` bool, `collapsed` bool | `toggle` {collapsed} | any |
| `stack` | a vertical or horizontal stack | `axis` v·h, `gap` *gap*, `align` start·center·end, `wrap` bool | — | any |
| `list` | a lazy list | `style` plain·inset·grouped | `more` | `row`, `section`, `empty`, `progress`, `notice` |
| `row` | a list row; optional swipe/context actions and content | `title`, `subtitle`, `detail`, `icon` *icon*, `badge`, `tone` *tone*, `mono` title·subtitle·detail·all, `nav` bool, `selected` bool, `disabled` bool | `tap` | any |
| `actions` | a row's swipe actions and context menu | — | — | `button` |
| `disclosure` | a collapsible group | `title`, `open` bool | `toggle` {open} | any |
| `tabs` | segmented tabs; only the selected tab is materialized | `selected`, `style` segmented·bar | `change` {key} | `tab`; only the selected one is built |
| `tab` | one tab of tabs; `key` is a prop here (it does not key the node) | `key`, `title`, `icon` *icon*, `badge` | — | any |
| `sheet` | a modal sheet | `open` bool, `title`, `detents` medium·large, or a list of them, `edge` bottom·leading | `dismiss` | any |
| `split` | list/detail: exactly two children; stacked when compact | `prefer` auto·single | — | exactly 2 |
| `spacer` | flexible space in a stack | — | — | — |
| `divider` | a separator line | — | — | — |

- `fragment` is made by the runtime, never written: several top-level elements; no visual of its own (a nav with a sheet laid over it).
- `screen` `search`: the search query; present (even "") shows the search field.
- `sheet` `edge`: where it comes from: bottom (a sheet, the default) or leading (a drawer over the screen — a conversation list).
<!-- /generated:prims-structure -->

### Content

- **`text` is verbatim**, always — never parsed as markup. `lines` clamps it,
  `selectable` lets the user select it. Formatting comes only from
  `markdown` (§Markdown).
- **`chart`**: `series` is `[{name, points: [[x, y], …]}]`; `x` is `time`
  (ms since the epoch), `number` or `category` (x values are strings); `y`
  formats as `number`, `bytes` or `percent`. `spark` is a small axis-less
  line for rows.
- **`code`**: `copy` adds a copy button, `wrap` wraps long lines instead of
  scrolling sideways.
- **`progress`**: `value` from 0 to 1, or none for an indeterminate spinner.
- **`image`**: see §Images.

<!-- generated:prims-content (node hack/native-docs.mjs --write) -->
| Primitive | What | Props | Events | Children |
|---|---|---|---|---|
| `text` | verbatim text (never parsed as markup) | `text`, `style` *type*, `tone` *tone*, `mono` bool, `selectable` bool, `lines` number | — | text → `text` |
| `markdown` | markdown; the runtime lexes `source` into `tokens` | `source`, `streaming` bool | `link` {href} | — |
| `image` | a tile-relative or data: (≤ 256 KiB) image, loaded by the app | `src`, `alt`, `aspect` fit·fill, `height` *height*, `preview` bool | `tap` | — |
| `icon` | a named icon (Icons below) | `name` *icon*, `tone` *tone* | — | — |
| `badge` | a small capsule label | `text`, `tone` *tone*, `pulse` bool | — | text → `text` |
| `notice` | an inset banner | `tone` *noticeTone*, `title`, `text` | — | — |
| `progress` | value 0…1, or absent for indeterminate | `value` number, `label` | — | — |
| `chart` | a line, bar, area or spark chart | `kind` line·bar·area·spark, `series` [{name, points}], `x` time·number·category, `y` number·bytes·percent, `height` *height* | — | — |
| `code` | monospaced text with horizontal scroll | `text`, `copy` bool, `wrap` bool | — | text → `text` |
| `empty` | an empty state | `icon` *icon*, `title`, `text` | — | — |

- `markdown` `source`: lexed by the runtime; the wire carries tokens instead.
- `markdown` `tokens` is set by the runtime, never by a tile.
- `chart` `kind="area"` needs the app feature `chart.area`.
<!-- /generated:prims-content -->

### Controls

- **`button`**: `busy` shows a spinner and blocks taps; `confirm {title,
  message, label, destructive}` asks before `@tap` fires; `copy="text"`
  copies without a round trip to your code. In a `toolbar` a button with an
  icon shows just the icon (the label is read out by accessibility).
- **`field`**: `kind` picks the keyboard and control (`multiline` grows,
  `date`/`time` are pickers); `hint` and `error` sit under it; `submit`
  names the return key (`go`, `send`, `done`, `search`, `next`) and
  `@submit` fires on it. **Secrets go in `kind="secure"`** (§Security rules).
- **`picker`**: `options` are `[{value, label, icon}]`; `value` is a string,
  number or bool; `style` is `menu` (the default), `segmented` or `inline`.
- **`menu`**: a pull-down of `button`s (and `divider`s), each firing its own
  `@tap`.

<!-- generated:prims-control (node hack/native-docs.mjs --write) -->
| Primitive | What | Props | Events | Children |
|---|---|---|---|---|
| `button` | a button; `confirm` asks first, `copy` copies without a round trip | `label`, `icon` *icon*, `role` primary·secondary·destructive·plain, `disabled` bool, `busy` bool, `confirm` {title, message, label, destructive}, `copy` | `tap` | text → `label` |
| `toggle` | an on/off switch | `label`, `value` bool, `disabled` bool | `change` {value} | — |
| `field` | a text field; app-owned while focused | `label`, `value`, `kind` text·secure·number·email·url·multiline·search·date·time, `placeholder`, `hint`, `error`, `disabled` bool, `submit` | `input` {value}, `change` {value}, `submit` {value} | — |
| `picker` | one value out of `options` | `label`, `value` string\|number\|bool, `options` [{value, label, icon}], `style` menu·segmented·inline | `change` {value} | — |
| `menu` | a pull-down; its buttons fire | `label`, `icon` *icon* | — | `button`, `divider` |

- `button` `copy`: copied natively on tap (no round trip).
- `field` `submit`: the return-key label (go, send, done, search, next).
<!-- /generated:prims-control -->

### The chat family

The app draws these with the same components as its own agent screen.

- **`transcript`**: `follow` sticks to the bottom as content grows; `older`
  shows a loader at the top that fires `@more`; `@scrolled {atBottom}`
  reports the user scrolling away.
- **`message`**: `role` is `user` (a bubble), `assistant` or `system`;
  `markdown` makes `text` markdown (§Markdown); set `streaming` while it
  grows; `time` is ms since the epoch or a string; `files` are
  `[{name, mime, src}]`; `queued` marks a message not yet sent. An `actions`
  child adds its buttons to the message.
- **`thinking`** shimmers while `live` and folds to "Thought for Ns"
  (`seconds`). **`toolcard`**: `state` is `writing`·`running`·`ok`·`error`·`canceled`;
  its children (the call's code, output, a diff, a nested `transcript` for a
  subagent) show when it is open; `@open` asks for a full-screen view.
- **`approval`**: `options` are the answers `[{id, label, kind}]` (agent
  kinds such as `allow_once`, `allow_always`, `reject_once`); `feedback`
  adds a text box; `@choose {id, feedback}`; `settled {by, id}` shows who
  answered what.
- **`question`**: `schema` is a flat JSON Schema object — `string`,
  `number`, `integer` and `boolean` properties, choices from `enum` or
  `oneOf` consts, `format` `email`·`uri`·`date`·`date-time`, `required`,
  `default`, `title`, `description`. `@submit {content}` carries the answers
  keyed by property; `@skip` when the user skips; a non-null `settled`
  shows it answered.
- **`plan`** entries have a `status` of `pending`, `in_progress` or
  `completed`. **`diff`**: `files` `[{path, status, add, del}]` plus a
  unified `patch`; `@open-file {path}`. **`activity`** is a status line,
  **`step`** a glyph and a line.
- **`composer`**: bind `value` with `@input`, send on `@send {value}`;
  `busy` turns the send button into stop (`@stop`); `slash` lists commands
  `[{name, hint, description}]` offered when the text starts with `/`; child
  `button`s are chips above the box. **Attachments**: with `upload {method,
  path}` the composer gets an attach button — the app shows its own pickers,
  uploads the chosen file's bytes itself with your frame token (to `path`
  under `/api/<self>/`, unless `path` starts with `/api/`; `{name}` in it
  becomes the file's name; `method` defaults to `PUT`; `accept` filters the
  types) and fires `@uploaded {name, response}` with your backend's answer
  (parsed when it is JSON). Show the files as `attachments`
  `[{id, name, mime, progress}]`; `@remove {id}` when the user drops one.

<!-- generated:prims-chat (node hack/native-docs.mjs --write) -->
| Primitive | What | Props | Events | Children |
|---|---|---|---|---|
| `transcript` | a chat transcript (stick to bottom with follow) | `follow` bool, `older` bool | `more`, `scrolled` {atBottom} | `message`, `thinking`, `toolcard`, `approval`, `question`, `plan`, `diff`, `activity`, `step`, `notice`, `text`, `markdown`, `image`, `progress` |
| `message` | one chat message (a user bubble, assistant text, a system line) | `role` user·assistant·system, `sender`, `text`, `markdown` bool, `streaming` bool, `time` string\|number, `files` [{name, mime, src}], `queued` bool | `tap`, `link` {href} | `actions` |
| `thinking` | the model's reasoning, folded to "Thought for Ns" | `text`, `live` bool, `seconds` number, `open` bool | `toggle` {open} | — |
| `toolcard` | a tool call: title, state, chips; its children show when open | `title`, `icon` *icon*, `family`, `state` writing·running·ok·error·canceled, `chips` [{text, tone}], `open` bool | `toggle` {open}, `open` | `code`, `text`, `diff`, `image`, `transcript`, `markdown`, `notice` |
| `approval` | a permission request: the options to choose from, optional feedback | `title`, `text`, `options` [{id, label, kind}], `note`, `feedback` bool, `settled` {by, id} | `choose` {id, feedback} | — |
| `question` | a form built from a flat JSON Schema | `title`, `schema` JSON, `settled` JSON | `submit` {content}, `skip` | — |
| `plan` | a checklist of plan entries | `entries` [{text, status}] | — | — |
| `diff` | changed files (+/-) and a unified patch | `files` [{path, status, add, del}], `patch` | `open-file` {path} | — |
| `activity` | a status line, shimmering while `live` | `text`, `live` bool | — | — |
| `step` | a glyph and a line of text | `glyph`, `text`, `tone` *tone* | — | — |
| `composer` | the message composer; attachments are uploaded by the app | `value`, `placeholder`, `busy` bool, `disabled` bool, `attachments` [{id, name, mime, progress}], `accept`, `upload` {method, path}, `slash` [{name, hint, description}] | `input` {value}, `send` {value}, `stop`, `uploaded` {name, response}, `remove` {id} | `button` |

- `message` `tokens` is set by the runtime, never by a tile.
<!-- /generated:prims-chat -->

### Escape hatches

- **`terminal`**: `src` is a WebSocket path of your own backend's pty
  endpoint speaking the `/ws/term` framing (binary data plus
  `{"op":"resize"}` frames — [protocol.md](/docs/protocol.md)). It connects
  as your tile; the user's own xbind terminal is the app's, never a tile
  element. Previews draw a placeholder.
- **`canvas`**: a web view inside the native screen — `src` a page of your
  own tile (a relative URL, in your tile's sandbox) or `html`, static markup
  that runs no scripts; `height` sizes it. It is the one drawing escape:
  use it for the one chart the vocabulary cannot express, not for the whole
  UI.

<!-- generated:prims-escape (node hack/native-docs.mjs --write) -->
| Primitive | What | Props | Events | Children |
|---|---|---|---|---|
| `terminal` | a terminal on the tile's own pty WebSocket (tile-relative) | `src`, `title` | — | — |
| `canvas` | a WebView island: a tile page (src) or static no-script html | `src`, `html`, `height` *height* | — | — |
<!-- /generated:prims-escape -->

## Tokens

Tiles name roles, never raw values: a `tone`, a type role, a gap, a height.
Each renderer maps them — the app to iOS system colours (xbin amber as the
tint) and Dynamic Type, so every native tile follows the user's light/dark
setting and text size; the reference renderer to the web shell's palette
plus a light one. Padding, radii and elevation belong to the renderer.
`bx lint --native` flags raw colours (`tone="#f00"`, `rgb(…)`).

<!-- generated:tokens (node hack/native-docs.mjs --write) -->
| Token set | Values | What | Used by |
|---|---|---|---|
| `tone` | `muted`, `accent`, `ok`, `warn`, `danger` | a semantic colour; the renderer picks the shade for text, icons or fills | `row tone`, `text tone`, `icon tone`, `badge tone`, `toolcard chips[].tone`, `step tone` |
| `noticeTone` | `info`, `muted`, `accent`, `ok`, `warn`, `danger` | `tone` plus `info`, a neutral banner | `notice tone` |
| `type` | `largeTitle`, `title`, `title2`, `title3`, `headline`, `body`, `callout`, `subheadline`, `footnote`, `caption`, `caption2`, `mono` | a type role (Dynamic Type text styles on iOS, so text scales with the user's setting) | `text style` |
| `gap` | `none` 0, `xs` 4, `s` 8, `m` 12, `l` 16, `xl` 24, `xxl` 32 | the space between a stack's children | `stack gap` |
| `height` | `xs` 48, `s` 96, `m` 160, `l` 240, `xl` 360 | the height of an image, chart or canvas | `image height`, `chart height`, `canvas height` |
| `icon` | 72 names (Icons below) | a curated icon name; an unknown one draws a neutral placeholder | `row icon`, `tab icon`, `icon name`, `empty icon`, `button icon`, `picker options[].icon`, `menu icon`, `toolcard icon` |
<!-- /generated:tokens -->

`tone` means: `muted` secondary, `accent` the xbin amber, `ok` success,
`warn` needs attention, `danger` failure or destructive. Gap and height
values are points (the reference renderer draws them as px).

### Icons

<!-- generated:icons (node hack/native-docs.mjs --write) -->
| Name | SF Symbol | Name | SF Symbol | Name | SF Symbol |
|---|---|---|---|---|---|
| `plus` | `plus` | `minus` | `minus` | `check` | `checkmark` |
| `xmark` | `xmark` | `copy` | `doc.on.doc` | `share` | `square.and.arrow.up` |
| `refresh` | `arrow.clockwise` | `gear` | `gearshape` | `terminal` | `apple.terminal` |
| `box` | `shippingbox` | `key` | `key` | `lock` | `lock` |
| `unlock` | `lock.open` | `globe` | `globe` | `shield` | `checkmark.shield` |
| `bolt` | `bolt` | `clock` | `clock` | `chart` | `chart.xyaxis.line` |
| `link` | `link` | `paperclip` | `paperclip` | `send` | `arrow.up.circle.fill` |
| `stop` | `stop.circle.fill` | `play` | `play.fill` | `pause` | `pause.fill` |
| `sparkles` | `sparkles` | `wrench` | `wrench.and.screwdriver` | `warning` | `exclamationmark.triangle` |
| `trash` | `trash` | `list` | `list.bullet` | `ellipsis` | `ellipsis` |
| `search` | `magnifyingglass` | `filter` | `line.3.horizontal.decrease` | `pencil` | `pencil` |
| `folder` | `folder` | `file` | `doc` | `doc` | `doc.text` |
| `photo` | `photo` | `bell` | `bell` | `person` | `person` |
| `people` | `person.2` | `star` | `star` | `heart` | `heart` |
| `pin` | `pin` | `archive` | `archivebox` | `tag` | `tag` |
| `calendar` | `calendar` | `mail` | `envelope` | `chat` | `bubble.left` |
| `info` | `info.circle` | `question` | `questionmark.circle` | `error` | `xmark.octagon` |
| `home` | `house` | `download` | `arrow.down.circle` | `upload` | `arrow.up.circle` |
| `cloud` | `cloud` | `server` | `server.rack` | `database` | `cylinder` |
| `cpu` | `cpu` | `network` | `network` | `code` | `chevron.left.forwardslash.chevron.right` |
| `branch` | `arrow.triangle.branch` | `eye` | `eye` | `eye-slash` | `eye.slash` |
| `external` | `arrow.up.right.square` | `back` | `chevron.left` | `forward` | `chevron.right` |
| `expand` | `chevron.down` | `collapse` | `chevron.up` | `power` | `power` |
| `sun` | `sun.max` | `moon` | `moon` | `agent` | `person.crop.circle.badge.checkmark` |
<!-- /generated:icons -->

### Feature flags

Some values need more than the primitive's revision. An app without the
flag cannot draw the value — the tile then shows its web page — so branch
on it:

```js
html`<chart kind=${xbin.native.supports('chart.area') ? 'area' : 'line'} series=${series}/>`
```

<!-- generated:features (node hack/native-docs.mjs --write) -->
| Feature | What needs it |
|---|---|
| `chart.area` | `<chart kind="area">` |
| `markdown.tables` | markdown tables; without it the runtime sends each table as a `code` block of its source |
<!-- /generated:features -->

## Markdown

`<markdown source=${md}/>`, or `<message markdown text=${md}/>` in a
transcript. The runtime lexes the source with the vendored `marked` (GFM,
single newlines are line breaks) into a small, sanitized token tree the app
renders natively — web and iOS agree on what a document is:

- headings, paragraphs, lists (ordered, task lists), code blocks (with their
  language), block quotes, tables, rules; bold, italic, strikethrough,
  inline code, links and line breaks;
- **raw HTML is dropped**, images show as the text `[image: alt]` and are
  never loaded, and links keep only `http:`, `https:` and `mailto:` targets
  (others become their text);
- tapping a link fires `@link {href}` when you listen; opening it outside
  the app takes `xbin.native.open(href)` and the `cap:open-links` grant;
- **streaming**: set `streaming` while text is being appended — the runtime
  re-lexes only the last block — and clear it when done (the final render is
  a full lex).

## Images

`<image src=… alt=…/>` (also `message files[].src`):

- `src` is a path on the workspace — relative to the tile directory
  (`icons/logo.png`) or your own API (`/api/${xbin.self}/thumbs/7`) — which
  the app loads **itself, with your frame token**, like `xbin.fetch`; or a
  `data:` image of at most 256 KiB. Serve remote images through your
  backend: an external URL is not a tile resource.
- Image bytes never cross the bridge between your code and the app.
- `aspect` `fit`·`fill`, `height` a token; `preview` opens a full-screen
  view on tap, and `@tap` fires as well when you listen.

## Security rules

- **Your native UI runs as your tile.** Same frame token, grants and
  opaque-origin sandbox as `index.html`; the user's credential never reaches
  tile code. Trusted chrome (`chrome: true` tiles, the shell) never gets a
  native UI.
- **No device APIs exist** for tiles: no camera, location, contacts, files,
  notifications. Where the user picks something (the composer's
  attachments), app UI does the picking and your code gets the result.
- **`text` is verbatim; markup comes only from `markdown`**, which is
  sanitized as above.
- **Secrets go in `field kind="secure"`**: the app never persists, logs or
  snapshots its value and clears it when it goes to the background. Every
  other prop is ordinary UI state — it shows up in previews, `bx native
  tree` output and diagnostics — and so does `saveState`: keep secrets out
  of both.
- **Leaving the app** (`xbin.native.open`, markdown links) takes `https:` and
  the `cap:open-links` grant, as new tabs do on the web (ND11).
- **Images and uploads** go to and from your own workspace paths with your
  frame token; a `canvas` with `html` runs no scripts; a `terminal` talks
  only to your own backend.

## Fallback, versions and older apps

The app shows the tile's **web page** instead, with a short banner and the
reason in the tile's report, when:

- `native.js` fails to load (a syntax error, a missing import);
- no tree arrives within 5 s of opening, or the first render throws (a later
  render that throws keeps the previous tree);
- the tree needs something this app lacks — a primitive, a prop newer than
  the app's revision of it, a feature flag (an older app: "update the app for
  the native view");
- the runtime crashes, or native views are switched off: by the user (the
  app's Settings), by the workspace (`whoami.native.runtime` 0), or for an
  app build with a known problem (the app's remote switch — so a bad app
  release falls back to web pages without an update).

Shipped apps lag behind xbind by months. The vocabulary only grows
([compat.md](/docs/compat.md)): a new prop raises its primitive's revision,
a new value that needs app support gets a feature flag. To use something
new without dropping older apps to the web page, branch on
`xbin.native.supports(name, rev)`. Keep `index.html` working — it is what
older apps, failures and every browser show — and check both.

## Performance

- Patches, not trees, cross to the app, and lists are lazy: re-render
  freely.
- Key lists with `repeat(items, keyFn, …)` so a changed list moves rows
  instead of rebuilding them.
- Keep props small. A list of thousands of rows is one screen of rows plus
  `@more` on a `list` (or `older` on a `transcript`) for the rest.
- Poll slower while `document.visibilityState` is `hidden`; the app may
  suspend a hidden tile's runtime and recreate it later — `saveState` what
  should survive (the open screen, the selected tab).
- Give images URLs, not `data:`; stream markdown with `streaming`.

## Checking it

An agent in a tile terminal cannot hold a phone. Three commands load the
tile's runtime document in headless Chromium — your code, identity and live
backend — and read what it renders ([bx.md §Native UIs](/docs/bx.md) has
every flag):

```sh
bx lint --native                                  # every native tile: errors, diagnostics, coverage
bx preview --native apps/x --out /tmp/x.png       # the reference renderer's picture — open it
bx preview --native apps/x --dark --large-text --out /tmp/x-dark.png
bx native tree apps/x                             # the tree JSON, cheapest to diff
bx preview --native apps/x --data fixture.json --out /tmp/x.png   # scripted data instead of the backend
```

`bx lint --native` reports the runtime's diagnostics — `unknown-tag`,
`unknown-prop`, `unknown-event`, `bad-type`, `bad-token`, `bad-value`,
`bad-children`, `child-rule`, `duplicate-key`, `lit-template`, … each with
the template's file, line and tag — plus uncaught errors, how long the first
tree took, the tree's size and which app revision each primitive needs.

In a browser, open `/c/<tile>/?native=1&preview=1` (add `&theme=dark`,
`&text=large`): the reference renderer draws the tile's native UI there, and
it takes taps and typing. It is a preview of what the app draws, not a
pixel-exact one.

## Under the hood

- The runtime turns each render into a tree of nodes `{k, t, p, e, c}` — key,
  primitive, props, the events you listen to, children — and posts a `mount`,
  then `patch`es (`set`, `unset`, `events`, `insert`, `remove`, `move`); the
  app answers with events, visibility changes, frame ticks and call results.
  The exact contract, the vocabulary as JSON and the renderer fixtures live
  in the xbin repository under `native/`.
- `/vendor/xb-native.js` also exports `createRuntime` (an independent runtime
  for tests), `attach` and `applyOps` (preview hosts), `boot` and `VOCAB`.
  The reference renderer is `/vendor/xb/render.js` (`<xb-view>`); it follows
  the app's look and is for previews and tests, not for tiles to import
  ([frontend-kit.md](/docs/frontend-kit.md)).
