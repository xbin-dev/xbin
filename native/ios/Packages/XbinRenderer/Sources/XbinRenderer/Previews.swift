#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// A fixture (`native/fixtures/<name>/expected.json`, plans/native.md §17)
/// drawn by ``XbinTreeView`` — for `#Preview`s and a debug gallery. The
/// fixture is found from `filePath` (the caller's `#filePath`: previews run
/// on the Mac that has the checkout) or `FIXTURES_DIR`. Open sheets draw
/// inline by default so a preview shows them.
public struct XbinFixturePreview: View {
    let name: String
    let filePath: String
    let options: XbinRenderOptions

    public init(_ name: String, filePath: String = #filePath, inlineSheets: Bool = true) {
        self.name = name
        self.filePath = filePath
        options = XbinRenderOptions(inlineSheets: inlineSheets)
    }

    public var body: some View {
        if let set = XbinFixtures.locate(from: filePath), let store = try? XbinFixtures.store(name, in: set) {
            XbinTreeView(store: store, send: { call in print("xbn:", call.javaScript) }, options: options)
        } else {
            ContentUnavailableView("Fixture \(name) not found", systemImage: "questionmark.folder",
                                   description: Text(verbatim: "native/fixtures above \(filePath)"))
        }
    }
}

// One preview per fixture (PreviewCoverageTests checks that none is missing).
#Preview("buttons") { XbinFixturePreview("buttons") }
#Preview("charts") { XbinFixturePreview("charts") }
#Preview("chat-tools") { XbinFixturePreview("chat-tools") }
#Preview("chat-transcript") { XbinFixturePreview("chat-transcript") }
#Preview("controls") { XbinFixturePreview("controls") }
#Preview("escape-hatches") { XbinFixturePreview("escape-hatches") }
#Preview("icons") { XbinFixturePreview("icons") }
#Preview("markdown") { XbinFixturePreview("markdown") }
#Preview("media") { XbinFixturePreview("media") }
#Preview("notices-empty-progress") { XbinFixturePreview("notices-empty-progress") }
#Preview("sections-rows") { XbinFixturePreview("sections-rows") }
#Preview("sheet-open") { XbinFixturePreview("sheet-open") }
#Preview("split") { XbinFixturePreview("split") }
#Preview("split-single") { XbinFixturePreview("split-single") }
#Preview("stack-layout") { XbinFixturePreview("stack-layout") }
#Preview("structure-nav") { XbinFixturePreview("structure-nav") }
#Preview("tabs") { XbinFixturePreview("tabs") }
#Preview("tabs-bar") { XbinFixturePreview("tabs-bar") }
#Preview("text") { XbinFixturePreview("text") }

#Preview("chat components") {
    ScrollView {
        VStack(alignment: .leading, spacing: 14) {
            MessageView(message: ChatMessage(id: "u", role: .user, sender: "Ana", text: "Why is checkout slow?", time: "18:02"))
            ThinkingView(thinking: ChatThinking(id: "t", text: "Check the idle timeout.", seconds: 4), isOpen: .constant(true))
            ToolCardView(card: ChatToolCard(id: "c", title: "go test ./...", icon: "terminal", state: .error,
                                            chips: [ChatChip(text: "exit 1", tone: .danger)]),
                         isOpen: .constant(true)) {
                CodeBlock(text: "--- FAIL: TestIdleReuse (0.21s)")
            }
            PlanView(plan: ChatPlan(entries: [.init(text: "Fix", status: .completed), .init(text: "Test", status: .inProgress)]))
            ApprovalView(approval: ChatApproval(id: "a", title: "Run a shell command", text: "rm legacy.go",
                                                options: [ApprovalOption(id: "y", label: "Allow once", kind: "allow_once"),
                                                          ApprovalOption(id: "n", label: "Reject", kind: "reject_once")])) { _, _ in }
            StepView(step: ChatStep(glyph: "✓", text: "Rolled back", tone: .ok))
            ActivityView(activity: ChatActivity(text: "Comparing configs…", live: true))
        }
        .padding()
    }
}
#endif
