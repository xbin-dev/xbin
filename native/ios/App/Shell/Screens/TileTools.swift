import Observation
import SwiftUI
import XbinAgent
import XbinCore
import XbinRenderer

// The sessions screen's Code, Logs and PRs tools (D132, B3/B4), read
// through the workspace session as the web terminal window's code, logs
// and proposals tabs are (web/bx-code.js, bx-logs.js, bx-prs.js). A file,
// a diff and a proposal open as sheets, so the tabs underneath stay as
// they were (a pushed screen would take their sockets down).

// MARK: - Code

@MainActor
@Observable
final class CodeToolModel {
    let workspace: WorkspaceModel
    let tile: String
    private(set) var tree: CodeTree?
    private(set) var commits: [GitCommit] = []
    private(set) var activity: GitActivity?
    private(set) var loaded = false
    var problem: String?

    init(workspace: WorkspaceModel, tile: String) {
        self.workspace = workspace
        self.tile = tile
    }

    func load() async {
        async let t = get(CodeRoute.tree(tile))
        async let l = get(CodeRoute.log(tile, limit: 50))
        async let a = get(CodeRoute.activity(tile))
        let (tj, lj, aj) = await (t, l, a)
        if let tj { tree = CodeTree(json: tj) }
        if let lj { commits = GitCommit.list(lj) }
        if let aj { activity = GitActivity(json: aj) }
        loaded = true
    }

    private func get(_ path: String) async -> XbinCore.JSONValue? {
        do {
            return try await workspace.auth.json(APIRequest("GET", path))
        } catch {
            if problem == nil { problem = workspace.describe(error) }
            return nil
        }
    }

    func file(_ path: String) async -> Result<CodeFile, ToolProblem> {
        do {
            let j = try await workspace.auth.json(APIRequest("GET", CodeRoute.file(tile, path)))
            guard let f = CodeFile(json: j) else { return .failure(ToolProblem(text: "Unreadable answer")) }
            return .success(f)
        } catch { return .failure(ToolProblem(text: workspace.describe(error))) }
    }

    func diff(_ rev: String) async -> Result<String, ToolProblem> {
        do {
            let j = try await workspace.auth.json(APIRequest("GET", CodeRoute.diff(tile, rev: rev)))
            return .success(j["diff"]?.stringValue ?? "")
        } catch { return .failure(ToolProblem(text: workspace.describe(error))) }
    }
}

/// Why a tool's read failed, in the server's words.
struct ToolProblem: Error, Equatable {
    let text: String
}

/// What a code sheet shows.
enum CodeSheet: Identifiable, Equatable {
    case file(String)
    case diff(rev: String, title: String)
    var id: String {
        switch self {
        case .file(let p): return "f:" + p
        case .diff(let r, _): return "d:" + r
        }
    }
}

struct CodeToolView: View {
    let model: CodeToolModel
    @State private var tab = 0
    @State private var open: Set<String> = []
    @State private var sheet: CodeSheet?

    var body: some View {
        List {
            Picker("", selection: $tab) {
                Text("Files").tag(0)
                Text("History").tag(1)
            }
            .pickerStyle(.segmented)
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets(top: 4, leading: 0, bottom: 4, trailing: 0))
            if let p = model.problem {
                Label { Text(verbatim: p) } icon: { Image(systemName: XbinGlyphs.symbol("error")) }.foregroundStyle(XbinColor.danger)
            }
            if tab == 0 { files } else { history }
        }
        .listStyle(.insetGrouped)
        .overlay { if !model.loaded { ProgressView() } }
        .task { if !model.loaded { await model.load() } }
        .refreshable { await model.load() }
        .sheet(item: $sheet) { s in
            CodeSheetView(model: model, sheet: s).presentationDetents([.large])
        }
    }

    @ViewBuilder private var files: some View {
        if let t = model.tree {
            Section {
                folderRows(t.root, depth: 0)
            } header: {
                Text(verbatim: "\(model.tile) · \(t.fileCount) file\(t.fileCount == 1 ? "" : "s")")
            }
        }
    }

    private func folderRows(_ f: CodeTree.Folder, depth: Int) -> AnyView {
        AnyView(Group {
            ForEach(f.folders) { sub in
                Button {
                    if open.contains(sub.path) { open.remove(sub.path) } else { open.insert(sub.path) }
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: open.contains(sub.path) ? "chevron.down" : "chevron.right").font(.caption2.weight(.bold))
                            .foregroundStyle(.secondary).frame(width: 12)
                        Image(systemName: "folder").foregroundStyle(XbinColor.muted)
                        Text(verbatim: sub.name).font(.callout.monospaced())
                        Spacer(minLength: 4)
                        Text(verbatim: "\(sub.count)").font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                    }
                    .padding(.leading, CGFloat(depth) * 14)
                }
                .tint(.primary)
                .accessibilityIdentifier("code-folder:\(sub.path)")
                if open.contains(sub.path) { folderRows(sub, depth: depth + 1) }
            }
            ForEach(f.files) { file in
                Button { sheet = .file(file.path) } label: {
                    HStack(spacing: 6) {
                        Color.clear.frame(width: 12)
                        Image(systemName: "doc.text").foregroundStyle(.secondary)
                        Text(verbatim: file.name).font(.callout.monospaced()).lineLimit(1)
                        Spacer(minLength: 4)
                        Text(verbatim: Self.size(file.size)).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                    }
                    .padding(.leading, CGFloat(depth) * 14)
                }
                .tint(.primary)
                .accessibilityIdentifier("code-file:\(file.path)")
            }
        })
    }

    @ViewBuilder private var history: some View {
        if let a = model.activity, !a.local.isEmpty {
            Section("Last 30 days") {
                let days = a.perDay(days: 30, now: Date())
                let top = max(1, days.max() ?? 1)
                HStack(alignment: .bottom, spacing: 2) {
                    ForEach(Array(days.enumerated()), id: \.offset) { _, n in
                        Rectangle()
                            .fill(n > 0 ? XbinColor.accent : XbinColor.border)
                            .frame(height: max(3, 36 * CGFloat(n) / CGFloat(top)))
                    }
                }
                .frame(height: 38)
                .accessibilityLabel(Text("\(days.reduce(0, +)) commits in the last 30 days"))
            }
        }
        Section("Commits") {
            Button { sheet = .diff(rev: "", title: "Uncommitted changes") } label: {
                Label("Uncommitted changes", systemImage: "pencil.and.list.clipboard")
            }
            .accessibilityIdentifier("code-uncommitted")
            ForEach(model.commits) { c in
                Button { sheet = .diff(rev: c.hash, title: c.short) } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(verbatim: c.subject).font(.callout).lineLimit(2)
                        HStack(spacing: 6) {
                            Text(verbatim: c.short).font(.caption.monospaced())
                            Text(verbatim: c.author)
                            Text(verbatim: Date(timeIntervalSince1970: TimeInterval(c.date)).formatted(.relative(presentation: .named)))
                            Spacer(minLength: 4)
                            Text(verbatim: "+\(c.added)").foregroundStyle(XbinColor.ok)
                            Text(verbatim: "−\(c.removed)").foregroundStyle(XbinColor.danger)
                        }
                        .font(.caption).foregroundStyle(.secondary)
                    }
                }
                .tint(.primary)
            }
            if model.loaded, model.commits.isEmpty { Text("No commits.").foregroundStyle(.secondary) }
        }
    }

    static func size(_ n: Int) -> String {
        n < 1024 ? "\(n) B" : n < 1_048_576 ? "\(n / 1024) KB" : String(format: "%.1f MB", Double(n) / 1_048_576)
    }
}

/// A file (monospaced, numbered lines) or a diff, in a sheet.
private struct CodeSheetView: View {
    let model: CodeToolModel
    let sheet: CodeSheet
    @Environment(\.dismiss) private var dismiss
    @State private var text: String?
    @State private var note: String?
    @State private var lines: [ChatDiff.Line]?

    var body: some View {
        NavigationStack {
            Group {
                if let lines {
                    DiffLinesView(lines: lines)
                } else if let text {
                    CodeTextView(text: text)
                } else if let note {
                    ContentUnavailableView(note, systemImage: "doc")
                } else {
                    ProgressView()
                }
            }
            .navigationTitle(Text(verbatim: title))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
        }
        .task { await load() }
    }

    private var title: String {
        switch sheet {
        case .file(let p): return p
        case .diff(_, let t): return t
        }
    }

    private func load() async {
        switch sheet {
        case .file(let p):
            switch await model.file(p) {
            case .success(.text(let t, let truncated)):
                text = t
                if truncated { note = nil }
            case .success(.binary): note = "A binary file"
            case .failure(let e): note = e.text
            }
        case .diff(let rev, _):
            switch await model.diff(rev) {
            case .success(let d): if d.isEmpty { note = rev.isEmpty ? "No uncommitted changes" : "No changes" } else { lines = ChatDiff.lines(d) }
            case .failure(let e): note = e.text
            }
        }
    }
}

/// A file's text: numbered, monospaced, scrolling both ways, drawn lazily.
struct CodeTextView: View {
    let text: String

    var body: some View {
        // (a file's final newline ends its last line; it starts no new one)
        let lines = (text.hasSuffix("\n") ? String(text.dropLast()) : text).split(separator: "\n", omittingEmptySubsequences: false)
        let width = String(lines.count).count
        ScrollView([.vertical, .horizontal]) {
            LazyVStack(alignment: .leading, spacing: 0) {
                ForEach(Array(lines.enumerated()), id: \.offset) { i, l in
                    HStack(alignment: .firstTextBaseline, spacing: 10) {
                        Text(verbatim: String(i + 1).leftPad(width)).foregroundStyle(.tertiary)
                        Text(verbatim: String(l).isEmpty ? " " : String(l)).textSelection(.enabled)
                    }
                    .font(.caption.monospaced())
                    .fixedSize()
                }
            }
            .padding(10)
        }
        .accessibilityIdentifier("code-text")
    }
}

/// A unified diff, line by line, coloured as the web's (bx-code.js).
struct DiffLinesView: View {
    let lines: [ChatDiff.Line]

    var body: some View {
        ScrollView([.vertical, .horizontal]) {
            LazyVStack(alignment: .leading, spacing: 0) {
                ForEach(Array(lines.enumerated()), id: \.offset) { _, l in
                    Text(verbatim: l.text.isEmpty ? " " : l.text)
                        .font(.caption.monospaced())
                        .foregroundStyle(color(l.kind))
                        .fixedSize()
                        .padding(.vertical, l.kind == .file ? 2 : 0)
                        .background(background(l.kind))
                }
            }
            .padding(10)
        }
    }

    private func color(_ k: ChatDiff.Line.Kind) -> Color {
        switch k {
        case .added: return XbinColor.ok
        case .removed: return .red
        case .hunk: return .blue
        case .file: return .secondary
        case .context: return .primary
        }
    }

    private func background(_ k: ChatDiff.Line.Kind) -> Color {
        switch k {
        case .added: return XbinColor.okBackground
        case .removed: return XbinColor.dangerBackground
        default: return .clear
        }
    }
}

extension String {
    func leftPad(_ n: Int) -> String { count >= n ? self : String(repeating: " ", count: n - count) + self }
}

// MARK: - Logs

@MainActor
@Observable
final class LogsToolModel {
    let workspace: WorkspaceModel
    let tile: String
    /// The deployment whose log shows ("" = the primary, as the server picks).
    var deployment = ""
    private(set) var log = LogLines(limit: 3000)
    private(set) var problem: String?
    private(set) var live = false

    init(workspace: WorkspaceModel, tile: String) {
        self.workspace = workspace
        self.tile = tile
    }

    /// The stream has delivered: it, not the snapshot, is the log now.
    private var streamed = false
    /// Why there is nothing to show yet (not an error).
    private(set) var note: String?

    /// Streams the log (its tail, then what is appended) until cancelled,
    /// reconnecting after a drop; a refusal (logs need terminal access)
    /// says so and stops. The tail shows first from a plain read, and is
    /// read again until the stream delivers: URLSession holds the first
    /// 512 bytes of a text/plain stream to sniff its type, so a short log
    /// would never show through the stream alone.
    func follow() async {
        let transport = AgentSessionTransport(auth: workspace.auth, transport: workspace.transport)
        while !Task.isCancelled {
            streamed = false
            guard await snapshot() else { return }
            let poll = Task { @MainActor [weak self] in
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(4))
                    guard let self, !self.streamed, !Task.isCancelled else { return }
                    _ = await self.snapshot()
                }
            }
            defer { poll.cancel() }
            do {
                let stream = try await transport.stream(HTTPRequest(method: "GET", path: CodeRoute.logs(tile, deployment: deployment)))
                problem = nil
                live = true
                for try await chunk in stream {
                    if !streamed {
                        streamed = true
                        poll.cancel()
                        log = LogLines(limit: 3000)
                        note = nil
                    }
                    log.append(chunk)
                }
                live = false
            } catch let e as AgentAPIError {
                live = false
                problem = e.description
                if e.status == 403 || e.status == 404 || e.status == 400 { return }
            } catch {
                live = false
                if Task.isCancelled { return }
                problem = workspace.describe(error)
            }
            poll.cancel()
            try? await Task.sleep(for: .seconds(3))
        }
    }

    /// Reads the tail once; false when the log is refused for good.
    private func snapshot() async -> Bool {
        do {
            let r = try await workspace.auth.send(APIRequest("GET", CodeRoute.logs(tile, deployment: deployment, follow: false)))
            if r.status == 404 {
                note = (try? r.json())?["error"]?.stringValue ?? "No logs yet."
                return true
            }
            guard r.isSuccess else {
                problem = APIError(r).description
                return !(r.status == 403 || r.status == 400)
            }
            guard !streamed else { return true }
            var l = LogLines(limit: 3000)
            l.append(r.body)
            log = l
            note = nil
            problem = nil
            live = true
        } catch {
            problem = workspace.describe(error)
        }
        return true
    }
}

struct LogsToolView: View {
    let model: LogsToolModel
    /// The tile's deployments to choose from (none: the primary only).
    let deployments: [String]
    @State private var following = true

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Circle().fill(model.live ? XbinColor.ok : XbinColor.muted).frame(width: 8, height: 8)
                    .accessibilityLabel(Text(model.live ? "Live" : "Not connected"))
                Text(verbatim: model.deployment.isEmpty ? model.tile : "\(model.tile)+\(model.deployment)")
                    .font(.footnote.monospaced()).lineLimit(1)
                Spacer()
                if deployments.count > 1 {
                    Picker("Deployment", selection: Binding(get: { model.deployment }, set: { model.deployment = $0 })) {
                        ForEach(deployments, id: \.self) { d in Text(verbatim: d).tag(d == deployments.first ? "" : d) }
                    }
                    .pickerStyle(.menu)
                }
                Button { following.toggle() } label: {
                    Image(systemName: following ? "arrow.down.to.line.circle.fill" : "arrow.down.to.line.circle")
                }
                .accessibilityLabel("Follow")
                .accessibilityValue(Text(following ? "on" : "off"))
            }
            .padding(.horizontal, 12).padding(.vertical, 6)
            .background(XbinColor.surface)
            if let p = model.problem {
                Label { Text(verbatim: p) } icon: { Image(systemName: "exclamationmark.triangle") }
                    .font(.footnote).foregroundStyle(XbinColor.danger).padding(8)
            }
            ScrollViewReader { proxy in
                ScrollView([.vertical, .horizontal]) {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(Array(model.log.lines.enumerated()), id: \.offset) { i, l in
                            Text(verbatim: l.isEmpty ? " " : l).font(.caption2.monospaced()).fixedSize()
                                .id(model.log.dropped + i)
                        }
                        if !model.log.pending.isEmpty {
                            Text(verbatim: model.log.pending).font(.caption2.monospaced()).foregroundStyle(.secondary).fixedSize()
                        }
                        Color.clear.frame(height: 1).id("end")
                    }
                    .padding(8)
                }
                .background(Color.black.opacity(0.04))
                .accessibilityIdentifier("logs-text")
                .onChange(of: model.log.lines.count) { _, _ in
                    if following { proxy.scrollTo("end", anchor: .bottom) }
                }
            }
            if model.log.lines.isEmpty, model.log.pending.isEmpty, model.problem == nil {
                Text(verbatim: model.note ?? (model.live ? "No output yet." : "Connecting…")).font(.footnote).foregroundStyle(.secondary).padding()
            }
        }
        .task(id: model.deployment) { await model.follow() }
    }
}

// MARK: - PRs

@MainActor
@Observable
final class ProposalsModel {
    let workspace: WorkspaceModel
    let tile: String
    private(set) var list: [Proposal] = []
    private(set) var loaded = false
    /// The proposals route answered 404/405: this xbind has none.
    private(set) var absent = false
    var problem: String?

    init(workspace: WorkspaceModel, tile: String) {
        self.workspace = workspace
        self.tile = tile
    }

    var openCount: Int { Proposal.openCount(list) }

    func load() async {
        do {
            let r = try await workspace.auth.send(APIRequest("GET", ProposalRoute.list(target: tile)))
            if r.status == 404 || r.status == 405 { absent = true; loaded = true; return }
            guard r.isSuccess else { throw APIError(r) }
            list = Proposal.list(try r.json())
            problem = nil
        } catch {
            problem = workspace.describe(error)
        }
        loaded = true
    }

    /// Keeps the list live on the tile's `pr` events (and after a reconnect).
    func follow() async {
        for await _ in workspace.events.proposals(of: tile) { await load() }
    }

    func one(_ n: Int) async -> Proposal? {
        guard let j = try? await workspace.auth.json(APIRequest("GET", ProposalRoute.one(target: tile, n: n))) else { return nil }
        return Proposal(json: j)
    }

    func series(_ n: Int) async -> String? {
        guard let r = try? await workspace.auth.call(APIRequest("GET", ProposalRoute.series(target: tile, n: n))) else { return nil }
        return String(decoding: r.body, as: UTF8.self)
    }

    /// A comment, or a decision (merged, rejected) with its note: the
    /// proposal as it is after, or the server's refusal.
    func post(_ path: String, _ body: XbinCore.JSONValue) async -> Result<Proposal, ToolProblem> {
        do {
            let j = try await workspace.auth.json(APIRequest.json("POST", path, body))
            Task { await load() }
            guard let p = Proposal(json: j) else { return .failure(ToolProblem(text: "Unreadable answer")) }
            return .success(p)
        } catch { return .failure(ToolProblem(text: workspace.describe(error))) }
    }
}

struct ProposalsToolView: View {
    let model: ProposalsModel
    @State private var all = false
    @State private var selected: Int?

    var body: some View {
        List {
            Picker("", selection: $all) {
                Text("Open").tag(false)
                Text("All").tag(true)
            }
            .pickerStyle(.segmented)
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets(top: 4, leading: 0, bottom: 4, trailing: 0))
            if let p = model.problem {
                Label { Text(verbatim: p) } icon: { Image(systemName: XbinGlyphs.symbol("error")) }.foregroundStyle(XbinColor.danger)
            }
            if model.absent {
                Text("This workspace's xbind has no change proposals.").foregroundStyle(.secondary)
            }
            let shown = model.list.filter { all || $0.isOpen }
            Section {
                ForEach(shown) { p in
                    Button { selected = p.number } label: {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(verbatim: "#\(p.number) \(p.title)").font(.callout).lineLimit(2)
                            HStack(spacing: 6) {
                                Text(verbatim: p.fromShort).font(.caption.monospaced())
                                Text(verbatim: DeployView.ago(p.updated.isEmpty ? p.created : p.updated, now: Date()))
                                Spacer(minLength: 4)
                                StateTag(state: p.state)
                            }
                            .font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    .tint(.primary)
                    .accessibilityIdentifier("pr:\(p.number)")
                }
                if model.loaded, shown.isEmpty, !model.absent {
                    Text(all ? "No proposals." : "No open proposals.").foregroundStyle(.secondary)
                }
            } footer: {
                Text(verbatim: "Another tile's agent proposes changes with bx code pr \(model.tile) --title … *.patch; apply them in this tile's terminal.")
            }
        }
        .listStyle(.insetGrouped)
        .overlay { if !model.loaded { ProgressView() } }
        .refreshable { await model.load() }
        .sheet(item: Binding(get: { selected.map { IdentifiedInt(id: $0) } }, set: { selected = $0?.id })) { s in
            ProposalSheet(model: model, number: s.id).presentationDetents([.large])
        }
    }
}

struct IdentifiedInt: Identifiable { let id: Int }

private struct StateTag: View {
    let state: String
    var body: some View {
        // Base Two's badge: square, a 1 pt edge, the state's word in its
        // colour (a proposal's state is not a status: open in the accent).
        Text(verbatim: state).font(.caption2.bold())
            .padding(.horizontal, 6).padding(.vertical, 1)
            .overlay(RoundedRectangle.xbinPlate.strokeBorder(color, lineWidth: 1))
            .foregroundStyle(color)
    }
    private var color: Color {
        switch state {
        case "open": return XbinColor.accent
        case "merged": return XbinColor.ok
        case "rejected": return XbinColor.danger
        default: return XbinColor.muted
        }
    }
}

/// One proposal: its header, the command that applies it, the patch
/// series, the review thread, a comment, and the target's decisions —
/// each asking for its note first, as the web's prompt does.
private struct ProposalSheet: View {
    let model: ProposalsModel
    let number: Int
    @Environment(\.dismiss) private var dismiss
    @State private var pr: Proposal?
    @State private var lines: [ChatDiff.Line]?
    @State private var stats: (added: Int, removed: Int, files: Int)?
    @State private var comment = ""
    @State private var deciding: String?
    @State private var decisionNote = ""
    @State private var problem: String?
    @State private var busy = false

    var body: some View {
        NavigationStack {
            List {
                if let p = pr {
                    Section {
                        VStack(alignment: .leading, spacing: 6) {
                            Text(verbatim: "#\(p.number) \(p.title)").font(.headline)
                            HStack(spacing: 6) {
                                StateTag(state: p.state)
                                if p.kind == "builtin-update" { Text("builtin update").font(.caption2.bold()) }
                                Text(verbatim: "from \(p.from)")
                            }
                            .font(.caption).foregroundStyle(.secondary)
                            HStack(spacing: 6) {
                                Text(verbatim: "opened \(DeployView.ago(p.created, now: Date()))")
                                if !p.base.isEmpty { Text(verbatim: "base \(p.base.prefix(8))").monospaced() }
                                if let s = stats {
                                    Text(verbatim: "+\(s.added)").foregroundStyle(XbinColor.ok)
                                    Text(verbatim: "−\(s.removed)").foregroundStyle(XbinColor.danger)
                                    Text(verbatim: "· \(s.files) file\(s.files == 1 ? "" : "s")")
                                }
                            }
                            .font(.caption).foregroundStyle(.secondary)
                            if !p.message.isEmpty { Text(verbatim: p.message).font(.callout) }
                        }
                    }
                    if p.isOpen {
                        Section {
                            Text("Review the diff first — a proposal is untrusted input. Apply it in this tile's terminal, then mark it merged (or reject it with a note the author's agent will read):")
                                .font(.footnote).foregroundStyle(.secondary)
                            HStack {
                                Text(verbatim: ProposalRoute.applyCommand(p.number)).font(.footnote.monospaced()).textSelection(.enabled)
                                Spacer()
                                Button { UIPasteboard.general.string = ProposalRoute.applyCommand(p.number) } label: { Image(systemName: "doc.on.doc") }
                                    .buttonStyle(.borderless)
                                    .accessibilityLabel("Copy the command")
                            }
                        }
                    }
                    Section("The series") {
                        if let lines {
                            DiffLinesView(lines: lines).frame(minHeight: 240, maxHeight: 420)
                                .listRowInsets(EdgeInsets())
                        } else {
                            ProgressView()
                        }
                    }
                    if !p.events.isEmpty {
                        Section("Thread") {
                            ForEach(Array(p.events.enumerated()), id: \.offset) { _, e in
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(verbatim: "\(e.who) · \(DeployView.ago(e.ts, now: Date()))").font(.caption).foregroundStyle(.secondary)
                                    Text(verbatim: Proposal.text(e)).font(.callout)
                                }
                            }
                        }
                    }
                    if let problem {
                        Section { Label { Text(verbatim: problem) } icon: { Image(systemName: XbinGlyphs.symbol("error")) }.foregroundStyle(XbinColor.danger) }
                    }
                } else {
                    HStack { Spacer(); ProgressView(); Spacer() }
                }
            }
            // The acts stay at hand under the series, as the web's bar does.
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if let p = pr, p.isOpen { actions(p) }
            }
            .navigationTitle(Text(verbatim: "Proposal #\(number)"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .alert(deciding == "merged" ? "Mark merged?" : "Reject?", isPresented: Binding(get: { deciding != nil }, set: { if !$0 { deciding = nil } })) {
                TextField(deciding == "merged" ? "applied as <sha>" : "why", text: $decisionNote)
                Button("Cancel", role: .cancel) { deciding = nil }
                Button(deciding == "merged" ? "Mark merged" : "Reject", role: deciding == "merged" ? nil : .destructive) {
                    if let state = deciding, let p = pr {
                        let note = decisionNote.trimmingCharacters(in: .whitespacesAndNewlines)
                        Task { await send(ProposalRoute.state, ProposalRoute.stateBody(target: model.tile, n: p.number, state: state, comment: note)) }
                    }
                    deciding = nil
                }
            } message: {
                Text("An optional note for the author (why, or applied as <sha>). Closing is final: a refused proposal is filed again.")
            }
        }
        .task {
            pr = await model.one(number)
            if let s = await model.series(number) {
                lines = ChatDiff.lines(s)
                stats = LineDiff.stats(s)
            } else {
                lines = []
            }
        }
    }

    private func actions(_ p: Proposal) -> some View {
        VStack(spacing: 8) {
            HStack(spacing: 8) {
                TextField("Comment for the author…", text: $comment, axis: .vertical)
                    .lineLimit(1...4)
                    .padding(8)
                    .background(XbinColor.surface, in: .xbinPlate)
                    .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.borderStrong, lineWidth: 1))
                Button("Comment") {
                    Task { await send(ProposalRoute.comment, ProposalRoute.commentBody(target: model.tile, n: p.number, body: comment.trimmingCharacters(in: .whitespacesAndNewlines))) }
                }
                .disabled(busy || comment.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
            HStack(spacing: 10) {
                Button { decisionNote = comment; deciding = "merged" } label: { Label("Mark merged", systemImage: "checkmark") }
                    .buttonStyle(.bordered).tint(XbinColor.ok)
                Button(role: .destructive) { decisionNote = comment; deciding = "rejected" } label: { Label("Reject", systemImage: "xmark") }
                    .buttonStyle(.bordered)
                Spacer(minLength: 0)
            }
            .disabled(busy)
        }
        .padding(.horizontal, 16).padding(.vertical, 10)
        .background(.bar)
    }

    private func send(_ path: String, _ body: XbinCore.JSONValue) async {
        busy = true
        defer { busy = false }
        switch await model.post(path, body) {
        case .success(let p):
            pr = p
            comment = ""
            problem = nil
        case .failure(let e):
            problem = e.text
        }
    }
}
