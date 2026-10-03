#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

// The chat family (plans/native.md §8.4): public components with plain view
// models, drawn the same for a native tile's tree (ChatNodes.swift) and the
// app's own ACP agent screen (§13).

/// A chat transcript: a lazy column that starts at (and, with `follow`,
/// sticks to) the bottom. It holds what the reader looks at in place (D130):
/// the rows are scroll targets identified by `ID` (the content's ForEach
/// ids) and the scroll position follows the row at the top, so rows loaded
/// above (`older` + `onMore`, asked for at rest within a screen and a half
/// of the top, again every half second while it stays so) or unloaded above
/// or below never move it; only at the bottom does it stick to the bottom
/// as content grows. `newer` + `onNewer` is the same below (rows the host
/// unloaded). `onScrolled` reports whether the reader is at the bottom (on
/// every change), `onVisible` the ids on screen once the list is at rest
/// (for the host to unload far rows). Away from the bottom,
/// `fresh` new rows (or rows unloaded below) show a "↓ N new — jump to
/// latest" pill: it scrolls to the bottom and calls `onJump` (for the host
/// to read the tail again when it had unloaded it). `nested` (a subagent
/// inside a tool card) doesn't scroll itself.
public struct TranscriptView<ID: Hashable & Sendable, Content: View>: View {
    public var follow: Bool
    public var older: Bool
    public var newer: Bool
    public var nested: Bool
    public var fresh: Int
    public var onMore: (@MainActor () -> Void)?
    public var onNewer: (@MainActor () -> Void)?
    public var onScrolled: (@MainActor (Bool) -> Void)?
    public var onVisible: (@MainActor ([ID]) -> Void)?
    public var onJump: (@MainActor () -> Void)?
    let content: Content

    @State private var position: ScrollPosition
    @State private var atBottom = true
    /// The pill was tapped while rows below were unloaded: scroll to the
    /// bottom again once the host brought them back.
    @State private var jumping = false
    /// The scroll view is at rest (no finger, no deceleration).
    @State private var idle = true
    /// The ids last on screen, reported when the scroll view comes to rest.
    @State private var seen: [ID] = []
    /// The reader is near the top or the bottom of what is loaded.
    @State private var near = Near()

    struct Near: Equatable {
        var top = false
        var bottom = false
    }

    public init(follow: Bool = true, older: Bool = false, newer: Bool = false, nested: Bool = false, fresh: Int = 0,
                idType: ID.Type,
                onMore: (@MainActor () -> Void)? = nil, onNewer: (@MainActor () -> Void)? = nil,
                onScrolled: (@MainActor (Bool) -> Void)? = nil, onVisible: (@MainActor ([ID]) -> Void)? = nil,
                onJump: (@MainActor () -> Void)? = nil,
                @ViewBuilder content: () -> Content) {
        self.follow = follow
        self.older = older
        self.newer = newer
        self.nested = nested
        self.fresh = fresh
        self.onMore = onMore
        self.onNewer = onNewer
        self.onScrolled = onScrolled
        self.onVisible = onVisible
        self.onJump = onJump
        self.content = content()
        // No edge: defaultScrollAnchor places it (an edge-based position
        // re-anchored the first drag by a row's worth — seen on iOS 27).
        _position = State(initialValue: ScrollPosition(idType: ID.self))
    }

    public var body: some View {
        if nested {
            VStack(alignment: .leading, spacing: 10) { content }
                .frame(maxWidth: .infinity, alignment: .leading)
        } else {
            let scrolled = onScrolled, visible = onVisible, more = onMore, newerRows = onNewer
            // Load at rest and early: within a screen and a half of either
            // end, so a page lands out of sight and the rows the reader then
            // scrolls into are laid out already (a page scrolled into during
            // the same drag it landed in shifted it — seen on iOS 27).
            let loadTop = older && more != nil && near.top && idle
            let loadBottom = newer && newerRows != nil && near.bottom && idle
            ScrollView {
                // The more rows are not scroll targets: the position always
                // names a row of the content.
                VStack(alignment: .leading, spacing: 14) {
                    if older, onMore != nil { MoreRow(label: "Loading earlier messages") }
                    LazyVStack(alignment: .leading, spacing: 14) { content }
                        .scrollTargetLayout()
                    if newer, onNewer != nil { MoreRow(label: "Loading later messages") }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
            }
            .scrollPosition($position, anchor: .top)
            .defaultScrollAnchor(follow ? .bottom : .top)
            .defaultScrollAnchor(follow && atBottom ? .bottom : nil, for: .sizeChanges)
            .onScrollGeometryChange(for: Bool.self) { geo in
                geo.contentOffset.y + geo.containerSize.height >= geo.contentSize.height - 32
            } action: { _, bottom in
                atBottom = bottom
                scrolled?(bottom)
            }
            .onScrollGeometryChange(for: Near.self) { geo in
                let reach = geo.containerSize.height * 1.5
                return Near(top: geo.contentOffset.y < reach,
                            bottom: geo.contentSize.height - geo.contentOffset.y - geo.containerSize.height < reach)
            } action: { _, n in
                near = n
            }
            // asks again every half second while it stays so (the host
            // ignores a request while one runs; a short page asks for the next)
            .task(id: Near(top: loadTop, bottom: loadBottom)) {
                while !Task.isCancelled, loadTop || loadBottom {
                    if loadTop { more?() }
                    if loadBottom { newerRows?() }
                    try? await Task.sleep(for: .milliseconds(500))
                }
            }
            .onScrollTargetVisibilityChange(idType: ID.self, threshold: 0.01) { ids in
                seen = ids
                if idle { visible?(ids) }
            }
            .onScrollPhaseChange { _, phase in
                idle = phase == .idle
                if idle { visible?(seen) } // unloading waits for rest too
            }
            .onChange(of: newer) { _, more in
                if jumping, !more {
                    jumping = false
                    position.scrollTo(edge: .bottom)
                }
            }
            .overlay(alignment: .bottom) {
                if follow, !atBottom, fresh > 0 || newer {
                    JumpPill(fresh: fresh) {
                        jumping = newer
                        position.scrollTo(edge: .bottom)
                        onJump?()
                    }
                    .padding(.bottom, 10)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .animation(.snappy, value: follow && !atBottom && (fresh > 0 || newer))
        }
    }
}

extension TranscriptView where ID == String {
    /// A transcript whose rows are identified by strings (or that has none to anchor on).
    public init(follow: Bool = true, older: Bool = false, nested: Bool = false,
                onMore: (@MainActor () -> Void)? = nil, onScrolled: (@MainActor (Bool) -> Void)? = nil,
                @ViewBuilder content: () -> Content) {
        self.init(follow: follow, older: older, nested: nested, idType: String.self, onMore: onMore, onScrolled: onScrolled,
                  content: content)
    }
}

/// Where more rows will be (the transcript asks for them near either end,
/// at rest — never mid-gesture: rows landing under a moving finger or a
/// deceleration are what makes a list jump; the web waits for scroll idle
/// on touch too, D124).
struct MoreRow: View {
    let label: String

    var body: some View {
        ProgressView()
            .frame(maxWidth: .infinity)
            .padding(.vertical, 4)
            .accessibilityLabel(label)
            .accessibilityIdentifier("transcript-more")
    }
}

/// "↓ N new — jump to latest".
struct JumpPill: View {
    let fresh: Int
    let action: @MainActor () -> Void

    var body: some View {
        Button(action: action) {
            Label(fresh > 0 ? "\(fresh) new — jump to latest" : "Jump to latest", systemImage: "arrow.down")
                .font(.footnote.weight(.semibold))
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(.bar, in: Capsule())
                .overlay(Capsule().strokeBorder(XbinColor.border))
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("transcript-jump")
    }
}

/// One chat message: the user's turns as trailing bubbles, the assistant's
/// full width (markdown when it has blocks), system notes centred; sender,
/// time, files (image thumbnails load through the environment's
/// ``XbinImages`` — `data:` ones without it — and open in Quick Look;
/// other files are chips), a queued marker and an optional actions view
/// under the bubble (a native tile's message: its ⋯ menu).
public struct MessageView<Actions: View>: View {
    public let message: ChatMessage
    public var onLink: (@MainActor (URL) -> Void)?
    public var onTap: (@MainActor () -> Void)?
    let actions: Actions

    public init(message: ChatMessage, onLink: (@MainActor (URL) -> Void)? = nil, onTap: (@MainActor () -> Void)? = nil,
                @ViewBuilder actions: () -> Actions) {
        self.message = message
        self.onLink = onLink
        self.onTap = onTap
        self.actions = actions()
    }

    public var body: some View {
        switch message.role {
        case .system:
            Text(verbatim: message.text)
                .font(.footnote)
                .foregroundStyle(XbinColor.muted)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 4)
        case .user:
            VStack(alignment: .trailing, spacing: 4) {
                meta
                HStack {
                    Spacer(minLength: 48)
                    bubble
                        .padding(.horizontal, 14)
                        .padding(.vertical, 10)
                        .background(XbinColor.bubble, in: RoundedRectangle.xbinPlate)
                        .opacity(message.queued ? 0.6 : 1)
                }
                if message.queued {
                    Label("queued", systemImage: XbinIcons.UI.queued)
                        .font(.caption)
                        .foregroundStyle(XbinColor.muted)
                }
                actions
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .modifier(TapIf(action: onTap))
        case .assistant:
            VStack(alignment: .leading, spacing: 6) {
                meta
                bubble
                actions
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .modifier(TapIf(action: onTap))
        }
    }

    @ViewBuilder
    private var meta: some View {
        if message.sender != nil || message.time != nil {
            HStack(spacing: 6) {
                if let s = message.sender { Text(verbatim: s).fontWeight(.semibold) }
                if let t = message.time { Text(verbatim: t) }
            }
            .font(.caption)
            .foregroundStyle(XbinColor.muted)
        }
    }

    @ViewBuilder
    private var bubble: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let blocks = message.markdown {
                MarkdownView(blocks: blocks, onLink: onLink)
            } else if !message.text.isEmpty || message.files.isEmpty {
                Text(verbatim: message.text)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
            }
            if !message.files.isEmpty {
                // Images with a source as thumbnails (tap: Quick Look),
                // anything else as a chip, in their order.
                FlowLayout(spacing: 6, hug: true) {
                    ForEach(Array(message.files.enumerated()), id: \.offset) { _, f in
                        if f.thumbnailSource != nil {
                            FileThumbnail(file: f)
                        } else {
                            FileChip(file: f)
                        }
                    }
                }
            }
            if message.streaming {
                Image(systemName: "ellipsis")
                    .foregroundStyle(XbinColor.muted)
                    .symbolEffect(.variableColor.iterative)
                    .accessibilityLabel("Writing")
            }
        }
    }
}

/// A tap gesture only when there is something to do (so links and text
/// selection keep working otherwise).
struct TapIf: ViewModifier {
    let action: (@MainActor () -> Void)?

    func body(content: Content) -> some View {
        if let action {
            content.contentShape(Rectangle()).onTapGesture { action() }
        } else {
            content
        }
    }
}

extension MessageView where Actions == EmptyView {
    public init(message: ChatMessage, onLink: (@MainActor (URL) -> Void)? = nil, onTap: (@MainActor () -> Void)? = nil) {
        self.init(message: message, onLink: onLink, onTap: onTap) { EmptyView() }
    }
}

/// A model's reasoning: "Thinking…" (shimmering) while live, "Thought for
/// Ns" after, folded unless opened.
public struct ThinkingView: View {
    public let thinking: ChatThinking
    @Binding public var isOpen: Bool

    public init(thinking: ChatThinking, isOpen: Binding<Bool>) {
        self.thinking = thinking
        _isOpen = isOpen
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button {
                withAnimation(.snappy) { isOpen.toggle() }
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: "sparkles")
                        .symbolEffect(.variableColor.iterative, isActive: thinking.live)
                    Text(verbatim: thinking.label)
                    Image(systemName: XbinIcons.UI.chevronForward)
                        .font(.caption2.weight(.semibold))
                        .rotationEffect(.degrees(isOpen ? 90 : 0))
                }
                .font(.subheadline)
                .foregroundStyle(XbinColor.muted)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityValue(isOpen ? "expanded" : "collapsed")
            if isOpen && !thinking.text.isEmpty {
                Text(verbatim: thinking.text)
                    .font(.subheadline)
                    .foregroundStyle(XbinColor.muted)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.leading, 10)
                    .overlay(alignment: .leading) {
                        Rectangle().fill(XbinColor.border).frame(width: 2)
                    }
            }
        }
    }
}

/// The one "what's happening" line under a running turn.
public struct ActivityView: View {
    public let activity: ChatActivity

    public init(activity: ChatActivity) { self.activity = activity }

    public var body: some View {
        HStack(spacing: 8) {
            if activity.live { ProgressView().controlSize(.small) }
            Text(verbatim: activity.text).font(.footnote).foregroundStyle(XbinColor.muted)
        }
    }
}

/// A step line: a glyph in a tone and a short text (rolled back, retried,
/// compacted …).
public struct StepView: View {
    public let step: ChatStep

    public init(step: ChatStep) { self.step = step }

    public var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(verbatim: step.glyph)
                .font(.footnote.weight(.semibold))
                .foregroundStyle(step.tone.map(XbinColor.toneText) ?? XbinColor.muted)
                .frame(minWidth: 14)
            Text(verbatim: step.text)
                .font(.footnote)
                .foregroundStyle(XbinColor.muted)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}
#endif
