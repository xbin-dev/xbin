# User / Org / Team UX — audit & fix plan

Pre-open-source audit of the multi-user experience. **Finding in one line: the
model and its enforcement are sound and complete — org/team caps (D19–D21),
Argon2id + login throttle + session TTLs, deleted-user session/grant
invalidation, and full `bx org/team/access/user` CLI coverage. Every gap below
is UX / onboarding / credential-delivery, not missing enforcement.** So this is
a polish-and-flows plan, not a redesign.

Two credential planes to keep in mind (they shape the onboarding story): the
**owner/root token** (`.xbin/token`, a bootstrap admin credential, printed as a
`/login?token=` URL to the server console at startup) and **human users**
(`data/users.json`, Argon2id, password login). No users configured ⇒ single-user
mode where the root token is the only principal.

## The problem, grouped

### A. Onboarding & credential delivery — the biggest hole
- **No invite flow at all.** `POST /users` *requires* the admin to set a
  password (`usersapi.go:230`, `cmd/bx/user.go:145`, `admin.js:1590`); the admin
  then hand-delivers username+password out of band. Nothing tokenized/per-user
  is ever generated — only the *shared* owner token gets a URL (`main.go:714`).
- **Nothing is surfaced after creating a user.** `_createUser` just resets the
  form (`admin.js:1596`) — no "here's Bob's sign-in link / temp password to
  send." (Contrast the owner-token rotate, which at least `prompt()`s the value.)
- **New non-admins are dropped onto admin-only tiles.** The first screen seeds
  `tiles/admin` and `tiles/manager` for *everyone* (`root/index.html:33,36`); a
  non-admin hits a CLI-worded wall — "No admin access… run `bx grant tiles/admin
  xbin:admin`" (`admin.js:682`) — or a create-time 403 (`manager/index.html:240`).
- **A member with no tiles yet sees a blank workspace** — empty sidebar +
  "open a tile from the sidebar" (`bx-shell.js:1548,1566`), no hint of who to ask
  or what they can reach.
- **No first-run/hardening prompt.** Creating the first admin user and disabling
  token login is a buried checkbox (`admin.js:1638`); startup only prints the
  reusable token URL.
- **Login-page copy is wrong**: "one-time token URL from the server logs"
  (`login.go:106`) — it's neither one-time nor per-user. The page is also a
  hardcoded Go string, dark-only, not themed with the workspace.

### B. Self-service — members can do almost nothing themselves
- **Owners can't share their own tiles.** `PUT /access` + the ⚙ panel require
  workspace/org-admin (`orgsapi.go:296`, `bx-shell.js:1062`); a user who *created*
  a tile (and holds `terminal` on it) cannot grant a teammate from the UI — the ⚙
  isn't even shown. **The single biggest sharing rough edge.**
- **No self-service password change and no forgot-password** (whoami is
  read-only; no `POST /account/password`).
- **No "my access / my memberships" view.** whoami already carries `orgs`/`tiles`
  (`usersapi.go:68`) but nothing renders it for a non-admin.
- **Per-tile ACLs are admin-only to view** (`orgsapi.go:296`); pattern/base
  entries are read-only in the tile panel with only a prose pointer to bx.
- **Access map is admin-only** (`orgsapi.go:430`) and renders empty on a
  root-token-only workspace.

### C. User lifecycle
- **No disable/suspend — only destructive delete** (`usersapi.go:335`), which
  also wipes the user's per-tile ACL rows (`users.go:437`). To pause one person
  you must delete + recreate, losing their grants. `User` has no `active` flag.

### D. Admin-console polish
- **Password-reset success is shown as an error** — `this._err = 'password reset
  for …'` rendered in the red `.err` style (`admin.js:1610,703`).
- **Reset / rotate / delete use browser `prompt()`/`confirm()`** with no
  validation; a freshly rotated owner token appears in a `prompt()` you must copy
  before dismissing or lose (`admin.js:1605,1663`; `bx-tile-admin.js:202`).
- **Admin-denial detection is a fragile string match** —
  `String(e.message).includes('admin')` (`admin.js:456`) — so a 503
  store-unavailable renders as a raw error banner instead of the denial panel.
- **Two subtly-different org UIs** (admin tile `_orgCard` vs shell
  `bx-org-admin`) whose capability differences live only in footnotes; the
  org-admin surface only appears *after* you already administer an org
  (`bx-shell.js:1550`) — chicken-and-egg for the first delegation.
- **Create-in-team is invisible** until teams exist and you're a member
  (`manager/index.html:222`), with no hint otherwise.

## The fixes

### 1. Invite flow + credential delivery *(A)*
`POST /users` accepts **no password** → the store mints a single-use, TTL'd
**invite token** (hashed in `users.json`, `used`+`expires` fields) and the API
returns an invite URL `/login?invite=<tok>`. A themed set-your-password page
(`GET /login?invite=`) verifies+consumes the token and sets the user's first
password (`POST /login/invite`). Admin-set password stays supported (checkbox:
"set a password" vs "send an invite link"). After create, the admin tile shows
the invite URL in a **copy-to-clipboard field** (reuse `bx-dialog`), and
`bx user add` prints it.
*Touch:* `internal/users` (invite token: mint/verify/consume + expiry),
`usersapi.go` (create returns invite; new consume+set-password route),
`internal/server/login.go` (invite page, themed), `admin.js` `_createUser`,
`cmd/bx/user.go`. → **new decision D22 (invite tokens).**

### 2. Owners can share their own tiles *(B)* — needs a decision
Allow a caller with `terminal` level on a tile to `PUT /access` for **exact**
user/team entries, **clamped to ≤ their own level** and still subject to org
policy ceilings; show the ⚙ access section to them. Mirrors "you can share a repo
you admin." This is a real policy change (a non-admin granting access), so it
needs its own decision + tests: clamp level, exact-entries-only, org-clamp for
org tiles, ceiling still wins.
*Touch:* `orgsapi.go` (`/access` GET+PUT gate: allow terminal-holder, clamp),
`bx-shell.js:1062` (show ⚙ for terminal-holders), `bx-tile-admin.js` (editable
for them). → **new decision D23 (creator-sharing).**

### 3. Stop dropping non-admins onto admin tiles + friendly first-run *(A)*
Filter seeded first-screen tiles by the viewer's access (the sidebar already
read-filters components; the seeded *screen* should skip tiles the user can't
use). Reword the admin/manager denials to human copy ("You don't have admin
access — ask your workspace admin," no `bx` command). Add a member "no tiles
yet" empty state naming their admin / linking their "my access" view (fix 5).
*Touch:* shell `_ensureScreen` / seed handling, `admin.js:682` copy,
`manager/index.html` pre-check, `bx-shell.js` empty state.

### 4. First-run hardening prompt *(A)*
When whoami shows single-user/token mode, show a one-time dismissible setup
checklist in the shell: **① create your admin account → ② disable token login.**
Deep-links into the users tab.
*Touch:* `bx-shell.js` (whoami probe already exists), a small setup card.

### 5. Member self-service surface *(B)*
A "my account" section in the shell 🔧 menu: renders whoami (your orgs/teams,
your tiles + levels) and a **change-password** form backed by a new
`POST /account/password` (verifies current password; not admin-gated).
*Touch:* `usersapi.go` (self password-change), `bx-shell.js` settings menu.

### 6. User disable/suspend *(C)*
Add `Disabled bool` to `User`; auth rejects disabled users (like the existing
per-workspace `tokenLoginDisabled`, but per-user) while **keeping their ACL
rows**. Toggle in the users tab; `bx user set <id> --disable/--enable`.
*Touch:* `users.go` (field + checks), `auth.go` (reject), `usersapi.go` PATCH,
`admin.js`, `cmd/bx/user.go`.

### 7. Admin-console polish *(D)*
Success vs error slots (reset success in a green notice, not `.err`); replace
`prompt()`/`confirm()` with `bx-dialog` + copy-field for tokens/invites; make the
API client carry the HTTP **status** so denial detection checks `403` not a
substring; add a one-line "org admins edit here / workspace admins there" header
linking the two org UIs, and make the org-admin button reachable; add a hint that
create-in-team exists.
*Touch:* `admin.js` (notice slot, dialogs, status-aware client, copy),
`bx-tile-admin.js`, `bx-org-admin.js`, `manager/index.html`.

## Suggested build order

- **M1 — Onboarding (unblocks self-hosted multi-user; do first):** fix 1 (invite +
  delivery), fix 3 (no admin tiles for non-admins + friendly denials), fix 4
  (first-run prompt), login-copy/theme fix. This is what makes "add a teammate"
  actually work for a self-hoster.
- **M2 — Self-service & sharing:** fix 2 (owner-can-share, D23), fix 5 (my
  account + change password + my access), member-visible ACL.
- **M3 — Lifecycle & polish:** fix 6 (disable), fix 7 (dialogs, success slots,
  robust denial detection, org-UI signposting, create-in-team hint).

## Decisions to confirm before building
- **D22 — invite tokens:** single-use, short TTL (e.g. 72h), hashed at rest,
  admin-set-password still allowed. (Recommended.)
- **D23 — creator sharing:** may a non-admin `terminal`-holder grant others
  access to that tile, clamped to ≤ own level + org ceilings? (Recommended yes —
  it's the difference between "usable multi-user" and "everything routes through
  an admin.") If no, M2 shrinks to read-only self-service.
- **Disable semantics:** confirm a disabled user keeps ACL rows and simply can't
  authenticate (recommended), vs. a softer "no login but sessions live until
  TTL."

## Non-goals (kept out on purpose)
SSO/OIDC, email delivery of invites (we surface a *link* the admin sends however
they like — no SMTP dependency), and any change to the capability model or
enforcement. Those can come later; none block open-sourcing.
