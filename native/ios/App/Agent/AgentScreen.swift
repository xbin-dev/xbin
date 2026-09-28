import Observation
import SwiftUI
import UIKit
import XbinAgent
import XbinCore
import XbinRenderer

/// One ACP agent session (plans/native.md §13): XbinAgent keeps it current
/// (the tail page, follow, catch-up, older pages, the D77 card rules); this
/// model drives the screen's actions. The transcript is `rows` — one
/// observable object per row, so an event re-renders its own row (D130).
@MainActor
@Observable
final class AgentScreenModel {
    let workspace: WorkspaceModel
    var cwd: String?
    var sessionID: String?
    var providerID: String?
    /// The loaded window of the transcript, row by row, and the session's facts.
    let rows = AgentRows()
    var providers: [AgentProvider] = []
    var history: [HistoryEntry] = []
    var error: String?
    /// The session's live events can't be followed (their stream won't
    /// open): why, shown over the composer while the feed retries.
    var streamProblem: String?
    var draft = ""
    var starting = false
    /// Open cards nested in a subagent's (a row's own is `AgentRow.isOpen`).
    var openNested: Set<String> = []
    var detail: ToolCall?
    var signInCommand: LoginNeeded?
    /// Files waiting to ride the next prompt (§13): photos, the camera, Files.
    var attachments: [PendingAttachment] = []
    let picker = AttachPicker()

    @ObservationIgnored private var feed: AgentSessionFeed?
    @ObservationIgnored private var tasks: [Task<Void, Never>] = []
    @ObservationIgnored let memo = MarkdownMemo()
    /// The navigation of the window this screen is in (never the focused
    /// window's: the user may be in another window when a session starts).
    @ObservationIgnored private weak var nav: WorkspaceNav?

    init(workspace: WorkspaceModel, nav: WorkspaceNav, cwd: String?, sessionID: String?) {
        self.workspace = workspace
        self.nav = nav
        self.cwd = cwd
        self.sessionID = sessionID
    }

    var client: AgentClient { workspace.agents }
    var provider: AgentProvider? { providers.first { $0.id == providerID } }
    var state: AgentSessionState? { rows.loaded ? rows.state : nil }
    var ended: Bool { rows.ended }

    func load() async {
        do {
            providers = try await client.providers()
            if let id = sessionID {
                let snap = try await client.session(id)
                cwd = snap.session.cwd
                providerID = snap.session.provider
                attach(id)
            } else if let cwd {
                history = (try? await client.history(cwd: cwd)) ?? []
                // Eager: the session exists before the first prompt, so the
                // pickers and slash commands load while the user types (§13).
                if providers.count == 1 { await create(provider: providers[0].id) }
            }
        } catch {
            self.error = workspace.describe(error)
        }
    }

    func create(provider: String, resume: String? = nil) async {
        guard let cwd, !starting else { return }
        starting = true
        defer { starting = false }
        do {
            let info = try await client.create(CreateAgentSession(cwd: cwd, provider: provider, resume: resume))
            providerID = provider
            sessionID = info.id
            attach(info.id)
            // This window shows the new session, if it still shows the launcher.
            nav?.started(session: info.id, cwd: cwd)
        } catch let e as AgentAPIError {
            error = e.description
            if e.looksLikeAuth { signInCommand = state?.signIn(provider: providers.first { $0.id == provider }, lastError: e.message) }
        } catch {
            self.error = workspace.describe(error)
        }
    }

    /// Events per page and the rows kept beyond the visible ones: xbind's
    /// default page and D130's margin. A Debug build takes
    /// -XbinAgentPageLimit / -XbinAgentKeepMargin, so a UI test crosses
    /// pages and unloads within a few screens.
    static var pageLimit: Int { debugOverride("XbinAgentPageLimit") ?? AgentWindow.pageLimit }
    static var keepMargin: Int { debugOverride("XbinAgentKeepMargin") ?? AgentWindow.keepMargin }

    private static func debugOverride(_ key: String) -> Int? {
        #if DEBUG
        let n = UserDefaults.standard.integer(forKey: key)
        return n > 0 ? n : nil
        #else
        return nil
        #endif
    }

    private func attach(_ id: String) {
        stop()
        let f = AgentSessionFeed(client: client, sessionID: id, pageLimit: Self.pageLimit)
        feed = f
        bottomTask = nil
        if !saidBottom { deliverBottom() }
        // A build chooser started this session with its first message: send
        // it now (the server waits for the agent's handshake); a failure
        // leaves it in the composer.
        if let first = workspace.takePendingPrompt(id) {
            draft = first
            Task { await send() }
        }
        tasks.append(Task { await f.run() })
        tasks.append(workspace.events.deliver(to: f)) // /ws/events session frames, catch-up after a gap
        tasks.append(LiveActivities.shared.follow(f, in: workspace)) // the turn's Live Activity (push.md §7)
        tasks.append(Task { [weak self] in
            for await w in await f.updates() {
                guard let self else { return }
                self.rows.apply(w) // touches only the rows (and facts) that changed
                let login = w.state.signIn(provider: self.provider, lastError: nil)
                if self.signInCommand != login { self.signInCommand = login }
            }
        })
        tasks.append(Task { [weak self] in
            for await s in await f.followStates() {
                guard let self else { return }
                switch s {
                case .failing(let why): self.streamProblem = "Can't follow the session (\(why)) — retrying"
                case .live, .ended: self.streamProblem = nil
                case .connecting: break
                }
            }
        })
    }

    func stop() {
        tasks.forEach { $0.cancel() }
        tasks = []
    }

    func catchUp() async { await feed?.catchUp() }

    // MARK: the reader's window (D130)

    /// The reader nears the top of the loaded rows (at rest): the page above.
    func loadOlder() {
        guard let feed else { return }
        Task { await feed.loadOlder() }
    }

    /// The reader nears the bottom of the loaded rows while rows below were unloaded.
    func loadNewer() {
        guard let feed else { return }
        Task { await feed.loadNewer() }
    }

    /// Whether the reader is at the bottom, as the list last said.
    @ObservationIgnored private var saidBottom = true
    @ObservationIgnored private var bottomTask: Task<Void, Never>?

    /// The list says whether the reader is at the bottom. Delivered in
    /// order, the last word winning: a flip and its correction around a
    /// page landing must not reach the feed the other way round.
    func atBottom(_ on: Bool) {
        saidBottom = on
        deliverBottom()
    }

    /// Sends the list's last word to the feed (also on attach: the list
    /// reports only changes, and one may come before the feed exists).
    private func deliverBottom() {
        guard let feed, bottomTask == nil else { return }
        bottomTask = Task { [weak self] in
            var sent: Bool?
            while let self, sent != self.saidBottom {
                let v = self.saidBottom
                await feed.setAtBottom(v)
                sent = v
            }
            self?.bottomTask = nil
        }
    }

    /// The rows on screen (reported at rest): pages far from them unload
    /// (once there are enough rows for that to matter).
    func visible(_ ids: [String]) {
        let margin = Self.keepMargin
        guard let feed, rows.rows.count > margin * 2, let span = rows.span(ids) else { return }
        Task { await feed.keep(visible: span.first, span.last, margin: margin) }
    }

    /// The pill: follow the bottom again (the tail is read again when it was unloaded).
    func jumpToLatest() {
        guard let feed else { return }
        Task { await feed.jumpToLatest() }
    }

    /// A card nested in a subagent's, open or folded.
    func nestedOpen(_ id: String) -> Binding<Bool> {
        Binding(get: { self.openNested.contains(id) },
                set: { if $0 { self.openNested.insert(id) } else { self.openNested.remove(id) } })
    }

    func send() async {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        let files = attachments
        guard !text.isEmpty || !files.isEmpty else { return }
        // Not attached yet (the session still loading, or it failed to):
        // say so and keep the draft — never a Send that does nothing.
        guard let feed else {
            error = sessionID == nil ? "No session to send to yet" : "Still connecting to the session — try again in a moment"
            Haptics.failed()
            return
        }
        draft = ""
        attachments = []
        do {
            if files.isEmpty {
                _ = try await feed.send(text)
            } else {
                _ = try await feed.send(text, attachments: files.map(\.prompt))
            }
            Haptics.send()
        } catch {
            draft = text
            attachments = files + attachments
            self.error = describe(error)
            if let e = error as? AgentAPIError, e.looksLikeAuth {
                signInCommand = state?.signIn(provider: provider, lastError: e.message)
            }
        }
    }

    /// The attach button: the app's pickers; photos made model-ready
    /// (PromptAttachment.imagePlan). A photo is read up to 64 MiB and held
    /// to the 10 MiB file limit only once redrawn; anything else must fit
    /// as it is (PromptAttachment.readLimit/refusal). A file that can't go
    /// is left out and said; past the prompt's total the files stay (to
    /// remove some) and the limit is said.
    func pickAttachments() async {
        let room = PromptAttachment.maxCount - attachments.count
        guard room > 0 else {
            error = "\(PromptAttachment.maxCount) files at most in one message"
            return
        }
        let limits = PickLimits(file: PromptAttachment.readLimit(image: false), image: PromptAttachment.readLimit(image: true))
        let picked = await picker.pick(accept: nil, maxCount: room, limits: limits)
        var notes: [String] = []
        if let n = picker.notice { notes.append(n) }
        for f in picked {
            if let size = f.oversize {
                notes.append(AttachmentProblem.tooLargeToRead(name: f.name, bytes: size).description)
                continue
            }
            let ready = AgentImages.prepare(f)
            if let problem = PromptAttachment.refusal(name: ready.name, bytes: ready.data.count) {
                notes.append(problem.description)
                continue
            }
            attachments.append(PendingAttachment(file: ready))
        }
        if let problem = PromptAttachment.check(attachments.map(\.prompt)) { notes.append(problem.description) }
        if !notes.isEmpty { error = notes.joined(separator: "\n") }
    }

    func removeAttachment(_ id: String) {
        attachments.removeAll { $0.id == id }
    }

    func cancel() async { try? await feed?.cancel() }

    /// A permission card's button (never answered for the user, D77). A
    /// plan rejected with feedback keeps the agent planning.
    func choose(_ card: PermissionCard, optionID: String, feedback: String) async {
        guard let feed, let choice = card.choices.first(where: { $0.id == optionID }) else { return }
        do {
            if card.isPlanApproval, !feedback.trimmingCharacters(in: .whitespaces).isEmpty {
                try await feed.keepPlanning(card, choice: choice, feedback: feedback)
            } else {
                try await feed.answer(card, choice: choice)
                if !choice.isReject { Haptics.approve() }
            }
        } catch {
            self.error = describe(error)
            Haptics.failed()
        }
    }

    func answer(_ card: QuestionCard, content: CoreJSON?) async {
        guard let feed else { return }
        var values: [String: AgentJSON] = [:]
        for (k, v) in content?.objectValue ?? [:] { if let a = AgentChat.agent(v) { values[k] = a } }
        do {
            try await feed.answer(card, action: content == nil ? .decline : .accept, values: values)
        } catch { self.error = describe(error) }
    }

    func set(_ picker: String, to value: String) async {
        do { try await feed?.set(picker, to: value) } catch { self.error = describe(error) }
    }

    func end() async {
        guard let id = sessionID else { return }
        try? await client.end(id)
    }

    func describe(_ e: any Error) -> String {
        if let a = e as? AgentAPIError { return a.description }
        if let f = e as? FormError { return f.description }
        return workspace.describe(e)
    }
}

/// The ACP agent, full screen, transcript only (§13, §15): diffs, tool
/// output and the sign-in terminal open as sheets and come back.
struct AgentScreen: View {
    let workspace: WorkspaceModel
    var cwd: String?
    var sessionID: String?

    @State private var model: AgentScreenModel?
    /// This window's navigation.
    @Environment(WorkspaceNav.self) private var nav
    @State private var fullScreen = false
    @State private var signingIn = false
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.openURL) private var openURL
    /// Hosted as a tab of a tile's sessions screen (D132): nil for a screen
    /// of its own. A tab puts its items in the bar only while in front.
    @Environment(\.sessionTab) private var tab
    @Environment(\.panelActive) private var panelActive

    var body: some View {
        Group {
            if let m = model {
                if m.sessionID == nil { AgentLauncher(model: m) } else { session(m) }
            } else {
                ProgressView()
            }
        }
        .modifier(SessionTitle(title: model?.state?.title ?? "Agent" + (cwd.map { " · \(TileInfo.humanize($0))" } ?? "")))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(fullScreen ? .hidden : .visible, for: .navigationBar)
        .task {
            if model == nil {
                let m = AgentScreenModel(workspace: workspace, nav: nav, cwd: cwd, sessionID: sessionID)
                model = m
                await m.load()
            }
        }
        .onDisappear { model?.stop() }
        .onChange(of: scenePhase) { _, p in if p == .active { Task { await model?.catchUp() } } }
    }

    @ViewBuilder private func session(_ m: AgentScreenModel) -> some View {
        VStack(spacing: 0) {
            if let login = m.signInCommand {
                HStack {
                    Label("\(login.provider) needs you to sign in", systemImage: "person.badge.key")
                        .font(.subheadline)
                    Spacer()
                    Button("Sign in") { signingIn = true }.buttonStyle(.borderedProminent)
                }
                .padding(10)
                .background(.orange.opacity(0.15))
            }
            AgentTranscriptList(model: m)
            if let p = m.streamProblem {
                Label { Text(verbatim: p) } icon: { Image(systemName: "exclamationmark.triangle") }
                    .font(.footnote).foregroundStyle(.orange).padding(.horizontal)
            }
            if let e = m.error {
                Text(verbatim: e).font(.footnote).foregroundStyle(.red).padding(.horizontal).onTapGesture { m.error = nil }
            }
            composer(m)
        }
        .modifier(AttachPickers(picker: m.picker))
        .toolbar {
            if tab == nil || panelActive {
            ToolbarItemGroup(placement: .primaryAction) {
                if let pickers = m.state?.pickers(provider: m.provider), !pickers.isEmpty {
                    Menu {
                        ForEach(pickers) { p in
                            Picker(selection: Binding(get: { p.current }, set: { v in Task { await m.set(p.id, to: v) } })) {
                                ForEach(p.values) { v in Text(verbatim: v.label).tag(v.value) }
                            } label: { Text(verbatim: p.label) }
                        }
                    } label: { Image(systemName: "slider.horizontal.3") }
                }
                Menu {
                    if tab == nil {
                        Button("Full screen", systemImage: "arrow.up.left.and.arrow.down.right") { fullScreen.toggle() }
                        Button("Terminal on this tile", systemImage: "apple.terminal") {
                            if let c = m.cwd { workspace.open(.terminal(cwd: c, session: nil), in: nav) }
                        }
                    }
                    Button("End session", systemImage: "stop.circle", role: .destructive) { Task { await m.end() } }
                } label: { Image(systemName: "ellipsis.circle") }
            }
            }
        }
        .overlay(alignment: .topTrailing) {
            if fullScreen {
                Button { fullScreen = false } label: {
                    Image(systemName: "arrow.down.right.and.arrow.up.left").padding(8).background(.ultraThinMaterial, in: Circle())
                }.padding(8)
            }
        }
        .sheet(item: Binding(get: { m.detail }, set: { m.detail = $0 })) { tool in ToolDetailSheet(tool: tool) }
        .fullScreenCover(isPresented: $signingIn) {
            if let c = m.cwd, let login = m.signInCommand {
                NavigationStack {
                    TerminalScreen(workspace: workspace, cwd: c, initialInput: login.command + "\n")
                        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Done") { signingIn = false } } }
                }
            }
        }
    }

    /// Stop, while a turn runs (a plain function: a ViewBuilder can't assign).
    private func stopAction(_ m: AgentScreenModel, busy: Bool) -> (@MainActor () -> Void)? {
        guard busy else { return nil }
        return { Task { await m.cancel() } }
    }

    @ViewBuilder private func composer(_ m: AgentScreenModel) -> some View {
        let busy = m.state?.isBusy ?? false
        let slash = (m.state?.commands ?? []).map {
            XbinRendererModel.SlashCommand(name: "/" + $0.name, hint: $0.hint.isEmpty ? nil : $0.hint,
                                           description: $0.description.isEmpty ? nil : $0.description)
        }
        let chips = m.attachments.map { ChatAttachment(id: $0.id, name: $0.file.name, mime: $0.file.mime) }
        let c = ChatComposer(placeholder: m.ended ? "The session ended" : "Message the agent", busy: busy,
                             disabled: m.ended, attachments: chips, canAttach: !m.ended, slash: slash)
        let text = Binding(get: { m.draft }, set: { m.draft = $0 })
        let onSend: @MainActor (String) -> Void = { text in
            m.draft = text
            Task { await m.send() }
        }
        let onStop = stopAction(m, busy: busy)
        let onAttach: @MainActor () -> Void = { Task { await m.pickAttachments() } }
        let onRemove: @MainActor (String) -> Void = { id in m.removeAttachment(id) }
        // The send button needs text; files alone go with this chip.
        if m.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, !m.attachments.isEmpty, !busy, !m.ended {
            ComposerView(composer: c, text: text, onSend: onSend, onStop: onStop, onAttach: onAttach,
                         onRemoveAttachment: onRemove) {
                Button {
                    Task { await m.send() }
                } label: {
                    Label(m.attachments.count == 1 ? "Send the file" : "Send \(m.attachments.count) files",
                          systemImage: "arrow.up.circle.fill")
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
            }
        } else {
            ComposerView(composer: c, text: text, onSend: onSend, onStop: onStop, onAttach: onAttach,
                         onRemoveAttachment: onRemove)
        }
    }
}

/// The transcript: the loaded rows (a list that holds the reader's place
/// through loads and unloads, D130), the activity line, the jump-to-latest
/// pill. Its own view, so a keystroke in the composer re-renders none of it
/// and a structural change (a row added or unloaded) nothing else.
struct AgentTranscriptList: View {
    let model: AgentScreenModel

    var body: some View {
        let rows = model.rows
        TranscriptView(follow: true, older: rows.hasOlder, newer: rows.hasNewer, fresh: rows.fresh, idType: String.self,
                       onMore: { model.loadOlder() }, onNewer: { model.loadNewer() },
                       onScrolled: { model.atBottom($0) }, onVisible: { model.visible($0) },
                       onJump: { model.jumpToLatest() }) {
            ForEach(rows.rows) { row in AgentRowView(model: model, row: row) }
            AgentActivityLine(rows: rows)
        }
        #if DEBUG
        // The window as the UI tests read it (nothing for a person).
        .overlay(alignment: .topLeading) {
            let w = rows.window
            Color.clear.frame(width: 1, height: 1)
                .accessibilityElement()
                .accessibilityIdentifier("agent-window")
                .accessibilityLabel("rows \(rows.rows.count) segments \(w.segmentCount) following \(w.following) detached \(w.detached) fresh \(rows.fresh) older \(rows.hasOlder) newer \(rows.hasNewer) last \(w.lastSeq)")
        }
        #endif
    }
}

/// One row: observes its own item (and open state) only.
struct AgentRowView: View {
    let model: AgentScreenModel
    let row: AgentRow

    var body: some View {
        let r = row, rows = model.rows
        AgentItemView(model: model, item: row.item, isOpen: Binding(get: { r.isOpen }, set: { rows.setOpen(r, $0) }))
    }
}

/// The line under a running turn.
struct AgentActivityLine: View {
    let rows: AgentRows

    var body: some View {
        if let a = AgentChat.activity(rows.activity) { ActivityView(activity: a) }
    }
}

/// One transcript item as a renderer chat component. It reads the session's
/// status only where it matters (a run that may still stream, a thought
/// not done), so a status change re-renders those rows and no others.
struct AgentItemView: View {
    let model: AgentScreenModel
    let item: TranscriptItem
    /// A thought's or tool card's open state.
    let isOpen: Binding<Bool>

    var body: some View {
        let rows = model.rows
        switch item {
        case .message(let msg):
            MessageView(message: AgentChat.message(msg, status: msg.open ? rows.status : .idle, ended: msg.open && rows.ended,
                                                   agentName: rows.agentName ?? model.provider?.name ?? "Agent",
                                                   blocks: { model.memo.blocks(id: msg.id, text: $0) }),
                        onLink: { url in UIApplication.shared.open(url) })
        case .thought(let t):
            ThinkingView(thinking: AgentChat.thinking(t, status: t.done ? .idle : rows.status, ended: !t.done && rows.ended),
                         isOpen: isOpen)
        case .tool(let t):
            ToolCardView(card: AgentChat.toolCard(t), isOpen: isOpen,
                         hasContent: AgentChat.hasContent(t), onOpen: { model.detail = t }) {
                ToolContentView(tool: t, model: model)
            }
        case .plan(let p):
            PlanView(plan: AgentChat.plan(p))
        case .permission(let p):
            ApprovalView(approval: AgentChat.approval(p)) { id, feedback in
                Task { await model.choose(p, optionID: id, feedback: feedback) }
            }
        case .question(let q):
            QuestionView(question: AgentChat.question(q), onSubmit: { content in
                Task { await model.answer(q, content: content) }
            }, onSkip: { Task { await model.answer(q, content: nil) } })
        case .changes(let c):
            DiffView(diff: AgentChat.diff(c.files))
        case .turnEnd(let d):
            StepView(step: AgentChat.divider(d))
        case .gap(let g):
            StepView(step: ChatStep(glyph: "…", text: g.label, tone: .muted))
        }
    }
}

/// What an opened tool card shows inline: its diff, the tail of its output
/// (ANSI already stripped to styled text), a subagent's own steps.
struct ToolContentView: View {
    let tool: ToolCall
    let model: AgentScreenModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let d = AgentChat.diff(tool) { DiffView(diff: d) }
            if !tool.output.isEmpty {
                let v = tool.outputView(maxCharacters: 4000, maxLines: 30)
                Text(verbatim: v.tail.plain)
                    .font(.caption.monospaced())
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if v.hiddenLines > 0 {
                    Button("Show all (\(v.hiddenLines) more lines)") { model.detail = tool }.font(.caption)
                }
            } else if tool.diffs.isEmpty {
                ForEach(Array(tool.texts().enumerated()), id: \.offset) { _, s in
                    Text(verbatim: s).font(.callout).frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            if !tool.children.isEmpty {
                TranscriptView(follow: false, nested: true) {
                    ForEach(tool.children) { c in AgentItemView(model: model, item: c, isOpen: model.nestedOpen(c.id)) }
                }
            }
        }
    }
}

/// A tool call full screen: the whole output, every diff.
struct ToolDetailSheet: View {
    let tool: ToolCall
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if !tool.command.isEmpty {
                        Text(verbatim: tool.command).font(.callout.monospaced()).textSelection(.enabled)
                    }
                    if let d = AgentChat.diff(tool) { DiffView(diff: d) }
                    if !tool.output.isEmpty {
                        Text(verbatim: tool.outputView(maxCharacters: 400_000).full.plain)
                            .font(.caption.monospaced())
                            .textSelection(.enabled)
                    }
                    if let raw = tool.rawInputText, tool.output.isEmpty, tool.diffs.isEmpty {
                        Text(verbatim: raw).font(.caption.monospaced()).textSelection(.enabled)
                    }
                }
                .padding()
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .navigationTitle(Text(verbatim: tool.headline))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
        }
    }
}

/// No session yet: pick the agent (created eagerly), or resume one from
/// history (D75).
struct AgentLauncher: View {
    let model: AgentScreenModel

    var body: some View {
        List {
            if model.cwd == nil {
                Text("Open an agent from a tile (its menu → Agent here).").foregroundStyle(.secondary)
            }
            Section("Start") {
                ForEach(model.providers) { p in
                    Button {
                        Task { await model.create(provider: p.id) }
                    } label: {
                        Label { Text(verbatim: p.name) } icon: { Image(systemName: "sparkles") }
                    }
                    .disabled(model.starting || model.cwd == nil)
                }
            }
            if !model.history.isEmpty {
                Section("Resume") {
                    ForEach(model.history) { h in
                        Button {
                            Task { await model.create(provider: h.provider, resume: h.id) }
                        } label: {
                            VStack(alignment: .leading) {
                                Text(verbatim: h.name.isEmpty ? h.preview : h.name).lineLimit(2)
                                Text(verbatim: "\(h.provider) · \(h.turns) turns").font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        .disabled(!h.loadable || model.starting)
                    }
                }
            }
            if let e = model.error { Section { Text(verbatim: e).foregroundStyle(.red) } }
        }
        .overlay { if model.starting { ProgressView() } }
    }
}
