# XbinRenderer

The SwiftUI renderer of native tiles (plans/native.md §8, §10, §15): one view
per primitive of the vocabulary (`web/xb/vocab.js` → `native/spec/vocab.json`),
the chat family as public components the app's ACP agent screen reuses, a
`#Preview` per fixture and snapshot tests of every fixture.

```
Sources/XbinRendererModel/   Foundation + Observation + XbinCore — builds and tests on Linux
  TreeModel.swift            XbinTreeModel / XbinNode: the store as observable nodes; emit()
  Vocabulary.swift           primitives + revisions (caps), event reports, token sets
  Tokens.swift, Icons.swift  tone/type/gap/height tokens, the palette, icon → SF Symbol
  Props.swift, Values.swift  lenient prop reads, picker options, data: URLs
  Markdown.swift             tokens → blocks/inlines → styled runs
  Chart.swift                chart data + the reference renderer's axis labels
  QuestionForm.swift         a flat JSON Schema → form fields → the answer
  ChatModels.swift, Diff.swift  the chat family's plain view models
  ScreenLayout.swift, Layout.swift  screen/list/sheet/drawer/tabs/split/toolbar splitting, drawer
                             metrics, flow layout, nav path, date values
  TextInput.swift            TextInputGate: controlled text under IME composition (tree.md §6)
  TextBreaks.swift           long identifiers wrap at characters, not hyphenated
  Preview.swift              Quick Look file names from an image's bytes
  Fixtures.swift             native/fixtures → TreeStore (previews, snapshots, tests)
Sources/XbinRenderer/        SwiftUI + UIKit (iOS 26), every file under #if canImport(UIKit)
  XbinTreeView.swift         the entry point and the per-primitive dispatch
  Environment.swift          XbinServices (what the app provides), options, context, confirm host
  Screen.swift, Navigation.swift, Structure.swift, Rows.swift   (drawers: Navigation.swift DrawerLayer)
  Content.swift, MarkdownView.swift, ChartView.swift, Controls.swift, Field.swift
  TextInput.swift            the IME-aware UITextField/UITextView wrappers (field, composer)
  Images.swift               XbinImages (per-tree loading + cache), Quick Look, message thumbnails
  Chat/                      TranscriptView, MessageView, ThinkingView, ToolCardView, ApprovalView,
                             QuestionView, PlanView, DiffView, ActivityView, StepView, ComposerView
                             (public) and their tree adapters (ChatNodes.swift)
  Previews.swift             XbinFixturePreview + a #Preview per fixture
Tests/XbinRendererModelTests/  Linux: the model against vocab.json, the fixtures and the reference renderer
Tests/XbinRendererTests/       Apple CI: SnapshotTests (PNG per fixture × light/dark × default/large/ax2)
```

## Using it

```swift
import XbinRenderer                       // re-exports XbinCore and XbinRendererModel

let store = TreeStore(savedState: saved)
// the runtime's WKScriptMessageHandler "xbn": store.receive(body: message.body)
let caps = XbinVocabulary.caps(app: "1.0 (42)")   // inject with RuntimeScript.documentStart(caps:state:)
XbinTreeView(store: store, send: { call in runtime.evaluate(call.functionBody) }, services: XbinServices(
    openLink: { url in … },                         // only with cap:open-links
    imageData: { src in try await tileFetch(src) }, // tile-relative, with the frame token
    terminal: { req in AnyView(TileTerminal(req)) },
    canvas: { req in AnyView(CanvasIsland(req)) },
    attach: { req in await pickAndUpload(req) }))
```

`send` receives every user action as a `RuntimeCall` (`xbn.event(k, type,
payload, n)`); the view shows a controlled prop's new value at once and the
tile's value replaces it only when the tile sets it (native/spec/tree.md §6 —
`TreeDelta.restated` makes a reset or a refusal apply even when the tree
already held the value).

## Testing

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"
swift build && swift test            # Linux: the model (the SwiftUI target is empty here)
```

On Linux the SwiftUI sources are parsed (inactive `#if` blocks must still
parse) but not type-checked by `swift build`; `SourceCoverageTests` also
checks that every fixture has a `#Preview` and every primitive a dispatch
branch. `native/tools/swiftui-stubcheck/run.sh` (and `--sendable-bindings`)
type-checks the views and the snapshot tests against stubs of the SDK — run
it after every change to the views; it catches our own mistakes, not SDK
drift. The views are
compiled, and the snapshots written, only by the Apple CI
(`native/ios/scripts/ci-snapshots.sh`: `xcodebuild test` on a simulator with
`TEST_RUNNER_SNAPSHOT_DIR`, seen by the tests as `SNAPSHOT_DIR`). PNGs are
`<fixture>-<light|dark>-<default|large|ax2>.png`; nothing is compared.

Images: `XbinTreeView` puts an `XbinImages` in the environment
(`\.xbinImages`) built from `services.imageData`; `image` elements and
message thumbnails load through it (`data:` sources are decoded without
it). The app's own screens can set one to get thumbnails in
`MessageView`. The iPhone Duo's `ArrangementView` (split) compiles only
with `-DXBIN_SDK_27_1`, off until the 27.1 SDK confirms its names.
