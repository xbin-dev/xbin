#if canImport(UIKit)
import SwiftUI
import UIKit
import XbinCore
import XbinRendererModel

// Content primitives (plans/native.md §8.2) and the escape hatches (§8.5).
// Markdown and charts have files of their own.

/// `text`: always verbatim (`Text(verbatim:)` — never markup, §8.2), in a
/// type role and tone. Unless selectable, a long identifier in it wraps at
/// characters instead of being hyphenated (``WrappingText``).
struct TextNodeView: View {
    let node: XbinNode

    var body: some View {
        let p = node.props
        let role = XbinTypeRole(p.string("style")) ?? .body
        let lines = p.number("lines").map { Int(max(0, $0)) } ?? 0
        let mono = p.bool("mono") || role == .mono
        if p.bool("selectable") {
            Text(verbatim: p.string("text") ?? "")
                .font(XbinFont.font(role))
                .monospaced(p.bool("mono"))
                .foregroundStyle(XbinColor.text(p.tone()))
                .lineLimit(lines > 0 ? lines : nil)
                .textSelection(.enabled)
        } else {
            WrappingText(text: p.string("text") ?? "", mono: mono)
                .font(XbinFont.font(role))
                .monospaced(p.bool("mono"))
                .foregroundStyle(XbinColor.text(p.tone()))
                .lineLimit(lines > 0 ? lines : nil)
        }
    }
}

/// `icon`: a symbol in a square box that scales with the text size, so a
/// row of icons with captions lines up (SF Symbols differ in height: `minus`
/// is a sliver, `trash` is tall), like the reference's fixed icon box.
struct IconNodeView: View {
    let node: XbinNode
    @ScaledMetric(relativeTo: .body) private var box: CGFloat = 24

    var body: some View {
        XbinIconImage(name: node.props.string("name"), tone: node.props.tone())
            .font(.body)
            .frame(width: box, height: box)
    }
}

/// `notice`: an inset banner in a tone (`info` is neutral).
struct NoticeView: View {
    let node: XbinNode
    @Environment(\.xbinPlacement) private var placement

    var body: some View {
        let p = node.props
        let tone = XbinNoticeTone(p.string("tone")) ?? .info
        let color = tone.tone.map(XbinColor.tone) ?? XbinColor.muted
        let content = HStack(alignment: .top, spacing: 10) {
            Image(systemName: tone.symbol).foregroundStyle(color).font(.body)
            VStack(alignment: .leading, spacing: 2) {
                if let t = p.nonEmpty("title") { Text(verbatim: t).font(.subheadline.weight(.semibold)) }
                if let x = p.nonEmpty("text") { Text(verbatim: x).font(.subheadline) }
            }
            .foregroundStyle(XbinColor.text)
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
        let fill = tone == .info ? XbinColor.fill : color.opacity(0.12)
        if placement == .list {
            content.listRowBackground(fill)
        } else {
            content
                .padding(12)
                .background(fill, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
    }
}

/// `progress`: a bar with its label and percentage, or a spinner when
/// `value` is absent.
struct ProgressNodeView: View {
    let node: XbinNode

    var body: some View {
        let p = node.props
        let label = p.nonEmpty("label")
        if let v = p.number("value") {
            let clamped = min(1, max(0, v))
            VStack(alignment: .leading, spacing: 6) {
                if let label {
                    HStack(alignment: .firstTextBaseline) {
                        Text(verbatim: label).font(.subheadline)
                        Spacer(minLength: 8)
                        Text(verbatim: "\(Int((clamped * 100).rounded()))%")
                            .font(.subheadline)
                            .monospacedDigit()
                            .foregroundStyle(XbinColor.muted)
                    }
                }
                ProgressView(value: clamped)
            }
        } else {
            HStack(spacing: 8) {
                ProgressView()
                if let label { Text(verbatim: label).font(.subheadline).foregroundStyle(XbinColor.muted) }
            }
        }
    }
}

/// `code`: monospaced text, scrolling sideways unless `wrap`, with a copy
/// button when `copy`.
struct CodeNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let p = node.props
        let copy: (@MainActor (String) -> Void)? = p.bool("copy") ? cx?.services.copy : nil
        CodeBlock(text: p.string("text") ?? "", wrap: p.bool("wrap"), onCopy: copy)
    }
}

/// A block of code: horizontally scrolling (or wrapped) monospaced text on a
/// neutral fill, with an optional copy button beside it (never over the
/// text) and an optional language label above it (markdown's fence info).
/// Reused by markdown code blocks and the agent screen's tool output.
public struct CodeBlock: View {
    public let text: String
    public var wrap: Bool
    public var language: String?
    public var onCopy: (@MainActor (String) -> Void)?
    @State private var copied = false

    public init(text: String, wrap: Bool = false, language: String? = nil, onCopy: (@MainActor (String) -> Void)? = nil) {
        self.text = text
        self.wrap = wrap
        self.language = language
        self.onCopy = onCopy
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let language, !language.isEmpty {
                Text(verbatim: language)
                    .font(.caption2)
                    .foregroundStyle(XbinColor.muted)
                    .padding(.horizontal, 12)
                    .padding(.top, 8)
                    .padding(.bottom, -4)
                    .accessibilityHidden(true)
            }
            HStack(alignment: .top, spacing: 0) {
                Group {
                    if wrap {
                        Text(verbatim: text)
                            .font(XbinFont.code)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(12)
                    } else {
                        ScrollView(.horizontal, showsIndicators: false) {
                            Text(verbatim: text)
                                .font(XbinFont.code)
                                .fixedSize(horizontal: true, vertical: false)
                                .padding(12)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .textSelection(.enabled)
                if let onCopy {
                    Button {
                        onCopy(text)
                        copied = true
                        Task { @MainActor in
                            try? await Task.sleep(for: .seconds(1.2))
                            copied = false
                        }
                    } label: {
                        Image(systemName: copied ? XbinIcons.UI.checkmark : "doc.on.doc")
                            .font(.footnote)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 10)
                    }
                    .buttonStyle(.borderless)
                    .accessibilityLabel(copied ? "Copied" : "Copy")
                }
            }
        }
        .background(XbinColor.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }
}

/// `empty`: a `ContentUnavailableView`, compact inside a list or card.
struct EmptyNodeView: View {
    let node: XbinNode
    @Environment(\.xbinPlacement) private var placement

    var body: some View {
        let p = node.props
        let symbol = XbinIcons.symbol(p.string("icon"))
        let title = p.string("title") ?? ""
        let text = p.nonEmpty("text")
        if placement == .list || placement == .card {
            VStack(spacing: 4) {
                if let symbol { Image(systemName: symbol).font(.title3).foregroundStyle(XbinColor.muted) }
                if !title.isEmpty { Text(verbatim: title).font(.subheadline.weight(.semibold)) }
                if let text { Text(verbatim: text).font(.footnote).foregroundStyle(XbinColor.muted) }
            }
            .multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 8)
        } else {
            ContentUnavailableView {
                Label {
                    Text(verbatim: title)
                } icon: {
                    if let symbol { Image(systemName: symbol) }
                }
            } description: {
                if let text { Text(verbatim: text) }
            }
        }
    }
}

/// `image`: loaded by the app with the frame token (``XbinServices/imageData``)
/// or decoded from a `data:` source; `preview` opens it in Quick Look.
struct ImageNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinImages) private var images
    @State private var loaded: XbinImages.Loaded?
    @State private var preview: URL?

    var body: some View {
        let p = node.props
        let src = p.string("src") ?? ""
        let fill = p.string("aspect") == "fill"
        let height = XbinHeight(p.string("height")).map { CGFloat($0.points) }
        let alt = p.string("alt") ?? ""
        let previews = p.bool("preview") && loaded != nil
        let tappable = previews || node.listens(to: "tap")
        ImageBox(image: loaded?.image, fill: fill, height: height)
            .contentShape(Rectangle())
            .onTapGesture {
                guard tappable else { return }
                if previews, let loaded { preview = QuickLookFile.write(loaded.data, name: alt) }
                if node.listens(to: "tap") { cx?.emit(node, "tap") }
            }
            .accessibilityElement()
            .accessibilityLabel(Text(verbatim: alt))
            .accessibilityAddTraits(tappable ? [.isImage, .isButton] : .isImage)
            .task(id: src) { loaded = await XbinImages.load(src, with: images) }
            .modifier(QuickLookModifier(url: $preview))
    }
}

/// An image (or its placeholder) at a height, filled or fitted.
private struct ImageBox: View {
    let image: UIImage?
    let fill: Bool
    let height: CGFloat?

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: 10, style: .continuous)
        if let image {
            if fill {
                Color.clear
                    .frame(maxWidth: .infinity)
                    .frame(height: height ?? 160)
                    .overlay { Image(uiImage: image).resizable().scaledToFill() }
                    .clipShape(shape)
            } else {
                Image(uiImage: image)
                    .resizable()
                    .scaledToFit()
                    .frame(maxWidth: .infinity, maxHeight: height)
                    .frame(height: height)
                    .clipShape(shape)
            }
        } else {
            shape
                .fill(XbinColor.fill)
                .frame(maxWidth: .infinity)
                .frame(height: height ?? 160)
                .overlay { Image(systemName: XbinIcons.UI.image).foregroundStyle(XbinColor.muted) }
        }
    }
}

/// `terminal`: the app's terminal view for the tile's pty endpoint
/// (``XbinServices/terminal``); a placeholder without one.
struct TerminalNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let p = node.props
        let request = XbinTerminalRequest(key: node.key, src: p.string("src") ?? "", title: p.nonEmpty("title"))
        if let make = cx?.services.terminal {
            make(request).frame(minHeight: 240)
        } else {
            VStack(alignment: .leading, spacing: 0) {
                HStack(spacing: 6) {
                    Image(systemName: XbinIcons.UI.terminal)
                    Text(verbatim: request.title ?? "terminal")
                }
                .font(.caption)
                .foregroundStyle(Color.white.opacity(0.7))
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                Text(verbatim: "# \(request.src.isEmpty ? "pty" : request.src)\n$ ▍")
                    .font(XbinFont.code)
                    .foregroundStyle(Color.white.opacity(0.85))
                    .padding(.horizontal, 12)
                    .padding(.bottom, 12)
                Spacer(minLength: 0)
            }
            .frame(maxWidth: .infinity, minHeight: 160, alignment: .topLeading)
            .background(Color(white: 0.1), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            .accessibilityElement(children: .combine)
        }
    }
}

/// `canvas`: the app's WebView island (``XbinServices/canvas``); a
/// placeholder without one.
struct CanvasNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let p = node.props
        let height = XbinHeight(p.string("height"))?.points ?? 160
        let request = XbinCanvasRequest(key: node.key, src: p.nonEmpty("src"), html: p.string("html"), height: height)
        if let make = cx?.services.canvas {
            make(request).frame(height: CGFloat(height))
        } else {
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .strokeBorder(XbinColor.border, style: StrokeStyle(lineWidth: 1, dash: [5, 4]))
                .frame(maxWidth: .infinity)
                .frame(height: CGFloat(height))
                .overlay {
                    Label(request.src ?? "canvas", systemImage: XbinIcons.UI.canvas)
                        .font(.footnote)
                        .foregroundStyle(XbinColor.muted)
                }
        }
    }
}
#endif
