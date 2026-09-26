// swift-tools-version: 6.2
// hatches-live — the escape hatches' and prompt attachments' non-UI halves
// against a running xbind (see Sources/hatches-live/main.swift). Linux only.
import PackageDescription

let package = Package(
    name: "hatches-live",
    platforms: [.macOS(.v26)],
    dependencies: [
        .package(path: "../../ios/Packages/XbinCore"),
        .package(path: "../../ios/Packages/XbinTerm"),
        .package(path: "../../ios/Packages/XbinAgent"),
    ],
    targets: [
        .executableTarget(name: "hatches-live", dependencies: [
            .product(name: "XbinCore", package: "XbinCore"), .product(name: "XbinTerm", package: "XbinTerm"),
            .product(name: "XbinAgent", package: "XbinAgent"),
        ]),
    ],
    swiftLanguageModes: [.v6]
)
