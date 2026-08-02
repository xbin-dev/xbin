# Ownership model — five-story UX review

> 2026-08-02, immediately after D22/D24–D28 shipped. Method: five parallel
> reviewers — four user stories (solo self-hoster; security-split family;
> 40-person corporate multi-org; discord friend group on shared hardware)
> plus a cross-cutting mechanics audit — each booted a **live xbind** with a
> fixture workspace and drove every claim per-persona with curl before
> writing it down. ~40 adversarial authz probes across the runs produced
> **zero unauthorized 200s**; everything below is UX, visibility, and one
> real enforcement-shape bug — not broken guardrails.
>
> **Status (same day):** the D29-D33 fix wave addressed P0 #1-4, P1 #5-12,
> and P2 #13 (exact-entry narrowing + `none`), #14 (X-XBin-User
> attribution), #16 partially (requester visibility + approver hints; a
> full human request-access queue remains open), plus the vault
> terminal/frame read hole found in review (now D30 backend-only). Still
> open: user disable (#15), per-tile memory limits (#17), hostname-granular
> egress (#18), SSO (#19), first-run hardening prompt and most P3 polish.

**Headline:** the model and its enforcement held up everywhere it was
attacked — intra-org vs cross-owner vs allowance-scoped approval, the
xbin/xbin:* floor, ceilings binding even root, share-vs-grant plane
separation, invite single-use, X-XBin-* stripping. The rot is one layer up:
**one blocker** (creating org-owned tiles is unreachable for non-admins), a
cluster of "delegation exists but nobody can see it" gaps (no pending
notifications, requester-blind queues, a stale ⚙ panel), and write-time
acceptance of allowance entries that can never fire. Each of these is
precisely the kind of friction that ends in "just make me ws-admin".

Severity buckets: **P0** broken today · **P1** needed before delegation is
genuinely self-serve · **P2** model decisions to take deliberately ·
**P3** polish.

---

## P0 — broken today

### 1. Creating org-owned tiles is unreachable for non-admins (BLOCKER)
Found independently by the corporate and friend-group reviewers.
`apiCreate` runs `canCreateAt` (internal/broker/create.go:45 →
policy.go:86-103) — which consults only `IsAdmin` ∨ the user's *personal*
`canCreate` patterns — **before** `resolveCreateOwner` ever reaches
`Access.CanCreateAs`/`member.Create`. So the D25 `create` knob (and the
admin tile's **Developer** preset) is dead code: every non-admin with
org-create rights 403s on `POST /create {owner:"org:X"}`, from the API, the
manager tile, and the organisations tile alike. Verified live in two
separate fixtures. The workarounds are both over-grants: personal
`canCreate:["apps/*"]` for everyone (also unlocks unlimited personal tiles
anywhere under the pattern), or ws-admin creates + transfers (the exact
bottleneck D24 removes).
**Fix:** when the request names `owner: "org:X"`, gate on `CanCreateAs(X)`
*instead of* personal patterns; keep the personal-pattern rule for personal/
workspace tiles. Add the missing composed-path test — unit tests cover
`canCreateAt` and `resolveCreateOwner` separately; the seam between them is
untested, which is why this shipped. Also fix the stale denial copy: both
messages still say "ask an admin for a create pattern **or a team**"
(policy.go:94,101) — teams are abolished.

### 2. The shell ⚙ access section still speaks the teams model
`workspace-template/shell/bx-tile-admin.js` predates the rewrite: it expects
`a.org`/`a.orgAdmins`/team entries from `GET /access` (lines 43, 122-125,
218-274), so an **org-owned** tile's ⚙ header renders "workspace-plane tile
(no org)", the add-entry picker offers a `team` kind the API rejects, and an
**org share** — the model's main sharing primitive — cannot be written from
⚙ at all. The UI wave updated admin/organisations/manager/bx-shell and
missed this file.
**Fix:** rewrite the section against the new `{tile, owner, entries[kind:
user|org], source}` shape; delete the team code and the teams-era comments
(bx-shell.js:589 too).

### 3. Allowance entries validated by class prefix only — dead entries accepted silently
`ValidateAllow` (internal/users/orgs.go:471-478) refuses literal
`xbin`/`xbin:*` but accepts, with a 200: `cap:xbin` (renders in
`resolvedAllow`; enforcement still refuses at approve-time, so no
escalation — but the *state lies* and will terrify an auditor);
`net:internet:api.example.com` (not a real target shape — matches nothing);
`tile:apps/sales-*` (dead: `tile:` uses `matchTile` — exact or trailing
`/*` only — while every other class uses mid-string `globMatch`; **two
pattern grammars in one list**, undocumented). The org admin then gets
refusals that never say why; the ws-admin believes delegation is configured.
This is the purest "friction → hand out broader rights" generator found.
**Fix:** refuse `cap:xbin*`; validate value shape per class at write time;
unify (or document) the pattern grammar; make the D26 refusal name the
closest-miss allowance entry. Same family: `canCreate:["apps/evan-*"]`
silently never matches (matchTile again) — validate on write.

### 4. The organisations tile never refreshes
`organisations.js` loads once in `connectedCallback` (lines 70-75) — no
events-hub subscription, no poll, no refresh button (contrast admin.js:383
which refreshes on `users`/`grants` events). The broker publishes on every
org mutation; nobody in this tile listens. Its headline feature is the
approval queue — left open, new pendings never appear.
**Fix:** subscribe to `users` + `grants` events; minimum a refresh button.

---

## P1 — before delegation is genuinely self-serve

### 5. Pending approvals are invisible to everyone who matters
The bundle (family, friend-group, mechanics reviewers, all verified live):
- **Requesters see nothing.** `GET /grants`/`GET /bindings` 403 for plain
  users; `bx-grants.js:74-75` silently renders nothing on non-OK. A user
  whose tile waits on a grant sees a tile that just doesn't work — no
  "pending net:internet — ask rio (org admin)". Manufactures the exact
  "hey admin, is it broken?" ping delegation was meant to kill.
- **Approvers aren't notified.** The ⚑ badge counts *orgs administered*
  (bx-shell.js:1782), not pendings, and nothing publishes an event when
  `Pending()` gains a row — the 2am loop runs on discord-ping polling.
- **Pendings on user-owned tiles are visible only to ws-admins** — nobody
  else can even see the queue exists (see #7).
- **bx-grants shows org admins enabled approve buttons that 403** — it
  checks `p.blocked` but not `approvable` (bx-grants.js:117-122), and
  `_approve` ignores the response, so the click fails silently. The
  friendly "N more need a workspace admin" copy exists only in the
  organisations tile.
**Fix:** requester-scoped pending view (tiles you hold ≥write on; requests
only, no approve) + a requester-facing "pending, ask &lt;approvers&gt;" render;
⚑ badge = org-scoped approvable-pending count; publish a `grants` event on
new pending; honor `approvable` in bx-grants and surface non-2xx.

### 6. The provider org has no consent and no visibility over cross-org consumption
Approval rights key on the **caller's** org only (`approverOrg(p, g.From)`,
delegated.go:24-33) and `orgFilterGrants` filters by `From` too. Verified:
org:eng (with an allowance) self-approved reader → writer → **admin** on
org:data's warehouse while data's org admin saw *nothing* — no row, no
pending, no way to revoke from her view. For the sensitive-data story this
inverts consent: the org that owns the data is the only party with no say.
**Fix, minimum:** extend the org-filtered grants/bindings views with rows
whose *target* is owned by the admin's org (read-only + revoke). Fuller: an
opt-in per-tile/per-org "provider approval required" bit making cross-org
grants dual-key.

### 7. Allowances are role-blind and provider-blind
- `tile:apps/warehouse` in an allowance delegates approval of **any** role
  on that tile — reader, writer, *admin* (verified: all three approved).
  `AllowanceCovers` never consults `Grant.Role`. The most common corporate
  intent — "read-only consumers of the data API" — is inexpressible.
  **Fix:** optional role qualifier (`tile:apps/warehouse@reader`,
  implying-downward); bare entries keep any-role for compat, editor nudges.
- `iface:&lt;service&gt;` covers binding to **any** provider of that service
  name workspace-wide (delegated.go:118-127) — a third org can stand up a
  same-named provider and be wired in. **Fix:** `iface:&lt;svc&gt;@&lt;tile-pat&gt;`
  (or reuse the `net:provider:` form).

### 8. User-owned tiles are a delegation dead end
Allowances attach to orgs; `orgAdminMayBind`/`orgAdminMayGrant` require the
tile to be org-owned. So every personal tile with net/caps/ingress needs a
ws-admin forever — and the default creation path IS personal (manager
picker defaults to "me"). Verified: neither the owner nor an org admin
could bind net on a user-owned tile. The escape hatch — transfer to the
org — works but silently hands every org admin implicit **terminal**, i.e.
the tile's vault secrets (orgs.go:706 + vault self-read): Dee must choose
Max-bottleneck or Rio-reads-my-bot-tokens, and no UI surfaces either half.
**Fix:** at minimum surface the tradeoff (consequence copy on the owner
picker + a transfer confirm noting secret access); consider whether a
user's *own* org allowance should cover their personal tiles (decision
needed — it widens D26).

### 9. Owner-picker semantics: "me" isn't me
The manager's default option `— me (personal) —` has value `""`, which
`resolveCreateOwner` maps for **admins** to *workspace-owned*
(create.go:84-89) — verified: a solo admin's tiles have no owner and never
appear in "my tiles". Harmless while solo (picker hidden), wrong the moment
orgs exist.
**Fix:** "me" sends `user:&lt;id&gt;`; add an explicit `— workspace —` option as
the admin default.

### 10. Routine incident response is ws-admin-only (the security-split killer)
The family story's core finding: each of these is a moment the daily-driver
account reaches for the admin password, and after the third one the split
dies:
- **Lifecycle**: `POST /lifecycle` gates on `IsAdmin` while its own comment
  says "lifecycle is the owner's to set" (lifecycle.go:20-21) — an org
  admin can't disable their own runaway tile. (Unbind-net IS allowed — good
  partial kill switch.) **Fix:** align with D24 — owner/org-admin may
  disable/enable their tiles; narrowing-shaped, low risk.
- **Vault unseal**: admin-only (vault.go:307) — after every reboot of a
  sealed-boot home box, HA stays down until the admin account logs in.
  **Fix:** consider org-admin unseal (availability, not confidentiality —
  the passphrase holder proves knowledge) or document the
  `XBIN_VAULT_PASSPHRASE` posture as the recommended home tradeoff.
- **Password reset / invite re-mint / user add**: ws-admin only. Invites
  staying admin-only is the D22 design (defensible); self-service password
  *change* is the missing M1 piece that would remove most of the traffic.

### 11. `bx doctor` learned nothing about ownership
With a fixture containing an orphaned owner row, an admin-less org, a
defaultTiles pattern matching nothing, and dead allowance entries, doctor
reported only the legacy checks. The users.go comment even claims orphaned
tiles are "listed by bx doctor" — they aren't (doctor only flags owners of
*missing components*, cmd/bx/doctor.go:61-75).
**Fix:** doctor checks for: orphaned/workspace-fallen owners after user
delete, admin-less or member-less orgs, dead defaultTiles patterns,
never-matching allowance entries, hand-edited sets references.

### 12. No approver audit trail
Grant rows store `{from, target, role}` — no approver, no timestamp; the
audit log line is `who=… POST /grants 200` *without the body*, so three
approvals in a minute are indistinguishable. With termNet AI agents filing
requests and one-click approval in the organisations tile, rubber-stamp
reconstruction is exactly what compliance will ask for.
**Fix:** log the grant triple in the audit line (cheap); consider
`approvedBy`/`approvedAt` on the grant row — it's a git-diffable capability
table already, provenance belongs there.

---

## P2 — model decisions to take deliberately

### 13. Union-only levels can't carve out one sensitive tile
`TileLevel` is max-of-sources; nothing narrows. Org-wide `terminal` means
terminal on **every** org tile — including the one holding prod keys
(terminal token → element principal → vault self-read). Excluding one
member from one tile means a second org or removing them entirely; the
path of least resistance is "Juno keeps read on the HA bridge" /
"everyone can read the prod keys".
**Decision needed:** per-tile narrowing entries (exact ACL rows that may
CLAMP below the org level on org-owned tiles) or a per-tile `sensitive:
admins-only` flag. Deliberately breaks the "levels only widen" invariant —
that's why it's a decision, not a fix.

### 14. `read` ≈ "fully drive the tile", and backends can't tell users apart
Read mints a frame token; a frame principal calling its own component is
**admin of itself** (broker Policy: `p.Component == target.Path → admin`),
and the proxy forwards only `X-XBin-From`/`X-XBin-Role` — no user identity.
So the HA tile's backend sees the same caller whether Pat or Juno clicked;
any destructive button the tile's own UI exposes is reachable at `read`,
and a tile cannot implement its own kid-mode. Pre-existing D16 semantics,
amplified now that org-wide `read` *looks like* a safety boundary.
**Decision needed:** forward attributed identity (e.g. `X-XBin-User` +
level) on frame-principal calls so tiles can gate in-app; document loudly
that read≈use meanwhile.

### 15. No user disable; offboarding is destructive and undiscoverable
Only `DELETE /users/{id}`: sessions die, ACL rows wiped, user-owned tiles
**silently** fall to workspace-owned (no list in the response, nothing in
doctor — opposite posture from org-delete's 409 "transfer them first").
Contractor pause = delete + recreate + rebuild grants by hand.
**Fix (already sketched in user-org-ux fix 6):** `Disabled bool` — auth
rejects, rows kept; make user-delete *report* the orphaned-tile list.

### 16. No request-access path for humans
Unreadable tiles are invisible (sidebar filtered; bare-text 403 with no
who-to-ask), so members can't even learn a tile's name to request it —
the whole loop is Slack-ops, and admins end up bulk-raising defaultTiles
or org levels to stop the pings. If name-hiding is the intended
information-hiding stance, keep it — but say so in docs.
**Fix, minimum:** owner-aware /c/ 403 copy ("owned by org:sales — ask its
admins"). Fuller: a human access-request row (tile + wanted level) in the
organisations tile mirroring the element queue. Related legibility gap:
when a share exists but an element grant doesn't, nothing explains the
three planes ("shared for viewing; API access needs an allowance or
ws-admin") — the asymmetry reads as a bug to every org admin who hits it.

### 17. Resource limits are one global knob
`XBIN_LIMIT_MEM` (default 2 GiB) applies identically to every backend;
allowance `res:` entries are *data* resources, not limits. A minecraft
container forces raising the cap for **all** tiles, including the sketchy
modpack — the opposite of "no surprises on the power bill". CPU fair-share
weights are fine.
**Fix:** per-component (or owner-pattern) `memMax` — natural as a policy-
row field (`{tiles, memMax}`) or a `res-limit:` allowance class so an org
admin can self-serve within a ceiling. Also: usage is point-in-time,
admin-only, unattributed — no per-org rollup, and non-admins can't see
even their own tiles' usage.

### 18. No hostname-granular egress
`net:` is `internet | host | lan:&lt;cidr&gt; | provider:&lt;tile&gt;` end to end —
"contractor scraper may reach api.stripe.com only" is inexpressible except
via a custom net-provider proxy tile (none ships). The least-trusted tier
gets `net:internet` or nothing.

### 19. SSO/OIDC (recorded, not news)
Corporate provisioning at 40 users is one-by-one invite links; every
forgot-password is an admin re-mint. Verified that nothing in D22's shape
fights OIDC — credential-agnostic User row + no-self-signup maps cleanly
to IdP-asserted identity + admin domain-rule provisioning. Right next
credential feature for the corporate story (churn, resets,
offboard-on-IdP-removal).

---

## P3 — polish

- **First-run hardening prompt** still missing; solo self-hosters are the
  most likely to run on the bootstrap token forever (M1 item; D22 removed
  its last excuse). Guard on `whoami.kind==='root' && users.length===0`.
- **Permission-set term flags are blunt**: `termNet` on a set confers to
  *every* member of attached orgs, even read-level (orgs.go:669-674); to
  scope to one user you must fall back to the per-user flag. Document.
- **Org-policy rows unreadable by org admins** even via GET
  (orgsapi.go:561) — they see `resolvedAllow` but not the deny rows their
  approvals can still trip on. Allow read-only GET.
- **Invite paper-cuts**: raw API returns a *relative* `inviteUrl` (curl
  admins paste a broken link once); redeeming while signed in silently
  replaces your session and burns the link (warn when a session cookie is
  present); the login/invite card is fixed 300px + padding without
  `box-sizing` → clips on 320px phones (login.go:85,121).
- **Organisations tile copy**: ws-admin with no orgs sees "an org admin or
  workspace admin adds you" — the reader *is* that admin; duplicate empty-
  state paragraphs (organisations.js:245,281); transfer uses `prompt()`
  with raw `user:&lt;id&gt;` syntax; share row is free-text.
- **Effective access is invisible**: whoami shows `tiles:{}` even when D27
  defaults grant three tiles; nothing renders "what can I see and why" for
  a member (defaults/shares with provenance would fit whoami or the
  organisations tile).
- **First-screen seeds have no UI**: making the family dashboard everyone's
  default screen means hand-editing `root/index.html`.
- **`POST /users` (password path) stamps `created: 0`** (Upsert never sets
  it; the invited path does) and responds `"created":0`.
- **Org id validation says "invalid user id"**; org ids may collide with
  user ids (consider refusing); case-normalization is silent.
- **`{"ok":"true"}`** string-not-boolean from several mutation endpoints;
  `PATCH /orgs` "bad body" gives no hint (members map-vs-array trap).
- **Approvable grants onto broken manifests**: a pending row for a role
  that doesn't parse was approvable; sanity-check the target role resolves.
- **No `bx defaults`** (GET/PUT /defaults has no CLI); `bx` help is ~45
  lines — consider grouped help if it grows further.
- **`tiles/apidocs` in defaultTiles** is builder noise for pure consumers
  (harmless; admins can edit defaults).

---

## Per-story verdicts

- **Solo self-hoster — well-served.** The org machinery is genuinely
  invisible when unused (⚑ hidden, picker hidden, tabs tucked away, zero
  ceiling checks); invites make self-hardening and the eventual +1 friend
  easy. Gaps: the hardening prompt, and the "me"-picker mislabel that bites
  when the workspace grows.
- **Security-split family — half-served.** With everything org-owned and
  the daily account as org admin, sharing/membership/allowance-approvals
  all work from the daily account — the design's promise, kept. But the
  edges betray it: default-personal ownership routes approvals to the
  admin-only queue invisibly, one sensitive tile can't be carved out,
  read doesn't mean what a parent thinks, and runaway-bot/sealed-vault/
  forgotten-password all require the admin account. As shipped, a
  pragmatic parent grants the daily account admin within a month.
- **Corporate — architecture right, shipping state would grind.** The
  layered model (ownership-not-position, sets, ceilings, humans-approve)
  held up under attack. Go/no-go set: the create blocker (#1), role-blind
  allowances (#7), provider blindness (#6). Fix those and delegation
  absorbs the daily load, leaving IT with users/sets/policy; ship without
  them and the IT admin is the product.
- **Friend group — close; Max is still tech support in four places.** The
  allowance grammar covered the entire 2am wishlist in one PATCH and
  enforcement was exactly right — but the create rake (#1), discord-ping
  approval loop (#5), personal-tile dead end (#8), and the global memory
  knob (#17) keep pulling Max back in.
- **Mechanics — enforcement solid, rot one layer up.** ~40 adversarial
  probes, zero unauthorized 200s; refusals name the responsible role and
  link the docs. The gaps are the stale ⚙ panel, write-time allowance
  validation, the static organisations tile, and doctor.

## What held up (so we don't re-litigate it)

Intra-org approvals need no allowance (one click); allowance-scoped
approvals match the grammar exactly (in-range ingress ports, cap:, net
classes) with out-of-scope cleanly refused; `xbin`/`xbin:*` floor held at
write and approve; policy ceilings bind everyone including root, naming the
blocking row; org shares confer UI access without leaking element-plane
rights; unbind-always-allowed is a real org-admin kill switch; owner
sharing (user-owned tile → exact user entries) is self-serve; delete-user
kills sessions instantly; permission sets compose (allow union, restrictive
ceiling union, flags confer) as documented; defaultTiles killed the
blank-first-login problem; the D22 invite flow was flawless end-to-end in
every story that exercised it; and the `internal/users`-owns-all-semantics
layering made the whole audit tractable — every question had one place to
look.
