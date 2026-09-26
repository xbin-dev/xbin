// swift-tools-version: 6.2
//
// XbinCore — everything in the xbin app that doesn't draw: the JSON value
// type, the tile tree and its patches, the runtime bridge codec, deep links,
// the workspace record and the device-login message. Foundation only (no
// SwiftUI, UIKit, Combine or OSLog), so it builds and tests on Linux:
//
//   export PATH="$HOME/.local/share/swiftly/bin:$PATH"
//   cd native/ios/Packages/XbinCore && swift build && swift test
//
// Design: plans/native.md §4, §5, §9, §17; dev loop: native/AGENTS.md.
import PackageDescription

let package = Package(
    name: "XbinCore",
    platforms: [.iOS(.v26), .macOS(.v26)],
    products: [
        .library(name: "XbinCore", targets: ["XbinCore"]),
    ],
    targets: [
        .target(name: "XbinCore"),
        .testTarget(name: "XbinCoreTests", dependencies: ["XbinCore"]),
    ]
)
