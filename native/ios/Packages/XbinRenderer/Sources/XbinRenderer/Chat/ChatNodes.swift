#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

// The chat family's tree adapters: a node's props → the view models in
// XbinRendererModel → the public components, with events back to the tile.

/// `transcript`: sticks to the bottom with `follow`; `more` when `older`
/// scrolls into view; `scrolled {atBottom}`. Inside a tool card (a
/// subagent) it is a plain column.
struct TranscriptNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement

    var body: some View {
        let p = node.props
        TranscriptView(follow: p.bool("follow"), older: p.bool("older"), nested: placement == .toolcard,
                       onMore: cx?.action(node, "more"),
                       onScrolled: cx?.action(node, "scrolled") { (atBottom: Bool) in ["atBottom": .bool(atBottom)] }) {
            ForEach(node.children) { NodeView(node: $0) }
        }
        .environment(\.xbinPlacement, .chat)
    }
}

/// `message`: `tap`, `link {href}`, and its `actions` as small buttons.
struct MessageNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let context = cx
        let n = node
        let actions = node.children.first { $0.type == "actions" }
        MessageView(message: ChatMessage(id: node.key, props: node.props),
                    onLink: { url in context?.link(url, in: n) }, onTap: cx?.action(node, "tap")) {
            if let actions, !actions.children.isEmpty {
                HStack(spacing: 14) { ForEach(actions.children) { NodeView(node: $0) } }
                    .environment(\.xbinPlacement, .inlineActions)
            }
        }
    }
}

/// `thinking`: `open` is controlled (`toggle {open}`).
struct ThinkingNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        ThinkingView(thinking: ChatThinking(id: node.key, props: node.props), isOpen: openBinding(node, cx))
    }
}

/// `toolcard`: `open` is controlled (`toggle {open}`); `open` (the event)
/// adds the open-full-screen button; children fold inside.
struct ToolCardNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        ToolCardView(card: ChatToolCard(id: node.key, props: node.props), isOpen: openBinding(node, cx),
                     hasContent: !node.children.isEmpty, onOpen: cx?.action(node, "open")) {
            ForEach(node.children) { NodeView(node: $0) }
                .environment(\.xbinPlacement, .toolcard)
        }
    }
}

/// The `open` prop of a thinking or tool card as a binding.
@MainActor
private func openBinding(_ node: XbinNode, _ cx: XbinRenderContext?) -> Binding<Bool> {
    mainBinding(
        get: { node.value("open")?.boolValue ?? false },
        set: { cx?.emit(node, "toggle", ["open": .bool($0)]) }
    )
}

/// `approval`: `choose {id, feedback}`.
struct ApprovalNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let context = cx
        let n = node
        ApprovalView(approval: ChatApproval(id: node.key, props: node.props)) { id, feedback in
            context?.emit(n, "choose", ["id": .string(id), "feedback": .string(feedback)])
        }
    }
}

/// `question`: `submit {content}`, `skip` when listened to.
struct QuestionNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let context = cx
        let n = node
        QuestionView(question: ChatQuestion(id: node.key, props: node.props),
                     onSubmit: { content in context?.emit(n, "submit", ["content": content]) },
                     onSkip: cx?.action(node, "skip"))
            .id(node.props["schema"].map(\.jsonString) ?? "")
    }
}

/// `diff`: `open-file {path}` when listened to.
struct DiffNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        DiffView(diff: ChatDiff(props: node.props),
                 onOpenFile: cx?.action(node, "open-file") { (path: String) in ["path": .string(path)] })
    }
}

/// `composer`: `value` is controlled (`input`, `send`); `stop` while busy;
/// attachments are uploaded by the app (``XbinServices/attach``) and
/// handed over as `uploaded {name, response}`; `remove {id}`; its button
/// children are chips.
struct ComposerNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let p = node.props
        let context = cx
        let n = node
        let composer = ChatComposer(props: p)
        let text = mainBinding(
            get: { Props.text(n.value("value")) ?? "" },
            set: { context?.emit(n, "input", ["value": .string($0)]) }
        )
        let stop = cx?.action(node, "stop")
        let remove = cx?.action(node, "remove") { (id: String) in ["id": .string(id)] }
        ComposerView(composer: composer, text: text, onSend: { value in
            context?.emit(n, "send", ["value": .string(value)])
            // An unbound composer clears itself; a bound one waits for the tile.
            context?.model.setRendererValue(n.key, "value", "")
        }, onStop: stop, onAttach: attachAction(p), onRemoveAttachment: remove) {
            ForEach(node.children) { NodeView(node: $0) }
                .environment(\.xbinPlacement, .chips)
        }
    }

    /// The attach button's action: the app's pickers and upload, then one
    /// `uploaded` per file. Nil without an upload target or an app handler.
    private func attachAction(_ p: Props) -> (@MainActor () -> Void)? {
        guard let upload = p.object("upload"), let path = Props.text(upload["path"]), !path.isEmpty,
              let attach = cx?.services.attach else { return nil }
        let context = cx
        let n = node
        let request = XbinAttachRequest(key: node.key, method: Props.text(upload["method"]) ?? "POST", path: path,
                                        accept: p.nonEmpty("accept"))
        return {
            Task { @MainActor in
                for file in await attach(request) {
                    context?.emit(n, "uploaded", ["name": .string(file.name), "response": file.response])
                }
            }
        }
    }
}
#endif
