import SwiftUI
import UIKit
import XbinAgent
import XbinCore
import XbinRenderer

// A turn's (or a tool call's) changes reviewed a file at a time (D210,
// #45). A turn that rewrote thousands of lines once put every line of its
// patch into one transcript row, always expanded — the Agent tab lagged on
// a phone. Now the transcript holds a folded card ("This turn changed N
// files +a −d"); open, its files; a file opens the review sheet at its
// diff — numbered lines in a lazy list, wrapped or scrolled sideways, the
// first 400 then "Show all", previous/next file at the bottom. A file's
// diff is loaded when it is opened (XbinAgent ReviewLoader: the event's
// own patch, else the full-diff route) and parsed once.

/// What the review sheet opens on: a change set, at a file (nil: its list).
struct ReviewRequest: Identifiable {
    let set: ChangeSet
    let index: Int?
    let id = UUID()
}

/// Where a review's diffs come from: the session's loader, and whether the
/// session still runs (the full-diff route answers only then).
struct ReviewSource {
    let loader: ReviewLoader
    let session: String?
    let live: Bool
}

/// The wrap choice, kept across reviews.
@MainActor
enum ReviewPrefs {
    private static let key = "xbin.agentReview.wrap"
    static var wrap: Bool {
        get { UserDefaults.standard.bool(forKey: key) }
        set { UserDefaults.standard.set(newValue, forKey: key) }
    }
}

/// A turn's changes in the transcript: folded to its counts; open, its files.
struct ChangesCard: View {
    let set: ChangeSet
    let title: String
    let isOpen: Binding<Bool>
    let onOpen: @MainActor (Int?) -> Void

    var body: some View {
        let chips = set.added + set.removed > 0 ? [ChatChip(text: "+\(set.added) −\(set.removed)")] : []
        ToolCardView(card: ChatToolCard(id: set.id, title: "\(title) \(set.count)", icon: "file", family: "edit", chips: chips),
                     isOpen: isOpen, hasContent: !set.files.isEmpty) {
            ChangedFilesList(set: set, onOpen: onOpen)
        }
    }
}

/// A change set's files in a card: the first `limit`, then "All N files".
/// A row opens that file's diff in the review.
struct ChangedFilesList: View {
    let set: ChangeSet
    var limit = 12
    let onOpen: @MainActor (Int?) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(set.files.prefix(limit).enumerated()), id: \.offset) { i, f in
                Button { onOpen(i) } label: { ChangedFileRow(file: f).contentShape(Rectangle()) }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("review-file:\(f.path)")
            }
            if set.files.count > limit {
                Button("All \(set.files.count) files") { onOpen(nil) }
                    .font(.footnote)
                    .padding(.top, 6)
                    .accessibilityIdentifier("review-all-files")
            }
        }
    }
}

/// A file of a change set: its status letter, its path, its counts.
struct ChangedFileRow: View {
    let file: ReviewFile
    /// The name above its folder (the review's list), else the path on one line.
    var twoLines = false

    var body: some View {
        HStack(spacing: 8) {
            Text(verbatim: file.letter)
                .font(.caption.weight(.bold).monospaced())
                .foregroundStyle(Self.letterColor(file.letter))
                .frame(width: 16)
            if twoLines {
                VStack(alignment: .leading, spacing: 1) {
                    Text(verbatim: file.name).font(.callout.monospaced()).foregroundStyle(XbinColor.text)
                        .lineLimit(1).truncationMode(.middle)
                    if !file.folder.isEmpty {
                        Text(verbatim: file.folder).font(.caption).foregroundStyle(XbinColor.muted)
                            .lineLimit(1).truncationMode(.head)
                    }
                }
            } else {
                Text(verbatim: file.oldPath.map { "\($0) → \(file.path)" } ?? file.path)
                    .font(.footnote.monospaced()).foregroundStyle(XbinColor.text)
                    .lineLimit(1).truncationMode(.middle)
            }
            Spacer(minLength: 4)
            ReviewCounts(added: file.added, removed: file.removed, binary: file.binary)
            Image(systemName: XbinIcons.UI.chevronForward)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(XbinColor.muted)
        }
        .padding(.vertical, twoLines ? 2 : 5)
        .accessibilityElement(children: .combine)
    }

    /// The web's status colours: added ok, deleted danger, modified and
    /// renamed the warn tone (agent-cards.js .fs), the rest muted.
    static func letterColor(_ l: String) -> Color {
        switch l {
        case "A": return XbinColor.toneText(.ok)
        case "D": return XbinColor.toneText(.danger)
        case "M", "R": return XbinColor.toneText(.warn)
        default: return XbinColor.muted
        }
    }
}

/// "+a −d" in the diff colours ("binary" for a binary file).
struct ReviewCounts: View {
    let added: Int?
    let removed: Int?
    var binary = false

    var body: some View {
        HStack(spacing: 4) {
            if binary {
                Text("binary").foregroundStyle(XbinColor.muted)
            } else {
                if let a = added { Text(verbatim: "+\(a)").foregroundStyle(XbinColor.toneText(.ok)) }
                if let d = removed { Text(verbatim: "−\(d)").foregroundStyle(XbinColor.toneText(.danger)) }
            }
        }
        .font(.caption.monospacedDigit())
    }
}

/// The review: the change set's files, and a file's diff pushed on them
/// (opened right at the file when one was tapped).
struct ChangeReviewSheet: View {
    let request: ReviewRequest
    let source: ReviewSource
    @State private var path: [Int]
    @Environment(\.dismiss) private var dismiss

    init(request: ReviewRequest, source: ReviewSource) {
        self.request = request
        self.source = source
        _path = State(initialValue: request.index.map { [$0] } ?? [])
    }

    var body: some View {
        NavigationStack(path: $path) {
            ChangedFilesScreen(set: request.set, onOpen: { path = [$0] }, onDone: { dismiss() })
                .navigationDestination(for: Int.self) { i in
                    FileReviewScreen(set: request.set, source: source, index: i, onDone: { dismiss() })
                }
        }
    }
}

/// Every file of a change set, a lazy list.
struct ChangedFilesScreen: View {
    let set: ChangeSet
    let onOpen: @MainActor (Int) -> Void
    let onDone: @MainActor () -> Void

    var body: some View {
        List {
            Section {
                ForEach(Array(set.files.enumerated()), id: \.offset) { i, f in
                    Button { onOpen(i) } label: { ChangedFileRow(file: f, twoLines: true).contentShape(Rectangle()) }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("review-list:\(f.path)")
                }
            } footer: {
                if set.added + set.removed > 0 { ReviewCounts(added: set.added, removed: set.removed) }
            }
        }
        .navigationTitle("\(set.count) changed")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { onDone() } } }
    }
}

/// One file's diff, full screen: its path and counts on top, the lines, and
/// the previous and next file at the bottom.
struct FileReviewScreen: View {
    let set: ChangeSet
    let source: ReviewSource
    @State var index: Int
    let onDone: @MainActor () -> Void
    @State private var wrap = ReviewPrefs.wrap

    var body: some View {
        let file = set.files[min(max(index, 0), set.files.count - 1)]
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Text(verbatim: file.letter)
                    .font(.caption.weight(.bold).monospaced())
                    .foregroundStyle(ChangedFileRow.letterColor(file.letter))
                Text(verbatim: file.oldPath.map { "\($0) → \(file.path)" } ?? file.path)
                    .font(.caption.monospaced())
                    .foregroundStyle(XbinColor.muted)
                    .lineLimit(1).truncationMode(.head)
                    .textSelection(.enabled)
                Spacer(minLength: 4)
                ReviewCounts(added: file.added, removed: file.removed, binary: file.binary)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .background(XbinColor.surface2)
            Divider()
            FileDiffPage(set: set, file: file, source: source, wrap: wrap)
                .id(file.path) // a page (and its load) per file
        }
        .background(XbinColor.surface)
        .onChange(of: wrap) { _, on in ReviewPrefs.wrap = on }
        .navigationTitle(file.name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) { Button("Done") { onDone() } }
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Toggle(isOn: $wrap) {
                        Label("Wrap lines", systemImage: "arrow.turn.down.left")
                    }
                    Button("Copy path", systemImage: "doc.on.doc") { UIPasteboard.general.string = file.path }
                } label: { Image(systemName: "ellipsis.circle") }
                .accessibilityIdentifier("review-menu")
            }
            ToolbarItemGroup(placement: .bottomBar) {
                Button { index -= 1 } label: { Label("Previous file", systemImage: "chevron.up") }
                    .disabled(index <= 0)
                    .accessibilityIdentifier("review-prev")
                Spacer()
                Text(verbatim: "\(index + 1) of \(set.files.count)")
                    .font(.footnote.monospacedDigit())
                    .foregroundStyle(XbinColor.muted)
                    .accessibilityIdentifier("review-position")
                Spacer()
                Button { index += 1 } label: { Label("Next file", systemImage: "chevron.down") }
                    .disabled(index >= set.files.count - 1)
                    .accessibilityIdentifier("review-next")
            }
        }
    }
}

/// A file's diff: loaded (once: the loader keeps it) when shown.
struct FileDiffPage: View {
    let set: ChangeSet
    let file: ReviewFile
    let source: ReviewSource
    let wrap: Bool
    @State private var review: FileReview?
    @State private var all = false

    var body: some View {
        Group {
            if let r = review {
                if let empty = r.emptyText {
                    Text(verbatim: empty)
                        .font(.callout)
                        .foregroundStyle(XbinColor.muted)
                        .multilineTextAlignment(.center)
                        .padding(24)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .accessibilityIdentifier("review-empty")
                } else {
                    ReviewLinesView(review: r, wrap: wrap, all: $all)
                }
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task {
            let (loader, s, f, session, live) = (source.loader, set, file, source.session, source.live)
            review = await loader.review(s, file: f, session: session, live: live)
        }
    }
}

/// A diff's lines, drawn lazily: numbered, the change tinted, wrapped or
/// scrolling sideways; the first FileReview.shownLines until "Show all".
struct ReviewLinesView: View {
    let review: FileReview
    let wrap: Bool
    @Binding var all: Bool

    var body: some View {
        let shown = all ? review.lines[...] : review.lines.prefix(FileReview.shownLines)
        let hidden = review.lines.count - shown.count
        GeometryReader { geo in
            ScrollView(wrap ? [.vertical] : [.vertical, .horizontal]) {
                LazyVStack(alignment: .leading, spacing: 0) {
                    ForEach(shown) { l in
                        ReviewLineRow(line: l, digits: review.numberDigits, wrap: wrap, width: geo.size.width)
                    }
                    VStack(alignment: .leading, spacing: 8) {
                        if hidden > 0 {
                            Button("Show all \(review.lines.count) lines") { all = true }
                                .font(.footnote)
                                .accessibilityIdentifier("review-show-all")
                        }
                        if review.cut {
                            Text("The rest of this file's diff is too large to keep")
                                .font(.footnote)
                                .foregroundStyle(XbinColor.muted)
                        }
                    }
                    .padding(12)
                    .frame(width: geo.size.width, alignment: .leading)
                }
                .textSelection(.enabled)
            }
        }
        .accessibilityIdentifier("review-diff")
    }
}

/// One line: its number, its +/− marker, its text on the change's tint.
struct ReviewLineRow: View {
    let line: ReviewLine
    let digits: Int
    let wrap: Bool
    /// The viewport's width: a tint spans it at least.
    let width: CGFloat

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            if line.kind != .hunk {
                // never squeezed: a line's text, sized to itself, takes what is left
                Text(verbatim: line.number.map { String($0).leftPad(digits) } ?? String(repeating: " ", count: digits))
                    .foregroundStyle(XbinColor.subtle)
                    .fixedSize()
                Text(verbatim: marker).foregroundStyle(markerColor).fixedSize()
            }
            text
        }
        .font(.footnote.monospaced())
        .padding(.horizontal, 8)
        .padding(.vertical, line.kind == .hunk ? 4 : 0)
        .frame(minWidth: wrap ? nil : width, maxWidth: wrap ? .infinity : nil, alignment: .leading)
        .background(background)
    }

    @ViewBuilder private var text: some View {
        let t = Text(verbatim: line.text.isEmpty ? " " : line.text).foregroundStyle(foreground)
        if wrap { t.fixedSize(horizontal: false, vertical: true) } else { t.fixedSize() }
    }

    private var marker: String {
        switch line.kind {
        case .added: return "+"
        case .removed: return "−"
        default: return " "
        }
    }

    private var markerColor: Color {
        switch line.kind {
        case .added: return XbinColor.toneText(.ok)
        case .removed: return XbinColor.toneText(.danger)
        default: return XbinColor.subtle
        }
    }

    private var foreground: Color {
        switch line.kind {
        case .hunk, .note, .meta: return XbinColor.muted
        default: return XbinColor.text
        }
    }

    private var background: Color {
        switch line.kind {
        case .added: return XbinColor.okBackground
        case .removed: return XbinColor.dangerBackground
        case .hunk: return XbinColor.infoBackground
        default: return .clear
        }
    }
}
