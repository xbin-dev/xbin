# Authorization: roles, grants & capabilities

Identity ([05-identity.md](05-identity.md)) answers *who is calling*; this
layer answers *what that principal may do*. The shape is one sentence:
**callees declare roles, callers request them, the owner approves, xbind
enforces at every call — default-deny.** Everything else on this page is
the machinery that keeps that sentence true under composition: multiple
grant sources, reserved capability targets, policy ceilings, and the
clamps that stop privileged tiles from being used as confused deputies.

**Related:** [05-identity.md](05-identity.md) ·
[07-users-orgs.md](07-users-orgs.md) · [10-resources.md](10-resources.md) ·
[11-interfaces.md](11-interfaces.md) · [/docs/auth.md](/docs/auth.md) ·
[/docs/protocol.md](/docs/protocol.md) · plans/auth.md

## The shape

```jsonc
// callee: apps/calendar/xbin.json — declares its surface
{ "expose": { "roles": {
    "reader": "Read events and calendars",     // descriptions are mandatory:
    "writer": "Create and modify events" } } } // they render in the approval UI

// caller: apps/email/xbin.json — requests access
{ "uses": [ { "target": "apps/calendar", "role": "reader" } ] }
```

A `uses` entry is a **request**, not a grant. Whether it becomes authority
is the owner's call (or automatic within a scope — below). The division of
labor (plans/auth.md §3): roles are *defined by the callee*, *granted by
the owner*, *verified by xbind*, *enforced at the callee* with one
middleware (`xbin.Role("writer", h)`). Declaring `uses` is deliberately
agent-safe: agents building tiles declare what they need and stop; approval
is human-in-the-loop by construction, because every approval endpoint is
admin-gated and a terminal token is never admin.

## Roles

- **Conventional ordering**: `admin ⊃ writer ⊃ reader` (ND4). The SDK and
  broker both know it; role checks are `roleSatisfies(have, want)`.
- **Custom roles** are exact-match unless the callee's manifest declares
  `implies` (e.g. `"auditor": ["reader"]`) — the implication graph is
  walked, cycle-safe, on the callee's own declaration.
- **Bus aliases**: on `bus` resources, `subscriber`/`publisher` normalize
  to reader/writer so both vocabularies work.
- **The owner passes every check as `admin`** — the human is root on the
  runtime plane. (A terminal's `$XBIN_TOKEN` is *not* the owner; see
  [05-identity.md](05-identity.md).)

## The grant table

Grants are rows in the workspace `xbin.json` — `(from, target, role)` — a
visible, git-diffable capability table, mutated only through the API:

- **Approval is admin-gated**: the owner, an admin user, or an
  admin-capable element (one holding `xbin:admin`). Approving publishes a
  `grants` event so the caller's already-open frame retries without a
  manual refresh.
- **Revocation (`DELETE`) is always allowed** — even for rows a policy
  ceiling now forbids — so over-ceiling leftovers can be cleaned up.
- New `net:*` grants are **rejected** with a pointer to `bx bind`: egress
  stopped being a grant when it became an interface binding
  ([12-egress.md](12-egress.md)); DELETE still works for stale rows.
- **Pending requests** are computed, not stored: every `uses` entry that no
  grant source currently satisfies surfaces in the grants panel /
  `bx grants`, annotated with the policy row that would block approval (so
  UIs grey out dead approve buttons instead of offering a 400). Offloaded
  components make no live requests.

## Three grant sources, one evaluator

`grantedRole(from, target)` is the single function that answers "what role
does this element hold on that target", consulted by the proxy, the
resource broker, the code API, GPU and net-cap resolution — everything.
It combines, in order:

1. **Explicit grant rows** — the table above; the best-satisfying row wins.
2. **HTTP-interface bindings** — *the binding is the grant*
   ([11-interfaces.md](11-interfaces.md)): a slot the owner bound to a
   provider carries the role the provider's matching provide declares
   (default `reader`). Wiring a dependency and authorizing it are one
   owner-gated act, so they can't drift apart.
3. **Same-scope auto-grants** (ND5) — a scope is one app, one trust unit:
   a `uses` entry whose target lives in the caller's own scope is granted
   as declared, no approval. This is what makes an app's internals
   frictionless while keeping every *cross*-scope edge explicit.

Before any source is consulted, the **policy ceiling**
([07-users-orgs.md](07-users-orgs.md), D20) gets a veto: pattern-keyed
workspace/org rows can deny capability classes (`net`, `gpu`, `xbin-caps`,
`ingress`) or allow-list call targets (`mayCall`) for the tiles they
cover. Because the ceiling sits *inside* the one evaluator, it caps all
three sources at once — a hand-edited grant row, a binding, even a
same-scope auto-grant goes inert the moment a row covers its tile. It is
also enforced at approval time with the blocking row named, so the config
error surfaces early; the evaluation-time check is what makes it stick.

Two authority rules live *outside* the evaluator, in the call-time policy:
an element is always **admin of itself** (its own API, vault, prefs), and
a **cron tick** carries the role its owning element chose at registration
— self-inflicted authority, since cron can only target its own element.

## Reserved capability targets

Not every target is a component or resource. Reserved targets grant
*workspace* powers, and each is classified deliberately under the policy
ceiling (a path allow-list can't name a capability, so they must never
fall into `mayCall` matching — the lesson of the 2026-07-12 `code:reader`
regression):

| Target | Grants the element | Ceiling class |
|---|---|---|
| `xbin` (`writer`) | create components — held by `tiles/manager` | `xbin-caps` |
| `xbin` (`admin`) | full workspace administration: any vault write/rotate, grant approval, lifecycle, users — held by `tiles/admin`; this **is** the element `IsAdmin` capability | `xbin-caps` |
| `xbin:users` | user/org management alone (a narrower admin; `xbin:admin` implies it) | `xbin-caps` |
| `code` | read **every** component's source + git history (`/api/xbin/code/*`, `/git/*`) — owner-level tooling: linters, search, stats | `xbin-caps` |
| `code:<component>` | read **one** component's source — governed exactly like *calling* that component (same-scope exempt, else `mayCall` must cover its path) | like a call |
| `gpu:all` / `gpu:<index>` / `gpu:<uuid>` | the matching GPUs: device nodes + driver libs mounted into the sandbox at spawn | `gpu` |
| `cap:net-admin` | keep CAP_NET_ADMIN/NET_RAW/NET_BIND_SERVICE inside the tile's own netns — required by net-**provider** tiles for their dataplane (D18a); admin-only to approve | `net` |
| `net:*` *(legacy)* | — rejected for new grants; egress is a `net` interface binding now | `net` |

Notes worth internalizing:

- **`xbin:admin` is the heaviest grant in the system** — approving it for
  an element is trusting that element as yourself for administration. It
  ships pre-granted only to `tiles/admin`. One deliberate carve-out: even
  `xbin:admin` cannot *read* another element's vault values — vault reads
  are self-only; admins manage (list/set/rotate/delete) but never exfiltrate
  ([10-resources.md](10-resources.md)).
- Reserved targets are never same-scope auto-granted — they aren't *in*
  any scope, so the ND5 shortcut can't reach them; every one is an explicit
  owner decision.
- An element always reads its **own** source without any grant; `code:*`
  is about reaching *others'*.
- Because element adminship flows through `grantedRole(comp, "xbin")`, an
  org's `xbin-caps` deny row doesn't just block future grants — it neuters
  a covered tile's existing admin capability at evaluation time.

## Resources as targets

`res:<scope>/<name>` targets (kv, blob, bus, cron, sqlite, filesystem) ride
the same grammar: declared in a scope's `scope.json` (or
`res:workspace/<name>` at the root), requested via `uses`, resolved by
longest-declared-scope prefix, checked by the same evaluator with the same
role semantics. Same-scope resources auto-grant (an app owns its own
database); cross-scope resource access is a deliberate, approvable edge —
and for file-backed types it is *brokered only*, never a shared file path
([10-resources.md](10-resources.md)).

## The call-time policy

Element→element API calls hit the proxy's policy, which is a thin shell
over the evaluator: admin principals get `admin`, an element gets `admin`
on itself, cron gets its registered role, and everything else is
`grantedRole` — with a 403 that tells the caller exactly what to declare
and how to get it approved. The verified outcome is what the callee sees
as `X-XBin-Role`.

Some grants aren't checked per call but **materialized at spawn**: resource
env/paths, GPU device mounts, and the net-admin capability set are baked
into the sandbox when the backend starts. Approving or revoking one of
those (`res:*`, `gpu:*`, `cap:net-admin`) therefore restarts the affected
backend automatically, so policy changes take effect *now*, not at the
next incidental restart. Interface bindings restart their component through
the same mechanism when rewired.

## Deputy-proofing: attributed humans clamp capable tiles

The capability-leak principle ([05-identity.md](05-identity.md)) says a
tile never inherits its driver's privilege. The inverse threat is a
*privileged tile driven by an under-privileged human* — the confused
deputy. Two clamps close it, applied uniformly across all five ways a
component can come to exist (create, clone, git import, builtin tile
import, template instantiate):

- **`canCreateAt`** — creating at a path requires: admin; or a human whose
  own (org/team-unioned) create patterns cover it; or an element holding
  `xbin:writer` — **and**, when a signed-in human is attributed on the
  element call (frame or terminal principal), *that human's own create
  rights must cover the path too*. Granting a user the manager tile never
  extends what they may create. Unattributed automation (instance tokens,
  the bootstrap owner) keeps plain capability semantics.
- **`attributedCanRead`** — copy-shaped creation (clone, template
  instantiate) additionally requires the attributed human to be able to
  *read the source*: copying is reading, and without this a manager-style
  tile would be a source-exfiltration route into the caller's own
  namespace.

Adjacent guards at the same choke points: reserved path segments (`o`/`u`
must bind to a real org — [07-users-orgs.md](07-users-orgs.md)), and no
nesting either way (a new component can't sit inside an existing one, nor
swallow one as a subtree). Creation also auto-grants the non-admin creator
`terminal` on the result (D16 — create ≈ own a namespace).

## The audit trail

Every state-changing call to the core API (`POST`/`PUT`/`PATCH`/`DELETE`
on `/api/xbin/…`) is logged as an `audit` line — actor (the verified
`From`: `owner`, `user:<id>`, or a component path), method, path, and
resulting status — so grant approvals, user changes, lifecycle flips,
vault management, and token operations leave a who-did-what trail with
outcomes (a 403 in the audit log is an attempt, not a change). The
high-frequency data plane (`prefs`, `kv`, `blob`, `bus`) is excluded as
noise. It is a log stream, not a queryable store: ship xbind's stderr
somewhere durable if you need retention.

## Honesty: enforcement tiers

The *model* on this page is enforced identically everywhere; **how hard it
is to cheat from inside a hostile element** depends on the deployment tier
(plans/auth.md §9, [/docs/auth.md](/docs/auth.md) → "Honesty"):

| Tier | Floor | Hostile element can still… |
|---|---|---|
| 1 — no `--isolate` (dev) | one uid, instance tokens, gateway default-deny | read sibling env via `/proc`, open sibling sockets, write workspace files |
| 2 — `--scope-uids` | per-scope uids; elements can't write source (even their own) | abuse only what it was granted |
| 3 — `--isolate` (production) | per-backend user/mount/pid/net namespaces, overlay rootfs, default-deny egress ([08-sandbox.md](08-sandbox.md)) | approximately nothing ungranted at the OS layer |

The grant system is the *same* at every tier — tiers change the attack
surface under it, never the semantics. That's deliberate: dev (`--no-auth`,
tier 1) exercises exactly the authorization paths production enforces, so
permission bugs surface before deploy rather than after.
