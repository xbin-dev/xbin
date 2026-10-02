# The numbers xbin may claim, measured

What the promo film and the website may say about speed, each figure measured
end to end on this repository's code, with the method, the command, the raw
numbers and the most a careful claim may say. Start with the claims table.
Each section below gives the evidence behind its row.

**Rules.**

- A number is a wall-clock time seen from outside, as a person or a program
  sees it: a request leaving and its answer arriving, a save and the new page
  painted, a key going down and its glyph on screen. None of them is a timer
  inside xbind.
- Each series has at least 10 samples and is given as n, min, median, p90
  and max. The p90 is the nearest rank, the ⌈0.9·n⌉-th smallest, never
  interpolated. The tables round **up** to 0.1 ms.
- A claim takes the p90 or the worst sample and rounds it up to a round
  number. Where the setup here was kinder than a customer's machine, the
  claim leaves room and says so.
- What couldn't be measured here is marked as not measured.

## The claims

All claims are for **a big workstation** (Setup below), not a small VPS.

| # | Claim | Basis |
|---|---|---|
| 1 | **A Firecracker microVM sandbox boots and runs its first command in under 0.3 s** (median 0.22 s). | 90 of 90 under 249 ms; p90 234 ms. Workspace on tmpfs (§1) |
| 1 | **64 microVM sandboxes asked for at once all answer within 1 s, and none fail.** | 5 bursts of 64, the slowest 943 ms; 600 VMs in 20 bursts of 8–64, 0 failed. One burst of 32 took 1.35 s (§1) |
| 1 | **A VM terminal shows its prompt in about 0.2 s** (a namespace terminal in 0.06 s). | 100 VM terminals: median 177 ms, p90 204 ms, one outlier 706 ms (§1) |
| 2 | **Save a page and it's on screen in under 0.35 s**, including a deliberate 0.3 s settle. | 90 saves painted in Chromium, max 336.8 ms (§2) |
| 2 | **Save Go code and the rebuilt backend serves in under 0.8 s.** | 90 saves, max 784.2 ms (§2) |
| 3 | **Redeploy under load without dropping a read.** 0 of 5.39 M GETs failed over 60 live redeploys at about 30,000 requests/s. | Writes caught at the exact swap instant can fail: 15 of 2.34 M POSTs got a 502. Worst wait 0.56 s, one request in 35 of 60 redeploys (§3) |
| 4 | **With a 300 ms round trip, what you type appears within one frame (≤ 17 ms) instead of after 0.3 s.** | 240 keys with prediction on or auto: in the DOM within 1.6 ms, painted by the next frame (≤ 16.7 ms). Off: ≥ 301.7 ms (§4) |
| 5 | **A person's own instance starts on their first request in under 0.3 s, then answers in about a millisecond.** | 39 cold starts, max 264.8 ms; 4,500 warm requests: 98 % under 1 ms, p99 1.5 ms, max 3.2 ms. Encrypted file volumes add about 0.03 s each (§5) |
| 6 | Install time: **not measured here.** It needs a fresh VPS (§6). | — |

## Setup

| | |
|---|---|
| Date | runs between `Sat Oct  3 12:01:08 AM CEST 2026` and about 12:31 AM CEST (`date`, in each run's `machine-*.txt`) |
| Product code | **4a9b5b47** (origin/master, v0.3.67), unchanged. The runs were taken from this branch (bc9061fd … d5ac8b7c), which adds only the measuring tools: `hack/demo/`, plus `test/xbindtest` listening on a given port. Each run's `machine-*.txt` names its exact commit |
| Machine | AMD Ryzen Threadripper PRO 7995WX, 96 cores / 192 threads; 755 GiB RAM; NVMe, btrfs (zstd) on dm-crypt, 97.5 % full; Linux 7.2.8-arch1-2 (Arch); `/dev/kvm`; NVIDIA RTX 5070 Ti (none of these use it) |
| VMs | Firecracker v1.17.0, guest kernel 6.18.54, the rootfs as an erofs image (erofs-utils 1.9.4) |
| xbind | `xbind --dev --isolate --rootfs .rootfs --workspace <fresh> --listen 127.0.0.1:9341` (`test/xbindtest`). Owner auth is on and the vault is unsealed. `--dev` logs every request at debug level, which costs time if anything |
| Rootfs | Ubuntu 26.04 LTS base, built 2026-09-29, before 0822725e moved terminals to Go 1.26.3. Nothing here builds Go inside a terminal; tile builds use the host's Go 1.26.3 |
| Browser | Chromium 149.0.7827.55, the Playwright 1.61.1 headless shell. Viewport 1400×900, 60 frames/s (measured) |
| Load | Other agents worked on the box at the same time. The 1-minute load average went from 2.6 to 21.7 of 192 threads. Every sample records `load1` |

**Where the workspaces lived.** This changes the numbers, so each section says which layout it used:

- **run3-prodlike** is production-like: the workspace on the NVMe (btrfs) and xbind's run dir on a tmpfs, as `RuntimeDirectory=xbin` gives `/run/xbin`. Sections 2–5 lead with it, and pool it with runs 1 and 2 where they say so. §1's VM terminals pool it with run2. run2 is the same layout with the run dir on disk.
- **run1, run4 and run5** have the workspace on tmpfs (RAM). §1's tile sandboxes come only from these runs: xbind starts no tile sandbox while the workspace disk is below its 10 % free reserve (`internal/broker/diskmon.go`), and the only writable disk here is 97.5 % full.
  - On the same disk, VM terminals took 6 ms longer at the median than on tmpfs, 10 ms at the p90, and had one outlier.
  - Partition cold starts took 34 ms longer at the median.
  - §1's claim therefore keeps more than 50 ms of room.

**Two things this session can't do,** both because it runs inside a single-uid user namespace (the agent's tool sandbox):

- **Sub-uid range mode.** xbind's sandboxes ran in **single-uid mode** (`MEASURE_SINGLE_UID=1` hides `newuidmap` from xbind's PATH). That is what a host without an `/etc/subuid` range runs. Range mode adds two setuid helper execs per sandbox start.
- **gocryptfs mounts.** The setuid `fusermount3` does nothing inside this namespace, so no tile whose state sits in an encrypted file volume could run. That rules out the builtin **coding-sandbox** manager (sqlite) and the **agent** template. §1 measures the runtime coding-sandbox drives, and §5 measures a partitioned tile whose per-person data is a kv resource (encrypted with envelope keys, no FUSE). The volume cost was measured on its own in §5.

**Rerun.**

```sh
hack/dev-setup.sh                      # once: the helpers, the rootfs, .dev.mk
MEASURE_OUT=$PWD/.film-media/measure/runN TMPDIR=<disk dir> RUNTIME_DIRECTORY=<tmpfs dir> \
  [MEASURE_SWAPS=40] [MEASURE_VM_ROUNDS=3] [MEASURE_SINGLE_UID=1] \
  hack/demo/measure/run.sh [vm|save|swap|partition|browser|all]
node hack/demo/measure/report.mjs $PWD/.film-media/measure/runN   # the tables, rounded up
```

`run.sh` needs:

- user namespaces;
- `/dev/kvm` (vm);
- Playwright with Chromium in `PLAYWRIGHT_DIR` (browser).

On a dev box, run it with the tool sandbox off. The code is in `hack/demo/measure/`, under the `xbinmeasure` build tag, so no other build compiles it. The latency proxy is `hack/demo/latency/`.

**Raw data** sits in the main checkout's git-excluded `.film-media/measure/`, one directory per run:

- `machine-*.txt`: the machine, the commit and the layout;
- `<name>.jsonl`: every sample, with its wall time and load;
- `<name>.summary.json`;
- `report.md`;
- the go test log;
- xbind's own log (run5's whole run; run2's and run3's refused VM test).

| run | layout | what |
|---|---|---|
| `run1-tmpfs` | workspace + run dir on tmpfs | everything, swap 10 saves |
| `run2-disk` | workspace + run dir on disk | everything. Tile sandboxes were refused (low disk), terminals ran |
| `run3-prodlike` | workspace on disk, run dir on tmpfs | everything, swap 40 saves. Tile sandboxes were refused (low disk), terminals ran |
| `run4-vm-tmpfs` | tmpfs | VM only |
| `run5-vm-tmpfs-3rounds` | tmpfs | VM only, three rounds of the 8/16/32/64 bursts, with xbind's log |
| `run6-gocryptfs` | inside `unshare -Urm`, volumes on disk and on tmpfs | gocryptfs init and mount, 20 times each |

## 1. Firecracker microVM sandboxes: request → answering

**Method** (`TestVM`, `vm_test.go`):

- **Tile sandbox.** A manager tile `apps/build-farm` holds `cap:sandboxes` (approved). Its Go backend drives xbind's tile-sandbox runtime through the SDK (`xbin.SandboxAPI()`), as `examples/sandbox-go` does, and coding-sandbox forwards to this same runtime. The client calls the tile through xbind's proxy:
  1. `POST /api/apps/build-farm/sandboxes {mode: vm}`. The runtime answers 201 `running` only once the VM booted and its in-box agent answered.
  2. Then `POST …/run {"cmd": "echo ready-$((6*7))"}` until `ready-42` comes back.
  3. Each sample is created, run, deleted and drained (xbind lists none, its removal backlog is empty) before the next.

  Every VM is coding-sandbox's default size "small": 2 GiB, 2 vCPUs and a 20 GiB sparse disk, which the guest formats at first start. The namespace rows are the same calls with `mode: namespace`.
- **First ever.** The first VM on a fresh workspace also builds the guest image (the rootfs → erofs, once per base). It is its own row.
- **Terminal.** `/ws/term?cwd=apps/ci&vm=1` (the terminal's VM toggle) is opened as the owner, and the clock stops at:
  1. the session frame;
  2. the shell's prompt (xbin's `… ❯`);
  3. the answer to `echo ready-$((6*7))`.

  The session is then ended (`DELETE /ws/term?session=`) and its socket waited out.
- **At once.** N goroutines are released together, each running create plus run, for N = 8, 16, 32 and 64. All N are then deleted and drained before the next level. The run counts the `firecracker` processes under xbind once all have answered, and the drop in host `MemAvailable`.

**Command:** `hack/demo/measure/run.sh vm` (`MEASURE_VM_ROUNDS=3` for run5).

**Numbers.** Tile sandboxes come from runs 1, 4 and 5 (workspace on tmpfs, pooled). Terminals come from runs 2 and 3 (on disk) and runs 1, 4 and 5 (tmpfs).

| series (ms) | n | min | median | p90 | max |
|---|---:|---:|---:|---:|---:|
| VM sandbox: request → answer | 90 | 204.1 | 219.3 | 233.2 | 248.5 |
| — create (boot + agent ready, 201) | 90 | 188.9 | 202.9 | 215.1 | 232.8 |
| — first command | 90 | 14.3 | 15.9 | 17.8 | 23.8 |
| — delete (204) | 90 | 34.0 | 55.4 | 66.3 | 81.0 |
| namespace sandbox: request → answer | 60 | 70.3 | 73.3 | 75.7 | 77.4 |
| VM terminal: open → prompt (on disk) | 40 | 155.9 | 182.7 | 208.3 | 706.0 |
| VM terminal: open → prompt (tmpfs) | 60 | 161.8 | 176.7 | 198.6 | 212.7 |
| namespace terminal: open → prompt (on disk) | 40 | 51.6 | 54.1 | 55.6 | 58.4 |
| first VM ever on a workspace (builds the guest image) | 3 | 4017.7 | 4168.3 | — | 4212.2 |
| first VM terminal on a workspace (builds the image; on disk) | 2 | 5871.3 | — | — | 6273.3 |

The command after a VM terminal's prompt answered within 1 ms. The one 706 ms terminal (run3) spent 569 ms before its session frame; the other 99 reached it within 28 ms.

**At once** (tmpfs; the wall time from the release to the last answer, each burst):

| N | wall time per burst (ms) | failed | `firecracker` running once all answered | `MemAvailable` drop |
|---:|---|---:|---:|---|
| 8 | 279, 804, 261, 259, 259 | 0 | 8 | 1.1–1.3 GiB |
| 16 | 312, 301, 296, 309, 295 | 0 | 16 | 2.1–2.6 GiB |
| 32 | 424, 429, **1351**, 461, 852 | 0 | 32 | 4.7–5.1 GiB |
| 64 | 743, 814, 702, 780, 943 | 0 | 64 | 9.3–9.8 GiB (148–156 MiB per VM) |

The bursts are runs 1 and 4 (one round each) and run5 (three rounds), in that order.

- Per sandbox in a burst of 64: median 587 ms, p90 771 ms and max 943 ms over 320 samples (the summaries have each level).
- Some bursts were slow as a whole. Every create of the 1351 ms burst took over 1.1 s, and xbind's own log timed its `POST /api/xbin/sandboxes` at 1.11–1.21 s, with nothing logged meanwhile. The cause wasn't found; other work on the box is a candidate.

**Raw:** `run{1,4,5}*/vm-sandbox.jsonl`, `run5*/TestVM.xbind.log`, `run{2,3}*/vm-sandbox.jsonl` (terminals).

**Claims:**

- *"A Firecracker microVM sandbox boots and runs its first command in under 0.3 s"* (all 90 under 249 ms, measured on tmpfs; the room covers a disk-backed workspace).
- *"64 microVMs asked for at once all answer within 1 s, none failed"* (5 of 5 bursts). Don't generalise to "any burst under 1 s": a burst of 32 once took 1.35 s.
- *"A VM terminal is ready in about 0.2 s"*. 9 in 10 of 100 took under 0.21 s (p90 204 ms); the worst took 0.71 s.

Not measured: the coding-sandbox manager's own bookkeeping (see Setup), and VMs on a smaller host.

## 2. Save to live

**Method** (`TestSaveToLive`, `TestBrowser/static`; run3 is production-like, runs 1 and 2 agree):

- **Static, browser.** A team-handbook tile (`apps/handbook`, 19 files: pages, CSS, JS, SVG) is open in the shell in Chromium, signed in as a workspace admin, its card scrolled into view.
  - Each sample writes a new `index.html` (a new headline), as an editor's save. The write time is Node's `performance.timeOrigin + now()`.
  - The clock stops at the tile's window painting the new page: its own first contentful paint, reported by a `PerformanceObserver('paint')` in the page as wall-clock time.
  - "Window starts loading" is the new document's `timeOrigin`: the reload event has crossed `/ws/events` and the shell re-navigated the frame.
  - 1.2 s pass between samples. The first save is discarded.
- **Static, event.** The same save measured to xbind's `reload` event on `/ws/events`, the moment every open window is told.
- **Go.** `apps/orders` is a Go tile over the SDK. Each sample rewrites `backend/main.go` with a new `release` constant and polls `GET /api/apps/orders/orders` every 5 ms through the proxy until the new release answers.
  - The event stream splits the time into the watcher (`reload`), the confined build plus the new generation's start and health check (`build-start` → `build-ok`), and the swap (`build-ok` → served, which includes up to 5 ms of polling).
  - The build cache is warm, as in a working session: the first build (4.2–4.6 s) and the first save are their own rows.

**Command:** `hack/demo/measure/run.sh save` and `… browser`.

**Numbers** (run3; `all` pools runs 1–3, n = 90):

| series (ms) | n | min | median | p90 | max | all: p90 / max |
|---|---:|---:|---:|---:|---:|---|
| static: save → new page painted in the shell | 30 | 319.3 | 331.0 | 335.3 | 336.4 | 332.6 / 336.8 |
| static: save → window starts loading | 30 | 306.2 | 307.3 | 308.1 | 308.9 | 308.4 / 308.9 |
| static: save → `reload` event | 30 | 300.9 | 301.3 | 301.5 | 301.8 | 301.6 / 301.9 |
| Go: save → new code serving | 30 | 707.5 | 724.3 | 740.0 | 781.5 | 738.9 / 784.2 |
| — save → `reload` (watcher) | 30 | 301.1 | 301.6 | 302.0 | 302.3 | |
| — build + start + health | 30 | 396.6 | 415.1 | 431.4 | 469.7 | |
| — `build-ok` → first new answer | 30 | 5.7 | 7.9 | 9.4 | 10.2 | |
| Go: tile written → first answer (cold build, once) | 1 | | 4508.8 | | | |

Most of a static save's time is the watcher's 300 ms debounce, which lets an editor's multi-file save settle.

**Raw:** `run3-prodlike/{save-to-live,browser}.jsonl`, `browser-static.jsonl`.

**Claims:**

- *"Save a page and it's on screen in under 0.35 s"* (90 of 90 under 337 ms).
- *"Save Go code and the rebuilt backend serves in under 0.8 s"* (90 of 90 under 785 ms, warm build cache). For "about a second": the cold first build of a new tile took 4.2–4.6 s.

## 3. Blue/green swap under load

**Method** (`TestSwap`):

- The Go tile `apps/orders` takes a closed request loop through xbind's proxy, owner token, keep-alive:
  - 8 workers `GET /orders`;
  - 4 workers `POST /orders` with a JSON body (2 in run1).

  That is about 30,000 requests/s.
- After a 2 s baseline, each save rewrites `backend/main.go` (a rebuild and a swap), waits until a GET answers with the new release, then 3 s more.
- Every request is logged: start, latency, status, which release answered, and the body of any non-2xx answer.
- "Stale" counts a GET answered by the previous release more than 50 ms after the new one first answered.

**Command:** `MEASURE_SWAPS=40 hack/demo/measure/run.sh swap` (runs 1 and 2: 10 saves).

**Numbers:**

| run | saves | GET: requests / failed | POST: requests / failed | stale | p50 / p99 / p99.9 (ms) | max (ms) | max outside the swap windows (ms) |
|---|---:|---|---|---:|---|---:|---:|
| run1 (tmpfs) | 10 | 1,019,323 / **0** | 242,668 / **3** | 0 | 0.21 / 1.10 / 1.76 (GET) | 438.3 | 4.2 |
| run2 (disk) | 10 | 909,347 / **0** | 438,016 / **0** | 0 | 0.22 / 1.21 / 1.92 (GET) | 432.5 | 3.6 |
| run3 (prod-like) | 40 | 3,465,731 / **0** | 1,661,807 / **12** | 0 | 0.22 / 1.20 / 1.93 (GET) | 553.7 | 28.3 |
| **all** | **60** | **5,394,401 / 0** | **2,342,491 / 15** | 0 | | 553.7 | |

The swap window runs from 1 s before `build-ok` to 1 s after the first new answer.

What the failures and the spike are:

- **15 failed POSTs, in 11 of 60 saves:** a 502 `backend error: EOF` or `… read: connection reset by peer`.
  - Each started 0.2–1.1 ms before `build-ok`, the swap instant.
  - The old generation's SIGTERM reset a connection xbind had just sent it.
  - `docs/elements.md` (Runtimes & backend lifecycle) documents this. Such a request is sent again only when that is safe: no body, and an idempotent method or an `Idempotency-Key` header (`internal/proxy/reroute.go`).
  - A request with a body is never re-sent, so no header helps a POST with a JSON body. A client that retries a 502 on a write it can repeat covers it.
  - No GET failed.
- **The worst wait, 0.41–0.56 s, hit exactly one request in 35 of 60 saves.**
  - That request started 0.1–0.8 ms before the `reload` event reached the client and was answered 4–8 ms after `build-ok`.
  - It reached the backend between `Runner.Changed` marking the tile dirty and the rebuild goroutine taking the build (`internal/runner/runner.go`), so it took the build itself and waited for it. The old generation was still serving.
  - Every other request answered within 28.3 ms; 99.9 % within 2 ms.

**Raw:** `run{1,2,3}*/swap-requests.csv.gz` (every request) and `swap.summary.json` (failures with bodies, the 20 slowest, each save's timeline).

**Claim:** *"Redeploy under load without dropping a read: 0 of 5.4 million GETs failed across 60 live redeploys at ~30,000 requests/s."* Don't claim zero failed requests: 15 of 2.3 M POSTs that hit the swap instant got a 502. For latency, say "requests keep answering in about a millisecond; at worst one request waits for the rebuild, about half a second".

## 4. Predictive echo at a 300 ms round trip

**Method** (`TestBrowser/predict`, `browser.js predict`):

- **The link.** `hack/demo/latency` is a TCP proxy that holds every chunk 150 ms in each direction, so a round trip gains 300 ms. It sits in front of xbind, and the browser talks to xbind only through it.
  - Ten fresh-connection `GET /healthz` took 301–305 ms.
  - The terminal's own ping measured 301–302 ms.
- **The terminal.** Chromium, signed in, opens `apps/handbook`'s terminal from the shell. Its predictive-echo setting (the wrench menu: auto, on, off) is set through `bx-terminal`'s `testApi().setPredict`; auto is the default.
- **The keys.** Each sample is one key at the shell prompt, from a realistic command line (`git status`, `make test`, …). Spaces are typed but not timed. Between keys the run waits for the echo's ack plus 250 ms, and `Ctrl+U` clears each line.
- **The probe.** A probe armed in the page stops the clock at the key's `keydown` timestamp and observes `.xterm-screen` (MutationObserver):
  - **glyph in the DOM** is the first moment the typed character sits at the cursor's cell, either in the prediction overlay (`.pov`, `web/bx-terminal.js`) or in xterm's rows (the shell's echo);
  - **next frame** is the next `requestAnimationFrame` after that, the latest the glyph is painted.

  For xterm's own echo, rendered inside a frame, "next frame" can be one frame late. So off's honest figure is its DOM time and on's is its next-frame time.
- 40 keys per mode per run, three runs.

**Command:** `hack/demo/measure/run.sh browser`.

**Numbers** (runs 1–3 pooled; every auto and on key was drawn by the prediction, every off key by the echo):

| mode | n | keydown → glyph in the DOM: median / p90 / max | keydown → next frame: median / p90 / max |
|---|---:|---|---|
| auto (default) | 120 | 0.4 / 0.6 / 1.6 ms | 10.6 / 15.5 / 16.7 ms |
| on | 120 | 0.4 / 0.5 / 0.8 ms | 9.7 / 15.5 / 16.5 ms |
| off | 120 | 306.9 / 310.2 / 318.2 ms (min 301.7) | 323.4 / 326.7 / 334.8 ms |

Predicted glyphs are drawn underlined until the server confirms them, because the link is slow (SRTT > 160 ms). Neither the OS's input latency nor the display's scan-out is measured; both modes share them.

**Raw:** `run{1,2,3}*/browser-predict.jsonl` (each key, mode, how it appeared, the RTT), `latency-proxy.log`.

**Claim:** *"With 300 ms between you and the server, what you type appears within one frame (under 17 ms), not after 0.3 s."* The default mode (auto) does it once the link is slower than 100 ms.

This was measured for keys typed one at a time at a shell prompt. Fast bursts, and full-screen programs that hide the cursor (vim, Claude Code; the engine's anchor mode, D71), weren't timed.

## 5. A person's partition: first request and warm requests

**Method** (`TestPartition`):

- **The tile.** `apps/notes` declares `"partition": ["user"]`. Its Go backend reads and writes a per-person `kv` resource. The workspace holds 14 people, each with read access to `apps/*`.
- **Each person,** as their own browser would:
  1. signs in (`POST /api/xbin/login`);
  2. mints the tile's frame token (`GET /api/xbin/frame-token`);
  3. calls `GET /api/apps/notes/notes`. The answer carries their user id, so the call is checked to have reached their own instance.
- **Rows:**
  - **first person ever:** the tile's first build, then their start;
  - **cold start:** each other person's first request (admission, a namespace sandbox, the backend's start and health check; the build is shared);
  - **restart:** `POST /api/xbin/partitions/stop` (what the idle stop does), 0.5 s, then their next request;
  - **warm:** 300 `GET /notes` each for 5 people.
- **Encrypted volumes, on their own.** A tile whose state is a `filesystem`, `sqlite` or `blob` resource also mounts an encrypted volume per person: gocryptfs, which can't mount in this session (Setup). xbind's own `gocryptfs` binary (v2.6.1+xbin) was timed with xbind's flags (`-q -passfile /dev/stdin`, `-scryptn 10` for partition volumes): `init`, a first mount and a remount, 20 times each inside `unshare -Urm`, with the volumes on the NVMe and on tmpfs.

**Command:** `hack/demo/measure/run.sh partition`. For the volumes, `hack/demo/measure/gocryptfs-cost.sh`; the copy that ran sits in `run6-gocryptfs/`, and its output's first line names the binary.

**Numbers** (run3 is production-like; `all` pools runs 1–3):

| series (ms) | n | min | median | p90 | max | all (n): median / p90 / max |
|---|---:|---:|---:|---:|---:|---|
| person's first request (cold start) | 13 | 227.3 | 235.2 | 251.9 | 264.8 | (39) 229.6 / 247.4 / 264.8 |
| restart after a stop (next request) | 12 | 212.6 | 228.9 | 233.0 | 235.1 | (36) 225.1 / 232.8 / 235.1 |
| warm request (`GET /notes`, 2 kv reads) | 1500 | 0.3 | 0.4 | 0.6 | 3.2 | (4500) 0.4 / 0.5 / 3.2 |
| first person ever (tile's first build + start) | 1 | | 4231.8 | | | |
| gocryptfs volume on disk: init + first mount | 20 | 26.1 | 29.7 | 31.6 | 31.6 | |
| gocryptfs volume on disk: remount | 20 | 12.9 | 14.9 | 16.8 | 17.6 | |
| gocryptfs volume on tmpfs: init + first mount | 20 | 22.9 | 25.3 | 27.5 | 30.6 | |
| gocryptfs volume on tmpfs: remount | 20 | 13.9 | 15.8 | 18.4 | 18.7 | |

With the workspace on tmpfs (run1), cold starts were 34 ms faster at the median: 201.2 against 235.2 ms.

**Raw:** `run3-prodlike/partition.jsonl`, `run6-gocryptfs/gocryptfs-cost-{disk,tmpfs}.jsonl`.

**Claims:**

- *"Your own instance starts on your first request in under 0.3 s; after that it answers in about a millisecond"* (39 of 39 cold starts under 265 ms; 98 % of 4,500 warm requests under 1 ms, p90 0.5 ms, p99 1.5 ms, the worst 3.2 ms). Don't say "always under 1 ms".
- For a tile that keeps encrypted files, add up to about 32 ms per volume on a person's first start, or 19 ms on a restart. Two volumes would still fit under 0.35 s (264.8 + 2 × 31.6 ms).
- For comparison, the I2 measurement on a 4-vCPU VPS (`plans/partitions/records/I2.md`) put the agent template's first start at 2.4 s, almost all of it the template's own schema migration.

## 6. Install

**Not measured here.** The installer (`deploy/install.sh`) has to run on a fresh VPS, and this box is a developer workstation with xbin's state all over it.

To measure it:

- Use a clean cloud VM of the size the film names.
- Time the documented one-liner (`docs/overview/15-operations.md`: `XBIN_VERSION=vX.Y.Z curl -fsSL https://xbin.dev/install.sh | sudo bash -s -- --system --prebuilt-rootfs`) from the command to the sign-in page answering.
- Do it on three fresh VMs and claim the slowest.

Until then the film should make no install-time claim.

## Findings for the code owners

The measurements turned up four things. None is fixed here; this branch changes only the measuring tools.

1. **A request can wait for a whole rebuild while the old generation still serves.**
   - In 35 of 60 saves under load, one request took 0.41–0.56 s.
   - `Runner.Changed` sets `dirty` and starts the rebuild on a goroutine. A request that reaches `ensureState` before that goroutine finds `dirty` with no build running, takes the build itself and blocks until the new generation is healthy.
   - Taking the build inside `Changed`, under the same lock (and running it on the goroutine), would leave every request on the old generation until the swap.
   - `docs/elements.md` says "Requests during a rebuild wait for the new one". Measured, all but that one were answered by the old generation.
2. **A swap can drop a request with a body.** 15 of 2.34 M POSTs got a 502 at the swap instant: the old generation's SIGTERM reset a connection xbind had just sent it. This is documented, but "zero-downtime deploys" holds only for reads.
   - Sending the old generation its SIGTERM only once the requests already routed to it have been answered would close the gap; no new request reaches it after the swap.
   - Buffering small bodies so a reset POST can be re-sent would also close it.
3. **Range mode doesn't fall back.** Where `newuidmap` is on PATH and `/etc/subuid` has a range, but the map write fails (here: xbind inside a single-uid user namespace), every sandbox start fails (`newuidmap: write to uid_map failed: Operation not permitted`). Even the Go build's confined run fails, and the tile shows a build error. Probing once at boot and falling back to single-uid mode with a warning would keep such hosts working.
4. **Tile sandboxes refuse to start on a disk below 10 % free,** with no knob: 49 GB free of 1.9 TB here. That is by design (`diskmon.go`), but on big disks an absolute floor might suit operators better.
