import Foundation

// What the app's Deployments tool shows for a tile's deployments state
// (D132): a port of the web terminal window's pure view modules —
// web/deploy-state.js (the live reload sentence, its actions, the
// launcher's banner, confirmations, results, refusals, events),
// web/deploy-branch.js (assigned branches, D131: the offers to follow a
// switch, the Branch row, a mismatch's question) and web/deploy-panel.js
// (the header, the rows with their Dev API and live reload tags, a
// deployment's overview, the deploy log). The words are the web's, held
// to its node tests (hack/deploy-*.test.mjs) by DeployViewTests. The
// server decides; this renders: a control the viewer may not use is
// disabled with the server's reason, never hidden, and a fact the state
// doesn't carry is left out.
//
// The app shows the M1 operations (pause, resume, Reload now, attach), a
// roll back from the deploy log and a deployment's branch; add, promote,
// reassign, protect and the edges stay on the web.

public enum DeployView {
    public static let branchesFeature = "branches/1"

    // MARK: Small facts

    /// How a deploy entry's `by` reads: "user:ana" → "ana".
    public static func who(_ by: String) -> String {
        if by.isEmpty { return "" }
        if by == "owner" { return "the owner" }
        return by.hasPrefix("user:") ? String(by.dropFirst(5)) : by
    }

    static func parseTime(_ s: String) -> Date? {
        guard !s.isEmpty else { return nil }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = f.date(from: s) { return d }
        f.formatOptions = [.withInternetDateTime]
        return f.date(from: s)
    }

    /// "12m ago", from an RFC 3339 time ("" when unreadable).
    public static func ago(_ at: String, now: Date) -> String {
        guard let t = parseTime(at) else { return "" }
        let s = max(0, Int(now.timeIntervalSince(t)))
        if s < 60 { return "just now" }
        let m = s / 60
        if m < 60 { return "\(m)m ago" }
        let h = m / 60
        if h < 24 { return "\(h)h ago" }
        return "\(h / 24)d ago"
    }

    static func files(_ n: Int) -> String { n == 1 ? "1 file" : "\(n) files" }
    static func ships(_ n: Int) -> String { n == 1 ? "ships" : "ship" }
    static let minus = "−"
    static func stat(_ c: DeployImpact.Code) -> String { "+\(c.added) \(minus)\(c.removed)" }
    static func cp(_ s: DeploymentsState, _ name: String) -> String { s.deployment(name)?.checkpoint ?? "" }

    /// What a deployment serves now: its generation's checkpoint, else its record's.
    static func serving(_ s: DeploymentsState, _ name: String) -> String {
        let v = s.deployment(name)?.status.serving ?? ""
        if !v.isEmpty, v != "work-tree" { return v }
        if v == "work-tree" { return "its current code" }
        let c = cp(s, name)
        return c.isEmpty ? "its current code" : c
    }

    // MARK: Facts

    public struct Facts: Equatable, Sendable {
        public var zero: Bool
        public var reader: Bool
        public var primary: String
        /// The live reload target; "" paused.
        public var attached: String
        public var paused: Bool
        /// Where live reload is, or last was.
        public var last: String
        public var changed: Int?
        public var since: String
        /// A deployment whose last deploy failed ("" none).
        public var failed: String
        /// protect | deploy | promote | rollback | branch | pause
        public var cause: String
        public var branch: BranchFacts
    }

    public static func facts(_ s: DeploymentsState) -> Facts {
        let zero = !s.record
        let reader = !zero && s.view == "reader"
        let primary = s.primary.isEmpty ? "main" : s.primary
        let attached = zero ? primary : s.liveReload
        let paused = !zero && !reader && attached.isEmpty
        let last = zero ? primary : (!s.lastLiveReload.isEmpty ? s.lastLiveReload : !attached.isEmpty ? attached : primary)
        let changed = paused ? s.workTree?.changed : nil
        let wtSince = paused ? (s.workTree?.since ?? "") : ""
        let since = wtSince.isEmpty ? cp(s, last) : wtSince
        var failed = ""
        if !zero {
            let bad = s.deployments.filter { $0.lastDeploy?.result == "failed" }.map(\.name)
            failed = bad.contains(last) ? last : bad.contains(primary) ? primary : (bad.first ?? "")
        }
        let how = paused ? (s.deployment(last)?.lastDeploy?.how ?? "") : ""
        let b = branchFacts(s, zero: zero, reader: reader, attached: attached, last: last)
        let cause = how == "protect" ? "protect" : ["deploy", "promote", "rollback"].contains(how) ? how
            : (paused && b.switched && !b.need.isEmpty ? "branch" : "pause")
        return Facts(zero: zero, reader: reader, primary: primary, attached: attached, paused: paused, last: last, changed: changed,
                     since: since, failed: failed, cause: cause, branch: b)
    }

    // MARK: Permissions

    public struct Control: Equatable, Sendable {
        public var enabled: Bool
        public var why: String
        public var kind: String

        public init(enabled: Bool, why: String = "", kind: String = "") {
            self.enabled = enabled; self.why = why; self.kind = kind
        }
    }

    static func viewAsWhy(_ name: String) -> String {
        "\(name.isEmpty ? "this user" : name) may do this — you are viewing as \(name.isEmpty ? "another user" : name) (read-only)."
    }

    /// One operation's `{enabled, why, kind}`: the tile-level
    /// `caller.can[op]`, or `deployments[i].can[op]` when a deployment is
    /// named; what the tile itself may not do (`allowed`) wins.
    public static func control(_ s: DeploymentsState, _ op: String, _ name: String? = nil, viewing: String = "") -> Control {
        let allowed = op == "pause" ? s.allowed["pause"] : op == "add" ? s.allowed["deployments"] : nil
        if let a = allowed, !a.ok { return Control(enabled: false, why: a.why, kind: a.kind.isEmpty ? "policy" : a.kind) }
        let can = name.map { s.deployment($0)?.can[op] } ?? s.caller.can[op]
        guard let can, can.ok else {
            let why = can?.why.isEmpty == false ? can!.why : (s.view == "reader" ? Reason.needsWrite(s.tile) : "")
            return Control(enabled: false, why: why, kind: can?.kind ?? "")
        }
        if s.caller.readOnly { return Control(enabled: false, why: viewAsWhy(viewing), kind: "authority") }
        return Control(enabled: true)
    }

    static func reloadNowControl(_ s: DeploymentsState, _ f: Facts) -> Control {
        let c = control(s, "reloadNow")
        if c.enabled, f.changed == 0, f.failed != f.last {
            return Control(enabled: false, why: "No changes since \(f.since).", kind: "state")
        }
        return c
    }

    // MARK: The live reload sentence

    static func pausedSentence(_ s: DeploymentsState, _ f: Facts, header: Bool, now: Date) -> String {
        let L = f.last
        let pin = cp(s, L)
        let entry = s.deployment(L)?.lastDeploy
        if f.cause == "protect" {
            let by = who(entry?.by ?? s.liveReloadSince?.by ?? "")
            return "Live reload paused when \(by.isEmpty ? "a tile manager" : by) protected \(L) — \(L) is pinned to \(pin)."
        }
        if f.cause == "rollback" {
            return "Live reload paused — \(L) was rolled back to \(pin). The work tree still holds the code you rolled back from: resuming ships it again."
        }
        if f.cause == "branch" { return Branch.pausedSentence(f.branch, L, pin) }
        if f.cause == "promote" || f.cause == "deploy" {
            let from = f.cause == "promote" && !(entry?.from ?? "").isEmpty ? " from \(entry!.from)" : ""
            return "Live reload paused — \(L) received \(pin)\(from). Resuming ships the work tree to \(L)."
        }
        let since = s.liveReloadSince
        let by = since.map { who($0.by) } ?? ""
        let when = since.map { ago($0.at, now: now) } ?? ""
        let head = "Live reload paused\(by.isEmpty ? "" : " by \(by)\(since?.agent == true ? " (agent)" : "")")\(when.isEmpty ? "" : " \(when)")"
        if let n = f.changed, n > 0 {
            return "\(head) — \(files(n)) changed since \(f.since), " + (header ? "the checkpoint \(L) runs." : "which \(L) runs. Reload now ships them to \(L) once.")
        }
        return "\(head) — no changes since \(f.since)."
    }

    static func failedSentence(_ s: DeploymentsState, _ f: Facts) -> String {
        let D = f.failed
        let retry = f.paused && D == f.last ? " Reload now retries." : ""
        return "The last deploy to \(D) failed; \(D) keeps running \(serving(s, D)).\(retry)"
    }

    static func zeroSentence(_ s: DeploymentsState) -> String {
        "Live reload: \(s.primary.isEmpty ? "main" : s.primary) — every save reaches everyone using \(s.tile)."
    }

    /// The chip (deploy-state.js `chip`): nil in the zero state; its
    /// `text` (the full bar's) and `title`, the sentence.
    public struct Chip: Equatable, Sendable {
        public var text: String
        public var title: String
        public var failed: Bool
    }

    public static func chip(_ s: DeploymentsState, now: Date = Date()) -> Chip? {
        let f = facts(s)
        if f.zero { return nil }
        let P = f.primary
        var base: String, title: String
        if f.reader, s.liveReload != P {
            let pin = cp(s, P)
            base = "📌 \(P) pinned to \(pin)"
            title = "\(P) is pinned to \(pin): saves in the work tree don't reach it."
        } else if f.paused, f.cause == "branch", f.branch.off {
            base = "📌 Live reload paused · ⎇ \(f.branch.workTreeBranch ?? "no branch")"
            title = pausedSentence(s, f, header: false, now: now)
        } else if f.paused {
            let count = f.cause == "protect" ? nil : f.changed
            base = (count ?? 0) > 0 ? "📌 Live reload paused · \(count!)" : "📌 Live reload paused"
            title = pausedSentence(s, f, header: false, now: now)
        } else if f.attached == P {
            base = "● Live reload: \(P)"
            title = "Live reload: \(P) — every save reaches everyone using \(s.tile)."
        } else {
            let A = f.attached
            base = "● Live reload: \(A)"
            title = "Live reload: \(A) — saves reach \(s.tile)+\(A). The primary, \(P), is pinned to \(cp(s, P)).\(Branch.offSentence(f.branch, A))"
        }
        let failed = !f.failed.isEmpty
        return Chip(text: failed ? "\(base) · deploy failed" : base, title: failed ? "\(title) \(failedSentence(s, f))" : title, failed: failed)
    }

    // MARK: Actions (the chip's menu, the panel's header)

    /// One action: an operation on the tile or on a deployment, or a
    /// submenu of them (Resume live reload on ▸, Attach live reload to ▸).
    public struct Action: Equatable, Sendable, Identifiable {
        /// pause | reloadNow | resume | attach | addFor | undo — or, for an
        /// offer, follow/<name>, keep/<name>, addFor/<branch>.
        public var id: String
        public var op: String
        public var label: String
        public var enabled: Bool
        public var why: String
        public var title: String
        public var deployment: String
        /// Resume or attach onto the work tree's branch this time
        /// (`confirm: "other-branch"`).
        public var other: Bool
        /// addFor: the branch.
        public var branch: String
        public var items: [Action]

        public init(id: String, op: String, label: String, enabled: Bool, why: String = "", title: String = "", deployment: String = "",
                    other: Bool = false, branch: String = "", items: [Action] = []) {
            self.id = id; self.op = op; self.label = label; self.enabled = enabled; self.why = why; self.title = title
            self.deployment = deployment; self.other = other; self.branch = branch; self.items = items
        }
    }

    public static let labelPause = "Pause live reload"
    public static let labelResume = "Resume live reload on ▸"
    public static let labelAttach = "Attach live reload to ▸"
    public static let labelReloadNow = "⇡ Reload now"

    static func reloadNowTip(_ f: Facts) -> String {
        if let n = f.changed, n > 0 { return "Ship the work tree to \(f.last) once (\(files(n))); \(f.last) stays pinned." }
        return "Ship the work tree to \(f.last) once; \(f.last) stays pinned."
    }
    static func pauseTip(_ A: String) -> String {
        "Keep \(A) on the code it runs now; saves stop reaching it until Reload now or Resume live reload."
    }
    static func resumeTip(_ f: Facts, _ Y: String) -> String {
        guard Y == f.last, let n = f.changed, n > 0 else { return "\(Y) follows the work tree again." }
        return "\(Y) follows the work tree again; the \(n) changed file\(n == 1 ? "" : "s") \(ships(n)) now."
    }
    static func attachTip(_ Y: String, _ A: String) -> String { "\(Y) follows every save; \(A) is pinned to its current code." }

    static func resumeCandidates(_ s: DeploymentsState, _ f: Facts) -> [String] {
        let names = s.deployments.map(\.name).filter { !(s.protectedPrimary && $0 == f.primary) }
        return names.filter { $0 == f.last } + names.filter { $0 != f.last }
    }

    static func attachCandidates(_ s: DeploymentsState, _ f: Facts) -> [String] {
        s.deployments.map(\.name).filter { $0 != f.attached && !(s.protectedPrimary && $0 == f.primary) }
    }

    /// The actions of the live reload header (deploy-state.js `chipItems`
    /// without its header, sentence and offers): Reload now and Resume ▸
    /// while paused, else Pause and Attach ▸. A reader gets none.
    public static func actions(_ s: DeploymentsState, undo: DeployEntry? = nil) -> [Action] {
        let f = facts(s)
        guard !f.reader else { return [] }
        var out: [Action] = []
        if f.paused {
            let rc = reloadNowControl(s, f)
            out.append(Action(id: "reloadNow", op: "reloadNow", label: (f.changed ?? 0) > 0 ? "\(labelReloadNow) · \(f.changed!)" : labelReloadNow,
                              enabled: rc.enabled, why: rc.why, title: reloadNowTip(f)))
            let rs = control(s, "resume")
            let sub = resumeCandidates(s, f).map {
                Action(id: "resume/\($0)", op: "resume", label: $0, enabled: rs.enabled, why: rs.why, title: resumeTip(f, $0), deployment: $0)
            }
            out.append(Action(id: "resume", op: "resume", label: labelResume, enabled: sub.contains { $0.enabled },
                              why: sub.isEmpty ? (rs.why.isEmpty ? "\(f.primary) is protected: live reload can't attach to it." : rs.why) : rs.why,
                              items: sub))
        } else {
            let pc = control(s, "pause")
            out.append(Action(id: "pause", op: "pause", label: labelPause, enabled: pc.enabled, why: pc.why, title: pauseTip(f.attached)))
            let others = f.zero ? [] : attachCandidates(s, f)
            if !others.isEmpty {
                let sub = others.map { y -> Action in
                    let c = control(s, "attach", y)
                    return Action(id: "attach/\(y)", op: "attach", label: y, enabled: c.enabled, why: c.why, title: attachTip(y, f.attached), deployment: y)
                }
                let any = sub.contains { $0.enabled }
                out.append(Action(id: "attach", op: "attach", label: labelAttach, enabled: any, why: any ? "" : sub[0].why, items: sub))
            }
        }
        // Undo after a code move onto the last target (deploy-panel.js panelHeader).
        if f.paused, ["deploy", "promote", "rollback"].contains(f.cause), let u = undo, !u.previous.isEmpty, u.deployment == f.last, u.result == "ok" {
            let c = control(s, "rollback", f.last)
            out.append(Action(id: "undo", op: "undo", label: "Undo: roll \(f.last) back to \(u.previous)", enabled: c.enabled, why: c.why,
                              title: "Put back the code \(f.last) ran before the last move.", deployment: f.last))
        }
        return out
    }

    /// The offers to follow a branch switch (D131), each judged as the
    /// operation it starts would be.
    public static func offers(_ s: DeploymentsState) -> [Action] {
        let f = facts(s)
        return Branch.offers(s, f) { op, name in control(s, op, name) }
    }

    /// The header sentence (deploy-panel.js `panelHeader`'s text).
    public static func header(_ s: DeploymentsState, now: Date = Date()) -> String {
        let f = facts(s), P = f.primary
        var text: String
        if f.zero { text = zeroSentence(s) }
        else if f.reader, s.liveReload != P { text = "\(P) is pinned to \(cp(s, P))." }
        else if f.paused { text = pausedSentence(s, f, header: true, now: now) }
        else if f.attached == P { text = "Live reload: \(P) (primary) — every save reaches everyone using \(s.tile)." }
        else {
            text = "Live reload: \(f.attached) — saves reach \(s.tile)+\(f.attached). The primary, \(P), is pinned to \(cp(s, P))."
                + Branch.offSentence(f.branch, f.attached)
        }
        if !f.failed.isEmpty, !f.zero {
            let t = text.hasSuffix(".") ? String(text.dropLast()) : text
            text = "\(t) — the last deploy to \(f.failed) failed; \(f.failed) keeps running \(serving(s, f.failed))."
        }
        return text
    }

    // MARK: The launcher

    /// The launcher's lines (deploy-state.js `launcher`): nil in the zero
    /// state and for readers.
    public struct Launcher: Equatable, Sendable {
        public var banner: Banner?
        /// Ends each session box's line: what a new session calls.
        public var subtitle: String
        /// Saves and new sessions go to different places.
        public var note: String?
    }

    public struct Banner: Equatable, Sendable {
        public var text: String
        /// paused (amber) | plain
        public var tone: String
        public var reloadNow: Bool
    }

    public static func launcher(_ s: DeploymentsState) -> Launcher? {
        let f = facts(s)
        if f.zero || f.reader { return nil }
        let P = f.primary
        var banner: Banner?
        if f.paused {
            let text: String
            if f.cause == "protect" { text = "Live reload is paused: \(f.last) is protected." }
            else if f.cause == "branch" { text = Branch.launcherText(f.branch, f.last) }
            else if let n = f.changed, n > 0 { text = "Live reload is paused: \(files(n)) changed since \(f.since), the checkpoint \(f.last) runs." }
            else { text = "Live reload is paused: no changes since \(f.since)." }
            banner = Banner(text: text, tone: "paused", reloadNow: reloadNowControl(s, f).enabled)
        } else if f.attached != P {
            banner = Banner(text: "Live reload: \(f.attached) — saves reach \(s.tile)+\(f.attached); \(P) is pinned to \(cp(s, P)).", tone: "plain", reloadNow: false)
        }
        if var b = banner, !f.failed.isEmpty {
            b.text = (b.text.hasSuffix(".") ? String(b.text.dropLast()) : b.text) + ", and the last deploy to \(f.failed) failed."
            banner = b
        }
        let def = defaultTarget(s)
        let subtitle = def == "primary" ? "· target: \(P)"
            : def == "off" ? "· tile API off (\(P) is protected and live reload is paused)"
            : "· target: \(def) (\(P) is protected)"
        let calls = def == "primary" ? P : def
        let note = !f.attached.isEmpty && def != "off" && calls != f.attached
            ? "Saves reach \(f.attached); new sessions call \(calls). Switch in the tile API select after starting." : nil
        return Launcher(banner: banner, subtitle: subtitle, note: note)
    }

    /// What a new session calls: "primary"; the live reload target while
    /// the primary is protected; "off" when that is paused too.
    public static func defaultTarget(_ s: DeploymentsState) -> String {
        guard s.record, s.protectedPrimary else { return "primary" }
        let A = s.liveReload
        return !A.isEmpty && A != (s.primary.isEmpty ? "main" : s.primary) ? A : "off"
    }

    /// A session's target from its echo (`api`, `deployment`): "off", a
    /// deployment's name, or "primary".
    public static func sessionTarget(api: Bool, deployment: String) -> String {
        !api ? "off" : deployment.isEmpty ? "primary" : deployment
    }

    // MARK: Rows, overview, log (deploy-panel.js)

    public static let tagDevAPI = "Dev API"
    public static let tagLiveReload = "● live reload"

    public static func devAPITitle(_ s: DeploymentsState, _ name: String) -> String {
        let P = s.primary.isEmpty ? "main" : s.primary
        let reach = name == P ? "\(name), the primary (XBIN_DEPLOYMENT is unset)" : "\(s.tile)+\(name) (XBIN_DEPLOYMENT=\(name))"
        return "Dev API: this tab's API calls and bx commands reach \(reach). The tile API select switches it; switching restarts the session."
    }

    public static func liveReloadTitle(_ s: DeploymentsState, _ name: String) -> String {
        let P = s.primary.isEmpty ? "main" : s.primary
        return "Live reload: saves reach \(name == P ? "\(s.tile) (\(name), the primary)" : "\(s.tile)+\(name)")."
    }

    public struct Row: Equatable, Sendable, Identifiable {
        public var name: String
        public var id: String { name }
        public var primary: Bool
        public var protected: Bool
        /// "📌 c:3f2a1c9" or "● work tree".
        public var code: String
        public var status: String
        /// The Dev API tag: the session's target is this deployment.
        public var target: Bool
        public var liveReload: Bool
        public var lastDeployFailed: Bool
        public var branch: String
        public var branchOverride: String
        /// Where people open it (`/c/<tile>+<name>/`, the primary's bare).
        public var url: String
    }

    static func pointer(_ d: DeploymentInfo?) -> String {
        guard let d, !d.checkpoint.isEmpty else { return "● work tree" }
        return "📌 \(d.checkpoint)"
    }

    static func statusText(_ d: DeploymentInfo?) -> String {
        guard let st = d?.status else { return "" }
        let moving = st.deploying ? "deploying\(st.deployingCheckpoint.isEmpty ? "" : " \(st.deployingCheckpoint)")…" : st.queued > 0 ? "queued" : ""
        return [st.state == "crash-looping" ? "failed" : st.state, moving].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    /// The deployments, primary first; `target`: the session's target
    /// ("primary", a name, "off"; nil: no session).
    public static func rows(_ s: DeploymentsState, target: String? = nil) -> [Row] {
        let P = s.primary.isEmpty ? "main" : s.primary
        let t = target == "primary" ? P : target
        return s.deployments.sorted { a, b in
            if (a.name == P) != (b.name == P) { return a.name == P }
            return a.name < b.name
        }.map { d in
            Row(name: d.name, primary: d.name == P, protected: d.name == P && s.protectedPrimary, code: pointer(d), status: statusText(d),
                target: t != nil && t == d.name, liveReload: s.record && !s.liveReload.isEmpty && s.liveReload == d.name,
                lastDeployFailed: d.lastDeploy?.result == "failed", branch: d.branch, branchOverride: d.branchOverride,
                url: !d.url.isEmpty ? d.url : "/c/\(s.tile)\(d.name == P ? "" : "+\(d.name)")/")
        }
    }

    static let howWords: [String: String] = ["deploy": "deploy", "promote": "promote", "rollback": "roll back", "reload-now": "reload now",
                                             "resume": "resume live reload", "pause": "live reload paused", "attach": "live reload attached",
                                             "add": "added", "protect": "protected", "reassign": "primary reassigned", "restart": "restart"]
    static let codeMoves: Set<String> = ["deploy", "promote", "rollback", "reload-now"]

    static func howPhrase(_ e: DeployEntry) -> String {
        switch e.how {
        case "promote": return e.from.isEmpty ? "promoted" : "promoted from \(e.from)"
        case "rollback": return "rolled back"
        case "reload-now": return "Reload now"
        default: return "deployed"
        }
    }

    /// A deployment's overview lines ([label, text]); `entry`: the newest
    /// deploy-log entry, which says how its code got there.
    public static func overview(_ s: DeploymentsState, _ name: String, entry: DeployEntry? = nil, now: Date = Date()) -> [(String, String)] {
        guard let d = s.deployment(name) else { return [] }
        var lines: [(String, String)] = []
        let e = entry?.deployment == name ? entry : d.lastDeploy
        var how = ""
        if let e, !e.how.isEmpty { how = codeMoves.contains(e.how) ? howPhrase(e) : (howWords[e.how] ?? e.how) }
        let at = [e?.finishedAt, e?.at, e?.requestedAt].compactMap { $0 }.first { !$0.isEmpty } ?? ""
        if d.primary { lines.append(("primary", "primary — everything from outside reaches it\(s.protectedPrimary ? " · 🛡 protected" : "")")) }
        var code = pointer(d)
        if !how.isEmpty {
            code += " · \(how)"
            if let by = e?.by, !by.isEmpty { code += " by \(who(by))" }
            if !at.isEmpty { code += ", \(ago(at, now: now))" }
        }
        lines.append(("code", code))
        lines.append(("status", statusText(d) + (d.status.error.isEmpty ? "" : " — \(d.status.error)")))
        if let bl = Branch.overviewLine(s, name) { lines.append(("branch", bl)) }
        if d.lastDeploy?.result == "failed" { lines.append(("last deploy", "last deploy failed — the deploy log has its error")) }
        return lines
    }

    public struct LogRow: Equatable, Sendable, Identifiable {
        public var id: String
        public var checkpoint: String
        public var code: String
        public var how: String
        public var result: String
        public var who: String
        public var feed: String
        public var branch: String
        /// "running": the entry the deployment runs now.
        public var state: String
        /// Roll back to this entry's checkpoint (older ok entries).
        public var rollback: Control?
        public var rollbackLabel: String
        public var failed: Bool
    }

    static func same(_ a: String, _ b: String) -> Bool { !a.isEmpty && !b.isEmpty && (a.hasPrefix(b) || b.hasPrefix(a)) }

    /// The deploy log (deploy-panel.js `logRows`): the entry that runs
    /// now, Roll back on older ok ones.
    public static func logRows(_ s: DeploymentsState, _ name: String, _ entries: [DeployEntry], now: Date = Date()) -> [LogRow] {
        let d = s.deployment(name)
        let cur = d?.checkpoint ?? ""
        var found = false
        return entries.map { e in
            let running = !found && e.result == "ok" && (e.followsWorkTree ? (d?.liveReload ?? false) : same(e.checkpoint, cur))
            let back = e.result == "ok" && !e.followsWorkTree && !e.checkpoint.isEmpty && !same(e.checkpoint, cur)
            found = found || running
            let how = (howWords[e.how] ?? e.how) + (e.how == "promote" && !e.from.isEmpty ? " (from \(e.from))" : "")
            let result = e.result == "failed" ? "failed\(e.error.isEmpty ? "" : " — \(e.error)")"
                : e.result == "running" ? "running\(e.phase.isEmpty ? "" : " · \(e.phase)")" : e.result
            let when = ago(e.finishedAt.isEmpty ? e.requestedAt : e.finishedAt, now: now)
            return LogRow(id: e.id, checkpoint: e.checkpoint, code: e.followsWorkTree || e.checkpoint.isEmpty ? "● work tree" : "📌 \(e.checkpoint)",
                          how: how, result: result, who: "\(who(e.by))\(e.agent ? " (agent)" : "") · \(when)",
                          feed: e.feed.isEmpty || e.feed == "work-tree" ? "work tree" : e.feed, branch: e.branch,
                          state: running ? "running" : "", rollback: back ? control(s, "rollback", name) : nil,
                          rollbackLabel: back ? "Roll back to \(e.checkpoint)" : "", failed: e.result == "failed")
        }
    }

    // MARK: Confirmations

    /// A confirmation rendered from a dry run's impact: the dialog's
    /// title, its message (one sentence a line), the verb, and the fields
    /// the real request adds (a reviewed checkpoint, a confirm token).
    public struct Confirmation: Equatable, Sendable {
        public var title: String
        public var message: String
        public var ok: String
        public var danger: Bool
        /// Extra body fields: `expect`, `checkpoint`.
        public var send: [String: JSONValue]
    }

    static func affects(_ s: DeploymentsState, _ im: DeployImpact?, _ name: String, _ tail: String) -> String {
        switch im?.affects {
        case "nobody": return "Affects: nobody now."
        case "everyone": return "Affects: everyone using \(s.tile): \(tail)."
        case "deployment": return "Affects: people using \(s.tile)+\(name): \(tail)."
        default: return ""
        }
    }

    static func against(_ c: DeployImpact.Code?) -> String {
        guard let c, !c.from.isEmpty, c.from != "work-tree" else { return "" }
        return "\(files(c.files)), \(stat(c)) against \(c.from)"
    }

    static func pausesLine(_ im: DeployImpact?, _ X: String, _ more: String = "") -> String {
        im?.pausesLiveReload == true ? "Pauses: live reload — later saves won't reach \(X) until you resume.\(more)" : ""
    }

    static func reach(_ s: DeploymentsState, _ im: DeployImpact?, _ X: String) -> String {
        if im?.affects == "everyone" {
            return "Affects: everyone using \(s.tile): frames reload once\(!(s.deployment(X)?.api ?? "").isEmpty ? "; WebSocket and SSE connections drop at the 30 s drain" : "")."
        }
        if im?.affects == "deployment" { return "Affects: only people using \(s.tile)+\(X)." }
        return affects(s, im, X, "")
    }

    /// deploy-state.js `confirmation` for the app's operations: pause,
    /// resume, reloadNow, attach, rollback (undo) and branch. `deployment`
    /// names the target; `checkpoint`/`entry`: a roll back's;
    /// `branch`: the branch route's new value (nil clears).
    public static func confirmation(_ op: String, state s: DeploymentsState, impact im: DeployImpact?, deployment: String = "",
                                    checkpoint: String = "", entry: DeployEntry? = nil, branch: String? = nil, now: Date = Date()) -> Confirmation {
        let f = facts(s)
        var title = "", ok = "", lines: [String] = [], send: [String: JSONValue] = [:], danger = false
        switch op {
        case "pause":
            let X = f.attached.isEmpty ? f.primary : f.attached, c = im?.code
            let to = (c?.to).flatMap { $0.isEmpty ? nil : $0 } ?? "a checkpoint of the work tree taken when you confirm"
            let ships = (c?.files ?? 0) > 0 ? " The work tree changed since \(X)'s last build (\(files(c!.files))): pausing live reload ships those changes to \(X) once, now." : ""
            title = "Pause live reload on \(s.tile)?"; ok = "Pause live reload"
            lines = ["Code: \(X) keeps running the code it runs now, pinned to \(to).\(ships)", "Data: nothing moves.",
                     "Pauses: live reload — saves stop reaching \(X) until Reload now or Resume live reload.", affects(s, im, X, "frames reload once")]
        case "resume":
            let Y = deployment.isEmpty ? f.last : deployment, c = im?.code, last = s.deployment(Y)?.lastDeploy
            let code = (c?.files ?? 0) > 0
                ? "\(Y) switches to the work tree now: \(files(c!.files)) (\(stat(c!))) changed since \(Y)'s \(c!.from.isEmpty ? cp(s, Y) : c!.from) \(ships(c!.files)) at once, then every save reaches \(Y)."
                : "\(Y) switches to the work tree now, then every save reaches \(Y)."
            let rolled = last?.how == "rollback" ? " This includes the change rolled back \(ago(last!.at, now: now))." : ""
            title = "Resume live reload on \(Y)?"; ok = "Resume live reload"
            lines = ["Code: \(code)\(rolled)", "Data: nothing moves.", affects(s, im, Y, "frames reload")]
        case "reloadNow":
            let X = f.last, c = im?.code, d = s.deployment(X)
            var as_ = ""
            if let c, !c.to.isEmpty { as_ = ", as \(c.to)" + (c.from.isEmpty ? "" : " (\(files(c.files)), \(stat(c)) against \(c.from))") }
            title = "Reload \(X) now?"; ok = "Reload now"
            if let to = c?.to, !to.isEmpty { send["expect"] = .string(to) }
            lines = ["Code: ships the work tree to \(X) once\(as_)\(d?.status.state == "static" ? "" : ": build, health check, swap"). \(X) stays pinned; later saves wait for the next Reload now.",
                     "Data: nothing moves.",
                     affects(s, im, X, "frames reload once\(!(d?.api ?? "").isEmpty ? "; open WebSocket and SSE connections drop at the 30 s drain" : "")")]
        case "attach":
            let A = f.attached, Y = deployment, c = im?.code
            let pin = (c?.to ?? "").isEmpty ? "" : "\(A) is pinned to a fresh checkpoint of the work tree, \(c!.to), and stops following saves. "
            let moves = (c?.files ?? 0) > 0
                ? "\(Y) switches to the work tree now: \(files(c!.files)) (\(stat(c!))) against \(Y)'s \(c!.from.isEmpty ? cp(s, Y) : c!.from) \(ships(c!.files)) at once, and \(Y) follows every save."
                : "\(Y) switches to the work tree now and follows every save."
            var a = affects(s, im, Y, "frames reload")
            if !a.isEmpty, let r = im?.reloads, !r.contains(A) { a += " Nobody using \(A) sees a change." }
            title = "Attach live reload to \(Y)?"; ok = "Attach to \(Y)"
            lines = ["Code: \(pin)\(moves)", "Data: nothing moves.", a]
        case "rollback", "undo":
            let X = deployment, C = checkpoint
            var was: [String] = []
            if let e = entry {
                let when = e.finishedAt.isEmpty ? e.requestedAt : e.finishedAt
                if !when.isEmpty { was.append("deployed \(ago(when, now: now))\(e.by.isEmpty ? "" : " by \(who(e.by))")") }
            }
            let ag = against(im?.code)
            if !ag.isEmpty { was.append(ag) }
            title = "Roll back \(X) to \(C)?"; ok = "Roll back"
            send["checkpoint"] = .string(C)
            lines = ["Code: \(X) runs \(C) again\(was.isEmpty ? "" : " (\(was.joined(separator: "; ")))").",
                     "Data: stays as it is — a roll back moves code, not state; resources the newer code created are kept.",
                     pausesLine(im, X, " The work tree still holds the code you roll back from."), reach(s, im, X)]
        case "branch":
            let r = Branch.row(s, im, deployment, branch)
            title = r.title; ok = r.ok; lines = r.lines
        default:
            title = op; ok = "OK"
        }
        let bl = op == "branch" ? "" : Branch.branchLine(im?.branch, op)
        let all = ([lines.first ?? "", bl] + lines.dropFirst()).filter { !$0.isEmpty }
        _ = danger
        return Confirmation(title: title, message: all.joined(separator: "\n"), ok: ok, danger: false, send: send)
    }

    /// The dialog for a refused action: the server's error, verbatim.
    public static func refusal(_ op: String, error: String) -> (title: String, message: String) {
        let names = ["pause": "Pause live reload", "resume": "Resume live reload", "reloadNow": "Reload now", "attach": "Attach live reload",
                     "rollback": "Roll back", "undo": "Roll back", "branch": "Setting the branch"]
        return ("\(names[op] ?? "The operation") was refused", error)
    }

    /// "seq" when the record moved (refetch, run the dry run again),
    /// "expect" when the reviewed code moved, nil otherwise.
    public static func conflict(status: Int, error: String) -> String? {
        guard status == 409 else { return nil }
        if error.hasPrefix("the deployments of "), error.range(of: #"^the deployments of .+ changed \(seq \d+\)"#, options: .regularExpression) != nil {
            return "seq"
        }
        if error.hasPrefix("the code changed since you reviewed") { return "expect" }
        return nil
    }

    // MARK: Results

    /// The result line of a deploy entry: a final one or a queued one; nil while it runs.
    public static func deployText(_ e: DeployEntry?, _ s: DeploymentsState?) -> String? {
        guard let e else { return nil }
        let D = !e.deployment.isEmpty ? e.deployment : (s?.primary ?? "main")
        switch e.result {
        case "ok": return codeMoves.contains(e.how) ? "\(D) now runs \(e.checkpoint) (\(howPhrase(e)))." : nil
        case "failed":
            let keeps = !e.previous.isEmpty ? e.previous : (s.map { serving($0, D) } ?? "its current code")
            return "Deploy to \(D) failed — \(D) keeps running \(keeps).\(e.error.isEmpty ? "" : " \(e.error)")"
        case "queued": return "Waiting for the deploy in progress on \(D)…"
        case "cancelled": return "Cancelled: \(D) was removed or \(s?.tile ?? "the tile") was disabled."
        default: return nil
        }
    }

    /// The line an operation's answer (`{state, deploy?, unchanged?}`)
    /// reads as; `prev`: the state the request was made on.
    public static func result(_ op: String, state s: DeploymentsState?, deploy e: DeployEntry?, unchanged: Bool, deployment: String = "",
                              prev: DeploymentsState? = nil) -> String? {
        if op == "branch", let s { return Branch.result(s, deployment) }
        if let e, e.result != "running", e.result != "ok" { return deployText(e, s) }
        guard let s else { return nil }
        let P = s.primary.isEmpty ? "main" : s.primary
        switch op {
        case "rollback", "undo":
            return unchanged ? "\(deployment.isEmpty ? P : deployment) already runs this code." : e?.result == "ok" ? deployText(e, s) : nil
        case "pause":
            let L = s.lastLiveReload.isEmpty ? P : s.lastLiveReload
            return "Live reload paused — \(L) is pinned to \(cp(s, L))."
        case "resume":
            let A = s.record && !s.liveReload.isEmpty ? s.liveReload : P
            return "Live reload: \(A) — saves reach \(A) again."
        case "attach":
            let X = prev?.liveReload ?? ""
            return X.isEmpty ? "Live reload: \(s.liveReload)." : "Live reload: \(s.liveReload) — \(X) is pinned to \(cp(s, X))."
        case "reloadNow":
            let L = s.lastLiveReload.isEmpty ? P : s.lastLiveReload
            if unchanged { return "No changes since \(cp(s, L))." }
            return e?.result == "ok" ? deployText(e, s) : nil
        default:
            return nil
        }
    }

    // MARK: Events

    /// A `deployments` event's data: op `work-tree` moves the pending count
    /// in place (no request per save); record, deploy, data and branch
    /// refetch the state; the rest leave it as it is.
    public static func apply(event op: String, changed: Int?, to s: DeploymentsState?) -> (state: DeploymentsState?, refetch: Bool) {
        guard var s else { return (nil, false) }
        if op == "work-tree" {
            guard s.record, s.liveReload.isEmpty, s.view != "reader" else { return (s, false) }
            var w = s.workTree ?? DeploymentsState.WorkTree()
            w.changed = changed ?? 0
            s.workTree = w
            return (s, false)
        }
        return (s, ["record", "deploy", "data", "branch"].contains(op))
    }

    // MARK: Reasons

    public enum Reason {
        public static func needsWrite(_ t: String) -> String { "Needs write access to \(t)." }
    }
}
