# W6-F — fabric: who the person is on a manager's sockets, their suite cases, AR-23

W6's fabric pack ([96-agtt-merge.md](../96-agtt-merge.md) §B1 F-M3, F-M4,
and the fabric part of §B4), on `pt/w6-base` (B0's merge). Branch
`pt/w6-f`. No xbind code, no new HTTP/WS surface: the contract text,
the conformance suite, the reference manager's test, the SDK's comments.
It edits neither `docs/changelog.md` nor `plans/DECISIONS.md`: their
texts are below.

Commits (oldest first):

- `35227227` sandboxcontract: `user-partitions/sockets` (F-M4), the fake's
  two skip lists, and the suite paragraph of docs/sandbox-manager.md and
  coding-sandbox API.md's "Testing on xbind".
- `04fe95cb` docs: from a user partition the person is the partition's,
  verified — terminals and stdio too; "§Consumers, sharing and people"
  everywhere AgTT cited the old name (F-M3).
- `a0c065f4` docs: stdio takeover and harness sign-ins in shared sandboxes
  (AR-23), the harness commands' trust base, coding-sandbox API.md's
  bullets (B4's fabric part).
- `333d7d6f` fakesandbox: its contract run requires `stdio` in hello.
- this record.

## F-M1 and F-M2 (B0's), verified

- **F-M1** (`4f57cd9b`): `GET /sandboxes/{name}/execs/{id}/stdio` is
  `GlobalOnlyRefused` in `internal/server/partitionclass.go`, beside
  `…/stdin`, `…/signal`, `…/resize`; `TestPartitionRouteClasses`
  (`internal/apicheck`) passes. A route without a class would have been
  refused to a person's partition anyway (fail closed); the row makes it
  the documented global-only refusal.
- **F-M2** (`43af5a2a`): `mutates` in coding-sandbox's `access.go` is
  AgTT's widened rule (any `Upgrade`, a path ending `/tty` or `/stdio`);
  `TestPageReaders` passes in `hack/tile-check.sh coding-sandbox` (race).

## F-M3 — who the person is

The rule is D140's (§Partitioned consumers: a user partition's person is
the partition's, verified; an `Sbx-User` naming anyone else is `403
not-allowed`). AgTT (D147) wrote the `tty`/`stdio` texts for an
unpartitioned consumer — "asserted, recorded, not verified", "the
partitions hold" in the sense of consumers. Now said, in each place the
plan lists and the ones next to them:

- docs/sandbox-manager.md: §Who is asking (the asserted bullet names the
  exception); §Consumers, sharing and people (the backend-call sentence);
  §Partitioned consumers' person bullet ("its backend's calls and sockets
  (`tty`, `stdio`) as much as its page's"); §stdio's "Who dials it" (who
  may, and the user partition's verified person); §Terminals (the
  backend bullet: consumers stay apart, and from a user partition the
  manager applies the person rules; the relay bullet cites §Consumers,
  sharing and people and says the manager applies the first check itself
  there, the terminal-at-all check staying the consumer's).
- docs/sdk.md: the consumer-terminals intro, "Your checks come first"
  (was "Your checks are the only ones about the person"), and the stdio
  paragraph.
- docs/changes/2026-09-30-manager-terminals-for-backends.md: "keeps
  consumers apart", the section's new name, the user-partition exception
  in "What changed" and in "How to migrate".
- `sdk/manager_tty.go` (file comment, `ManagerTTYOptions.User`),
  `sdk/manager_stdio.go` (file comment, `ManagerStdioOptions.User`: `""`
  there is the partition's person).
- `sdk/sandboxcontract/tty.go`: the `tty/backend` check's comment and
  `backendTTYWarn`'s.
- The old section name: sandbox-manager.md §Terminals, sdk.md, the
  migration note, and `plans/agtt-harness.md` §5.2's "§Partitions" (the
  only other citation; the changelog's quote of the old name is the
  rename's own history, left).

## F-M4 — the suite's socket cases

`user-partitions/sockets` (`sdk/sandboxcontract/partitions.go`), run when
hello offers `tty` or `stdio` (each half gated on its own):

- alice's partition makes a `team` sandbox, starts a tty exec and a split
  `cat` with stdin, and attaches the latter's stdio socket;
- on `…/tty`, `…/execs/{id}/tty` and `…/execs/{id}/stdio`: bob's partition
  (backend, page, backend naming bob), the consumer's instance without a
  partition and as `global`, and both naming alice get `404 not-found`;
  `alice.Asserting("mallory")` gets `403 not-allowed` — all before the
  upgrade;
- alice's socket still holds stdin after those dials (a refused dial
  takes nothing over);
- her partition's backend naming her attaches the tty exec, her page opens
  a terminal;
- carol's private sandbox at the consumer's non-personal identity: 404 to
  alice's partition on `…/tty` and on an exec's `…/stdio`; a `team` one
  there: her partition opens a terminal and drives a stdio exec.

A mutation run (a throwaway test, not committed) showed it has teeth: a
fake whose socket routes took every dial as alice's partition fails at
bob's `…/tty` (upgraded, want 404); one that dropped `Sbx-User` on socket
routes fails at mallory (upgraded, want 403).

Skips: fakesandbox's `TestContractBeforeStdio` and
`TestContractPolicingTTY` skip `user-partitions` (TestContract runs it).
`test/isolated`'s `csPartitionSkips` needed nothing: every caller the check
makes is one xbind makes (it names no verified person without their
partition), so it **runs through a real isolated xbind** against
coding-sandbox — the plan's F-S4 ("another partition's stdio dial gets
404") for the manager half; the agent half is B5's.

## B4, the fabric part

- **§Partitioned consumers (AR-23):** "Isolation stops at the sandbox"
  adds the stdio attach (it takes the exec's stdin from the socket before:
  a coding agent then takes its input from that partition) and the
  sandbox's `home` (a coding agent's sign-in there serves whoever runs the
  agent afterwards, from any partition that sees it or in a clone, as the
  person who signed in); a consumer keeps sign-ins with a person's private
  work, in their partition's sandboxes.
- **Hello's `harnesses[].login`:** the same note; "a partitioned consumer
  offers a person the sign-in only in a sandbox homed in their own
  partition"; and, after the list, `login` and `argv` run beside the
  person's credentials, so whoever may set them (operators, whoever may
  change the manager's code) is in the trust base of everyone using the
  image's coding agents. The builtin manager's partitioned-consumers bullet
  names the harness commands among what its writers hold.
- **coding-sandbox API.md:** the stdio bullet says "the consumer (a user
  partition is its own), the person rules", and so does the ports bullet
  (the same wording, side by side); its Images section says where sign-ins
  live and who is in their trust base.

## Changelog entry (docs/changelog.md, under 2026-09-30)

- **Sandbox managers: a user partition's person on terminals and stdio
  sockets, and what one shared sandbox shares**
  ([sandbox-manager.md](/docs/sandbox-manager.md) §Partitioned consumers,
  §Terminals, §stdio, §hello). The contract now says what its partitions
  rule already meant for the routes coding agents added: from a
  partitioned consumer's user partition the person is the partition's and
  verified on the `tty` and `stdio` routes too — a manager with
  `partitions` applies the person rules itself, and an `Sbx-User` naming
  anyone else is `403 not-allowed`; "asserted, the consumer's to check" is
  an unpartitioned consumer's backend call (or a global instance's).
  `sdk/sandboxcontract`'s `user-partitions` section gains `sockets` (run
  where hello offers `tty` or `stdio`): another partition, the consumer's
  global instance and a person it names get `404` on a partition-homed
  sandbox's terminals and stdio sockets, the partition naming someone else
  `403`, and a refused dial takes no stdin. `hack/fakesandbox` and
  `coding-sandbox` pass. §Partitioned consumers names two more things the
  partitions that see one sandbox share: an exec's stdio socket, whose
  attach takes over its stdin (a coding agent's), and the sandbox's home,
  where a coding agent's sign-in serves whoever runs that agent there
  afterwards; hello's `harnesses[].login` adds that a partitioned consumer
  offers the sign-in only in a sandbox homed in the person's own
  partition, and that whoever sets `argv`/`login` is in its users' trust
  base. `ManagerTTYOptions.User` and `ManagerStdioOptions.User` say the
  same. Nothing changes for an unpartitioned consumer or a manager without
  `partitions`.

Corrections to fold into existing 2026-09-30 lines (the fold owns
docs/changelog.md):

- master's "Sandbox managers: coding agents in hello, terminals for
  consumer backends, …" bullet, **Terminals for consumer backends**: "the
  consumer checks that person first, the manager keeps its partitions" →
  "the consumer checks that person first, the manager keeps consumers
  apart (from a partitioned consumer's user partition the person is the
  partition's, verified: the manager checks them)".
- partitions' "Sandbox managers key partitioned consumers per person"
  (D140): "Isolation stops at the sandbox: partitions that see one sandbox
  share its execs and terminals." → "… share its execs, terminals and
  stdio sockets, and what its home holds (a coding agent's sign-in)."

## Decision entry

None of W6-F's own: F-M3 states D140's rule where D147's text contradicted
it, and F-M4 checks it on the routes D147 added. For the fold's one W6
decision ("coding agents under partitions", 96 §B4, the next free
D-number), the fabric part, to take in as its bullets:

- **The contract, unchanged in rule:** from a partitioned consumer's user
  partition the person is the partition's and verified on every route,
  the `tty` and `stdio` sockets included (D140); D147's "asserted, the
  consumer's to check" is an unpartitioned consumer's, or a global
  instance's. `sdk/sandboxcontract` checks it (`user-partitions/sockets`).
- **AR-23 grows:** partitions that see one sandbox share its stdio sockets
  (an attach takes over a coding agent's stdin) and its `$HOME` (a
  sign-in serves whoever runs the agent there, and its clones). So a
  partitioned consumer offers sign-ins only in sandboxes homed in the
  person's partition (with §I15: never at global, in shared or hosted
  chats), and whoever sets an image's `harnesses` commands is in the
  trust base of their users.

## Seams for the other packs

- **W6-A / W6-U:** sandbox-manager.md now says a partitioned consumer
  offers the sign-in only in a sandbox homed in the person's partition —
  A-S3 (a partition keeps only its homed sandboxes for harnesses), U-M3
  and U-M4 make the agent do so; until they land the sentence is ahead of
  the agent.
- **W6-A:** from a user partition, every manager call's `Sbx-User` is the
  partition's person or empty, or the manager answers 403. The agent's
  harness pipe dials stdio with `User: p.t.Conn.User` and its relays with
  `c.user`: under §I15 (a harness only in the person's own conversations)
  those are the partition's person; a harness run for anyone else in a
  partition would fail closed (403, a `manager-error`), never act as them.
- **The fold:** `plans/agtt-harness.md` §2.2's line on partitions'
  D-numbers (96 §B4) is untouched here; D147's dated amendment is the
  fold's.

## Deviations

- One check, `sockets`, holds the plan's cases for both sockets (each half
  gated on its capability), plus two it didn't list: a refused dial takes
  no stdin, and the partition's own backend (naming its person) and page
  attach.
- The plan's "an asserted person" is the consumer's non-personal identity
  naming alice (with and without `X-XBin-Partition: global`); its verified
  alice without a partition (in `apart`) is left out, so the check runs
  through xbind too (xbind never makes that caller of a partitioned
  consumer).
- Beyond the plan's list of places: §Who is asking's and §Consumers'
  backend-call sentences, §Partitioned consumers' person bullet, the
  builtin manager's trust-base bullet, coding-sandbox API.md's ports
  bullet (the stdio bullet's wording, side by side) and Images sentence,
  its stale "Testing on xbind" sentence (it said `user-partitions` runs
  only in-process; I1 runs `global`, `global-home` — and now `sockets` —
  through xbind), and the fake's must-offer `stdio` (it always offered
  it; the list didn't require it, so a fake that lost it would skip the
  stdio half silently).
- `hack/fakesandbox/fsb.go` is untouched, so its mirrors are too.

## Bugs found

None in code. Docs: coding-sandbox API.md's "Testing on xbind" said the
`user-partitions` section runs only in-process (stale since I1); fixed.

## Tests

On `pt/w6-f`:

- `go test -race -count=1 ./hack/fakesandbox` — ok (TestContract with
  `user-partitions/sockets` run, tty and stdio halves; the two skip-list
  tests skip the section).
- `TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh coding-sandbox` — ok
  (`TestContract/user-partitions/sockets` PASS in-process, `TestPageReaders`
  for F-M2); `hack/tile-check.sh sandbox-terminal` — ok (it imports the
  suite).
- `test/isolated` `TestCodingSandboxContract/user-partitions` through an
  isolated xbind (.dev.mk's env, sandbox disabled, `-parallel 3`): global,
  global-home, **sockets** and user-partitions-xbind PASS; apart, shares,
  person, recreated the declared skips. `TestCodingSandboxContractVM` (the
  same suite on VM sandboxes) not run.
- `cd sdk && go vet ./... && go test -count=1 ./...` — ok.
- `go test ./internal/docscheck ./internal/sizebudget ./internal/builtins`
  and `-run TestPartitionRouteClasses ./internal/apicheck` — ok.
- `make fmt-check vet` — ok.
- Not run: the agent template's checks — this pack changes no agent
  file, and of what the agent imports only comments changed
  (`sdk/manager_tty.go`, `sdk/manager_stdio.go`).
