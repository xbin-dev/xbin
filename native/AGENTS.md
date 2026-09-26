# AGENTS.md — working on the native client

The design is **plans/native.md** (with plans/tile-asset-auth.md and
plans/agent-template-native.md). Read it first; decisions are recorded in
plans/DECISIONS.md once taken. This file is the **dev loop**: how to build,
test and see native work on a Linux box with no Mac, and how to use CI as the
only Apple toolchain.

## The situation

- **No Mac, no Xcode, no simulator locally.** The Apple toolchain exists only
  in GitHub Actions (`runs-on: xcode-27`). A Mac over ssh may come later; this
  loop must not depend on it.
- **Fast local loops exist** for everything that isn't SwiftUI/UIKit: Go
  (xbind), JavaScript (the runtime, the Lit reference renderer), Swift 6 on
  Linux (Foundation only), headless chromium for screenshots.
- **Each CI round trip takes 10–20 minutes** (push to green). Batch changes;
  never push to "see if it compiles" what could have been checked locally.

## Layout (as it lands)

```
native/
  AGENTS.md, README.md
  fixtures/<name>/          the contract every renderer is tested against
    native.js               tile code under test
    data.json               scripted xbin stub: responses, SSE frames, bus events, now/locale/tz
    expected.json           the rendered tree {"v":1,"root":…}
  spec/                     the wire contracts: tree.md (the runtime bridge), vocab.json (exported
                            from web/xb/vocab.js), device-login.md, push.md (+ push-vectors.json)
  ios/
    project.yml             XcodeGen spec — the .xcodeproj is generated, never committed
    Packages/XbinCore/      Foundation only, tests on Linux: JSON, tree + patches, the runtime bridge,
                            deep links, device login, and Client/ (the app's workspace logic)
    Packages/XbinTerm/      Foundation only, tests on Linux: /ws/term codec + session, the predictive
                            echo port, the keyboard model
    Packages/XbinAgent/     Foundation only, tests on Linux: ACP agent sessions (models, transcript
                            reducer, feed, HTTP client), held to the web's output
    Packages/XbinRenderer/  XbinRendererModel (tests on Linux) + the SwiftUI views, one per primitive,
                            a #Preview per fixture; Tests/XbinRendererTests = the snapshot tests (CI)
    App/                    the app target: Model/ (workspaces, sessions, transport, device keys),
                            Shell/ (root, switcher, navigator, add-workspace, inbox, settings),
                            Tiles/ (scheme handler, web tiles, native tiles), Terminal/, Agent/, Push/
    Shared/                 compiled into the app AND the notification extension (push crypto, Keychain)
    NotificationService/    the Notification Service Extension (decrypts pushes)
    Support/                Info.plists and entitlements
    SnapshotHost/           the empty app the hosted snapshot tests run in (CI only, see §4)
    scripts/                CI: pick-sim.sh, ci-*.sh (what ios.yml runs), ci-local-check.sh
  tools/                    fixture runner (fixture.mjs), shots.mjs + gallery/ (reference screenshots),
                            swiftui-stubcheck/, term-stubcheck/ (App/Terminal against stubs),
                            app-check/ (the app's UIKit-free sources on Linux),
                            term-live/, agent-parity.mjs, bridge-check.mjs, runtime-check.mjs,
                            markdown-parity.mjs
web/xb-native.js            the runtime's template layer, served at /vendor/ (frozen once shipped)
web/xb/                     the runtime's modules (rt-*.js, vocab.js) and the Lit reference renderer
                            (render*.js, preview-host.js — previews and tests only)
relay/                      the push relay (Go, stdlib only, its own module)
.github/workflows/ios.yml   the Apple CI
```

## The loop, fastest first

### 1. Go — xbind additions (seconds)

Routes, the runtime document, device auth, notify, the relay: ordinary repo
work (`make test`, `make check`; AGENTS.md at the root has the rules — docs in
the same change, additive APIs, D78 confinement).

### 2. JavaScript — runtime and reference renderer (seconds)

- `xb-native.js` and the Lit renderer are plain ES modules: **no build step,
  no TypeScript** (root AGENTS.md hard rules). `make js-check` covers syntax.
- The fixture runner (`node native/tools/fixture.mjs`, `make native-check`)
  renders `native/fixtures/<name>/native.js` against its `data.json` in node
  (xb-native's JSON target), plays its interactions and diffs with
  `expected.json`; a full check also demands that the fixtures exercise the
  whole vocabulary. `--update <name>` rewrites one and prints what changed.
  Updating a fixture is a reviewed diff of `expected.json`, never a blind
  regenerate. Format and how to add one: native/fixtures/README.md.
- The JSON target is `hack/xbn/node.mjs`: `runNative({entry, data, steps})`
  (or `node hack/xbn/node.mjs native.js [data.json] [steps.json]`) runs a
  `native.js` in a worker with `/vendor/` resolved from `web/`, a scripted
  `xbin` stub and a virtual clock, and returns the tree and every message.
  The wire contract is `native/spec/tree.md`; the vocabulary
  `native/spec/vocab.json` (from `web/xb/vocab.js`). `make js-test` runs
  `hack/xb-native*.test.mjs`, including the plans/native.md §18 trees.
  For a real tile's tests, `data.setup` swaps in the tile's own fake backend
  (a module), steps may name nodes by what they are (`{tap: {t: 'button',
  p: {label: 'Retry'}, in: {t: 'composer'}}}`) and `{snapshot: name}` keeps
  the tree mid-run — see `hack/agent-template-native.test.mjs`.
- Screenshots of the reference renderer (`web/xb/render.js`, `<xb-view>`):
  `PLAYWRIGHT_DIR=~/lcad-wasm node native/tools/shots.mjs --out <dir>` draws
  every `native/fixtures/*/expected.json` (or, with none, the design's §18
  trees) at **390×844**, light **and** dark, default and large text
  (`--trees native/tools/gallery` adds trees that exercise every primitive;
  `--full` grows the view to its content; `--sheet` makes a contact sheet).
  **Look at the PNGs** (the Read tool shows images) and fix what's wrong
  before moving on. `node --test hack/xb-render.test.mjs` checks that every
  visible node is drawn and drives a runtime through the preview host.

### 3. Swift 6 on Linux — the packages (tens of seconds)

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"   # swiftly-installed Swift 6.4
make swift-test                                      # all four packages (skips without swift)
cd native/ios/Packages/XbinCore && swift test        # or one of them
```

Everything that doesn't draw lives in four SwiftPM packages that build and
test here. **XbinCore**: the JSON value type, tree decoding, patch
application, the runtime bridge (native/spec/tree.md), deep links, the
workspace records, device-login messages (native/spec/device-login.md), the
fixture codec, and `Client/` — the app's workspace logic (sessions and
re-sign, enrollment, the catalog, the scheme handler's rules, the tile
bridge, native tile fallback, push, markdown). Rules for all of them:

- **Foundation only.** No SwiftUI, UIKit, Combine, OSLog — and no
  `#if canImport(SwiftUI)` escape hatches. Transports are injected (a protocol
  for "send/receive frames"), so Core never needs URLSession (which lives in
  FoundationNetworking on Linux).
- JSON booleans stay distinct from numbers (don't round-trip through
  `NSNumber`); key order doesn't matter, key presence does.
- Anything testable here is tested here — CI time is for what only Xcode can do.
- **Stacks are small on Apple platforms.** Swift Testing runs tests on the
  cooperative pool, whose threads get 512 KiB stacks there and 8 MiB on
  Linux: a recursive JSON parser passed here and crashed CI's run with
  SIGBUS (fixed in 3facd67). `make swift-test` therefore runs the tests
  under `ulimit -s 512` (glibc sizes new threads from it; the old parser
  fails that way here too). By hand: `swift build --build-tests && (ulimit
  -s 512; swift test --skip-build)`. Code that recurses on tile-supplied
  input keeps an explicit stack instead.

**XbinTerm**, the terminal's half, is its own package under the same rules,
`Packages/XbinTerm` (`swift test` there; its README says how the app glues
SwiftTerm to it): the `/ws/term` codec and session state machine, the
predictive echo port and the keyboard model. The port's conformance is every
case of `hack/term-predict.test.mjs` plus a differential trace of the JS
engine: a change to `web/term-predict.js` runs `node
hack/term-predict-trace.mjs` and ports the change in the same commit (`make
js-test` fails until the trace matches). `native/tools/term-live` checks a
session against a running xbind.

**XbinAgent** is the native model of ACP agent sessions (the Agent tab):
Codable models of every agent route and event, the transcript reducer
(bx-agent.js's `_blocks()` made incremental), the D77 permission and
headline rules, diffs, and `AgentSessionFeed` (replay, follow, catch-up).
It is held to the web's own output: `node native/tools/agent-parity.mjs`
runs the web's modules over a corpus and the captured sessions
(`Tests/XbinAgentTests/Fixtures`, written by the gated Go test
`internal/term/agent_capture_test.go` driving `hack/fakeacp`) and
`ParityTests` requires the Swift port to match exactly — regenerate both
together (the package README says how).

**XbinRenderer** follows the same split: it keeps everything that doesn't
draw in its `XbinRendererModel` target (the tree as observable nodes,
controlled props, the vocabulary tables, markdown/chart/question/chat view
models — `swift test` there, held to `vocab.json`, the fixtures and the Lit
renderer's output); its SwiftUI target is empty off Apple platforms. After changing a view, run
`native/tools/swiftui-stubcheck/run.sh` (and `--sendable-bindings`): it
type-checks the views against stubs of the SDK — our mistakes, not SDK drift.

### 3b. The app's own code on Linux (a minute)

The app target is SwiftUI/UIKit/WebKit and only compiles on CI, so its
decisions live in the packages (XbinCore `Client/`: sessions and re-sign,
enrollment, the catalog, the scheme handler's rules, the tile bridge, native
tile fallback, push, markdown) and are tested there. What remains in the
app but needs no UIKit is checked by `native/tools/app-check`, which compiles
the very files (symlinks) against swift-crypto and SwiftTerm's headless
`Terminal`:

```sh
cd native/tools/app-check && swift test          # push vectors, device-login vector, SwiftTermScreen + predictor,
                                                 # the terminal's selection vs SwiftTerm's own copy, TerminalPrefs
swift run app-live 127.0.0.1:9461 admin admin    # the workspace client against a running xbind (--dev: admin/admin)
PLAYWRIGHT_DIR=~/lcad-wasm node native/tools/bridge-check.mjs http://127.0.0.1:9461
                                                 # the tile bridge + xbin-client in headless Chromium
```

`app-live` covers password sign-in, in-app enrollment, device login, one
re-sign for concurrent requests on a dead session, a tile page by frame
token, push registration and device removal. Before touching an app file,
`swiftc -frontend -parse <file>` at least catches syntax errors here; for
`App/Terminal`, `native/tools/term-stubcheck/run.sh` (and `--sdk-27-1`)
type-checks the whole directory against stubs, as the renderer's stubcheck
does for XbinRenderer.

### 4. Apple — only through GitHub Actions (minutes)

The workflow is `.github/workflows/ios.yml` (`runs-on: xcode-27`). It runs on
a push to **any branch but master** that touches `native/ios/**`,
`native/fixtures/**`, `native/spec/**` or the workflow itself, and by hand
(`gh workflow run ios.yml --ref <branch>`); a newer push to the same branch
cancels the run in progress. Three independent jobs — a broken app build
still yields snapshots:

| job | does | artifacts |
|---|---|---|
| `packages` | logs the toolchain (`xcodebuild -version`, `-showsdks`, simulator device types and runtimes); `swift test` in `Packages/XbinCore`, `XbinTerm`, `XbinAgent` — each if present, all run even when one fails (XbinRenderer's model tests run in `snapshots`) | — |
| `app` | `brew install xcodegen` → `xcodegen generate` (if `project.yml` exists) → `xcodebuild build -scheme Xbin` for a simulator, `CODE_SIGNING_ALLOWED=NO` | `xcresult-app` (`app-build.xcresult` + the full `app-build.log`) |
| `snapshots` | `xcodebuild test -scheme XbinRenderer` in `Packages/XbinRenderer` on a simulator: every fixture, light/dark × the default, xxxLarge (`large`, the reference's large text) and accessibility2 (`ax2`) Dynamic Type sizes; then the same tests hosted by an app (`ci-hosted-snapshots.sh`, below) | `snapshots` (the PNGs; the hosted ones under `hosted/`), `xcresult-snapshots` (`.xcresult` + log of both) |

Artifacts upload even when a step fails; each job's summary page has a
one-line result (toolchain, package table, PNG count).

The logic is in `native/ios/scripts/`, so it runs the same on any Mac (and
a Mac over ssh later) and most of it is checked here:

- `pick-sim.sh` — prints the destination (`platform=iOS Simulator,id=…`):
  an iPhone on the newest iOS runtime, newest model generation, base model
  before Pro/Max; no iPhone → any iOS simulator; none at all → creates one.
  `XBIN_SIM="iPhone 17 Pro"` prefers a name.
- `ci-toolchain.sh` (every job), `ci-swift-test.sh`, `ci-build-app.sh`,
  `ci-snapshots.sh`, `ci-hosted-snapshots.sh`, sharing `ci-lib.sh`. Bash 3.2 (macOS's), no GNU-only
  flags. Results go to `$RUNNER_TEMP/xbin-ci/`, which is what the workflow
  uploads.
- Pin an Xcode when the runner has several: set `XBIN_XCODE:
  /Applications/Xcode_27.1.app` in the workflow's `env` (empty = the
  runner's default).

What the packages and tests must do for CI:

- **`swift test` runs on the macOS host**, not a simulator: in XbinCore,
  XbinTerm and XbinAgent, UIKit-only code sits behind `#if canImport(UIKit)`;
  anything needing a simulator is tested through xcodebuild (XbinRenderer).
  Declare a macOS platform next to iOS in `Package.swift` (`platforms:
  [.iOS(.v18), .macOS(.v15)]`, say) — otherwise SwiftPM builds for its old
  default macOS target and newer Foundation APIs fail availability checks
  on CI though they pass on Linux, where `platforms` is ignored.
- **The app scheme is `Xbin`**, shared, declared in `project.yml`
  (`targets.Xbin.scheme` or `schemes.Xbin`); the package scheme is
  `XbinRenderer` (`XbinRenderer-Package` is used if that's the only one).
- **Snapshot tests write PNGs to `SNAPSHOT_DIR`** (the environment of the
  test process; the workflow sets `TEST_RUNNER_SNAPSHOT_DIR` and xcodebuild
  strips the prefix), named `<fixture>-<light|dark>-<size>.png` with sizes
  `default`, `large` (xxxLarge — what `shots.mjs` draws as large, so the two
  compare like for like) and `ax2` (accessibility2, iOS only: the overflow
  test); the contact sheet below uses `default` (and `large` for a second).
  `FIXTURES_DIR` is the absolute path of `native/fixtures`. When `SNAPSHOT_DIR`
  is unset (Xcode locally) tests skip writing rather than fail. Images a test
  only *attaches* to the result are exported into `snapshots/attachments/` as
  a fallback.
- **Two snapshot runs, one test file.** The package run has no app, so its
  windows are offscreen and drawn with `layer.render`, which skips Liquid
  Glass, materials and vibrancy: bar items, back buttons, tab bars and the
  bottom search field come out white or blank, and a scrolled navigation bar
  shows the content under it. `project.yml` therefore also declares
  `XbinSnapshotHost` (an empty app) and `XbinSnapshotTests` (the same
  `SnapshotTests.swift`, compiled with `XBIN_SNAPSHOT_HOST`), scheme
  `XbinSnapshots`: there the windows sit on the host's window scene and are
  drawn with `drawHierarchy`, as the screen shows them
  (`ci-hosted-snapshots.sh`, PNGs in `snapshots/hosted/`, same names). Use the
  hosted PNGs for comparisons when the run produced them.

Before pushing anything under `.github/workflows/ios.yml` or
`native/ios/scripts/`:

```sh
native/ios/scripts/ci-local-check.sh   # YAML shape (python3+PyYAML or ruby), actionlint if
                                       # installed, bash -n + shellcheck, and ci-dry-test.sh:
                                       # the scripts against fake xcrun/xcodebuild/xcodegen/swift
CI_LOCAL_BASH32=1 native/ios/scripts/ci-local-check.sh   # + the dry test under bash 3.2
                                       # (macOS's) in a container — after editing a script
```

`make shellcheck` (part of `make check`) covers `native/ios/scripts/` too.

Then:

```sh
git push -u origin <feature-branch>                     # never master
gh run list --workflow ios.yml --branch <feature-branch> -L 1
gh run watch <run-id> --exit-status
gh run view <run-id> --log-failed                       # on failure: read, fix, batch, push once
gh run download <run-id> -D "$SCRATCH/ios-<run-id>"     # snapshots/, xcresult-app/, xcresult-snapshots/
gh run download <run-id> -n snapshots -D "$SCRATCH/ios-<run-id>/snapshots"   # just the PNGs
```

- **Check the SDK before using new APIs.** Every job logs `xcodebuild
  -showsdks` and `xcrun simctl list devicetypes` / `runtimes` (its
  "toolchain" step). The iPhone Duo APIs (`ArrangementView`, reserved regions, hinge)
  come with the **iOS 27.1** SDK; gate their use with `#available(iOS 27.1,
  *)` and confirm the names against the SDK the runner actually has — the
  design cites them from secondary sources.
- **Batch.** One push should carry every fix you can make from one failure log.

What the runner turned out to be (runs 36237646621–36243877514, 2026-09-26):

- `xcode-27` is Xcode 27.0 (27A266a) with Swift 6.4 and **only the iOS 27.0
  SDK and simulator runtime**. There is no 27.1 SDK yet, so no Duo API can be
  compiled on CI. When the image gains one, pin it with `XBIN_XCODE`.
- The image has no **Metal Toolchain**, and SwiftTerm's shader needs it.
  `ci-build-app.sh` fetches it (`xcodebuild -downloadComponent
  MetalToolchain`, about 840 MB, a few seconds). The build runs with
  `-IDEBuildingContinueBuildingAfterErrors=YES`, so one log lists every
  target's errors.
- Job times: `packages` takes about 1.5 minutes, `app` 2–3 and `snapshots`
  11–17 (both snapshot runs, 162 PNGs each). The `snapshots` artifact is
  about 90 MB.
- **Xcode's type checker gives up where the stub check doesn't.** One big
  initializer call full of inline closures, one of them behind `?:`, failed
  with "failed to produce diagnostic for expression" (`NativeTile.swift`'s
  `services`, 1904702). Build such arguments as typed locals first. A
  ternary between closures also loses `@Sendable` inference, which shows up
  as a Swift 6 data-race warning.
- `actions/checkout@v4` and `upload-artifact@v4` get a Node 20 deprecation
  annotation (the runner forces Node 24). This is harmless for now; bump the
  majors when newer ones are out.

## Comparing iOS with the reference

After a green run, download the snapshots (the `hosted/` ones when present:
they show bars and materials as a device does), render the same fixtures with
the reference renderer (`shots.mjs`, same names), and compare side by side,
fixture by fixture. Differences that are intended platform conventions stay:
fonts and their metrics, control chrome (glass bar items, segmented controls
without badges, switches, menus), list insets and grouped section headers,
swipe actions for the reference's `⋯` menus, pull to refresh for its refresh
button, a floating tab bar or bottom search field over the content, a chat
anchored at the bottom. Anything else (missing content, wrong tone, broken
layout, clipped text at large Dynamic Type) gets fixed. Not bugs, the
harness: a secure field's text never shows in a capture (iOS keeps it out),
and without the app's services a `canvas` or `terminal` is a placeholder and
tile images are grey boxes (the reference's previews show the same boxes).
Then make the contact sheets (`size` = `default`, then `large`):

```sh
# one row per fixture: iOS light | web light | iOS dark | web dark
for f in $(ls native/fixtures | grep -v README); do
  montage -label "$f · iOS light" ios/$f-light-$size.png -label "$f · web light" web/$f-light-$size.png \
    -label "$f · iOS dark" ios/$f-dark-$size.png -label "$f · web dark" web/$f-dark-$size.png \
    -tile 4x1 -geometry 390x844+8+8 -background '#111' -fill '#ccc' -pointsize 18 "$SCRATCH/row-$f.png"
done
montage "$SCRATCH"/row-*.png -tile 1x -geometry +0+12 -background '#111' -depth 8 contact-sheet.png
```

## Rules

- **Push only to feature branches.** No signing, no secrets, no TestFlight,
  no provisioning — not even "just to try".
- **Never commit the `.xcodeproj`**; `project.yml` is the source.
- **The fixtures are the contract.** A vocabulary change updates, in one
  change: the fixture(s), `expected.json`, the runtime, the reference renderer,
  the SwiftUI renderer, and the design (plans/native.md now; docs/native.md once
  the runtime ships — docs describe what exists).
- **Additive only** for anything a shipped app or a shipped tile depends on:
  the vocabulary, the tree format, `xb-native.js`, `xbin.native`, the bridge
  (docs/compat.md).
- **No device APIs for tile code**, ever (plans/native.md §2).
- **Report honestly** what was verified where: "passes `swift test` on Linux",
  "compiles on CI", "snapshot looked right", "not run on a device".
- **Shared checkout:** other agents often work in `~/buxon`. Use a worktree
  (`git worktree add ~/buxon-native -b <branch> master`) and review
  `git status`/diffs before committing.
