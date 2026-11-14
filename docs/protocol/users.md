# Protocol: identity, users and sign-in

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page: who the caller is and who may sign in — frame tokens, path
tickets, `whoami`, view-as, user accounts, passwords, invites, sessions and
the sign-in settings (SSO, the owner token).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Identity and tokens


```
GET    /frame-token?component=<p>  a principal that may use the tile: humans
                                   (cookie) any tile they can read; a tile
                                   frontend its OWN component — including
                                   cookie-less (sandboxed frames renew with
                                   their token alone); never a backend's
                                   instance token (403: a token with no
                                   person behind it would read as the
                                   owner's frame). {token} — bound to
                                   the caller's login (a renewal keeps its
                                   token's binding; see Authentication).
                                   &deployment=<name>: a person asks for a
                                   deployment's token (write on the tile for a
                                   non-primary one, checked at every
                                   renewal); a tile renews only its own bound
                                   deployment's, and the renewal keeps it.
                                   Without it a person gets the primary's
                                   token (a six-field one when the primary
                                   isn't main); a session that follows the
                                   primary gets the current primary's. The
                                   answer echoes deployment when one was
                                   named. 400 a malformed name, and a
                                   component= that names a deployment as
                                   tile+name (D127j); 403 "deployment URLs
                                   need write access on <tile>" for a
                                   non-primary deployment and a caller
                                   without write, whether or not it exists
                                   (a frame bound beyond the primary whose
                                   user no longer writes the tile
                                   included), and "a tile's own credentials
                                   act only on their own deployment
                                   (<bound>)"; 404 unknown
POST   /path-tickets              a tile's page (its frame token) only.
                                   {path: "<prefix>"} → {url: "/api/~<ticket>/",
                                   expires (unix ms)}: a path ticket (D135,
                                   below) to a prefix of the page's own
                                   tile's API. The prefix is a relative path
                                   of unreserved characters (A–Z a–z 0–9
                                   . _ ~ -), no . or .. segment, ≤ 256 bytes
                                   (400 otherwise); anyone but a tile's page
                                   is 403. The url is used as
                                   /api/~<ticket>/<rest> (§Path tickets)

GET    /whoami                    any. caller identity + permissions; for
                                   users also orgs:[{id,name,level,create,
                                   admin,suspended?,via?,viaGroups?}]
                                   (the self-service membership view), and
                                   tileCreation: any|org-only — the
                                   workspace's tile-creation policy (D52)
                                   — and personalTiles: may THIS caller
                                   (the human behind a tile call too) own
                                   tiles personally, the policy and their
                                   account's noPersonalTiles folded in
                                   (D88; owner pickers adapt to it). A
                                   signed-in non-admin also gets personal:
                                   {sets, netSets, netRules, allow} — their
                                   resolved personal plane. An admin's
                                   view-as session adds impersonatedBy
                                   (owner | admin id) and readOnly:true
                                   (D64; the shell's banner). On
                                   element principals driven by a signed-in
                                   human, `user` reports the driver, SCOPED
                                   by the tile's trust (docs/auth.md): every
                                   tile gets {id,name}; a tile inside an org
                                   additionally gets that one org's
                                   membership slice; a workspace-management
                                   tile (xbin / xbin:users capability) gets
                                   {admin, orgs:[…]} in full — the element's
                                   own privilege is unchanged either way.
                                   Every caller also gets native:
                                   {runtime: 1} — this xbind serves native
                                   runtime documents (/c/<tile>/?native=1)
                                   — or {runtime: 0, disabled: true} while
                                   an admin has turned native tile UIs off
                                   (PUT /native-runtime): the app opens
                                   every tile as its web page.
                                   A tile credential bound to a deployment
                                   other than the primary also gets
                                   deployment, its name (a session that
                                   follows the primary gets none)

POST   /impersonate               admin. {user} → {url, user, expiresIn}:
                                   a one-shot ticket (2 min) to VIEW the
                                   workspace as that user (docs/auth.md
                                   §Viewing the workspace as a user, D64).
                                   Open url top-level in the same browser —
                                   it swaps the cookie for a read-only
                                   session as the user (impersonatedBy in
                                   /whoami and /sessions; every write 403,
                                   terminals too). Bound to the minting
                                   admin's browser; not yourself, not a
                                   disabled account, no nesting
POST   /impersonate/stop          a view-as session. ends the view, hands
                                   the browser back to the admin's own
                                   session → {ok, restored} (restored:false:
                                   it had expired — sign in again). POST
                                   /logout from a view does the same
```

## Users, sign-in and sessions


```
GET    /users                     admin or xbin:users. [{id,name,role,
                                   tiles:{path:level}, termApi, termNet,
                                   canCreate (deprecated, ignored — D82),
                                   noPersonalTiles?, noTerminal?, sets?,
                                   netSets?, personal? (the resolved plane
                                   for non-admins: {sets, netSets,
                                   netRules, allow} — D88),
                                   disabled?, invitePending?,
                                   deviceCount? (enrolled app devices),
                                   deviceLimit (their effective limit),
                                   maxDevices? (their override, D204),
                                   email?, roleVia?, lastLogin?,
                                   lastLoginVia?, lastSSO?, ssoGroups?,
                                   ssoSyncError?}] — levels
                                   read|write|terminal (docs/auth.md, D16).
                                   Sign-in facts (D53): lastLogin (unix) +
                                   lastLoginVia (password|invite|sso|
                                   device); lastSSO (unix) = the last SSO
                                   sign-in;
                                   ssoGroups = the IdP groups seen at the
                                   last SSO sign-in; ssoSyncError = the
                                   last group-fetch failure; roleVia "sso"
                                   = admin by group rule
POST   /users                     admin/xbin:users. create a user: {id,
                                   name?, role?, email?, tiles?,
                                   termApi?, termNet?, password? | sso?,
                                   orgs?: [{org, level, create, admin}],
                                   noPersonalTiles?, noTerminal?, sets?,
                                   netSets?}. The personal plane (D88):
                                   unknown sets refuse before anything is
                                   created; the switches are OR'd with the
                                   seed's (a request can't lift them).
                                   canCreate? is still accepted (and
                                   PATCH-able) but deprecated and ignored
                                   (D82). orgs joins the account at creation
                                   (validated first — a bad org creates
                                   nothing; D53) → response adds orgs.
                                   Under SSO-only mode a non-admin needs
                                   sso:true or a password (409 otherwise).
                                   WITH password → ready to sign in;
                                   WITHOUT → credential-less account + a
                                   single-use invite link the admin
                                   delivers: {user, invite, inviteUrl:
                                   /login?invite=…, inviteExpires} (72h,
                                   D22); sso:true (email required) →
                                   credential-less with NO invite — the
                                   bound email's IdP sign-in is the
                                   credential (SSO pre-provisioning, D52).
                                   Every new account is seeded with the
                                   new-account defaults (/defaults
                                   newUsers) on top of the given fields.
                                   There is NO self-signup — accounts only
                                   come from here. (id: [a-z0-9._-],
                                   immutable; password ≥ 8; the legacy body —
                                   tiles array + terminal bool — is still
                                   accepted and migrated)
POST   /account/password          signed-in users. {current, new,
                                   removeDevices?} — self-service rotation;
                                   verifies the current password (D38).
                                   removeDevices:true also removes the
                                   caller's app devices (all but the one
                                   calling) → {ok, devicesRemoved}
POST   /login                     none — the password is the credential.
                                   The native app's sign-in: {username,
                                   password} → the app token response
                                   ({token, tokenType, user, expiresIdle,
                                   expiresMax}, as POST /login/device).
                                   Same rules as the form login: throttled,
                                   disabled accounts refused, SSO-only mode
                                   (D53) refuses non-admins (403)
GET    /login/methods             none — public, not throttled. The
                                   native app's sign-in discovery: what
                                   the login page offers, as data →
                                   {api:1, title, auth, password:{enabled,
                                   adminOnly}, sso:{enabled, label},
                                   invites}. title: the branding title,
                                   else "xbin". auth:false in no-auth
                                   mode (everything else off then).
                                   password.enabled: the workspace has
                                   accounts; adminOnly: SSO-only mode
                                   (D53). sso.enabled: the login page's
                                   SSO button shows (configured and
                                   --external-url set), label its text.
                                   invites: invite links can be redeemed
                                   (below). tunnel: {enabled, required} —
                                   the app tunnel is on, and the app's
                                   traffic is refused off it (D204; absent
                                   on an older xbind). Cache-Control:
                                   no-store; api
                                   versions the shape (fields only added)
POST   /invite/check              none — the invite is the credential.
                                   {invite} (the token of an invite link
                                   <origin>/login?invite=<token>) →
                                   {user:{id, name}, expires, title}: whom
                                   it is for, without spending it. 403
                                   {error:"invalid or expired invite"}:
                                   unknown, used, expired or a disabled
                                   account's (throttled; failures count)
POST   /invite/redeem             none — the invite is the credential.
                                   {invite, password} → the app token
                                   response (as POST /login): the invite
                                   form (POST /login/invite) for the app —
                                   sets the first password, spends the
                                   single-use invite, signs in (Via
                                   "app", last sign-in via "invite";
                                   audit-logged). 400 {error}: the password
                                   fails the policy (min 8 characters) —
                                   the invite is NOT spent; 403 as above;
                                   409 {error: "waiting for <person> to
                                   confirm…"}: a link held for its person
                                   (docs/partitions.md §Credential resets;
                                   stored held, so an xbind without the
                                   check never redeems it),
                                   not spent; 429 throttled

POST   /users/<id>/invite         admin/xbin:users — or an ORG ADMIN for a
                                   non-admin member of their org
                                   (delegated reset-by-link, D38).
                                   (re)mint an invite link
                                   for an existing user — credential delivery
                                   or reset-by-link; re-minting invalidates
                                   the previous link; the current password
                                   keeps working until redemption. → {invite,
                                   inviteUrl, inviteLink (absolute, from the
                                   request host), inviteExpires}. 409 for a
                                   non-admin under SSO-only mode (D53).
                                   For a person who holds partitions
                                   (docs/partitions.md §Credential resets)
                                   the link is audited and the person told
                                   (push account.credential, a notice);
                                   with the workspace policy
                                   credentialResetConfirm on it is minted
                                   held — the answer adds held: true,
                                   heldUntil — and its redemption answers
                                   409 "waiting for <person> to confirm…"
                                   (the web form shows it) until the person
                                   allows it or 24 h after they were told
PATCH  /users/<id>                admin/xbin:users. update — present fields
                                   overlay (+password reset). {disabled:
                                   bool} suspends/restores the account
                                   (D34): login, sessions, tokens and
                                   invite redemption refuse while set;
                                   rows/memberships/ownership stay.
                                   Guarded: not yourself, not the last
                                   enabled admin. The personal plane
                                   overlays by presence (D88):
                                   noPersonalTiles, noTerminal (switching
                                   it on ends the user's live terminal and
                                   agent sessions), sets, netSets (a change
                                   restarts their net tiles). maxDevices:
                                   n (0–32) overrides the workspace device
                                   limit for them, null clears it (D204).
                                   A password or
                                   SSO email set for someone else who holds
                                   partitions is audited and they are told;
                                   with credentialResetConfirm on it is held
                                   instead of applied (the old one keeps
                                   working) and the answer carries
                                   X-XBin-Credential-Held: password,email
                                   (docs/partitions.md §Credential resets).
                                   Disabling a person (or a change that
                                   takes their read on a tile) stops their
                                   running partition instances
DELETE /users/<id>                admin/xbin:users. remove (revokes
                                   sessions) → {ok, orphanedTiles: […]} —
                                   tiles that fell to workspace-owned, so
                                   the handover is explicit. A partition
                                   holder's instances stop and their
                                   partitions are orphaned (swept after 30
                                   days, or purged); their personal binds,
                                   consents, notices and held credentials
                                   go; homes/<id> and data/agent-history/<id>
                                   move to data/orphans/<id>-<uid8>/
DELETE /users/<id>/sessions       admin/xbin:users. "sign out everywhere"
                                   (D53): ends every browser and app
                                   session, terminal token and frame token
                                   of the user and voids their pending
                                   enrollment codes → {ok, dropped,
                                   devicesLeft, devicesRemoved?}. They can
                                   sign in again — disable the account to
                                   stop that. Enrolled app devices stay
                                   (devicesLeft of them, able to sign in
                                   without a password) unless ?devices=1
                                   removes them too
GET    /sessions                  admin/xbin:users. {sessions: [{user, name,
                                   created, lastActive, ip, lastIP,
                                   current, impersonatedBy?, via,
                                   device?}]} — live login sessions (via:
                                   session = a browser, device = the
                                   native app signed in with a device key
                                   — device names it — app = the app's
                                   password/SSO sign-in; a browser the app
                                   signed in (POST /web-ticket) is session
                                   with device set, created = the device
                                   login's; impersonatedBy: an
                                   admin's read-only view of the user, D64) with
                                   client IPs (login IP + last-seen IP),
                                   newest activity first; the caller's own
                                   row is marked current:true. Session ids
                                   are credentials and never returned.
                                   Stateless bootstrap token logins don't
                                   appear. This is the attribution view for
                                   the /c/ warm-IP gate (admin console →
                                   user management → sessions)
GET    /auth-settings             admin/xbin:users. {tokenLoginDisabled,
                                   hasAdminUser, canDisable,
                                   passwordLoginDisabled,
                                   canDisablePassword, sso: {enabled,
                                   ready, kind, preset, issuer, clientId,
                                   clientSecretSet, allowedDomains,
                                   buttonLabel, externalUrl, groupsClaim,
                                   groupsScope, adminGroups, groupSync:
                                   {rulesActive, knownGroups, lastError:
                                   {user, at, error}|null}}} — sign-in
                                   policy (docs/auth.md §SSO): the SSO
                                   config with the secret reduced to a
                                   bool, SSO-only mode (D53), and the
                                   group-sync status (every group the IdP
                                   has been seen sending, the newest
                                   recorded fetch failure)
POST   /auth-rotate-token         admin. Rotate the owner token: rewrites
                                   .xbin/token, old token dies immediately
                                   (bearer + cookie), with the frame tokens
                                   it opened and the owner's push
                                   registrations. → {token} (shown once).
PATCH  /auth-settings             admin/xbin:users. {tokenLoginDisabled?:
                                   bool, sso?: {kind, preset, issuer,
                                   clientId, clientSecret, allowedDomains,
                                   buttonLabel, groupsClaim, groupsScope,
                                   adminGroups}|null,
                                   passwordLoginDisabled?: bool}. Disabling
                                   token login requires a signed-in admin
                                   user (Bearer owner token unaffected); an
                                   sso object replaces the config (empty
                                   clientSecret keeps the stored one), null
                                   clears it; passwordLoginDisabled = SSO-
                                   only mode for non-admins — enabling
                                   needs a READY provider (409 otherwise).
                                   A new provider identity (kind, issuer,
                                   client id) is a credential for every
                                   partition holder bound by email: audited
                                   naming them, each told; with
                                   credentialResetConfirm their SSO sign-ins
                                   are held (?sso_err=held) until they allow
                                   it or 24 hours pass (docs/partitions.md
                                   §Credential resets)
POST   /auth-settings/sso/test    admin/xbin:users. probe the provider
                                   without a user (D53): OIDC discovery +
                                   JWKS, or GitHub API reachability. Body
                                   {} = the stored config, {sso: {…}} = an
                                   unsaved draft (empty clientSecret = the
                                   stored one). Always 200 → {ok, kind,
                                   issuer, redirectUri, externalUrl, ready,
                                   endpoints, jwksKeys, warnings, error}
```
