# swiftui-stubcheck — type-check the SwiftUI renderer on Linux

There is no Apple toolchain here (native/AGENTS.md); SwiftUI code otherwise
meets a compiler only on the Apple CI, minutes per round trip. This tool
compiles `native/ios/Packages/XbinRenderer`'s views (and its snapshot tests,
twice: as the package runs them and with `XBIN_SNAPSHOT_HOST`, as the hosted
XbinSnapshotTests target does) against **hand-written stubs** of the SwiftUI, UIKit and Charts API they use
(`Stubs/`), with the real XbinCore and XbinRendererModel, in Swift 6 mode:

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"
native/tools/swiftui-stubcheck/run.sh                        # the stubs as written
native/tools/swiftui-stubcheck/run.sh --sendable-bindings    # Binding closures @Sendable
```

What a pass means: our code is consistent with the stubs — calls into our own
model use its real API, optionals and closure types line up, and actor
isolation holds under Swift 6's rules (a closure inside a ternary once failed
here that nothing else would have caught before CI). What it does **not**
mean: that it compiles for iOS. The stubs model only the declarations the
renderer calls, from memory of the SDK, and are permissive where the real
signature is uncertain (e.g. most action closures are plain `() -> Void`).
When the Apple CI disagrees, fix the code and then the stub, so the two keep
converging.

Adding a SwiftUI call to the renderer that the stubs lack fails here with
"no member"/"cannot find": add the declaration to `Stubs/` with the SDK's real
signature (labels, generics, isolation), not a looser one.
