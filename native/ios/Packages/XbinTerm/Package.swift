// swift-tools-version:6.2
// XbinTerm — the terminal's non-drawing half for the xbin app (plans/native.md
// §12): the /ws/term codec, the session state machine, the predictive echo
// engine (a port of web/term-predict.js, D70/D71) and the keyboard model.
// Foundation only: it builds and tests on Linux (native/AGENTS.md).
import PackageDescription

let package = Package(
    name: "XbinTerm",
    platforms: [.iOS(.v26), .macOS(.v26)],
    products: [
        .library(name: "XbinTerm", targets: ["XbinTerm"]),
    ],
    targets: [
        .target(name: "XbinTerm"),
        .testTarget(
            name: "XbinTermTests",
            dependencies: ["XbinTerm"],
            // the differential trace of web/term-predict.js (hack/term-predict-trace.mjs)
            resources: [.copy("Resources/term-predict-trace.json")]
        ),
    ],
    swiftLanguageModes: [.v6]
)
