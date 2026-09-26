// The host app of the hosted snapshot tests (project.yml: XbinSnapshotHost,
// scheme XbinSnapshots; native/AGENTS.md). It shows nothing: it exists so
// the tests (Packages/XbinRenderer/Tests/XbinRendererTests, compiled with
// XBIN_SNAPSHOT_HOST) can put their windows on a real UIWindowScene and draw
// them with drawHierarchy, which composites what layer.render skips —
// Liquid Glass bar items, tab bars, materials and vibrancy.
import SwiftUI

@main
struct SnapshotHostApp: App {
    var body: some Scene {
        WindowGroup { Color.clear }
    }
}
