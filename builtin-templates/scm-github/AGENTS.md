# Working on this scm-github copy

This tile is an **scm provider** (`/docs/scm.md`): tiles bound to its `scm`
provide — the agent, anything else — get repo credentials, repos, pull
requests, issues and CI from GitHub through the contract's `/scm/*`
routes. API.md says how it is set up and what it does; /docs/scm.md is the
contract, and it wins.

## Where things are

| Part | Files |
|---|---|
| the instance and the caller (legacy, global, a person's partition; tile, person's consumer, relay, page, manager) | `_backend/mode.go` |
| refusals, the contract's shapes, the one type a secret lives in (`secretString`) | `_backend/errors.go`, `_backend/types.go` |
| GitHub: REST and GraphQL, ETags, the rate limit, errors → refusals; the App's JWT | `_backend/gh_client.go`, `_backend/gh_jwt.go` |
| the App, setup, policy | `_backend/app.go`, `_backend/setup.go`, `_backend/policy.go` |
| bot tokens; a person's tokens, refresh and epoch; the device flow; the relay to global | `_backend/bot.go`, `_backend/person.go`, `_backend/signin.go`, `_backend/relay.go` |
| the `/scm/*` routes | `_backend/{hello,tokens,repos,pulls,checks,issues,poll}.go` |
| the page's API; kv and the vault | `_backend/page.go`, `_backend/store.go` |
| events: webhooks (signature, accounts, dedupe, access caches, health, catch-up); GitHub → event v1 and the checks a body's fields get; subscriptions and the relay's; the outbox and `GET /scm/events`; delivery and retries | `_backend/hook.go`; `_backend/normalize.go`, `_backend/normalize_gh.go`; `_backend/subs.go`; `_backend/outbox.go`; `_backend/deliver.go` |
| the page; the xbin app's view | `index.html` + `scm.js`; `native.js` |
| the fake GitHub (its webhook side: signed fixtures, fake consumers) and the tests | `_backend/fakegh*_test.go`, `_backend/harness_test.go`, `_backend/*_test.go`, `_backend/testdata/` |

## Rules

- **Never a secret in kv, a log line, an error or an answer.** Keys,
  client secrets, webhook secrets and people's tokens live in the vault and
  in memory as `secretString` (it prints and marshals as `[secret]`);
  `POST /scm/token` builds its body with `tokenJSON`, explicitly.
  `TestNoSecretsInResponsesOrLogs` checks it.
- **Who is asking comes from xbind's headers** (`mode.go`), never a body.
  A call carrying `X-XBin-Partition: user:…` is never the tile itself at
  global. A relay body is the person's own input — their frame can send
  anything — so global re-checks every field against GitHub and the policy.
- **A person's sign-in stays in their partition.** Global may see an access
  token in transit (identity, scope, revoke) and never keeps it.
- Text GitHub's users wrote (bodies, comments, CI output, logs) passes
  through untrusted, clipped where the contract says. A webhook body is
  attacker-controlled: every field an event takes from it goes through
  `normalize_gh.go`'s checks, and a delivery for an account outside
  `allowedAccounts` is dropped before anything reads it.
- Additive only: new caps, fields and routes; the contract's shapes are
  frozen for protocol 1 (/docs/scm.md §Versions).
- Go files stay under 800 lines; the backend needs the sdk and the standard
  library only (`go.mod`: no other requirement).

## Testing

`hack/tile-check.sh scm-github` in the xbin checkout (vet and the tests
against this template's `go.mod.tile`), or `go test ./...` in a copy's
`_backend`'s module. The fake GitHub (`_backend/fakegh_test.go`) serves
the whole surface the tile uses — JWT-checked App routes, installation
tokens, the OAuth applications API, the device flow, content and CI — with
ETags, counters and injected failures; `harness_test.go` runs a global
instance and people's partitions in memory, each partition's relay wired to
global as xbind would attribute it. A live GitHub App is never needed.
