# Coding-agent sign-ins

A coding agent (Claude Code, Codex, Gemini CLI, OpenCode) needs someone's
account to call its model. There are three ways a session gets one:

1. **the workspace's LLM connector** ([llm.md](/docs/llm.md)) — no
   sign-in at all, for the coding agents that can be pointed at it;
2. **a sign-in you saved with xbind** (D230, this page) — made once, kept
   sealed, used for your sessions on every tile where that is safe;
3. **the tile's own login** — the CLI's own sign-in in a terminal (`claude
   auth login`, `codex login`), which lives in your `$HOME`
   ([protocol/agents.md](/docs/protocol/agents.md)).

The wire is [protocol/signins.md](/docs/protocol/signins.md).

## What a saved sign-in is

A credential the coding agent takes from its environment:

| Coding agent | Guided sign-in | Or paste |
|---|---|---|
| Claude Code | `claude setup-token`: a token good for a year, for model requests only | an `sk-ant-oat…` token, or an Anthropic API key |
| Codex | — | an OpenAI API key |
| Gemini CLI | — | a Gemini API key |
| OpenCode | — | a provider's key (Anthropic, OpenRouter, OpenAI, Google, Groq, xAI) |

Only Claude Code has a sign-in that prints a credential which can be
kept. The other CLIs' subscription sign-ins keep a refresh token in a
file; a copy of such a file signs the original out the first time either
refreshes, so xbind never copies one. Their subscription login stays the
tile's own.

Each person keeps their own: named, up to 8 per coding agent, one of them
the default.

## Making one

**The guided sign-in.** xbind runs the CLI's own sign-in for you — not in
your tile, but in a throwaway sandbox of its own: the base image, no tile
directory, no home, an empty `HOME`, internet egress only. You get a link;
you sign in on the vendor's page in your own browser and bring back the
code it shows; xbind types it into the CLI and keeps the token the CLI
prints. The token goes from xbind's reader into the store. It is never
sent to your browser, never logged, and nobody can enter the sandbox it
was printed in.

**Pasting.** A key or token you already have, pasted once. xbind checks
its shape only (one line, no spaces or quotes) — it calls no vendor — so a
wrong key shows the first time a session uses it.

## Where it is kept

`data/signins/` under the workspace, one file per person, outside every
sandbox's view. The metadata (name, coding agent, kind, the times) is
readable while the vault is sealed; the secrets are sealed under the
workspace vault, each person's under a key of their own.

| The vault is | Then |
|---|---|
| open | sign-ins are used and saved |
| sealed | none can be used or saved until an admin unseals; the list still reads and Remove still works |
| not set up | saving is refused: a sign-in is never kept in the clear |
| plaintext (`--insecure-vault`, `--no-auth`) | kept in the clear like every secret of such a workspace |

**Nothing reads a secret back.** No route, event or log line carries one
or a part of one — there is no "show" and no "last four". You can rename
a sign-in, make it the default, replace it or remove it.

The store needs sandboxes (`xbind --isolate`): without them every terminal
runs as xbind's own user and could read a coding agent's environment. A
development workspace (`--dev`, `--no-auth`) is excepted, and the list says
so.

## Admins and hosts

- **The workspace's switch** — "people may save coding-agent sign-ins", on
  by default (`PUT /agent/signins/policy`). Off: none is used or saved;
  what is saved stays and works again when the switch returns.
- **The host** sets and locks it with `"agentSignIns": true | false` in
  the deployment file ([llm.md](/docs/llm.md) → "The host's file").
- **Admins see that, not what**: who has sign-ins, for which coding agent,
  of which kind, made and last used when — not their names, never a
  secret — and can revoke one. The person is told who did.
- **Backups** carry `data/signins/` as they carry every vault file: sealed,
  and restorable only with the same vault.
- An xbind older than this feature ignores the directory.
