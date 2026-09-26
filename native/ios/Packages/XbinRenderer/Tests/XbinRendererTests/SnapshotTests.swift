// Snapshots of every fixture (plans/native.md §17, native/AGENTS.md): each
// native/fixtures/<name>/expected.json drawn by XbinTreeView at 390×844
// points, scale 2, light and dark, at three Dynamic Type sizes — default
// (Large), large (xxxLarge, the size the reference renderer's "large"
// screenshots use, so the two compare like for like) and ax2
// (accessibility2, the overflow test) — written as
// <name>-<light|dark>-<default|large|ax2>.png to SNAPSHOT_DIR (the
// Apple CI passes TEST_RUNNER_SNAPSHOT_DIR, which xcodebuild hands the test
// process as SNAPSHOT_DIR). Nothing is compared: the PNGs are for eyes and
// the web-vs-iOS contact sheet. Without a snapshot directory every fixture
// is still rendered once (a crash test) and nothing is written.
//
// Two ways to run: as the XbinRenderer package's tests (no host app — the
// windows are offscreen and drawn with layer.render, which skips Liquid
// Glass, materials and vibrancy: bar items come out white), and compiled
// into native/ios/project.yml's XbinSnapshotTests (XBIN_SNAPSHOT_HOST), hosted
// by an empty app, where the windows are made with UIWindow(windowScene:)
// on its scene and drawn with drawHierarchy as the screen shows them
// (ci-hosted-snapshots.sh).
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
               (.light, "light", .xxxLarge, "large"), (.dark, "dark", .xxxLarge, "large"),
               (.light, "light", .accessibility2, "ax2"), (.dark, "dark", .accessibility2, "ax2")]
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

/// Renders a view to PNG: hosted in a window and drawn — with drawHierarchy
/// on the host app's scene, else by its layer tree — so UIKit-backed pieces
/// (lists, fields, navigation bars) are captured, which `ImageRenderer`
/// draws as placeholders; `ImageRenderer` is the last resort.
@MainActor
enum Snapshot {
    static func png<V: View>(of view: V, size: CGSize, scale: CGFloat, scheme: ColorScheme, type: DynamicTypeSize) -> Data? {
        hosted(view, size: size, scale: scale, scheme: scheme, type: type) ?? rendered(view, size: size, scale: scale)
    }

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
        let (window, onScene) = makeWindow(size: size)
        window.overrideUserInterfaceStyle = style
        window.rootViewController = controller
        window.isHidden = false
        controller.view.frame = window.bounds
        controller.view.setNeedsLayout()
        controller.view.layoutIfNeeded()
        // Let SwiftUI settle: lists, navigation bars, scroll anchors, tasks.
        for _ in 0..<6 { RunLoop.main.run(until: Date().addingTimeInterval(0.05)) }
        controller.view.layoutIfNeeded()
        let format = UIGraphicsImageRendererFormat()
        format.scale = scale
        format.opaque = true
        var how = "layer.render"
        let image = UIGraphicsImageRenderer(size: size, format: format).image { ctx in
            // drawHierarchy draws what the screen shows (glass, materials,
            // vibrancy) but needs a window on a scene; layer.render works
            // off screen, as swift-snapshot-testing does, without those.
            if onScene && window.drawHierarchy(in: window.bounds, afterScreenUpdates: true) {
                how = "drawHierarchy"
            } else {
                window.layer.render(in: ctx.cgContext)
            }
        }
        if !loggedMode {
            loggedMode = true
            print("snapshot: drawn with \(how); window safe area \(window.safeAreaInsets)")
        }
        window.isHidden = true
        window.rootViewController = nil
        return image.pngData()
    }

    /// Whether the drawing mode was logged (once per run).
    static var loggedMode = false

    /// The window to draw in, and whether it is on a window scene: the
    /// snapshot host app's scene (`UIWindow(windowScene:)`) when the tests
    /// run there, or any app's scene they run in; else offscreen.
    static func makeWindow(size: CGSize) -> (UIWindow, Bool) {
        let frame = CGRect(origin: .zero, size: size)
        if let scene = windowScene() {
            let window = UIWindow(windowScene: scene)
            window.frame = frame
            return (window, true)
        }
        // The package's own run has no app, so no scene to make a window
        // on: only the scene-less initializer iOS 26 deprecated is left.
        let maker: OffscreenWindowMaking.Type = OffscreenWindow.self
        return (maker.window(frame), false)
    }

    /// A connected window scene: the host app's, once it has connected
    /// (the tests may start first); none without an app.
    static func windowScene() -> UIWindowScene? {
        #if XBIN_SNAPSHOT_HOST
        let deadline = Date().addingTimeInterval(15)
        repeat {
            if let scene = connectedScene() { return scene }
            RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        } while Date() < deadline
        print("snapshot: the host app has no window scene; drawing offscreen")
        return nil
        #else
        // A hostless test bundle has no UIApplication to ask.
        guard Bundle.main.bundleURL.pathExtension == "app" else { return nil }
        return connectedScene()
        #endif
    }

    static func connectedScene() -> UIWindowScene? {
        UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
    }

    static func rendered<V: View>(_ view: V, size: CGSize, scale: CGFloat) -> Data? {
        let renderer = ImageRenderer(content: view.frame(width: size.width, height: size.height))
        renderer.scale = scale
        renderer.proposedSize = ProposedViewSize(size)
        return renderer.uiImage?.pngData()
    }
}
/// A window without a scene, for the hostless package run only. Reached
/// through a protocol so the deprecated initializer is named in one
/// deprecated place (every scene that exists gets init(windowScene:)).
@MainActor
private protocol OffscreenWindowMaking {
    static func window(_ frame: CGRect) -> UIWindow
}

@MainActor
private enum OffscreenWindow: OffscreenWindowMaking {
    @available(iOS, deprecated: 26.0, message: "only where no window scene exists (tests without an app)")
    static func window(_ frame: CGRect) -> UIWindow { UIWindow(frame: frame) }
}
#endif
