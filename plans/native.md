# Native client — the xbin app (iOS first)

> Status: **implemented** (2026-09-26; decisions D91–D98). Built and tested:
> the xbind side (runtime document, discovery, device login, frame-token
> binding, push and `POST /notify`, the agent-session additions — §20 rows
> 1–10), the runtime `/vendor/xb-native.js` and its vocabulary, the Lit
> reference renderer, the fixtures, the `bx` native commands, the eight tile
> rewrites and the agent template's native view. The iOS app, its Swift
> packages and the SwiftUI renderer are written; the packages and the
> renderer's model pass `swift test` on Linux, but nothing has been compiled
> by Xcode or run on a device yet (the first Apple CI run is pending).
> Open: who operates the push relay (decision 13) and App Review (16). §26
> lists where the build departs from this text. Companion plans:
> plans/tile-asset-auth.md (strict frontend gating) and
> plans/agent-template-native.md (the agent tile's native mode). Dev loop:
> native/AGENTS.md.

## 1. What and why

A native app for xbin, iOS first (Android later). It is **the mobile client of
a workspace**: phones and tablets get an app that feels platform-first, while
the desktop keeps today's web shell and today's web frontends untouched.

The bar, set by the owner:

- **Existing tiles never break.** The builtins are examples; roughly 500k LOC
  of third-party tiles are out there. Every tile works in the app on day one —
  as an excellent WebView of its `index.html`. Native UI is opt-in per tile.
- **Web stays web.** The desktop shell renders web frontends only; they are,
  by definition, the most expressive surface (many charts, graphics, complex
  interaction). Native tile UI exists for mobile platforms only.
- **Frontend logic stays on the frontend**, ideally the same code as the web
  frontend: a tile's native UI is JavaScript running on the device, using the
  same `window.xbin` API and able to import the same modules as `index.html`.
- **Multi-workspace is first-class**: several xbind instances, fast switching —
  a browser keeps workspaces in tabs, the app must do at least as well.
- **Device login** with a hardware-bound key: Secure Enclave, Face ID.
- **Terminal and ACP agent get uncompromising native UX** — they are why people
  will open the app.
- **The agent template is the show-off tile** (plans/agent-template-native.md).
- **Strict frontend permission gating** — every tile asset load is credentialed
  and checked (plans/tile-asset-auth.md; the app is strict from day one, §6).
- **iPhone Duo (the foldable) and iPad inform the design now**; the terminal
  and the agent always get one full-screen surface — more screen means more
  text, never a second pane.

## 2. Principles and invariants

1. **A human credential never reaches tile code.** The app's user credential
   (a device session, §5) is used only by the app's own Swift code: the tile
   list, whoami, frame-token minting, `/ws/term`, agent sessions. Every tile —
   web or native — runs as *that tile*, through its frame token, in its
   opaque-origin sandbox, exactly like a web frame (plans/auth.md §6, ND8).
2. **No credential-less loads in the app.** Every request a tile makes carries
   that tile's frame token (§6.1). Nothing relies on IP or Fetch-Metadata
   heuristics.
3. **No device APIs for tiles.** Tile JS gets the web platform WebKit offers,
   the `xbin` client API, and the vocabulary renderer — no camera, location,
   contacts, files, notifications or any other native API is exposed to tile
   code (App Review 4.7.2, §23). Attachments and pickers are app UI whose
   *result* (bytes, a string) is handed to the tile.
4. **Additive only.** Every server change is additive (docs/compat.md); an old
   xbind keeps working with a new app (web tiles, reduced shell features), a
   new xbind with an old app (the vocabulary and bridge only grow).
5. **The vocabulary is a rendering format, not an API bridge.** Tiles describe
   semantic UI; the app decides how it looks and behaves on each platform.
6. **One surface at a time for text-heavy work.** The terminal, the ACP agent
   and chat transcripts are never split with another pane (§15).

## 3. Architecture

```
┌──────────────────────────── xbin app (Swift) ───────────────────────────────┐
│ Workspaces · device keys (Secure Enclave) · switcher · inbox · settings     │
│                                                                             │
│  shell-native surfaces            tile surfaces                             │
│  ┌──────────────┐ ┌─────────────┐ ┌───────────────────┐ ┌─────────────────┐ │
│  │ Terminal     │ │ ACP agent   │ │ Web tile (default)│ │ Native tile     │ │
│  │ SwiftTerm +  │ │ transcript, │ │ WKWebView of      │ │ hidden WKWebView│ │
│  │ predictive   │ │ cards, diff,│ │ index.html        │ │ runs native.js  │ │
│  │ echo         │ │ composer    │ │                   │ │  → tree/patches │ │
│  └──────┬───────┘ └──────┬──────┘ └─────────┬─────────┘ │ SwiftUI renderer│ │
│         │ device session │                  │ frame     └────────┬────────┘ │
│         │ (bearer)       │                  │ token via          │ frame    │
│         │                │                  │ scheme handler     │ token    │
└─────────┼────────────────┼──────────────────┼────────────────────┼──────────┘
          ▼                ▼                  ▼                    ▼
   /ws/term, /api/xbin/term/sessions   /c/<tile>/…, /api/<tile>/…, /ws/events?frame=
                                xbind (unchanged semantics)
```

Three kinds of content:

- **Web tiles (default for every tile)** — a WKWebView of the tile's
  `index.html`, wrapped in native chrome (§6).
- **Native tiles** — the tile ships `native.js`; it runs in a hidden WebKit
  runtime and renders the semantic vocabulary; SwiftUI draws it (§7–§9).
- **Shell-native surfaces** — the workspace switcher, tile navigator, "Needs
  you" inbox, settings, the **terminal** (§12) and the **ACP agent** (§13),
  written in Swift against xbind's existing protocols.

## 4. Multi-workspace

A **workspace** in the app is `(server URL, user, device key)`. One server can
appear more than once (two accounts). Workspaces are fully isolated:

| Per workspace | How |
|---|---|
| credentials | its own Secure Enclave key + Keychain items (session, relay handle) |
| web storage | `WKWebsiteDataStore(forIdentifier:)` — tiles of different workspaces never share storage |
| sockets | the foreground workspace keeps its sockets; others close after a grace period and resume fast (cached lists, `?since` cursors) |
| identity | branding title + icon (`GET /api/xbin/branding`, D76) cached for the switcher |

**Switching.** A switcher overlay, available everywhere with one gesture (a
two-finger swipe down, or tapping the workspace title), lists workspaces with
branding icon, unread/needs-you counts and last activity; hardware-keyboard
`⌘1…⌘9` jump straight to one. It never docks as a rail — content keeps the
full screen. Recents (tiles, terminal sessions, agent sessions) span
workspaces, so "the thing I was just doing on the other box" is one tap.

**Needs you** — one inbox across workspaces: ACP permission/elicitation
requests, agent-template approvals/questions (via `POST /api/xbin/notify`,
§14), failed automations. Tapping an item opens it in its workspace.

**Windows = tabs.** On iPad and the unfolded Duo, each window (scene) shows one
workspace + one surface; a tile, terminal or agent can be dragged into a new
window (Stage Manager). Scene state (`@SceneStorage`) restores the exact
surface after relaunch or a fold/unfold.

**Navigating a workspace.** The tile navigator mirrors the web shell's model
without copying its layout: personal screens (prefs key `layout`), org screens
and folders (`GET /api/xbin/screens`), personal tiles (D88), and search over
`/api/xbin/components` (RBAC-filtered). Each tile shows whether it opens
natively (`native` in `/components`, §11) or as web.

**Deep links.** `xbin://<workspace-id>/c/<tile>[#fragment]`,
`xbin://…/term/<session>`, `xbin://…/agent/<session>`, `xbin://enroll?…`
(§5). Universal links can't cover self-hosted domains, so the custom scheme is
the contract; the web shell gains "Open in app" where the app is installed.

## 5. Device login (Secure Enclave, Face ID)

Today a human's only credential is the in-memory session cookie from
`POST /login` (a 302 + `Set-Cookie`, `internal/server/server.go`), bearer tokens
are owner/instance/terminal only (`internal/auth/auth.go`), and sessions die
when xbind restarts. The app adds a **device credential**:

- **Key.** A P-256 key generated **in the Secure Enclave**, access control
  `.biometryCurrentSet` (Face ID/Touch ID; re-enrolling biometrics invalidates
  it), non-exportable, one per workspace. The server stores only the public key.
- **Enrollment** (one of):
  1. *From a signed-in browser:* the shell's account menu gets **Devices →
     Add a device**, which mints a one-time enrollment code (5 min, bound to the
     user) and shows a QR code `xbin://enroll?u=<server>&c=<code>`; the app
     scans it, generates the key, `POST /api/xbin/devices/enroll {code, name,
     publicKey}`.
  2. *In the app:* password login (a JSON variant of `/login`), or SSO through
     `ASWebAuthenticationSession` ending in a one-shot ticket redirected to
     `xbin://sso?ticket=…`, redeemed with a PKCE-style verifier the app chose at
     the start (so another app registering the scheme can't redeem it) — then
     enroll as above with the fresh session.
- **Login.** `POST /login/device/challenge {device}` → a single-use nonce
  (~60 s, bound to workspace + device); the app signs `nonce ‖ server-origin ‖
  device-id` with the enclave key (Face ID prompt) → `POST /login/device
  {device, nonce, sig}` → `ecdsa.VerifyASN1` → a session like a cookie session
  (12 h idle / 30 d max, the existing defaults) returned as JSON and usable as a
  **bearer** (`Via: "device"`). It is a *human* principal: used by the app's
  Swift code only (§2 invariant 1).
- **Revocation.** Sessions record their device id; revoking a device drops its
  sessions. **Frame tokens gain a credential/generation field** so they die with
  the session that minted them — today they renew themselves indefinitely
  (`internal/server/api.go`, the `p.Component == comp` branch) and survive
  logout; the fix closes that gap for browsers too. Tokens minted before the
  upgrade keep verifying until they expire, and renew into bound tokens (tied
  to the user's current credential generation), so open pages survive it.
- **Restarts.** Sessions live in memory, so an xbind restart signs everyone out;
  the app catches the 401 and re-signs (one Face ID prompt). A setting offers
  "require Face ID when opening the app" independently.
- **Rules kept:** the login throttle, `Disabled` users, the SSO-only mode (D51),
  audit (`TouchLogin(…,"device")`), admins' "sign out everywhere".
- **UI.** Users manage their devices in the shell account menu (name, last
  used, revoke); admins see and revoke devices in the Users tab.

Passkeys were considered: they need an associated-domains entry per relying
party, which a generic app talking to arbitrary self-hosted domains cannot
ship. A raw enclave key with a challenge fits (decision 4).

## 6. Web tiles — the WebView fallback, made excellent

Every tile without `native.js` (and any native tile that fails, §7.6) opens as
a WKWebView of its `index.html`. This is the path most tiles will use for a long
time, so it gets real product work.

### 6.1 Loading and auth — strict from day one

- Each tile WebView uses a per-workspace **`WKURLSchemeHandler`**: the page is
  loaded as `xbin-ws://<workspace>/c/<tile>/` and *every* request the page makes
  to that scheme — the document, relative and absolute `/c/<tile>/…` assets,
  `fetch`/XHR to `/api/…`, `/vendor/…` — is forwarded by the app to the real
  server with **that tile's frame token** attached (`X-XBin-Frame-Token`).
  xbind authorizes (user, tile) against live RBAC. No cookies, no
  credential-less loads, no dependence on plans/tile-asset-auth.md; absolute
  `/c/<self>/…` references keep working because the handler sees them.
- Responses pass through unchanged, including xbind's CSP sandbox header — each
  document stays an **opaque origin**, as in the browser.
- WebSockets bypass scheme handlers. `xbin.ws()` and the events socket already
  carry `?frame=`; the one addition is an injected
  `<meta name="xbin-ws-origin" content="wss://host">` (xbind adds it when the
  request says it comes from the app) that `xbin-client.js` prefers over
  `location` (additive; a custom-scheme page has no `wss` origin to derive).
- The app calls `/whoami` before loading (fresh session), and reloads a page on
  foreground if it has been suspended longer than its token.
- To validate in Phase 1: streaming responses (SSE over `fetch`) through the
  handler, CORS for opaque-origin `fetch` to a custom scheme (xbind already
  answers `Access-Control-Allow-Origin: null`), `EventSource` on a custom
  scheme. Fallback if one fails: load from the real origin with plans/
  tile-asset-auth.md mechanism A/B.

### 6.2 The bridge — the existing protocol, nothing new

The tile ↔ shell `postMessage` protocol (docs/protocol.md "Tile ↔ shell
messaging": `xbin:resize | dialog | window | window-close`, replies
`xbin:reply`) is the whole bridge. In a top-level WKWebView `window.parent ===
window`, so `xbin-client.js`'s messages land on the page itself; a ~30-line
injected user script relays `xbin:*` messages to the app, and the app answers
with `xbin:reply` posted to the same window (which passes xbin-client's sender
check). No new API is exposed (§2 invariant 3).

- `dialog` → a native alert/sheet from the same data spec `bx-dialog` renders.
- `window` → a pushed screen (compact) or a new window (iPad/Duo), with the
  shell's traversal stripping and per-tile caps.
- `target=_blank` / `window.open` → Safari, only with `cap:open-links` (ND11),
  exactly as the browser sandbox allows.
- JS `alert/confirm/prompt` → native dialogs (WKUIDelegate).

### 6.3 Native chrome around the page

A native title bar (the tile's title; back/close), pull-to-refresh (reload),
a progress indicator, error and offline states with retry, keyboard avoidance,
safe areas, downloads handed to the share sheet / Files, "Open in Safari"
(signed in via a one-shot ticket, the D64 pattern), "Open on desktop" (Handoff).
Viewport: pages that set a mobile viewport get it; desktop-first pages render
at a comfortable width with zoom-to-fit available. Dark appearance follows the
tile (xbin tiles are dark by default).

**Chrome tiles** (`chrome: true`, e.g. the admin tile) act *as the human* and
can't run under a frame token — the app opens them in Safari (signed in by
ticket), never in a tile WebView.

## 7. Native tiles — the runtime

### 7.1 Entry point and discovery

A tile opts in by shipping **`native.js`** next to its `xbin.json` (or naming
another file: `"native": "./mobile/main.js"` in the manifest — additive, unknown
to old xbinds, ignored by them). `GET /api/xbin/components` gains
`native: {entry}` for such tiles, so the navigator knows before opening one.

### 7.2 The runtime: a hidden WebKit document per open tile

For each open native tile the app creates a hidden WKWebView (kept in the view
hierarchy, invisible, with WebKit's inactive-scheduling policy
(`WKPreferences.inactiveSchedulingPolicy`, to verify on the SDK) set so timers
aren't throttled while its tile is on screen, and throttled/suspended when not). It
loads an **xbind-generated runtime document**, `GET /c/<tile>/?native=1`,
through the same scheme handler as §6.1. The document is generated, not a tile
file, and gets the same D4 injection a tile page gets:

```html
<!doctype html><html><head>
  <!-- D4 injection: import map, xbin-component, frame token, sandbox meta,
       interfaces, /vendor/xbin-client.js -->
  <meta name="xbin-native" content="1">
  <script type="module">
    import '/vendor/xb-native.js';
    await import('./native.js');   // resolves to /c/<tile>/native.js
  </script>
</head><body></body></html>
```

Consequences:

- **Same identity and sandbox as the web frame**: the tile's frame token, CSP
  sandbox (opaque origin), `window.xbin` (fetch, ws, bus, events, iface, self,
  dialog, window, status, notify).
- **The full web platform**: ES modules, `fetch` with streaming bodies,
  WebSocket, `TextDecoderStream`, `Intl`, timers — and WebKit's JIT (JIT is
  unavailable to JavaScriptCore embedded directly in an app, decision 1).
- **Shared code**: `native.js` imports the tile's own modules (`./model.js`,
  `./fmt.js`, …) — the same files `index.html` imports. Even modules that import
  lit keep working (the runtime is a real document; it just never shows one).
- Browser-only calls behave: `confirm/alert/prompt` are native dialogs,
  `document.visibilityState` reflects whether the tile is on screen,
  `requestAnimationFrame` is driven by the renderer's frame clock.

### 7.3 `xb-native.js` — the template layer

Served at `/vendor/xb-native.js` (frozen once shipped, additive thereafter —
compat rule 3). A lit-html-shaped API, so agents and humans write what they
already write for the web:

```js
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

render(html`
  <screen title="Counter" style="form">
    <section>
      <row title="Count" detail=${count}/>
      <button role="primary" @tap=${inc}>+1</button>
    </section>
  </screen>`);
```

- **Templates** are parsed once per call site (like lit), producing a static
  structure plus holes. Tags must be vocabulary primitives (§8); self-closing
  tags are allowed; `&amp; &lt; &gt; &quot;` entities work.
- **Bindings:** `name="static"`, `name=${value}` (the raw JS value, not a
  string), `?name=${bool}`, `@event=${handler}`; `.name=${v}` is accepted as an
  alias of `name=${v}` for lit habits. Text content of `text`, `button`,
  `badge`, `code` becomes their `text`/`label` prop.
- **Children:** templates, arrays, `repeat(items, keyFn, tplFn)` (keyed),
  `nothing`, strings (wrapped as `text`).
- **`render(tpl)`** schedules a render; renders coalesce per frame. The runtime
  diffs against the previous tree and posts **patches** (§9). Re-rendering an
  unchanged tree costs nothing on the bridge — tiles can simply `paint()` after
  every poll.
- **Events** arrive as `{type, value, …}` objects: `@input=${(e) => draft =
  e.value}`.
- **Controlled and uncontrolled state.** A bound `value/open/collapsed/selected`
  makes the prop controlled (the tile owns it); left unbound, the renderer owns
  it. Text fields are app-owned while focused (no bridge round trip per
  keystroke); the tile's value is applied when it differs from the last value
  the field reported, so resets (`draft = ''`) work.
- **Validation.** Unknown tags, props of the wrong type, raw colours/sizes where
  a token is required: a diagnostic with the template location (in the Xcode
  console, `bx lint --native`, the preview). An element the *app* doesn't
  support (older app) triggers the fallback (§11).

### 7.4 `xbin.native` — the small app API

| Member | What |
|---|---|
| `xbin.native.caps` | `{v, renderer, app, prims: {name: rev}, features: []}` injected before `native.js` runs |
| `xbin.native.supports(name[, rev])` | feature test |
| `xbin.native.meta({title, icon, badge})` | title/icon/badge for the navigator and switcher |
| `xbin.native.copy(text)` | clipboard (the app writes it; no focus needed) |
| `xbin.native.share({text, url, file})` | the share sheet; `file` is a tile-relative path the app downloads with the frame token |
| `xbin.native.open(url)` | https only, only with `cap:open-links` (ND11) |
| `xbin.native.state` / `.saveState(obj)` | a small JSON blob the app keeps across runtime restarts (scroll/nav position) |

None of these is a device API; each is app UI acting on data the tile hands it.

### 7.5 Performance

- Patches, not trees, cross the bridge; SwiftUI lists are lazy.
- Markdown is lexed **in the runtime** (the vendored `marked` lexer) into
  tokens; the app renders tokens natively. Streaming re-lexes only the tail
  block. Web and iOS therefore agree on what a document *is*.
- Images are loaded by the app from tile-relative URLs with the frame token
  (never through the bridge); `data:` rasters ≤ 256 KiB are allowed.
- A budget per runtime (memory, bridge bytes/s) with diagnostics; at most a few
  runtimes alive (LRU), the rest suspended and re-created on return
  (`xbin.native.state` restores position).

### 7.6 Failure → web

Runtime crash, a module error, a render timeout (no tree within 5 s), an
unsupported primitive, or the remote kill switch (§23) all fall back to the web
tile (§6) with a quiet banner and the diagnostic in the tile's report.

### 7.7 Live reload

The existing `/ws/events` `reload` event for the component reloads the runtime
(like a web frame). xbind stops restarting the backend for `native.js` edits
(today `watchLoop` hands every changed path to the runner,
`internal/boot/serve.go`).

## 8. The vocabulary (v0)

Props are JSON values; tokens are named (§10), never raw colours or sizes.
"Controlled" props follow §7.3. SwiftUI and Lit columns name the mapping; the
Lit reference renderer (`/vendor/xb/`) exists for previews and tests only.

### 8.1 Structure and navigation

| Primitive | Props | Events | Children | SwiftUI | Lit (reference) |
|---|---|---|---|---|---|
| `nav` | — | `pop {depth}` | `screen`+ (first = root) | `NavigationStack(path:)` | stack with back bar |
| `screen` | `title`, `subtitle`, `style` list·form·scroll, `large`, `refreshable`, `search` | `refresh`, `search {value}`, `appear` | any; list/form: `section`s (+ one `toolbar`) | `List`/`Form`/`ScrollView` + `.navigationTitle` | header + body |
| `toolbar` | — | — | `button`, `menu`, `picker` | `.toolbar` | header actions |
| `section` | `title`, `badge`, `footer`, `collapsible`, `collapsed` | `toggle {collapsed}` | rows, controls, content | `Section` (collapsible) | `<fieldset>`-like group |
| `stack` | `axis` v·h, `gap`, `align`, `wrap` | — | any | `VStack`/`HStack`/`FlowLayout` | flex |
| `list` | `style` plain·inset·grouped | `more` | `row`, `section` | `List` (lazy) | list |
| `row` | `title`, `subtitle`, `detail`, `icon`, `badge`, `tone`, `mono` title·subtitle·detail·all, `nav`, `selected`, `disabled` | `tap` | optional `actions`, optional content | `LabeledContent`/`NavigationLink`, `.swipeActions` + `.contextMenu` | row, inline actions |
| `actions` | — | — | `button`s | swipe + context menu | inline buttons |
| `disclosure` | `title`, `open` | `toggle {open}` | any | `DisclosureGroup` | `<details>` |
| `tabs` | `selected`, `style` segmented·bar | `change {key}` | `tab`s | segmented `Picker` / `TabView` | segmented bar |
| `tab` | `key`, `title`, `icon`, `badge` | — | any | tab content | panel |
| `sheet` | `open`, `title`, `detents` medium·large | `dismiss` | any (+`toolbar`) | `.sheet` | modal |
| `split` | `prefer` auto·single | — | exactly 2 (primary, detail) | `ArrangementView` (iOS 27.1) / `NavigationSplitView`; stacked when compact | two columns |
| `spacer`, `divider` | — | — | — | `Spacer`/`Divider` | — |

`split` is for list/detail tiles only — never for transcripts or terminals (§15).

### 8.2 Content

| Primitive | Props | Events | SwiftUI | Lit |
|---|---|---|---|---|
| `text` | `text`, `style` (type role), `tone`, `mono`, `selectable`, `lines` | — | `Text(verbatim:)` | `<span>` (never `unsafeHTML`) |
| `markdown` | `source` → wire `tokens`, `streaming` | `link {href}` | native blocks from tokens; links only with `cap:open-links` | blocks from tokens |
| `image` | `src` (tile-relative or `data:`), `alt`, `aspect` fit·fill, `height` token, `preview` | `tap` | custom loader with frame token; Quick Look on tap | `<img>` via the preview's fetch |
| `icon` | `name` (§10.4), `tone` | — | SF Symbol | vendored SVG |
| `badge` | `text`, `tone`, `pulse` | — | capsule | pill |
| `notice` | `tone` info·ok·warn·danger, `title`, `text` | — | inset banner | banner |
| `progress` | `value` 0…1 or absent (indeterminate), `label` | — | `ProgressView` | `<progress>` |
| `chart` | `kind` line·bar·area·spark, `series` `[{name, points: [[x,y],…]}]`, `x` time·number·category, `y` number·bytes·percent, `height` | — | Swift Charts | inline SVG |
| `code` | `text`, `copy`, `wrap` | — | monospaced, horizontal scroll, copy button | `<pre>` |
| `empty` | `icon`, `title`, `text` | — | `ContentUnavailableView` | empty state |

`text` is always verbatim: SwiftUI `Text` built from a `LocalizedStringKey`
would parse Markdown and make links tappable, so renderers must use
`Text(verbatim:)`; markup is only ever the `markdown` primitive's tokens.

### 8.3 Controls

| Primitive | Props | Events | SwiftUI |
|---|---|---|---|
| `button` | `label`, `icon`, `role` primary·secondary·destructive·plain, `disabled`, `busy`, `confirm {title, message, label, destructive}`, `copy` | `tap` | `Button` (`confirm` → `.confirmationDialog` before `tap`; `copy` copies natively, no round trip) |
| `toggle` | `label`, `value`, `disabled` | `change {value}` | `Toggle` |
| `field` | `label`, `value`, `kind` text·secure·number·email·url·multiline·search·date·time, `placeholder`, `hint`, `error`, `disabled`, `submit` | `input {value}`, `change {value}`, `submit {value}` | `TextField`/`SecureField`/`TextEditor`/`DatePicker` |
| `picker` | `label`, `value`, `options [{value, label, icon}]`, `style` menu·segmented·inline | `change {value}` | `Picker` |
| `menu` | `label`, `icon` | — (children fire) | `Menu` |

`secure` field values are never persisted, logged or snapshotted
(`.privacySensitive()`), and are cleared when the app backgrounds.

### 8.4 The chat family (shared with the shell's ACP agent screen)

The agent template (plans/agent-template-native.md) and the shell's ACP agent
(§13) render with the **same Swift components**, so both are as good as the
best of them.

| Primitive | Props | Events | Children |
|---|---|---|---|
| `transcript` | `follow` (stick to bottom), `older` | `more`, `scrolled {atBottom}` | the kinds below |
| `message` | `role` user·assistant·system, `sender`, `text`, `markdown`, `streaming`, `time`, `files [{name, mime, src}]`, `queued` | `tap` | optional `actions` |
| `thinking` | `text`, `live`, `seconds`, `open` | `toggle` | — |
| `toolcard` | `title`, `icon`, `family`, `state` writing·running·ok·error·canceled, `chips [{text, tone}]`, `open` | `toggle`, `open` | `code`, `text`, `diff`, `image`, nested `transcript` (subagents) |
| `approval` | `title`, `text`, `options [{id, label, kind}]`, `note`, `feedback`, `settled {by, id}` | `choose {id, feedback}` | — |
| `question` | `title`, `schema` (flat JSON Schema), `settled` | `submit {content}`, `skip` | — |
| `plan` | `entries [{text, status}]` | — | — |
| `diff` | `files [{path, status, add, del}]`, `patch` | `open-file {path}` | — |
| `activity` | `text`, `live` | — | — |
| `step` | `glyph`, `text`, `tone` | — | — |
| `composer` | `value`, `placeholder`, `busy`, `disabled`, `attachments [{id, name, mime, progress}]`, `accept`, `upload {method, path}`, `slash [{name, hint, description}]` | `input`, `send {value}`, `stop`, `uploaded {name, response}`, `remove {id}` | chips (`button`s) |

Attachments: the composer's attach button opens app pickers (Photos, camera,
Files); the app **uploads the bytes itself** to the tile-relative `upload.path`
(with the frame token) and hands the tile the server's response — tile logic
stays in JS, no multi-MiB base64 crosses the bridge, and no device API is
exposed.

### 8.5 Escape hatches

| Primitive | Props | What |
|---|---|---|
| `terminal` | `src` (tile-relative WebSocket path), `title` | a terminal view connected **as the tile** to its own backend's pty endpoint, speaking the `/ws/term` framing (binary data + `{"op":"resize"}`); the human's xbind terminal is a shell surface (§12), never a tile element |
| `canvas` | `src` (a tile page) or `html` (static, no scripts), `height` | a WebView island in the tile's sandbox with the §6.2 bridge — the one sanctioned drawing escape; `html` gets a no-script CSP (the agent template's render preview) |

## 9. The tree wire format and the bridge

Runtime → app (one `WKScriptMessageHandler`, `xbn`):

```json
{"op":"mount","v":1,"root":{"k":"r","t":"screen","p":{"title":"Counter","style":"form"},"c":[]}}
```

```json
{"op":"patch","ops":[["set","r.0.0",{"detail":"43"}],["insert","r.0",2,{"k":"r.0.2","t":"notice","p":{"tone":"ok","text":"saved"}}],["remove","r.0.3"],["move","r.1:a",  "r.1",0]]}
```

A template with several top-level elements renders as a `fragment` node (no
visual of its own; e.g. a `nav` plus a `sheet` laid over it). A node is
`{k, t, p?, e?, c?}`: `k` a stable key, `t` the primitive, `p`
props (JSON), `e` the events the tile listens to, `c` children. Keys: a static
child is `<parent>.<slot>`; a keyed child (from `repeat` or `key=`) is
`<parent>.<slot>:<key>`; a hole holding a single-root template gives that root
the hole's key, a multi-root one gives its roots `<hole>.<i>`. Other messages: `{"op":"meta",…}`, `{"op":"error",
"kind","message","where"}`, `{"op":"call","id","what","args"}` (copy, share,
open — answered through `xbn.resolve(id, value)`).

App → runtime (`callAsyncJavaScript`): `xbn.event(k, type, payload)`,
`xbn.visibility(state)`, `xbn.resolve(id, value)`, `xbn.frame()` (the
renderer's frame clock). `caps` and `state` are injected at document start.

XbinCore (Swift, Foundation-only) owns the codec, the tree, patch application
and the diff invariants; the SwiftUI renderer observes the tree.

## 10. Theme tokens

Tiles name roles; each renderer maps them. The web shell's `theme.css` is
dark-only today; native tokens define both schemes.

### 10.1 Colour roles

| Role | Dark (web reference) | Light (web reference, new) | iOS |
|---|---|---|---|
| `bg` | `#1b1e24` (`--bx-bg`) | `#f6f7f9` | `systemGroupedBackground` |
| `surface` | `#23272e` (`--bx-panel`) | `#ffffff` | `secondarySystemGroupedBackground` |
| `surface2` | `#2b3038` (`--bx-panel-2`) | `#eef0f3` | `tertiarySystemGroupedBackground` |
| `border` | `#363c45` | `#d5d9df` | `separator` |
| `text` | `#d4d9e0` | `#1b1e24` | `label` |
| `muted` | `#868f9a` | `#5f6873` | `secondaryLabel` |
| `accent` (fills) | `#f5a623` | `#f5a623` | tint = xbin amber |
| `accentText` (text/icons in accent) | `#f5a623` | `#a86400` (4.7:1 on white) | amber, darkened in light |
| `onAccent` (text on an accent fill) | `#1b1e24` | `#1b1e24` (8.3:1 on amber) | dark label |
| `ok` | `#4caf50` | `#2e7d32` | `systemGreen` |
| `warn` | `#f2a71b` | `#9a6700` | `systemOrange` |
| `danger` | `#ef5350` | `#c62828` | `systemRed` |

(`#f5a623` on white is ≈1.9:1 — never use `accent` for text on light
backgrounds; that is what `accentText` is for.)

`tone` props take `muted`, `accent`, `ok`, `warn`, `danger` (the renderer picks
`accentText`/`onAccent` as the context needs); `notice` also takes `info`, a
neutral surface.

### 10.2 Type roles

`largeTitle`, `title`, `title2`, `title3`, `headline`, `body`, `callout`,
`subheadline`, `footnote`, `caption`, `caption2`, `mono`. iOS: the Dynamic Type
text styles of the same names (`mono` = `.body.monospaced()`), so every native
tile scales with the user's text size. The Lit reference renderer uses iOS's
default point sizes as px (body 17) so 390-wide previews line up with iOS.

### 10.3 Spacing and shape

One token set, for `stack gap` only: `none 0`, `xs 4`, `s 8`, `m 12`, `l 16`,
`xl 24`, `xxl 32`. Padding, radii and elevation belong to the renderer.

### 10.4 Icons

A curated name set (≈60), mapped to SF Symbols on iOS and to vendored SVGs in
the reference renderer. A sample:

| Name | SF Symbol | Name | SF Symbol |
|---|---|---|---|
| `plus` | `plus` | `trash` | `trash` |
| `check` | `checkmark` | `xmark` | `xmark` |
| `copy` | `doc.on.doc` | `share` | `square.and.arrow.up` |
| `refresh` | `arrow.clockwise` | `gear` | `gearshape` |
| `terminal` | `apple.terminal` | `box` | `shippingbox` |
| `key` | `key` | `lock` | `lock` |
| `globe` | `globe` | `shield` | `checkmark.shield` |
| `bolt` | `bolt` | `clock` | `clock` |
| `chart` | `chart.xyaxis.line` | `link` | `link` |
| `paperclip` | `paperclip` | `send` | `arrow.up.circle.fill` |
| `stop` | `stop.circle.fill` | `sparkles` | `sparkles` |
| `wrench` | `wrench.and.screwdriver` | `warning` | `exclamationmark.triangle` |

An unknown icon name renders a neutral placeholder (a diagnostic, not a
fallback).

## 11. Capabilities and versioning

- **Discovery over HTTP.** `/api/xbin/whoami` gains `native: {runtime: 1}`
  (this xbind serves runtime documents); `/components` gains `native:
  {entry}` per tile. An xbind without them: every tile is a web tile.
- **What the app supports** is injected as `xbin.native.caps` — vocabulary
  version `v`, a revision per primitive (additive props bump it), and feature
  flags (`chart.area`, `markdown.tables`, …). The runtime checks every rendered
  node; a tile can branch with `xbin.native.supports()`.
- **Skew.** New xbind + old app: an element or prop revision the app lacks →
  that tile falls back to web with a banner "update the app for the native
  view". Old xbind + new app: fine (the app speaks the older runtime).
- **Compat rule (docs/compat.md, new section):** shipped apps lag by months, so
  the vocabulary, `xb-native.js`, `xbin.native` and the tree format change
  **additively only**; removing or changing a meaning needs a new major `v`,
  served side by side.

## 12. The terminal (shell surface)

A native client of the existing `/ws/term` (docs/protocol.md): the `session`
frame, scrollback replay (≤ 256 KiB), binary PTY data, `resize`,
`ping`/`pong`, `ack` for predictive echo, `exit`; the session directory (D73:
list, rename, reattach, kill); network scopes (D65) and the VM toggle and disk
(D89/D90); environment reset.

- **Emulator:** SwiftTerm (MIT; decision 8) behind a thin adapter, so it can be
  replaced. CoreText rendering, 10k-line scrollback.
- **Input made for a phone:** a keyboard accessory row — `esc`, `ctrl`
  (sticky), `tab`, arrows (swipe on the row = arrow repeat), `| ~ / -`, and a
  customizable slot; hardware-keyboard shortcuts (`⌘K` clear, `⌘T` new
  session, `⌘⇧[`/`⌘⇧]` previous/next session); long-press to select, a precise selection mode,
  copy/paste, tappable links (with confirmation), pinch to resize the font,
  scrollback search.
- **Predictive echo (D70/D71):** a Swift port of `web/term-predict.js`,
  validated against `hack/term-predict.test.mjs` as its conformance suite; RTT
  from `ping`/`pong`, acks applied after the emulator has parsed prior output,
  the anchor rules for hidden cursors.
- **Reattach** resets the emulator before the replay (the server replays the
  whole scrollback, no offset).
- **Sessions UI:** a sheet over the terminal (never a docked pane) — sessions
  on this tile, rename, network scope and VM pickers (restarts ask first, as on
  the web), env reset.
- **Layout: one full-screen terminal.** On the unfolded Duo or an iPad the
  terminal gets more columns and rows, not a second pane. **Duo tabletop
  posture:** the terminal fills the upper half, the keyboard the lower half.
  Extra windows only when the user opens them.

## 13. The ACP agent (shell surface) — uncompromising

A native client of agent sessions (D74/D75/D77; docs/protocol.md "Agent
session events"): create with provider/mode/model/options, `prompt`, `cancel`,
`permissions/<pid>`, `elicitations/<eid>`, `options`, `restart`, history and
resume. Events via `GET …/events?since&follow=1` and `session` frames; the
client applies by `seq`, refetches on gaps, reconnect and foreground.

- **Transcript** (the §8.4 components): streaming markdown; thoughts that
  shimmer while live and fold to "Thought for Ns"; the plan checklist; tool
  cards with the D77 headline rules, exit-code chips, ANSI-rendered shell
  output (tail + "show all"), native diff viewer (per-call and per-turn
  `files.changed`); subagents nested (`parent`), openable full screen; turn
  dividers with stop reasons; gap/truncation markers.
- **Permission cards** keep D77's safety rules: the agent's own options,
  reject first when `defaultToNo`, "allow for the session" hidden when the rule
  isn't scoped, never auto-answered, "answered by X" when another client won.
  **Plan approval** with the feedback box. **Questions** as native forms from
  the elicitation schema.
- **Composer:** multiline, dictation, a slash palette (`status.commands`, with
  hints), mode/model/option pickers; the session is created eagerly so the
  pickers load before the first prompt. **Attachments** (images, files) need
  an additive server change (`/prompt` is text-only today).
- **Sign-in in one tap:** opens a full-screen terminal sheet running the
  provider's login command in the same `$HOME` (as the web does); URLs in it
  open in Safari.
- **Beyond the web:** a **Live Activity** / Dynamic Island for a running turn
  (elapsed, "waiting for you"); **push** for permission, question and turn end
  (§14); haptics on settle; resume from history; restart on net/VM change keeps
  the conversation (as the web does).
- **Layout: full screen, transcript only.** Diffs, files, tool output and the
  agent's terminal open as full-screen pushed views or sheets and return to the
  transcript. On the Duo/iPad the extra width goes to a comfortable text column
  and wider code/diff blocks — never a second pane.

## 14. Notifications and the push relay

Self-hosted xbind can't send APNs pushes (the APNs key belongs to the app's
publisher). An **opt-in relay run by the xbin project**:

- **Relay** (code in this repo, `relay/`; Go, stdlib only): holds the APNs key;
  maps opaque *handles* to device tokens; forwards sealed payloads. It never
  sees content.
- **Registration:** the app registers its APNs token with the relay → a handle;
  then, per workspace, registers `{handle, publicKey (X25519), kinds}` with
  xbind (`POST /api/xbin/devices/push`).
- **Delivery:** xbind seals `{workspace, kind, title, body, link, collapseId}`
  to the device key (HPKE) and posts it to the relay; the relay sends an alert
  with generic text and `mutable-content: 1`; the app's Notification Service
  Extension decrypts and shows the real text. Live Activity updates can't be
  decrypted by an extension, so they carry only generic state (running,
  waiting, elapsed).
- **Sources:** ACP `permission.request`, `elicitation.request`, `turn.end`
  (when the app isn't showing it); **`POST /api/xbin/notify {user, title,
  body, link}`** — a new, additive platform API any tile backend can use to
  reach a user who can read the tile (rate-limited, mutable per tile per user);
  the agent template uses it for Needs-you.
- **Control:** a workspace admin opts in (relay URL + per-workspace relay key);
  users choose kinds per workspace; everything is rate-limited both at xbind
  and the relay.

## 15. Adaptive layout — iPhone, iPhone Duo, iPad

- **Size classes.** iPhone: compact. **iPhone Duo** (5.4″ outer, 7.6″ inner,
  shipping with iOS 27.1): folded = compact; unfolded = regular × regular;
  book and tabletop postures while partially open. iPad: regular, Stage Manager.
- **One surface, full screen, for text.** The terminal, the ACP agent and chat
  transcripts (including the agent template) never share the screen. The
  navigator, switcher, session lists and conversation lists are overlays or
  sheets that dismiss once something is picked. A bigger screen buys more text.
- **Two panes only for list/detail tiles that ask** (`split`): `ArrangementView`
  on iOS 27.1, `NavigationSplitView` otherwise, stacked when compact.
- **Folding never loses state**: runtimes, sockets and scroll positions live
  above the view layer; `@SceneStorage` restores the surface.
- **The fold** (iOS 27.1 SDK, reported: `GeometryProxy.reservedRegions(kind:)`
  with `.division`/`.occlusion`, `onHingeChange`, vertical toolbars): keep
  controls out of an active division; tabletop mode for the terminal (§12) and
  the composer. **Verify every one of these names against the Xcode 27.1 SDK
  on CI before depending on it** (native/AGENTS.md).

## 16. Previews and linting for agents

An agent writing `native.js` in a tile terminal needs to see what it made:

- `bx lint --native [tile…]` — runs each `native.js` in headless chromium
  (the terminal rootfs has Playwright/chromium) against the live backend with
  the tile's frame token; reports errors, unknown primitives/props, raw values,
  which primitives need which app version, render time, tree size — and the
  **native coverage** of the workspace (which tiles have native UIs, which
  render cleanly). Without a browser it degrades to static checks.
- `bx preview --native <tile> [--dark] [--size 390x844] [--data d.json] --out
  shot.png` — the same run, drawn by the Lit reference renderer, screenshotted.
  `--data` replays a fixture instead of the live backend.
- `bx native tree <tile>` — prints the rendered tree JSON (diffable, cheapest).
- Pixel-exact iOS rendering comes from CI snapshots of the fixtures (§17), not
  from an agent's terminal.

## 17. Fixtures — the contract

`native/fixtures/<name>/`:

- `native.js` — the tile code under test (fixtures for each primitive, plus the
  tile rewrites of §18);
- `data.json` — a scripted `xbin` stub: HTTP responses by method + path, SSE
  frames, bus events, and pinned `now`, locale and time zone;
- `expected.json` — the rendered tree (`{"v":1,"root":…}`) after the script.

Every renderer is tested against `expected.json`: the runtime tests produce it
from `native.js` + `data.json` (node, xb-native's JSON target); the Lit
reference renderer and the SwiftUI renderer render it — light and dark, default
and a large Dynamic Type size — for screenshots and a montage contact sheet
(web vs iOS per fixture).

## 18. Eight tiles, rewritten

Each rewrite is faithful to today's `index.html`: same endpoints, same
behaviour, logic kept in JS. Where a module is shared with the page, the page
would import it too (a small, behaviour-preserving extraction). Trees are shown
for a sample state; `e` lists the events the tile listens to.

### 18.1 counter-go — the smallest round trip

```js
// examples/counter-go/native.js
import { html, render } from '/vendor/xb-native.js';

const api = (p, o) => xbin.fetch(`/api/${xbin.self}${p}`, o);
let count = null, busy = false;

async function load() {
  count = (await (await api('/count')).json()).count;
  paint();
}
async function inc() {
  busy = true; paint();
  try { await api('/count', { method: 'POST' }); await load(); }
  finally { busy = false; paint(); }
}
const paint = () => render(html`
  <screen title="Counter" style="form">
    <section>
      <row title="Count" detail=${count ?? '…'} mono="detail"/>
      <button role="primary" icon="plus" ?busy=${busy} @tap=${inc}>+1</button>
    </section>
  </screen>`);
load();
```

```json
{"v":1,"root":{"k":"r","t":"screen","p":{"title":"Counter","style":"form"},"c":[
  {"k":"r.0","t":"section","c":[
    {"k":"r.0.0","t":"row","p":{"title":"Count","detail":"42","mono":"detail"}},
    {"k":"r.0.1","t":"button","p":{"label":"+1","role":"primary","icon":"plus","busy":false},"e":["tap"]}
  ]}
]}}
```

### 18.2 calendar — a list, a form, bus-driven refresh

```js
// examples/calendar/native.js
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

const api = (p, o) => xbin.fetch(`/api/${xbin.self}${p}`, o);
let events = null, err = '';
const draft = { time: '', title: '' };

async function load() {
  try { events = (await (await api('/events')).json()).events; err = ''; }
  catch (e) { err = String(e.message ?? e); }
  paint();
}
async function add() {
  if (!draft.title.trim()) return;
  await api('/events', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    // same day the page sends (its UTC date — kept identical on purpose)
    body: JSON.stringify({ day: new Date().toISOString().slice(0, 10), time: draft.time, title: draft.title }),
  });
  draft.time = ''; draft.title = ''; paint();   // the bus event reloads the list
}
const set = (k) => (e) => { draft[k] = e.value; paint(); };
const paint = () => render(html`
  <screen title="Today" style="list">
    ${err ? html`<section><notice tone="danger" text=${err}/></section>` : nothing}
    <section title="today">
      ${events === null ? html`<progress label="loading…"/>`
        : events.length === 0 ? html`<empty title="nothing today"/>`
        : repeat(events, (e) => e.id, (e) => html`<row title=${e.title} detail=${e.time || '--:--'} mono="detail"/>`)}
    </section>
    <section title="new event">
      <field label="Time" placeholder="HH:MM" value=${draft.time} @input=${set('time')}/>
      <field label="Title" placeholder="new event" value=${draft.title} @input=${set('title')} @submit=${add}/>
      <button role="primary" ?disabled=${!draft.title.trim()} @tap=${add}>add</button>
    </section>
  </screen>`);

// own resources through xbin.self — never a hardcoded install path
xbin.bus.on(`res:${xbin.self}/bus/events/`, load);
load();
```

```json
{"v":1,"root":{"k":"r","t":"screen","p":{"title":"Today","style":"list"},"c":[
  {"k":"r.1","t":"section","p":{"title":"today"},"c":[
    {"k":"r.1.0:1","t":"row","p":{"title":"standup","detail":"09:30","mono":"detail"}},
    {"k":"r.1.0:2","t":"row","p":{"title":"lunch with Ana","detail":"--:--","mono":"detail"}}
  ]},
  {"k":"r.2","t":"section","p":{"title":"new event"},"c":[
    {"k":"r.2.0","t":"field","p":{"label":"Time","placeholder":"HH:MM","value":""},"e":["input"]},
    {"k":"r.2.1","t":"field","p":{"label":"Title","placeholder":"new event","value":""},"e":["input","submit"]},
    {"k":"r.2.2","t":"button","p":{"label":"add","role":"primary","disabled":true},"e":["tap"]}
  ]}
]}}
```

### 18.3 egress-approver — an approval queue

Same single-flight 1.2 s poll and optimistic moves as the page; whois becomes a
pushed screen. `fmt.js` (`fmtBytes`, `ago`) is extracted from the page and
imported by both. Because the runtime diffs, the page's "render only when the
signature changed" trick is unnecessary: an unchanged poll costs no bridge
traffic, a ticking "38s ago" costs one `set`.

```js
// builtin-tiles/egress-approver/native.js
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { fmtBytes, ago } from './fmt.js';

const api = (p, o) => xbin.fetch(`/api/${xbin.self}${p}`, o);
const post = (p, body) => api(p, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
const norm = (d) => { d.pending ??= []; d.approved ??= []; d.denied ??= []; return d; };
let st = { pending: [], approved: [], denied: [], clients: 0, egressReady: false };
let whois = null;          // {ip, lines} while the detail screen is open
const cache = {};          // ip -> whois lines (survives polls)

async function act(ip, action) {          // move locally at once, then reconcile
  const v = [...st.pending, ...st.approved, ...st.denied].find((x) => x.ip === ip) || { ip, rdns: '' };
  for (const k of ['pending', 'approved', 'denied']) st[k] = st[k].filter((x) => x.ip !== ip);
  if (action === 'approve') st.approved.unshift(v);
  else if (action === 'deny') st.denied.unshift(v);
  paint();
  try { await post(`/${action}`, { ip }); } catch { /* the next poll reconciles */ }
  refresh();
}
async function openWhois(ip) {
  whois = { ip, lines: cache[ip] ?? null }; paint();
  if (cache[ip]) return;
  try {
    const d = await (await api(`/detail?ip=${encodeURIComponent(ip)}`)).json();
    const w = d.rdap || {};
    cache[ip] = [['reverse dns', d.rdns || '(none)'], ['network', w.name], ['range', w.cidr],
      ['org', w.org], ['country', w.country], ['handle', w.handle], ['error', w.error]].filter(([, x]) => x);
  } catch { cache[ip] = [['error', 'lookup failed']]; }
  if (whois?.ip === ip) whois.lines = cache[ip];
  paint();
}

const pendingRow = (v) => html`
  <row title=${v.ip} mono="title" tone="warn" nav subtitle=${v.rdns || 'resolving…'}
       detail=${`↑${fmtBytes(v.bytesOut)} · ${ago(v.lastSeen)}`} @tap=${() => openWhois(v.ip)}>
    <actions>
      <button role="primary" icon="check" @tap=${() => act(v.ip, 'approve')}>approve</button>
      <button role="destructive" icon="xmark" @tap=${() => act(v.ip, 'deny')}>deny</button>
    </actions>
  </row>`;
const approvedRow = (v) => html`
  <row title=${v.ip} mono="title" tone="ok" subtitle=${v.rdns}
       detail=${`↑${fmtBytes(v.bytesOut)} ↓${fmtBytes(v.bytesIn)} · ${ago(v.lastSeen)}`}>
    <actions><button role="destructive" @tap=${() => act(v.ip, 'forget')}>revoke</button></actions>
  </row>`;
const deniedRow = (v) => html`
  <row title=${v.ip} mono="title" subtitle=${v.rdns}>
    <actions><button @tap=${() => act(v.ip, 'forget')}>un-deny</button></actions>
  </row>`;

const main = () => html`
  <screen title="Egress Approver" style="list"
          subtitle=${`${st.clients ?? 0} client${st.clients === 1 ? '' : 's'} · egress ${st.egressReady ? 'ready' : 'unbound'}`}>
    <section title="pending" badge=${String(st.pending.length)}>
      ${st.pending.length ? repeat(st.pending, (v) => v.ip, pendingRow)
        : html`<empty text=${st.clients ? 'no new destinations — all quiet' : 'bind a component’s net to this tile to start gating'}/>`}
    </section>
    <section title="approved" badge=${String(st.approved.length)}>
      ${st.approved.length ? repeat(st.approved, (v) => v.ip, approvedRow) : html`<empty text="nothing approved yet"/>`}
    </section>
    ${st.denied.length ? html`<section title="denied" collapsible>${repeat(st.denied, (v) => v.ip, deniedRow)}</section>` : nothing}
  </screen>`;
const whoisScreen = (w) => html`
  <screen title=${w.ip} style="list">
    <section title="whois">
      ${w.lines ? w.lines.map(([k, x]) => html`<row title=${k} detail=${String(x)} mono="detail"/>`)
        : html`<progress label="looking up…"/>`}
    </section>
  </screen>`;
const paint = () => render(html`
  <nav @pop=${() => { whois = null; paint(); }}>
    ${main()}
    ${whois ? whoisScreen(whois) : nothing}
  </nav>`);

let inFlight = false;       // single flight, as the page
async function refresh() {
  if (inFlight) return;
  inFlight = true;
  try { const r = await api('/state'); if (r.ok) { st = norm(await r.json()); paint(); } }
  catch { /* transient; next tick */ } finally { inFlight = false; }
}
(function loop() {
  refresh().finally(() => setTimeout(loop, document.visibilityState === 'visible' ? 1200 : 5000));
})();
```

```json
{"v":1,"root":{"k":"r","t":"nav","e":["pop"],"c":[
  {"k":"r.0","t":"screen","p":{"title":"Egress Approver","style":"list","subtitle":"2 clients · egress ready"},"c":[
    {"k":"r.0.0","t":"section","p":{"title":"pending","badge":"1"},"c":[
      {"k":"r.0.0.0:203.0.113.7","t":"row","p":{"title":"203.0.113.7","mono":"title","tone":"warn","nav":true,"subtitle":"cdn.example.net","detail":"↑1.5K · 38s ago"},"e":["tap"],"c":[
        {"k":"r.0.0.0:203.0.113.7.0","t":"actions","c":[
          {"k":"r.0.0.0:203.0.113.7.0.0","t":"button","p":{"label":"approve","role":"primary","icon":"check"},"e":["tap"]},
          {"k":"r.0.0.0:203.0.113.7.0.1","t":"button","p":{"label":"deny","role":"destructive","icon":"xmark"},"e":["tap"]}
        ]}
      ]}
    ]},
    {"k":"r.0.1","t":"section","p":{"title":"approved","badge":"1"},"c":[
      {"k":"r.0.1.0:198.51.100.20","t":"row","p":{"title":"198.51.100.20","mono":"title","tone":"ok","subtitle":"api.github.com","detail":"↑47.1K ↓1.0M · 2m ago"},"c":[
        {"k":"r.0.1.0:198.51.100.20.0","t":"actions","c":[
          {"k":"r.0.1.0:198.51.100.20.0.0","t":"button","p":{"label":"revoke","role":"destructive"},"e":["tap"]}
        ]}
      ]}
    ]}
  ]}
]}}
```

### 18.4 s3-archiver — a settings form with a secret

Same calls and order as the page: `PUT /config`, then the two secrets, then
reload, then `POST /check`. Note: the page writes the secrets to
`/api/xbin/vault/<self>/…` straight from the frame, and D30 makes the vault a
backend-only store — that write should move behind a backend endpoint (as
llm-gw v5 did); the native view simply follows whatever the page does.

```js
// builtin-tiles/s3-archiver/native.js
import { html, render, nothing } from '/vendor/xb-native.js';

const self = xbin.self;
const api = (p, o) => xbin.fetch(`/api/${self}${p}`, o);
const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
const setSecret = (key, value) => xbin.fetch(`/api/xbin/vault/${self}/${encodeURIComponent(key)}`, json('PUT', { value }));
const cfg = { endpoint: '', region: '', bucket: '', prefix: '' };
let hasCreds = false, accessKey = '', secretKey = '', busy = '', msg = null;   // msg: {tone, text}

async function load() {
  try { const d = await (await api('/config')).json(); Object.assign(cfg, d.config || {}); hasCreds = !!d.hasCreds; }
  catch { /* first load / not admin */ }
  paint();
}
async function save() {
  busy = 'save'; msg = { tone: 'info', text: 'Saving…' }; paint();
  try {
    const r = await api('/config', json('PUT', { endpoint: cfg.endpoint.trim(), region: cfg.region.trim(),
      bucket: cfg.bucket.trim(), prefix: cfg.prefix.trim() }));
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'save failed');
    if (accessKey.trim() && !(await setSecret('accessKey', accessKey.trim())).ok) throw new Error('could not store access key in the vault');
    if (secretKey && !(await setSecret('secretKey', secretKey)).ok) throw new Error('could not store secret key in the vault');
    secretKey = '';
    await load();
    await check();          // saving verifies it actually reaches the bucket
  } catch (e) { msg = { tone: 'danger', text: String(e.message ?? e) }; }
  finally { busy = ''; paint(); }
}
async function check() {
  msg = { tone: 'info', text: 'Checking connection…' }; paint();
  try {
    const r = await api('/check', { method: 'POST' });
    const d = await r.json().catch(() => ({}));
    msg = r.ok ? { tone: 'ok', text: `Connected — bucket “${d.bucket}” is reachable.` }
      : { tone: 'danger', text: d.error || `Connection failed (${r.status}).` };
  } catch (e) { msg = { tone: 'danger', text: 'Connection check failed: ' + (e.message ?? e) }; }
  paint();
}
const test = async () => { busy = 'test'; await check(); busy = ''; paint(); };
const set = (k) => (e) => { cfg[k] = e.value; paint(); };
const paint = () => render(html`
  <screen title="S3 Archiver" style="form">
    <section footer="Where component backups are stored: any S3-compatible endpoint (AWS, MinIO, R2, B2). Needs egress — bind this tile's net interface.">
      <field label="Endpoint" kind="url" placeholder="https://s3.us-east-1.amazonaws.com" value=${cfg.endpoint} @input=${set('endpoint')}/>
      <field label="Region" placeholder="us-east-1" value=${cfg.region} @input=${set('region')}/>
      <field label="Bucket" placeholder="my-backups" value=${cfg.bucket} @input=${set('bucket')}/>
      <field label="Prefix (optional)" placeholder="xbin/" value=${cfg.prefix} @input=${set('prefix')}/>
    </section>
    <section title="Credentials" footer=${hasCreds ? 'Secret access key · set' : 'Secret access key · not set'}>
      <field label="Access key ID" value=${accessKey} @input=${(e) => { accessKey = e.value; paint(); }}/>
      <field label="Secret access key" kind="secure" placeholder="leave blank to keep" value=${secretKey} @input=${(e) => { secretKey = e.value; paint(); }}/>
    </section>
    <section>
      ${msg ? html`<notice tone=${msg.tone} text=${msg.text}/>` : nothing}
      <button role="primary" ?busy=${busy === 'save'} ?disabled=${!!busy} @tap=${save}>Save & test</button>
      <button ?busy=${busy === 'test'} ?disabled=${!!busy} @tap=${test}>Test connection</button>
    </section>
  </screen>`);
load();
```

```json
{"v":1,"root":{"k":"r","t":"screen","p":{"title":"S3 Archiver","style":"form"},"c":[
  {"k":"r.0","t":"section","p":{"footer":"Where component backups are stored: any S3-compatible endpoint (AWS, MinIO, R2, B2). Needs egress — bind this tile's net interface."},"c":[
    {"k":"r.0.0","t":"field","p":{"label":"Endpoint","kind":"url","placeholder":"https://s3.us-east-1.amazonaws.com","value":"https://s3.eu-central-1.amazonaws.com"},"e":["input"]},
    {"k":"r.0.1","t":"field","p":{"label":"Region","placeholder":"us-east-1","value":"eu-central-1"},"e":["input"]},
    {"k":"r.0.2","t":"field","p":{"label":"Bucket","placeholder":"my-backups","value":"acme-backups"},"e":["input"]},
    {"k":"r.0.3","t":"field","p":{"label":"Prefix (optional)","placeholder":"xbin/","value":"xbin/"},"e":["input"]}
  ]},
  {"k":"r.1","t":"section","p":{"title":"Credentials","footer":"Secret access key · set"},"c":[
    {"k":"r.1.0","t":"field","p":{"label":"Access key ID","value":""},"e":["input"]},
    {"k":"r.1.1","t":"field","p":{"label":"Secret access key","kind":"secure","placeholder":"leave blank to keep","value":""},"e":["input"]}
  ]},
  {"k":"r.2","t":"section","c":[
    {"k":"r.2.0","t":"notice","p":{"tone":"ok","text":"Connected — bucket “acme-backups” is reachable."}},
    {"k":"r.2.1","t":"button","p":{"label":"Save & test","role":"primary","busy":false,"disabled":false},"e":["tap"]},
    {"k":"r.2.2","t":"button","p":{"label":"Test connection","busy":false,"disabled":false},"e":["tap"]}
  ]}
]}}
```

### 18.5 webhooks — list, detail, a reveal-once sheet, conditional fields

The page's cards become a list with a detail screen per hook (a phone-first
arrangement of the same actions); the reveal-once secret becomes a sheet.

```js
// builtin-tiles/webhooks/native.js
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi as api, jbody } from '/vendor/bx-kit.js';

const self = xbin.self;
let st = null, err = '', shown = null, open = null;    // open: hook id on the detail screen
const draft = { name: '', auth: 'token', preset: '', agent: '' };
const PRESETS = {
  '': { eventIdFrom: '', topicFrom: '' },
  github: { auth: 'hmac', eventIdFrom: 'header:X-GitHub-Delivery', topicFrom: 'header:X-GitHub-Event' },
  gitlab: { auth: 'token', eventIdFrom: 'header:X-Gitlab-Event-UUID', topicFrom: 'json:object_kind' },
};
async function load() { try { st = await api('/hooks'); err = ''; } catch (e) { err = e.message; } paint(); }
async function act(fn) { err = ''; try { await fn(); } catch (e) { err = e.message; } await load(); }
function create() {
  const p = PRESETS[draft.preset] || {};
  const body = { name: draft.name.trim(), auth: p.auth || draft.auth, agent: draft.agent, eventIdFrom: p.eventIdFrom, topicFrom: p.topicFrom };
  return act(async () => { const r = await api('/hooks', jbody(body, 'POST')); shown = { id: r.hook.id, secret: r.secret, auth: r.hook.auth }; draft.name = ''; });
}
const toggle = (h) => act(() => api(`/hooks/${h.id}`, jbody({ enabled: !h.enabled }, 'PUT')));
const rotate = (h) => act(async () => { const r = await api(`/hooks/${h.id}/rotate`, { method: 'POST' }); shown = { id: h.id, secret: r.secret, auth: h.auth }; });
const del = (h) => act(async () => { await api(`/hooks/${h.id}`, { method: 'DELETE' }); open = null; });
const hookURL = (h) => `${st.host ? `https://${st.host}` : 'https://<your hooks host>'}/hook/${h.id}`;
const ago = (s) => { const d = Math.max(0, Date.now() / 1000 - s); return d < 90 ? 'just now' : d < 5400 ? `${Math.round(d / 60)} min ago` : `${Math.round(d / 3600)} h ago`; };
const tone = (status) => ({ 2: 'ok', 4: 'warn', 5: 'danger' })[String(status)[0]] || 'muted';
const set = (k) => (e) => { draft[k] = e.value; paint(); };

const main = () => html`
  <screen title="Webhooks" style="list">
    ${err ? html`<section><notice tone="danger" text=${err}/></section>` : nothing}
    <section title="Wiring">
      ${st.agents?.length
        ? html`<row title="Pushes to" detail=${st.agents.map((a) => a.provider).join(', ')}/>`
        : html`<notice tone="warn" text="Not bound to an agent yet"/><code copy text=${`bx bind ${self} agents=apps/agent`}/>`}
      ${st.host ? html`<row title="Public at" detail=${st.host} mono="detail"/>`
        : html`<code copy text=${`bx expose ${self} hooks=apps/traefik --host hooks.example.com`}/>`}
    </section>
    <section title="Hooks">
      ${st.hooks?.length ? repeat(st.hooks, (h) => h.id, (h) => html`
          <row title=${h.name} nav icon="link" badge=${h.enabled ? '' : 'off'}
               subtitle=${`${h.auth === 'hmac' ? 'signed (HMAC)' : 'token'}${h.agent ? ` · to ${h.agent}` : ''}`}
               @tap=${() => { open = h.id; paint(); }}/>`)
        : html`<empty text="none yet"/>`}
    </section>
    <section title="New hook">
      <field label="Name (its events' topic)" placeholder="deploy" value=${draft.name} @input=${set('name')}/>
      <picker label="Sent by" value=${draft.preset} @change=${set('preset')}
              options=${[{ value: '', label: 'anything (a token)' }, { value: 'github', label: 'GitHub (signed)' }, { value: 'gitlab', label: 'GitLab (a token)' }]}/>
      ${draft.preset === '' ? html`<picker label="Checked by" value=${draft.auth} @change=${set('auth')}
              options=${[{ value: 'token', label: 'a token' }, { value: 'hmac', label: 'an HMAC signature (X-Hub-Signature-256)' }]}/>` : nothing}
      ${(st.agents?.length ?? 0) > 1 ? html`<picker label="To" value=${draft.agent} @change=${set('agent')}
              options=${[{ value: '', label: 'every bound agent' }, ...st.agents.map((a) => ({ value: a.provider, label: a.provider }))]}/>` : nothing}
      <button role="primary" ?disabled=${!draft.name.trim()} @tap=${create}>Create</button>
    </section>
    <section title="Recent deliveries">
      ${st.deliveries?.length ? repeat(st.deliveries.slice().reverse(), (x) => `${x.at}:${x.hook}`, (x) => html`
          <row title=${x.topic || x.hook} mono="title" subtitle=${x.result} detail=${ago(x.at)}
               badge=${String(x.status)} tone=${tone(x.status)}/>`)
        : html`<empty text="nothing yet"/>`}
    </section>
  </screen>`;
const detail = (h) => html`
  <screen title=${h.name} style="form">
    <section>
      <toggle label="Enabled" value=${h.enabled} @change=${() => toggle(h)}/>
      <code copy text=${hookURL(h)}/>
    </section>
    <section>
      <button icon="key" confirm=${{ title: 'Make a new secret?', message: 'The old one stops working at once.', label: 'New secret' }}
              @tap=${() => rotate(h)}>New secret</button>
      <button role="destructive" icon="trash" confirm=${{ title: `Remove the hook "${h.name}"?`, message: 'Its URL stops working.', label: 'Remove', destructive: true }}
              @tap=${() => del(h)}>Remove</button>
    </section>
  </screen>`;
const secret = () => html`
  <sheet open=${!!shown} title="Copy this secret now" @dismiss=${() => { shown = null; paint(); }}>
    <text>It is not shown again.</text>
    <code copy text=${shown?.secret ?? ''}/>
    <text tone="muted">${shown?.auth === 'token' ? 'Send it as ?token=… or Authorization: Bearer …'
      : 'Use it as the webhook secret: the sender signs each body with it (GitHub: Secret).'}</text>
    <button role="primary" @tap=${() => { shown = null; paint(); }}>Done</button>
  </sheet>`;
const paint = () => render(st ? html`
  <nav @pop=${() => { open = null; paint(); }}>
    ${main()}
    ${open && st.hooks?.find((x) => x.id === open) ? detail(st.hooks.find((x) => x.id === open)) : nothing}
  </nav>
  ${secret()}` : html`<screen title="Webhooks">${err ? html`<notice tone="danger" text=${err}/>` : html`<progress label="loading…"/>`}</screen>`);
load();
```

(The page renders the secret text inline; a sheet is its phone-first form.
The tree below is the main screen of a workspace with one GitHub hook and one
delivery; `r.1` is the closed secret sheet.)

```json
{"v":1,"root":{"k":"r","t":"fragment","c":[
  {"k":"r.0","t":"nav","e":["pop"],"c":[
    {"k":"r.0.0","t":"screen","p":{"title":"Webhooks","style":"list"},"c":[
      {"k":"r.0.0.1","t":"section","p":{"title":"Wiring"},"c":[
        {"k":"r.0.0.1.0","t":"row","p":{"title":"Pushes to","detail":"apps/agent"}},
        {"k":"r.0.0.1.1","t":"row","p":{"title":"Public at","detail":"hooks.example.com","mono":"detail"}}
      ]},
      {"k":"r.0.0.2","t":"section","p":{"title":"Hooks"},"c":[
        {"k":"r.0.0.2.0:h7","t":"row","p":{"title":"deploy","nav":true,"icon":"link","badge":"","subtitle":"signed (HMAC)"},"e":["tap"]}
      ]},
      {"k":"r.0.0.3","t":"section","p":{"title":"New hook"},"c":[
        {"k":"r.0.0.3.0","t":"field","p":{"label":"Name (its events' topic)","placeholder":"deploy","value":""},"e":["input"]},
        {"k":"r.0.0.3.1","t":"picker","p":{"label":"Sent by","value":"","options":[{"value":"","label":"anything (a token)"},{"value":"github","label":"GitHub (signed)"},{"value":"gitlab","label":"GitLab (a token)"}]},"e":["change"]},
        {"k":"r.0.0.3.2","t":"picker","p":{"label":"Checked by","value":"token","options":[{"value":"token","label":"a token"},{"value":"hmac","label":"an HMAC signature (X-Hub-Signature-256)"}]},"e":["change"]},
        {"k":"r.0.0.3.4","t":"button","p":{"label":"Create","role":"primary","disabled":true},"e":["tap"]}
      ]},
      {"k":"r.0.0.4","t":"section","p":{"title":"Recent deliveries"},"c":[
        {"k":"r.0.0.4.0:1790000000:h7","t":"row","p":{"title":"deploy/push","mono":"title","subtitle":"trigger deploy ran","detail":"5 min ago","badge":"202","tone":"ok"}}
      ]}
    ]}
  ]},
  {"k":"r.1","t":"sheet","p":{"open":false,"title":"Copy this secret now"},"e":["dismiss"],"c":[
    {"k":"r.1.0","t":"text","p":{"text":"It is not shown again."}},
    {"k":"r.1.1","t":"code","p":{"copy":true,"text":""}},
    {"k":"r.1.2","t":"text","p":{"tone":"muted","text":"Use it as the webhook secret: the sender signs each body with it (GitHub: Secret)."}},
    {"k":"r.1.3","t":"button","p":{"label":"Done","role":"primary"},"e":["tap"]}
  ]}
]}}
```

(A template with two top-level elements renders as a `fragment` root — the
renderer lays the sheet over the navigation stack.)

### 18.6 devbox — tabs, swipe actions, a detail screen

```js
// builtin-tiles/devbox/native.js
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

const self = xbin.self;
const api = async (p, opt) => {
  const r = await xbin.fetch(`/api/${self}${p}`, opt);
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || (await r.text()) || r.status);
  return r.status === 204 ? null : r.json();
};
let s = null, err = '', tab = 'containers', open = null, sheet = '';
const draft = { name: '', image: '', key: '' };

async function refresh() {
  try { s = await api('/state'); err = ''; } catch (e) { err = String(e.message ?? e); }
  paint();
}
async function act(path, method, body) {
  try { await api(path, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined }); await refresh(); return true; }
  catch (e) { err = String(e.message ?? e); paint(); return false; }
}
const create = async () => { if (await act('/containers', 'POST', { name: draft.name.trim(), image: draft.image.trim() })) { draft.name = draft.image = ''; sheet = ''; paint(); } };
const addKey = async () => { if (await act('/keys', 'POST', { key: draft.key.trim() })) { draft.key = ''; sheet = ''; paint(); } };
const running = (c) => c.state === 'running';
const sshCmd = (c) => `ssh -p<host-port> ${c.name}@<xbin-host>`;
const set = (k) => (e) => { draft[k] = e.value; paint(); };

const containerRow = (c) => html`
  <row title=${c.name} mono="title" nav icon="box" subtitle=${c.image}
       badge=${c.status || c.state} tone=${running(c) ? 'ok' : 'muted'} @tap=${() => { open = c.name; paint(); }}>
    <actions>
      <button icon=${running(c) ? 'stop' : 'play'} @tap=${() => act(`/containers/${encodeURIComponent(c.name)}/${running(c) ? 'stop' : 'start'}`, 'POST')}>${running(c) ? 'stop' : 'start'}</button>
      <button role="destructive" icon="trash" confirm=${{ title: `Remove container ${c.name}?`, message: 'Its filesystem is lost.', label: 'Remove', destructive: true }}
              @tap=${() => act(`/containers/${encodeURIComponent(c.name)}`, 'DELETE')}>remove</button>
    </actions>
  </row>`;
const main = () => html`
  <screen title="Devbox" style="list" refreshable @refresh=${refresh}>
    <toolbar><button icon="plus" @tap=${() => { sheet = tab === 'keys' ? 'key' : 'container'; paint(); }}>Add</button></toolbar>
    ${err || s?.error ? html`<section><notice tone="danger" text=${err || s.error}/></section>` : nothing}
    <tabs selected=${tab} @change=${(e) => { tab = e.key; paint(); }}>
      <tab key="containers" title="Containers" icon="box">
        <section footer=${s?.podmanVersion ? `podman ${s.podmanVersion} · storage persisted` : ''}>
          ${s?.containers?.length ? repeat(s.containers, (c) => c.name, containerRow) : html`<empty text="no containers yet"/>`}
        </section>
        <section title="SSH">
          ${s?.sshError ? html`<notice tone="danger" text=${`ssh proxy: ${s.sshError}`}/>`
            : html`<text tone="muted">Publish the SSH port to a host port, then connect:</text>
                   <code copy text=${`bx expose ${self} ssh=runtime --listen ${s?.sshPort ?? 2222}`}/>`}
        </section>
      </tab>
      <tab key="keys" title="SSH keys" icon="key">
        <section footer="The SSH proxy is default-deny — nobody can connect until you add a public key here.">
          ${s?.keys?.length ? repeat(s.keys, (k) => k.fingerprint, (k) => html`
              <row title=${k.comment || k.type} subtitle=${k.fingerprint} mono="subtitle" detail=${k.type}>
                <actions><button role="destructive" @tap=${() => act(`/keys/${encodeURIComponent(k.fingerprint)}`, 'DELETE')}>remove</button></actions>
              </row>`) : html`<empty text="no keys — SSH is closed"/>`}
        </section>
      </tab>
    </tabs>
  </screen>`;
const detail = (c) => html`
  <screen title=${c.name} style="form">
    <section>
      <row title="Image" detail=${c.image} mono="detail"/>
      <row title="State" badge=${c.status || c.state} tone=${running(c) ? 'ok' : 'muted'}/>
    </section>
    <section title="Connect"><code copy text=${sshCmd(c)}/></section>
  </screen>`;
const sheets = () => html`
  <sheet open=${sheet === 'container'} title="New container" @dismiss=${() => { sheet = ''; paint(); }}>
    <field label="Name (→ ssh user)" value=${draft.name} @input=${set('name')}/>
    <field label="Image" placeholder="docker.io/library/ubuntu:24.04" value=${draft.image} @input=${set('image')}/>
    <button role="primary" ?disabled=${!draft.name.trim() || !draft.image.trim()} @tap=${create}>create</button>
  </sheet>
  <sheet open=${sheet === 'key'} title="Add SSH key" @dismiss=${() => { sheet = ''; paint(); }}>
    <field kind="multiline" label="Public key" placeholder="ssh-ed25519 AAAA… you@host" value=${draft.key} @input=${set('key')}/>
    <button role="primary" ?disabled=${!draft.key.trim()} @tap=${addKey}>add key</button>
  </sheet>`;
const paint = () => render(html`
  <nav @pop=${() => { open = null; paint(); }}>
    ${main()}
    ${open && s?.containers?.find((c) => c.name === open) ? detail(s.containers.find((c) => c.name === open)) : nothing}
  </nav>
  ${sheets()}`);

refresh();
setInterval(() => { if (document.visibilityState === 'visible') refresh(); }, 5000);
```

(The containers tab of a workspace with one running container; sheets closed
and trimmed from the tree for brevity.)

```json
{"v":1,"root":{"k":"r","t":"fragment","c":[
  {"k":"r.0","t":"nav","e":["pop"],"c":[
    {"k":"r.0.0","t":"screen","p":{"title":"Devbox","style":"list","refreshable":true},"e":["refresh"],"c":[
      {"k":"r.0.0.0","t":"toolbar","c":[
        {"k":"r.0.0.0.0","t":"button","p":{"label":"Add","icon":"plus"},"e":["tap"]}
      ]},
      {"k":"r.0.0.2","t":"tabs","p":{"selected":"containers"},"e":["change"],"c":[
        {"k":"r.0.0.2.0","t":"tab","p":{"key":"containers","title":"Containers","icon":"box"},"c":[
          {"k":"r.0.0.2.0.0","t":"section","p":{"footer":"podman 5.2.1 · storage persisted"},"c":[
            {"k":"r.0.0.2.0.0.0:dev","t":"row","p":{"title":"dev","mono":"title","nav":true,"icon":"box","subtitle":"docker.io/library/ubuntu:24.04","badge":"Up 3 hours","tone":"ok"},"e":["tap"],"c":[
              {"k":"r.0.0.2.0.0.0:dev.0","t":"actions","c":[
                {"k":"r.0.0.2.0.0.0:dev.0.0","t":"button","p":{"label":"stop","icon":"stop"},"e":["tap"]},
                {"k":"r.0.0.2.0.0.0:dev.0.1","t":"button","p":{"label":"remove","role":"destructive","icon":"trash","confirm":{"title":"Remove container dev?","message":"Its filesystem is lost.","label":"Remove","destructive":true}},"e":["tap"]}
              ]}
            ]}
          ]},
          {"k":"r.0.0.2.0.1","t":"section","p":{"title":"SSH"},"c":[
            {"k":"r.0.0.2.0.1.0.0","t":"text","p":{"tone":"muted","text":"Publish the SSH port to a host port, then connect:"}},
            {"k":"r.0.0.2.0.1.0.1","t":"code","p":{"copy":true,"text":"bx expose apps/devbox ssh=runtime --listen 2222"}}
          ]}
        ]},
        {"k":"r.0.0.2.1","t":"tab","p":{"key":"keys","title":"SSH keys","icon":"key"},"c":[]}
      ]}
    ]}
  ]}
]}}
```

(An unselected tab's content is not materialized; it mounts when selected.)

### 18.7 prometheus-viewer — logic stays in JS, charts go native

The page's Prometheus parser, rate math and formatters move into `prom.js`,
imported by both views; scraping keeps running in the tile's frontend exactly as
today (no backend added). Sparklines become `chart kind="spark"`.

```js
// builtin-tiles/prometheus-viewer/native.js
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { parseProm, rateOf, fmtN, fmtRate, labelStr } from './prom.js';

const asEndpoints = (i) => i?.endpoints ?? (i?.url ? [{ url: i.url, provider: i.service }] : []);
const ENDPOINTS = asEndpoints(xbin.iface('sources'));
const epLabel = (e) => (e.instance ? `${e.provider}#${e.instance}` : e.provider);
const POLL_MS = 3000, HIST = 30;
const hist = new Map();                      // series key -> [{t, v}]
let sources = ENDPOINTS.map(() => ({ error: '', metrics: null })), live = false;

async function scrape() {
  const now = Date.now();
  sources = await Promise.all(ENDPOINTS.map(async (e) => {
    try {
      const r = await xbin.fetch(`${e.url}/metrics`);
      if (!r.ok) throw new Error(`${r.status}`);
      const metrics = parseProm(await r.text());
      for (const m of metrics.values()) for (const s of m.samples) {
        const h = hist.get(s.key) ?? []; h.push({ t: now, v: s.value }); if (h.length > HIST) h.shift(); hist.set(s.key, h);
      }
      return { error: '', metrics };
    } catch (err) { return { error: String(err.message ?? err), metrics: null }; }
  }));
  live = sources.some((s) => s.metrics);
  paint();
}
const series = (m, s) => {
  const h = hist.get(s.key) ?? [];
  const rate = m.type === 'counter' ? rateOf(h) : null;
  return html`
    <row title=${labelStr(s.labels) || m.name} mono="all"
         detail=${`${fmtN(s.value)}${rate != null ? ` · ${fmtRate(rate)}/s` : ''}`}>
      <chart kind="spark" series=${[{ name: s.key, points: h.map((p) => [p.t, p.v]) }]} x="time"/>
    </row>`;
};
const paint = () => render(html`
  <screen title="Prometheus" style="list">
    <toolbar><badge tone=${live ? 'ok' : 'muted'} ?pulse=${live}>${live ? 'live · every 3s' : 'idle'}</badge></toolbar>
    ${ENDPOINTS.length === 0 ? html`<empty icon="chart" title="No Prometheus source bound"
        text="Bind one or more components exporting /metrics to this tile's sources interface."/>`
      : repeat(ENDPOINTS, (e) => e.url, (e, i) => html`
        <section title=${epLabel(e)} footer=${e.url}>
          ${sources[i].error ? html`<notice tone="danger" text=${`⚠ ${sources[i].error}`}/>`
            : !sources[i].metrics ? html`<progress label="scraping…"/>`
            : sources[i].metrics.size === 0 ? html`<empty text="no metrics exported"/>`
            : repeat([...sources[i].metrics.values()], (m) => m.name, (m) => html`
                <disclosure title=${m.name}>
                  <row title=${m.name} mono="title" subtitle=${m.help} badge=${m.type}
                       tone=${m.type === 'counter' ? 'accent' : m.type === 'gauge' ? 'ok' : 'muted'}/>
                  ${repeat(m.samples, (s) => s.key, (s) => series(m, s))}
                </disclosure>`)}
        </section>`)}
  </screen>`);

(function loop() {
  scrape().finally(() => setTimeout(loop, document.visibilityState === 'visible' ? POLL_MS : POLL_MS * 5));
})();
```

```json
{"v":1,"root":{"k":"r","t":"screen","p":{"title":"Prometheus","style":"list"},"c":[
  {"k":"r.0","t":"toolbar","c":[
    {"k":"r.0.0","t":"badge","p":{"tone":"ok","pulse":true,"text":"live · every 3s"}}
  ]},
  {"k":"r.1:https://node.example/api/apps/node-exporter","t":"section","p":{"title":"apps/node-exporter","footer":"https://node.example/api/apps/node-exporter"},"c":[
    {"k":"r.1:https://node.example/api/apps/node-exporter.0:process_cpu_seconds_total","t":"disclosure","p":{"title":"process_cpu_seconds_total"},"c":[
      {"k":"r.1:https://node.example/api/apps/node-exporter.0:process_cpu_seconds_total.0","t":"row","p":{"title":"process_cpu_seconds_total","mono":"title","subtitle":"Total user and system CPU time spent in seconds.","badge":"counter","tone":"accent"}},
      {"k":"r.1:https://node.example/api/apps/node-exporter.0:process_cpu_seconds_total.1:process_cpu_seconds_total{}","t":"row","p":{"title":"process_cpu_seconds_total","mono":"all","detail":"12.34 · 0.021/s"},"c":[
        {"k":"r.1:https://node.example/api/apps/node-exporter.0:process_cpu_seconds_total.1:process_cpu_seconds_total{}.0","t":"chart","p":{"kind":"spark","x":"time","series":[{"name":"process_cpu_seconds_total{}","points":[[1790000000000,12.28],[1790000003000,12.3],[1790000006000,12.34]]}]}}
      ]}
    ]}
  ]}
]}}
```

### 18.8 chat — the agent loop stays in JS; the transcript goes native

The chat tile's whole engine (models from each bound `llm` endpoint, the MCP
JSON-RPC client, tool routing, SSE parsing, the up-to-8-round agent loop,
`<think>` splitting) moves from inline page script into `chat-core.js`, an
event-emitting class both views import. The page renders bubbles from its
events; `native.js` renders the chat family (§8.4).

```js
// builtin-tiles/chat/native.js
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { Chat } from './chat-core.js';   // extracted from index.html, no DOM

const chat = new Chat({ llm: xbin.iface('llm'), mcp: xbin.iface('mcp') });
let draft = '';
chat.addEventListener('change', () => paint());   // any turn/tool/stream update

const turn = (m) => {
  if (m.role === 'user') return html`<message role="user" text=${m.text}/>`;
  if (m.role === 'tool') return html`
    <toolcard title=${m.label} icon="wrench" family="tool" state=${m.state}>
      <code text=${m.args}/>
      ${m.result != null ? html`<code text=${m.result}/>` : nothing}
    </toolcard>`;
  return html`
    ${m.think ? html`<thinking text=${m.think} ?live=${m.thinking} seconds=${m.thinkSecs}/>` : nothing}
    <message role="assistant" markdown text=${m.text} ?streaming=${m.streaming}/>`;
};
const paint = () => render(html`
  <screen title="Chat" style="scroll">
    <toolbar>
      <picker style="menu" value=${chat.model} options=${chat.models} @change=${(e) => chat.setModel(e.value)}/>
      ${chat.tools.length ? html`<badge tone="muted">${`${chat.tools.length} tools`}</badge>` : nothing}
      <button icon="plus" @tap=${() => chat.reset()}>New chat</button>
    </toolbar>
    <transcript follow>
      ${repeat(chat.history, (m) => m.id, turn)}
      ${chat.note ? html`<step glyph="!" tone="warn" text=${chat.note}/>` : nothing}
    </transcript>
    <composer value=${draft} placeholder="Message" ?busy=${chat.busy}
              @input=${(e) => { draft = e.value; paint(); }}
              @send=${(e) => { chat.send(e.value); draft = ''; paint(); }}
              @stop=${() => chat.abort()}/>
  </screen>`);
chat.loadModels().then(() => chat.loadTools()).finally(paint);
```

```json
{"v":1,"root":{"k":"r","t":"screen","p":{"title":"Chat","style":"scroll"},"c":[
  {"k":"r.0","t":"toolbar","c":[
    {"k":"r.0.0","t":"picker","p":{"style":"menu","value":"0 qwen3-32b","options":[{"value":"0 qwen3-32b","label":"qwen3-32b"}]},"e":["change"]},
    {"k":"r.0.1","t":"badge","p":{"tone":"muted","text":"3 tools"}},
    {"k":"r.0.2","t":"button","p":{"label":"New chat","icon":"plus"},"e":["tap"]}
  ]},
  {"k":"r.1","t":"transcript","p":{"follow":true},"c":[
    {"k":"r.1.0:1","t":"message","p":{"role":"user","text":"How many open issues are labelled bug?"}},
    {"k":"r.1.0:2","t":"toolcard","p":{"title":"apps/github-mcp · search_issues","icon":"wrench","family":"tool","state":"ok"},"c":[
      {"k":"r.1.0:2.0","t":"code","p":{"text":"{\"q\":\"is:open label:bug\"}"}},
      {"k":"r.1.0:2.1","t":"code","p":{"text":"{\"total_count\":7}"}}
    ]},
    {"k":"r.1.0:3.0","t":"thinking","p":{"text":"The tool says 7.","live":false,"seconds":2}},
    {"k":"r.1.0:3.1","t":"message","p":{"role":"assistant","markdown":true,"text":"There are **7** open issues labelled `bug`.","streaming":false}}
  ]},
  {"k":"r.2","t":"composer","p":{"value":"","placeholder":"Message","busy":false},"e":["input","send","stop"]}
]}}
```

(In the tree the markdown message carries `text`; on the wire the runtime adds
its `tokens` — omitted here.)

## 19. The builder docs (to write when the runtime lands)

`workspace-template/AGENTS.md` gains a **"Native app UI"** section, and
docs/ gains `native.md` (the reference). Written in the same change that ships
the runtime — docs describe what exists. Outline:

1. **When** — every tile works in the app as its web page; add `native.js` when
   the tile is used on phones and a list/form/chat UI would serve better than a
   shrunken web page. Complex visual tiles stay web.
2. **How** — `native.js` beside `xbin.json`; `html`/`render` from
   `/vendor/xb-native.js`; share logic with `index.html` by importing the same
   modules; the vocabulary (link to docs/native.md); tokens, not raw values.
3. **Rules** — keep logic in shared modules, views thin; no device APIs
   exist; images from tile-relative URLs; `field kind="secure"` for secrets.
4. **Check it** — `bx lint --native`, `bx preview --native … --out shot.png`
   (look at the picture), `bx native tree`.
5. **Fallback** — anything unsupported shows the web page; test both.

## 20. Server additions (all additive)

| # | Change | Track |
|---|---|---|
| 1 | Device enrollment, challenge login, bearer device sessions, Devices UI (user + admin) | A |
| 2 | Frame tokens bound to the minting session/generation (web too) | S |
| 3 | Strict tile asset gating for browsers (plans/tile-asset-auth.md) | S |
| 4 | Runtime document `/c/<tile>/?native=1`; `native` in `/components`; `native` in `/whoami` | B |
| 5 | `xbin-ws-origin` meta (app requests) honoured by `xbin-client.js` | A |
| 6 | Push: device push registration, relay sender, `POST /api/xbin/notify` | A |
| 7 | ACP prompts with attachments; a full-diff route; pending elicitations in the session snapshot; document `status.title` | A |
| 8 | Per-user session status events (waiting/idle) for the inbox | A |
| 9 | `native.js` edits don't restart backends | B |
| 10 | Agent template: `/stream?deltas=1`, paged `/view`, thumbnails (plans/agent-template-native.md) | B |
| 11 | Optional: a terminal replay offset (resume without a full replay) | A |

Built 2026-09-26: rows 1–10 (D93, D95, D91, D94, D97, D96). Row 11 is not.

## 21. Phases

Phase 0 is this document. Then three tracks, in parallel:

- **Track S — security.** Strict tile asset gating (its own plan); frame-token
  binding (ships with device login). The app does not wait for it (§6.1).
- **Track A — the shell.** Workspaces + device login → web tiles (§6) →
  terminal (§12) → ACP agent (§13) → push relay + inbox (§14).
- **Track B — the tile runtime.** `xb-native.js` + fixtures + the Lit reference
  renderer (Linux-verifiable) → XbinCore tree/patch (Swift on Linux) → the
  SwiftUI renderer → the eight tile rewrites as fixtures → the agent template
  (its own plan).

**CI.** `.github/workflows/ios.yml` on `runs-on: xcode-27`: `brew install
xcodegen` → generate → build → test (XbinCore, renderer snapshots of every
fixture: light/dark × default/large Dynamic Type) → upload PNGs and the
`.xcresult`; triggered by a push to a feature branch (never master) that
touches `native/ios/**`, `native/fixtures/**`, `native/spec/**` or the
workflow, and by hand. Then
download the snapshots, compare with the Lit reference screenshots, fix what
isn't an intended platform difference, and publish a montage contact sheet
(iOS vs web per fixture). Details: native/AGENTS.md.

## 22. Compatibility

- Nothing changes for existing tiles: they open as web tiles; `native.js` is
  new and optional; manifest `native` is an unknown key to old xbinds.
- All server changes are additive (§20). The only breaking item anywhere is
  the browser-side strict asset gating's handling of absolute self-references
  (plans/tile-asset-auth.md mechanism B), which has its own rollout and
  migration note.
- docs/compat.md gains a native section: vocabulary, tree format,
  `xb-native.js`, `xbin.native` and the bridge are additive-only; shipped apps
  lag by months.

## 23. App Store position

- App Review 4.7 allows HTML5/JS mini apps and plug-ins not embedded in the
  binary, with conditions: 4.7.2 forbids exposing native platform APIs to such
  software without Apple's permission; 4.7.1/4.7.4/4.7.5 add content
  reporting, an index and age gating; 2.5.6 requires WebKit for web content;
  4.2 requires more than a repackaged website.
- Our position: the app is a client for the user's own, self-hosted workspace
  (like Home Assistant, Blink, Scriptable); tile code comes from the user's
  server, not from us; all web content runs in WebKit; the vocabulary is a
  rendering format and **no device API is exposed** (§2); the shell's terminal
  and agent surfaces are substantial native functionality.
- Mitigations: raise 4.7.2 with App Review early (TestFlight review); a remote
  switch per app build turns the native runtime off (everything becomes a web
  tile) without an app update.

## 24. Risks and open questions

- **Scheme-handler edge cases** (§6.1): streaming, CORS for opaque origins,
  `EventSource`. Fallback: plans/tile-asset-auth.md mechanism A/B.
- **Hidden-WebView timers and memory**: `inactiveSchedulingPolicy`, runtime
  LRU; measure on device early.
- **Controlled text fields over an async bridge**: the §7.3 rule must hold
  under fast typing and IME composition; fixture tests with scripted input.
- **iPhone Duo APIs** are known from secondary sources; confirm against the
  Xcode 27.1 SDK before use; the design only needs size classes + two optional
  APIs (reserved regions, hinge).
- **App Review** (§23).
- **Relay operations**: who runs it, where, abuse limits (decision 13).

## 25. Decisions for review

Each with a recommendation. **Recorded 2026-09-26** as the recommendations
were built: D91 (1, 2, 9, 10), D92 (3, 5, 6, 8, 11, 12), D93 (4, 7), D94
(15, and 13 except its open question — the relay's operator and domain),
D97 (14), D98 (§16); 17 was followed (parallel tracks). 16 is open: the
remote kill switch exists in the app but nothing sets it yet, and App
Review has not been asked. The companion plans carry their own lists:
plans/tile-asset-auth.md (target mechanism, host ids, enforcement timing,
per-tile storage) and plans/agent-template-native.md (the model/view split,
its server additions, the drawer).

1. **JS engine for native tiles** — a hidden WKWebView per open native tile
   (recommended: the whole web platform, JIT, the same `xbin-client`, sandbox
   and module loading) vs a JavaScriptCore `JSContext` (lighter; no JIT for
   embedded JSC, needs polyfills and a module loader).
2. **Runtime document and loading** — xbind generates the runtime document in
   the tile's sandbox; it and web tiles load through the app's per-workspace
   scheme handler with the tile's frame token on every request (recommended)
   vs an app-bundled runtime page.
3. **Minimum iOS** — iOS 26 (SwiftUI `WebView`/`WebPage`,
   per-identifier website data stores, current design language; recommended)
   vs iOS 18; iPhone Duo APIs behind `#available(iOS 27.1, *)`.
4. **Device enrollment** — QR from a signed-in browser + in-app password/SSO
   with a PKCE-protected ticket (recommended) vs passkeys.
5. **Face ID policy** — a biometric prompt per new session (after expiry or an
   xbind restart) plus an optional app lock (recommended) vs a prompt on every
   app open.
6. **Sessions dying on restart** — the app re-signs automatically on the 401
   (one Face ID prompt; recommended) vs persisting sessions server-side.
7. **Frame-token binding** — tokens carry the minting session's generation so
   logout/revocation kills them, browsers included (recommended).
8. **Terminal emulator** — SwiftTerm behind an adapter (recommended) vs our
   own.
9. **Markdown** — lexed in the runtime into tokens, rendered natively
   (recommended) vs a native parser.
10. **The chat family in the vocabulary**, shared with the shell's ACP agent
    (recommended).
11. **Lit reference renderer** — shipped at `/vendor/xb/` for previews and
    `bx preview` only, frozen once shipped (recommended) vs repo-only.
12. **iOS colours** — platform semantic colours + xbin amber as tint, with a
    light palette defined for tokens (recommended) vs the exact xbin palette.
13. **Push relay** — run by xbin-dev, code in `relay/`, E2E-encrypted,
    opt-in per workspace: **who operates it and at which domain?**
14. **Agent-session server additions** — prompt attachments, a full-diff route,
    pending elicitations in the snapshot (recommended).
15. **`POST /api/xbin/notify`** — a generic backend → user notification API
    (recommended), rate-limited, limited to users who can read the tile.
16. **App Store** — raise 4.7.2 with App Review early; ship the runtime kill
    switch (recommended).
17. **Tracks** — S, A, B in parallel as in §21 (the owner chose parallel
    tracks); Track A starts with workspaces + device login, Track B with
    `xb-native.js` + fixtures (recommended).

## 26. Implementation notes (2026-09-26)

Where the build departs from, or adds to, the text above. The exact
contracts are native/spec/tree.md (the runtime bridge), native/spec/vocab.json
(the vocabulary), native/spec/device-login.md and native/spec/push.md.

**The runtime and the tree (§7–§9, D91).**
- Tree ops are `set / unset / events / insert / remove / move` (§9 had no
  `events` or `unset`); `move` stays within the node's parent; ops apply in
  order with final-position indexes. `mount` and `patch` carry a sequence
  number `n`, which the app returns as `xbn.event`'s fourth argument (§7.3's
  ordering for controlled props); `xbn.remount()` asks for a fresh mount. The
  §9 illustrative patch lists ops in an order the runtime never emits.
- Messages beyond §9: `{op:"state"}` (`saveState`), `{op:"diag"}`
  (diagnostics); `error` kinds are `unsupported`, `exception`, `module`
  (fatal: the app shows the web page) and `uncaught` (reported only).
  Runtime → app messages are JSON **strings**, so booleans stay distinct from
  numbers.
- The app injects caps and state as `window.xbin = {native: {caps, state}}`
  before `xbin-client.js` freezes `window.xbin`; the runtime document loads
  the entry with `boot()` so a module failure is reported at once.
- `p`, `e` and `c` may be absent (= empty); keys are opaque (they may hold
  `.`, `:` and `/`). `tabs` without `selected` materializes every tab.
- Vocabulary additions to §8: `toolbar` takes `badge`; `message` has a `link`
  event; `list` may hold `empty`/`progress`/`notice`; `transcript` also takes
  `notice`/`text`/`markdown`/`image`/`progress`; `toolcard` takes
  `markdown`/`notice`; `menu` takes `divider`; `sheet` has `edge`
  (bottom | leading — the agent template's drawer); a `height` token set
  (xs…xl); 72 icon names; `fragment` is a runtime primitive. Chart
  `y: "percent"` values are fractions (0…1).
- §18: the eight rewrites are real files (examples/, builtin-tiles/) and
  `native/fixtures/tile-*` import them. They render a loading tree before
  their first fetch (§7.6's 5 s timeout), use the kit's `selfApi` rather than
  local `api()` helpers, and follow their pages where the design differed
  (chat's model separator is U+FFFD; its turn keys count tool-only rounds).
  `hack/xb-native-examples.test.mjs` still runs the design's own code.

**Device login and binding (§5, D93).** The request fields are `deviceId`,
`signature` and `platform`; the signed message is
`"xbin-device-login-v1\n" + origin + "\n" + deviceId + "\n" + nonce`, and
the origin is the one fixed at enrollment. Enrolling is a step-up (a sign-in
under 10 minutes old or the password). SSO-only mode lets a non-admin's
device sign in only within the session max TTL of their last SSO sign-in.
Sign-out-everywhere keeps enrolled devices unless asked (`?devices=1`).
Frame-token generations persist across restarts (`.xbin/frame-gens.json`)
and frame use slides the login's idle window. Deep links also accept the
server's `host[:port]` for `<ws>`, plus `xbin://<ws>` and
`xbin://sso?error=<code>`.

**Web tiles in the app (§6).** A top-level page with WebKit's `xbin`
handler counts as embedded for `xbin-client.js`, so `xbin.dialog` and
`xbin.window` reach the app (§6.2 assumed the page's own posts would). The
scheme handler follows redirects only on the workspace origin. "Open in
Safari" opens the plain URL (no one-shot signed-in ticket route exists).

**Push (§14, D94).** Sealing is ephemeral X25519 + HKDF-SHA256 + AES-256-GCM
(envelope `{v, epk, n, ct}`), not RFC 9180 HPKE. The SDK helper is
`xbin.NotifyUser` (`xbin.Notify` is the existing toast). A frontend may
notify only its own user; over a person's shared tile budget a notification
is dropped with 202, not refused. Push kinds are `agent.permission`,
`agent.question`, `agent.turn`, `tile`, `tile.<kind>` and `test`. The relay
adds `PUT/DELETE /v1/handles/{handle}`, `GET /v1/workspace`, error codes and
token verification; its state is a snapshot plus a journal. Registrations
follow users (sign-out-everywhere, disable, delete) and devices (removal,
their session signing out, owner-token rotation).

**The terminal and the agent (§12–§13, D97).** The `/ws/term` codec, the
session machine and the predictor are `XbinTerm`; the agent model is
`XbinAgent` (native/AGENTS.md). A session that fails uses the upgrade's HTTP
status (bx-terminal can't see it) and resets the emulator before every
replay. Images past the inline limits (3.75 MiB each, 4 MiB per prompt)
become files the agent opens (`inline:false`) instead of a 400. Status
changes ride `term` op `status`; `term`/`session` events reach humans only,
and a session's own sandbox token may not drive it. The app has no
`/ws/events` socket yet: Needs-you comes from the session directory and
pushes, and native runtimes don't reload live on file changes.

**Asset gating (§6.1, D95).** The origins exchange is a one-time,
session-bound `?xbin_ticket=`, not `?frame=`; origins mode renames the
session cookie `__Host-xbin_session`. `legacy` carries three security fixes
(race-free file serving, CSP sandbox on non-documents, frame tokens only for
a human or the tile itself — plus navigations within one tile tree).

**Tools (§16–§17, D92, D98).** `bx preview --native` writes to `$TMPDIR`
without `--out`; `bx lint` and `bx preview` require `--native`. bx's
credential reaches only the tile's own document and files. The fixtures
split the suggested groups into 19 one-screen tiles plus the 8 `tile-*`
ones, and must exercise the whole vocabulary.

**Rendering (§10, §15).** The SwiftUI tint is `accentText` (darkened amber in
light mode) for text contrast. `split` stacks both panes when compact, like
the reference renderer; iPhone Duo APIs are not used yet. Snapshots render
a hosted offscreen window (ImageRenderer draws UIKit-backed views as
placeholders). The reference renderer draws pull-to-refresh as a bar button
and folds swipe actions into a ⋯ popover.
