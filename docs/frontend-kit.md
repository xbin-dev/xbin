# The frontend kit — what a tile may import from `/vendor/`

Every shipped frontend module lives under `/vendor/` beside the vendored
libraries (lit, xterm, marked, highlight.js, qrcode-generator — the last at
`/vendor/qrcode.mjs`), served unauthenticated so a sandboxed tile frame can
load it. This page says which of those modules are **for tiles**, which are
**shell chrome**, and the rules that keep the URLs stable across xbind
upgrades ([compat.md](/docs/compat.md) rule 3).

Import by **absolute URL** — never a bare specifier. The import map lives
in each workspace's `xbin.json` and no upgrade rewrites it, so a bare name
that works in a fresh workspace 404s in an older one:

```js
import { xbinApi, jbody, esc } from '/vendor/bx-kit.js';
import '/vendor/bx-frame.js';
```

## Modules tiles may import

| Module | What it is |
|---|---|
| `/vendor/bx-kit.js` | the helper kit: `api(url, opts)` (JSON out, throws the server's `error`; in a sandboxed tile the request carries the frame token — your tile's identity — in chrome the session cookie, i.e. the signed-in human), `xbinApi('/grants')`, `selfApi('/runs')` (your own backend), `jbody(value, method?)`, `esc(text)` (HTML-escapes `&<>"'`: safe in text and in attribute position), `deepActive()`, `pathHas(event, selector)`, `clampBox(box, {minW, minH, margin})`, `anchorBox(rect, {dx, dy, w, h}, bounds?)` / `anchorOffsets(rect, box)` (a window anchored to an element, as a viewport box and back; `bounds` fences its top-left), `followBox(target, box, alive)` (the animation-frame loop that keeps such a window on its anchor), `dragPointer({ev, cursor, onMove, onUp, onCancel})` (a window-level pointer drag: the iframe shield holds the pointer — captured from `ev`, the pointerdown — until the release, then `onUp(e)`; Escape, a cancelled or lost pointer or the window losing focus end it too, through `onCancel()` or else `onUp`; the shield never outlives the drag; a tile page that had the keyboard has it again after the drag, unless a callback focused something else), `dragWindow(ev, el, {bounds, onMove, onUp})` (a title-bar drag of a fixed window, fenced by `bounds`; Escape puts it back), `dragResize(ev, el, edge, {maxW, maxH, onMove, onUp})` (a fixed window's resize grip — `edge` `e`, `s` or `se` — through the same drag: use it instead of CSS `resize`, whose drag a tile iframe under a fast cursor takes), `dragShield(cursor)`, `sandboxed()` |
| `/vendor/bx-frame.js` | `<bx-frame src="apps/x">` — embed another tile (with its terminal pop-up); exports `clampBox` for compatibility. `src` may name a tile deployment, `apps/x+dev`: the frame loads `/c/apps/x+dev/` with a frame token for that deployment, never as chrome, and reloads and paints its build overlay from the tile's `deployments` events ([tile-deployments.md](/docs/tile-deployments.md)). Or keep `src` the tile and set `deployment="dev"`: only the page follows it — its terminal pop-up, code, logs and proposals stay the tile's (the shell's windows do this). `open(layout)` also takes `'deployments'`, the window's Deployments panel (a layout never restored after a reload: the window comes back on the terminal). `open({session: "new", draft?})` opens the window on a **new session's launcher** over its panes — the coding agents the tile allows — with `draft` (plain text, at most 8000 characters, never interpreted) shown beside them; nothing starts until the person picks one, whose composer then holds the words, unsent (resolves `true`; the workspace agent's "Hand this to <tile>'s own agent", reached through the shell — a tile page's `xbin.openSession` can't ask for it) |
| `/vendor/bx-terminal.js` | `<bx-terminal src="…">` — a terminal on any endpoint speaking the terminal wire, dialled with your frame token: a sandbox manager's `tty` through your bound interface, your own pty route ([elements.md](/docs/elements.md) §`<bx-terminal>`). Its `/ws/term` attributes (`cwd`, `net`, …) are the shell's |
| `/vendor/bx-dialog.js` | `<bx-dialog>` — a modal; `xbin.dialog()` falls back to it outside the shell |
| `/vendor/bx-grants.js`, `/vendor/bx-bindings.js` | the owner's grant-approval and interface-binding panels |
| `/vendor/bx-multiselect.js` | `<bx-multiselect>` — a compact multi-pick control |
| `/vendor/bx-netrules.js` | the network-set rule grammar (`RULE_KINDS`, `parseRule`, `fmtRule`, `ruleProblem`, `netOptions`, …) — interpreted client-side exactly as the server defines it (D54) |
| `/vendor/bx-allow.js` | the allowance grammar (`ALLOW_KINDS`, `parseAllow`, `fmtAllow`, `allowProblem`, `describeAllow`, `capInfo`) (D57) |
| `/vendor/bx-grant-row.js` | `grantArrow(g)` — a grant or request's `from → target` with the policy-block / capability tooltip, as rendered by the shell, the admin console and the organisations tile |
| `/vendor/bx-code.js` | `<bx-code>` (file tree + highlighted viewer + diffs); exports `diffHTML(diff, startLang?)`, `diffStats`, `hl`, `langFor`, `buildTree` |
| `/vendor/bx-md.js` | the hardened markdown renderer for **untrusted** text (a model's output, what a person typed): `md(text)` → HTML in which raw HTML is escaped, an image is its text (`<span class="md-img">[image: alt]</span>`, never loaded), a link opens in a new tab with no opener and never on a `javascript:`, `data:` or `vbscript:` URL, and a closed code fence in a language highlight.js knows is coloured (at most 32 KiB; a fence still being written stays plain; the language becomes a `language-…` class only when it is a plain name); a parse error is escaped text. `mdInto(el, text, memo?)` renders into `el` a top-level block at a time (`<div class="md-b">` each, `display: contents`) so text that streams re-parses only its last block and a selection above it survives; `memo`, an object you keep with the text, spares the parse when the element is new. `mdCssText(scope)` is the CSS for the result under a selector, on the theme's tokens with Night's fallbacks (for a shadow root: `unsafeCSS(mdCssText('.md'))`). `closedFence(raw)`, `mdEpoch` and `onHighlighter(fn)` are the chat kit's (next row); here the highlighter is loaded with the module, so the first HTML is coloured and `mdEpoch` never moves afterwards. It has a marked instance of its own: your document's `marked` is not changed by it |
| `/vendor/chat/md.js` | the same renderer — the same functions and state as `/vendor/bx-md.js` in a document — without the highlighter up front: `/vendor/highlight.min.js` is imported on the first fence that could use it, code is plain until it arrives (and stays plain if it never does), then `mdEpoch` goes up by one and every `onHighlighter(fn)` callback runs: HTML you kept from an earlier epoch is stale, so render again (`mdInto` and its `memo` do that themselves at the next call). `useHighlighter(hljs)` hands over one you already loaded. For a page that should paint before 127 KB of highlighter arrives; the agent template's chat uses it |
| `/vendor/chat/rev.js` | `KIT` — the revision of the chat kit this xbind serves (the modules under `/vendor/chat/`), a whole number that only goes up, as the kit only grows; the file lists what each revision brought. A page that ships apart from xbind (a template instance, a builtin tile in a workspace) and needs the kit imports this first, guarded — `try { ({ KIT } = await import('/vendor/chat/rev.js')) } catch {}` — and compares before it imports anything else from the kit: on an xbind from before the kit the URL is a 404. The agent template's `boot.js` is the pattern ([compat.md](/docs/compat.md) §What this does not promise) |
| `/vendor/events-socket.js` | `onEvent(cb)` — every `/ws/events` frame goes to `cb(e)` over the one shared socket; returns an unsubscribe. Filter on `e.type` yourself |
| `/vendor/theme.css` | the design tokens (§Theme below: Concrete Night and Concrete Day, D184), the self-hosted fonts, opt-in `.bx` base and control styles, and the workspace's thin scrollbars (D123): on a mouse/trackpad a 6px bar whose thumb is drawn 3px and fattens under the pointer, the scroller the next wheel or key would move tinted in the focus colour (`[data-bx-scroll]`); touch keeps its native bars. Link it to take the theme; it is **never injected** into your document, and your document turns light only when it opts in (`<html data-bx-theme="auto">`). A document that links it also gets the focused-scroll tracker (below) from `xbin-client.js`; `<meta name="xbin-scroll-focus" content="off">` opts out |
| `/vendor/bx-theme.js` | a document's appearance, for code that paints or follows the person (§Theme): `appearance()`, `setAppearance()`, `follows()`, `scheme()`, `token(name, el)`, `onAppearance(cb)`. Dependency-free |
| `/vendor/bx-icons.js` | `<bx-icon name="lock">` and `iconSvg(name, {size, label})`: the workspace's drawn glyphs, 16px in `currentColor` (§Theme › Icons). Dependency-free |
| `/vendor/fonts/` | Instrument Sans, JetBrains Mono and Bricolage Grotesque (woff2, SIL OFL 1.1, the licences beside them); `theme.css` loads them, each URL with its version, which xbind serves as immutable — a reload draws them from the cache. Use the `--bx-sans`, `--bx-mono` and `--bx-display` tokens rather than naming them |
| `/vendor/bx-scroll.js` | the scrollbar CSS for **shadow roots** (a document stylesheet does not reach them): `scrollCssText`, the same rules `theme.css` carries — a lit element puts `unsafeCSS(scrollCssText)` (or `/vendor/scroll-css.js`'s shared `scrollCss`) in its `static styles`. Importing it installs `installScrollFocus()` once per document: it keeps `data-bx-scroll` on the scroller the next scroll would move — the innermost scrollable under the mouse, the one a wheel actually latches onto (at its end, the next one out), or the focused element's on a scroll key — and hands off to/from framed documents (`xbin:scroll-focus`, [protocol.md](/docs/protocol.md)). Dependency-free |
| `/vendor/chat/blocks.js`, `/vendor/chat/rev.js` | the **chat kit**'s contract ([The chat kit](#the-chat-kit) below says how the modules fit) — the kit is what xbind's Agent tab draws its transcript with, and what a tile's own chat can. `blocks.js` is pure (no lit, no DOM, no imports): the block model a transcript is a list of — JSDoc typedefs of every kind (`user`, `assistant`, `draft`, `thought`, `tool`, `agent`, `changes`, `plan`, `perm`, `ask`, `signin`, `notice`, `turn`, `gap`, and `host`: a card the host draws itself), each with a key `k` and a version `v`; the one record of a tool call (`ToolRecord`, `newToolRecord(id)`, `toolDone(status)`, `toolState(rec, hint)`); what a view may ask of its host (`ChatCtx`); a reducer's helpers — `block`, `up`, `bump`, `adopt`, `nest`, `inner`, `walk`, `find`, `Versions`, `plain`, `cached(block, slot, fn)` (a value memoized per version); `usageBadge(usage)` (`ctx 26% · $0.41`, its tone and title); `KINDS`, `TOOL_KINDS`, `TOOL_STATUS`, `TOOL_STATES`. `rev.js` exports `KIT`, the kit's revision (5: a plan's and a question's words in the host's; 3: the views; 2: `tools.js` and `ask.js`): import it with a guarded dynamic `import()` — an xbind without the kit answers 404 — and compare before relying on a newer kit. The shapes only grow: a field or an export is never removed or re-shaped |
| `/vendor/chat/tools.js` | the chat kit's model of **one tool call**, read from its `ToolRecord` — pure (its one import is `blocks.js`), so a native view loads it too. The record from an agent's events: `mergeTool(rec, data, {cap})` (one `tool.call` / `tool.update`: fields replace, a content list is read into `text`, `diffs` and `terminal` and never loses the diffs or text a later list leaves out, `output` replaces then `outputDelta` appends, `files` = locations then diffs), `startsNewCall`, `ToolRecords` / `records(events)` (a whole log, the id-reuse rule included), `toolDiff`, `lineCounts`, `textLines`, `changeSet`, `firstText`, `OUTPUT_MAX`. Its words: `headline(rec)` — the harness's label, a `description` argument, a shell command read deterministically (`describeCommand`: "Python script (12 lines) → main.go", "Edit a.txt (sed)"), the title's first line, a reading by kind (`reading`: "Read a.go:10–49"), the tool's name — `subline(rec, said)` (an execute's `$ command` under a headline that says something else), `commandOf`, `inputCommand`, `inputPath`, `rawText`, `taskOf`, `placesOf`, `firstLine`, `oneLine`. What it came to: `outcome(rec)` (`exit 1`, `+3 −1`, `2 files …`, `12 lines`, `4 matches` as `{text, tone}`), `stateChip(rec, state)`, `diffTotals`. Output: `stripAnsi` (the one rule: CSI with any parameter byte, an OSC up to its own terminator — a hyperlink keeps its text — character-set designations, two-byte escapes), `outputTail(rec, n)` → `{text, cut, dropped}`, `OUTPUT_TAIL` (20 000). Diffs, as a patch or as the two texts: `diffFiles`, `diffPatch`, `unifiedDiff`, `joinPatches`, `patchLines` (a patch's lines with `bx-code.js` `diffHTML`'s classes, for drawing one plain), `filesStat`; a request's preview: `lineDiff`, `contentPreview`, `diffsPreview`, `patchOf`, `PREVIEW_LINES`. Plans: `isPlanApproval`, `planText`, `planPath` |
| `/vendor/chat/ask.js` | the chat kit's model of **what an agent asks and what a person types to it** — pure. A permission request (a `perm` block) as its card: `permission(perm, {name, can, diffs, scopedOnly, where})` → `{plan, planText, planPath, lead, title, label, command, raw, preview, description, rule, options, hidden, reject}` — the agent's options in its order (rejections first when it defaults to no), an `explicit` one (it raises what the agent does without asking) only for who may raise, counted in `hidden` otherwise, what "always" will also allow in words; `decisions(perm)` (Allow once / Allow for the session / Deny for a host that takes a bare decision), `permAnswer(option, feedback)` → `{optionId \| decision, verdict, feedback}` (feedback only with a rejection), `optionConfirm`, `modeConfirm`, `isAllow`, `isReject`, `PERM_VERB`. A question (an `ask` block): `question(ask)` → a form (`fields`), a page to open (`mode: 'url'`, a link only when http(s)) or only its message; `formFields(schema)` (a single choice from `oneOf`, `anyOf` or `enum` with `enumNames`, a multiple one, boolean, number, text; each property's `default` as `dflt`; an "Other" box riding on its question), `formContent(fields, values)` (an untouched field answers its default), `missingRequired`, and for the native `question` primitive `nativeSchema` / `nativeContent`. The slash menu's one rule — names that start with what is typed, then names or descriptions containing it, at most `SLASH_MAX` (8): `slashCommands`, `slashQuery`, `matchCommands`, `slashMenu`, `commandHint`, `slashText`. The composer while a turn runs: `composerWords(state)`, `steerTracker(ms)`. `cleanCode(s)` (a pasted sign-in code) |
| `/vendor/chat/views.js` | the chat kit's **views**: lit template functions of `(block, ctx)`. `rowsView(blocks, ctx)` — a transcript's rows, one keyed `<div class="bxc-row" data-k>` per block, each drawn again only when its block's `v`, the host's `ctx.rev(block)` or a highlighter changed; `rowView`, `blockView(block, ctx, depth)` (the host is asked first, `ctx.other`; then the kit's view of the kind), `innerView(block, ctx, depth)` (a block inside another's card: `<div class="bxc-in" data-k>`); `userView` (the reader's own words on their side; another person's under their name; file chips with a picture or a glyph, the path and a size), `assistantView` (markdown; a Copy where the host has `ctx.copyMessage`), `draftView`, `thoughtView`, `noticeView` (a line; a block's `icon`, `tone`, `md`, `fold` / `title` and `action` make it a journal line, a folded heading, a button), `turnView`, `gapView`, `hostView`; `mdLive(text, block)` (a directive: streaming markdown, a block at a time); `activityView(blocks, ctx)` ("Running …" under a running transcript); `usageView(usage, {short})`. Imports lit by URL (`/vendor/lit-all.min.js`), so it loads on a page with no import map |
| `/vendor/chat/cards.js` | the kit's **cards**: `toolView(block, ctx, depth, child)` (a tool call, folded to one line: its glyph, its headline with the command under a description, what it came to — `exit 1`, `+3 −1`, `12 matches` —, a still mark while it runs, and its state only while that needs a look (needs approval, failed, cancelled); open, the tool's name, its command with a copy button, its terminal output's tail and exit status, its diffs, the places it touched, what it changed on disk a file at a time; a block's own `icon`, `sub`, `outcome`, `args` and `result` word a tool of the host's own; a subagent's call with its blocks nested), `agentView` (a sub-conversation: a card whose line reads like a call's — what it is called, `#ref`, where it is while it works, its steps, "open" — unfolding to its task, its blocks and its answer; what it waits on a person for is drawn under the card, folded or not), `changesView` (a turn's change set), `filesView(block, set, ctx, label)`, `planView` (an entry a line; from revision 5 a plan block may carry its `title`, a `meta` line beside it, a `note` under it, `numbered`, and an entry's `tag` — `{text, icon, tone}`, a word with a glyph at the line's end: "reads only", "asks you", "waiting for you"), `commandView`, `contentView`, `diffsView(block, diffs, ctx)`, `textView`, `argsView(args)` (arguments a row each), `clipView(text, block, ctx, slot)` (a plain text, its first 1 200 characters until "show all"), `diffView(text, ctx, lang)`, `KIND_ICON`. Diffs are highlighted by `/vendor/bx-code.js` `diffHTML`, imported on the first diff drawn — `useDiffHighlighter(diffHTML, langFor)` hands it over when the host has it already — and drawn plain with the same line classes until it arrives or where it cannot load; `diffEpoch` moves when it does |
| `/vendor/chat/asks.js` | the kit's views of **what an agent asks**: `permView` (a permission request — who asks what ("Fable asks to run a command", or the block's own `lead`), the command or the calls it is for, the diff it would make, the agent's options, a word with a rejection where the host can send one; a plan's approval with a box for what should change; once answered, by whom), `askView` (a question: the form, a page to open with Done / Skip, or its words alone), `signinView` (the agent is signed out; `ctx.signin` fills it), `canOf(ctx)`, `bagOf(ctx, block)`, and the element `<bx-chat-form>` (`BxChatForm`: properties `block`, `ctx`, `fields`, `readonly`; methods `values()`, `submit()`, `skip()`; light DOM; its buttons say the ask block's `submit` and `skip` when it has them — `skip: ''` draws no Skip — from revision 5). An option that raises what the agent does without asking (`explicit`) is drawn as a caution, offered only to who may raise, and confirmed through `ctx.confirm` before `ctx.permit` is called |
| `/vendor/chat/composer.js` | `<bx-chat-composer>` (`BxChatComposer`, light DOM) — the message box: text, files (a button, a paste, a drop; staged as chips until sent), the slash-command menu, pickers (`<select>`s the host describes: the agent, its mode, its model), the messages held while a turn runs with a × that takes one back, Send and Stop. Properties `draft`, `placeholder`, `label` (the box's accessible name; default the placeholder — revision 5), `sendLabel`, `sendTitle`, `stopTitle`, `disabled`, `sending`, `busy`, `commands`, `queue`, `queueHint`, `attach`, `limits`, `pickers`, `unavailable`, `fit`; events `bx-chat-draft {text}`, `bx-chat-send {text, files: [{name, mime, size, data}], interrupt, sent()}` (data is base64; the host calls `sent()` once the message went), `bx-chat-stop`, `bx-chat-take-back {id}`, `bx-chat-pick {id, value}`; methods `send(interrupt)`, `addFiles(files)`, `removeFile(i)`, `slashItems()`, `pickSlash(command)`, `focus()`, and `files`. It sends nothing itself |
| `/vendor/chat/window.js` | `ChatWindow` — a long transcript as a window of its rows over `/vendor/scroll-window.js`: `place(blocks)` → `{start, end}` right before a render, `after(scroller)` right after; `fill()`, `older()`, `latest()`, `loadAll()`, `unload()`, `load(fn)`, `keepView()`, `toBottom()`, `pill()` ("3 new — jump to latest"), `reset()`, `detach()`, `atBottom`. The host passes a `source` (`version`, `hasOlder`, `hasNewer`, `fresh`, `loadOlder()`, `loadNewer()`, `latest()`, `keep(lo, hi, dropTail)`, `follow(atBottom)`) and `changed()`; options `keyOf`, `page`, `trim`, `unload`, `minMargin`, `growFirst`, `holdWhileBusy`, `retrim`, `paused`, `onUnload` |
| `/vendor/chat/css.js` | `chatCssText` — the kit's one stylesheet as text: every class starts with `bxc-`, every colour and size is a theme token with Night's value as its fallback. Put it in a shadow root (`unsafeCSS(chatCssText)`) or in a document's `<style>`; it styles nothing outside its prefix (and the two elements above). Pure text — no lit |
| `/vendor/scroll-window.js` | `ScrollWindow` — a long list that renders a window of its rows and never moves what the reader is looking at (D124, D130; the Agent tab's, framework-free): `attach(scroller)` (it sets `overflow-anchor: none`), then `before()` right before every render and `after()` right after it, in the same frame — the bottom stays pinned while the reader is there, else the first visible row keeps its place. Rows are the scroller's direct children with a key (`rows`: a selector, default `:scope > [data-k]`; `keyOf(el)`). `wantsAbove()` / `wantsBelow()` say when to render (or fetch) more rows, `trimAbove()` / `trimBelow()` the key of the last row to keep once more than 6 views of rows lie beyond the view (keep 2.5: never ping-pongs with the 1.5-view fill); `onScroll`, `onResize`, `atBottom`, `keepView`, `toBottom()`, `firstVisible()`, `rowsPerView()`. A touch scroll grows the window only once it settles. Dependency-free |
| `/vendor/xb-native.js` | a tile's **native UI** for the xbin mobile app: `html`, `render`, `repeat`, `nothing` (lit-shaped), and `widget` (the tile's card on the app's screens) over the native vocabulary (`/vendor/xb/vocab.js`: `screen`, `section`, `row`, `field`, `button`, …). A tile's `native.js` imports it; the app runs that file in a hidden document with the tile's own identity and draws what it renders with platform UI, re-rendering by patches. Also exports `native` — in the app the same object as `xbin.native` (`caps`, `supports()`, `meta()`, `copy()`, `share()`, `open()`, `state`, `saveState()`, `widgetSize`, `on('widgetsize')`). Outside the app nothing loads `native.js`; browsers keep showing `index.html`. The reference — templates, every primitive, the rules — is [native.md](/docs/native.md). Worked examples: `examples/counter-go/native.js` (one round trip, and a widget) and `examples/calendar/native.js` (a list, a form, a bus refresh); the builtin chat, egress-approver, prometheus-viewer, s3-archiver and webhooks tiles ship one too — logic a page and its native view share lives in a plain module both import (chat's `chat-core.js`, the viewer's `prom.js`) |
| `/vendor/xbin-client.js` | injected into every tile document by xbind — do not import it yourself. Besides `xbin.self` (the tile path in every deployment) it sets `xbin.deployment` only in a document of a tile deployment other than the primary (`/c/<tile>+<name>/`): its name; absent means the primary. Likewise `xbin.partition`, only in a partitioned tile's document: the partition the viewer reaches, `user:<id>` or `global` ([partitions.md](/docs/partitions.md)) |

## The chat kit

`/vendor/chat/…` is how xbind's Agent tab draws a conversation with a coding
agent, offered to tiles as the same modules. It has three layers, and a tile
can stop at any of them:

1. **The model** (pure: no lit, no DOM) — `blocks.js` (what a transcript is),
   `tools.js` (one tool call from its record), `ask.js` (a request's card, a
   form, the slash menu). A native view imports these too.
2. **The views** — `views.js`, `cards.js`, `asks.js`: lit template functions
   of `(block, ctx)`, with `css.js` their one stylesheet.
3. **The widgets** — `composer.js` (`<bx-chat-composer>`), `asks.js`'s
   `<bx-chat-form>`, `window.js` (`ChatWindow`).

A host is whatever owns the conversation: it makes the blocks (a *reducer*:
the Agent tab folds its session's event log, `/vendor/agent-fold.js`
`blockOf`; a tile folds its own rows), renders them with `rowsView(blocks,
ctx)`, and answers what the views ask through **`ctx`** — the only way a
view reaches it:

```js
import { html, render } from '/vendor/lit-all.min.js';
import { rowsView } from '/vendor/chat/views.js';
import { chatCssText } from '/vendor/chat/css.js';

const open = new Set();
const ctx = {
  running: false, ended: false, agentName: 'Fable', role: 'owner',
  isOpen: (b, slot, dflt) => !!dflt || open.has(b.k + ':' + slot),
  setOpen: (b, slot, o) => { o ? open.add(b.k + ':' + slot) : open.delete(b.k + ':' + slot); paint(); },
  rev: (b) => [...open].filter((k) => k.startsWith(b.k + ':')).length,
  permit: (b, answer) => api(`permissions/${b.pid}`, answer),   // {optionId | decision, verdict, feedback}
  answer: (b, answer) => api(`questions/${b.eid}`, answer),     // {action, content}
  repaint: () => paint(),
};
const paint = () => render(html`<style>${chatCssText}</style><div class="scroller">${rowsView(blocks, ctx)}</div>`, el);
```

**`ChatCtx`** (the typedef in `blocks.js` is the reference; a member a host
leaves out is simply not offered):

| Member | What the views use it for |
|---|---|
| `running`, `ended`, `readonly` | a turn runs (a live thought stays open); the session is over; the reader only looks — requests are then drawn without their answers |
| `agentName` | the agent in a card's words ("Fable asks to run a command") |
| `me`, `role`, `can` | the reader: their id (another person's message carries its sender), `'owner'` \| `'participant'` \| `'viewer'`, or `{answer, raise}` outright. A viewer answers nothing; only who may `raise` is shown an `explicit` option |
| `isOpen(block, slot, dflt)`, `setOpen(block, slot, open, dflt)` | the folds: a card's body (`body`), a change set's files (`files`), one file's diff (`f:<path>`), a diff shown whole (`…:all`), a long result shown whole (`full`). A fold is drawn as `isOpen` says — the default while the host holds nothing — and `setOpen` hears every move of the reader's, closing included (a host may ignore that and keep a body it already drew) |
| `rowAttrs(block, depth)` | the host's own hooks on a block's element (a row, or a block inside a card): `{class: 'a b', 'data-x': …}` beside the kit's `bxc-row` / `bxc-in`, `data-k` and `data-kind` — for its tests and its own styles |
| `rev(block)` | anything that changes when the host's state for the block does (its folds, a diff that loaded): the row is drawn again |
| `state(block)` | a bag for the block's widgets (a form's values, a rejection's feedback) that outlives the row leaving a window |
| `permit(block, answer)`, `answer(block, answer)` | send an answer; a promise keeps the buttons waiting |
| `callOf(block)`, `rejectWord` | a request's card: the `ToolRecord` of the call it is about (its diffs are previewed when the request carries none); the host can send a word with a rejection (the answer's `feedback`), so the card has a box for one |
| `confirm(text)` | confirm an `explicit` option (default: the browser's `confirm`) |
| `note(block)` | why the last answer was refused, under the card |
| `sessionRules` | the host's "always" is its own rule: `allow_always` is offered only when the request says what the rule would cover |
| `fileDiff(block, set, file)`, `openDiff(block, set, file)` | one file's diff of a change set (`{state: 'ok' \| 'loading' \| 'none', text, cut, note}`); show it in the host's own viewer |
| `fileState(block, file)`, `openFile(block, file)` | a message's file: still there? open it |
| `copy(text)`, `copyMessage(block, event)` | the clipboard (default `navigator.clipboard`); copy one of the agent's messages — its Copy button is drawn only for a host that has this |
| `act(block)` | a notice's `action` was pressed |
| `openAgent`, `loadAgent`, `agentError` | a sub-conversation's card: show it on its own, read its blocks, why that failed |
| `signin(block)`, `host(block)`, `other(block, depth)` | the host's own drawing: the inside of the sign-in card; a `host` block's card; any block at all (asked first — `null` leaves it to the kit) |
| `feedbackHint` | the plan-feedback box's placeholder (`agentLabel` is no longer drawn: messages carry a name only when another person wrote them) |
| `repaint()` | draw again (a highlighter arrived, a widget's state moved) |

**A tool of the host's own.** A `tool` block draws from its `ToolRecord`
(`tools.js` says its headline, its subline, what it came to). A reducer
that knows a call better than a record can say — a tool of its own, with
its own summary and its own reading of the result — sets the block's own
words and the card uses them: `icon` (a `bx-icons` glyph), `sub`,
`outcome` (`{text, tone}`), `args` (its arguments worth showing, a row
each) and `result` (plain text). One card, whoever made the call.

**A card of the host's own.** A block of kind `host` (`{kind: 'host', type,
data, title}`) gets a row and a frame from the kit and its inside from
`ctx.host(block)`. Use it for what only the host can say or may do — a
session it started elsewhere, a request that trusted chrome must grant. The
kit never draws an answer for one.

**The stylesheet.** `chatCssText` uses only `bxc-` classes and the theme's
`--bx-*` tokens; `--bxc-gap` (default 12px) is the space under a message.
Rows are block elements: give them a scroller (`ChatWindow` wants
`overflow-anchor: none` on it, which `ScrollWindow` sets). Inside a rendered
diff the classes are `bx-code.js`'s (`fh`, `h`, `d`, `a`, `ctx`), inside
markdown `chat/md.js`'s; both are styled under `.bxc-diff` and `.bxc-md`.
The rule that keeps a host's own CSS safe: the kit never styles an element
it did not write.

**Rows, not elements.** The views are template functions so a selection
spans rows and a host measures them as the scroller's children; only the
widgets with state of their own are elements, and those are light DOM —
they wear the stylesheet of the root they sit in.

## Shell-only modules

`/vendor/bx-menu.js` (the shell's context menus — action closures, not a
tile API; tiles use `xbin.dialog` / `xbin.window`), `/vendor/bx-logs.js`,
`/vendor/bx-prs.js`, and the tile window's side panels and tabs (D202:
`/vendor/code-tree.js` `<bx-code-tree>` and `<bx-code-changes>`,
`/vendor/code-view.js` `<bx-code-view>` and `<bx-code-diff>`,
`/vendor/pr-panels.js` `<bx-pr-list>` and `<bx-pr-view>`, which ask the
window for a tab with `bx-open-tab {ref}`; `/vendor/code-shared.js` their
pure rules) (the terminal pop-up's panels — reachable through
`<bx-frame>`; `<bx-logs deployment="<name>">` shows a non-primary tile
deployment's log, and nothing unless the server echoes it — on a
partitioned tile `<bx-logs partition="global|user:<id>">` asks for the
global instance's log or a person's shared one under the same echo rule
(`X-XBin-Partition`), and its corner offers what the viewer may read
([partitions.md](/docs/partitions.md) §Operating people's partitions) — and
`<bx-terminal deployment="<name>">` opens sessions targeting one — the
server's echo is what it shows), `/vendor/bx-agent.js` (the pop-up's Agent
tab, D74; its markdown is `/vendor/bx-md.js`, which is a tile's to import —
the table above), `/vendor/agent-fold.js`, `/vendor/agent-pages.js` (the session log folded,
and the pages of it the tab holds, D124/D130), `/vendor/agent-tools.js`
and `/vendor/agent-slash.js` (since D224 re-exports of the chat kit's
`/vendor/chat/tools.js` and `/vendor/chat/ask.js` — the table above — under
the names the tab always used, with the tab's old record of a call:
`newTool`, `foldTool`, `headline`, `commandOf`, `isPlanApproval`,
`planText`, `firstLine`, `describeCommand`, `filesStat`, `stripAnsi`,
`rawText`, `unifiedDiff`, `formFields`, `formContent`, `missingRequired`;
`slashQuery`, `matchCommands`, `commandHint`. New code imports the kit),
`/vendor/agent-cards.js` (since D224 the chat kit's cards under the names
the tab used — `toolCard`, `permCard`, `askCard`, `changesCard`,
`filesBlock`, `diffStat`, `cardsCss`, `mdLive`, `KIND_ICON` — each taking
the `<bx-agent>` element and one of its fold's blocks),
`/vendor/agent-css.js` (the element's own chrome; what the transcript draws
is `/vendor/chat/css.js`), `/vendor/agent-queue.js`, `/vendor/agent-testapi.js` (its test
surface) and
`/vendor/frame-titlebar.js` (the pop-up's title bar: the name, the path and
the close), `/vendor/frame-ide.js` (the pop-up's layout as data — its panes,
their tabs, the tools, the dock — and the window pref's `ide`, D202),
`/vendor/frame-ide-view.js` (the pop-up as an IDE: the tool strip, the side
panel, the panes and their tabs, the sessions' stage, the logs dock),
`/vendor/frame-status.js` (its status bar: the session in view's pickers and
the tile's controls), `/vendor/term-predict.js`
(the terminal's prediction engine, D70), `/vendor/term-src.js` (where
`<bx-terminal src>` connects and when it reconnects),
`/vendor/term-chrome.js` (the terminal's own markup and styles, and
xterm's loader), `/vendor/term-palettes.js` (the terminal settings menu's
named palettes, D184), `/vendor/term-sessions.js` (the
frame's view of the terminal session directory, D73),
`/vendor/frame-deploy.js` (the terminal window's live reload controls: the
`deploy` entry, the chip and its menu, Reload now, the launcher's banner, the
grey lines in open terminals —
[tile-deployments.md](/docs/tile-deployments.md)) with
`/vendor/deploy-state.js` (its pure view model: which chip, menu items and
tile API select entries a tile's state and a viewer's permissions yield, and
every string they show, from `GET /api/xbin/deployments`; imports only
`/vendor/deploy-branch.js`, the pure words and offers of branch-assigned
deployments),
`/vendor/bx-deploy.js` (defines `<bx-deployments component="<tile>">`, the
terminal window's Deployments panel: live reload, a tile's deployments,
promotion with the diff first, the deploy log, data, vault, deliveries,
edges and registrations; its host frame, the `frame` property, asks
every question; `target` — `primary`, a deployment's name or `off` — is the
session in view's, whose row it tags `Dev API`) with `/vendor/deploy-panel.js` (the panel's pure view
model, beside `deploy-state.js`), `/vendor/frame-panels.js` (before D202
the terminal window's body; kept for what imports it, its exports now
mapped onto the IDE), `/vendor/frame-css.js` (`<bx-frame>`'s own styles), `/vendor/org-agents.js` (`<bx-org-agents>`: the editor of an org's constraints on agents, its pin and the workspace's bounds, D229 — the admin console and the organisations tile), `/vendor/agent-prefs.js` (the person's agent preferences as the launcher and the Agent tab use them, D229: `agentPrefs()` over `GET`/`PUT /api/xbin/agent/prefs`, with this browser's older record as the migration source and the fallback), `/vendor/win-resize.js`
(a floating window's resize corner over the kit's `dragResize`, D217),
`/vendor/frame-testapi.js` (`<bx-frame>`'s
test surface for the UI harness), and the shell's own siblings under
`shell/`. They are served, and they will keep being served, but their
shapes follow the shell.

**The workspace agent's chrome** (D232;
[workspace-agent.md](/docs/workspace-agent.md) §Using it,
[protocol/delegation.md](/docs/protocol/delegation.md) §The chrome) is
shell-only in a stricter sense: it sends a person's decisions about their
agent, which xbind takes from the person's own session alone, so it works
only in a document that holds one — the shell, `/xbin/agent` — and does
nothing in a tile's frame.

| Module | What it is |
|---|---|
| `/vendor/bx-agent-dock.js` | `<bx-agent-dock>` — the side panel: xbin's header (title, **Needs you** with its count, Pause / Resume, a menu: Approvals, What my agent did, Preferences), the agent's own page in a sandboxed frame below it — or the state that explains why there is none — and every trusted overlay. Properties `open`, `full` (fill the page), `context` (`{tile}` or `{screen}`: told to the page), `page`; methods `toggle()`, `show(view)`, `review(kind, id)`; events `bx-agent-dock {open, width}` (the shell makes room; 360–720 px, remembered per person, the whole page under 640 px) and `bx-agent-open-session {tile, session}`; `bx-agent-new-session {tile, draft}` (cancelable) when the agent's page asks for a new session of a tile's own agent with the person's words — a shell that opens it (`open({session: "new", draft})`) takes the event, and where none does (an older shell, `/xbin/agent`) the dock shows a plain link to the tile and the words to copy |
| `/vendor/bx-agent-approve.js` | `<bx-agent-approve>` — the approval sheet of one request (a tile call, a tile agent's start, a tile's creation), drawn from xbind's reading of it: properties `request`, `person`; event `bx-agent-done {id, verdict}`. Allow is armed 600 ms after the sheet shows and only while nothing covers it; Enter never allows; the wider grant has a second step |
| `/vendor/bx-agent-changes.js` | `<bx-agent-changes>` — the review of an admin change set: a line an item with its before and after, warnings (a warned item starts unticked), blocked items with xbind's reason, the sign-in step, the results, Undo (which proposes a new set). Properties `set`, `person`; events `bx-agent-done`, `bx-agent-review` A **plan** (`set.plan`) is reviewed in the same dialog: lines that create a tile or start a tile agent (the coding agent, the mode and the brief "sent exactly like this"), what each line needs and what needs it (unticking a line unticks those), an `admin` line (unticked, "applied as you": the admin's own tick is the consent), `needsAdmin` lines with **Ask an admin**, and — for an admin — a request `from` another person. After an Apply in which every chosen line was applied a plan's review closes itself (`bx-agent-done {self: true}`); a failure keeps it open |
| `/vendor/bx-agent-panels.js` | `<bx-agent-needs>` (what waits), `<bx-agent-approvals>` (standing approvals, **Take back**), `<bx-agent-activity>` (what the agent did, in sentences), `<bx-agent-prefs>`, `<bx-agent-setup>` (the states shown in place of the page) |
| `/vendor/agent-chrome-store.js` | `agentStore()` — the document's one reading of the person's delegation, kept current by the `delegate` events (no polling), and the only place a decision is sent from |
| `/vendor/agent-chrome-words.js` | pure: every word the chrome shows and every choice of its design in one object (`CHOICES`: the dock's bounds, the arming delay, each grant's default duration, the shortcut), the state the dock is in (`dockState`), the chip (`chip`, `needCount`), and what a message from the agent's page is read as (`pageMessage`) |
| `/vendor/bx-agent-permits.js` | `<bx-agent-permits>` — a tile agent's permission requests answered in the dock's Needs you (the chat kit's permission card; the person's own answer through `POST /term/sessions/<id>/permissions/<pid>`): for sessions the person's workspace agent started. Property `session`; event `bx-agent-permitted` |
| `/vendor/agent-plan-words.js` | pure: the rules and words of a plan's review ([protocol/delegation.md](/docs/protocol/delegation.md) §Plans) — which lines start ticked (`planTicks`: never a warned one, an admin's line of one's own plan, or one whose needed line isn't ticked), a tick and the lines that need it (`setTick`, `neededBy`, `canTick`), what stands between a line and its tick (`lineState`: `ok` \| `blocked` \| `needs-admin` \| `decided` \| `unknown` — a kind the page doesn't know is never tickable), a line's notes (`lineNotes`), the review's words with the Ask an admin row (`planWords`), whether the review closes itself after Apply (`closesItself`, `PLAN_CLOSE_MS`), what waits for xbin (`waitingWords`), a start line as the single start's sheet draws it (`startRows`), and what was sent to an admin (`askedWords`). The app's port is held to it by the same vectors |
| `/vendor/agent-chrome-css.js` | the chrome's one stylesheet (`chromeCss`) |

A shell needs three things of these, and the scaffold's
`shell/shell-agent.js` is the pattern: a chip in its top bar from
`GET /api/xbin/delegate/state` (through `chip()`; import
`agent-chrome-words.js` with a guarded dynamic `import()` — an xbind
without it answers 404 and the shell draws nothing), a lazy
`import('/vendor/bx-agent-dock.js')` on the first open, and room for the
dock beside its canvas.

## Native preview modules

`/vendor/xb/render.js` — `<xb-view>`, the reference renderer of the native
vocabulary: it draws a native tree (or the runtime's `mount`/`patch`
messages) the way the xbin app does, light or dark, default or large text,
and reports the user's taps and typing as the app would.
`/vendor/xb/preview-host.js` puts it to work inside a tile's native runtime
document (`/c/<tile>/?native=1&preview=1`), so a browser shows the tile's
native UI; `/vendor/xb/fixture.html?tree=<url>` draws one tree. They exist
for previews and tests — a tile never imports them, and they follow the
app's look rather than a frozen API.

## Theme

The workspace draws in two themes from the Base Two system (D184): **Concrete
Night** (dark) and **Concrete Day** (light). Every colour, font, corner and
shadow is a custom property in `/vendor/theme.css`; everything else uses
`var(--bx-…)`. A person picks one in the shell's settings — *Theme*: System,
Light or Dark, and *Density*: Compact or Comfortable — and System follows
the device's light or dark setting. This section is the mechanism; how a
tile should look with it (the rules, the components, the words) is
[design.md](/docs/design.md).

### Opting in

```html
<!doctype html>
<html lang="en" data-bx-theme="auto">
<head>
  <meta charset="utf-8">
  <link rel="stylesheet" href="/vendor/theme.css">
</head>
<body class="bx">
```

- `data-bx-theme="auto"` makes the document **follow the person**: their
  choice, or the system's when they chose System. It is right in the first
  painted frame, with no script of yours: xbind's document injection adds
  `<meta name="xbin-theme">` / `<meta name="xbin-density">` when the person
  chose something ([protocol.md](/docs/protocol.md)), and the sheet reads
  them and `prefers-color-scheme`. Later changes reach a framed document as
  `xbin:appearance` from its embedder, which `xbin-client.js` applies; a
  page open in a browser tab of its own picks a change of the setting up on
  reload, and follows the system live. No other value of the attribute is
  defined.
- A document that links the sheet **without** the attribute gets Concrete
  Night — new values under the same names — so a tile written for the old
  dark palette, light text hard-coded, stays legible. It turns light only
  when it opts in. Two things there stay as they were: the old status
  names keep their old values (below), and `button.primary`, `.quiet`,
  `.danger` and the 28 px control size are left to the document (Controls,
  below).
- A document that doesn't link the sheet: the core elements in it render
  from their fallbacks, which are Night.
- `<body class="bx">` takes the base: the panel background, the text colour
  and the UI type (13/18; 14/20 comfortable).

Then look at your tile in both themes (the system setting, or the shell's
settings) and keep every colour on a token.

### Theme precedence

What a document that follows the person shows, first match wins (D188):

1. **The device** — the shell's settings, *This device*: Follow my setting,
   Light or Dark. Light or Dark holds in that browser only (its
   `localStorage`, `xbin-theme-device`), whatever the person's Theme says:
   a dark laptop beside a light office screen. Nothing is synced; Follow my
   setting clears it.
2. **The person** — *Theme*: Light or Dark, synced to all their devices
   (the shell's prefs bucket, `theme`).
3. **The system** — *Theme*: System (or no choice): the device's
   `prefers-color-scheme`.

Density is always the person's. A tile needs nothing for this: the device's
theme arrives the way the person's does — in the injected
`<meta name="xbin-theme">` of its first frame (`<bx-frame>` asks for it with
`?xbin-appearance=light|dark` on the tile's URL,
[protocol.md](/docs/protocol.md)) and as `xbin:appearance` when it changes.
The workspace root page applies it before its first paint with
`/vendor/theme-boot.js` (a root from before D188 gets it when the shell
loads); while it is set, the `xbin_theme` hint cookie holds the device's
theme, so xbind's sign-in pages follow it on that browser too.

### Tokens

The values are in `theme.css` itself (Night is its `:root`, Day the block an
opted-in document gets); the names and what they are for:

| Group | Tokens | For |
|---|---|---|
| Surfaces | `--bx-bg` (the canvas), `--bx-sidebar`, `--bx-panel` (windows, cards, bars, menus, `body.bx`), `--bx-panel-2` (inset panels, toolbars), `--bx-hover`, `--bx-selection` / `--bx-selection-text`, `--bx-border` (hairlines), `--bx-border-strong` (inputs, secondary buttons), `--bx-code-bg`, `--bx-canvas-dot` | backgrounds and edges |
| Text | `--bx-text`, `--bx-muted` (secondary, icons at rest), `--bx-subtle` (placeholders, tertiary) | |
| Accent | `--bx-accent`, `--bx-accent-hover`, `--bx-accent-ink` (text and icons on the accent), `--bx-link` | primary actions, selection, prose links: nothing else |
| Focus | `--bx-focus`, `--bx-focus-gap`, `--bx-focus-width`, `--bx-focus-offset`, `--bx-focus-outline`, `--bx-focus-halo` | the focus ring |
| Status | `--bx-ok`, `--bx-warn`, `--bx-danger`, `--bx-info`, each with a `-bg` tint | always with an icon and a word |
| Old names | `--bx-green`, `--bx-amber`, `--bx-red` | text colours that keep working. In a document that opted in they are ok, warn and danger; in one that didn't they keep their values from before Base Two (`#4CAF50`, `#F2A71B`, `#EF5350`), and `--bx-ok` / `-warn` / `-danger` follow them there (`--bx-info` follows `--bx-muted`). A fill is a `-bg` tint with the status colour or `--bx-text` on it — not an old name, whose Day value is a dark text colour |
| Partition marker | `--bx-part` | yours / shared / global ([partitions.md](/docs/partitions.md)) |
| Window chrome | `--bx-titlebar`, `--bx-titlebar-active`, `--bx-title-text`, `--bx-title-text-inactive`, `--bx-window-border`, `--bx-window-border-active`, `--bx-control-hover`, `--bx-close-hover` / `-ink` | |
| Parts and fields | `--bx-part-tab-shell` / `-terminal` / `-agent` / `-admin` (3px tabs, `--bx-part-tab-h`), `--bx-field-yellow` / `-green` / `-magenta` / `-cobalt` with `-ink`, `--bx-elevated-bg` / `-ink` | brand colour, sparingly (below) |
| Elevation, shape | `--bx-shadow` (a card), `--bx-shadow-rest` / `-active` (windows), `--bx-shadow-pop` (menus, popovers, dialogs, toasts), `--bx-scrim`, `--bx-radius` (2px) | |
| Code | `--bx-syn-keyword`, `-string`, `-number`, `-comment`, `-function`, `-type`, `-attr`, `-builtin`, `-deletion`, `-addition`; `--bx-diff-add`, `-del`, `-hunk`, `-context`, each with `-bg` | highlighted code and diffs |
| Terminal | `--bx-term-bg`, `-fg`, `-cursor`, `-cursor-ink`, `-selection`, and the 16 ANSI colours `--bx-term-black` … `--bx-term-bright-white` | xterm's theme |
| Type | `--bx-sans` (Instrument Sans), `--bx-mono` (JetBrains Mono), `--bx-display` (Bricolage Grotesque); `--bx-font` (the UI shorthand), `--bx-font-micro`, `-meta`, `-ui`, `-body`, `-title`, `-heading`, `-hero`, `-code`, `-number`; `--bx-tracking-micro` / `-heading` / `-hero` | `font: var(--bx-font-…)` |
| Density | `--bx-row` (28px; 32px comfortable), `--bx-control-h`, `--bx-pad`, `--bx-grid` (4px), `--bx-text-size` / `-line`, `--bx-term-size` / `-line`, `--bx-mono-size` (mono beside UI text: ids, paths, times; 12px, 13px comfortable), `--bx-density` | compact or comfortable |
| Layout, motion | `--bx-titlebar-h`, `--bx-topbar-h`, `--bx-statusbar-h`, `--bx-sidebar-w`, `--bx-rail-w`, `--bx-dock-w`, `--bx-elevated-h`; `--bx-ease-out`, `--bx-ease-in-out`, `--bx-dur-instant` / `-ui` / `-panel` / `-window` (0 under reduced motion) | |
| The theme in force | `--bx-scheme` (`dark` or `light`) | for code that paints |

Rules of use:

- **The accent** is for primary buttons, the selection (its background plus
  a 2px accent rule), the active tab's underline and links in prose — never
  for identifiers, paths, status, row actions or decoration.
- **Status** is an icon, a word and its colour, never the colour alone; the
  `-bg` tints are for alert and badge backgrounds. A brand field colour
  never signals status, and it belongs to the workspace's own chrome only:
  the part tabs on system windows, the elevated banner and the first-run
  screen. A tile uses none ([design.md](/docs/design.md) rule 9).
- **Corners** are `var(--bx-radius)` or 0: no pills, no circles (a status
  dot is an 8px square, an avatar a square).
- **Type** is 13px or larger (`var(--bx-font)`, `--bx-font-ui`, `-body`,
  `-title`); 11px only for the micro caps of section labels
  (`--bx-font-micro` with `--bx-tracking-micro`, uppercase), 12px only for
  timestamps and meta (`--bx-font-meta`). Mono for paths, commands,
  hostnames, ids and times; display type for headings and empty states;
  tabular figures (`font-variant-numeric: tabular-nums`) in tables and
  numbers.
- **Elevation**: windows `--bx-shadow-rest` / `-active`, anything that pops
  over `--bx-shadow-pop`, scrims `--bx-scrim`, cards a border.
- **In a shadow root** the document's rules don't reach: give it
  `button, input, select, textarea { font: inherit }`, the code font on
  `pre, code`, `::placeholder { color: var(--bx-subtle); opacity: 1 }` and
  `:focus-visible { outline: var(--bx-focus-outline); outline-offset:
  var(--bx-focus-offset); box-shadow: var(--bx-focus-halo) }`.
- **Fallbacks** are `var(--bx-x, <its Night value>)`, or none.
- **Code that paints** (a canvas, SVG built in JS, xterm) reads tokens with
  `token()` and paints again in `onAppearance()` (below).
- Nothing tuned for one theme only. `make theme-check` keeps the
  workspace's own code to these rules ([maintenance.md](/docs/maintenance.md));
  your tiles are yours, and the rules are how they match.

### Controls

In a `.bx` document (or on a `.bx` element): `button` is a secondary button
(the panel, a `--bx-border-strong` edge, 600 weight), and `input`, `select`
and `textarea` are fields with a strong edge; every interactive element
gets the focus ring on `:focus-visible`; tables get tabular figures;
`.bx-label` is the micro caps label. In a document that **opted in**,
`button.primary` is the accent fill with the accent ink, `button.quiet` text
only, `button.danger` the destructive outline (a danger fill only inside a
confirmation), a disabled button fades, and controls are 28 px (32 px
comfortable). Those wait for the opt-in because a document from before Base
Two may give the same class names a meaning — and sizes — of its own; and
they are written at the base rule's specificity (`.bx button`), so your own
later `button.primary { … }` still wins. An opted-in document also gets
`accent-color` for checkboxes, radios and ranges, and themed placeholders
and text selection.

### A palette of your own

A document may set the tokens itself — its own `:root { … }` after the
`theme.css` link wins in both themes. A tile from before Base Two did it
through the old names (`--bx-bg`, `--bx-panel`, `--bx-panel-2`,
`--bx-border`, `--bx-text`, `--bx-muted`, `--bx-accent`, `--bx-green`,
`--bx-amber`, `--bx-red`, `--bx-term-bg`, …), and in Night the roles Base
Two added on top of them are written from them, so they follow:

| Role | Written from (Night) |
|---|---|
| `--bx-hover` | the panel, 8.3% toward `--bx-muted` |
| `--bx-code-bg` | `--bx-bg`, 5% toward `--bx-text` |
| `--bx-accent-hover`, `--bx-accent-ink`, `--bx-link` | the accent 25% toward white; `--bx-bg`; the accent |
| `--bx-ok-bg`, `-warn-bg`, `-danger-bg`, `-info-bg` | 12% of the status colour over the panel |
| `--bx-ok`, `-warn`, `-danger`, `-info` (a document that didn't opt in) | `--bx-green`, `-amber`, `-red`, `--bx-muted` |
| `--bx-titlebar`, `-titlebar-active`, `--bx-title-text`, `-inactive` | the panel, `--bx-panel-2`, the text, `--bx-subtle` |
| `--bx-window-border`, `--bx-control-hover`, `--bx-close-hover`, `-ink` | `--bx-border-strong`, `--bx-border`, the danger colour, `--bx-bg` |

Each computes to Concrete Night's value exactly when nothing is set. The
rest are values of their own: set them too if your palette needs them —
`--bx-selection` with `--bx-selection-text`, `--bx-focus` with
`--bx-focus-gap`, `--bx-subtle`, `--bx-border-strong`, `--bx-part`, the
syntax (`--bx-syn-*`) and diff (`--bx-diff-*`) palettes a `<bx-code>`
draws code in, and the terminal's (`--bx-term-*`). A document that opts in
gets Concrete Day's own values for those roles in light; set them in your
`:root` rule for both themes if you keep a palette of your own there.

### `/vendor/bx-theme.js`

```js
import { token, onAppearance } from '/vendor/bx-theme.js';

const paint = () => {
  ctx.fillStyle = token('--bx-panel', canvas);
  ctx.strokeStyle = token('--bx-accent', canvas);
  // …
};
paint();
onAppearance(paint); // the person's change, the system's light/dark or contrast
```

| Export | What |
|---|---|
| `appearance(doc?)` | `{theme, density}`: `'system'`, `'light'` or `'dark'`, and `'compact'` or `'comfortable'`, as the document has them |
| `follows(doc?)` | the document opted in (`data-bx-theme="auto"`) |
| `scheme(el?)` | `'light'` or `'dark'`: what `el` renders in now |
| `token(name, el?)` | a token's current value at `el` |
| `onAppearance(cb, win?)` | `cb(appearance)` after a change of the person's choice, the system's scheme or its contrast setting; returns an unsubscribe |
| `setAppearance({theme, density}, doc?)` | change the document's appearance (the shell's settings do; a tile has no reason to) |
| `appearanceMessage()`, `applyAppearanceMessage(data)` | the `xbin:appearance` relay (`<bx-frame>` and `xbin-client.js` use them) |
| `rememberTheme(theme)`, `THEMES`, `DENSITIES`, `MESSAGE`, `EVENT`, `COOKIE` | the hint cookie (the device's theme while it overrides), and the names |
| `deviceTheme(win?)`, `setDeviceTheme(v, doc?)`, `syncDeviceTheme(doc?)` | this browser's override (D188): `'light'`, `'dark'` or `''`; set or clear it and apply; apply what is stored (the shell does, on load and on another tab's change) |
| `personTheme(doc?)`, `personAppearance(doc?)`, `setPersonAppearance(next, doc?)` | the person's own choice under an override (set aside on `<html data-bx-theme-person>` while the page shows the device's), and changing it |
| `effectiveTheme(person, device)`, `deviceUrl(url, doc?)`, `DEVICE_KEY`, `DEVICE_PARAM` | the precedence (§Theme precedence), and a tile URL that asks for the override |

### Icons

`/vendor/bx-icons.js` draws the workspace's glyphs, in place of emoji and of
symbols set in whatever font a button fell back to. An icon is 16px (or
`size="20"`), takes `currentColor` and sits on the text's baseline:

```js
import '/vendor/bx-icons.js';
html`<button title="Close" aria-label="Close"><bx-icon name="xmark"></bx-icon></button>`;
html`<span class="st"><bx-icon name="warning"></bx-icon> 2 warnings</span>`;
```

`<bx-icon name="…">` is decorative (hidden from assistive technology);
`label="…"` makes it an image with that name; an icon-only button needs its
own `aria-label` or `title`. `iconSvg(name, {size, label})` returns the same
SVG as a string, `hasIcon(name)` says whether a name exists, and `ICON_NAMES`
lists them all. A name that doesn't exist draws nothing (and says so once in
the console). `<option>` text can't hold an icon: there the word stands
alone. The names follow the native vocabulary's where the meaning is the
same:

`agent` `archive` `arrow-left` `arrow-right` `bell` `bell-slash` `bolt`
`box` `branch` `calendar` `call` `caret-down` `caret-right` `chart` `chat`
`check` `chevron-down` `chevron-left` `chevron-right` `chevron-up`
`clipboard` `clock` `code` `columns` `compact` `copy` `cpu` `database` `deploy`
`device` `diff` `doc` `download` `ellipsis` `error` `eye` `eye-slash`
`file` `filter` `folder` `globe` `grid` `grip` `home` `host` `info` `key`
`link` `list` `live` `lock` `mail` `maximize` `menu` `minimize` `minus`
`network` `ok` `org` `paperclip` `pause` `pencil` `people` `person`
`photo` `pin` `play` `plug` `plus` `popout` `power` `question` `refresh`
`restore` `save` `search` `send` `server` `settings` `shield` `signal`
`split` `tag` `terminal` `thought` `tiling` `trash` `unlock` `upload` `vm` `wait`
`warning` `window` `xmark`, and the aliases `attach` `back` `building`
`channel` `close` `collapse` `danger` `edit` `expand` `external` `forward`
`gear` `gpu` `hourglass` `image` `laptop` `logs` `more` `package` `phone`
`rack` `reload` `stop` `storage` `team` `user` `view-as` `warn` `wrench`.

A person's own emoji (a folder's icon, an agent class's) is their content,
and is shown as they wrote it.

### `/vendor/theme-boot.js`

For a page xbind serves with no document injection (its own partitions
page): a classic script in `<head>`, before the `theme.css` link, that
copies the `xbin_theme` hint cookie the shell keeps into
`<meta name="xbin-theme">`, so the page opens in the person's theme. The
workspace root page runs it too (D188): when the browser has a device
override (§Theme precedence) it replaces the injected meta with the
device's theme, keeping the person's on `<html data-bx-theme-person>`. A
tile never needs it: its documents get the injection.

## Screen modes: the size your tile is given

A person picks how each screen is arranged (the screen tab's menu or the
canvas menu: Layout; D187, D193), and the screen keeps it — a screen
shared to an org arrives in its mode for every member:

- **Canvas** — tiles anywhere on a grid of 48 px cells, each window the
  size the person dragged it to (down to about 192 × 144 px); the page
  scrolls both ways.
- **Fixed width** — the same grid cut to 48 columns that stretch with the
  browser window (rows stay 48 px): a window's **width follows the
  browser's**, its height is fixed.
- **Document** — rows of one, two or four tiles, each as tall as its
  document (§Document mode, below).
- **Tiled** — one tile fills the screen; each tile opened splits a tile's
  space in two, and the person drags the dividers or drops a tile on half
  of another (or of the screen). A window's width and height both change
  as tiles come and go. A new screen starts Tiled (D205).

A tile can't tell which mode it is in, and needn't: reflow to whatever
size the frame is, from about 192 px wide to the whole window, and scroll
inside when the content is taller than a fixed-height window (Canvas,
Fixed width, Tiled). Never scroll sideways.

## Document mode: how a tile grows

A person can switch a screen to **Document mode** (the screen tab's menu:
Layout ▸ Document; D187): the screen becomes a scrolling page of rows, one
tile per row (as wide as the page) or two or four side by side, and each
tile is **as tall as its document** — no fixed window height, no scrolling
inside the tile. The person may limit the page's width (a handle on its
right edge, D193), so a one-tile row can be anything from about 480 px to
the whole window. On the canvas the same tile is a fixed-size window, as before; a tile
can't tell which it is in, so write it to work in both.

- **How it grows.** `xbin-client.js` (which xbind injects into every tile
  document) reports the height of your `<html>` element to the frame
  (`xbin:resize`), and in Document mode the frame takes it (24–20000 px).
  Nothing to call: when your content changes height, the frame follows.
- **Report your height naturally.** Let `html` and `body` be as tall as
  their content. Don't pin them to the viewport — `height: 100%`,
  `height: 100vh` or `min-height: 100vh` on `html`/`body` (or a full-height
  flex column with an inner `overflow: auto` pane) makes your document
  exactly as tall as the frame, so it can never grow: the tile stays at the
  480 px it starts with and scrolls inside, like a window. The person can
  still give such a tile a fixed height (the handle under its card).
- **Apps that scroll inside on purpose** — a terminal, a chat with its
  composer pinned to the bottom, a map — keep the viewport pin: they are
  windows, and the handle is how a person sizes them on a page.
- **Short is fine.** A tile with little to show is a short card; don't pad
  it to a height.
- **Narrow still applies.** A row of four tiles is a quarter of the page
  each, and below 820px every row is one column: reflow, never scroll
  sideways.
- **Growing is safe while the person works.** The focused tile's title
  bar holds its place in the window (D191): a page that grows or shrinks
  under someone's hands — a list expanding, a form showing its errors —
  moves the tiles below it, not what the person is looking at; and when
  another tile grows above the focused one, the page scrolls with it. You
  don't scroll the shell yourself: nothing to call.

## Headings and text on a screen

A person can put **headings** (H1–H3, one line) and **text** (Markdown)
on a screen without making a tile for them (D192): the canvas menu or the
screen tab's menu → Add heading / Add text. They sit on the canvas's grid
like tiles (dragged by their grip, resized by their corner, pushing tiles
and being pushed), are edited in place with a double-click, and in
Document mode each is a row of its own, as wide as the page. They
are the shell's, not a tile's: no frame, no tile API, nothing a tile can
read or change. What it means for a tile: it may share the page with a
heading that already names the section, so it needn't repeat a page title
of its own in a Document-mode screen.

Text is rendered by `/vendor/bx-md.js`: raw HTML shows as text, images
as their alt text, links only to safe schemes and in a new tab with no
opener. Who may change them is who may change the screen's tiles — the
person on a personal screen; on an org screen, its `edit` level, through
the same draft and **Save and update for everyone**.

**Where they live** (for tools that read or write layouts): a screen's
`blocks` array beside its `tiles` — never inside it —
`[{id, kind: "heading"|"text", text, level?, x, y, w, h, doc?}]`: in the
`layout` pref's `screens[i].blocks` for a personal screen, and the org
screen's `blocks` ([protocol/workspace.md](/docs/protocol/workspace.md) `PUT /screens/org`)
for a shared one. `x, y, w, h` are the canvas geometry in logical px on
the 48 px grid, `doc` the Document-mode place (`{row, col, cols}`, the
same numbering as the tiles'), `level` 1–3 on a heading (default 2). A
reader that knows only `tiles` sees the screen as before; one that
rewrites a layout keeps every field it doesn't know, `blocks` included.

## Rules

- **URLs are frozen.** A module served today stays at its path; a helper
  that moves leaves a re-export behind (`clampBox` in `bx-frame.js`).
- **One home per helper.** `make js-check` refuses a second definition of
  a kit helper (`api`, `jbody`, `esc`, `deepActive`, `pathHas`, `clampBox`),
  of the code viewer's highlight helpers, of `bx-theme.js`'s and
  `bx-icons.js`'s, or of the markdown renderer's (`mdInto`, `mdCssText`,
  `closedFence`, …), anywhere in the shipped trees.
- **Theme fallbacks stay.** Core elements carry `var(--bx-x, <literal>)`
  fallbacks so a document that never linked `theme.css` still renders;
  `make theme-check` keeps every literal equal to Concrete Night's value in
  `theme.css`. Your own document decides its theme: link the sheet (and opt
  in to follow the person), or set the tokens yourself.

## lit pitfalls met in this codebase

- A plain field (not in `static properties`) never re-renders on its own —
  call `this.requestUpdate()` after changing one that the template reads.
- `?selected` on `<option>` never *resets* a `<select>` back to a value the
  DOM already moved away from; set `.value` on the select after render.
- Re-creating an `<iframe>` (a changed `sandbox` attribute applies only to
  the next navigation) needs lit's `keyed()` — see `bx-frame`.
- `e.target` is retargeted at shadow boundaries: an `<input>` inside a
  nested element never matches `e.target.closest('input')`; use
  `pathHas(e, 'input')` from the kit.
- `<select>` dropdowns never render in headless screenshots; the harness
  dumps their options to text instead.
