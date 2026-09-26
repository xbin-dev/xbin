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
- **Each CI round trip takes minutes.** Batch changes; never push to "see if it
  compiles" what could have been checked locally.

## Layout (as it lands)

```
native/
  AGENTS.md, README.md
  fixtures/<name>/          the contract every renderer is tested against
    native.js               tile code under test
    data.json               scripted xbin stub: responses, SSE frames, bus events, now/locale/tz
    expected.json           the rendered tree {"v":1,"root":…}
  ios/
    project.yml             XcodeGen spec — the .xcodeproj is generated, never committed
    Packages/XbinCore/      SwiftPM, Foundation only — builds and tests on Linux
    Packages/XbinRenderer/  SwiftUI, one view per primitive + #Preview per fixture (CI only)
    App/                    the thin app target (shell screens)
    Tests/                  snapshot tests (ImageRenderer → PNG)
  tools/                    fixture runner, screenshot + contact-sheet scripts
web/xb-native.js            the runtime's template layer, served at /vendor/ (frozen once shipped)
web/xb/                     the Lit reference renderer (previews and tests only)
relay/                      the push relay (Go, stdlib only)
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
- Screenshots of the reference renderer: headless chromium through Playwright
  from `~/lcad-wasm` (`PLAYWRIGHT_DIR=~/lcad-wasm`, as the UI harness does),
  viewport **390×844**, `colorScheme` light **and** dark. **Look at the
  PNGs** (the Read tool shows images) and fix what's wrong before moving on.

### 3. Swift 6 on Linux — XbinCore (tens of seconds)

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"   # swiftly-installed Swift 6.4
cd native/ios/Packages/XbinCore
swift build && swift test
```

XbinCore holds everything that doesn't draw: the JSON value type, tree
decoding, patch application, the protocol state machines (the runtime bridge,
`/ws/term` framing, ACP agent events, device-login messages), the predictive
echo engine (ported from `web/term-predict.js`, checked against
`hack/term-predict.test.mjs`'s cases), and the fixture codec. Rules:

- **Foundation only.** No SwiftUI, UIKit, Combine, OSLog — and no
  `#if canImport(SwiftUI)` escape hatches. Transports are injected (a protocol
  for "send/receive frames"), so Core never needs URLSession (which lives in
  FoundationNetworking on Linux).
- JSON booleans stay distinct from numbers (don't round-trip through
  `NSNumber`); key order doesn't matter, key presence does.
- Anything testable here is tested here — CI time is for what only Xcode can do.

The renderer follows the same split: `Packages/XbinRenderer` keeps
everything that doesn't draw in its `XbinRendererModel` target (the tree as
observable nodes, controlled props, the vocabulary tables, markdown/chart/
question/chat view models — `swift test` there, held to `vocab.json`, the
fixtures and the Lit renderer's output); its SwiftUI target is empty off
Apple platforms. After changing a view, run
`native/tools/swiftui-stubcheck/run.sh` (and `--sendable-bindings`): it
type-checks the views against stubs of the SDK — our mistakes, not SDK drift.

### 4. Apple — only through GitHub Actions (minutes)

The workflow (`.github/workflows/ios.yml`, `runs-on: xcode-27`): `brew install
xcodegen` → `xcodegen generate` → build → test (XbinCore, XbinRenderer
snapshots of every fixture — light/dark × default and one large Dynamic Type
size) → upload the snapshot PNGs and the `.xcresult` as artifacts. It triggers
only on `native/ios/**` and `native/fixtures/**`.

```sh
git push -u origin <feature-branch>                     # never master
gh run list --workflow ios.yml --branch <feature-branch> -L 1
gh run watch <run-id> --exit-status
gh run view <run-id> --log-failed                       # on failure: read, fix, batch, push once
gh run download <run-id> -D "$SCRATCH/ios-<run-id>"     # snapshots + .xcresult
```

- **Check the SDK before using new APIs.** Log `xcodebuild -showsdks` and
  `xcrun simctl list devicetypes` in the workflow. The iPhone Duo APIs
  (`ArrangementView`, reserved regions, hinge) come with the **iOS 27.1** SDK;
  gate their use with `#available(iOS 27.1, *)` and confirm the names against
  the SDK the runner actually has — the design cites them from secondary
  sources.
- **Batch.** One push should carry every fix you can make from one failure log.

## Comparing iOS with the reference

After a green run, download the snapshots, render the same fixtures with the
reference renderer, and compare side by side. Differences that are intended
platform conventions (fonts, control chrome, list insets) stay; anything else
(missing content, wrong tone, broken layout, clipped text at large Dynamic
Type) gets fixed. Then make the contact sheet:

```sh
# one row per fixture: iOS light | web light | iOS dark | web dark
for f in $(ls native/fixtures); do
  montage -label '%t' ios/$f-light.png web/$f-light.png ios/$f-dark.png web/$f-dark.png \
    -tile 4x1 -geometry 390x844+8+8 -background '#111' -fill '#ccc' "$SCRATCH/row-$f.png"
done
montage "$SCRATCH"/row-*.png -tile 1x -geometry +0+12 -background '#111' contact-sheet.png
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
