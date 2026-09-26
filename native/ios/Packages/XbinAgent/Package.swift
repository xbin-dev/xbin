// swift-tools-version: 6.2
// XbinAgent — the native client's model of ACP agent sessions (plans/native.md
// §13): the wire types, the transcript reducer, the D77 card rules and an HTTP
// client over an injected transport. Foundation only, so it builds and tests
// on Linux (native/AGENTS.md: "anything testable here is tested here").
import PackageDescription

let package = Package(
    name: "XbinAgent",
    platforms: [.iOS(.v26), .macOS(.v26)],
    products: [
        .library(name: "XbinAgent", targets: ["XbinAgent"]),
    ],
    targets: [
        .target(name: "XbinAgent"),
        .testTarget(
            name: "XbinAgentTests",
            dependencies: ["XbinAgent"],
            resources: [.copy("Fixtures")]
        ),
    ]
)
