# Adding a backend to this sandbox manager

This tile is a **sandbox manager** (`/docs/sandbox-manager.md`): tiles bound
to it — the agent, `sandbox-terminal`, anything — create and use sandboxes
through the contract's `/sbx/*` routes. It is a template: the copy you are in
runs its sandboxes on xbind's own tile-sandbox runtime (the `xbin` backend:
VMs where the workspace runs them). When someone asks for sandboxes on
**a cloud** (Hetzner, AWS, GCP, Fly…), **machines over ssh**, containers
somewhere else — your job is to write **one backend**:
`_backend/backend_<name>.go` plus its test. Everything else is done:

| Part | Who does it |
|---|---|
| The contract (every `/sbx/*` route, its errors, `?wait`, clamping), partitions and shares, owners and people, visibility and members, labels, versions, `clientId`s, contract ids, images (setup scripts built once and cloned), sizes, quotas, egress words, the operators' `/ops/*`, the page (web and native), storage, restarts | **the manager** (`_backend/*.go` but `backend_*.go`, `model/`, `web*.js`, `native*`) |
| Sandboxes **by name** on the substrate: what it offers, create/clone, get, change, delete, start, stop; commands (a blocking run, background execs with their output by byte offset, stdin, signals, resizes); a terminal relayed as a WebSocket; files, trees, snapshots | **your backend** |

Read `_backend/backend.go` first (the interface, ~200 lines). The shapes are
the Go SDK's (`sdk/sandbox*.go`: `xbin.SandboxSpec`, `SandboxInfo`,
`RunRequest`, `ExecInfo`, `OutputChunk`, `FileStat`, …) on purpose: the
`xbin` backend (`_backend/backend_xbin.go`) is the SDK itself, and the test
fake (`_backend/fake_*_test.go`: host directories and processes) is a
complete backend with no network — read it next. API.md says what the
manager adds.

## The steps

1. **Write `_backend/backend_<name>.go`**: a type with the `Backend`
   methods (`Fleet` + `Sandbox(name) Box`) and a `Box` per sandbox, and
   register it:

   ```go
   func init() { registerBackend("hetzner", newHetzner) }

   func newHetzner(env BackendEnv) (Backend, error) {
   	token, err := xbin.Secret("hcloud-token") // the tile's vault — never files, kv or logs
   	if err != nil || token == "" {
   		return nil, fmt.Errorf("no hcloud-token in this tile's vault (bx vault set %s hcloud-token …)", xbin.Self())
   	}
   	region, _ := env.Config["region"].(string) // config.backendConfig: your shape
   	return &hetzner{api: hcloud(token), region: orStr(region, "fsn1")}, nil
   }
   ```

   An error from the constructor leaves the manager answering
   `503 unavailable` with it, and the operators' page shows it.
2. **Switch the copy to it**: `PUT /ops/config {"backend": "hetzner",
   "backendConfig": {"region": "fsn1"}}` (an operator; the page has no
   control for it on purpose). The manager refuses a switch while
   sandboxes or built images live on the old backend — they would be
   stranded — so do it before anyone uses the copy.
3. **Give the manager's own backend the network it needs** to reach your
   substrate (its sandboxes' networks are separate — below): add
   `"net": {"kind": "net"}` to `interfaces` in `xbin.json`, and have the
   person bind it to exactly the API and the instances, e.g.
   `bx bind <tile> net=internet:api.hetzner.cloud:443,internet:*:22`.
   Remove `cap:sandboxes` from `uses` if the copy never runs on xbind.
4. **Add client libraries** to `go.mod` if you need them
   (`golang.org/x/crypto/ssh`, `github.com/pkg/sftp`, a cloud SDK). The first
   build fetches them.
5. **Test it** (below): your backend against an `httptest` double of the
   cloud API and an in-process ssh server, and the contract's conformance
   suite against the manager over it. `go test ./_backend/` must pass.
6. **Tell the person** what to do next: the vault secret, the `net`
   binding, the switch, and which images and sizes make sense on it
   (`PUT /ops/config {images, sizes}` or the page's Images and Settings).

Keep your code in `backend_<name>*.go`. The template's own files get updates
later, and they merge cleanly when you haven't edited them.

## Mapping the methods

The contract's "Building a manager" table (`/docs/sandbox-manager.md`), at
the level of this interface:

| Method | On a cloud + ssh | Notes |
|---|---|---|
| `Runtime` | fixed facts plus the account's quota | `Enabled`, `Isolation` true (false: every call is `503`); `Modes` e.g. `[{Mode: "cloud-vm"}]` — the sandbox's `isolation` (`vm`, `container`, `namespace`, `cloud-vm`); `Caps` what you serve (`exec`, `files` required; `tar`, `tty`, `stdio`, `snapshots`, `clone` optional — leave out what you lack and the manager offers it to nobody); `Egress` the classes below; `Limits` (`perSandbox` caps filter the sizes; `waitMaxSec`, `fileMax`, … are hello's) |
| `Create` | the instance API (a server from an image, a disk of `DiskGiB`), polled | `spec.Name` is the manager's runtime name — tag the instance with it and with `spec.Labels`, and make a repeated `spec.ClientID` (= the name) answer the one already made. `spec.Defaults` is the layout (cwd, uid/gid, shell, env: `HOME`, `USER`, `SANDBOX_ID`, `SANDBOX_NAME`); answer the one that applied. `spec.From` is a clone: an image made from that sandbox's snapshot |
| `Get`, `List` | the instance API | `State` one of `stopped starting running stopping error`; `Net.Egress`/`Reach` what it has; `LastActive`, `DiskBytes` when you know them. `List` is every instance of this manager (the operators see the ones the table doesn't know as orphans) |
| `Patch` | resize, firewall rules, labels | a change that waits for a restart says `RestartNeeded` (an egress: `Net.EgressNext`) |
| `Start`, `Stop`, `Delete` | power on/off, delete the server and its disk | return once done or `wait` ran out, saying where it stands |
| `Run` | `ssh host -- 'cd -- <cwd> && exec setsid $SHELL -lc <cmd>'` | as `r.UID`/`r.GID` (the layout's; the manager's own prepare runs as root); `TimeoutMs`: TERM to the group, KILL 5 s later, over a second connection; output past `MaxOutput` keeps a head quarter and a tail three quarters |
| `Exec`, `Execs`, `GetExec`, `Output`, `Stdin`, `Signal`, `Resize`, `Kill` | `setsid nohup <cmd> > /var/lib/sbx/<eid>/out 2>&1; echo $? > /var/lib/sbx/<eid>/exit` | output by byte offset (`tail -c +<since+1>`), a ring of `OutputRing` bytes; `ClientID` is per sandbox (the manager prefixes it per consumer); an exec from before a restart is `lost` (410) |
| `RelayTTY`, `RelayNewTTY` | `ssh -t`, window-change on resize | serve the consumer's WebSocket (`sdk/ws`) on the terminal wire (`/docs/protocol.md` §The terminal wire): the session frame with `o.SessionID`/`o.SandboxID`, binary both ways, `resize` and `ping`, `exit` at the end. Refusals come **before** the upgrade, as `xbin.WriteSandboxError`. The tty exec must be listed by `Execs` and attachable again |
| `RelayStdio` (optional: `StdioBox`, with `stdio` in `Caps`) | one ssh session per attach: `tail -c +<off+1> -f` of the exec's stdout log and its stderr log (`ExecRequest.Split`: `2>` a second log), stdin into a fifo the exec reads | serve the consumer's stdio WebSocket (`/docs/sandbox-manager.md` §stdio): `hello`, stdout as binary frames from `since` (a `gap` frame where your log was cut), stderr as `stderr` frames from `errSince`, `exit` at the end; stdin binary frames, `eof`, `ping`; the socket attached last holds stdin (close the one before with 4001). `Output` takes `Stream: "stderr"` for a split exec. Without it the manager doesn't offer `stdio` and consumers poll |
| `Stat`, `ReadFile`, `WriteFile`, `List`, `Mkdir`, `Remove`, `Move` | sftp | `ETag` a content hash; a write goes to a temporary name and is renamed; `IfMatch`/`IfNoneMatch` are `precondition` (412) with the current etag |
| `GetTar`, `PutTar` | `tar -C <dir> -cf -` / `-xf -` over ssh | |
| `Snapshots`, `Snapshot`, `RestoreSnapshot`, `DeleteSnapshot` | disk snapshots | a restore kills the execs |

**Refusals** are `*xbin.SandboxError` with the contract's enum (`invalid`,
`not-found`, `state` with `State`, `exists`, `lost`, `precondition` with
`ETag`, `too-large`, `limit`, `unsupported`, `unavailable` with
`RetryAfter`). Anything else becomes `503 unavailable`. Put the runtime
name in a message freely: the manager rewrites it to the contract id. Your
exec and snapshot ids reach consumers as they are, so any string can come
back as one: an id that names nothing is `not-found`, never `invalid`.

**Networks.** A sandbox's `Net.Egress` is `none` or `class:internet` /
`class:open` — this tile's `sandbox-net` classes. On a cloud, map them to
firewalls: `none` denies all egress (ssh in from the manager only),
`internet` allows the public internet and nothing private, `open` what the
operator configured (in `backendConfig`). Report them in `Runtime().Egress`
as `{Class, Slot, Ref, Reach}` with a `Ref` (non-empty: "bound") for the
classes you serve — the manager offers `internet` and `open` only while
they have one.

**Mounts** (`spec.Mounts`, the operators' `config.mounts`) are xbind's
filesystem resources: refuse them (`unsupported`) unless your substrate can
reach the data.

## Testing it

- **Your backend** against doubles: an `httptest.Server` playing the cloud's
  API, and an in-process ssh server (`golang.org/x/crypto/ssh` has one; give
  it a `session` handler that runs commands in a temporary directory). The
  `xbin` backend's test (`_backend/backend_xbin_test.go`) is the pattern:
  a double of the substrate's API and the manager over it, checking what
  each contract request becomes.
- **The contract**, with the conformance suite, `sdk/sandboxcontract`
  (`/docs/sandbox-manager.md` "Check it against the contract"). Serve this
  manager over your backend in-process and run it:

  ```go
  func TestContractHetzner(t *testing.T) {
  	st, _ := openStore(filepath.Join(t.TempDir(), "db.sqlite"))
  	m, err := newManager(st, newHetznerForTest(t)) // your backend over its doubles
  	if err != nil {
  		t.Fatal(err)
  	}
  	m.Logf = t.Logf
  	srv := httptest.NewServer(m.contractHandler())
  	t.Cleanup(func() { srv.Close(); m.Close() })
  	sandboxcontract.Run(t, sandboxcontract.Target{
  		URL:   srv.URL,
  		Caps:  []string{"exec", "files", "tar", "tty", "snapshots", "clone"}, // what hello must offer
  		Grace: 5 * time.Second,                                               // your TERM → KILL grace
  	})
  }
  ```

  `_backend/contract_test.go` does exactly this over the fake;
  `serveManager` and `freshTarget` there build what `Target.Fresh` needs (a
  manager with a small output ring, a small `fileMax`, fewer capabilities).
  `go test ./_backend/ -run 'TestContractHetzner/tty'` runs one section.
  Checks you know fail go in `Target.Skip` with why — never silently.
- **For real**, against the cloud, from this tile's terminal: the same
  test with the real backend behind an environment switch, run by hand
  (it costs money; clean up what it made — `/ops/state` lists orphans).

The xbind-side end to end of the `xbin` backend is the runtime's (API.md
§Testing on xbind).
