// Snapshots of every fixture (plans/native.md §17, native/AGENTS.md): each
// native/fixtures/<name>/expected.json drawn by XbinTreeView at 390×844
// points, scale 2, light and dark, default and accessibility-2 Dynamic Type,
// written as <name>-<light|dark>-<default|large>.png to SNAPSHOT_DIR (the
// Apple CI passes TEST_RUNNER_SNAPSHOT_DIR, which xcodebuild hands the test
// process as SNAPSHOT_DIR). Nothing is compared: the PNGs are for eyes and
// the web-vs-iOS contact sheet. Without a snapshot directory every fixture
// is still rendered once (a crash test) and nothing is written.
//
// UIKit only (the Apple CI runs this on a simulator); elsewhere the file is
// empty.
#if canImport(UIKit)
import Foundation
import SwiftUI
import Testing
import UIKit
import XbinCore
import XbinRenderer
import XbinRendererModel

@MainActor
@Suite struct SnapshotTests {
    static let size = CGSize(width: 390, height: 844)
    static let scale: CGFloat = 2

    /// Where PNGs go, or nil (render only).
    static var outputDirectory: URL? {
        let env = ProcessInfo.processInfo.environment
        for name in ["SNAPSHOT_DIR", "TEST_RUNNER_SNAPSHOT_DIR"] {
            if let dir = env[name], !dir.isEmpty { return URL(fileURLWithPath: dir) }
        }
        return nil
    }

    @Test func everyFixture() throws {
        let set = try #require(XbinFixtures.locate(from: #filePath), "native/fixtures not found")
        let names = try set.names()
        #expect(!names.isEmpty)
        let out = Self.outputDirectory
        if let out { try FileManager.default.createDirectory(at: out, withIntermediateDirectories: true) }
        let variants: [(ColorScheme, String, DynamicTypeSize, String)] = out == nil
            ? [(.light, "light", .large, "default")]
            : [(.light, "light", .large, "default"), (.dark, "dark", .large, "default"),
               (.light, "light", .accessibility2, "large"), (.dark, "dark", .accessibility2, "large")]
        var written = 0
        for name in names {
            for (scheme, schemeTag, type, typeTag) in variants {
                let store = try XbinFixtures.store(name, in: set)
                let view = XbinTreeView(store: store, send: { _ in }, options: XbinRenderOptions(inlineSheets: true))
                    .environment(\.colorScheme, scheme)
                    .environment(\.dynamicTypeSize, type)
                let png = Snapshot.png(of: view, size: Self.size, scale: Self.scale, scheme: scheme, type: type)
                #expect(png != nil, "\(name) \(schemeTag) \(typeTag) rendered nothing")
                if let out, let png {
                    try png.write(to: out.appendingPathComponent("\(name)-\(schemeTag)-\(typeTag).png"))
                    written += 1
                }
            }
        }
        if let out { print("snapshots: \(written) PNG in \(out.path)") }
        if let out { try hierarchyExperiment(set: set, out: out) }
    }

    /// An experiment: whether `drawHierarchy` (which composites glass,
    /// materials and vibrancy, all of which `layer.render` skips — bar items
    /// come out white) works in a test process without a host app. Writes
    /// experiments/<name>-hierarchy.png next to the snapshots and logs the
    /// result; nothing depends on it.
    func hierarchyExperiment(set: FixtureSet, out: URL) throws {
        let dir = out.appendingPathComponent("experiments")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        for name in ["buttons", "tabs-bar", "tile-chat"] {
            let store = try XbinFixtures.store(name, in: set)
            let view = XbinTreeView(store: store, send: { _ in }, options: XbinRenderOptions(inlineSheets: true))
                .environment(\.colorScheme, .light)
                .environment(\.dynamicTypeSize, .large)
            let (drawn, png) = Snapshot.hierarchy(of: view, size: Self.size, scale: Self.scale)
            print("snapshot experiment: drawHierarchy \(name) returned \(drawn), \(png?.count ?? 0) bytes")
            if let png { try png.write(to: dir.appendingPathComponent("\(name)-hierarchy.png")) }
        }
    }

    @Test func chatComponentsStandAlone() {
        // The agent screen uses the components without a tree.
        let view = VStack(spacing: 12) {
            MessageView(message: ChatMessage(id: "m", role: .assistant, text: "", markdown: Markdown.blocks(
                try? JSONValue(parsing: #"[{"t":"paragraph","c":[{"t":"strong","c":[{"t":"text","text":"Hi"}]}]}]"#))))
            ComposerView(composer: ChatComposer(slash: [SlashCommand(name: "/deploy")]), text: .constant("/d"), onSend: { _ in })
            QuestionView(question: ChatQuestion(id: "q", title: "Pick", form: QuestionForm(schema: nil)), onSubmit: { _ in })
            DiffView(diff: ChatDiff(files: [.init(path: "a.go", added: 2, removed: 1)], patch: "@@ -1 +1 @@\n-a\n+b\n"))
        }
        #expect(Snapshot.png(of: view, size: Self.size, scale: 1, scheme: .light, type: .large) != nil)
    }
}

/// Renders a view to PNG: hosted in a window and drawn by its layer tree (so
/// UIKit-backed pieces — lists, fields, navigation bars — are captured,
/// which `ImageRenderer` draws as placeholders), falling back to
/// `ImageRenderer`.
@MainActor
enum Snapshot {
    static func png<V: View>(of view: V, size: CGSize, scale: CGFloat, scheme: ColorScheme, type: DynamicTypeSize) -> Data? {
        hosted(view, size: size, scale: scale, scheme: scheme, type: type) ?? rendered(view, size: size, scale: scale)
    }

    /// Whether the safe-area insets were logged (once per run).
    static var loggedInsets = false

    static func hosted<V: View>(_ view: V, size: CGSize, scale: CGFloat, scheme: ColorScheme, type: DynamicTypeSize) -> Data? {
        let controller = UIHostingController(rootView: view.frame(width: size.width, height: size.height))
        // Lay the tree out in exactly size.width × size.height, like the
        // reference renderer's viewport: an offscreen window's safe area is
        // whatever the simulator makes of a window without a scene, and a
        // 844-point frame inside a smaller safe area overflowed it (the
        // docked composer was cut off at the bottom).
        controller.safeAreaRegions = []
        let style: UIUserInterfaceStyle = scheme == .dark ? .dark : .light
        controller.overrideUserInterfaceStyle = style
        controller.traitOverrides.preferredContentSizeCategory = UIContentSizeCategory(type)
        let window = UIWindow(frame: CGRect(origin: .zero, size: size))
        window.overrideUserInterfaceStyle = style
        window.rootViewController = controller
        window.isHidden = false
        controller.view.frame = window.bounds
        controller.view.setNeedsLayout()
        controller.view.layoutIfNeeded()
        // Let SwiftUI settle: lists, navigation bars, scroll anchors, tasks.
        for _ in 0..<6 { RunLoop.main.run(until: Date().addingTimeInterval(0.05)) }
        controller.view.layoutIfNeeded()
        if !loggedInsets {
            loggedInsets = true
            print("snapshot: window safe area \(window.safeAreaInsets), view safe area \(controller.view.safeAreaInsets)")
        }
        let format = UIGraphicsImageRendererFormat()
        format.scale = scale
        format.opaque = true
        // layer.render works off screen (a test bundle has no window scene),
        // as swift-snapshot-testing does; it skips blur materials.
        let image = UIGraphicsImageRenderer(size: size, format: format).image { ctx in
            window.layer.render(in: ctx.cgContext)
        }
        window.isHidden = true
        window.rootViewController = nil
        return image.pngData()
    }

    /// The experiment's renderer: the window drawn with `drawHierarchy`.
    static func hierarchy<V: View>(of view: V, size: CGSize, scale: CGFloat) -> (Bool, Data?) {
        let controller = UIHostingController(rootView: view.frame(width: size.width, height: size.height))
        controller.safeAreaRegions = []
        controller.overrideUserInterfaceStyle = .light
        let window = UIWindow(frame: CGRect(origin: .zero, size: size))
        window.rootViewController = controller
        window.isHidden = false
        controller.view.frame = window.bounds
        controller.view.layoutIfNeeded()
        for _ in 0..<6 { RunLoop.main.run(until: Date().addingTimeInterval(0.05)) }
        controller.view.layoutIfNeeded()
        let format = UIGraphicsImageRendererFormat()
        format.scale = scale
        format.opaque = true
        var drawn = false
        let image = UIGraphicsImageRenderer(size: size, format: format).image { _ in
            drawn = window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
        }
        window.isHidden = true
        window.rootViewController = nil
        return (drawn, image.pngData())
    }

    static func rendered<V: View>(_ view: V, size: CGSize, scale: CGFloat) -> Data? {
        let renderer = ImageRenderer(content: view.frame(width: size.width, height: size.height))
        renderer.scale = scale
        renderer.proposedSize = ProposedViewSize(size)
        return renderer.uiImage?.pngData()
    }
}
#endif
