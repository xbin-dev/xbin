import Observation
import SwiftUI
import UIKit
import XbinAgent
import XbinCore
import XbinRenderer

/// One ACP agent session (plans/native.md §13): XbinAgent keeps it current
/// (replay, follow, catch-up, the D77 card rules); this model drives the
/// screen's actions.
@MainActor
@Observable
final class AgentScreenModel {
    let workspace: WorkspaceModel
    var cwd: String?
    var sessionID: String?
    var providerID: String?
    var transcript: AgentTranscript?
    var providers: [AgentProvider] = []
    var history: [HistoryEntry] = []
    var error: String?
    var draft = ""
    var starting = false
    var openTools: Set<String> = []
    var openThoughts: Set<String> = []
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
    var state: AgentSessionState? { transcript?.state }
    var ended: Bool { state?.isEnded ?? false }

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
            if e.looksLikeAuth { signInCommand = transcript?.state.signIn(provider: providers.first { $0.id == provider }, lastError: e.message) }
        } catch {
            self.error = workspace.describe(error)
        }
    }

    private func attach(_ id: String) {
        stop()
        let f = AgentSessionFeed(client: client, sessionID: id)
        feed = f
        tasks.append(Task { await f.run() })
        tasks.append(workspace.events.deliver(to: f)) // /ws/events session frames, catch-up after a gap
        tasks.append(Task { [weak self] in
            for await t in await f.updates() {
                guard let self else { return }
                self.transcript = t
                self.signInCommand = t.state.signIn(provider: self.provider, lastError: nil)
            }
        })
    }

    func stop() {
        tasks.forEach { $0.cancel() }
        tasks = []
    }

    func catchUp() async { await feed?.catchUp() }

    func send() async {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        let files = attachments
        guard !text.isEmpty || !files.isEmpty, let feed else { return }
        draft = ""
        attachments = []
        do {
            if files.isEmpty {
                _ = try await feed.send(text)
            } else {
                _ = try await feed.send(text, attachments: files.map(\.prompt))
            }
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
            }
        } catch { self.error = describe(error) }
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

    var body: some View {
        Group {
            if let m = model {
                if m.sessionID == nil { AgentLauncher(model: m) } else { session(m) }
            } else {
                ProgressView()
            }
        }
        .navigationTitle(Text(verbatim: model?.state?.title ?? "Agent" + (cwd.map { " · \(TileInfo.humanize($0))" } ?? "")))
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
            TranscriptView(follow: true) {
                if let t = m.transcript {
                    ForEach(t.items) { item in AgentItemView(model: m, item: item) }
                    if let a = AgentChat.activity(t.activity(ended: m.ended)) { ActivityView(activity: a) }
                }
            }
            if let e = m.error {
                Text(verbatim: e).font(.footnote).foregroundStyle(.red).padding(.horizontal).onTapGesture { m.error = nil }
            }
            composer(m)
        }
        .modifier(AttachPickers(picker: m.picker))
        .toolbar {
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
                    Button("Full screen", systemImage: "arrow.up.left.and.arrow.down.right") { fullScreen.toggle() }
                    Button("Terminal on this tile", systemImage: "apple.terminal") {
                        if let c = m.cwd { workspace.open(.terminal(cwd: c, session: nil), in: nav) }
                    }
                    Button("End session", systemImage: "stop.circle", role: .destructive) { Task { await m.end() } }
                } label: { Image(systemName: "ellipsis.circle") }
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

/// One transcript item as a renderer chat component.
struct AgentItemView: View {
    let model: AgentScreenModel
    let item: TranscriptItem

    var body: some View {
        let status = model.state?.status ?? .idle
        let ended = model.ended
        switch item {
        case .message(let msg):
            MessageView(message: AgentChat.message(msg, status: status, ended: ended,
                                                   agentName: model.state?.agent?.name ?? model.provider?.name ?? "Agent",
                                                   blocks: { model.memo.blocks(id: msg.id, text: $0) }),
                        onLink: { url in UIApplication.shared.open(url) })
        case .thought(let t):
            ThinkingView(thinking: AgentChat.thinking(t, status: status, ended: ended),
                         isOpen: Binding(get: { model.openThoughts.contains(t.id) },
                                         set: { if $0 { model.openThoughts.insert(t.id) } else { model.openThoughts.remove(t.id) } }))
        case .tool(let t):
            ToolCardView(card: AgentChat.toolCard(t),
                         isOpen: Binding(get: { model.openTools.contains(t.id) },
                                         set: { if $0 { model.openTools.insert(t.id) } else { model.openTools.remove(t.id) } }),
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
                    ForEach(tool.children) { c in AgentItemView(model: model, item: c) }
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
