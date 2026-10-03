# hack/demo/cam — the camera

Scripted shots of xbin for the promo video and the website's screenshots: a
shot module drives the real UI the way a person would — a visible cursor
gliding on human paths, clicks with a ripple, typing at ~10 characters a
second — and the camera films it as a 4K60 master (or PNG stills), logging
every beat the edit needs to a JSON sidecar. A retake is a rerun.

```sh
# something to film: the UI harness (or the film set's workspace) on :9311
PORT=9311 HARNESS_DIR=/tmp/me/h PLAYWRIGHT_DIR=~/lcad-wasm hack/ui-harness/run.sh --keep adminTabs

C=hack/demo/cam
node $C/shot.js --list
node $C/shot.js agent    --out out/ --url http://127.0.0.1:9311 --mode still     # 3840×2160 PNGs
node $C/shot.js agent    --out out/ --url http://127.0.0.1:9311                  # 4K60 master, headless (beginframe)
$C/capture.sh   terminal --out out/ --url http://127.0.0.1:9311                  # 4K60 master through Xvfb + x11grab
node $C/shot.js terminal --out out/ --url http://127.0.0.1:9311 --mode scratch   # quick webm to block a shot out
```

Needs node, Playwright with its Chromium (`PLAYWRIGHT_DIR` as for the UI
harness), ffmpeg (h264_nvenc when there is an NVIDIA GPU, libx264 otherwise);
capture.sh also needs Xvfb (`xorg-server-xvfb` on Arch, `xvfb` on Debian) —
`capture.sh --check` says what is missing.

## Writing a shot

A shot module exports the action, which is filmed; `setup` runs first and
isn't (log in, dress the set, place the cursor):

```js
module.exports = async (cam) => {
  const doc = await cam.in('apps/agent');          // a tile's document
  await cam.hold(700);                             // a beat for the audience
  await cam.mark('agent', '.card[data-path="apps/agent"]');
  await cam.click(doc.locator('#msg'));            // glide there, click (ripple)
  await cam.type(cam.args.prompt);                 // ~10 chars/s, human rhythm
  await cam.press('Enter');
  await cam.mark('sent', doc.locator('#msg'), { text: cam.args.prompt });
  await doc.locator('#timeline .msg.assistant').last().waitFor();
  await cam.still('answer');                       // a 4K PNG for the website
  await cam.hold(2000);
};
module.exports.defaults = { prompt: 'Which of our services had errors overnight?' }; // --set prompt=…
module.exports.setup = async (cam) => {
  await cam.login();                               // lib.js login(), on this capture's browser
  await cam.openShell();
  await cam.sh((t) => t.setGeom(() => [{ path: 'apps/agent', x: 48, y: 0, w: 1536, h: 864 }]));
  await cam.cursorAt([1500, 300]);
};
```

| | |
|---|---|
| `cam.login(user?, pass?)`, `cam.open(url)`, `cam.goto(path)` | the page (`cam.page`); `--user/--pass`, default admin/admin |
| `cam.moveTo(t, {pace, point, reveal})`, `cam.hover(t)` | a human path to the target (minimum-jerk easing on a gently bowed curve, Fitts's-law timing, a small correction on long throws); scrolls it into view first, smoothly |
| `cam.click(t)`, `dblclick`, `rightClick`, `drag(from, to)` | arrive, settle, press — the cursor dips and an amber ripple spreads — release |
| `cam.type(text)`, `cam.typeIn(t, text)`, `cam.press('Enter')` | ~10 chars/s, log-normal jitter, longer beats after words and before Enter; `\n` is Enter |
| `cam.scroll(dy, {at})` | a wheel flick, eased |
| `cam.hold(ms)` / `cam.sleep(ms)` | a beat (skipped at `--pace fast`) / a wait (never skipped) |
| `cam.mark(name, t?, extra?)` | `{name, t, box, …extra}` into the sidecar; `{optional: true}` tolerates a missing target |
| `cam.still(name, {cursor, caption})` | `<take>-<name>.png` at size × dpr; no cursor unless asked (while x11/screencast roll, the cursor stays: hiding it would show); `caption`: what it shows, kept in the sidecar |
| `cam.cursorAt(t)`, `cam.showCursor(v)` | place / hide the cursor without a move |
| `cam.sh`, `cam.fr`, `cam.waitFor`, `cam.waitSel`, `cam.settle`, `cam.openShell`, `cam.usePersonalScreen`, `cam.openTile`, `cam.in(tile)` | the UI harness helpers (`hack/ui-harness/lib.js`) on this page |
| `cam.args`, `cam.o` | `--set k=v` over the module's `defaults`; the run's options (url, size, …) |

A target is a selector (Playwright syntax, pierces open shadow roots), a
Locator (any frame — `cam.in(tile).locator(…)` reaches into a tile), a box
`{x, y, width, height}` or a point `[x, y]` — CSS px in the viewport. All
randomness (paths, rhythm) comes from `--seed` and the shot's name, so takes
move alike. Wait on conditions (`waitFor`, `waitSel`, a locator's `waitFor`)
and pause with `cam.hold`/`cam.sleep`, never `page.waitForTimeout`: under
beginframe the camera's clock is virtual (below), and a real-time pause
would not be the pause the footage shows.

The cursor is the page's own drawing (`overlay.js`) — headless and Xvfb
captures have no pointer: an arrow, a hand over `cursor: pointer`, an I-beam
over text, in a closed shadow root in the top layer (re-raised over modal
dialogs), animated by the compositor so a busy main thread can't stutter it.
It can't look inside a sandboxed tile's frame, so a move onto a target there
takes the cursor the target itself asks for.

## Options

```
node shot.js <shot> --out DIR [--mode still|video|scratch] [--capture beginframe|x11|screencast]
             [--size 1920x1080] [--dpr 2] [--fps 60] [--quality lossless|high] [--codec auto|h264_nvenc|libx264]
             [--frames png|jpeg] [--pace human|fast] [--url URL] [--user U --pass P] [--take NAME]
             [--seed S] [--set k=v]… [--display :N] [--cursor-scale N] [--keep-frames]
```

`--mode video` makes a master: H.264 lossless 4:4:4 (`--quality high`: 4:2:0
QP 16 for players and editors that can't take 4:4:4), colour-tagged so it
decodes exact. `--mode still` defaults to `--pace fast` (no glides, no
holds): website stills in seconds. A take writes `<take>.mp4` (`.webm` for
scratch), `<take>.json` and `<take>-<still>.png` into `--out`; a rerun
replaces them.

## The sidecar

```json
{ "shot": "agent", "take": "agent", "mode": "video", "capture": "beginframe",
  "viewport": {"width": 1920, "height": 1080}, "dpr": 2, "fps": 60, "seed": 1, "args": {…},
  "roll": {"t": 875.3}, "cut": {"t": 13008.4},
  "video": {"file": "agent.mp4", "startT": 875.3, "frames": 728, …},
  "marks":  [{"name": "composer", "t": 2392, "box": {"x": 937.6, "y": 970.8, "width": 811.3, "height": 30.2}}, …],
  "events": [{"kind": "click", "t": 2341.2, "at": [1217.1, 983.1], "box": {…}}, {"kind": "type", "t": …, "text": "…"}, …],
  "stills": [{"name": "answer", "t": 11800.2, "file": "agent-answer.png", "cursor": false}] }
```

- `t`: ms since the shot started, on the capture's clock — `video time = t −
  video.startT` (frame `round((t − startT) × fps / 1000)`).
- `box`: where the element was on screen, CSS px; × `dpr` for the master's
  pixels. Marks name the beats; events (moves, clicks, typing, keys,
  scrolls) are logged by the camera itself — zoom on a click without marking it.
- The file is rewritten after every mark, so a take that dies keeps its log
  (`error` says why).

## Which capture

Measured on this box (RTX 5070 Ti, Chromium 149, 3840×2160) against the
frame counter (`framecheck/`, below) — each capture filmed 10 s of it and
`check.js` read the video back, judged from the roll:

| capture | the frame counter, 10 s | the page | capture speed | use for |
|---|---|---|---|---|
| `beginframe` (default) | **perfect**: 601/601 frames, each the next page frame; the same with 40 animated cards loading the page | 60 fps | 0.29× real time (0.19× loaded); the terminal and agent shots 0.21–0.23× | **masters** without X, and stills |
| `x11` (capture.sh) | 4K60 sustained, never torn, but not frame-exact: 9–45 phase slips per 10 s (a frame shown twice, the next one skipped — two free-running 60 Hz clocks beating) | 60 fps; 45 fps with 40 animated cards (software compositing at 4K) | real time | masters where live timing matters more than an even cadence |
| `screencast` | 485 distinct frames in 600, uneven (holds up to 83 ms) | slowed to 48 fps by the capture itself | real time | scratch only |
| `scratch` | 25 fps VP8 at 1920×1080: 243 distinct frames, 158 lost, a 1 s hold at the start | 38 fps | real time | blocking a shot out |

Rerun `framecheck` after changing the machine, the GPU driver or Chromium.

**beginframe** runs Playwright's chrome-headless-shell under
`--enable-begin-frame-control`: nothing renders until the camera sends
`HeadlessExperimental.beginFrame`. At the roll the page goes onto virtual
time (`Emulation.setVirtualTimePolicy`); frame k is rendered at exactly
k/60 s and the page's clock — `performance.now`, timers, CSS and Web
Animations — gets one frame of budget per frame, as do the shot's own waits.
However long a 4K frame takes to capture (~55–85 ms as PNG), the footage
plays at exactly the shot's pace. Still frames cost ~1 ms
(the frame is probed for damage first). Caveats:

- Virtual time never runs ahead of real time, but it falls behind while
  frames are slow to capture: what arrives from outside the page — a
  terminal's echo, a server's or a model's answer — lands *earlier* in the
  footage than it would live (a fake model's instant reply shows on the next
  frame). Pace those beats in the shot (`hold`) or in the replayed model.
- Something on the page that reports damage without changing pixels (the
  agent tile does ~20 times a second) costs a screenshot each time: capture
  slows, the footage doesn't change.
- A frame-counting animation (rare; time-based ones are fine) runs one extra
  step on the frame where a still stretch ends.

**x11** is the real display pipeline: capture.sh starts Xvfb (:99,
3840x2160x24, no X cursor); shot.js launches headful Chromium in kiosk at
1920×1080 × device scale 2 (the browser's startup window — the only one kiosk
applies to — with a profile that never shows "Save password?" or a prompt)
and ffmpeg x11grab → h264_nvenc (GPU RGB→YUV 4:4:4, CFR: a missed grab is a
repeated frame, not a gap) into an mkv that survives a crash, remuxed to mp4
at the cut. The recording rolls once frames flow; x11grab's first timestamp
places the video on the sidecar's clock.

## Website stills

`site-stills.sh` shoots the website's stills of the demo film set
(`hack/demo/README.md`): each `shots/site-*.js` on a desk (1440×900 at
device scale 2) and on a phone (390×844 at 3), into one directory, then
`shots.json` beside them (`stills-manifest.js`: file, pixel size, viewport,
the persona, what the frame shows — the shot's `caption` — and the marks
logged for it).

```sh
# the set in the UI harness (hack/demo/README.md), isolated: partitions, VM sandboxes
HARNESS_SEED=demo HARNESS_ISOLATE=1 PORT=9331 HARNESS_DIR=/tmp/me/h hack/ui-harness/run.sh --keep
TZ=America/Los_Angeles $C/site-stills.sh --out .film-media/stills --ws /tmp/me/h/ws --url http://127.0.0.1:9331 [shot…]
```

| shot | who | what |
|---|---|---|
| `canvas` | Maya (CEO) | her Company screen: Lark, the CRM's pipeline, the ops report, the calendar |
| `live` | Priya, then Tomás | the onboarding tracker before and after a code change lands (a go-live timeline: `tiles/onboarding/next/`), and the diff in the tile's code window |
| `agent` | Priya | Lark mid-task (tool calls done, the answer coming), then its answer; a phone films the chat app instead (the agent's page has no narrow layout) |
| `terminal` | Jonas | a shell on the onboarding tile: its files and Lark's commits |
| `sandboxes` | Lukas | the coding sandboxes: three VM sandboxes, their owners and quotas |
| `network` | Tomás | the telematics tile's network, routed through the egress approver |
| `admin` | Tomás | the admin console's people, then viewing the workspace as Priya |
| `partitions` | Priya | her partitions page: her own instance of Lark and of expenses |
| `phone` | Priya | the shell on a phone: her expense book, the inbox, the drawer |

`site.js` is what they share: signing in as a person of `company.json`,
putting their screens from `data/layouts.json` back first (a retake starts
the same), the font a viewport gets (the person's seeded size on a desk,
the shell's 13 px on a phone), waiting for a tile's content, and the still
itself (the mouse parked off every scroller, a caption). Shots that change
the set put it back: `live` restores the tracker's files, `network` binds
the tile again, `agent` deletes its earlier take's conversation. Clicks
inside a tile use `dispatchEvent` or `focus()`: under the shell's font
zoom a pointer's coordinates land off target.

Film in the set's time zone: the browser's clock (TZ) and the seed's
`DEMO_TZ` must agree, or times of day ("05:30", "4:14 PM") shift. The set's
company is American; a US zone keeps its times of day — and "today" —
true to it.

## framecheck

`framecheck/index.html` draws its frame number every `requestAnimationFrame`
as 32 big black/white blocks (`pattern.js`: a 20-bit counter, CRC-8, a sync
pattern — readable through scaling and lossy encoding, and a frame caught
mid-update fails its CRC). `?counter=time` counts frame *time* instead
(skips then also show frames the page never drew); `?stress=N` adds
animated cards under the info line.

```sh
node $C/shot.js framecheck --out out/ --capture screencast --set seconds=10   # any mode/capture
node $C/framecheck/check.js out/framecheck.mp4 [--json report.json] [--expect perfect]
$C/capture.sh --framecheck out/ 10                                            # x11, then the check
node $C/framecheck/selftest.js                                                # the checker on known faults
```

`check.js` reports repeated, dropped (and in how many skips) and torn frames,
the longest hold and the page's own rate. `selftest.js` proves it: a clean
synthetic counter video encoded lossy, then copies damaged by ffmpeg filters
(`select` drops 5 frames in 3 skips, `loop` repeats 3, an overlay of the
next frame's lower rows tears 2, `tpad` adds a lead-in) — the checker must
report exactly that. `hack/demo-cam.test.mjs` (`make js-test`) covers the
pure parts.

## Files

| | |
|---|---|
| `shot.js` | the runner: setup, roll, action, cut, the sidecar |
| `cam.js` | the camera API above |
| `motion.js` | paths, easing, typing rhythm, the seeded randomness (pure) |
| `overlay.js` | the in-page cursor and ripples |
| `clock.js` | the real and the frame clock |
| `rec.js` | the still, scratch, x11 and screencast captures; ffmpeg |
| `beginframe.js` | the deterministic capture |
| `capture.sh` | the Xvfb master path |
| `shots/` | `terminal`, `agent` (examples against the UI harness), `framecheck`; `site-*` (the website stills) |
| `site.js`, `site-stills.sh`, `stills-manifest.js` | the website stills: shared set-up, the runner, `shots.json` |
| `framecheck/` | the frame counter, its checker, the checker's self-test |

## Notes

- **The camera never needs secrets.** What answers in the agent shot is
  whatever model the workspace binds — on the UI harness, `hack/fakeopenai`
  ("ok: <prompt>" to anything unscripted). Real keys (ANTHROPIC_API_KEY,
  OPENAI_API_KEY, …) belong to the workspace's model gateway, not here.
- **Retakes start the same.** The example shots reset what they touch:
  the terminal shot ends the tile's shells and drops its window pref; the
  agent shot deletes exactly the conversations its previous take into the
  same `--out` created (`.agent.agent-runs.json` there).
- **Headless isn't hidden.** Every capture renders at a forced device scale
  factor with scrollbars on (Playwright's headless default hides them; a
  real browser shows them), so stills, beginframe and x11 frames match.
- **Encrypted resources on a sandboxed dev box.** The agent tile keeps its
  data in gocryptfs mounts; where `fusermount3` can't mount (a user namespace
  that maps only your uid), the agent is "held". Starting the harness in its
  own user+mount namespace fixes that:
  `unshare -Urm hack/ui-harness/run.sh --keep adminTabs`.
- Media go under `.film-media/<part>/` of the main checkout (git-excluded) —
  never into git.
