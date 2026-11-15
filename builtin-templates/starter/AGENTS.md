# Working on this tile

A copy of the **starter** template: a static page (`index.html`) and a
manifest (`xbin.json`). The workspace's own `AGENTS.md` (at its root) and
`/docs/elements.md` say how a tile is built — a backend, grants, resources,
views. This file is about one thing that is easy to miss.

## A person's workspace agent

People here can have a **workspace agent**: software a model drives, which
xbind lets call a tile's API *as that person* (`/docs/auth.md` §Delegation).
This tile says nothing about it, so the default holds: once the tile has a
backend, a person's agent may call `/api/<this tile>/…` wherever that person
can read the tile — reads freely, and every write only after the person
approved that call (xbind checks). That is the point of a workspace agent,
and for an ordinary app it is what you want. Leave it.

Narrow it in `xbin.json` when the tile is not an ordinary app:

```jsonc
"agent": "read"   // agents see what the person sees and change nothing:
                  // GET and HEAD only. For a dashboard, or while you decide.
"agent": "off"    // agents get nothing. For a tile whose API hands out
                  // secrets or is a control surface: tokens, firewall or
                  // routing rules, terminals, other people's data.
```

Say why in a comment beside it. Absent means `"full"`.

**Routes that must stay a person's.** xbind approves a *call*; only the tile
knows what the call means. If a route *answers* something a person was
asked — an approval, a confirmation, a permission, a sign-in — or changes
what this tile's own automations may do, an agent must not be the one to
call it, or it approves its own way through. Such a call carries
`X-XBin-Agent`; refuse it in the handler:

```go
func approve(w http.ResponseWriter, r *http.Request) {
	if xbin.Caller(r).ByAgent() { // the person's workspace agent, not the person
		xbin.WriteError(w, http.StatusForbidden, "this is for the person themselves")
		return
	}
	// …
}
```

In another runtime, read the header: xbind strips inbound `X-XBin-*`, so
`X-XBin-Agent` on a request is its word. Three more rules of thumb:

- judge a caller by `X-XBin-Role` or `X-XBin-User-Level`, never by
  `X-XBin-From` starting with `user:` — an agent's call has that too, at
  role `reader` or `writer`, never `admin`;
- when an agent's call changes something, store `Caller(r).Agent` beside the
  user, so the history says "Ada's agent" and not "Ada";
- say in `API.md` which routes an agent may not use.
