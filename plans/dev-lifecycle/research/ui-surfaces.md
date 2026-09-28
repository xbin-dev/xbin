# UI surfaces: terminal window, shell, shipped tiles, native hooks

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: workflow dev-lifecycle-explore (wf_b2976d01-667), researcher `ui`.

## Report

## UI surfaces for tile dev lifecycle / deployments: research report

Scope: `web/` (served from the binary), `workspace-template/shell` and the scaffold tiles (owned by the workspace), native iOS hooks, size budgets, tests. Every fact cites file:line. Design reasoning is marked *(inference)*.

### 0. Two facts behind every placement decision

1. **Binary-served surfaces vs workspace-owned surfaces.**
   - `web/` is embedded (`assets.go:11-15`) and served at `/vendor/<name>` with `Cache-Control: no-cache` ("vendor changes on upgrade", `internal/server/static.go:585-606`).
   - `workspace-template/` is copied once by `xbind init`, and upgrades never rewrite it (`docs/compat.md` rules 2 and 4; `docs/maintenance.md` "Editing the scaffold").
   - So these reach every workspace on upgrade: bx-frame, frame-titlebar, frame-launcher, bx-code, bx-logs, bx-prs, bx-terminal, bx-dialog, bx-menu, events-socket, xbin-client.
   - These change only through `bx builtin update` or the manager's Updates tab (`tiles/manager/index.html:173-185`): bx-shell, bx-canvas, bx-side, menus.js, bx-tile-admin, and the admin, manager and organisations tiles.
   - bx-terminal states the rule itself: "the terminal ships with xbind but the shell that zooms it is workspace-owned, so relying on the shell to announce its zoom would leave the fix half-applied on an un-updated workspace" (`web/bx-terminal.js:258-263`).
   - *(inference)* The core pause/deploy UI belongs in the terminal window, which is binary-served. Scaffold surfaces (a card chip, a sidebar badge, a menu square, an admin section) are optional extras. They must cope with an old shell and with a server field being absent.
2. **Size budgets are nearly exhausted exactly where the obvious edits would go.**
   - bx-frame.js is at 866 of the 900-line cap for unlisted files; bx-shell.js 1879/1892; admin.js 314/318; shots.js 870/877 (§5).
   - The established pattern is to extract. frame-titlebar.js and frame-launcher.js both say they were "extracted from bx-frame … the frame is at its size budget" (`web/frame-titlebar.js:2-4`, `web/frame-launcher.js:2-4`).
   - Other examples of the same pattern: the shell children bx-canvas and bx-side, the admin console's `tabs/*.js`, and the harness's `passes/*.js`.

### 1. The tile's terminal window

#### 1.1 Structure (`web/bx-frame.js`)

**State** (`static properties`, L86-110):
- `_termOpen`, `_sessions` (the tabs), `_active`.
- `_layout`, typed `'term' | 'code' | 'split' | 'logs' | 'prs'` (L95), and `_codeW`.
- `_frame` (iframe facts), `_prCount` (L98).
- `_narrow` and `_tools` (the degraded bar, L100-101).
- `_dialog` (L102), `_providers`, `_history`, `_envOld` (L105), `_menu` (L106).
- Plain fields set by frame-launcher: `_vmStatus`, `_vmPref`.

**Render** (L814-863):
- `.frame-wrap` holds:
  - the iframe (`_frame.url`, keyed by `_frameKey`);
  - the build-error overlay `<pre class="overlay">` (L825-826);
  - the 7×7 `.edit` button, unless the frame has `no-edit` (L827-828).
- When the window is open, `.pop` contains:
  - `titlebar(this)` (L834), plus `toolsRow` when `_narrow && _tools` (L835);
  - `<bx-dialog>` (L836) and `<bx-menu>` (L837-838);
  - `.panels`: `<bx-code>` for code/split (L840-841), the split divider (L842), `<bx-logs component>` (L843), `<bx-prs component>` (L844), and `.term-host` holding the launcher or one `<bx-terminal>`/`<bx-agent>` per tab (L845-858).
- The term host stays mounted and is hidden outside the term and split layouts (L671, L845).

**Public API the shell uses:**
- `toggleTerminal()` (L531).
- `open(layout)` (L533-541). Its comment: "'term' | 'code' | 'split' | 'logs' | 'prs' … The shell's tile menu uses it for terminal / logs / source / proposals".
- `fitToViewport()` (L546-558), `popBox()` and the `bx-pop` event (L291-292), `reloading`/`hovered` (L428-431), `popBounds` (L107-109).
- bx-frame is on the "modules tiles may import" list (`docs/frontend-kit.md`), so its attributes, events and methods are a compatibility surface.

**Layout and window persistence:**
- `_setLayout` widens the pop to 960 px for code/split/prs (L792-800).
- The window's state `{open, active, activeId, pop, layout, codeW}` is saved per user as the pref `term:<tile>` (L305-312; `web/term-sessions.js:16, 37-38`).
- It is restored in `_restoreTerm` (L317-339; `w.layout` at L328).

**testApi** (L563-601):
- iframe; hovered / `setHover`; reloading / `beginReload` / `notifyLoad`.
- terminalOpen / `closeTerminal`; `open`; pop / `setPop` / `popElement`.
- tabs, history, activeTab / `setActiveTab`, layout, narrow, `setTools`.
- `newTerm`, `newAgent`, `startKind`, `launcherItems`, `closeTab`.
- dialog / `answerDialog`; `agent(i)`.

#### 1.2 The title bar (`web/frame-titlebar.js`)

**Left to right** (L26-49):
1. `.path`, the tile path. It shrinks first (min 48px, L227-232).
2. The `.tabs` strip. It scrolls, and a mouse wheel scrolls it sideways (L51-57).
3. `+` (`.mknew`), which opens the launcher menu.
4. On the full bar, `layoutGroup` + spacer + `settings`. On the degraded bar, a `⋯` (`.more`) button that toggles the tools row instead.
5. `✕` (`.winx`), titled "close (session keeps running)".

**The groups:**
- **`layoutGroup`** (L121-136): the buttons `>_ { } ⇋ ▤ ⇄N`. The PR count is inline text, `⇄ ${f._prCount}` (L132-134). `.on` marks the active layout.
- **`settings`** (L106-109): an ended or history agent tab gets only `layerButtons`. Every other tab gets `pickers`, which ends with `layerButtons`. With no tabs, `cur` is undefined and the pickers render with default values.
- **`pickers`** (L145-178):
  - the network select `select.scope.net`, whose scopes come from the session frame or default to internet / host / offline; host is filtered out inside a VM;
  - the tile-API select (🔌 / ⛔);
  - `vmToggle` (L186-201): a `⧉ VM` button with `.on` in accent colour, disabled with the tooltip "VM sandbox unavailable: <reason>";
  - a GPU select, shown when GPUs exist and the tab is not in a VM.
  - Every tooltip names the consequence, e.g. "switching restarts the terminal" (L154).
- **`layerButtons`** (L205-213): the only tile-scoped group on the bar.
  - `⬆ base update`: amber `.upgrade`, shown only when the base image is outdated; it is the one button allowed to shrink (L259-265).
  - `⟲`: reset the sandbox.

**Degrade, never clip (D107, `plans/DECISIONS.md:3217-3235`):**
- `fitBar` (L76-96) degrades the bar under 640 px or on the phone sheet. Otherwise it measures overflow, remembers `_barNeed`, and restores the full bar once the window is that wide again.
- It keeps the active tab in view, but only when something that could move the tab changed (L86-94).
- The frame calls it after every render and from a ResizeObserver (`bx-frame.js:237-243, 262-271`).
- `barKey` (L61-62) lists the state the bar's width depends on: the active tab, GPU count, `_vmStatus`, `_envOld`, `_prCount`, and each tab's kind/name/provider/ended/history/vm/net/baseOutdated. A change resets `_barNeed` (`bx-frame.js:239-240`).
- `toolsRow` (L100-102) re-renders `layoutGroup` and `settings` under the bar.
- Selects built from data mark their option with `.selected=${live(...)}` (L140-144).

**Dragging:** `dragWindow` ignores pointerdowns on `button, select, .tab` (`web/bx-kit.js:145-146`). Pressing any other control on the bar starts a window drag.

#### 1.3 How the pickers and layer actions show state and confirm

**Showing state:**
- A select's value, or a `.on` class.
- Offers appear only when relevant (base update).
- An unavailable action is a disabled control whose tooltip says why (the VM toggle).

**Confirming:**
- The frame uses its own themed dialog, one at a time (`bx-frame.js:714-725`): `_ask(spec)` stores `_dialog`, which renders `<bx-dialog>` inside the pop. `_confirm(title, message, okLabel)` offers Cancel and a primary `okLabel`; `_prompt` asks for text.
- The code comment reads "Themed, harness-drivable dialogs … in place of the native confirm()/prompt()".

**Pickers** — `_respawn` (L753-765):
- Asks only when the session is live: "Restart this terminal <what>?" / "Its shell and anything running in it end, and the scrollback is lost." / "Restart".
- If declined it resolves false, and the change handler puts the select back (`frame-titlebar.js:159, 164, 172`).

**Agent tabs** — `restartAgent` (`frame-launcher.js:95-124`):
- Confirms, then marks the tab `restarting`.
- POSTs `/api/xbin/term/sessions/<id>/restart`.
- On failure shows `_confirm('The agent could not restart', …)`.

**Layer reset** — `_resetEnv` (L774-786):
- Confirm text: "Rebuild <tile>'s terminals on the newer base image?" or "Reset the sandbox for <tile>?". The message names who is affected: "Every terminal and agent on this tile restarts." The button says "Rebuild" or "Reset".
- Then `DELETE /ws/term/env?cwd=`, which is followed by `restartFresh()` on every shell tab (`bx-terminal.js:486-497`).

*(inference)* "Reload now", deploy and promote are tile-wide actions that affect other users, like `_resetEnv`. They should use the same `_confirm` shape: a question naming the effect, a message naming who is affected, and a verb on the OK button.

#### 1.4 The launcher (`web/frame-launcher.js`)

What an empty window shows (L152-184):
- "Start a session in <tile>".
- The amber `.lbase` banner with `⬆ base update` (L160-164).
- The `.lvm` VM switch (`role=switch`), only where VMs can run (L188-197).
- Session cards and recent sessions.

The `+` menu is `launcherItems` rendered in a `<bx-menu>`, as a sheet on phones (L129-147; `bx-frame.js:655-658`).

`loadTileState` (L35-40) loads the tile's state: agent history, `GET /ws/term/env` (giving `_vmStatus` and `_envOld`), and the VM pref. It runs when the window opens (`bx-frame.js:250-255`) and on every relist after a `term` event (`bx-frame.js:348`).

#### 1.5 The panels

- **bx-logs:**
  - Only a `component` attribute (`observedAttributes` L61); a change clears and re-streams (L92-97).
  - Streams `GET /api/xbin/logs?component=<p>&follow=1` (L141) and reconnects (L158-166).
  - Access: admin, the tile itself, or a terminal-level user (`docs/overview/09-terminals.md:541-547`). It reads `.xbin/log/<compkey>.log`, across all generations.
- **bx-code:**
  - Tabs Files / Changes / Analysis.
  - Reads `code/tree`, `code/file`, `git/log`, `git/diff`, `git/activity` by component (L237-264).
  - Refreshes on `reload` or `build-ok`, debounced 250 ms (L206-212, 267-283).
  - Shows no branch; `git/log` returns commits and the remote, no branch (`internal/broker/code.go:244-270`).
- **bx-prs:**
  - Proposals per target tile (L127-177), refreshed on `pr` events (L103-108).
  - A proposal is applied in the tile's terminal with `bx code pr fetch N | git am --3way` (L223-229).
  - "Mark merged" / "Reject" uses the native `prompt()` (L171).
- **bx-terminal:**
  - A change to net/gpu/api/vm restarts the session (L472-484).
  - Session frame handling is at L619-647.
  - Grey notice lines are written as `\x1b[90m[…]` (netNote L632-635; restart messages L477-481).

#### 1.6 What the frame already knows about its tile

| Source | When | What | Ref |
|---|---|---|---|
| `GET /api/xbin/components`, cached once per page | first mount | chrome / sandbox / origin | `frame-info.js:44-51` |
| `GET /api/xbin/components/<path>` | `grants` event | refresh one entry | `frame-info.js:65-69`; `bx-frame.js:216-226` |
| `GET /api/xbin/frame-token?component=` | each credentialless load | bootstrap token | `frame-info.js:75-95` |
| `GET /api/xbin/term/sessions?cwd=` + pref `term:<tile>` | mount, `term` events, tab becomes visible | tabs, window | `bx-frame.js:317-350` |
| `GET /api/xbin/gpus` (admin-only; `[]` otherwise) | open | GPU picker | `bx-frame.js:76-83` |
| `/agent/providers`, `/agent/history` | open, relist | launcher | `frame-launcher.js:23-28` |
| `GET /ws/term/env?cwd=` → `{exists, baseOutdated, vm}`; pref `termvm:<tile>` | open, relist | base update, VM | `frame-launcher.js:34-40`; `server.go:326-332` |
| `GET /api/xbin/code/prs?target=&state=open` | open, `pr` events | ⇄N | `bx-frame.js:398-403` |
| /ws/term session frame `{op:"session", id, net, baseOutdated, label, scopes, netNote, vm, echoAck}` | every attach | per-tab pickers | `internal/term/attach.go:190-194`; `bx-terminal.js:621-647`; `bx-frame.js:730-746` |
| /ws/events | always | reload / build-error / build-ok / grants / pr / term | `bx-frame.js:366-396` |

**Not fetched:**
- `GET /api/xbin/tile-status` (runtime metrics, "self or admin", `internal/boot/api.go:87-117`). Only `bx status` (`cmd/bx/main.go:371-420`) and harness passes read it.
- `/backends` and `/runtime` (admin-only).

**Gaps that matter here:**
- The frame knows nothing about the backend: `build-start` is ignored, so it can never show "building…".
- It does not know the viewer's access level on the tile. A 403 from `/ws/term/env` silently becomes `{}` (`frame-launcher.js:34`).

#### 1.7 (a) A hot-reload pause toggle and "reload now" *(inference)*

- **Title bar:** put them beside `layerButtons`, the tile-scoped group. Render them in both branches of `settings()`, so ended/history tabs and the tools row keep them. Two controls:
  - a toggle that shows `.on` while paused;
  - a "reload now" button showing the number of pending changes, enabled only while paused and something is pending.
- **Bar mechanics:** add the paused flag and the pending count to `barKey`, and make both controls `button` elements.
- **Launcher:** an `.lbase`-style banner, e.g. "live reload paused · N changes · Reload now", so the state is visible before any session exists.
- **Visibility:** pausing affects everyone who views the tile, but only people who open the terminal window see that UI.
  - The only binary-served ambient surface is bx-frame's `.frame-wrap` (L820-829), where the build overlay lives.
  - A small chip drawn there would reach old shells. Card and sidebar badges need a scaffold update.
- **State:** could come from an additive field on `/ws/term/env`, which is already gated at terminal level (`server.go:339-351`), or from a new endpoint, plus a new event type for live updates.
- **Name collision:** ⏸ is already the "Disable" icon (`menus.js:114`), and "paused" is used to mean disabled (`tabs/runtime.js:667-669`).

#### 1.8 (b) A deployments panel *(inference)*

- **Where:** a sixth `.lyt` button that opens a lazily mounted panel element in `.panels`, next to bx-logs and bx-prs (`bx-frame.js:843-844`).
  - The panel lives in a new `/vendor` module, because bx-frame has 34 lines left.
  - The new layout id has to be added to `_setLayout`'s widen list, to the documented set in `open(layout)`, and to the saved pref.
  - A badge on the button (like `⇄N`) must be part of `barKey`.
- **Layout:** follow bx-prs's side list and main pane (`bx-prs.js:41-95`).
- **Each deployment row:**
  - status, reusing the admin states healthy / building / failed / idle (`tiles/admin/admin-css.js:294-298`);
  - a primary badge;
  - which deployment hot reload is attached to;
  - the branch, in the git-branch variant.
- **Actions** go through `f._confirm`.
- **Per-deployment logs:** embed `<bx-logs>` with a new `deployment` attribute (`observedAttributes` must grow) and add an additive `&deployment=` parameter to `/api/xbin/logs`.

### 2. How browsers learn about builds and reloads

#### 2.1 Who emits events

- **File watcher** — `watchLoop` (`internal/boot/serve.go:161-181`):
  - publishes `{type:"reload", component:c.Path}` for every changed component (the component path, not the file path);
  - then calls `run.Changed(c)` for components whose backend must restart. `changedComponents` (L192-206) decides this; an edit that touches only the native UI reloads without a restart.
- **Runner:**
  - `build-start` (`runner.go:285`; `env.go:72`, which adds the text "setting up environment…");
  - `build-error` with the compiler text (`runner.go:289/309/315/345`);
  - `build-ok` (`runner.go:352`).
- **Lifecycle changes** publish `reload` (`internal/broker/lifecycle.go:114`).
- **Tile status:** `status` (`internal/obs/status.go:118-124`). A component's status is cleared on every `build-start` for that component (L129-142).
- **Event shape:** `{type, component, text, topic, data}` (`internal/events/events.go:11-17`); the documented list is `docs/protocol.md:2179-2230`.
- **Delivery:** every subscriber, tiles included, receives all non-bus events. The exceptions are `pr` (filtered to readers of the tile) and `term`/`session` (per user) (`internal/server/server.go:592-610`).

#### 2.2 Who consumes them

| Type | Consumer | Reaction | Ref |
|---|---|---|---|
| reload | bx-frame | `_reload()` if `isReloadTarget(this, e.component)` | `bx-frame.js:371-373` |
| build-error / build-ok | bx-frame | set / clear the overlay if `e.component === this.src` | `bx-frame.js:374-379` |
| build-start | nothing in `web/` | (server side, `obs/status.go` clears the status) | |
| grants | bx-frame | `_regrant`: re-key the iframe if its sandbox tokens changed, else reload | `bx-frame.js:380-385` |
| pr / term | bx-frame | PR count while open / re-list unless op is `status` | `bx-frame.js:386-393` |
| reload, build-ok | bx-code | debounced refetch | `bx-code.js:267-283` |
| reload, grants | bx-grants, bx-bindings | refetch the approval panels | `bx-grants.js:81`; `bx-bindings.js:88` |
| reload / grants / users / branding / status / pr | shell (via `xbin.events`) | refetch `/components`; dots and toasts; PR summary | `bx-shell.js:194-201` |
| grants / reload / build-ok / users | admin tile | `_refreshSoon()` | `tiles/admin/admin.js:155-157` |
| reload, build-ok | llm-gw (builtin tile) | `_refresh()` | `builtin-tiles/llm-gw/llm-gw.js:100-102` |
| users, grants | organisations tile | reload | `organisations.js:81-86` |

#### 2.3 Reload and overlay mechanics in bx-frame

**`_reload()`** (L405-417):
- Clears the build error and calls `_beginReload()`.
- A credentialless frame gets `_prepareFrame()`, which mints a new token and so a new URL.
- A sandboxed frame gets `src = /c/<src>/` again.
- Anything else gets `location.reload()`.

**Focus and z-order are protected** (L419-461):
- `reloading` stays true until the load event plus 400 ms, capped at 15 s.
- Focus is handed back after 350 ms unless the pointer is over the tile.
- The shell skips fronting a frame that is reloading and not hovered (`bx-canvas.js:369-379`).

**Overlay:** `build failed — <src>` (L825-826; CSS L133-141).

**Timing:** the frame reloads while the backend is still rebuilding. The blue/green swap keeps the old generation serving (`docs/protocol.md` "Backend contract").

#### 2.4 `events-socket.js`

- One socket per page; reconnects starting at 500 ms, doubling up to 15 s (L14-24).
- `onEvent(cb)` returns an unsubscribe function (L26-30). `mountedFrames` is the registry (L33).
- `isReloadTarget` (L35-49): a frame covers a component if the path matches exactly or starts with `src + '/'`. The longest `src` wins, and "ties [are] broken arbitrarily but deterministically by first registration".
- It is keyed by `src` only.

#### 2.5 `xbin-client.js` inside the tile

- There is no reload logic in the tile; the parent bx-frame reloads it.
- It exposes the raw stream `xbin.events.on(cb)` on the tile's own socket `/ws/events?frame=<token>` (L142-159, 180-182), which reconnects only while handlers exist, and `xbin.bus.on` (L161-178).
- `docs/sdk.md:250-251` documents `xbin.events.on` as the stream of "reload / build-start / build-error / build-ok / bus / grants". That makes it a public SDK surface, covered by compat rule 8.
- `xbin.self` comes from the injected `xbin-component` meta (`xbin-client.js:50`; injection at `internal/server/static.go:431-470`).

#### 2.6 Which events need a deployment dimension *(inference)*

- **Yes:** `reload`, `build-start`, `build-error`, `build-ok`. Every consumer keys them by component alone.
- **`status`: yes if a non-primary backend can report status.** There is one status slot per component (`obs/status.go:104-116`; `bx-shell.js:1257-1266`).
- **No:** `grants`, `pr`, `users`, `branding`, `native`. `term` and `session` only if sessions become tied to a deployment.
- **New:** a pause-state change event (paused, pending count, who paused).

#### 2.7 How old clients react

"Old clients" here means:
- browser tabs already open during an upgrade (the page picks up new `/vendor` code on reload);
- code owned by the workspace: the shell, the admin and organisations tiles, llm-gw copies;
- third-party tiles that use `xbin.events`;
- the iOS app, which lags months behind (compat rule 10).

**A new field on an existing event type is ignored**, so:
- a non-primary `reload` carrying the primary's component reloads the primary frame and the app's open view (`WorkspaceEvents.swift:243-245`), and makes the shell, admin tile, llm-gw and bx-grants refetch;
- a non-primary `build-error` paints its overlay on the primary frame, and a non-primary `build-ok` clears a real one;
- a non-primary `build-start` wipes the primary tile's reported status on the server (`obs/status.go:129-142`).

**A new event type is ignored everywhere:**
- bx-frame's switch has no default branch;
- the shell and the admin tile use if-chains on `type`;
- bx-code returns early;
- the iOS app maps unknown types to `.other` (`Events.swift:44-45, 80-81`);
- `docs/compat.md`: "Receivers ignore fields and messages they do not know".

**A component string without a `/` boundary** (e.g. `apps/x@dev`) is ignored by bx-frame's ownership check (L368), by `isReloadTarget`, and by iOS `covers` (`Events.swift:161-165`). The shell still refetches `/components` on any `reload` (`bx-shell.js:195`).

**Pausing is naturally safe for old clients** if paused edits simply publish no `reload`, and "reload now" publishes an ordinary one *(inference)*.

### 3. The shell (all workspace-owned)

#### 3.1 `bx-shell.js`

- **Components:** refetches `/api/xbin/components` on mount and on every `reload`, `grants` or `users` event (L194-197, 564-572). Removes offloaded tiles from screens (L586-596); counts hidden tiles (L582-584).
- **Status:**
  - `GET /api/xbin/tile-report` plus `status` events; a transient status becomes a toast (L1251-1266).
  - The browser title gets 🔴 or 🟡 (L1278-1283).
  - Toasts are `_pushToast(comp, {level, message, action})` (L1285-1290, rendered at L1739-1745).
- **PRs:** `GET /api/xbin/code/prs/summary` gives `{path: n}` (L1271-1276).
- **Pending approvals:**
  - `_loadPendingCount` sums actionable items from `/grants`, `/bindings` and `/access-requests` (L1382-1401).
  - The count appears on the sidebar button "⚑ organisations N" (`bx-side.js:443-447`).
  - The `<bx-grants>` and `<bx-bindings>` panels sit above the canvas (L1839) and "render nothing when there is nothing to decide" (`bx-grants.js:1-5`).
- **Menus:**
  - Built from `_menuState` and `_menuActions` (L890-911). Note `confirm: (m) => confirm(m)` is the native browser confirm (L907).
  - Lifecycle changes POST `/api/xbin/lifecycle` and report errors as toasts (L915-921).
  - `_frameOpen` → `bx-canvas.frameOpen` → `bx-frame.open(layout)`, opening the card first if needed (L927-929; `bx-canvas.js:82-96`).
- **Screen tabs** are tinted with the worst status of their tiles (L1790-1819).
- **Mobile:** `_mobile` follows `matchMedia('(max-width: 820px)')` (L207-210).
- **Layout keying:** tiles are keyed by path (`_isOpen`, L996; the canvas `repeat` is keyed by `o.path`, `bx-canvas.js:419`).
- **testApi:** L1672-1721.

#### 3.2 Cards (`bx-canvas.js` `_cardTemplate`, L274-303)

- **Head, left to right:** runtime dot (L284), path (L285), a `⇄N` badge button that opens the proposals panel (L286; `shell-kit.js:55-63`), `>_` (L288-290), `⚙` (desktop only, for tile admins; L291-293), pin (L294-295), `⋯` (L296-298), `✕` (L299).
- There is **no lifecycle or status indicator** on a card.
- On phones only `>_` and `⋯` remain (D56).
- A right-click relayed from inside the tile's iframe arrives as `bx-contextmenu` (L279).

#### 3.3 Sidebar rows (`bx-side.js` `_itemTemplate`, L270-301)

- A row shows: the runtime dot; the label; a `⇄N` marker (L293); a breathing `.stdot` plus an `st-<level>` class for status (L276, 294); `⚠` for a manifest error (L295); a `hidden` badge `.hidb` plus the `.hid` class (L276, 296); the runtime label; `⋯`.
- The tree filters out templates, offloaded tiles, hidden tiles (unless show-hidden is on) and filed tiles (L57-65).
- The show-hidden toggle is at L437-440. There is no badge for disabled tiles.

#### 3.4 Tile menu (`menus.js` + `web/bx-menu.js`)

`tileMenuItems` (`menus.js:86-127`) lists, in order:
1. A grid of four squares: terminal, logs, source, proposals. Each calls `a.frameOpen(path, layout)`; the ⇄ square shows `badge: s.prs[path]` (L93-98).
2. Close and pin, or open.
3. "Open full page", which is `window.open('/c/<p>/')`.
4. For tile admins, an admin block (L109-124):
   - Enable / Unhide, or `⏸ Disable` (danger, confirmed);
   - `⊘ Hide`;
   - one line per section: Access…, Runtime…, Vault…, Roles & grants…, Interfaces…, Backup…, Cron….

The builders are pure functions tested by `hack/menus.test.mjs`.

The item schema is at `bx-menu.js:13-23`. The desktop grid is `auto-fit, minmax(42px, 1fr)` (L102); on the phone sheet the grid is exactly 4 columns (L145).

#### 3.5 Tile admin popover (`bx-tile-admin.js`)

- **Header:** a state pill, and a `⟳` button that reloads the panel's data, not the tile (L509-517).
- **Sections** are `<details data-sec>`:
  - lifecycle: the pill plus enable / disable / hide, using the native `confirm()` (L178-194);
  - access;
  - runtime: from `/runtime`, admin-only; shows state, generation and restarts (L196-214);
  - vault, grants, interfaces, backup, cron (L519-526).
- `show(section)` opens a section and scrolls to it (L494-507).
- `_do` keeps an action's refusal inside the section that asked (L163-174).
- On phones the popover becomes a sheet (`shell-css.js:265-271`).

#### 3.6 Admin, manager and organisations tiles

- **Admin tile:**
  - Runtime tab: a table with backend state (from `/runtime`), a lifecycle cell and an offloaded list (`tabs/runtime.js:336-417, 667-687`).
  - Backup tab: the guided disable → back up → offload flow (`tabs/backup.js:193-222`).
  - Map tab: hidden-tile filtering (`tabs/map.js:24-140`).
  - `setLifecycle` (`shared.js:156-165`).
- **Manager tile** (`tiles/manager/index.html`):
  - Tabs: create, clone, new from template, import builtin, from git, updates (L73-80).
  - Clone: "Secrets and stored resource data don't come along" (L113-118). This is a precedent for "a new deployment starts with empty data".
  - The Updates tab (Replace / Propose as PR / Merge / Skip, L461-509) is the only way new shell UI reaches an existing workspace.
- **Organisations tile:** a list of org tiles with hide/unhide (`organisations.js:720-741`) and a pending-approvals section (L427-445).

#### 3.7 Where lifecycle or runtime state is shown or controlled today

| Surface | Shows | Controls |
|---|---|---|
| Tile menu (`menus.js:109-124`) | – | Enable/Unhide, ⏸ Disable, ⊘ Hide |
| Tile admin (`bx-tile-admin.js:178-214, 509-516`) | state pill, backend runtime | enable/disable/hide |
| Sidebar (`bx-side.js:276, 294-296, 437-440`) | `hidden` badge, status dot | show-hidden |
| Shell (`bx-shell.js:574-596`) | removes offloaded tiles, counts hidden | – |
| Open-tile menu (`menus.js:69-82`) | filters offloaded/hidden | – |
| Admin runtime / backup / map tabs | backend state, lifecycle | enable/disable/hide/offload |
| Organisations tile | hidden pill | hide/unhide |
| Shell footer (`bx-shell.js:1292-1338`) | "N / M running" | – |
| `bx status` CLI | `/tile-status` | – |
| iOS navigator (`Catalog.swift:111-113`) | hides offloaded and disabled tiles | – |

*(inference)* A paused or deployed state belongs next to the lifecycle pill in the tile admin, next to the `hidden` badge in the sidebar, and next to `⇄N` on the card head.

### 4. Native iOS app: hooks that would care (identify only)

- **Event parsing:** `AppEvent.parse` (`native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:23-83`).
- **Reload targeting:** `ReloadTargets` (L154-181), the D99 mirror of `isReloadTarget`.
- **Routing:** `WorkspaceEvents.receive`, `reloads(of:)` and `onReload(of:)` (`native/ios/App/Model/WorkspaceEvents.swift:99-121, 239-266`). Consumers:
  - `NativeTile.swift:115-122, 373-374`;
  - `TileScreens.swift:121-122`;
  - `TileHatches.swift:86-90`;
  - `WebTileController.swift:116-122`.
  - Build events are parsed but not used.
- **Tile listing:** `TileInfo` / `Catalog`, which parse leniently, with `isListed` (`Catalog.swift:1-8, 47-113`); fetched at `WorkspaceModel.swift:102`.
- **Layout pref:** `ScreenInfo.tilePaths` drops duplicate paths (`Catalog.swift:184-203`).
- **URLs:**
  - `TileScheme.pageURL`, `runtimeURL` (`/c/<tile>/?native=1`), and `tile(forPath:)`, which takes the longest match and ignores the query (`TileLoading.swift:44-80`);
  - `TileSurface.pick` (L18-26);
  - Safari, handoff and deep links: `WebTileController.swift:157`, `TileScreens.swift:88/252/277`, `WebHandoff.swift:95`, `DeepLink.swift:138`.
- **Runtime document:** `/c/<tile>/?native=1` (`docs/elements.md` §Native app UI; D99 at `plans/DECISIONS.md:2973-3004`).

### 5. Size budgets

Source: `hack/size-budget.txt`, enforced by `internal/sizebudget`. An unlisted JS/HTML file may have at most 900 lines. A listed file that shrinks below 90% of its budget fails the test until the number is lowered.

| File | Lines | Budget | Headroom |
|---|---|---|---|
| web/bx-frame.js | 866 | 900 (cap) | 34 |
| web/frame-titlebar.js | 314 | 900 | 586 |
| web/frame-launcher.js | 231 | 900 | 669 |
| web/bx-code.js | 507 | 900 | 393 |
| web/bx-logs.js | 170 | 900 | 730 |
| web/bx-prs.js | 260 | 900 | 640 |
| web/bx-terminal.js | 713 | 900 | 187 |
| web/bx-agent.js | 783 | 900 | 117 |
| web/xbin-client.js | 412 | 900 | 488 |
| web/events-socket.js | 49 | 900 | 851 |
| web/bx-menu.js | 429 | 900 | 471 |
| web/bx-dialog.js | 149 | 900 | 751 |
| web/frame-info.js / term-sessions.js / bx-kit.js | 100 / 124 / 173 | 900 | – |
| web/bx-grants.js / bx-bindings.js | 176 / 255 | 900 | – |
| shell/bx-shell.js | 1879 | 1892 (listed) | 13 (floor 1702) |
| shell/bx-canvas.js | 428 | 900 | 472 |
| shell/bx-side.js | 454 | 900 | 446 |
| shell/bx-tile-admin.js | 531 | 900 | 369 |
| shell/menus.js | 127 | 900 | 773 |
| shell/shell-kit.js / shell-css.js | 91 / 580 | 900 | – |
| tiles/admin/admin.js | 314 | 318 (listed) | 4 (floor 286) |
| tiles/admin/tabs/runtime.js | 689 | 900 | 211 |
| tiles/manager/index.html | 564 | 900 | 336 |
| tiles/organisations/organisations.js | 751 | 900 | 149 |
| hack/ui-harness/shots.js | 870 | 877 (listed) | 7 (floor 789) |

### 6. Tests

#### 6.1 UI harness passes and what they cover

- **reloadFocus** (`shots.js:353-431`, asserting): bx-frame's reload focus and z-order — `beginReload`, `notifyLoad`, `setHover`, `raiseFocusedFloat`. It simulates a reload rather than editing a file.
- **windows** (asserting): pop geometry (D66), floats, spawned windows, grid drags.
- **tabStrip** (asserting): tab-strip scrolling on the degraded bar (fitBar).
- **termSessions** (asserting):
  - the tab and `✕` stay inside the window and uncovered (`passes/termsessions.js:42-56`);
  - the rename dialog, answered with `answerDialog`.
- **termSets** (asserting): the restart confirm for the network picker (L20-21).
- **vmToggle** (asserting): the disabled reason; the confirm (L97-100); a restored window loads the tile state; the launcher's VM switch; the `termvm` pref.
- **termRun** / **predict** (asserting): the terminal.
- **agentTab** (asserting): the agent tab has the same bar. **It asserts the `.lyt button` count is exactly 5** (`passes/agenttab.js:238`) and checks code/split.
- **menus** / **mobile** (`shots.js:197-316`, screenshots only):
  - the four tile-menu squares; clicking "logs" opens the pop;
  - the sheets, the drawer, the admin sheet.
- **menuOpen** (asserting): D80 placement and the find box.
- **contextCopy**, **netPickers** (the tile-admin interfaces section), **adminTabs** / **adminMap** (asserting).
- **newTile** (asserting): the error box in bx-dialog.
- **viewAs** (asserting): every write is refused.
- **tileAssets** (asserting): asset-gating modes; writes files into `$WS` (L66).
- **tilePages** (asserting).
- **agentTemplate** (asserting): writes a file to swap a backend mid-call (`agenttemplate.js:159-161`). SKIPs without gocryptfs.

**Not exercised by any pass:**
- the build-error overlay;
- a real save → `reload` → frame reload;
- the content of the logs and proposals panels;
- `isReloadTarget`;
- an OLD scaffold against a new binary. `run.sh` serves the source scaffold via `--dev-overlay` (L69-75), and the legacy-workspace fixture only tests boot (`docs/maintenance.md:162-183`).

#### 6.2 Node tests

`make js-test` runs `node --test hack/*.test.mjs`. Relevant files:
- `menus.test.mjs`: the four squares (L109-111); the admin block, indexed by position (L131-150).
- `term-sessions.test.mjs`, `grid-layout.test.mjs`, `rev-draft.test.mjs`.
- `xbin-client-ws.test.mjs`: the events socket origin.
- `term-predict*.test.mjs`.

No `web/*.test.mjs` exists. No node test covers bx-frame, frame-titlebar/fitBar, frame-launcher, events-socket, bx-dialog or bx-menu.

`make js-check` parses every script, resolves named imports, and fails on `._member` access inside `hack/ui-harness` (`Makefile:190-192`).

#### 6.3 Harness conventions (`docs/maintenance.md` "UI harness"; D107)

- Drive elements only through `testApi()`.
- DOM hooks such as `.card[data-path]`, `.item[data-path]`, `details[data-sec]` are part of the markup contract.
- Wait for conditions (`waitFor` / `waitSel` / `settle`), never for time.
- Write `SKIP <reason>` when the environment lacks something; never time out, never pass silently.
- Leave no window behind: end sessions and delete the `term:<tile>` and `termvm:<tile>` prefs.
- Use `showPickers` / `PICKERS`; never assume `.titlebar select.scope`.
- A new pass is `passes/<name>.js`, registered in `PASSES` in `shots.js` (the registry is at `shots.js:844-848`).

### 7. UX conventions new UI must follow

- **bx-dialog specs are data only:** `{title, message, error, fields:[{name, label, type: text|password|number|textarea|select|checkbox, value, placeholder, options}], buttons:[{label, value, primary, danger}]}`.
  - It resolves with `{button, values}`; `null` means dismissed.
  - Strings are rendered as text only. `from` shows which tile asked (`bx-dialog.js:9-31`).
  - D56 rejected rich content in dialogs as an anti-phishing property.
  - Inside the frame, use `_ask` / `_confirm` / `_prompt`. The native `confirm()`/`prompt()` still used in the shell menus, bx-tile-admin and bx-prs is the older style and is not harness-drivable.
- **bx-menu items are plain objects** `{label, icon, hint, badge, mono, disabled, danger, checked, keywords, quiet, title, action, items}`, plus `{kind: 'sep' | 'header' | 'grid' | 'input'}`.
  - The action runs after the menu closes.
  - Menus are anchored on desktop and a bottom sheet with drill-in on phones.
  - Builders stay pure in `menus.js` and get a test case.
  - bx-menu is shell-internal, not a tile API (D56; `docs/frontend-kit.md`).
- **Approvals:** the self-hiding `<bx-grants>` and `<bx-bindings>` panels above the canvas, plus the "⚑ organisations N" count.
- **Health:** `xbin.status` gives a breathing dot, a tab tint and a title mark; `xbin.notify` gives a toast.
- **Badges:** amber `.prb` `⇄N`, pill badges, and `.on` for an active toggle. Tooltips name the consequence. Offers are amber and appear only when relevant. A disabled control explains why in its tooltip.
- **The bar degrades, never clips:** new content must feed `barKey`, render in both the bar and the tools row, and use `button`/`select`. A select built from data marks its option with `live` `.selected`.
- **Mobile, under 820px:**
  - the terminal window becomes a full-screen sheet (`bx-frame.js:154-158`) with the bar always degraded;
  - the card head keeps only `>_` and `⋯`;
  - the admin popover becomes a sheet;
  - a long press (450 ms, within 8 px) opens menus;
  - sheet rows are at least 44px tall; the grid has 4 columns.
- **Theme:** every `var(--bx-*, literal)` fallback must equal `theme.css` (`make theme-check`).
- **Imports:** only through `/vendor/…` URLs. `lit` is the only bare specifier; it is mapped in every workspace's `xbin.json`.
- **Docs to update** alongside: `docs/elements.md` §bx-frame, `docs/overview/04-frontend.md`, `docs/overview/09-terminals.md` (title bar, logs tab), `docs/protocol.md` (/ws/events, /logs), `docs/frontend-kit.md` (shell-only modules), `docs/sdk.md` (`xbin.events`), `docs/changelog.md`.

## Surfaces this feature touches

### Terminal window title bar

- **Paths:** `web/frame-titlebar.js:26-49`, `web/frame-titlebar.js:61-62`, `web/frame-titlebar.js:76-96`, `web/frame-titlebar.js:106-109`, `web/frame-titlebar.js:121-136`, `web/frame-titlebar.js:145-213`, `web/bx-kit.js:145-146`
- **Today:** Path, tab strip, +, layout group (>_ { } ⇋ ▤ ⇄N), per-session pickers (net / API / VM / GPU) and the tile-scoped layerButtons (⬆ base update, ⟲ reset), then ✕. The bar degrades via fitBar, and barKey lists the state its width depends on. Everything is keyed by f.src and the active tab.
- **Change needed:** Add a tile-level live-reload control: a paused toggle plus a 'reload now' button with a pending count. Put it next to layerButtons and render it in both settings() branches and in the tools row. Add a 6th .lyt button for the deployments panel, with an optional badge. Add paused, pending and deployment count to barKey. New controls must be button/select elements.
- **Compat:** A shell-only module whose shape follows the shell (docs/frontend-kit.md). Served from the binary, so every workspace gets it on upgrade. Tiles that never pause behave the same, but a new button changes the UI for every tile; gating it on opt-in is an open question. agenttab.js:238 asserts exactly 5 .lyt buttons and must be updated.

### bx-frame core (reload, overlay, layouts, public API)

- **Paths:** `web/bx-frame.js:86-110`, `web/bx-frame.js:250-255`, `web/bx-frame.js:366-396`, `web/bx-frame.js:405-461`, `web/bx-frame.js:531-541`, `web/bx-frame.js:563-601`, `web/bx-frame.js:714-725`, `web/bx-frame.js:792-800`, `web/bx-frame.js:814-863`
- **Today:** The iframe loads /c/<src>/. It reloads on reload events (isReloadTarget), shows a build-error overlay on build-error and clears it on build-ok, re-keys on grants, counts PRs, and re-lists sessions. open(layout) accepts term|code|split|logs|prs. testApi for the harness. 866 of 900 lines. No backend state and no viewer tile level.
- **Change needed:** New layout id(s) in _layout, _setLayout and open(). Handle new event types (pause state, deployment builds) while keeping the reload path for the primary deployment unchanged. Load pause/deployment state with the rest of the tile state. Possibly draw a paused chip over the iframe (.frame-wrap) as the one surface that reaches old shells. Extend testApi. The logic must go into a new sibling module because of the budget.
- **Compat:** Tiles may import bx-frame (frontend-kit.md), so attributes, events and methods must stay. New layouts and an optional deployment attribute are additive. A downgrade that reads a new layout value from the saved pref shows an empty window body (L328, L839-845).

### Launcher and tile-state loader

- **Paths:** `web/frame-launcher.js:34-40`, `web/frame-launcher.js:129-147`, `web/frame-launcher.js:152-197`, `internal/server/server.go:326-351`
- **Today:** loadTileState loads agent history, GET /ws/term/env ({exists, baseOutdated, vm}, terminal-level gate) and the termvm pref. The empty window shows a base-update banner (.lbase), the VM switch, session cards and recent sessions. A 403 from /ws/term/env silently becomes {}.
- **Change needed:** Add pause/deployment state to the tile-state load, either as additive fields on /ws/term/env or from a new endpoint that also returns an explicit 'you may control this' flag. Show a paused / pending banner with 'Reload now' in the launcher, modelled on .lbase.
- **Compat:** Additive JSON fields are ignored by old code. frame-launcher is shell-only and binary-served.

### Deployments panel (new element)

- **Paths:** `web/bx-prs.js:41-95`, `web/bx-logs.js:61`, `web/bx-logs.js:92-97`, `web/bx-logs.js:141`, `workspace-template/tiles/admin/admin-css.js:294-298`
- **Today:** Does not exist. The nearest models are bx-prs (list and detail split, actions) and bx-logs (per-component stream with only a component attribute).
- **Change needed:** A new /vendor module mounted lazily in .panels. It lists deployments with status (healthy / building / failed / idle), a primary badge, the deployment hot reload is attached to, and the branch. Actions (deploy current code to X, promote X to Y, attach, make primary) confirm through f._confirm. Per-deployment logs via <bx-logs deployment=…> and an additive &deployment= parameter on /api/xbin/logs.
- **Compat:** A new /vendor URL is additive and must be listed as shell-only in frontend-kit.md. bx-logs needs 'deployment' added to observedAttributes.

### Other terminal-window panels (code, prs, terminal)

- **Paths:** `web/bx-code.js:206-283`, `web/bx-prs.js:103-177`, `web/bx-terminal.js:472-497`, `web/bx-terminal.js:619-647`, `internal/term/attach.go:190-194`
- **Today:** bx-code shows tree, file, log, diff and activity per component and refreshes on reload / build-ok; it shows no branch. bx-prs handles proposals per tile, applied with git am in the terminal, and uses native prompt(). bx-terminal gets the session frame {id, net, baseOutdated, label, scopes, netNote, vm, echoAck} and writes grey notice lines.
- **Change needed:** Optional: a branch or 'deployed-at' marker in bx-code for the git-branch variant; a grey terminal notice when hot reload is paused or a deployment happens (modelled on netNote); proposals stay per tile.
- **Compat:** Additive only; these panels follow the shell and are served from the binary.

### events-socket and reload targeting

- **Paths:** `web/events-socket.js:14-49`, `web/bx-frame.js:368-373`
- **Today:** One socket per page. mountedFrames is keyed by .src. isReloadTarget picks the longest-prefix src, and two frames with the same src resolve to whichever registered first.
- **Change needed:** If frames of non-primary deployments are mounted (a dev preview), targeting must key by src and deployment. New event types for deployment builds and reloads, and for pause state.
- **Compat:** onEvent is on the list of modules tiles may import, so keep its signature. Use new event types so old consumers ignore them.

### /ws/events vocabulary

- **Paths:** `internal/events/events.go:11-17`, `internal/boot/serve.go:161-206`, `internal/runner/runner.go:285-352`, `internal/obs/status.go:118-142`, `internal/server/server.go:592-610`, `docs/protocol.md:2179-2230`
- **Today:** reload, build-start, build-error, build-ok and status are keyed only by component and broadcast to every subscriber, tiles included. obs/status.go clears a tile's reported status on any build-start for that component.
- **Change needed:** Non-primary deployments must not reuse type + component. Add new type(s), or put data only on new types. Add a pause-state event (paused, pending count, who). Paused edits should publish no reload; 'reload now' publishes an ordinary one.
- **Compat:** Every existing consumer (bx-frame, bx-code, the shell, the admin tile, llm-gw, the iOS app) switches on type, so new types are ignored. New fields on old types would misfire in old clients (see hazards).

### xbin-client (inside the tile)

- **Paths:** `web/xbin-client.js:20`, `web/xbin-client.js:50`, `web/xbin-client.js:142-182`, `internal/server/static.go:431-470`, `docs/sdk.md:250-251`
- **Today:** Raw events via xbin.events.on on /ws/events?frame=. The tile has no reload logic of its own. xbin.self comes from the xbin-component meta. The SDK documents events as reload/build-*.
- **Change needed:** Possibly xbin.deployment, read from a new meta, so a non-primary frontend can label itself. A non-primary frontend's API calls need routing to the matching backend (token or URL; server design).
- **Compat:** New metas and new fields on the frozen object are additive. SDK semantics may only become more permissive (compat rule 8), so the meaning of existing reload events must not change for tiles.

### Shell card head

- **Paths:** `workspace-template/shell/bx-canvas.js:274-303`, `workspace-template/shell/shell-kit.js:55-63`, `workspace-template/shell/bx-canvas.js:419`
- **Today:** Runtime dot, path, a ⇄N badge button, >_, ⚙, pin, ⋯, ✕. No lifecycle or status indicator. On phones only >_ and ⋯. Cards keyed by path.
- **Change needed:** An optional chip (paused / deployment) next to ⇄N, modelled on prBadge.
- **Compat:** Workspace-owned: it only appears after bx builtin update, so it must tolerate a missing server field. Never add a second card for the same path.

### Shell sidebar row

- **Paths:** `workspace-template/shell/bx-side.js:270-301`, `workspace-template/shell/bx-side.js:57-65`, `workspace-template/shell/bx-side.js:437-447`
- **Today:** Dot, label, ⇄N marker, breathing status dot, ⚠ manifest error, 'hidden' badge, runtime label, ⋯. Filters offloaded and hidden tiles. The '⚑ organisations N' pending count.
- **Change needed:** An optional paused badge modelled on .hidb.
- **Compat:** Workspace-owned. Never list deployments as separate rows, since old shells would show them as tiles.

### Tile menu (squares and admin block)

- **Paths:** `workspace-template/shell/menus.js:86-127`, `web/bx-menu.js:13-23`, `web/bx-menu.js:102`, `web/bx-menu.js:145`, `hack/menus.test.mjs:109-150`
- **Today:** Four squares (terminal, logs, source, proposals) that call frameOpen(path, layout). Open full page = /c/<p>/. Admin block: Enable/Unhide, ⏸ Disable, ⊘ Hide, section lines.
- **Change needed:** A 5th square that opens the deployments layout; a 'Pause live reload' / 'Reload now' line; 'Open <deployment> full page'; a 'Deployments…' admin line.
- **Compat:** Workspace-owned, so the terminal window must be able to reach everything without it. On the phone sheet the grid has 4 columns and a 5th square wraps. menus.test.mjs asserts 4 cells and indexes the admin block by position. ⏸ is already Disable.

### Tile admin popover

- **Paths:** `workspace-template/shell/bx-tile-admin.js:178-214`, `workspace-template/shell/bx-tile-admin.js:494-528`, `workspace-template/shell/bx-shell.js:745-803`
- **Today:** Header state pill and ⟳ (reloads the panel's data). Sections lifecycle, access, runtime (/runtime, admin-only, matched by path), vault, grants, interfaces, backup, cron. Native confirm(). A sheet on phones.
- **Change needed:** A 'deployments' <details data-sec> section, and a paused / deployed pill next to the lifecycle pill.
- **Compat:** Workspace-owned. data-sec is a harness hook. /runtime rows matched by path become ambiguous if deployment backends share the path.

### Shell global state (bx-shell.js)

- **Paths:** `workspace-template/shell/bx-shell.js:194-201`, `workspace-template/shell/bx-shell.js:564-596`, `workspace-template/shell/bx-shell.js:1251-1290`, `workspace-template/shell/bx-shell.js:1382-1401`, `workspace-template/shell/bx-shell.js:890-929`
- **Today:** Refetches /components on every reload. Status map, PR summary map, pending count, toasts. Menu state and actions. 13 lines of budget left.
- **Change needed:** An optional summary of paused tiles and deployments (a new endpoint and event, modelled on /code/prs/summary), passed down to canvas, sidebar and menus. The logic must live in a sibling module.
- **Compat:** Workspace-owned. Old shells ignore new events. Extra reload events only cause extra /components fetches.

### Admin, manager and organisations tiles

- **Paths:** `workspace-template/tiles/admin/tabs/runtime.js:336-417`, `workspace-template/tiles/admin/tabs/runtime.js:667-687`, `workspace-template/tiles/admin/admin.js:155-157`, `workspace-template/tiles/manager/index.html:113-118`, `workspace-template/tiles/manager/index.html:461-509`, `workspace-template/tiles/organisations/organisations.js:720-741`
- **Today:** Admin runtime tab: backend state by path and a lifecycle cell; refreshes on reload / build-ok. Manager: clone (code only, no data or secrets) and the scaffold Updates tab. Organisations: hide/unhide for org tiles.
- **Change needed:** Admin: rows or a column per deployment in the runtime tab (tabs/runtime.js has room; admin.js has 4 lines). Manager: probably none; its Updates tab is how new shell UI arrives. Organisations: none.
- **Compat:** Workspace-owned. /backends and /runtime entries matched by path would collide if deployment backends reuse the path.

### iOS app hooks

- **Paths:** `native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:23-83`, `native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:154-181`, `native/ios/App/Model/WorkspaceEvents.swift:99-121`, `native/ios/App/Model/WorkspaceEvents.swift:239-266`, `native/ios/Packages/XbinCore/Sources/XbinCore/Client/Catalog.swift:47-113`, `native/ios/Packages/XbinCore/Sources/XbinCore/Client/Catalog.swift:184-203`, `native/ios/Packages/XbinCore/Sources/XbinCore/Client/TileLoading.swift:18-80`, `native/ios/App/Tiles/NativeTile.swift:373-374`, `native/ios/App/Tiles/TileScreens.swift:121-122`
- **Today:** reload goes to the most specific open tile (D99). Build events are parsed but unused. The tile list comes from /components (lenient parsing, isListed). The layout pref is read with paths de-duplicated. Tiles load at /c/<tile>/ and /c/<tile>/?native=1.
- **Change needed:** Identify only: for v1 the app should keep showing and reloading only the primary deployment.
- **Compat:** Apps lag months behind (compat rule 10). Never publish non-primary reloads as 'reload' for the same component; never add deployments as /components rows or layout tiles.

### Harness and node tests

- **Paths:** `hack/ui-harness/passes/agenttab.js:235-247`, `hack/ui-harness/passes/termsessions.js:42-60`, `hack/ui-harness/passes/tabstrip.js`, `hack/ui-harness/passes/vmtoggle.js:97-100`, `hack/ui-harness/shots.js:353-431`, `hack/ui-harness/shots.js:844-848`, `hack/ui-harness/passes/agenttemplate.js:159-161`, `hack/ui-harness/lib.js:147-158`, `hack/menus.test.mjs:109-150`
- **Today:** reloadFocus simulates reloads. Bar reachability and dialog flows are asserted. The .lyt count is fixed at 5. Menu squares are only screenshotted. No test covers the build overlay, a real save-and-reload, or old scaffold against a new binary.
- **Change needed:** A new passes/<name>.js that edits a file in $WS (precedents: agenttemplate, tileassets) to test pause, pending count, reload now, and the deployments panel through new testApi names. Update agenttab and menus.test. Restore pause state at the end of the pass.
- **Compat:** shots.js has 7 lines left, so a new pass costs one require plus one PASSES entry. SKIP, never time out. Harness code may use testApi only.

### Docs

- **Paths:** `docs/elements.md:309-360`, `docs/overview/04-frontend.md:108-200`, `docs/overview/09-terminals.md:459-547`, `docs/protocol.md:2179-2230`, `docs/frontend-kit.md`, `docs/sdk.md:250-251`
- **Today:** Document live reload, the build overlay, the title-bar degrade rule, the four menu squares, the logs tab, the event list and the xbin.events types.
- **Change needed:** Document the pause/reload-now control, the deployments panel, new event types, new or extended endpoints, and the new shell-only /vendor module.
- **Compat:** A changelog entry is required (compat rule 11).

## Hazards

- Publishing a non-primary deployment's build-error/build-ok with the primary's component string makes old bx-frame code paint the dev failure over the MAIN frame, or clear a real main overlay (web/bx-frame.js:374-379).
- A non-primary 'reload' with the primary's component spuriously reloads main frames (web/bx-frame.js:371-373) and the iOS app's open view (native/ios/App/Model/WorkspaceEvents.swift:243-245), and makes the shell, admin tile, llm-gw and bx-grants refetch.
- internal/obs/status.go:129-142 clears a tile's reported status on ANY build-start for its component, so a dev build-start keyed like main would erase main's sidebar status.
- Tile status is one slot per component (internal/obs/status.go:104-116; workspace-template/shell/bx-shell.js:1257-1266), so a dev backend calling xbin.status/notify would overwrite main's dot, tab tint and title mark.
- isReloadTarget (web/events-socket.js:35-49) is keyed by src with ties broken by first registration, so a primary frame and a dev preview frame with the same src would not both reload.
- Layout tiles are keyed by path (workspace-template/shell/bx-shell.js:996; bx-canvas.js:419) and read by the iOS app, which de-duplicates paths (Catalog.swift:184-203); a second card for the same path breaks old shells.
- Listing deployments as extra /api/xbin/components rows would make them appear as tiles in old shell sidebars (bx-side.js:57-65) and in the app navigator (Catalog.swift lenient parsing).
- URL shape for a non-primary frontend (inference): /c/~<token>/ is already taken by strict asset gating (web/xbin-client.js:324-343); /c/<tile>/<sub> is a sub-path of the tile; /c/<tile>@dev/ fails the longest-prefix lookup in frame-info.js:55-62 and TileLoading.swift:74-79; a query parameter keeps the tile identity but iOS drops it.
- Budgets are nearly exhausted: web/bx-frame.js 866/900, shell/bx-shell.js 1879/1892, tiles/admin/admin.js 314/318, hack/ui-harness/shots.js 870/877, so new UI must go into new sibling modules.
- Names already taken: ⏸ = 'Disable' (menus.js:114); 'paused' = disabled (tabs/runtime.js:667-669) and suspended memberships (tabs/orgs.js:155); ⟳ 'reload' = refetch the admin panel (bx-tile-admin.js:516); 'identity' = the tile path and the sender window (bx-frame.js:464); 'deployment' = obsolete plans/deployment.md and the deploy/ installer.
- UI placed only in scaffold files (shell, menus, sidebar, tile admin, admin/manager tiles) never reaches workspaces that do not run bx builtin update (docs/compat.md rules 2 and 4); only web/ ships with the binary.
- Existing tests hard-code today's shape: agenttab.js:238 expects exactly 5 .lyt buttons, and menus.test.mjs:110 and :135 expect 4 squares and a fixed admin-block index.
- Pause is tile-wide server state; a harness pass that pauses must restore it (like the term:/termvm: prefs), or later passes such as reloadFocus run against a paused tile.
- Non-bus events reach every subscriber, including tiles and users who cannot read the tile (internal/server/server.go:592-610), so dev deployment names and compiler output would be broadcast the same way main's are today.
- frame-info caches /api/xbin/components once per page (web/frame-info.js:44-51), so pause/deployment state must not be read from it or it will go stale.
- The frame does not know the viewer's level on the tile: a /ws/term/env 403 silently becomes {} (web/frame-launcher.js:34-38), so new controls need an explicit permission flag or they will render for read/write users and then fail.
- Native confirm()/prompt() in the shell menus (bx-shell.js:907), bx-tile-admin (e.g. L188) and bx-prs (L171) cannot be driven by the harness; new UI in the frame should use _confirm/_ask (bx-frame.js:714-725).
- The phone menu sheet grid is fixed at 4 columns (web/bx-menu.js:145), so a fifth tile-menu square wraps to a second row on phones.
- dragWindow skips only button, select and .tab targets (web/bx-kit.js:146), so any other element added to the title bar starts a window drag when pressed.
- View-as sessions are read-only (D64; passes/viewas.js), so pause/deploy/reload actions must be refused by the server and the UI must show the refusal.

## Open questions

- Does 'reload now' mean rebuild the backend and reload every viewer's frame, or just reload this viewer's frame? The web UI has no local reload action today; the iOS app has a local Reload menu item (NativeTile.swift:346, TileScreens.swift:86).
- Who may pause, deploy or promote: terminal-level users (termEnvGate, server.go:339-351) or owner/admin (the lifecycle rule, D24)? And how does the frame learn the viewer's level, which it never fetches today?
- Should the paused state be visible to every viewer on the tile itself (a chip drawn by bx-frame, which ships with the binary), or only in scaffold surfaces (card head, sidebar) that need bx builtin update?
- Should a pause carry attribution ('paused by dev1, 3 changes waiting') so co-editors understand why their saves do not reload?
- How is a non-primary frontend viewed: a second frame (which needs a URL scheme and a frame token bound to the deployment), a preview inside the deployments panel, a window like xbin.window, or only 'open full page'?
- Where does the deployments UI live: a 6th layout button, a deployment picker next to the path label, a tile-admin section, or all of them? Should it appear only for tiles that opted in or have more than one deployment, to honour 'zero change'?
- Event design: new event types, versus a deployment field on existing types, versus component strings like apps/x@dev (can such a string collide with a real tile path)? What does the shell or app need (a summary endpoint like /code/prs/summary)?
- Per-deployment logs: a deployment selector inside the existing ▤ logs tab, or logs embedded in the deployments panel?
- In the git-branch variant, should bx-code show the checked-out branch and which deployment each commit is live on (git/log returns no branch today)?
- Naming and iconography: 'deployments' vs 'identities' vs something else, and a pause glyph that does not clash with ⏸ Disable.
- Does the iOS app need anything in v1 beyond ignoring non-primary events, or will non-primary deployments get native.js runtime documents later?
- Should open terminals print a grey notice line on pause or deploy events, like netNote (bx-terminal.js:632-635)?
