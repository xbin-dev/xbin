# app-stubcheck — type-check the app's SwiftUI/UIKit files on Linux

The app target (`native/ios/App`) is SwiftUI, UIKit and WebKit and compiles
only on the Apple CI; `native/tools/app-check` runs its UIKit-free files,
`native/tools/swiftui-stubcheck` type-checks the renderer's views and
`native/tools/term-stubcheck` the terminal (`App/Terminal`). This tool
covers, in Swift 6 mode:

- the app's **Model** (`App/Model`: workspaces, windows, the events socket,
  the kill switch, Handoff, haptics) and **Shell** (`App/Shell`: the root
  view, switcher, navigator, inbox, settings, add-a-device, Safari) — every
  file but `Model/AppTransport.swift`, `Model/DeviceKeys.swift` and
  `Shell/AddWorkspaceView.swift` (URLSession delegates, the Secure Enclave,
  VisionKit/AuthenticationServices);
- `FILES` in `run.sh`: a native tile's escape hatches (`Tiles/TileAttach`,
  `TileTerminal`, `TileCanvas`, `TileHatches`) and the Agent tab
  (`Agent/*`).

They are compiled together, so the seams between them (a window's
`WorkspaceNav`, the events socket's hooks, the Agent screen's model) are
checked against the real declarations, against layered stubs:

1. `swiftui-stubcheck/Stubs` — the renderer's SwiftUI and UIKit (prepared by
   `swiftui-stubcheck/run.sh --sources-only`, which also yields the
   renderer's views as `XbinRendererCheck`);
2. `term-stubcheck/Stubs` — the terminal's UIKit and SwiftUI additions and
   SwiftTerm's API;
3. `Stubs/` here — the SwiftUI (`@SceneStorage`, `openWindow`,
   `userActivity`, `onDrag`, view-controller representables, …) and UIKit
   (`NSUserActivity` and `NSItemProvider`, which Linux's Foundation lacks,
   haptics, the image picker) the app adds, and `WebKit`,
   `SafariServices`, `CoreImage`, `PhotosUI`, `UniformTypeIdentifiers`;
4. `Stubs/App/AppStubs.swift` — stand-ins for the app types of the files
   not compiled here (the rest of `Tiles/`, `Terminal/`, `Push/`,
   `Shared/`, the three files above);
5. the real XbinCore, XbinTerm, XbinAgent and XbinRendererModel.

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"
native/tools/app-stubcheck/run.sh                        # the stubs as written
native/tools/app-stubcheck/run.sh --sendable-bindings    # Binding closures @Sendable
```

Each app file gets `import UIKit` first (the stub re-exports
FoundationNetworking), `#selector(x)` rewritten to a stub `Selector("x")`
(no Objective-C runtime on Linux) and `import XbinRenderer` read as
`XbinRendererCheck` + `XbinRendererModel`. A new Model/Shell file is
included on its own; add a hatch or Agent file to `FILES`.

`--sendable-bindings` is the strictest reading of the SDK. Model and Shell
pass it; the Agent screen's and the canvas island's `Binding(get:set:)`
closures read main-actor state, as `Tiles/TileScreens.swift` has always
done and the Apple CI builds, so that variant reports them.

What a pass means is what it means for swiftui-stubcheck: our code agrees
with these stubs — model APIs, optionals, closure types, actor isolation —
not that it compiles for iOS; the stubs are declarations from memory of the
SDK, permissive where unsure. When the Apple CI disagrees, fix the code and
then the stub. A call the stubs lack fails with "no member"/"cannot find":
add the SDK's real declaration (labels, generics, isolation) to the layer
it belongs to — the renderer's, the terminal's or this tool's — not a
looser one, and never the same declaration in two layers.
