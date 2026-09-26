// swift-tools-version:6.2
// term-live — XbinTerm's TermSession against a real xbind /ws/term (Linux).
// See Sources/term-live/main.swift for how to run it.
import PackageDescription

let package = Package(
    name: "term-live",
    platforms: [.macOS(.v26)],
    dependencies: [.package(path: "../../ios/Packages/XbinTerm")],
    targets: [.executableTarget(name: "term-live", dependencies: [.product(name: "XbinTerm", package: "XbinTerm")])],
    swiftLanguageModes: [.v6]
)
