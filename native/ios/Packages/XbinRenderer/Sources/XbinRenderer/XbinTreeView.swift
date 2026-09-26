#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// A native tile's UI: the tree its runtime renders (a ``TreeStore`` fed
/// from the runtime's `xbn` messages), drawn with SwiftUI — one view per
/// primitive of the vocabulary (plans/native.md §8), user actions sent back
/// through `send` (`callAsyncJavaScript(call.functionBody, …)` on the
/// tile's runtime WebView).
///
/// ```swift
/// let store = TreeStore(savedState: saved)
/// // WKScriptMessageHandler "xbn": store.receive(body: message.body)
/// XbinTreeView(store: store, send: { call in runtime.call(call) }, services: services)
/// ```
///
/// The view observes the store: feed it messages as they arrive. When the
/// store fails (``TreeStore/failure``) the app shows the web tile instead
/// (§7.6); the view itself only draws a placeholder. Create the view once
/// per store (or pass an ``XbinTreeModel`` you keep): the model it builds is
/// kept for the view's lifetime, with the `send` and services it was given.
public struct XbinTreeView: View {
    @State private var context: XbinRenderContext

    public init(store: TreeStore, send: @escaping @MainActor (RuntimeCall) -> Void,
                services: XbinServices = XbinServices(), options: XbinRenderOptions = XbinRenderOptions()) {
        let model = XbinTreeModel(store: store, send: send)
        _context = State(initialValue: XbinRenderContext(model: model, services: services, options: options))
    }

    public init(model: XbinTreeModel, services: XbinServices = XbinServices(),
                options: XbinRenderOptions = XbinRenderOptions()) {
        _context = State(initialValue: XbinRenderContext(model: model, services: services, options: options))
    }

    public var body: some View {
        RootView(model: context.model)
            .environment(\.xbin, context)
            .environment(\.xbinImages, context.images)
            .modifier(ConfirmHostModifier())
            .tint(XbinColor.tint)
    }
}

/// The root: full-screen primitives lay themselves out; anything else gets
/// a scrolling body with margins (the reference renderer's "loose" root).
private struct RootView: View {
    let model: XbinTreeModel

    static let fullScreen: Set<String> = ["screen", "nav", "fragment", "split", "tabs", "sheet", "transcript"]

    var body: some View {
        if let root = model.root {
            if Self.fullScreen.contains(root.type) {
                NodeView(node: root)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 12) { NodeView(node: root) }
                        .padding(16)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .background(XbinColor.background)
            }
        } else if model.failure != nil {
            ContentUnavailableView("Native view unavailable", systemImage: "exclamationmark.triangle")
        } else {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}

/// Draws one node: dispatches on the primitive. A primitive this renderer
/// doesn't know (a newer runtime) draws a neutral placeholder (tree.md §2).
struct NodeView: View {
    let node: XbinNode

    static let structure: Set<String> = [
        "fragment", "nav", "screen", "toolbar", "section", "stack", "list", "row", "actions", "disclosure",
        "tabs", "tab", "sheet", "split", "spacer", "divider",
    ]
    static let content: Set<String> = [
        "text", "markdown", "image", "icon", "badge", "notice", "progress", "chart", "code", "empty",
        "terminal", "canvas",
    ]
    static let controls: Set<String> = ["button", "toggle", "field", "picker", "menu"]
    static let chat: Set<String> = [
        "transcript", "message", "thinking", "toolcard", "approval", "question", "plan", "diff", "activity",
        "step", "composer",
    ]

    var body: some View {
        let t = node.type
        if Self.structure.contains(t) {
            StructureNode(node: node)
        } else if Self.content.contains(t) {
            ContentNode(node: node)
        } else if Self.controls.contains(t) {
            ControlNode(node: node)
        } else if Self.chat.contains(t) {
            ChatNode(node: node)
        } else {
            UnknownNodeView(node: node)
        }
    }
}

/// The children of `node`, drawn in place.
struct ChildrenView: View {
    let node: XbinNode

    var body: some View {
        ForEach(node.children) { NodeView(node: $0) }
    }
}

private struct StructureNode: View {
    let node: XbinNode

    var body: some View {
        switch node.type {
        case "fragment": FragmentView(node: node)
        case "nav": NavView(node: node)
        case "screen": ScreenView(node: node)
        case "toolbar": ToolbarInline(node: node)
        case "section": SectionView(node: node)
        case "stack": StackView(node: node)
        case "list": ListNodeView(node: node)
        case "row": RowView(node: node)
        case "disclosure": DisclosureNodeView(node: node)
        case "tabs": TabsView(node: node)
        case "tab": ChildrenView(node: node)
        case "sheet": SheetView(node: node)
        case "split": SplitView(node: node)
        case "spacer": Spacer(minLength: 0)
        case "divider": Divider()
        default: EmptyView() // `actions`: drawn by its row or message
        }
    }
}

private struct ContentNode: View {
    let node: XbinNode
    @Environment(\.xbinPlacement) private var placement

    var body: some View {
        switch node.type {
        case "text": TextNodeView(node: node)
        case "markdown": MarkdownNodeView(node: node)
        case "image": ImageNodeView(node: node)
        case "icon": IconNodeView(node: node)
        case "badge": Pill(text: node.props.string("text") ?? "", tone: node.props.tone(), pulse: node.props.bool("pulse"),
                           small: placement == .toolbar)
        case "notice": NoticeView(node: node)
        case "progress": ProgressNodeView(node: node)
        case "chart": XbinChart(model: ChartModel(props: node.props))
        case "code": CodeNodeView(node: node)
        case "empty": EmptyNodeView(node: node)
        case "terminal": TerminalNodeView(node: node)
        default: CanvasNodeView(node: node)
        }
    }
}

private struct ControlNode: View {
    let node: XbinNode

    var body: some View {
        switch node.type {
        case "button": ButtonNodeView(node: node)
        case "toggle": ToggleNodeView(node: node)
        case "field": FieldNodeView(node: node)
        case "picker": PickerNodeView(node: node)
        default: MenuNodeView(node: node)
        }
    }
}

private struct ChatNode: View {
    let node: XbinNode

    var body: some View {
        switch node.type {
        case "transcript": TranscriptNodeView(node: node)
        case "message": MessageNodeView(node: node)
        case "thinking": ThinkingNodeView(node: node)
        case "toolcard": ToolCardNodeView(node: node)
        case "approval": ApprovalNodeView(node: node)
        case "question": QuestionNodeView(node: node)
        case "plan": PlanView(plan: ChatPlan(props: node.props))
        case "diff": DiffNodeView(node: node)
        case "activity": ActivityView(activity: ChatActivity(props: node.props))
        case "step": StepView(step: ChatStep(props: node.props))
        default: ComposerNodeView(node: node)
        }
    }
}

/// A primitive this renderer doesn't draw.
struct UnknownNodeView: View {
    let node: XbinNode

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: XbinIcons.UI.unknown)
            Text(verbatim: node.type)
        }
        .font(.footnote)
        .foregroundStyle(XbinColor.muted)
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(XbinColor.border, style: StrokeStyle(lineWidth: 1, dash: [4, 3])))
    }
}
#endif
