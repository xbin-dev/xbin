# 2026-08-02 — ownership fixes: org-governed access, backend-only secrets

Two behavior changes in this wave can alter what existing principals can do.
Both are tightenings; check the two lists below after upgrading.

## 1. Access resolution on org-owned tiles (D31)

**What changed.** An org-owned tile is now governed by the org: a user's
level on it comes from their org membership, org-sanctioned shares, and
exact per-user entries. Two consequences:

- **Personal pattern entries and workspace `defaultTiles` no longer reach
  org-owned tiles.** A user who could open an org tile only because a broad
  pattern (`apps/*: read`) or a workspace default matched it loses that
  access. If it was intended, express it the org way: add them as a member,
  share the tile to one of their orgs, or write an exact entry
  (`bx access <tile> set user:<id>=read`).
- **Exact per-user entries are authoritative.** Where a user previously got
  `max(exact, pattern, org level, …)`, the exact entry now wins outright —
  down as well as up — and the new level `none` explicitly excludes one
  user from one tile. If you had exact entries coexisting with higher
  pattern grants, the effective level may DROP to the exact entry's value;
  delete the exact entry to fall back to the union
  (`bx access <tile> rm user:<id>`).

**Who to check.** Run `bx access <tile>` on org-owned tiles that mixed
exact + pattern entries, and any workspace whose `defaultTiles` patterns
(`bx doctor` warns on dead ones) were relied on for org-tile visibility.

## 2. Vault secret reads are backend-only (D30)

**What changed.** `GET /api/xbin/vault/<comp>/<key>` (the secret's VALUE)
now requires the tile's **instance token** — the running backend. Two
principals that could previously read values no longer can:

- **tile terminals** (`bx vault get <comp> <key>` inside a shell on the
  tile → 403; `ls`/`set`/`rm` keep working — terminals are write-only
  managers now);
- **tile frontends** (frame tokens) lose the vault API entirely.

**If a tile's code read its secrets from the terminal or frontend**, move
the read into the backend (`xbin.Secret("key")` in the Go SDK, or the
equivalent runtime call) and expose whatever derived behavior the frontend
needs through the tile's own API. Admin management (list/set/rotate via the
admin console or `bx vault`) is unchanged.

Everything else in this wave is additive: provider-side approvals and
visibility, requester-visible pendings, allowance role/instance qualifiers,
`X-XBin-User[-Level]` attribution headers (new — backends that ignore them
behave as before), `approvedBy` on grant rows, lifecycle for owners, and
`bx new --owner` replacing the dead `--team` flag.
