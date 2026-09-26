#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// Markdown tokens (tree.md §11) drawn natively: headings, paragraphs of
/// styled runs (built from the tokens' text, never parsed as markup), lists
/// with task boxes, horizontally scrolling code, quotes, tables as a
/// `Grid`, rules. A tapped link goes to `onLink` (nothing opens by itself).
/// Shared by the `markdown` primitive, markdown messages and the agent
/// screen.
public struct MarkdownView: View {
    public let blocks: [MarkdownBlock]
    public var onLink: (@MainActor (URL) -> Void)?

    public init(blocks: [MarkdownBlock], onLink: (@MainActor (URL) -> Void)? = nil) {
        self.blocks = blocks
        self.onLink = onLink
    }

    public var body: some View {
        let handler = onLink
        MarkdownBlocks(blocks: blocks, spacing: 10)
            .environment(\.openURL, OpenURLAction { url in
                MainActor.assumeIsolated { handler?(url) }
                return .handled
            })
    }
}

/// The styled text of inline tokens at a base font.
enum MarkdownText {
    static func attributed(_ inlines: [MarkdownInline], font: Font) -> AttributedString {
        var out = AttributedString()
        for run in Markdown.runs(inlines) {
            var a = AttributedString(run.text)
            var f = run.code ? font.monospaced() : font
            if run.strong { f = f.bold() }
            if run.emphasis { f = f.italic() }
            a[AttributeScopes.SwiftUIAttributes.FontAttribute.self] = f
            if run.strikethrough {
                a[AttributeScopes.SwiftUIAttributes.StrikethroughStyleAttribute.self] = .single
            }
            if run.code {
                a[AttributeScopes.SwiftUIAttributes.BackgroundColorAttribute.self] = XbinColor.fill
            }
            if let href = run.link, let url = URL(string: href) {
                a[AttributeScopes.FoundationAttributes.LinkAttribute.self] = url
                a[AttributeScopes.SwiftUIAttributes.ForegroundColorAttribute.self] = XbinColor.accentText
                a[AttributeScopes.SwiftUIAttributes.UnderlineStyleAttribute.self] = .single
            }
            out.append(a)
        }
        return out
    }

    static func heading(_ level: Int) -> Font {
        switch level {
        case 1: return Font.title2.bold()
        case 2: return Font.title3.bold()
        case 3: return Font.headline
        case 4: return Font.subheadline.bold()
        default: return Font.footnote.bold()
        }
    }
}

/// Blocks in a column.
struct MarkdownBlocks: View {
    let blocks: [MarkdownBlock]
    var spacing: CGFloat = 10

    var body: some View {
        VStack(alignment: .leading, spacing: spacing) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                MarkdownBlockView(block: block)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// One block.
struct MarkdownBlockView: View {
    let block: MarkdownBlock

    var body: some View {
        switch block {
        case .heading(let level, let inlines):
            Text(MarkdownText.attributed(inlines, font: MarkdownText.heading(level)))
                .foregroundStyle(level >= 6 ? XbinColor.muted : XbinColor.text)
                .padding(.top, level <= 2 ? 6 : 2)
                .accessibilityAddTraits(.isHeader)
        case .paragraph(let inlines):
            Text(MarkdownText.attributed(inlines, font: .body))
                .fixedSize(horizontal: false, vertical: true)
        case .list(let list):
            MarkdownListView(list: list)
        case .code(let text, let language):
            CodeBlock(text: text, language: language)
        case .quote(let blocks):
            HStack(alignment: .top, spacing: 10) {
                RoundedRectangle(cornerRadius: 1.5).fill(XbinColor.border).frame(width: 3)
                MarkdownBlocks(blocks: blocks, spacing: 8).foregroundStyle(XbinColor.muted)
            }
            .fixedSize(horizontal: false, vertical: true)
        case .table(let table):
            MarkdownTableView(table: table)
        case .rule:
            Divider().padding(.vertical, 4)
        }
    }
}

private struct MarkdownListView: View {
    let list: MarkdownList

    var body: some View {
        VStack(alignment: .leading, spacing: list.loose ? 8 : 4) {
            ForEach(Array(list.items.enumerated()), id: \.offset) { i, item in
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    if item.task {
                        Image(systemName: item.checked ? "checkmark.square.fill" : "square")
                            .foregroundStyle(item.checked ? XbinColor.accentText : XbinColor.muted)
                            .accessibilityLabel(item.checked ? "done" : "to do")
                    } else {
                        Text(verbatim: list.marker(i))
                            .monospacedDigit()
                            .foregroundStyle(XbinColor.muted)
                    }
                    MarkdownBlocks(blocks: item.blocks, spacing: list.loose ? 8 : 4)
                }
            }
        }
    }
}

private struct MarkdownTableView: View {
    let table: MarkdownTable

    var body: some View {
        let columns = table.columns
        ScrollView(.horizontal, showsIndicators: false) {
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 8) {
                GridRow {
                    ForEach(0..<columns, id: \.self) { c in
                        cell(c < table.header.count ? table.header[c] : [], bold: true)
                            .gridColumnAlignment(Self.alignment(table.alignment(c)))
                    }
                }
                Divider()
                ForEach(Array(table.rows.enumerated()), id: \.offset) { _, row in
                    GridRow {
                        ForEach(0..<columns, id: \.self) { c in
                            cell(c < row.count ? row[c] : [], bold: false)
                        }
                    }
                }
            }
            .padding(12)
        }
        .background(XbinColor.fill.opacity(0.5), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private func cell(_ inlines: [MarkdownInline], bold: Bool) -> Text {
        Text(MarkdownText.attributed(inlines, font: bold ? Font.subheadline.bold() : .subheadline))
    }

    static func alignment(_ a: MarkdownAlignment?) -> HorizontalAlignment {
        switch a {
        case .center?: return .center
        case .right?: return .trailing
        default: return .leading
        }
    }
}

/// `markdown`: the runtime's tokens; `link` events for tapped links.
struct MarkdownNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let context = cx
        let n = node
        MarkdownView(blocks: Markdown.blocks(props: node.props)) { url in context?.link(url, in: n) }
    }
}
#endif
