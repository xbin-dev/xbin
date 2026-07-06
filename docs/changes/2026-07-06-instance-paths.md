# 2026-07-06 — instance path prefixes are provider-relative

**BREAKING** for tiles that provide interface **instances**
(`provides { …: { kind: "http", "instances": true } }`).

## What changed

`PUT /api/xbin/iface-instances` now **rejects workspace-absolute path
prefixes** (`"/api/…"`) with a 400. Registered prefixes are
**provider-relative**: xbind composes the URL it injects into consumers as
`/api/<provider><prefix>`.

Previously an absolute registration was accepted silently and the composed
URL doubled the prefix
(`…/api/apps/imap-connector/api/apps/imap-connector/m/1`), so every consumer
call 404'd — in the *consumer's* logs, far from the actual mistake. Trailing
`/` on a prefix is now also normalized away (consumers append `/sub` paths).

## Who's affected

Any provider that registered instances with its own `/api/<self>/…` prefix,
e.g.:

```go
// BROKEN (now rejected at registration):
instances[id] = fmt.Sprintf("/api/%s/m/%d", xbin.Self(), acctID)
```

Consumers need no changes. Providers registering relative prefixes already
were correct.

## How to migrate

Register the prefix relative to your own API root:

```go
// CORRECT:
instances[id] = fmt.Sprintf("/m/%d", acctID)
// → PUT /api/xbin/iface-instances {"instances":{"aurora":"/m/1"}}
// → consumers get url http://xbin/api/<you>/m/1
```

Then restart/re-register once after upgrading xbind — registration replaces
the stored map, and requesters bound to you are restarted with clean URLs.
Stored absolute paths from before the upgrade stay broken until you
re-register (they were 404ing anyway).

Serve the instance sub-surface under your normal mux, e.g.
`GET /m/{acct}/messages` — an instance is a sub-surface of *your* API, and a
bound consumer's injected URL routes straight into it.

## Why

Two reasons this is the contract (and not "inject absolute paths as-is"):

- `provider#instance` must only ever route **into the provider** — the
  binding grants a role on *you*, so an instance is structurally a sub-path
  of your API, never an arbitrary redirect.
- Persisted registrations must not embed install paths: `/api/apps/x/…`
  goes stale the moment the tile is renamed or cloned. Same rule as "never
  hardcode your own path in code" (workspace `AGENTS.md`), applied to state.

The old docs showed an ambiguous example (`"/api/path/prefix"`) in several
places, which is how absolute registrations happened in the first place —
those docs are fixed, and the validation now catches the mismatch at the
provider, where it's fixable.
