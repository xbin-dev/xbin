# sandbox-terminal — terminals onto coding sandboxes, for people

Terminals onto the coding sandboxes of one or more **sandbox managers**
([/docs/sandbox-manager.md](/docs/sandbox-manager.md)), in the browser and
over SSH (D121). The tile is a **consumer** of the contract that creates
nothing: a sandbox reaches it by being **shared** with it — a share naming
this tile (`apps/sandbox-terminal`), for everyone (`"*"`) or a list of
people — or by being its own.

## Setting it up

1. **Bind it to the managers** whose sandboxes it opens:
   `bx bind apps/sandbox-terminal sandboxes=apps/coding-sandbox` (more:
   `--add`). The binding grants this tile the managers' `consumer` role.
2. **Share sandboxes with it.** From the agent (its Sandboxes dialog →
   **Share with a terminal tile…** on a sandbox you own: for you, or for
   everyone who may use it when it is a team sandbox), from the manager's
   own page, or any consumer that owns one: `PATCH …/sbx/sandboxes/{id}`
   with `{"shares": [{"consumer": "apps/sandbox-terminal", "users": "*"}]}`.
3. **For SSH, publish the port** (an admin):
   `bx expose apps/sandbox-terminal ssh=runtime --listen :2222`. People
   register their public keys on the tile's page, and a manager of the tile
   (write access) sets the address people use (`PUT /settings`), which the
   page shows in the `ssh` command.

## Who may use a sandbox here

The contract's person rules, which this tile enforces for the people it acts
for: on a sandbox shared with this tile, the share must name the person
(`"*"` names everyone); then the person must be its **owner**, one of its
**members**, or the sandbox must be **team**. A sandbox that isn't shared
with this tile doesn't exist here.

- **In the browser** the page dials the manager's `tty` route itself with its
  frame token (`<bx-terminal src="<manager url>/sbx/sandboxes/{id}/tty?cwd=…">`),
  so the manager checks the **verified** person. This backend isn't in the
  path.
- **Over SSH** the key says who the person is, and this backend calls the
  manager naming them in `Sbx-User` (an **asserted** person: the manager
  records it, and trusts this tile to have checked the rules above). No xbin
  identity reaches a sandbox; only a terminal's bytes cross.

## Sandboxes of a partitioned agent

An agent may be partitioned — one instance per person, plus a global one;
new copies of the agent template are, by default
([/docs/partitions.md](/docs/partitions.md)). A manager that knows it
(`partitions` in its `hello.caps`) keeps each person's partition apart
([/docs/sandbox-manager.md](/docs/sandbox-manager.md) §Partitioned
consumers): a sandbox made in alice's private conversation is homed in her
partition, and nothing else sees it. This tile isn't partitioned — one SSH
port, one key store keyed by person — and is one consumer,
`apps/sandbox-terminal`, as before:

- **Such a sandbox reaches this tile only when it is shared with it**, for
  its person: the agent's **Share with a terminal tile…** on it shares it
  `{"consumer": "apps/sandbox-terminal", "users": ["alice"]}` from her
  partition. Then alice (its owner) opens it here, in the browser and over
  SSH, as ever; nobody else does — the person rules above still apply. Her
  partition can take the share away again.
- **The global instance's sandboxes** (shared conversations, team
  sandboxes) are shared as before, `"*"` for a team sandbox.
- **Keys stay per person**, as they already were: a key logs in only its
  own person, and only while xbind says they may use this tile.
- **Who is in whose trust base.** This tile's backend reaches every sandbox
  shared with it, for SSH, as an asserted person the manager trusts it to
  have checked. So its writers — who can change its code — and admins are
  in the trust base of every person who shares a sandbox with it: sharing
  is that person's choice. A manager of the tile sees key metadata
  (names, fingerprints, last use) and live sessions, never a terminal.
- **Binding** it to managers (`bx bind apps/sandbox-terminal
  sandboxes=…`) is an ordinary bind, made by whoever may bind today — an
  admin, an org admin within their org, a personal tile's owner to what
  they own. A personal bind doesn't apply: this tile isn't partitioned, so
  it has one wiring for everyone.

## The page

- **Sandboxes**, grouped by the manager they are on (a manager that didn't
  answer says why): state, private or team, owner, the consumer that shared
  it (`from apps/agent`), image and network, and its `ssh` command once SSH
  is published. **Open terminal** starts the sandbox user's login shell at
  its workdir (a stopped sandbox starts; an archived one says to thaw it in
  its manager) in a new tab; several can be open, on one sandbox or many.
- **Terminals are the manager's, not the page's.** A tab is `<bx-terminal
  src>` on the manager's route, dialled by the page with its frame token.
  **✕** closes the tab and leaves the shell running; **End** ends it
  (`DELETE …/execs/{id}` at the manager, confirmed); ⤢ makes it larger.
  Each sandbox lists its **running terminals** — the manager's tty execs
  labelled `terminal`, read as you (`GET …/execs`), so after a reload or
  from another browser **Attach** opens one again (its screen replays,
  then it goes on) and **End** ends one. They are everyone's who may use
  the sandbox: the agent's Open terminal and SSH logins with a terminal
  are there too.
- **SSH**: whether people can log in — the server isn't running (why), or
  the port isn't published yet (the `bx expose` an admin runs; the tile
  can't see xbind's port binding, so "published" means a manager of the
  tile has set the address people type) — the host key's fingerprint and
  `known_hosts` line, **your keys** (add one by pasting its `.pub`;
  remove), and your live SSH sessions. A manager of the tile also sets the
  address, and sees and revokes **everyone's keys** — a key whose person
  lost access to the tile says `inactive`.
- **Empty states** say how sandboxes get here: bind a manager, then share
  a sandbox with this tile (the agent's Sandboxes → Share with a terminal
  tile…, or the manager's own page).
- Viewing the workspace as someone (D64) shows their list read only: no
  terminals, no key changes.

What the page shows is `sbxterm.js` (no DOM; `index.html` and `native.js`
both draw from it).

## The native view (the xbin app)

`native.js` shows the same list, a detail screen per sandbox (its `ssh`
command, the terminals running in it — **End** behind the swipe), and a
keys screen (add, remove, the host key, your live SSH sessions; a manager
sets the address, and sees and revokes everyone's keys). It
opens **no terminal** — a difference from the page (D96): the app's
`terminal` primitive dials only this tile's own routes, and a sandbox's
terminal is its manager's; relaying it through this tile's backend would
turn the verified person the manager checks into an asserted one. So where
a terminal would be it says so and offers **Open in the browser**: the
workspace the app reached, opened outside the app when this tile holds
`cap:open-links`, and otherwise copied to paste into a browser.

## SSH

```
ssh api-dev@sbx.example.com -p 2222          # a login shell, in a terminal
ssh api-dev@sbx.example.com -p 2222 make test  # a command, without a terminal
ssh -t api-dev@sbx.example.com -p 2222 htop  # a command, in a terminal
```

- **The user name picks the sandbox**: its *login* — its name in lower case
  with every run of characters other than `a-z 0-9 . _ -` turned into one
  `-` (`API dev` → `api-dev`) — or its id. When a name gives several
  sandboxes you may use, they are `<name>~1`, `<name>~2`, … (in the managers'
  order, then by age), and logging in as the bare name lists them with their
  ids. A name never gives a `~`, so these can't be another sandbox's own
  login (a sandbox named `web.1` is `web.1`, the second of two `web`s is
  `web~2`). An unknown name lists the sandboxes you may use. `GET /sandboxes`
  gives every sandbox's login.
- **The key** is one you registered on the tile's page (below): an ed25519,
  ECDSA, security-key (`sk-…`) or RSA (2048 bits or more) public key.
  Certificates and `authorized_keys` options aren't taken. A key belongs to
  one person, and logs them in only while they may use this tile (below).
- **Access is checked at every login** — and for every session a
  connection opens, and every 30 s while it lives: xbind says what the
  key's person may do on this tile now (`GET /api/xbin/access/<user>`,
  kept 30 s). Someone removed from the workspace, disabled, or taken off
  this tile is told `access revoked` (exit 1) and nothing runs; a
  connection they have open is cut, its sessions told why. Their keys are
  **kept, marked inactive** — access may come back, and the next login
  (or visit to the page) that finds it marks them active again. When xbind
  doesn't answer, logins fail closed (`can't be checked right now`); a
  live connection isn't cut over a check that failed.
- **With a terminal** (a login shell, or `ssh -t`) the session is the
  manager's `tty` route: the login shell, or the command, in a
  pseudo-terminal of the requested size; resizing follows the window; the
  exit status is the command's. A manager without `tty` runs it without a
  terminal, and says so.
- **Without one** (`ssh host cmd`, `ssh -T host < script`) the command runs
  as the manager's background exec with stdin: input is posted to it,
  output read back exactly — **stdout and stderr arrive together on
  stdout** (the contract keeps one stream). A shell without a terminal is
  the sandbox's shell as a login shell reading its script from stdin.
- **A stopped sandbox starts** on the login; an archived one says to thaw it
  in its manager.
- **Leaving.** When the client disconnects while the command runs, the
  command's process group gets `HUP`, and is killed if it still runs two
  seconds later. Work that should outlive the connection belongs in its own
  session (`setsid`, `tmux`).
- **Not in v1:** port forwarding (local and remote), agent forwarding, X11,
  `sftp` (and so today's `scp`), environment variables from the client.
- **The host key** is an ed25519 key made on first start and kept in the
  tile's vault; `GET /me` shows its fingerprint and `known_hosts` line.
- **Limits.** 10 s to authenticate, 6 attempts a connection, 10 sessions
  a connection. At most 32 connections in their handshake at once; a new
  one over that drops a **random older** one still in its handshake
  (randomized early drop) rather than being refused, so connections that
  never finish can't keep people out. Failed key attempts are rate-limited
  per source address **and user name** (a burst of 20, then one every 2 s;
  an attempt over the rate is answered 2 s late — a tarpit, never a
  lockout: a good key is never slowed). xbind's port relay presents one
  source address inside the sandbox, so keyed by the name too, a flood of
  bad keys against one name doesn't slow anyone else's.
- **Revoking a key** (the person, or a manager of the tile) ends that key's
  live connections too.

## Routes (the tile's own page, with its frame token)

Every route answers the tile's own page (the verified person, with their
access level to the tile) and the owner token. A **manager** of the tile is a
person with write or terminal access to it, or the owner. An admin viewing
the workspace as someone (D64) reads, and changes nothing. A person whose
access to the tile is gone (a frame token outlives it a while: xbind sends
no level) reads `GET /me` and their own keys, removes them, and gets 403
everywhere else.

| Method & path | Body | Result |
|---|---|---|
| `GET /me` | — | `{user, level, manager, viewedBy, self, managers: [provider…], keys: n, ssh}` — `ssh` is `{port, address, listening, error?, hostKey: {type, fingerprint, publicKey}}` |
| `GET /keys` | — | `{keys: [key…]}` — the caller's own |
| `GET /keys?all=1` | — | everyone's (managers) — each person's access asked of xbind first (cached), so `inactive` is current |
| `POST /keys` | `{publicKey, name?}` | **201** + the key (**200** when the caller already registered it); `name` defaults to the key's comment. 400 for anything but one plain public key, 409 for a key someone else registered or past 20 keys a person, 403 when xbind says the caller may no longer use the tile, 503 when it didn't answer |
| `DELETE /keys/{id}` | — | **204** — the caller's own, or anyone's for a manager; ends the key's live SSH connections |
| `GET /sandboxes` | — | `{managers: [{provider, title, tty, error?}], sandboxes: [sandbox…], ssh}` — the sandboxes the caller may use, on every bound manager |
| `GET /sessions` | — | `{sessions: [{user, login, provider, sandbox, name, kind: terminal\|command\|starting, remote, started, key}]}` — the caller's live SSH sessions; `?all=1` everyone's (managers) |
| `PUT /settings` | `{sshAddress}` | the settings (managers) — `sshAddress` is `host` or `host:port`, what people type |

A **key** is `{id, user, name, type, fingerprint, publicKey, added,
lastUsed?, inactive?}`: `id` is the key's SHA-256 in base64url (the route's
`{id}`), `fingerprint` is `SHA256:…` as `ssh-keygen -l` prints it, times are
unix milliseconds (`lastUsed` is updated at most every ten minutes).
`inactive` is when xbind last said the key's person may no longer use this
tile: the key logs nobody in while that holds, and loses the mark once a
check finds their access back.

A **sandbox** in `GET /sandboxes` is `{provider, id, name, login, state,
owner, via, visibility, members, shared, egress, isolation, image, workdir,
tty, created}`: `provider` is the manager as the binding names it
(`apps/coding-sandbox`, `apps/cs#eu` for an instance) — the page finds its
URL in `xbin.iface("sandboxes")` — and `tty` says a browser terminal can
open (the manager and the sandbox offer `tty`).

## Storage

`res:apps/sandbox-terminal/state` (kv): `keys` (the registered keys) and
`settings`. The SSH host key is the vault's `ssh-host-key`. What a manager
says is never stored: hellos are cached for a minute, sandbox lists not at
all. What xbind says of a person's access is kept 30 s, in memory.
