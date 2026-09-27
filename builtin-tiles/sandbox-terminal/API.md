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
  address, and sees and revokes **everyone's keys**.
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
keys screen (add, remove, the host key; a manager sets the address). It
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
  sandboxes you may use, they are `<name>.1`, `<name>.2`, … (in the managers'
  order, then by age), and logging in as the bare name lists them with their
  ids. An unknown name lists the sandboxes you may use. `GET /sandboxes`
  gives every sandbox's login.
- **The key** is one you registered on the tile's page (below): an ed25519,
  ECDSA, security-key (`sk-…`) or RSA (2048 bits or more) public key.
  Certificates and `authorized_keys` options aren't taken. A key belongs to
  one person.
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
- **Limits.** Failed key attempts are rate-limited per source address (a
  burst of 20, then one every 2 s; an attempt over the rate is answered
  2 s late — a good key is never slowed), at most 32 logins in flight, 30 s
  to authenticate, 6 attempts a connection, 10 sessions a connection.
  xbind's port relay presents one source address inside the sandbox, so
  behind it the rate is shared.
- **Revoking a key** (the person, or a manager of the tile) ends that key's
  live connections too. A person who loses access to the tile keeps their
  keys until they or a manager remove them — xbind has no way for a tile to
  ask whether someone still has access to it.

## Routes (the tile's own page, with its frame token)

Every route answers the tile's own page (the verified person, with their
access level to the tile) and the owner token. A **manager** of the tile is a
person with write or terminal access to it, or the owner. An admin viewing
the workspace as someone (D64) reads, and changes nothing.

| Method & path | Body | Result |
|---|---|---|
| `GET /me` | — | `{user, level, manager, viewedBy, self, managers: [provider…], keys: n, ssh}` — `ssh` is `{port, address, listening, error?, hostKey: {type, fingerprint, publicKey}}` |
| `GET /keys` | — | `{keys: [key…]}` — the caller's own |
| `GET /keys?all=1` | — | everyone's (managers) |
| `POST /keys` | `{publicKey, name?}` | **201** + the key (**200** when the caller already registered it); `name` defaults to the key's comment. 400 for anything but one plain public key, 409 for a key someone else registered or past 20 keys a person |
| `DELETE /keys/{id}` | — | **204** — the caller's own, or anyone's for a manager; ends the key's live SSH connections |
| `GET /sandboxes` | — | `{managers: [{provider, title, tty, error?}], sandboxes: [sandbox…], ssh}` — the sandboxes the caller may use, on every bound manager |
| `GET /sessions` | — | `{sessions: [{user, login, provider, sandbox, name, kind: terminal\|command\|starting, remote, started, key}]}` — the caller's live SSH sessions; `?all=1` everyone's (managers) |
| `PUT /settings` | `{sshAddress}` | the settings (managers) — `sshAddress` is `host` or `host:port`, what people type |

A **key** is `{id, user, name, type, fingerprint, publicKey, added,
lastUsed?}`: `id` is the key's SHA-256 in base64url (the route's `{id}`),
`fingerprint` is `SHA256:…` as `ssh-keygen -l` prints it, times are unix
milliseconds (`lastUsed` is updated at most every ten minutes).

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
all.
