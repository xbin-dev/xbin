// swift-tools-version:6.2
// app-check — the app's crypto and SwiftTerm glue, checked on Linux.
//
// The files under Tests/AppCheckTests and Sources/app-live that are symlinks
// ARE the app's own sources (native/ios/Shared, native/ios/App/Terminal,
// native/ios/App/Model/WorkspaceEvents.swift — the events socket's driver):
// swift-crypto gives Linux CryptoKit's API and SwiftTerm's headless Terminal
// builds here, so the code the app and its Notification Service Extension
// ship is run against the spec's vectors and a real emulator — not only
// compiled on CI.
//
//   cd native/tools/app-check && swift test            # offline checks
//   swift run app-live 127.0.0.1:9461 admin admin       # against a running xbind (see main.swift)
import PackageDescription

let package = Package(
    name: "app-check",
    platforms: [.macOS(.v26)],
    dependencies: [
        .package(path: "../../ios/Packages/XbinCore"),
        .package(path: "../../ios/Packages/XbinTerm"),
        .package(path: "../../ios/Packages/XbinAgent"),
        .package(url: "https://github.com/apple/swift-crypto.git", from: "3.9.0"),
        .package(url: "https://github.com/migueldeicaza/SwiftTerm.git", from: "1.20.0"),
    ],
    targets: [
        .testTarget(
            name: "AppCheckTests",
            dependencies: [
                .product(name: "XbinCore", package: "XbinCore"),
                .product(name: "XbinTerm", package: "XbinTerm"),
                .product(name: "XbinAgent", package: "XbinAgent"),
                .product(name: "Crypto", package: "swift-crypto"),
                .product(name: "SwiftTerm", package: "SwiftTerm"),
            ]
        ),
        .executableTarget(
            name: "app-live",
            dependencies: [
                .product(name: "XbinCore", package: "XbinCore"),
                .product(name: "XbinAgent", package: "XbinAgent"),
                .product(name: "Crypto", package: "swift-crypto"),
            ]
        ),
    ],
    swiftLanguageModes: [.v6]
)
