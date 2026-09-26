# term-stubcheck — type-check the app's terminal on Linux

`native/ios/App/Terminal` is UIKit and SwiftUI glue around SwiftTerm and
XbinTerm; without this it meets a compiler only on the Apple CI. This tool
compiles those files (all but `WebSocketTransport.swift`, whose URLSession
WebSocket API Linux lacks) against stubs, with the real XbinCore and XbinTerm,
in Swift 6 mode:

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"
native/tools/term-stubcheck/run.sh                      # the stubs as written
native/tools/term-stubcheck/run.sh --sdk-27-1           # + the XBIN_SDK_27_1 code (Duo fold)
native/tools/term-stubcheck/run.sh --sendable-bindings  # Binding(get:set:) closures @Sendable
```

The stubs are the renderer stubcheck's (`../swiftui-stubcheck/Stubs`) plus
`Stubs/` here: more UIKit (UIView with `init(frame:)`, gesture recognizers,
`UIButton.Configuration`, `UITextLoupeSession`…), more SwiftUI
(`GeometryReader`, `KeyPress`, `@Bindable`, `LazyVGrid`, the iOS 27.1
`reservedRegions` as reported), SwiftTerm 1.20's API as the app uses it (from
its v1.20.0 sources) and the few App/Model types the terminal calls. There is
no Objective-C runtime on Linux, so `@objc` is dropped and `#selector(x)`
becomes a stub `Selector("x")` in the copies that get compiled.

What a pass means: our code is consistent with these stubs — our own model
APIs, optionals, closure types, actor isolation. It does **not** mean it
compiles for iOS; when the Apple CI disagrees, fix the code, then the stub.
The UIKit-free terminal files (`SwiftTermScreen.swift`, `SwiftTermLines.swift`)
are also compiled against the real SwiftTerm, and run, by `../app-check`.

`--sendable-bindings` reports the link alert's `Binding(get:set:)` in
`TerminalScreen.swift`, which the real SDK accepts (the app builds on CI);
new terminal code binds through `@Bindable` instead.

If `../swiftui-stubcheck/Stubs` gains a declaration that `Stubs/` here also
has, delete it here.
