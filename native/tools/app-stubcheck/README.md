# app-stubcheck — type-check the app's Model and Shell on Linux

The app target (`native/ios/App`) is SwiftUI, UIKit and WebKit and compiles
only on the Apple CI; `native/tools/app-check` runs its UIKit-free files, and
`native/tools/swiftui-stubcheck` type-checks the renderer's views. This tool
covers the app's **Model** (`App/Model`: workspaces, windows, the events
socket, the kill switch, Handoff, haptics) and **Shell** (`App/Shell`: the
root view, switcher, navigator, inbox, settings, add-a-device, Safari) in
Swift 6 mode against:

- the SwiftUI stubs of `swiftui-stubcheck/Stubs/SwiftUI`, plus
  `Stubs/SwiftUI/AppShell.swift` here (windows and scenes, `@Bindable`,
  `@SceneStorage`, `openWindow`, `userActivity`, `onDrag`, representables…);
- `Stubs/UIKit` (a fuller UIKit than the renderer's, with `NSUserActivity`
  and `NSItemProvider`, which Linux's Foundation lacks), `Stubs/WebKit`,
  `Stubs/SafariServices`, `Stubs/CoreImage`;
- `Stubs/App/AppStubs.swift`: the app types these folders use from
  `Tiles/`, `Terminal/`, `Agent/` and `Push/`, and the three files it stands
  in for (`Model/AppTransport.swift`, `Model/DeviceKeys.swift`,
  `Shell/AddWorkspaceView.swift`);
- the real XbinCore, XbinTerm, XbinAgent and XbinRendererModel.

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"
native/tools/app-stubcheck/run.sh                        # the stubs as written
native/tools/app-stubcheck/run.sh --sendable-bindings    # Binding closures @Sendable
```

Every `Model/*.swift` and `Shell/*.swift` is compiled (a new file is
included on its own), each with `import UIKit` first (the stub re-exports
FoundationNetworking) and `#selector(x)` rewritten to a stub `Selector("x")`
(no Objective-C runtime on Linux).

What a pass means is what it means for swiftui-stubcheck: our code agrees
with these stubs — model APIs, optionals, closure types, actor isolation —
not that it compiles for iOS; the stubs are declarations from memory of the
SDK, permissive where unsure. When the Apple CI disagrees, fix the code and
then the stub. A call the stubs lack fails with "no member"/"cannot find":
add the SDK's real declaration (labels, generics, isolation) to the stubs
here, not a looser one.
