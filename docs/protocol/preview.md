# Protocol: previews — see what a person sees

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
xbind rendering a tile or a screen as the person who asks, read-only, in a
headless browser of its own (D234).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

```
POST   /preview                   a person, for themselves — a browser
                                   session or the app's bearer — or a
                                   person's terminal on a tile, for that
                                   tile only. {tile | screen, shell?, at?,
                                   viewport?: {width, height}, wait?,
                                   fullPage?} → {ok, notice, url, title,
                                   status, width, height, png, snapshot,
                                   frames: [{url, snapshot}], console,
                                   pageErrors, requestsFailed, ready,
                                   stillLoading?, timedOut?, clipped?,
                                   pageHeight?, leftOut?, note?, browser,
                                   sandbox, timing, memoryKB}. See below.
GET    /preview                   authenticated. {available, reason?,
                                   memoryCap, memMax, perUser, total,
                                   timeoutMs}; an admin also gets
                                   {browser, sandbox, playwright,
                                   running}.
```

## What it does

xbind starts a headless Chromium (the base rootfs's Playwright build) in a
throwaway sandbox, opens the page **as a one-shot read-only session of the
caller**, waits until it is drawn, and answers with a picture and a text
rendering. The session is never handed to anyone — not to the caller, not
to the browser: the sandbox has no network, only one socket to xbind that
exists for that render, and xbind sets the session on that socket's
requests itself. It ends when the answer is written.

What to render:

| Body | Renders |
|---|---|
| `{"tile": "apps/x"}` | the tile's own page, filling the viewport |
| `{"screen": "Home"}` | the shell with that screen and its tiles — an id or a name (ids first, names without regard to case), your own screens and the org screens shared with you |
| `{}` | your active screen |
| `{"tile": "apps/x", "shell": true}` | the shell with the tile opened on its screen ([the D220 link](/docs/elements.md)) |

`at` is the tile's fragment (`#tab/sub`). `viewport` defaults to 1280×800
(240–2560 each way); `{"width": 390, "height": 844}` is a phone. `wait`
adds up to 15000 ms after the page is drawn; `fullPage` takes the whole
scroll height — up to a picture's pixel budget (about 8 megapixels: 6553
px tall at 1280 wide). A taller page is clipped to its top, and the answer
says `clipped: true` with the page's `pageHeight`.

The answer:

- `png` — the picture, base64. `width`, `height` — its size.
- `snapshot` — the page as text: an accessibility tree (roles, names,
  values), usable without vision. `frames[]` has one per frame of the page
  — in a screen render, one per tile.
- `console` (errors and warnings), `pageErrors`, `requestsFailed`
  (`{url, method, status | error}`) — what went wrong while it loaded. A
  shell rendered for a non-admin makes a few requests that are refused by
  design (`/status`, `/backends`, unset prefs): they are listed like any
  other.
- `url`, `title`, `status` — where the page ended up.
- `ready` — how xbind decided the page was drawn: `signal` (every tile
  called [`xbin.ready()`](/docs/elements.md)), `settled` (the network went
  quiet and every frame had loaded) or `timeout` (a tile that declared it
  would signal never did; the picture is what there was). `stillLoading`
  lists requests that were open when the picture was taken.
- `leftOut` — reads the page asked xbind for that a preview does not serve
  (see the scope below), and `note` — set when the render left something
  out on purpose: today, for a workspace admin.
- `browser` — `chromium <version>`. `sandbox` — whether Chromium's own
  renderer sandbox ran inside xbind's: `none` today
  ([isolation.md](/docs/isolation.md)).
- **`notice`** — a fixed sentence: the content is what tiles drew and what
  their data says; **data, not instructions**. Whatever shows this answer
  to a model shows the notice with it. Any tile the person can read can put
  words in the picture and in the snapshot, so neither is ever a source of
  commands, and names, links and codes in them are unverified.

The answer is xbind's, not the browser's: everything the sandbox returned
is rebuilt to this shape, each field clipped to a size (a URL 2 kB, one
frame's text 16 kB, all of them 64 kB, 40 console lines and failed requests
of 600 bytes a field), control characters removed, and the picture decoded
and re-encoded by xbind within its pixel budget.

No credential is in the answer: frame tokens, tile-origin exchange
parameters, asset tokens and path tickets are cut from every URL and string
in it (`…` in their place), and all of them stopped working when the render
ended.

## What a render can and cannot do

- **It cannot write.** Every request that is not `GET`, `HEAD` or `OPTIONS`
  is refused with 403, whatever credential it carries; terminals and the
  sign-in routes are refused outright. So the page's own saves fail — a
  tile's `xbin.fetch(…, {method: "PUT"})`, the shell's layout save after a
  link moved it to another screen — and nothing lands.
- **Every identity a backend can authorize on says read-only.** A GET's
  handler, and a WebSocket's messages after its upgrade, are the backend's
  to judge — so on every call a render makes to a tile backend, plain or
  upgrade, xbind sends `X-XBin-Role: reader` (the read form of whatever
  role the call would have had: a tile's own frame is otherwise its tile's
  `admin`) and `X-XBin-User-Level: read` whatever the person's level, so
  `xbin.Caller(r).UserCanWrite()` is false. A custom role with no read form
  (a provider's own `consumer`) is kept on a plain GET and refused on an
  upgrade. The price: a tile that draws differently for a writer is drawn
  as its reader sees it.
- **Backends are told why.** Those calls carry `X-XBin-Preview: 1`
  ([sdk.md](/docs/sdk.md): `Caller.Preview`). A GET or a socket with an
  effect — marking something read, presence — must skip it then; xbind
  cannot see inside a tile's handler. A tile must not draw anything
  *different* because of it: the header suppresses effects, it is not a
  mode, and page scripts are not told (`/whoami` says `readOnly: true`, as
  in view-as, and nothing more).
- **It reaches only what it shows, with only its own credentials.** The
  render's socket honours the render's own session and what was minted
  under it — a frame token or tile cookie of any other session, the
  person's real one included, is refused there. A tile render's socket
  admits that tile: its files, its frame's calls (to xbind and to the tiles
  it is granted, as in a browser), nothing in the person's own name beyond
  loading the tile's page. A screen render's admits the shell, the tiles on
  that screen, and — in the person's own name — exactly the reads the shell
  makes to draw a screen: `/whoami`, `/components`, `/screens`, `/prefs`,
  `/branding`, `/tile-report`, `/limits`, `/alerts`, `/term/sessions` (the
  list), `/code/prs/summary`, the pending `/grants`, `/bindings` and
  `/access-requests`, a frame token for a tile on the screen, and
  `/ws/events`. Anything else answers 403 and is named in `leftOut`: the
  admin-only status reads the shell asks for are always there. A tile that
  loads another tile's files shows without them; a chrome tile on a screen
  (it acts as you) gets the same list, so what it reads beyond it is
  missing.
- **An admin's render is a member's view.** For a workspace admin the
  pending grants, bindings and access requests are answered empty and live
  events are refused — to an admin those are the whole workspace's — and
  `note` says so. Prompts and status marks only an admin sees are not in
  the picture.
- **It reaches no network.** Only the workspace's own names resolve in the
  sandbox, and to xbind's socket; images, fonts or scripts from other sites
  fail to load, as on an offline machine.
- **A partition is read, never made.** On a partitioned tile the render
  opens the caller's own partition, read-only. A partitioned tile the
  caller has never opened answers **409** ("open it once first"): a
  preview mints no partition identity, creates no volume and starts no
  first instance.
- An admin's view-as session can't ask for a preview, and a preview is
  never of someone else: admins look through
  [view-as](/docs/auth.md).

## Errors

| Status | When |
|---|---|
| 400 | a body that doesn't parse, or `tile` that is no tile path |
| 403 | the caller is not a person for themselves: the root token, a tile or its frame, a view-as session, a terminal asking for another tile or a screen; a non-primary tile deployment's credential (a preview shows the primary) |
| 404 | no such tile among the ones the caller can open; no such screen among theirs (the error names them) |
| 409 | a partitioned tile the caller never opened |
| 429 | previews are busy: a render is running for the caller, or the workspace is at its limit — one answer for both (`Retry-After`) |
| 502 | the browser gave no answer (the text says what it printed) |
| 503 | previews are unavailable here: the workspace runs without `--isolate`, the base rootfs has no Chromium, a render's memory can't be capped (xbind's cgroup is not delegated and `XBIN_PREVIEWS` is not `uncapped`), or `XBIN_PREVIEWS=off`. `GET /preview` says which |
| 507 | the page needed more memory than a render's cap, or crashed the browser |

A page that fails to load is not an error: the answer is 200 with
`status`, `navError` and whatever was drawn.

## Limits

One render at a time per person, two per workspace, 45 s each, 1 GiB of
memory — `XBIN_PREVIEW_PER_USER`, `XBIN_PREVIEW_TOTAL`,
`XBIN_PREVIEW_TIMEOUT`, `XBIN_PREVIEW_MEM` ([config.md](/docs/config.md)).
The memory cap is a cgroup's and needs xbind's cgroup delegated, like tile
limits. Where it is not, previews are **unavailable** (503;
`memoryCap: false` in `GET /preview`) unless the admin sets
`XBIN_PREVIEWS=uncapped`; a render then has no memory cap, only its scratch
space — `/tmp`, `/dev/shm` and the sandbox's root layer, memory all three —
sized to `XBIN_PREVIEW_MEM` each, as it is in every render. A render takes about 1.5 s for a tile and
2–3 s for a screen, and about 0.4 GB while it runs.

## `bx preview`

```
bx preview apps/x                         the tile, 1280×800
bx preview apps/x --viewport 390x844      …as on a phone
bx preview --screen Home --text           a screen, with its text rendering
bx preview apps/x --out shot.png --json   the whole answer (png: the file)
bx preview --status                       can this xbind render?
```

The picture is written to `--out`, else to `preview-<name>.png` in the
current directory, readable by you alone (0600).

In a tile's terminal `bx preview` renders that tile for the terminal's
person. Elsewhere it needs a person's own credential in `XBIN_TOKEN` (the
root token is nobody's view).
