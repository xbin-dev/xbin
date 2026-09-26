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
itself (its own frame, terminal or instance token, or an `xbin.window`
sub-path of it), or is a navigation within the tile's own tree of nested
pages. Another tile's frontend or backend that fetches the document through
its user's access now gets the HTML with `content=""`. Code-grant reads
already got none.

## Who's affected

- Tiles or scripts that parsed the frame token (never supported).
- Dashboards or tiles left open for more than 30 days since sign-in, and
  pages open in a browser whose login was ended explicitly — they stop
  until reloaded.
- A tile that fetched **another** tile's HTML to reuse the frame token in
  it (to act with that tile's grants). That was a privilege escalation,
  not an API.

## How to migrate

- Treat the frame token as opaque; nothing else changes for tiles that use
  `xbin.fetch`/`xbin.ws`/`xbin.url`.
- A tile that needs another tile's data calls that tile's API with its own
  frame token (the other tile decides, `X-XBin-*` identity headers), or
  declares a grant (docs/auth.md) — it does not borrow the other tile's
  identity.
- Long-running wall displays: reload once a month, or sign in again when
  the page reports that its login ended.

## Why

A frame token that outlives its login is a credential nobody can revoke:
signing out on a shared computer left every open tile working. And any
tile could lift another tile's token (the admin tile's, with its
`xbin:admin` grant) out of a document it could read through its user's
access — the native runtime document made that possible even for tiles
that never published one (`inject: false`, no `index.html`).
