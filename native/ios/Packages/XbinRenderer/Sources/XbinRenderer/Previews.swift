#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// A fixture (`native/fixtures/<name>/expected.json`, plans/native.md §17)
/// drawn by ``XbinTreeView`` — for `#Preview`s and a debug gallery. The
/// fixture is found from `filePath` (the caller's `#filePath`: previews run
/// on the Mac that has the checkout) or `FIXTURES_DIR`. Open sheets draw
/// inline by default so a preview shows them. A widget fixture (data.json
/// `widget`) is drawn as its card, compact, as a phone screen shows it.
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
            if let size = try? XbinFixtures.widgetSize(name, in: set) {
                GeometryReader { g in
                    XbinTreeView(store: store, send: { call in print("xbn:", call.javaScript) },
                                 options: XbinRenderOptions(compact: size))
                        .xbinWidgetCard()
                        .frame(width: XbinWidgetMetrics.cardWidth(size, screenWidth: g.size.width))
                        .padding(XbinWidgetMetrics.margin)
                }
                .background(XbinColor.background)
            } else {
                XbinTreeView(store: store, send: { call in print("xbn:", call.javaScript) }, options: options)
            }
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
#Preview("doc-reader") { XbinFixturePreview("doc-reader") }
#Preview("drawer") { XbinFixturePreview("drawer") }
#Preview("escape-hatches") { XbinFixturePreview("escape-hatches") }
#Preview("folded-actions") { XbinFixturePreview("folded-actions") }
#Preview("icons") { XbinFixturePreview("icons") }
#Preview("ime-field") { XbinFixturePreview("ime-field") }
#Preview("markdown") { XbinFixturePreview("markdown") }
#Preview("media") { XbinFixturePreview("media") }
#Preview("message-files") { XbinFixturePreview("message-files") }
#Preview("old-app") { XbinFixturePreview("old-app") }
#Preview("old-app-agent") { XbinFixturePreview("old-app-agent") }
#Preview("notices-empty-progress") { XbinFixturePreview("notices-empty-progress") }
#Preview("search-toolbars") { XbinFixturePreview("search-toolbars") }
#Preview("sections-rows") { XbinFixturePreview("sections-rows") }
#Preview("sheet-open") { XbinFixturePreview("sheet-open") }
#Preview("sheet-stack") { XbinFixturePreview("sheet-stack") }
#Preview("split") { XbinFixturePreview("split") }
#Preview("split-collapse") { XbinFixturePreview("split-collapse") }
#Preview("split-single") { XbinFixturePreview("split-single") }
#Preview("stack-layout") { XbinFixturePreview("stack-layout") }
#Preview("structure-nav") { XbinFixturePreview("structure-nav") }
#Preview("tabs") { XbinFixturePreview("tabs") }
#Preview("tabs-bar") { XbinFixturePreview("tabs-bar") }
#Preview("text") { XbinFixturePreview("text") }
#Preview("tile-calendar") { XbinFixturePreview("tile-calendar") }
#Preview("tile-chat") { XbinFixturePreview("tile-chat") }
#Preview("tile-counter-go") { XbinFixturePreview("tile-counter-go") }
#Preview("tile-devbox") { XbinFixturePreview("tile-devbox") }
#Preview("tile-egress-approver") { XbinFixturePreview("tile-egress-approver") }
#Preview("tile-prometheus-viewer") { XbinFixturePreview("tile-prometheus-viewer") }
#Preview("tile-s3-archiver") { XbinFixturePreview("tile-s3-archiver") }
#Preview("tile-sandbox-terminal") { XbinFixturePreview("tile-sandbox-terminal") }
#Preview("tile-sandbox-terminal-keys") { XbinFixturePreview("tile-sandbox-terminal-keys") }
#Preview("tile-webhooks") { XbinFixturePreview("tile-webhooks") }
#Preview("widget-counter") { XbinFixturePreview("widget-counter") }
#Preview("widget-wide") { XbinFixturePreview("widget-wide") }
#Preview("workspace-agent") { XbinFixturePreview("workspace-agent") }
#Preview("workspace-agent-empty") { XbinFixturePreview("workspace-agent-empty") }

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

// The workspace agent's components (D233), as the app's own screens may
// build them from plain models.
#Preview("workspace agent components") {
    VStack(spacing: 0) {
        ConnectionView(connection: ConnectionModel(connected: "14 tiles · 3 orgs", not: "6 not connected", model: "big"),
                       onTap: {}, onModel: {})
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                PlanView(plan: ChatPlan(entries: [.init(text: "Look at Orders’ export", status: .completed),
                                                  .init(text: "Rebuild the customer links in Invoices", status: .pending)],
                                        title: "Plan", numbered: true,
                                        tags: [.init(text: "done", icon: "check", tone: .ok), .init(text: "asks you", icon: "shield", tone: .warn)],
                                        note: "Saying go ahead starts step 1. It does not approve step 2: it asks when it is reached."))
                CardView(card: CardModel(title: "Ledger is blocked by a rule", icon: "lock", tone: .warn, badge: "Finance",
                                         note: "This is a rule set by an admin, not something the agent can work around."),
                         buttonCount: 2) {
                    Text(verbatim: "Finance allows private models only.").font(.subheadline)
                } buttons: {
                    Button("Change the model…") {}.xbinSecondary()
                    Button("Open scope") {}.xbinSecondary()
                }
                TrustedPlaceholder(title: XbinTrustedNode.request(id: "rq_1", kind: "call").placeholderTitle, session: false)
                TrustedPlaceholder(title: XbinTrustedNode.session(id: "ts_1", tile: "apps/orders").placeholderTitle, session: true)
                TranscriptEmptyView(empty: TranscriptEmpty(
                    title: "Ask for an outcome", text: "The workspace agent plans the work and asks you before anything changes.",
                    lists: [.init(id: 0, title: "It can", items: ["Look across your 14 connected tiles", "Remember what you tell it"])],
                    suggestionsTitle: "Try one — from your tiles",
                    suggestions: [.init(id: "fix", kind: "Fix", text: "Team calendar’s last sync failed. Find out why.")])) { _ in }
            }
            .padding()
        }
        ComposerView(composer: ChatComposer(placeholder: "Ask for an outcome…",
                                            context: [ChatContextChip(id: "apps/orders", label: "About Orders", icon: "box")],
                                            replies: [ChatReply(id: "go", label: "Go ahead"), ChatReply(id: "change", label: "Change the plan")],
                                            hint: "Say what you want and how you’ll know it’s done."),
                     text: .constant(""), onSend: { _ in },
                     extras: ComposerExtras(onContext: { _ in }, onRemoveContext: { _ in }, onReply: { _ in }))
    }
}
#endif
