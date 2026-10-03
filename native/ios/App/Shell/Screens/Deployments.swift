import Observation
import SwiftUI
import XbinCore
import XbinRenderer
import XbinTerm

/// A tile's live reload and deployments in the app (D132; D119, D127,
/// D131): the state (`GET /api/xbin/deployments?tile=`), kept live by the
/// `deployments` events, and the operations the app runs — pause, resume,
/// Reload now, attach, roll back, a deployment's branch — each a dry run
/// first, confirmed with the server's `impact` in the web's words
/// (XbinCore DeployView), as the web's terminal window does. Add, promote,
/// reassign, protect and the edges stay on the web.
@MainActor
@Observable
final class DeploymentsModel {
    let workspace: WorkspaceModel
    let tile: String
    /// nil until read, and on an xbind without tile deployments (`absent`).
    private(set) var state: DeploymentsState?
    /// This xbind has no tile deployments (a plain 404 or 405).
    private(set) var absent = false
    private(set) var loaded = false
    private(set) var log: [DeployEntry] = []
    var problem: String?
    /// The last operation's result line.
    var message: String?
    private(set) var busy = false
    /// A dry run's confirmation, waiting for the user.
    var pending: Pending?
    /// A 409 because the work tree is on another branch: use it this time?
    var mismatch: Ask?
    /// A refused operation: the server's words.
    var refused: Ask?

    struct Request: Equatable {
        var op: String
        var deployment: String = ""
        var other = false
        var checkpoint = ""
        var entry: DeployEntry?
        /// The branch route: the new branch, nil to clear.
        var branch: String?
    }

    struct Pending: Identifiable, Equatable {
        let id = UUID()
        var request: Request
        var confirmation: DeployView.Confirmation
        var seq: Int?
    }

    struct Ask: Identifiable, Equatable {
        let id = UUID()
        var request: Request?
        var title: String
        var message: String
        var ok: String
    }

    init(workspace: WorkspaceModel, tile: String) {
        self.workspace = workspace
        self.tile = tile
    }

    var speaksBranches: Bool { state?.speaks(DeployView.branchesFeature) ?? false }

    /// The launcher's banner and what a new session calls (nil in the zero
    /// state, for readers, and without tile deployments).
    var launcherInfo: DeployView.Launcher? { state.flatMap(DeployView.launcher) }

    // MARK: Reading

    func load() async {
        do {
            let r = try await workspace.auth.send(APIRequest("GET", DeploymentsRoute.state(tile: tile)))
            if DeploymentsRoute.absent(status: r.status) {
                absent = true
                state = nil
            } else if r.isSuccess, let s = DeploymentsState(json: try r.json()) {
                absent = false
                state = s
                problem = nil
                await loadLog()
            } else {
                problem = APIError(r).description
            }
        } catch {
            problem = workspace.describe(error)
        }
        loaded = true
    }

    func loadLog() async {
        guard let s = state, s.record, s.view != "reader",
              let j = try? await workspace.auth.json(APIRequest("GET", DeploymentsRoute.log(tile: tile, limit: 30))) else {
            log = []
            return
        }
        log = j["entries"]?.arrayValue?.compactMap { DeployEntry(json: $0) } ?? []
    }

    /// Follows the tile's `deployments` events until cancelled: a save's
    /// count moves in place, anything else re-reads the state.
    func follow() async {
        for await ev in workspace.events.deployments(of: tile) {
            guard let ev else {
                await load()
                continue
            }
            let (next, refetch) = DeployView.apply(event: ev.op, changed: ev.changed, to: state)
            if refetch || state == nil { await load() } else { state = next }
            if ev.op == "deploy" { await loadLog() }
        }
    }

    // MARK: Operations

    private func body(_ r: Request, seq: Int?, other: Bool, dryRun: Bool) -> JSONValue {
        var b: [String: JSONValue] = ["tile": .string(tile)]
        switch r.op {
        case "resume", "attach":
            if !r.deployment.isEmpty { b["deployment"] = .string(r.deployment) }
        case "rollback", "undo":
            b["deployment"] = .string(r.deployment)
            if !r.checkpoint.isEmpty { b["checkpoint"] = .string(r.checkpoint) }
        case "branch":
            b["deployment"] = .string(r.deployment)
            b["branch"] = r.branch.map { .string($0) } ?? .null
        default:
            break
        }
        if let seq { b["seq"] = .int(Int64(seq)) }
        // Strict bodies: the D131 fields go only to an xbind that lists branches/1.
        if other, speaksBranches { b["confirm"] = .string("other-branch") }
        if dryRun { b["dryRun"] = .bool(true) }
        return .object(b)
    }

    private func post(_ op: String, _ body: JSONValue) async throws -> APIResponse {
        try await workspace.auth.send(APIRequest.json("POST", DeploymentsRoute.op(op), body))
    }

    private static func errorText(_ r: APIResponse) -> String {
        (try? r.json())?["error"]?.stringValue ?? APIError(r).description
    }

    /// Starts `r`: a dry run of the exact request renders the confirmation
    /// (`pending`); a 409 because the record or the code moved re-reads the
    /// state and runs it again; a branch mismatch asks to use the work
    /// tree's branch this time (`mismatch`); any other refusal shows the
    /// server's text (`refused`).
    func start(_ r: Request) async {
        guard !busy, let s = state else { return }
        busy = true
        message = nil
        defer { busy = false }
        let other = r.other
        var cur = s
        for attempt in 0..<3 {
            do {
                let dry = try await post(r.op, body(r, seq: cur.seq, other: other, dryRun: true))
                if !dry.isSuccess {
                    let err = Self.errorText(dry)
                    if DeployView.conflict(status: dry.status, error: err) != nil, attempt < 2 {
                        await load()
                        cur = state ?? cur
                        continue
                    }
                    if !other, let m = DeployView.Branch.mismatch(err) {
                        let q = DeployView.Branch.mismatchDialog(m, error: err)
                        var again = r
                        again.other = true
                        mismatch = Ask(request: again, title: q.title, message: q.message, ok: q.ok)
                        return
                    }
                    let f = DeployView.refusal(r.op, error: err)
                    refused = Ask(title: f.title, message: f.message, ok: "OK")
                    return
                }
                let j = try dry.json()
                if let st = j["state"].flatMap(DeploymentsState.init(json:)), st.tile == tile {
                    cur = st
                    state = st
                }
                let c = DeployView.confirmation(r.op, state: cur, impact: DeployImpact(json: j["impact"]), deployment: r.deployment,
                                                checkpoint: r.checkpoint, entry: r.entry, branch: r.branch)
                var rr = r
                rr.other = other
                pending = Pending(request: rr, confirmation: c, seq: cur.seq)
                return
            } catch {
                problem = workspace.describe(error)
                return
            }
        }
    }

    /// The user confirmed: the request, with the dry run's seq and what its
    /// confirmation adds (Reload now's reviewed checkpoint).
    func confirm(_ p: Pending) async {
        pending = nil
        busy = true
        defer { busy = false }
        let prev = state
        var b = body(p.request, seq: p.seq, other: p.request.other, dryRun: false)
        if case .object(var o) = b {
            for (k, v) in p.confirmation.send { o[k] = v }
            b = .object(o)
        }
        do {
            let r = try await post(p.request.op, b)
            if !r.isSuccess {
                let err = Self.errorText(r)
                if DeployView.conflict(status: r.status, error: err) != nil {
                    await load()
                    await start(p.request)
                    return
                }
                if !p.request.other, let m = DeployView.Branch.mismatch(err) {
                    let q = DeployView.Branch.mismatchDialog(m, error: err)
                    var again = p.request
                    again.other = true
                    mismatch = Ask(request: again, title: q.title, message: q.message, ok: q.ok)
                    return
                }
                let f = DeployView.refusal(p.request.op, error: err)
                refused = Ask(title: f.title, message: f.message, ok: "OK")
                return
            }
            let j = try r.json()
            let st = j["state"].flatMap(DeploymentsState.init(json:))
            if let st, st.tile == tile { state = st } else { await load() }
            message = DeployView.result(p.request.op, state: st ?? state, deploy: DeployEntry(json: j["deploy"]),
                                        unchanged: j["unchanged"]?.boolValue == true, deployment: p.request.deployment, prev: prev)
            await loadLog()
        } catch {
            problem = workspace.describe(error)
        }
    }

    func cancelPending() { pending = nil }
}

/// The Deployments tool (D132): live reload's sentence and actions, the
/// offers to follow a branch switch (D131), the deployments with their
/// tags ("Dev API": what the last session tab's API calls reach, D129;
/// "live reload": where saves go), a deployment's overview — its view
/// link, its Branch row with Set and Clear — and its deploy log with Roll
/// back. What stays on the web says so.
struct DeploymentsToolView: View {
    let model: DeploymentsModel
    let sessions: TileWorkspaceModel

    @Environment(WorkspaceNav.self) private var nav
    @State private var selected: String?
    @State private var settingBranch: String?
    @State private var branchName = ""
    @State private var addFor: String?

    var body: some View {
        List {
            if let s = model.state {
                liveReload(s)
                deployments(s)
                if let name = selected ?? s.deployments.first(where: { !$0.primary })?.name ?? s.deployments.first?.name,
                   s.deployment(name) != nil {
                    detail(s, name)
                }
                web
            } else if model.absent {
                Section {
                    Label("This workspace's xbind has no tile deployments.", systemImage: "info.circle")
                        .foregroundStyle(.secondary)
                } footer: {
                    Text("Live reload follows every save, as before.")
                }
            } else if !model.loaded {
                HStack { Spacer(); ProgressView(); Spacer() }
            }
            if let p = model.problem {
                Section { Label { Text(verbatim: p) } icon: { Image(systemName: XbinGlyphs.symbol("error")) }.foregroundStyle(XbinColor.danger) }
            }
        }
        .listStyle(.insetGrouped)
        .overlay { if model.busy { ProgressView().controlSize(.large) } }
        .refreshable { await model.load() }
        .alert(branchDialog?.title ?? "",
               isPresented: Binding(get: { settingBranch != nil && model.state != nil }, set: { if !$0 { settingBranch = nil } })) {
            TextField("feature/x", text: $branchName)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
            Button("Cancel", role: .cancel) { settingBranch = nil }
            Button("Assign branch") {
                let name = branchName.trimmingCharacters(in: .whitespaces)
                if let d = settingBranch {
                    if DeployView.Branch.nameOK(name) {
                        Task { await model.start(.init(op: "branch", deployment: d, branch: name)) }
                    } else {
                        model.refused = .init(title: "That isn't a branch name", message: DeployView.Branch.badName, ok: "OK")
                    }
                }
                settingBranch = nil
            }
        } message: {
            Text(verbatim: branchDialog?.message ?? "")
        }
        .alert("Add a deployment for \(addFor ?? "")", isPresented: Binding(get: { addFor != nil }, set: { if !$0 { addFor = nil } })) {
            Button("Cancel", role: .cancel) { addFor = nil }
            Button("Open the web shell") { addFor = nil; openWeb() }
        } message: {
            Text("Adding a deployment stays on the web: in \(model.tile)'s terminal window, Deployments → Add deployment, with Branch: current (\(addFor ?? "")) and live reload attached.")
        }
    }

    /// The Set branch… form of the deployment being set.
    private var branchDialog: (title: String, message: String, value: String)? {
        guard let s = model.state, let d = settingBranch else { return nil }
        return DeployView.Branch.dialog(s, d)
    }

    // MARK: Live reload

    @ViewBuilder private func liveReload(_ s: DeploymentsState) -> some View {
        let f = DeployView.facts(s)
        Section {
            Text(verbatim: DeployView.header(s))
                .font(.subheadline)
                .foregroundStyle(f.paused ? Color.primary : Color.primary)
                .accessibilityIdentifier("live-reload-header")
            ForEach(DeployView.offers(s)) { o in
                Button {
                    if o.op == "addFor" { addFor = o.branch } else {
                        Task { await model.start(.init(op: o.op, deployment: o.deployment, other: o.other)) }
                    }
                } label: {
                    Label { Text(verbatim: o.label) } icon: { Image(systemName: "arrow.triangle.branch") }
                }
                .tint(XbinColor.accent)
                .disabled(!o.enabled || model.busy)
                .accessibilityHint(o.enabled ? o.title : o.why)
            }
            ForEach(DeployView.actions(s, undo: model.log.first { $0.deployment == f.last })) { a in
                action(a)
            }
            if let m = model.message {
                Text(verbatim: m).font(.footnote).foregroundStyle(.secondary)
            }
        } header: {
            HStack(spacing: 10) {
                Label(f.paused ? "Live reload paused" : "Live reload",
                      systemImage: XbinGlyphs.symbol(f.paused ? DeployView.Glyph.pinned : DeployView.Glyph.attached))
                if !f.branch.need.isEmpty || !(f.branch.workTreeBranch ?? "").isEmpty, let w = f.branch.workTreeBranch, !w.isEmpty {
                    Label { Text(verbatim: w).monospaced() } icon: { Image(systemName: XbinGlyphs.symbol("branch")) }
                        .accessibilityLabel(Text("branch \(w)"))
                }
            }
        }
    }

    @ViewBuilder private func action(_ a: DeployView.Action) -> some View {
        if a.items.isEmpty {
            Button {
                Task { await model.start(.init(op: a.op, deployment: a.deployment)) }
            } label: {
                VStack(alignment: .leading, spacing: 2) {
                    if a.op == "reloadNow" {
                        Label { Text(verbatim: a.label) } icon: { Image(systemName: XbinGlyphs.symbol(DeployView.Glyph.reloadNow)) }
                    } else {
                        Text(verbatim: a.label)
                    }
                    if !a.enabled, !a.why.isEmpty { Text(verbatim: a.why).font(.caption).foregroundStyle(.secondary) }
                }
            }
            .disabled(!a.enabled || model.busy)
        } else {
            Menu {
                ForEach(a.items) { it in
                    Button {
                        Task { await model.start(.init(op: it.op, deployment: it.deployment)) }
                    } label: {
                        Text(verbatim: it.label)
                        if !it.title.isEmpty { Text(verbatim: it.title) }
                    }
                    .disabled(!it.enabled)
                }
            } label: {
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: a.label + "…")
                    if !a.enabled, !a.why.isEmpty { Text(verbatim: a.why).font(.caption).foregroundStyle(.secondary) }
                }
            }
            .disabled(!a.enabled || model.busy)
        }
    }

    // MARK: Deployments

    @ViewBuilder private func deployments(_ s: DeploymentsState) -> some View {
        let rows = DeployView.rows(s, target: sessions.devAPITarget)
        Section {
            ForEach(rows) { r in
                Button { selected = r.name } label: { row(s, r) }
                    .tint(.primary)
                    .accessibilityIdentifier("deployment:\(r.name)")
            }
        } header: {
            Text("Deployments")
        } footer: {
            if let l = model.launcherInfo, sessions.devAPITarget == nil {
                Text(verbatim: "New sessions \(l.subtitle.replacingOccurrences(of: "· target: ", with: "call "))")
            }
        }
    }

    private func row(_ s: DeploymentsState, _ r: DeployView.Row) -> some View {
        let current = (selected ?? s.deployments.first(where: { !$0.primary })?.name) == r.name
        return VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Text(verbatim: r.name).font(.body.weight(current ? .semibold : .regular))
                if r.primary { pill("primary", XbinColor.muted) }
                if r.protected { pill("protected", XbinColor.muted, glyph: DeployView.Glyph.shield) }
                if r.target { pill(DeployView.tagDevAPI, XbinColor.accent).accessibilityHint(DeployView.devAPITitle(s, r.name)) }
                if r.liveReload {
                    pill(DeployView.tagLiveReload, XbinColor.accent, glyph: DeployView.tagLiveReloadIcon)
                        .accessibilityHint(DeployView.liveReloadTitle(s, r.name))
                }
                Spacer(minLength: 0)
                if r.lastDeployFailed {
                    // Status: glyph, word and colour.
                    Label("deploy failed", systemImage: XbinGlyphs.symbol("error")).font(.caption.weight(.semibold))
                        .foregroundStyle(XbinColor.danger)
                }
            }
            HStack(spacing: 8) {
                Label { Text(verbatim: r.code).font(.caption.monospaced()) } icon: {
                    Image(systemName: XbinGlyphs.symbol(r.codeIcon)).font(.caption2)
                }
                if !r.status.isEmpty { Text(verbatim: r.status).font(.caption) }
                if !r.branch.isEmpty {
                    Label { Text(verbatim: r.branch).font(.caption.monospaced()) } icon: {
                        Image(systemName: XbinGlyphs.symbol("branch")).font(.caption2)
                    }
                    .accessibilityLabel(Text("branch \(r.branch)"))
                }
            }
            .foregroundStyle(XbinColor.muted)
        }
        .padding(.vertical, 2)
    }

    /// A tag (Base Two's badges: square, a 1 pt edge, the glyph before
    /// the word).
    private func pill(_ text: String, _ color: Color, glyph: String? = nil) -> some View {
        HStack(spacing: 3) {
            if let glyph { Image(systemName: XbinGlyphs.symbol(glyph)).font(.system(size: 9, weight: .semibold)) }
            Text(verbatim: text).font(.caption2.bold()).lineLimit(1)
        }
        .fixedSize()
        .padding(.horizontal, 6).padding(.vertical, 2)
        .overlay(RoundedRectangle.xbinPlate.strokeBorder(color, lineWidth: 1))
        .foregroundStyle(color)
    }

    // MARK: A deployment

    @ViewBuilder private func detail(_ s: DeploymentsState, _ name: String) -> some View {
        let lines = DeployView.overview(s, name, entry: model.log.first)
        let row = DeployView.rows(s).first { $0.name == name }
        Section {
            ForEach(Array(lines.enumerated()), id: \.offset) { _, l in
                HStack(alignment: .firstTextBaseline, spacing: 10) {
                    Text(verbatim: l.0).font(.footnote).foregroundStyle(.secondary)
                    Spacer(minLength: 8)
                    Text(verbatim: l.1).font(.footnote).multilineTextAlignment(.trailing)
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier(l.0 == "branch" ? "branch-row" : "overview-\(l.0)")
            }
            ForEach(DeployView.Branch.actions(s, name)) { a in
                Button {
                    if a.id == "clearBranch" {
                        Task { await model.start(.init(op: "branch", deployment: name, branch: nil)) }
                    } else {
                        branchName = DeployView.Branch.dialog(s, name).value
                        settingBranch = name
                    }
                } label: {
                    Label { Text(verbatim: a.label) } icon: { Image(systemName: a.id == "clearBranch" ? "xmark" : "arrow.triangle.branch") }
                }
                .disabled(!a.enabled || model.busy)
            }
            Button {
                sessions.showLogs(deployment: name)
            } label: {
                Label("Logs", systemImage: "list.bullet.rectangle")
            }
            .accessibilityIdentifier("deployment-logs")
            if let row {
                Button {
                    // The deployment's own URL, signed in, in the app's web view (D125).
                    let path = row.primary ? model.tile : "\(model.tile)+\(name)"
                    model.workspace.open(.tile(path), in: nav)
                } label: {
                    Label { Text(verbatim: "Open \(row.primary ? model.tile : "\(model.tile)+\(name)")") } icon: { Image(systemName: "safari") }
                }
                .accessibilityIdentifier("open-deployment")
            }
        } header: {
            Text(verbatim: name)
        }
        let entries = model.log.filter { $0.deployment == name || ($0.deployment.isEmpty && name == s.primary) }
        if !entries.isEmpty {
            Section("Deploy log") {
                ForEach(DeployView.logRows(s, name, Array(entries.prefix(12)))) { e in
                    VStack(alignment: .leading, spacing: 3) {
                        HStack(spacing: 6) {
                            Label { Text(verbatim: e.code).font(.caption.monospaced()) } icon: {
                                Image(systemName: XbinGlyphs.symbol(e.codeIcon)).font(.caption2)
                            }
                            Text(verbatim: e.how).font(.caption.weight(.semibold))
                            if e.state == "running" { pill("runs now", XbinColor.ok, glyph: "ok") }
                            Spacer(minLength: 0)
                            Text(verbatim: e.result).font(.caption).foregroundStyle(e.failed ? XbinColor.danger : XbinColor.muted).lineLimit(2)
                        }
                        HStack(spacing: 6) {
                            Text(verbatim: e.who)
                            if !e.branch.isEmpty {
                                Label { Text(verbatim: e.branch).monospaced() } icon: { Image(systemName: XbinGlyphs.symbol("branch")) }
                            }
                        }
                        .font(.caption2).foregroundStyle(XbinColor.muted)
                        if let rb = e.rollback {
                            Button(e.rollbackLabel) {
                                let entry = entries.first { $0.id == e.id }
                                Task { await model.start(.init(op: "rollback", deployment: name, checkpoint: e.checkpoint, entry: entry)) }
                            }
                            .font(.caption.weight(.semibold))
                            .buttonStyle(.borderless)
                            .disabled(!rb.enabled || model.busy)
                        }
                    }
                }
            }
        }
    }

    // MARK: The web

    private var web: some View {
        Section {
            Button { openWeb() } label: {
                Label("Manage on the web", systemImage: "globe")
            }
        } footer: {
            Text("Adding, promoting, reassigning the primary, protecting it and the edges stay in the tile's terminal window on the web.")
        }
    }

    /// The web shell, signed in, in the app's web view (D125).
    private func openWeb() {
        model.workspace.open(.tile("root"), in: nav)
    }
}

/// The launcher's live reload banner (deploy-state.js `launcher`): the
/// warn tint while paused, with Reload now.
struct DeployBannerView: View {
    let banner: DeployView.Banner
    let model: DeploymentsModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label { Text(verbatim: banner.text).font(.footnote) } icon: {
                Image(systemName: XbinGlyphs.symbol(banner.tone == "paused" ? DeployView.Glyph.pinned : DeployView.Glyph.attached))
                    .foregroundStyle(banner.tone == "paused" ? XbinColor.warn : XbinColor.accent)
            }
            if banner.reloadNow {
                Button(DeployView.labelReloadNow, systemImage: XbinGlyphs.symbol(DeployView.Glyph.reloadNow)) {
                    Task { await model.start(.init(op: "reloadNow")) }
                }
                .buttonStyle(.bordered).tint(XbinColor.accent).controlSize(.small)
                .buttonBorderShape(.roundedRectangle(radius: XbinShapes.radius))
                .disabled(model.busy)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .xbinCard(banner.tone == "paused" ? XbinColor.warnBackground : XbinColor.surface)
    }
}

/// The operations' dialogs, on the sessions screen (the launcher's Reload
/// now asks too): a dry run's confirmation, a branch mismatch's question,
/// a refusal in the server's words.
struct DeploymentsAlerts: ViewModifier {
    let model: DeploymentsModel

    func body(content: Content) -> some View {
        content
            .alert(model.pending?.confirmation.title ?? "", isPresented: Binding(get: { model.pending != nil }, set: { if !$0 { model.cancelPending() } })) {
                Button("Cancel", role: .cancel) { model.cancelPending() }
                Button(model.pending?.confirmation.ok ?? "OK") {
                    if let p = model.pending { Task { await model.confirm(p) } }
                }
            } message: {
                Text(verbatim: model.pending?.confirmation.message ?? "")
            }
            .alert(model.mismatch?.title ?? "", isPresented: Binding(get: { model.mismatch != nil }, set: { if !$0 { model.mismatch = nil } })) {
                Button("Cancel", role: .cancel) { model.mismatch = nil }
                Button(model.mismatch?.ok ?? "OK") {
                    if let r = model.mismatch?.request {
                        model.mismatch = nil
                        Task { await model.start(r) }
                    }
                }
            } message: {
                Text(verbatim: model.mismatch?.message ?? "")
            }
            .alert(model.refused?.title ?? "", isPresented: Binding(get: { model.refused != nil }, set: { if !$0 { model.refused = nil } })) {
                Button("OK", role: .cancel) { model.refused = nil }
            } message: {
                Text(verbatim: model.refused?.message ?? "")
            }
    }
}
