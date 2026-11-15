# 2026-11-15 — The agent template's chat is drawn by the chat kit: what a patched instance has to rename

## What changed

The agent template (`builtin-templates/agent`) no longer draws its
transcript with templates of its own. Every block — a tool call, a request,
a message, a thought, a notice, a journal line, a subagent's card — is drawn
by xbind's chat kit (`/vendor/chat/…`, [frontend-kit.md](/docs/frontend-kit.md)
§The chat kit) through the template's `chat-kit.js`. A row of the timeline is
now the kit's element (`<div class="bxc-row" data-k="…">`; a block inside a
card is `<div class="bxc-in" data-k="…">`) with the kit's markup inside it,
styled by the kit's stylesheet (`bxc-…` classes).

The **row** keeps the classes and data attributes the template's cards
always had, so a selector or a rule that stops at the row still matches.
**Inside** a row the old class names are gone.

Kept, on the row:

| Hook | Still there |
|---|---|
| a tool call | `.tcard`, `.hcard` (a coding agent's), its state class (`running`, `done`, `error`, `approval`, `stopped`, `writing`), `.on` while open, `data-k`, `data-fam`, `data-tool`, `data-status` |
| a message | `.msg.user` (`.other` for another person's), `.msg.assistant` (`.live` while it is written) |
| a thought, a notice, a journal line | `.think` (`.live`, `.on`), `.notice` (`.on`), `.step.<kind>` (`.md-step` for a finish with a result) |
| a subagent's card | `.acard` with its state class and `.on`, `data-child` |
| what a run waits on a person for | `.ask`; `.hask`, `.hplan`, `.hq` for a coding agent's; `.approve`, `.grant` for the engine's own; `data-park` |
| a card a feature module draws itself (`ext.block`) | unchanged: a coding agent's child card (`.acard.hkid` and everything in it), a document's line (`.step.doc`) |

Renamed or gone, inside a row:

| Old | Now |
|---|---|
| `.tch` (a card's line) | `summary` of the row's `details.bxc-tool` |
| `.hl`, `.sub` | `.bxc-title`, `.bxc-sub` |
| `.oc` (`.ok`, `.bad`, `.run`), `.oc.steps` | `.bxc-oc` (`.bxc-ok`, `.bxc-bad`, `.bxc-run`), `.bxc-steps` |
| `.st` (`.warn`, `.bad`, `.run`) | `.bxc-st` (`.bxc-warn`, `.bxc-bad`, `.bxc-run`) |
| `.spin` in a card | `.bxc-spin` |
| `.tcb`, `.hcb` (a card's body) | `.bxc-body` |
| `.tname` | `.bxc-meta` (the tool's name only: no `acp:<kind>` prefix — read `data-tool` on the row) |
| `.args`, `.arg`, `.arg .k`, `.arg .v` | `.bxc-args`, `.bxc-arg`, `.bxc-ak`, `.bxc-av` |
| `.res pre` | `pre.bxc-res` |
| `.hcmd pre` | `pre.bxc-cmd` (the command without the leading `$ `); its copy is `button.bxc-copy` |
| `.hout pre` | `pre.bxc-out`; the part before the tail is in `details.bxc-outx` |
| `.hexit` (`.ok`, `.bad`) | `.bxc-exit` (`.bxc-ok`, `.bxc-bad`) |
| `.hfiles`, `.hfile`, `.hfh`, `.hpath`, `.hadd`, `.hdel`, `.hst` (an edit's row per file) | gone: an open edit card draws each patch directly as `pre.bxc-diff`; the totals are the line's `.bxc-oc` |
| `.hdiff` (`.hl`, `.plain`) | `pre.bxc-diff` (`.hljs` when highlighted) |
| `.hnopatch` | `.bxc-nopatch` |
| `.hplaces` | `.bxc-places` |
| `.hpre` (raw input behind a link) | `details.bxc-raw` |
| `.hnote` | `.bxc-task` |
| `.hkids` (a Task's steps) | `.bxc-children` |
| `.hin` (the ↳ of an orphan) | `.bxc-under` |
| `data-kind` on a coding agent's card | gone (the kit's `data-kind` is the block's kind): use `data-tool="acp:<kind>"` |
| `.txt`, `.who` | `.bxc-text`, `.bxc-who` |
| `.afiles`, `.afile`, `.afile.gone`, `data-afile` | `.bxc-att`, `.bxc-att-file`, `.bxc-gone`, `data-path` |
| `.msg.assistant .md` | `.msg.assistant .bxc-md` (the template's `.md` rules still style its other markdown: the wiki, a task pin) |
| `.acts .act` | `.bxc-acts .bxc-act` (`.done` is still set on it after a copy) |
| `.cur` | `.bxc-cursor` (on the draft's markdown element) |
| `.think .th`, `.think .tb` | `.think summary`, `.think .bxc-md` |
| `.notice .nh`, `.notice .nb` | `.notice summary`, `.notice .bxc-nbody` |
| `.step .g`, `.step .steplnk`, `.step.finish .md` | `.step .bxc-ng`, `.step button`, `.step.finish .bxc-md` |
| `.ach`, `.acb`, `.rid`, `.task`, `.answer`, `.readfail` of a built-in subagent's card | `summary`, `.bxc-body` / `.bxc-children`, `.bxc-ref`, `.bxc-task`, `.bxc-answer`, `.bxc-err` (its Retry keeps `data-act="retry"`; its open link is `.bxc-open`) |
| `.hlead`, `.htitle`, `.hlabel` | `.bxc-q` (its `b` is the lead), `.bxc-ptitle`, `.bxc-desc` |
| `.hopts`, `.hopts .btn`, `data-opt` | `.bxc-btns`, `button`, `data-option` (`.bxc-primary` the first allow, `.bxc-deny` a rejection) |
| `.btn.hwarn` | `button.bxc-explicit` |
| `.hfb` | `input.bxc-word` (a word with a rejection), `.bxc-reject textarea` (a plan: what should change) |
| `.hrule`, `.hplanmd`, `.hmsg` | `.bxc-rulenote`, `.bxc-plan-md`, `.bxc-qmsg` |
| `.hfield`, `.hfl`, `.hopt`, `.hother`, `.hurl` | `.bxc-field[data-field]`, `.bxc-fh`, `label.bxc-opt`, `.bxc-other`, `.bxc-field a` |
| `.hprev`, `.hdh` (a request's diff preview) | gone: the diff is `pre.bxc-diff` in `.bxc-pbody` |

Words that changed: a stopped call's chip says `cancelled` (was `stopped`);
the engine's question leads with "The agent asks" (was "The agent is
asking:"); its approval with "The agent asks to run a tool" and the calls
as a list; a url question's second button is Skip (was Cancel).

Exports and files:

- `chat-cards.js` exports `blocksTpl`, `sessionTpl`, `queueTpl` and — still —
  `approvalTpl(calls, decide, lead, grant)`, now drawn by the kit; its
  templates for single kinds (never exported) are gone.
- `harness-ask.js` exports `permissionTpl(run, _, who, blocks)` and
  `questionTpl(run, _, who)`: the second argument (the card's model) is
  ignored — the kit reads the run's park itself.
- `harness-cards.js` registers only `top` (the usage badge, the counts, the
  plan pin); a coding agent's calls are no longer an `ext.block` hook.
- `chat-window.js` keeps `ChatWindow` and its methods; it no longer renders
  every block on an xbind without `/vendor/scroll-window.js` (the page asks
  for kit revision 4 before anything loads).
- `test/backend.mjs` `serveTile(ctx, {noWindow})`: the option is ignored.
- New: `chat-kit.js` (`kitRow`, `kitPark`, `kitCtx`, `approvalTpl`),
  `model/fold.js` `blockOf` / `blocksOf`, `model/harness-ask.js` `parkBlock`.
- `boot.js` `NEEDS.kit` is 4: on an older xbind the page says "This agent
  needs xbind ≥ v0.3.80".

The composer (`#msg`, `#send`, `#stop`, the pickers, `#hctl`, the
attachments) is unchanged.

## Who's affected

An **instance of the agent template** (a copy in a workspace) that carries
patches of its own — and only once it merges the template at this version.
Nothing changes for an instance that does not merge: the v0.3.79 template
runs unchanged on the new xbind.

After the merge:

- a stylesheet rule or a script of yours that selects **inside** a card by
  one of the old names matches nothing — silently;
- a patch to the bodies of `chat-cards.js` (`toolTpl`, `userTpl`,
  `agentTpl`, `stepTpl`, …), `harness-cards.js` (`cardTpl` and below) or
  `harness-ask.js` (`permissionTpl`, `questionTpl`, their styles) conflicts:
  that code is gone;
- a browser test of yours written against the old markup fails on the
  selectors above.

Tiles that are not this template, and the Agent tab, are not affected by
the renames (the Agent tab's look did change: see the changelog entry).

## How to migrate

1. **Merge, and resolve conflicts in the three files by taking the new
   version.** Then re-apply what your patch did, in the place it now
   belongs (2–4).
2. **A style of your own**: keep rules that select the row (`.tcard`,
   `.msg.user`, `.ask.hask`, …) — they still match, on the kit's row — and
   rename what reaches inside with the table above, e.g.

   ```css
   /* was: .tcard[data-tool="deploy"] .hl { font-weight: 600 } */
   .tcard[data-tool="deploy"] .bxc-title { font-weight: 600; }
   ```

   The kit never styles an element it did not write, and its rules are all
   under `bxc-` classes, so a rule of yours in `index.html` (or a `<style>`
   your module adds) wins by ordinary specificity. `chat-kit.js`'s `SKIN`
   is where the template itself tunes the kit for this page.
3. **A hook of your own on a row** (a class or a `data-*` your tests or
   styles look for): add it in `chat-kit.js` `hooksOf(b, open)`, which is
   what the page hands the kit as `ChatCtx.rowAttrs` — `class` and `data-*`
   only; `data-k` and `data-kind` are the kit's.
4. **A block drawn your own way**: that was always `ext.block` (`web-ext.js`)
   and still is — it is asked first, for a row and for a block inside a
   card, and what it returns is drawn untouched. A tool of your own that
   should look like every other call needs nothing: it gets the kit's card
   with the fold's headline; to give it a glyph, a subline or an outcome,
   set them where the block is converted (`model/fold.js` `convert`: the
   block's own `icon`, `sub`, `outcome`, `args`, `result`).
5. **Tests**: rename selectors with the table; a card's line is clicked
   with `[data-k="…"] > details > summary`, and `.on` on the row still says
   it is open.
6. Update xbind first: the merged page needs v0.3.80 or later, and says so
   on an older one.

## Why

The Agent tab and the agent template each had a renderer for the same
conversation — two tool cards, two permission cards, two windows — and a
third host (the workspace agent) is coming. One renderer (D224) means a fix
or a look lands once, and a conversation reads the same whatever produced
it. The row-level hooks were kept precisely so that an instance's patches
and tests have something stable to hold on to; the inside of a card is the
kit's so that it can keep changing in one place.
