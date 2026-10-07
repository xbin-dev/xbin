# AGENTS.md — working on the native client

The design is **plans/native.md** (with plans/tile-asset-auth.md and
plans/agent-template-native.md). Read it first; decisions are recorded in
plans/DECISIONS.md once taken. This file is the **dev loop**: how to build,
test and see native work on a Linux box with no Mac, and how to use CI as the
only Apple toolchain.

## The situation

- **No Mac, no Xcode, no simulator locally.** The Apple toolchain is GitHub
  Actions (the hosted `xcode-27` runner) and, once it exists, the owner's
  **Mac mini**: the same CI on it is one repository variable away, and
  `native/ios/scripts/mac-remote.sh` drives it over ssh ("Mac mini" below).
  Nothing here may depend on the Mac being there.
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
    Widgets/                the widget extension: the agent turn's Live Activity (Shared/: also in the app)
    Support/                Info.plists and entitlements
    SnapshotHost/           the empty app the hosted snapshot tests run in (CI only, see §4)
    UITests/                XbinUITests: the app end to end on a simulator against a real xbind
    scripts/                CI: pick-sim.sh, ci-*.sh (what ios.yml runs), ci-local-check.sh and the
                            dry tests; the Mac: mac-setup.sh, mac-remote.sh, mac-cleanup.sh,
                            release-build.sh (the release user's signed build; unused yet),
                            e2e-xbind.sh (the UI tests' xbind, on this box), footage.sh (screen
                            recordings for the promo video and the website)
  tools/                    fixture runner (fixture.mjs), shots.mjs + gallery/ (reference screenshots),
                            swiftui-stubcheck/, term-stubcheck/ (App/Terminal against stubs),
                            uitest-stubcheck/ (the UI tests vs XCUITest stubs),
                            app-stubcheck/ (Model, Shell, hatches, Agent against stubs),
                            widget-stubcheck/ (the widget extension and Live Activities),
                            app-check/ (the app's UIKit-free sources on Linux),
                            term-live/, hatches-live/, events-live.mjs, agent-parity.mjs,
                            bridge-check.mjs, runtime-check.mjs, markdown-parity.mjs
web/xb-native.js            the runtime's template layer, served at /vendor/ (frozen once shipped)
web/xb/                     the runtime's modules (rt-*.js, vocab.js) and the Lit reference renderer
                            (render*.js, preview-host.js — previews and tests only)
relay/                      the push relay (Go, stdlib only, its own module)
.github/workflows/ios.yml   the Apple CI (ci.yml's `native` job: the Linux half, below)
```

## The loop, fastest first

### 1. Go — xbind additions (seconds)

Routes, the runtime document, device auth, notify, the relay: ordinary repo
work (`make test`, `make check`; AGENTS.md at the root has the rules — docs in
the same change, additive APIs, D78 confinement). With a read-only module
cache, set only `GOMODCACHE` for `make check`. `GOFLAGS=-mod=mod` fails
`make vet` under the repo's go.work ("-mod may only be set to readonly or
vendor when in workspace mode"); it is for single-module builds only.

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
make swift-stubcheck                                 # the SwiftUI/UIKit code against SDK stubs (every *-stubcheck)
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
The Live Activity code (`Widgets/`, `App/Push/`) has its own:
`native/tools/widget-stubcheck/run.sh` (strict ActivityKit/WidgetKit stubs —
`Activity` is not Sendable there — plus shims of the app types it touches);
its model is XbinAgent's `LiveActivity.swift`, tested with the package.

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

The rest of the app's Model and Shell (SwiftUI, windows, the events socket's
owners), a native tile's hatches and the Agent tab type-check together
against stubs of the SDK — `native/tools/app-stubcheck/run.sh` (and
`--sendable-bindings`), the app's counterpart of swiftui-stubcheck: run it
after touching `App/Model`, `App/Shell`, `App/Agent` or a hatch in
`App/Tiles` (its README lists what it covers and how the stubs layer).

`app-live` covers password sign-in, in-app enrollment, device login, one
re-sign for concurrent requests on a dead session, the app's `/ws/events`
socket (the device session as its bearer, via `native/tools/events-live.mjs`
since libcurl here has no WebSockets; `APPLIVE_WORKSPACE=<dir>` adds a file
change → the open tile's reload), the web ticket through its "Continue as"
page to a cookie session (or its fallback), minting a code that enrolls a
second device, a tile page by frame token, push
registration and device removal. Before touching an app file,
`swiftc -frontend -parse <file>` at least catches syntax errors here; for
`App/Terminal`, `native/tools/term-stubcheck/run.sh` (and `--sdk-27-1`)
type-checks the whole directory against stubs, as the renderer's stubcheck
does for XbinRenderer. `native/tools/hatches-live` (its header says how)
runs the hatches' non-UI halves — the frame-token upload, the tile pty
socket, prompt attachments — against a running xbind with its test tile.

### 4. Apple — GitHub Actions (minutes), or the Mac mini

The workflow is `.github/workflows/ios.yml`. It runs on a push to **any
branch but master** that touches `native/ios/**`, `native/fixtures/**`,
`native/spec/**` or the workflow itself, and by hand (`gh workflow run
ios.yml --ref <branch>`); a newer push to the same branch cancels the run
in progress. Three independent jobs — a broken app build still yields
snapshots:

| job | does | artifacts |
|---|---|---|
| `packages` | logs the toolchain (`xcodebuild -version`, `-showsdks`, simulator device types and runtimes); `swift test` in `Packages/XbinCore`, `XbinTerm`, `XbinAgent` — each if present, all run even when one fails (XbinRenderer's model tests run in `snapshots`) | — |
| `app` | xcodegen if missing (`ci-xcodegen.sh`) → `xcodegen generate` → `xcodebuild build -scheme Xbin` for a simulator, `CODE_SIGNING_ALLOWED=NO` → `build-for-testing -scheme XbinUITests` (compile only: there is no xbind to test against; "Mac mini" below runs them) | `xcresult-app` (`app-build.xcresult` + the full `app-build.log`, `uitests-build.*`) |
| `snapshots` | `xcodebuild test -scheme XbinRenderer` in `Packages/XbinRenderer` on a simulator: every fixture, light/dark × the default, xxxLarge (`large`, the reference's large text) and accessibility2 (`ax2`) Dynamic Type sizes; then the same tests hosted by an app (`ci-hosted-snapshots.sh`, below) | `snapshots` (the PNGs; the hosted ones under `hosted/`), `xcresult-snapshots` (`.xcresult` + log of both) |

Artifacts upload even when a step fails; each job's summary page has a
one-line result (toolchain, package table, PNG count, cache).

**Where it runs.** Every job's `runs-on` is `${{ fromJSON(vars.XBIN_IOS_RUNNER
|| '"xcode-27"') }}`: without the repository variable, GitHub's hosted
`xcode-27` runner; with `XBIN_IOS_RUNNER=["self-hosted","macOS","xbin-mini"]`,
the Mac mini ("Mac mini" below). Switching is one command either way:

```sh
gh variable set XBIN_IOS_RUNNER --body '["self-hosted","macOS","xbin-mini"]'   # the Mac mini
gh variable delete XBIN_IOS_RUNNER                                             # back to xcode-27
```

**Caches.** Each job's DerivedData and SwiftPM clones (`SourcePackages`,
shared by every xcodebuild of the job through
`-clonedSourcePackagesDirPath`) outlive it (`ci-cache.sh`): on a hosted
runner through `actions/cache`, keyed `ios-<job>-<Xcode version>-<hash of
native/ios's committed tree, project.yml and every
Package.swift/Package.resolved>`, with the newest entry for that Xcode as
the fallback (the build is incremental; a stale tree beats none), saved
after a green job whose sources changed; on a self-hosted runner in
`~/Library/Caches/xbin-ci/<runner>/<Xcode>/` on its own disk (nothing
uploaded; `mac-cleanup.sh` drops trees unused for a week). A restored tree
only helps because `ci-cache.sh` also gives every tracked file under
`native/ios` an mtime derived from its content (a checkout stamps them all
"now", and Xcode and the Swift driver decide by mtime) and sets XCBuild's
`IgnoreFileSystemDeviceInodeChanges` default (inodes differ per checkout):
before that a hit still recompiled all our sources; after it, an unchanged
app builds in 14 s with nothing compiled (run 36260203523). Builds skip the
index store (`COMPILER_INDEX_STORE_ENABLE=NO`). A suspected stale cache:
`gh variable set XBIN_CI_CACHE --body off` (a clean build; delete the
variable after), or bump `XBIN_CI_CACHE_VERSION` in the workflow to drop
every entry. xcodegen is used when on PATH, else the pinned release zip
(SHA-256 checked), else Homebrew; the Metal toolchain is fetched only when
`xcrun metal` fails.

The logic is in `native/ios/scripts/`, so it runs the same on any Mac (the
Mac mini over ssh included) and most of it is checked here:

- `pick-sim.sh` — prints the destination (`platform=iOS Simulator,id=…`):
  an iPhone on the newest iOS runtime, newest model generation, base model
  before Pro/Max; no iPhone → any iOS simulator; none at all → creates one.
  Never the UI tests' own `xbin-e2e*` (an e2e run erases it), not even
  through `XBIN_SIM` — only `XBIN_SIM_ENSURE` reaches it.
  `XBIN_SIM="iPhone 17 Pro"` prefers a name; `XBIN_SIM_ENSURE=xbin-e2e` uses
  (or creates) the device of exactly that name.
- `ci-toolchain.sh` (every job), `ci-cache.sh`, `ci-xcodegen.sh`,
  `ci-swift-test.sh`, `ci-build-app.sh`, `ci-uitests.sh`, `ci-snapshots.sh`,
  `ci-hosted-snapshots.sh`, sharing `ci-lib.sh`. Bash 3.2 (macOS's), no
  GNU-only flags. Results go to `$RUNNER_TEMP/xbin-ci/`, which is what the
  workflow uploads.
- Knobs in the workflow's `env`: `XBIN_XCODE: /Applications/Xcode_27.1.app`
  pins an Xcode when the runner has several (empty = the runner's default);
  `XBIN_SWIFT_CONDITIONS: XBIN_SDK_27_1` compiles the iPhone Duo code (every
  build gets `SWIFT_ACTIVE_COMPILATION_CONDITIONS=$(inherited) …`) — only
  with an Xcode that has the iOS 27.1 SDK, so the two go together.
  `XBIN_SIGNING=adhoc` signs to run locally (the scripts' default is
  unsigned; `mac-remote.sh run`/`e2e` use adhoc, the app's Keychain needs
  its entitlements).

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
  `XbinRenderer` (`XbinRenderer-Package` is used if that's the only one);
  the UI tests' is `XbinUITests` (`schemes.XbinUITests`).
- **Snapshot tests write PNGs to `SNAPSHOT_DIR`** (the environment of the
  test process; the workflow sets `TEST_RUNNER_SNAPSHOT_DIR` and xcodebuild
  strips the prefix), named `<fixture>-<light|dark>-<default|large|ax2>.png`:
  `default`, `large` (xxxLarge — what `shots.mjs` draws as large, so the two
  compare like for like) and `ax2` (accessibility2, iOS only: the overflow
  test). A fixture with a `split` is also drawn as an iPad shows it,
  1180×820 points at a regular width, as `<fixture>-<light|dark>-ipad.png`
  (D189). `FIXTURES_DIR` is the absolute path of `native/fixtures`. When
  `SNAPSHOT_DIR` is unset (Xcode locally) tests skip writing rather than
  fail. Images a test only *attaches* to the result are exported into
  `snapshots/attachments/` as a fallback.
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
- **UI tests** (`native/ios/UITests`, target and scheme `XbinUITests`, host
  app `Xbin`) skip unless `XBIN_E2E_URL`, `XBIN_E2E_USER` and
  `XBIN_E2E_PASSWORD` reach them (`TEST_RUNNER_XBIN_E2E_*`); they sign in
  through the app's Log in as a person does (the app has no token login)
  and query the app by what a person sees
  (labels, placeholders), so a renamed button breaks them — run
  `native/tools/uitest-stubcheck/run.sh` after editing them, and "Mac mini"
  below to run them.

Before pushing anything under `.github/workflows/` or `native/ios/scripts/`:

```sh
native/ios/scripts/ci-local-check.sh   # ios.yml + ci.yml shape (the runner expression evaluated,
                                       # the cache wiring, action majors), actionlint if installed,
                                       # bash -n + shellcheck, ci-dry-test.sh + mac-dry-test.sh (the
                                       # scripts against fake xcrun/xcodebuild/xcodegen/swift/ssh…),
                                       # the UI tests against XCUITest stubs (with swift on PATH)
CI_LOCAL_BASH32=1 native/ios/scripts/ci-local-check.sh   # + the dry tests under bash 3.2
                                       # (macOS's) in a container — after editing a script
XCODEGEN=/path/to/xcodegen native/ios/scripts/ci-local-check.sh   # + project.yml generates, with
                                       # the three schemes (XcodeGen builds on Linux: swift build)
```

`make shellcheck` (part of `make check`) covers `native/ios/scripts/` too, and
ci.yml's **`native` job** (Linux, on master and pull requests) runs Swift 6.4
through swiftly (`ci-linux-swift.sh`: the pinned swiftly, SHA-256 checked;
the toolchain cached on the script's pins) with `make swift-test` and `make
swift-stubcheck`, then `make native-check` and `CI_LOCAL_BASH32=1
ci-local-check.sh` with actionlint.

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
  "toolchain" step). The iPhone Duo APIs (`ArrangementView`, reserved
  regions, hinge) come with the **iOS 27.1** SDK: they sit behind `#if
  XBIN_SDK_27_1` (off unless `XBIN_SWIFT_CONDITIONS` says so) and
  `#available(iOS 27.1, *)`; confirm the names against the SDK the runner
  actually has — the design cites them from secondary sources.
- **Batch.** One push should carry every fix you can make from one failure log.
- **A clean merge proves nothing about the scripts.** native and
  native-ios2 both reserved the UI tests' simulator in `pick-sim.sh`, each
  under the name `reserved`: a function on one side, a set on the other.
  git merged them without a conflict, the set shadowed the function, and
  every pick crashed. After merging branches that both touched
  `native/ios/scripts/` or the Swift sources, run `ci-local-check.sh` (with
  `CI_LOCAL_BASH32=1`), `make swift-test` and `make swift-stubcheck`
  before pushing.

What the hosted runner turned out to be (runs 36237646621–36243877514,
2026-09-26):

- `xcode-27` defaults to Xcode 27.0 (27A266a) with Swift 6.4, the iOS 27.0
  SDK and only the 27.0 simulator runtimes. Since 2026-09-26 the image also
  carries **Xcode 27.1 (27A9269)** at `/Applications/Xcode_27.1_beta.app`
  (`Xcode_27.1.app` links to it) and Xcode 27.2 beta (27B5019j) —
  `ci-toolchain.sh` prints every Xcode's version and build. The Duo code
  needs the iOS 27.1 SDK: `XBIN_XCODE: /Applications/Xcode_27.1.app` with
  `XBIN_SWIFT_CONDITIONS: XBIN_SDK_27_1` should compile it, not yet tried
  (check that job's `-showsdks` first). The Mac mini has Xcode 27.1
  (27A9269) as `Xcode-beta.app`.
- The image has no **Metal Toolchain**, and SwiftTerm's shader needs it.
  `ci-build-app.sh` fetches it (`xcodebuild -downloadComponent
  MetalToolchain`, about 840 MB, a few seconds). The build runs with
  `-IDEBuildingContinueBuildingAfterErrors=YES`, so one log lists every
  target's errors.
- Job times on `xcode-27` (runs 36256774609–36261195423, 186 PNGs per
  snapshot run): `packages` 1.5 minutes, `app` 1.5–2.5 (a cache hit with
  unchanged sources: under 1.5), `snapshots` 10–15. The `snapshots`
  artifact is about 115 MB.
- **A fresh hosted VM's first package-mode xcodebuild waits 3–7 minutes**
  before doing anything (`xcodebuild -list` or `test` in
  `Packages/XbinRenderer`: 184–406 s), in
  `waitForRemoteSourcePackagesToFinishLoading` while SwiftPM reads a child
  process, though every dependency is local; later ones start at once and
  project-mode ones never wait. `ci-snapshots.sh` starts the listing next
  to the simulator's boot and, while it outlives the boot, prints the
  process tree under it every 10 s — the next hosted run names the child.
  On the Mac mini the listing takes 2 s.
- The Mac mini (run 36261923449, its first on this branch): `packages`
  31 s, `app` 28 s, `snapshots` 3.5 minutes, one job at a time. Its PNGs are
  not byte-identical to the hosted runner's (90 of 372 differ: antialiasing
  in charts, and `folded-actions`' thread sheet scrolled differently, the
  same way in every hosted run) — compare snapshots from the same runner.
  Even there a few differ from run to run (31 of 372 between two hosted
  runs: `tile-prometheus-viewer`, `notices-empty-progress`,
  `chat-transcript`, `escape-hatches`, `chat-tools`).
- **Xcode's type checker gives up where the stub check doesn't.** One big
  initializer call full of inline closures, one of them behind `?:`, failed
  with "failed to produce diagnostic for expression" (`NativeTile.swift`'s
  `services`, 1904702). Build such arguments as typed locals first. A
  ternary between closures also loses `@Sendable` inference, which shows up
  as a Swift 6 data-race warning.
- The actions are on their Node 24 majors (`checkout@v7`,
  `upload-artifact@v7`, `cache@v6`; `ci-local-check.sh` refuses older
  ones). They need runner ≥ 2.327.1 — a self-hosted runner updates itself.

## Comparing iOS with the reference

After a green run, download the snapshots (the `hosted/` ones when present:
they show bars and materials as a device does), render the same fixtures with
the reference renderer (`shots.mjs`), and compare side by side, fixture by
fixture. Both sides name a PNG `<fixture>-<light|dark>-<default|large|ax2>.png`
(`ax2` is iOS only), so the same name is the same fixture, scheme and text
size. Differences that are intended platform conventions stay:
fonts and their metrics, control chrome (glass bar items, segmented controls
without badges, switches, menus), list insets and grouped section headers,
swipe actions for the reference's `⋯` menus, pull to refresh for its refresh
button, a floating tab bar or bottom search field over the content, a chat
anchored at the bottom. Anything else (missing content, wrong tone, broken
layout, clipped text at large Dynamic Type) gets fixed. Not bugs, the
harness: a secure field's text never shows in a capture (iOS keeps it out),
and without the app's services a `canvas` or `terminal` is a placeholder and
tile images are grey boxes (the reference's previews show the same boxes).
Then make the contact sheets, one per size:

```sh
gh run download <run-id> -n snapshots -D "$SCRATCH/snap"
ios=$SCRATCH/snap; [ -d "$ios/hosted" ] && ios=$ios/hosted      # the hosted run when there is one
PLAYWRIGHT_DIR=~/lcad-wasm node native/tools/shots.mjs --out "$SCRATCH/web"
# one row per fixture: iOS light | web light | iOS dark | web dark
for size in default large; do
  rm -f "$SCRATCH"/row-*.png
  for f in $(ls native/fixtures | grep -v README); do
    montage -label "$f · iOS light" "$ios/$f-light-$size.png" -label "$f · web light" "$SCRATCH/web/$f-light-$size.png" \
      -label "$f · iOS dark" "$ios/$f-dark-$size.png" -label "$f · web dark" "$SCRATCH/web/$f-dark-$size.png" \
      -tile 4x1 -geometry 390x844+8+8 -background '#111' -fill '#ccc' -pointsize 18 "$SCRATCH/row-$f.png"
  done
  montage "$SCRATCH"/row-*.png -tile 1x -geometry +0+12 -background '#111' -depth 8 "$SCRATCH/contact-sheet-$size.png"
done
# ax2 has no web side: an iOS-only sheet, light | dark
montage $(for f in $(ls native/fixtures | grep -v README); do echo "$ios/$f-light-ax2.png $ios/$f-dark-ax2.png"; done) \
  -tile 2x -geometry 390x844+8+8 -background '#111' -depth 8 "$SCRATCH/contact-sheet-ax2.png"
```

Look at every sheet (the Read tool shows images) before calling a
comparison done.

## Mac mini

The owner's Mac mini (24 GB, Apple silicon) is the CI machine and an Apple
toolchain reachable over ssh. `XBIN_IOS_RUNNER` has pointed ios.yml at it
since run 36261923449, and every run there has been green. **What has run
on it:** the Actions runner and every ios.yml job. **What hasn't yet:**
`mac-setup.sh` end to end (the box was set up by hand, see "What the box
turned out to be"), the ssh dev loop (`mac-remote.sh`), the UI tests
(`e2e`), and `release-build.sh`. Those are checked here only against fake
Mac tools (`ci-local-check.sh`).

### What the box turned out to be (2026-09-26)

- **The runner is a system LaunchDaemon that enters ci's session, not
  `svc.sh`'s LaunchAgent.** A user's LaunchAgent only loads with a GUI
  login, and this box has none. `dev.xbin.actions-runner` in
  `/Library/LaunchDaemons` runs `launchctl asuser <ci's uid> sudo -u ci -H
  bash -c 'cd ~ci/actions-runner && exec ./runsvc.sh'` (RunAtLoad,
  KeepAlive), which gives ci a session the way an ssh login does. The daily
  cleanup, `dev.xbin.ci-cleanup` at 04:30, runs the same way. Simulators
  boot and run in that session: an 18 s boot, and every job green. So
  neither automatic login nor a ci FileVault login is needed.
  `mac-setup.sh` still installs and checks the LaunchAgent, and its
  `--check` warns about automatic login. Folding the daemon into it is
  open work; the box used a one-off installer.
- **FileVault is on. After a reboot the admin unlocks it over ssh**
  (macOS 27's pre-boot ssh unlock works), and then the daemon starts the
  runner. The KVM is only needed when ssh is down.
- Xcode 27.0 is selected, with Xcode 27.1 (27A9269) beside it as
  `Xcode-beta.app`. Homebrew belongs to the admin.
- The users so far are the admin, ci and release. **dev does not exist
  yet**, so the ssh dev loop has nowhere to run. The fork pull request
  approval is set to *all external contributors*.
- **The runner's job hook is older than the foreign-job refusal.**
  `~ci/xbin-ci/bin/` holds the hook from the first setup: it shuts the
  simulators down, but its `mac-cleanup.sh` has no `job_allowed`, and
  `job-hook.sh` sets no `XBIN_CI_REPO`. Until both are replaced with what
  `mac-setup.sh` writes today (`hook_body`), the fork approval is the only
  gate. Rerunning `mac-setup.sh` as ci would also reinstall the
  LaunchAgents that the daemons replaced, so copy the two files by hand,
  or fold the daemons into the script first.
- An ios.yml run takes about 4.5 minutes there (the jobs run one at a
  time), against 10–15 minutes on `xcode-27`.

### The box

It lives headless in a datacenter, on an isolated VLAN: nothing reaches it
from the internet, and ssh comes in over a VPN (WireGuard or Tailscale on
the Mac or on the VLAN's router) — never a public ssh port. Around it:

- **A KVM-over-IP** on its HDMI and USB: the console for the FileVault
  unlock after a power loss, the login window, and anything ssh can't do
  (System Settings, a stuck boot). It is the way in when ssh is down, so it
  sits on the same VPN-only network.
- **An HDMI dummy plug** when the KVM isn't attached: without a display the
  login session has no real screen — a tiny default resolution for Screen
  Sharing, and simulators and UI tests that don't render as on a desk.
- **A switchable PDU** outlet: a power cycle is the last resort for a hung
  Mac, and with `autorestart` (below) the Mac boots when power returns.
- **No Apple ID signed in** — for any user. Nothing on this box syncs,
  buys or backs up to iCloud; Xcode needs no Apple ID to build (signing
  uses an App Store Connect API key, below). Install Xcode with `xcodes`
  or a downloaded `.xip` rather than the App Store for the same reason.
- **Power:** `sudo pmset -a sleep 0 autorestart 1 womp 1` (never sleep,
  boot after a power loss, wake for network access) and `sudo systemsetup
  -setrestartfreeze on` (reboot after a kernel freeze).
- **sshd, keys only**, in `/etc/ssh/sshd_config.d/100-xbin.conf` (macOS's
  `sshd_config` includes that directory first, and the first value wins):

  ```
  PasswordAuthentication no
  KbdInteractiveAuthentication no      # PAM would still ask for a password through it
  PermitRootLogin no
  AllowUsers owner ci dev release      # the four accounts below, nobody else
  # ListenAddress <the VPN address>    # optional: only the VPN interface
  ```

  with each user's `~/.ssh/authorized_keys`; test a second login before
  closing the first. Remote Login on in System Settings → General →
  Sharing ("Allow full disk access for remote users" off).
- **Firewall on, stealth mode on** (`sudo /usr/libexec/ApplicationFirewall/socketfilterfw
  --setglobalstate on --setstealthmode on`); Screen Sharing off, or on and
  allowed for the admin only (System Settings → General → Sharing → Screen
  Sharing → Only these users) — the KVM-over-IP covers the rest.

**FileVault or an unattended reboot — pick FileVault.** With FileVault on,
the disk is encrypted at rest (a stolen or decommissioned Mac leaks
nothing), but after any unplanned restart — a power loss, a panic — the Mac
stops at the unlock screen until someone types a password, and macOS turns
automatic login off. With it off, the Mac comes back by itself (automatic
login as ci starts the runner), but the disk holds the release user's App
Store Connect key and the runner's registration in the clear. This box
holds signing material, so: **FileVault on**, the unlock through the
KVM-over-IP after a power loss, and planned reboots with `sudo fdesetup
authrestart` (it asks for a FileVault user's name and password and boots
once past the unlock). Make ci a FileVault user (`sudo fdesetup add
-usertoadd ci`) and unlock as ci — FileVault then logs ci in, so the
runner's LaunchAgent starts; check that once, with the KVM watching, after
the first authrestart. (The owner's box skips this: its runner is a
LaunchDaemon and the admin unlocks over ssh, see "What the box turned out
to be".) A CI Mac that is down until someone unlocks it is
the price; the PDU's power cycle can't bring it back on its own.

### The users

Four accounts, each doing one thing:

| user | kind | does | never |
|---|---|---|---|
| the owner's (e.g. `owner`) | admin | setup, updates, Xcode, Homebrew, `sudo` | runs the runner or holds the release key |
| `ci` (`XBIN_CI_USER`) | standard | the GitHub Actions runner and its simulators | reads the release key (different uid, `~release` mode 700); the ssh dev loop |
| `dev` (`XBIN_DEV_USER`) | standard | the ssh dev loop (`XBIN_MAC=dev@…`): builds, runs, snapshots, the UI tests' e2e and its tunnel | runs CI jobs |
| `release` (`XBIN_RELEASE_USER`) | standard | owns the App Store Connect key, runs `release-build.sh` by hand | runs CI jobs or anything from a pull request |

The dev loop is not ci's because a CI job is code from any pushed branch
running as ci: it could leave a `~/.zshenv` behind that reads the e2e
account's password off the next ssh session, or reach the e2e tunnel's port, and every
job shuts ci's simulators down — the dev loop's included, were they the
same user's. Each user has its own simulators (CoreSimulator is per user).
`mac-remote.sh` refuses `e2e` and `tunnel` as the runner's user.

`sudo sysadminctl -addUser ci -fullName 'xbin CI' -password -` (and the same
for `dev` and `release`) makes a standard user; `chmod 700 /Users/release`.

### Setting it up (once, then after each Xcode update)

With Xcode 27 installed (`brew install xcodes && xcodes install 27.0`, or
a downloaded `.xip`) and opened once, first as the **admin** (sudo: Xcode's
licence and first launch, the simulator platform, Homebrew):

```sh
native/ios/scripts/mac-setup.sh --check          # what is missing; changes nothing
native/ios/scripts/mac-setup.sh --no-runner      # the system half
```

then as **ci**, with a runner registration token from GitHub → the
repository → Settings → Actions → Runners → New self-hosted runner (valid
an hour) — the system items are already ok, and ci gets its own simulator,
cleanup agent and runner — and as **dev** with `--no-runner` (its own
simulator and cleanup agent: `XBIN_MAC=dev@mini native/ios/scripts/mac-remote.sh setup --no-runner`):

```sh
native/ios/scripts/mac-setup.sh --runner-token <token>
# or from this box, over ssh (interactive: the token prompt; it ships only
# the scripts, by tar — the full mirror needs the Homebrew rsync this installs):
XBIN_MAC=ci@mini native/ios/scripts/mac-remote.sh setup
```

It is idempotent — each item is checked and only fixed when missing — and
does: Xcode (`XBIN_XCODE`, else the selected one, else the newest
`/Applications/Xcode*.app`) and `xcode-select`; the licence; the
first-launch packages; the iOS simulator runtime matching the SDK
(`xcodebuild -downloadPlatform iOS`); the Metal toolchain; Homebrew with
xcodegen, xcbeautify and rsync; the simulator `xbin-e2e` (the UI tests'
own); a LaunchAgent `dev.xbin.ci-cleanup` running `mac-cleanup.sh` daily at
04:30 (DerivedData, CI caches and the ssh loop's trees unused for 7 days,
unavailable simulators; skipped while a job runs; log in
`~/Library/Logs/xbin-ci/`); and the GitHub Actions runner in
`~/actions-runner` — the pinned release, SHA-256 checked, registered to the
**repository** with the labels `self-hosted`, `macOS`, `xbin-mini`,
installed as a launchd service, with a job hook (`~/xbin-ci/bin/job-hook.sh`)
that fails every job but this repository's `ios.yml` on a push or a manual
dispatch before any of its steps run (Security, below), and shuts the
simulators down before and after every job. It refuses to put the runner
under the release or the dev user and warns when an admin would hold it.

Then it reports **the box** — never changing it: pmset (sleep 0,
autorestart 1, womp 1), restart after a freeze, FileVault, automatic login,
the users (an admin, ci and release standard), Remote Login and sshd
(passwords off, root off, `AllowUsers`), the firewall and stealth mode,
Screen Sharing restricted, no Apple ID (for the user running it — run
`--check` as each), a display, free disk (`XBIN_MIN_FREE_GB`, default 50),
the installed Xcodes, SDKs and simulator runtimes, whether the runner
runs, and — with `gh` signed in as a repository admin — whether fork pull
requests' workflows wait for approval from all external contributors. Every warning names the command or setting that fixes it; warnings
don't fail `--check` (only missing build items do). `systemsetup` needs an
admin, so run `--check` as the admin for the whole picture. Then point the
CI at it:

```sh
gh variable set XBIN_IOS_RUNNER --body '["self-hosted","macOS","xbin-mini"]'
gh workflow run ios.yml --ref <feature-branch>    # and watch it as in §4
```

On the Mac the jobs keep DerivedData and SwiftPM clones on disk
(`~/Library/Caches/xbin-ci/`), run one at a time (one runner), and start
from a clean checkout (`actions/checkout` cleans the workspace;
`$RUNNER_TEMP` is emptied per job).

### Signing and releases

The release user signs and uploads with an **App Store Connect API key**
and Xcode's automatic signing, which uses Apple's **cloud-managed
distribution certificate**: no distribution private key is created on the
Mac, and none is ever exported or copied to it. (The archive step is signed
with the release user's development identity, which Xcode makes the first
time — a development key can't publish anything.)

**The key** — the owner (Account Holder or an Admin) makes it once:
App Store Connect → **Users and Access → Integrations** → App Store Connect
API → Team Keys (the Account Holder requests access the first time) → **+**,
a name like "xbin-mini release", role **Admin** → Generate. Download the
`.p8` (it can be downloaded only once) and note the **Key ID** and the
**Issuer ID** (above the list). Admin, because automatic signing with
`-allowProvisioningUpdates` registers the bundle ids and profiles and uses
the cloud-managed distribution certificate, which Apple lets team keys do
only with the Admin role as of this writing — if the key page offers App
Manager with certificate and cloud-signing access, take that instead
(least privilege). A team key reaches every app of the team: only the
release user reads it, and it is revoked in App Store Connect the moment
the Mac is suspect (lost, reinstalled, a user it shouldn't have).

```sh
scp AuthKey_<KEYID>.p8 release@mini:        # then delete the downloaded copy
ssh release@mini 'mkdir -p ~/.appstoreconnect/private_keys && chmod 700 ~/.appstoreconnect ~/.appstoreconnect/private_keys &&
  mv ~/AuthKey_*.p8 ~/.appstoreconnect/private_keys/ && chmod 600 ~/.appstoreconnect/private_keys/AuthKey_*.p8'
```

**Once per team: a registered device.** The archive is signed with a
development profile, and Apple issues one only to a team with at least one
device ("Your team has no devices from which to generate a provisioning
profile"): register an iPhone under Certificates, Identifiers & Profiles →
Devices (its UDID; neither Developer Mode nor leaving Lockdown Mode is
needed). The export's App Store profiles need none.

**A build**, as release, over ssh, from its own clean checkout (a tag).
Nothing to unlock:

```sh
native/ios/scripts/release-build.sh --team <TEAMID> --key-id <KEYID> --issuer <ISSUERID> --dry-run
native/ios/scripts/release-build.sh --team <TEAMID> --key-id <KEYID> --issuer <ISSUERID> [--version 1.0.0] [--upload]
```

It archives (`xcodebuild archive … -allowProvisioningUpdates
-authenticationKeyPath/-KeyID/-IssuerID`, `DEVELOPMENT_TEAM`, the build
number from the UTC time or `--build`) and exports with
`method app-store-connect` — an `.ipa` in `~/xbin-release/<build>-<commit>/`
(mode 700), or with `--upload` straight to App Store Connect for TestFlight.
It refuses to run as ci or an admin, as anyone but the release user, from
a pull_request- or fork-triggered workflow — or from any workflow without
`XBIN_RELEASE_FROM_ACTIONS=1`, reserved for a protected, manually
dispatched one that does not exist —, with a key that isn't the user's own
at mode 600 outside any checkout, or from a dirty tree; and it warns when an
Apple Distribution identity is in the keychain (it would be used instead of
the cloud certificate, and is a distribution key on disk). The team id is
on developer.apple.com → Membership details.

**The signing keychain.** Over ssh, with no desktop session, codesign can't
use a key in the login keychain: it fails with `errSecInternalComponent`
(and "unable to build chain to self-signed root") whether the keychain is
unlocked in that session or not, and with codesign in the key's partition
list. It can in a keychain made with `security create-keychain` — what CI
setups do. So release-build.sh keeps its own,
`~/Library/Keychains/xbin-signing.keychain-db`, made on its first run with a
random password in `~/.appstoreconnect/signing-keychain.pass` (mode 600,
next to the API key, which is worth more). For the build it unlocks it,
copies Apple's public WWDR intermediates in, makes it the user's only and
default keychain — xcodebuild then makes the development identity there —
lets codesign use its keys (`set-key-partition-list`, retried once after the
first run makes the identity mid-build), and puts the user's keychain list
and default back on exit. It holds only that development identity. Apple
keeps **one development certificate per Mac**: one made before (whose key is
in the login keychain) must be revoked in Certificates, Identifiers &
Profiles first — the script says so when xcodebuild asks.

### App Store: the icon, privacy, export compliance

What App Store Connect asks for besides a signed build, and where each
answer lives. `native/tools/store-check.py` (in `ci-local-check.sh`) keeps
the files in agreement with the code.

**The icon** is `App/Resources/AppIcon.icon`, an Icon Composer document (the
iOS 26+ format: Liquid Glass, with the clear and tinted looks derived by the
system). It is the mark, M3 (D183, D185; the masters are
`plans/brand/marks/`, `icon-1024.svg` the full-bleed drawing): a solid
cobalt `#1F3DFF` fill, square art the system masks, and two flat layers on
the 1024 canvas, `b.svg` (the white b, even-odd) and `x.svg` (the yellow x
`#FFD000`, in a group of its own above it), with no specular highlight and
no translucency (the brand's mark never glows). The Dark look is the
Default's, because the tile never changes between themes
(`plans/brand/marks/README.md`): the fill is given for the dark appearance
too (`fill-specializations`, cobalt in both). With a fill for the default
appearance alone, iOS derived its own Dark, a near-black tile with a cobalt
b and the yellow x loose on it, which the mark's rules forbid (D185's
review). Clear and Tinted are the system's monochrome. BrandMastersTests
holds the layers and the fills to the masters. Edit the SVGs from the
masters, or open the document in Icon Composer
(`/Applications/Xcode.app/Contents/Applications/Icon Composer.app`).
Preview every look on the Mac without a build:

```sh
T="/Applications/Xcode.app/Contents/Applications/Icon Composer.app/Contents/Executables/ictool"
for r in Default Dark ClearLight ClearDark TintedLight TintedDark; do
  "$T" AppIcon.icon --export-image --output-file $r.png --platform iOS --rendition $r \
    --width 512 --height 512 --scale 2   # Tinted*: add --tint-color 0.25 --tint-strength 0.75
done
```

The build compiles it into `Assets.car` (`ASSETCATALOG_COMPILER_APPICON_NAME:
AppIcon` in project.yml), with the marketing icon App Store Connect wants.

**The faces** (D185) are `App/Resources/Fonts/`: Bricolage Grotesque 800
(large titles, the first-run stair) and JetBrains Mono regular and bold
(terminals, code), with their OFL texts, listed in `App-Info.plist`'s
`UIAppFonts`. They are the workspace's own (`web/vendor/fonts`, pinned by
`hack/vendor.sh`), converted by `native/ios/scripts/app-fonts.sh`
(woff2_decompress and fontTools; the same bytes every run): rerun it when
vendor.sh moves a font. XbinRendererModel's `XbinFaces` names them; a face
that isn't registered falls back to the system's. The snapshot tests (both
runs) register them from the source tree beside the fixtures, so their PNGs
show the app's type.

**Privacy.** The app's code collects nothing for the developer. It talks to
the workspaces its user adds, which are their servers, and fetches the kill
switch (`https://xbin.dev/app/ios.json`) with no identifier. Push is the
exception once a relay ships: the app then registers its APNs token with the
relay the xbin project runs (spec/push.md). Three places say this and must
agree:

| | While `XbinPushRelay` (Support/App-Info.plist) is empty: push off | Once it names the project's relay |
|---|---|---|
| `App/Resources/PrivacyInfo.xcprivacy` → collected data | none | `NSPrivacyCollectedDataTypeDeviceID`, linked false, tracking false, purpose App Functionality |
| App Store Connect → App Privacy | "Data Not Collected" | Identifiers → Device ID: App Functionality, not linked to identity, not used for tracking |
| https://xbin.dev/privacy.html (`website/privacy.html`) | already describes the relay as opt-in | unchanged |

The manifest also declares the required-reason APIs the binary uses (`nm -u`
on a build: `NSUserDefaults`, `stat`, `fstat`). They are UserDefaults
`CA92.1` for the app's own settings, and file timestamp `C617.1` for SwiftTerm's
kitty-graphics `stat`/`fstat` of files in the app's temporary directory.
Neither extension uses one, so neither has a manifest; store-check.py fails
when the app's sources use UserDefaults without the declaration. The privacy
policy URL for App Store Connect is `https://xbin.dev/privacy.html` (deploy
with the website: `make website`). Its sentence that the relay does not
store IP addresses holds only if the relay's deployment (its reverse proxy
included) keeps no access logs with them.

**Plain http (App Review).** `NSAllowsArbitraryLoads` is on, alone: the
app connects to servers its user runs, at whatever address they give —
often http on a tailnet or LAN. (With `NSAllowsLocalNetworking` beside
it, iOS ignores the allow-any rule and only localhost-like names get
http.) App Review asks why: "xbin is a client for self-hosted
workspaces; users enter their own server's address, which may be plain
http on a private network or VPN. The app warns when an http address may
travel unencrypted." The sign-in page's note is
`ServerOrigin.isEncryptedInTransit` (https, loopback, Tailscale).

**Export compliance.** `ITSAppUsesNonExemptEncryption` is `false`: the app's
encryption is TLS through URLSession and Apple's CryptoKit and Secure Enclave
(push sealing, device keys). That is encryption within Apple's operating
system, which the exemption covers. No own cipher code is compiled for iOS
(`Shared/PushCrypto.swift` uses swift-crypto only on Linux, for the tests).
It is the account holder's declaration: revisit it if the app ever ships its
own cryptography.

### The help screenshots

The app's "where do I find the QR code?" help shows two pictures of the web
shell: `App/Resources/Help/help-1-settings.png` (the top bar's **settings**
chip and its menu, **add a device** ringed) and `help-2-add-device.png`
(the add-device panel with its QR code and *address your phone uses*).
They are made by the UI harness, not by hand — the `appHelp` pass
(`hack/ui-harness/passes/apphelp.js`) on a fresh seeded workspace, dev1's
view, dark theme, 2×, cropped to about 1000–1100 px wide, with the
enrollment answer stubbed to a fixed code at `https://xbin.example.com` so
nothing of a real workspace shows and the files change only when the shell
does. Rerun whenever the top bar, the settings menu or the device panel
changes, then look at both before committing:

```sh
PLAYWRIGHT_DIR=~/lcad-wasm PORT=8931 hack/ui-harness/app-help-shots.sh   # a private port, never the default
```

It runs `run.sh --keep appHelp`, stops the harness xbind and copies the two
PNGs from `$HARNESS_DIR/out/app-help-*.png` into `App/Resources/Help/`. The
pass also asserts what it shows (the chip, "add a device" first, the QR code
decoding to the example link — `zbarimg` when installed).

### The ssh dev loop

`native/ios/scripts/mac-remote.sh`, from this box, with `XBIN_MAC=user@host`
(key-based ssh; `XBIN_MAC_SSH_OPTS` for a port or key): it mirrors the
working tree to `~/xbin-remote/tree` on the Mac (tracked and untracked
files as git sees them, deletions included; the Mac's `.build` and
generated `.xcodeproj` stay), runs the same scripts CI runs there, and pulls
the results into `$XBIN_MAC_PULL` (default `${TMPDIR:-/tmp}/xbin-mac/<command>/`):

```sh
export XBIN_MAC=dev@mini.local                    # the dev user, never ci
native/ios/scripts/mac-remote.sh build            # xcodegen + build Xbin: app-build.log, .xcresult
native/ios/scripts/mac-remote.sh packages         # swift test, on macOS
native/ios/scripts/mac-remote.sh snapshots        # renderer snapshots, package + hosted: the PNGs
native/ios/scripts/mac-remote.sh run --url 'xbin://127.0.0.1:9871/c/apps/counter' --wait 10
                                                  # build (signed to run locally), boot, install,
                                                  # launch, open the link: screen.png + app.log
native/ios/scripts/mac-remote.sh shell            # a shell in the mirror
```

DerivedData stays on the Mac (`~/xbin-remote/derived`), so the second
build is incremental. `XBIN_SIM`, `XBIN_XCODE`, `XBIN_SIGNING`,
`XBIN_SWIFT_CONDITIONS`, `XBIN_SIM_GUI=1` (show the simulator on the Mac's
screen), `XBIN_SIM_ENSURE` and `XBIN_E2E_ERASE` pass through. Look at the
pulled PNGs.

### The UI tests against a real xbind

```sh
native/ios/scripts/mac-remote.sh e2e              # all of XbinUITests
native/ios/scripts/mac-remote.sh e2e --only XbinUITests/XbinE2ETests/test03NativeCounter --keep
```

This starts `e2e-xbind.sh` here — a fresh workspace with
`examples/counter-go` (its `native.js` included) as `apps/counter` and the
scripted fake ACP agent, xbind started as the UI harness starts it: `xbind
--dev --dev-overlay workspace-template --workspace <ws> --listen
127.0.0.1:9871 --external-url http://127.0.0.1:9871` with
`XBIN_AGENT_FAKE=bin/fakeacp XBIN_BIN=bin XBIN_SDK_PATH=sdk` — creates the
admin account `e2e` with a random password (with the owner token from
`<ws>/.xbin/token`, which stays on this box), deletes the `admin`/`admin`
login `--dev` seeds, and waits until the counter's backend answers. The
run's ssh connection carries a reverse tunnel (`-R
127.0.0.1:9871:127.0.0.1:9871`), so the simulator reaches the same origin,
`http://127.0.0.1:9871`; the account's name and password travel on ssh's
stdin, a line each. The password is the only way in from the Mac; without
`--isolate` (`XBIN_E2E_XBIND_ARGS="--isolate --rootfs …"`) its terminals
are shells as you on this box, and `mac-remote.sh` says so. On the Mac,
`XbinUITests` runs on `xbin-e2e` (`XBIN_SIM_ENSURE=<name>` for a simulator
of your own), erased first. `XbinE2ETests`: sign in by Log in → Enter
workspace address → the password (discovery, enrollment and device login
on every run), open the web tile `apps/welcome` from Home's search (its 13 px heading
readable at 1:1), open the native counter and tap +1 (checked on the
server and in the row), type into a terminal on `apps/welcome`, opened
from the tile's long press → New session… → the launcher's Bash (its output — an OSC
title only the shell's arithmetic makes — must reach the navigation bar;
the key row sits right on the software keyboard and its ↑ recalls a
command; back on Home the tile's row counts the shell and its long press
lists it, D128), an agent session with the fake agent (its `echo: …`
answer in the transcript), the viewports (`apps/wide`, a desktop-first
page from `scripts/testdata/e2e-tiles/`, laid out at its 1200 px and
fitted, pinch zoom; `apps/phone` keeps its own viewport) and the key row
docked at the bottom with no software keyboard (test07). A headless
simulator can't attach a hardware keyboard — XCUITest always brings the
software one up — so test07 launches the app with `-XbinNoSoftKeyboard
YES` (Debug builds: the terminal gets an empty input view).
`XbinScreensTests` (D125) seed the account's `layout` pref as the web
shell writes it (one screen, `E2E screen`, of three tiles) and clear its
`mobile-screens`, then: Home → the screen's cards → a tile, a left-edge
swipe back and a right-edge one forward (`E2E.edgeSwipe`: a press at the
edge, dragged, held a moment), a short slow one only peeks; Edit reorders
(the row's reorder handle dragged), makes a card wide and hides one, Done
writes the pref and a relaunch shows it so; + Create tile → the build
chooser → the fake agent answers its prompt in the new tile; a chrome tile
(`tiles/organisations`) in the app's web view through the web ticket's
"Continue as" page; the terminal's keyboard follows a drag down
(`keyboardDismissMode`); the switcher's "Used recently"; and `test09Gallery`
shoots Home, All tiles (with a seeded personal folder), a screen, edit
mode, the create sheet and the chooser, light and dark
(`gallery-<light|dark>-NN-<screen>.png`). The tests find panels by
the bar's identifiers (`panel-back`, `panel-home`), screens by
`screen:<id>`, cards by `card:<path>`, All tiles' folders by
`folder:<context>/<id>`, a tile's row by its path (in the row's
accessibility label: the row shows the name only); a panel not in front is hidden from
accessibility, its web view and terminal included.
`XbinSessionsTests` (D132): a tile's sessions screen — the launcher from
the long press, a Bash tab, `+`, an agent tab beside it, switching and a
tab's End session; then Tools → Live reload & deployments on `apps/wide`
(a static tile: the e2e xbind has no `--isolate`) with a `dev` deployment
the test adds, and — dev assigned a branch the work tree isn't on, live
reload paused — the "Keep dev on <branch> this time" offer confirmed from
its dry run; the test puts the tile back in the zero state; then on
`apps/counter` Tools → Code opens `xbin.json`, Logs shows the backend's
log, and PRs opens a proposal the test files through the API and rejects
it through its confirmation.
`XbinPartitionsTests` (D181) use the workspace's `apps/parted`, a static
tile declaring `"partition": ["user"]` (`scripts/testdata/e2e-tiles/`):
its row's accessibility value carries the partitioned marker's words (and
`apps/welcome`'s doesn't), Settings → Your partitions and the push link
`xbin://<host:port>/xbin/partitions` each open xbind's partitions page in
the app's web view through the web ticket's "Continue as" page, with no
password form and no page-back onto the spent ticket. Every test launches with `-XbinUITesting
YES` (Debug builds): UIKit animations are off and the terminal's cursor
doesn't blink, so the app idles between steps — XCUITest otherwise waited
60 s for "animations complete" after each step with a terminal on screen
(test04 took 1129 s; 157 s now) and after each long press or menu. A test that signs
in on a fresh start can forget what it added: `E2E.forgetFreshWorkspaces`
(a Debug fresh start forgets the previous one's workspaces as Remove does
— test09's two same-server workspaces, left behind, stalled the next
test's native widget).
`XbinOnboardingTests` launch the app as a fresh install (Debug builds'
`-XbinFreshStart YES`: no workspace, the saved list untouched): the
Welcome's levels and the help, Install xbin, address → methods →
password, joining with an invite the test makes (spent on the server
afterwards), an unreachable address ("Can't connect"), a code the
workspace never minted ("Code refused"), "Sign in again" replacing its
workspace after the device is removed, and `test10Gallery`: every
onboarding screen in light and dark (`-XbinAppearance`), as
`gallery-<light|dark>-NN-<screen>.png`. The screenshots land in
`$XBIN_MAC_PULL/e2e/e2e/` (and in `uitests.xcresult`): **look at them**.
XCUITest types with key presses: the software keyboard hides while
`typeText` types and slides back up a moment later, so a control that rides
on the keyboard (the composer's Send) is found at one place and tapped when
the keyboard is back there — the tap presses a key. Tap such a control
with `E2E.tapAfterTyping` (Connect, the agent's Send; the terminal's key
row waits in `keyRowAboveKeyboard`). Where a tap landed is in the
simulator's log (`xcrun simctl spawn <udid> log show`): testmanagerd's
"Synthesizing event … Touch down at x, y".
`--keep` leaves the xbind up; `--port` moves it; an xbind the Mac reaches
by itself: `XBIN_E2E_URL=… XBIN_E2E_USER=… XBIN_E2E_PASSWORD=…
mac-remote.sh e2e` (no tunnel; an admin account, the tests make invites).
`e2e-xbind.sh smoke` checks here, over HTTP and `/ws/term`, everything
the tests need of the server; `e2e-xbind.sh invite` makes an invited
account and prints its link. `mac-remote.sh tunnel` holds only the
tunnel, for running the tests from Xcode on the Mac
(`TEST_RUNNER_XBIN_E2E_URL`/`_USER`/`_PASSWORD` in the environment of
`xcodebuild test -scheme XbinUITests`).

Sharing the Mac (learned 2026-09-26, several agents at once):

- **A simulator, a port and a mirror of your own**: `XBIN_SIM_ENSURE=
  xbin-e2e-<you>` (made if missing; the run erases it, never anyone
  else's), `--port` other than 9871, `XBIN_MAC_DIR=xbin-remote-<you>`,
  `XBIN_MAC_PULL` and `XBIN_E2E_DIR` of your own (the latter defaults to
  `/tmp/xbin-e2e`, whose `stop` kills whichever xbind is recorded there).
  `XBIN_E2E_ERASE=0` keeps the simulator's app and workspace between runs.
- **After a failed run, reboot the simulator** (`xcrun simctl shutdown
  <udid>; xcrun simctl boot <udid>`): the next run otherwise often dies
  with "Timed out waiting for AX loaded notification", and xcodebuild then
  waits up to 600 s on `simctl diagnose` (kill that one — it's yours by
  its `--udid`).
- **"Critical process Xbin crashed in (null)" may be someone else's
  crash.** xcodebuild watches crash reports by process *name* for the
  whole Mac user (the session log: "Registering/updating daemon-based
  crash report observer for process names (Xbin)"), so an Xbin crashing
  in another agent's simulator — or the owner's — fails whatever test
  runs in yours, while your app lives on (it ends with the run's
  SIGTERM). Before chasing it, find the pid: `xcrun xcresulttool export
  diagnostics --path uitests.xcresult --output-path <dir>` and grep its
  `Session-*.log` for "Process crashed with pid" (here, without
  xcresulttool: `zstd -dc uitests.xcresult/Data/* | grep -a "Process
  crashed"`). Not your app's pid: not your bug — run again. (2026-09-27:
  Xbins with pids 4638, 7163 and 7305, none of them the run's own app,
  failed test04 and test05 this way.)
- **A link into the running app**: `XCUIApplication.open(url)` relaunches
  the app (a cold start with the link); `XCUIDevice.shared.system.open(url)`
  hands it to the running app as a notification's tap does
  (XbinNavigationTests `openInRunningApp`). The app's Debug builds log
  navigation as `xbin-nav …` (NSLog): `xcrun simctl spawn <udid> log show
  --last 20m --style compact --predicate 'eventMessage CONTAINS "xbin-nav"'`.
- **`--keep` and a second run**: point the second at the kept xbind with
  `XBIN_E2E_URL`/`_USER`/`_PASSWORD` from `$XBIN_E2E_DIR/env` and hold the
  tunnel yourself. Under an ssh ControlMaster `mac-remote.sh tunnel`
  returns at once — the forward lives on the master connection; drop it
  with `ssh -O cancel -R 127.0.0.1:P:127.0.0.1:P <mac>`.

### Footage (the promo video, the website)

```sh
PORT=9400 DEMO_DIR=/tmp/me/set hack/demo/up.sh --isolate     # the film set (Larkspan), sandboxed
XBIN_MAC=dev@mini XBIN_E2E_SET=/tmp/me/set/ws FOOTAGE_AGENT=claude \
  native/ios/scripts/footage.sh --out ~/buxon/.film-media/ios   # [--appearance dark] [--name N]
XBIN_MAC=dev@mini native/ios/scripts/footage.sh clean   # its Mac tree (xbin-footage/) and simulator, gone
```

Footage is of the demo film set (`hack/demo/README.md`): `XBIN_E2E_SET`
names its workspace, and footage.sh signs in as the set's phone persona
(Maya Okafor) with the set's random password — and refuses to tunnel a set
that runs without `--isolate` (its terminals would be shells on this box).
It refuses the walk's default agent, the scripted test one (`fake`), unless
`FOOTAGE_ALLOW_FAKE=1` (a test take, never footage). The Mac's results land
in `…/footage/raw/` (replaced by each run, never by a failed one) and the
named take in `--out` (default `…/footage/takes/`).

`simctl io <udid> recordVideo` records a simulator while a walk through the
app runs with UIKit's animations on: Home → a screen → the native counter
(+1 twice) → a terminal (`ls`) → an agent (a prompt and its answer), a beat
on each. Without a set it starts the xbind here like `e2e` (`e2e-xbind.sh`,
port 9871, its own `XBIN_E2E_DIR`) and tunnels it, dressed as the film
set's company (`hack/demo/company.json`: its branding and mark, the phone
persona's name, the calendar example with a day of meetings, a prompt and
terminal title that name the person and the tile, not this box; the UI
tests' fixture tiles gone); it builds in its own
`XBIN_MAC_DIR=xbin-footage`, and films the simulator `xbin-e2e-footage`
(erased, US English, status bar at 9:41). Out come the clip, a 60 fps copy,
a cue sheet (each step's second in the clip) and a full-size still at each
beat. A run takes about four and a half minutes once the Mac has built the
app (the xbind and its backends, the simulator's erase and two boots, the
sign-in, the walk); the first build adds about three. The script's header
lists the knobs. The walk itself is `UITests/XbinFootageTests.swift`, which
is not committed: footage.sh needs a copy in the tree, and it skips without
`FOOTAGE=1`.

What the first takes taught (2026-10-02, Xcode 27.0, iOS 27.0):

- **Recording needs no screen.** It ran from an ssh session (`launchctl
  managername`: Background); no Simulator or DeviceHub window. (The dev
  user also had a console login then, the owner's; the CI user's runner
  boots simulators with none.) H.264 at the display's pixels — 1206×2622 on
  an iPhone 18 Pro — and a frame only when the screen changes: about 22
  fps on average over a take, 60 in an animation, none while it stands
  still.
- **"Recording started" on stderr is the first frame; SIGINT ends it**, and
  simctl finalizes the file before it exits. A background job of a script
  ignores SIGINT: footage.sh resets it before simctl starts.
- **The file trips ffmpeg**: large composition offsets (and a duplicated
  timestamp) make it fall back to decode times, and frames land seconds out
  of place. `-fflags +igndts` keeps the real ones; the 60 fps copy is made
  so, for editors that want a constant rate.
- **A simulator takes the Mac's region**: here en_PL, a 24-hour "09:41" and
  a Polish keyboard beside the English one. footage.sh sets en_US and boots
  again.
- **XCUITest is the slow part.** Without `-XbinUITesting` every step waits
  for animations to end, which a terminal's blinking cursor never does (60 s
  a step); every query snapshots the app (about a second here). The walk
  replaces XCUIApplicationProcess's private quiescence wait, taps at points
  read from one snapshot once an element's frame holds still (a panel still
  sliding moves it — a tap at its first frame opened the next card over),
  and types on the software keyboard's keys in one synthesized event
  (XCPointerEventPath, private too) — `typeText` slides the keyboard away
  and back, and a tap per key took a second. The clip runs 65–90 s, with
  a few still stretches an editor cuts (the agent's session starting).
- **The app holds the tunnel.** ssh doesn't end while a forwarded
  connection is open, and the app's sockets are: the walk quits it, and
  footage.sh shuts the simulator down after.
- **The Home Screen shows the test runner's icon** (`XbinUITests-Runner`),
  so the clip starts in the app, not on the springboard.

What read as a test fixture in the first takes (2026-10-02: the e2e xbind,
dressed as another company, and the fake agent): the launcher's "Fake agent
(tests)" box, the tab "fake: …", the author "fakeacp" and its scripted
answer, a counter at 26. Those takes are gone; footage.sh now films the
film set and refuses the fake agent. A real provider is what an agent step
needs (`FOOTAGE_AGENT=claude` once the xbind can run its ACP adapter and has
its key; footage.sh's header). The walk (`XbinFootageTests.swift`) still
taps a native counter's +1 — the set's tiles have none, so `FOOTAGE_TILE`
names one of the set's (`apps/onboarding`, `apps/crm`) and the walk needs a
step for it before the next take. And the messages' times are the Mac's
clock, not 9:41.

What the camera caught in the app: **a panel push blanks the panel it
leaves at once** — three or so frames of plain background, then the new one
slides in over it, where the old one should slide aside under it (it looks
as if PanelStack's delayed `opacity` change takes effect at once). The UI
tests never show it: their animations are off.

### Security

- **The runner is repository-scoped** (registered with the repository's
  URL), never an organization's, and runs as an ordinary user on a machine
  that does nothing else.
- **A fork's pull request never runs on the Mac — the Mac enforces it.**
  `ios.yml` runs on pushes to this repository's branches and by hand only,
  but that alone keeps nothing out: the runner serves every workflow of the
  repository, and a pull request's workflows run from its own merge commit
  — it can add a file naming the runner, or edit away the checks in this
  repository that would object. So the runner's job hook
  (`mac-cleanup.sh --job-hook`, the Mac's own copy, installed by
  `mac-setup.sh`) runs before any step and fails every job that isn't this
  repository's `.github/workflows/ios.yml` on a push to a branch or a
  manual dispatch: a `pull_request*` event, a pull request's head, a fork's
  event, another workflow file. Keep GitHub's approval for fork workflows
  on *all external contributors* too (`mac-setup.sh --check` reads it with
  `gh`): the approval is the gate, the hook the lock. `ci-local-check.sh`'s
  checks (no `pull_request*` in `ios.yml`, no `ci.yml` job on a
  self-hosted runner) catch our own mistakes only.
- **No secrets on the runner.** The workflows use none (`permissions:
  contents: read`, no signing, no provisioning, no TestFlight); the runner's
  own credentials are its registration, nothing else.
- **The e2e xbind is the dev user's.** A throwaway workspace on the Linux
  box whose only way in from the Mac is the `e2e` admin account's random
  password (the seeded dev login is deleted; the owner token stays on the
  Linux box); the tunnel binds the Mac's loopback, which every local user
  can reach, and an xbind without `--isolate` hands whoever signs in a
  shell as you on the Linux box. So the password and the tunnel go only to
  the dev user's ssh session — never ci's, whose jobs are code from any
  pushed branch (`mac-remote.sh` refuses `e2e` and `tunnel` as ci).
- **The release key is another user's.** It lives in the release user's
  home (mode 700, the `.p8` 600), which the runner's user ci — standard,
  no sudo — can't read; `release-build.sh` refuses to run as ci, as an
  admin, or from a pull request's or fork's workflow. FileVault keeps it
  encrypted at rest.
- **Reachable only over the VPN**, keys only, `AllowUsers` the four
  accounts; the KVM-over-IP and the PDU sit on the same isolated network.
- **Clean workspaces.** Every job checks out clean, `$RUNNER_TEMP` is
  emptied per job, the job hook shuts the simulators down, the UI tests
  erase their simulator first; caches (`~/Library/Caches/xbin-ci`) hold only
  build products keyed by their inputs, and the daily cleanup drops what
  goes unused.

## Rules

- **Push only to feature branches.** No signing, no secrets, no TestFlight,
  no provisioning — not even "just to try". `release-build.sh` is the
  owner's, run as the Mac's release user; an agent never runs it or
  touches its key.
- **Never commit the `.xcodeproj`**; `project.yml` is the source.
- **The fixtures are the contract.** A vocabulary change updates, in one
  change: the fixture(s), `expected.json`, the runtime, the reference renderer,
  the SwiftUI renderer, and the design (plans/native.md now; docs/native.md once
  the runtime ships — docs describe what exists).
- **Additive only** for anything a shipped app or a shipped tile depends on:
  the vocabulary, the tree format, `xb-native.js`, `xbin.native`, the bridge
  (docs/compat.md).
- **A vocabulary change, step by step** (rev 2, D189, is the worked
  example): in `web/xb/vocab.js` raise the primitive's `rev` and mark the
  addition — `since` on a prop or event, `enumSince` on a new enum value,
  the child rule's `since` for a newly allowed child; `node hack/xbn/vocab-json.mjs
  > native/spec/vocab.json`; the runtime's checks (`rt-build.js`) and a
  `hack/xb-native.test.mjs` case for a rev-1 app; the reference renderer and
  a `hack/xb-render.test.mjs` case; the model in XbinRendererModel (tested
  on Linux, `VocabularyTests` holds the revision table and the reports) and
  the SwiftUI view (stubcheck; new SDK APIs get stubs in
  `tools/swiftui-stubcheck/Stubs`); a fixture whose final tree exercises
  every new prop, event, enum value and child (`make native-check` counts
  them), plus a `#Preview`; `node hack/native-docs.mjs --write`, prose in
  docs/native.md, a changelog entry, plans/native.md. An older app is a
  fixture too: `caps` with the old revisions and `expectUnsupported` listing
  exactly what it must refuse (`old-app`). Never re-mean a rev-1 prop: a
  rev-1 tree on a newer app must look as before (a `split` without `detail`
  still stacks).
- **No device APIs for tile code**, ever (plans/native.md §2).
- **Report honestly** what was verified where: "passes `swift test` on Linux",
  "compiles on CI", "snapshot looked right", "not run on a device".
- **Shared checkout:** other agents often work in `~/buxon`. Use a worktree
  (`git worktree add ~/buxon-native -b <branch> master`) and review
  `git status`/diffs before committing.
