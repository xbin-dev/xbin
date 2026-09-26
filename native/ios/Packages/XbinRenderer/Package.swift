// swift-tools-version: 6.2
//
// XbinRenderer — the SwiftUI renderer of native tiles (plans/native.md §8,
// §10, §15): one view per primitive of the vocabulary (web/xb/vocab.js,
// native/spec/vocab.json), the chat family as reusable components, and
// snapshot tests of every fixture in native/fixtures.
//
// Two targets:
//
//   XbinRendererModel  Foundation + Observation + XbinCore — the tree as
//                      observable nodes, controlled props, the vocabulary
//                      tables, markdown/chart/question/chat view models.
//                      Builds and tests on Linux:
//
//     export PATH="$HOME/.local/share/swiftly/bin:$PATH"
//     cd native/ios/Packages/XbinRenderer && swift build && swift test
//
//   XbinRenderer       SwiftUI + UIKit (iOS 26) — the views; built and
//                      snapshot-tested only by the Apple CI
//                      (native/ios/scripts/ci-snapshots.sh, xcodebuild on a
//                      simulator). Empty wherever UIKit is missing.
//
// Design: plans/native.md; dev loop: native/AGENTS.md; package notes:
// README.md next to this file.
import PackageDescription

let core: Target.Dependency = .product(name: "XbinCore", package: "XbinCore")

let package = Package(
    name: "XbinRenderer",
    platforms: [.iOS(.v26), .macOS(.v26)],
    products: [
        // The app links this one product (views + model).
        .library(name: "XbinRenderer", targets: ["XbinRenderer", "XbinRendererModel"]),
    ],
    dependencies: [.package(path: "../XbinCore")],
    targets: [
        .target(name: "XbinRendererModel", dependencies: [core]),
        // Every file is wrapped in `#if canImport(UIKit)`: on Linux and a
        // macOS host the module is empty, so `swift build`/`swift test` there
        // build and test the model alone.
        .target(name: "XbinRenderer", dependencies: ["XbinRendererModel", core]),
        .testTarget(name: "XbinRendererModelTests", dependencies: ["XbinRendererModel", core]),
        .testTarget(name: "XbinRendererTests", dependencies: ["XbinRenderer", "XbinRendererModel", core]),
    ]
)
