# 2026-09-26 — frame tokens: bound to the login that opened the tile, minted only for the tile itself (D93, D95)

## What changed

A tile's frame token — `<meta name="xbin-frame-token">`, what
`xbin.fetch`/`xbin.ws`/`xbin.url` attach and `xbin-client.js` renews every
ten minutes — changed in two ways.

**It is bound to the login that opened the tile.** It stops working the
moment that login ends: sign-out, the session expiring, the device being
removed, *sign out everywhere*, the user being disabled, or — for a page
opened with the bootstrap owner token — rotating that token. Before, a
tile's token renewed itself forever, even after sign-out.

- A tile in use keeps its login alive: its renewals and requests count as
  activity, so the 12 h idle window slides. The 30-day cap since sign-in
  still applies, so a page left open for more than 30 days needs a reload.
- An xbind restart still signs everyone out, but pages already open keep
  working until their login would have expired.
- The token has five `|`-separated fields instead of four. It was always
  opaque; pages open across the upgrade keep working (old tokens verify
  until they expire and renew into bound ones).
- A renewal that answers 401 means the login ended; `xbin-client.js` logs a
  one-line hint, and reloading the page signs in again.
- Tokens-mode asset tokens (docs/auth.md §Tile asset gating) follow the
  same binding.

**It is minted only for a person or the tile itself.** The `<head>`
injection of `/c/<tile>/` — and of the tile's native runtime document,
`/c/<tile>/?native=1` — carries a frame token only when the request comes
from a human (browser or app session, the owner token) or from the tile
itself (its own frame or terminal token, or an `xbin.window` sub-path of
it), or is a navigation within the tile's own tree of nested pages (when
everyone who can write the page navigating can also write the target — a
parent's writers write its whole tree, so parent → nested page always
qualifies; a nested page whose own writers can't write its parent doesn't
get the parent's token). Another tile's frontend or backend that fetches
the document through its user's access now gets the HTML with
`content=""`, and so does another tile's page **opened or framed** with
`xbin.url()`: `<iframe src=${xbin.url('/c/<other>/')}>`,
`location.assign(xbin.url(…))`, `window.open(xbin.url(…))` load the other
tile's page without a token — its `xbin.fetch`, `xbin.ws` and events fail. Code-grant reads already got
none. A tile's **own backend** gets none either, from its page or from
`GET /api/xbin/frame-token` (403): its instance token names no person, so a
frame token minted for it read as the owner's frame — owner reach on every
tile, where the instance token itself reaches only its own tile.

## Who's affected

- Tiles or scripts that parsed the frame token (never supported).
- Dashboards or tiles left open for more than 30 days since sign-in, and
  pages open in a browser whose login was ended explicitly — they stop
  until reloaded.
- A tile that fetched **another** tile's HTML to reuse the frame token in
  it (to act with that tile's grants). That was a privilege escalation,
  not an API.
- A tile that **embeds or opens another tile's page** through `xbin.url()`
  (a dashboard or launcher framing other tiles inline, or opening them with
  the frame token in the URL). No tile in this repository does.
- A backend that fetched its own tile's page, or `/api/xbin/frame-token`,
  with its instance token to get a frame token. Also an escalation (see
  above); a backend calls xbind with its instance token.

## How to migrate

- Treat the frame token as opaque; nothing else changes for tiles that use
  `xbin.fetch`/`xbin.ws`/`xbin.url`.
- A tile that needs another tile's data calls that tile's API with its own
  frame token (the other tile decides, `X-XBin-*` identity headers), or
  declares a grant (docs/auth.md) — it does not borrow the other tile's
  identity.
- To show another tile: open it in a shell window with `xbin.window({src:
  '<other tile>'})`, or open its page in a new tab with a **plain** URL —
  `window.open('/c/<other>/')` with `cap:open-links`, no `xbin.url` — which
  the browser loads with the person's own session, so the page gets its own
  token. There is no replacement for framing another tile's page *inside*
  yours: the token in the URL was the other tile's credential handed to
  yours.
- Long-running wall displays: reload once a month, or sign in again when
  the page reports that its login ended.

## Why

A frame token that outlives its login is a credential nobody can revoke:
signing out on a shared computer left every open tile working. And any
tile could lift another tile's token (the admin tile's, with its
`xbin:admin` grant) out of a document it could read through its user's
access — the native runtime document made that possible even for tiles
that never published one (`inject: false`, no `index.html`).
