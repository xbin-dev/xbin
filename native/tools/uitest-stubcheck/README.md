# uitest-stubcheck — type-check the UI tests on Linux

`native/ios/UITests` (the XbinUITests target: the app end to end on a
simulator against a real xbind) is XCTest/XCUITest code, which only an Apple
toolchain compiles: the ios.yml `app` job builds it for testing, a round trip
of minutes. This tool compiles the same files here, in Swift 6 mode, against
**hand-written stubs** of the XCTest and XCUITest API they call
(`Stubs/XCTest.swift`):

```sh
export PATH="$HOME/.local/share/swiftly/bin:$PATH"
native/tools/uitest-stubcheck/run.sh
```

What a pass means: the tests are consistent with the stubs — syntax, types,
optionals, and actor isolation under the SDK's rules as the stubs state them
(since Xcode 16, `XCUIApplication`, `XCUIElement`, `XCUIElementQuery`,
`XCUICoordinate` and `XCUIScreen` are `@MainActor`; `XCTestCase` and
`XCTAttachment` are not — so each test method that drives the app is
`@MainActor`, and `setUpWithError` stays nonisolated). Dropping a test's
`@MainActor` fails here, as it would on CI. What it does **not** mean: that
it compiles for iOS. The stubs are written from memory of the SDK's headers
and model only what the tests call; `NSPredicate(format:)`, unavailable in
swift-corelibs-foundation, is renamed to a stub with the same signature.

When the Apple CI disagrees, fix the test and then the stub. A new XCUITest
call the stubs lack fails with "no member"/"cannot find": add it to
`Stubs/XCTest.swift` with the SDK's real signature and isolation.
