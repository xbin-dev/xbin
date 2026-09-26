import Foundation

// AgentTranscript folds a session's event log into what the agent screen
// shows — the port of web/bx-agent.js `_blocks()` (D74/D75/D77), made
// incremental: deltas of one run concatenate, a thought ends (and gets its
// duration) when anything else arrives, tool updates land on their call
// (ids scoped per turn — an agent may reuse them), a subagent's messages,
// thoughts and calls nest under its Task call by `parent`, a plan replaces
// the turn's plan, files.changed lands on its call or before its turn's
// divider, requests settle in place.
//
// Seq discipline (docs/protocol.md §Agent session events): the log is the
// source of truth. A live event is applied only when it is the next seq —
// anything else returns `.refetch(since:)` and the caller re-reads
// `GET …/events?since=<cursor>`; pages merge by seq (duplicates ignored, an
// earlier unseen event re-folds the whole log); a truncated page or a
// follow stream's `gap` leaves a GapMarker where events were lost.

public enum SyncAction: Sendable, Hashable {
    case none
    /// Re-read `GET …/events?since=` from this cursor (a skipped seq).
    case refetch(since: UInt64)
}

public struct AgentTranscript: Sendable, Hashable {
    /// The transcript, top level (subagents hold their own items).
    public private(set) var items: [TranscriptItem] = []
    public private(set) var state = AgentSessionState()
    /// The newest seq applied — the cursor for `?since=`.
    public private(set) var lastSeq: UInt64 = 0

    // the applied log, in order (gap markers sort after their `before`)
    private var log: [AgentEvent] = []
    private var seen: Set<UInt64> = []
    private var gapsSeen: Set<UInt64> = []
    private var allowJump = false
    private var r = Reducer()

    public init() {}

    /// A transcript of these events (a replay, a past session).
    public init(events: [AgentEvent], truncated: Bool = false) {
        apply(events: events, since: 0, truncated: truncated)
    }

    /// Earlier events were dropped somewhere (show "… earlier events dropped").
    public var truncated: Bool { !gapsSeen.isEmpty }
    /// Every applied event, in seq order (gap markers included).
    public var events: [AgentEvent] { log }

    // MARK: applying

    /// A live event (a `/ws/events` `session` frame): applied when it is the
    /// next seq; an old one is ignored; a skipped seq asks for a refetch.
    @discardableResult
    public mutating func receiveLive(_ e: AgentEvent) -> SyncAction {
        if e.type == AgentEventType.gap.rawValue { return receiveStream(e) }
        if e.seq <= lastSeq { return .none }
        if e.seq == lastSeq + 1 {
            append(e)
            return .none
        }
        return .refetch(since: lastSeq)
    }

    /// A line of a `?follow=1` stream: consecutive from the cursor, except
    /// right after a `gap` line (the ring dropped what came before).
    @discardableResult
    public mutating func receiveStream(_ e: AgentEvent) -> SyncAction {
        if e.type == AgentEventType.gap.rawValue {
            let before = e.data["before"]?.uint64 ?? lastSeq
            addGap(before: before)
            allowJump = true
            return .none
        }
        if e.seq <= lastSeq { return .none }
        if e.seq == lastSeq + 1 || allowJump {
            allowJump = false
            if e.seq != lastSeq + 1 { addGap(before: lastSeq) }
            append(e)
            return .none
        }
        return .refetch(since: lastSeq)
    }

    /// A page of `GET …/events?since=<since>`.
    public mutating func apply(page: EventsPage, since: UInt64) {
        apply(events: page.events, since: since, truncated: page.truncated)
    }

    /// Merges events by seq (duplicates ignored; one older than the cursor
    /// that was never seen re-folds the log). `truncated`: the cursor
    /// predated the ring — a gap after `since`.
    public mutating func apply(events: [AgentEvent], since: UInt64 = 0, truncated: Bool = false) {
        if truncated { addGap(before: since) }
        var needRebuild = false
        for e in events.sorted(by: { $0.seq < $1.seq }) {
            if e.type == AgentEventType.gap.rawValue { addGap(before: e.data["before"]?.uint64 ?? since); continue }
            guard e.seq > 0, !seen.contains(e.seq) else { continue }
            if e.seq > lastSeq, !needRebuild {
                append(e)
            } else {
                insertSorted(e)
                needRebuild = true
            }
        }
        if needRebuild { rebuild() }
    }

    /// Starts over (a session replaced by its restart, a reconnect that lost trust).
    public mutating func reset() { self = AgentTranscript() }

    private mutating func append(_ e: AgentEvent) {
        log.append(e)
        seen.insert(e.seq)
        lastSeq = max(lastSeq, e.seq)
        r.fold(e, into: &items, state: &state)
    }

    private mutating func insertSorted(_ e: AgentEvent) {
        seen.insert(e.seq)
        lastSeq = max(lastSeq, e.seq)
        let k = orderKey(e)
        let at = log.firstIndex { orderKey($0) > k } ?? log.count
        log.insert(e, at: at)
    }

    private mutating func addGap(before: UInt64) {
        guard !gapsSeen.contains(before) else { return }
        gapsSeen.insert(before)
        let g = AgentEvent(seq: 0, type: AgentEventType.gap.rawValue, data: ["before": .number(Double(before))])
        if before >= lastSeq {
            log.append(g)
            r.fold(g, into: &items, state: &state)
        } else {
            let k = orderKey(g)
            log.insert(g, at: log.firstIndex { orderKey($0) > k } ?? log.count)
            rebuild()
        }
    }

    private mutating func rebuild() {
        items = []
        state = AgentSessionState()
        r = Reducer()
        for e in log { r.fold(e, into: &items, state: &state) }
    }

    /// Events sort by seq; a gap sits right after the seq it follows.
    private func orderKey(_ e: AgentEvent) -> (UInt64, Int) {
        e.type == AgentEventType.gap.rawValue ? (e.data["before"]?.uint64 ?? 0, 1) : (e.seq, 0)
    }

    // MARK: reading

    /// The items grouped by turn (each ends with its divider; the last group
    /// is the turn in progress, when there is one).
    public var turns: [TurnGroup] {
        var out: [TurnGroup] = []
        var cur: [TranscriptItem] = []
        for it in items {
            cur.append(it)
            if case .turnEnd(let d) = it {
                out.append(TurnGroup(id: "g" + d.id, number: d.turn, items: cur, end: d))
                cur = []
            }
        }
        if !cur.isEmpty { out.append(TurnGroup(id: "g-open-\(out.count)", number: nil, items: cur, end: nil)) }
        return out
    }

    /// Unanswered permission requests, oldest first (the inbox, the Live Activity).
    public var pendingPermissions: [PermissionCard] {
        items.compactMap { if case .permission(let p) = $0, p.isPending { return p }; return nil }
    }

    /// Unanswered questions, oldest first.
    public var pendingQuestions: [QuestionCard] {
        items.compactMap { if case .question(let q) = $0, q.isPending { return q }; return nil }
    }

    /// The line under a running turn: the in-flight tool ("Running npm test…"),
    /// else "Working…"; nil when idle or while a thought streams (it shows itself).
    public func activity(ended: Bool = false) -> String? {
        guard !ended, state.status == .running else { return nil }
        if case .thought(let t)? = items.last, !t.done { return nil }
        for it in items.reversed() {
            if case .tool(let t) = it, t.status == .inProgress { return "Running \(t.headline)…" }
        }
        return "Working…"
    }

    /// The last status reported after `seq` (e.g. a plan's "keep planning"
    /// feedback is sent once the rejected turn settles to idle).
    public func lastStatus(after seq: UInt64) -> SessionStatus? {
        var s: SessionStatus?
        for e in log where e.seq > seq && e.type == AgentEventType.status.rawValue {
            if let v = e.data["status"]?.string, !v.isEmpty { s = SessionStatus(rawValue: v) }
        }
        return s
    }

    /// Finds an item anywhere (subagents included) by id.
    public func item(id: String) -> TranscriptItem? {
        func find(_ list: [TranscriptItem]) -> TranscriptItem? {
            for it in list {
                if it.id == id { return it }
                if case .tool(let t) = it, !t.children.isEmpty, let f = find(t.children) { return f }
            }
            return nil
        }
        return find(items)
    }
}

// MARK: - the fold

/// Where things are while folding: paths of indices into the item tree
/// (top level, then a subagent's children, …).
private struct Reducer: Sendable, Hashable {
    typealias Path = [Int]
    var toolsByKey: [String: Path] = [:] // "<epoch>/<id>"
    var byId: [String: Path] = [:] // a call's latest record: files.changed may land after its turn ended
    var perms: [String: Path] = [:]
    var asks: [String: Path] = [:]
    var plan: Path?
    var cur: Path? // the open top-level message/thought
    var boxCur: [String: Path] = [:] // a subagent's open message/thought, by the subagent's path
    var epoch = 0 // turns ended so far: scopes tool ids

    static func key(_ p: Path) -> String { p.map(String.init).joined(separator: ".") }

    /// A placeholder a slot holds while its value is mutated outside it: the
    /// value's buffers (a message's text, a call's output) are then uniquely
    /// referenced and a delta appends in place instead of copying.
    static let hole = TranscriptItem.gap(GapMarker(id: "", before: 0))

    // MARK: tree access

    static func get(_ items: [TranscriptItem], _ p: Path) -> TranscriptItem? {
        guard let i = p.first, i < items.count else { return nil }
        if p.count == 1 { return items[i] }
        guard case .tool(let t) = items[i] else { return nil }
        return get(t.children, Array(p.dropFirst()))
    }

    static func update(_ items: inout [TranscriptItem], _ p: Path, _ f: (inout TranscriptItem) -> Void) {
        guard let i = p.first, i < items.count else { return }
        if p.count == 1 { f(&items[i]); return }
        guard case .tool(var t) = items[i] else { return }
        items[i] = hole // release the reference: the children mutate in place
        update(&t.children, Array(p.dropFirst()), f)
        items[i] = .tool(t)
    }

    /// Appends into a subagent's children (box) or the top level; returns the new path.
    static func append(_ items: inout [TranscriptItem], _ it: TranscriptItem, box: Path?) -> Path {
        guard let box else {
            items.append(it)
            return [items.count - 1]
        }
        var n = 0
        update(&items, box) { b in
            if case .tool(var t) = b {
                t.children.append(it)
                n = t.children.count - 1
                b = .tool(t)
            }
        }
        return box + [n]
    }

    func isThought(_ items: [TranscriptItem], _ p: Path?) -> Bool {
        guard let p, case .thought? = Self.get(items, p) else { return false }
        return true
    }

    func endThought(_ items: inout [TranscriptItem], _ p: Path, ts: Int64?) {
        Self.update(&items, p) { it in
            if case .thought(var t) = it {
                if let ts, ts != 0 { t.endedAt = ts }
                t.done = true
                it = .thought(t)
            }
        }
    }

    /// Moves a list's "open" run: the previous message stops streaming.
    mutating func setCur(_ items: inout [TranscriptItem], box: Path?, to p: Path?) {
        let old = box.map { boxCur[Self.key($0)] } ?? cur
        if let old, old != p {
            Self.update(&items, old) { it in
                if case .message(var m) = it { m.open = false; it = .message(m) }
            }
        }
        if let box { boxCur[Self.key(box)] = p } else { cur = p }
    }

    /// A top-level insertion at `at` shifted everything after it.
    mutating func shift(from at: Int) {
        func sh(_ p: Path) -> Path { p.first.map { $0 >= at ? [$0 + 1] + p.dropFirst() : p } ?? p }
        toolsByKey = toolsByKey.mapValues(sh)
        byId = byId.mapValues(sh)
        perms = perms.mapValues(sh)
        asks = asks.mapValues(sh)
        plan = plan.map(sh)
        cur = cur.map(sh)
        var bc: [String: Path] = [:]
        for (k, v) in boxCur {
            let kp = k.split(separator: ".").compactMap { Int($0) }
            bc[Self.key(sh(kp))] = sh(v)
        }
        boxCur = bc
    }

    /// The subagent call a parent id names, when it is one.
    func box(_ items: [TranscriptItem], parent: String?) -> Path? {
        guard let parent, !parent.isEmpty, let bp = byId[parent], case .tool(let t)? = Self.get(items, bp), t.isSubagent else { return nil }
        return bp
    }

    // MARK: fold

    mutating func fold(_ e: AgentEvent, into items: inout [TranscriptItem], state: inout AgentSessionState) {
        // a top-level thought ends when anything else arrives: that is its duration
        if e.type != "thought.delta", e.type != "status", e.type != "files.changed", isThought(items, cur) {
            endThought(&items, cur!, ts: e.ts)
        }
        switch e.payload {
        case .messageDelta(let d):
            if d.role == .user, d.parent.isEmpty { state.userPrompted(ts: e.ts) }
            delta(&items, e, parent: d.parent, text: d.text) { id in
                .message(Message(id: id, role: d.role, messageId: d.messageId, text: d.text, parent: d.parent, ts: e.ts, open: true))
            } merge: { it in
                if case .message(var m) = it, m.role == d.role, m.messageId == d.messageId {
                    it = Self.hole
                    m.text += d.text
                    it = .message(m)
                    return true
                }
                return false
            }
        case .thoughtDelta(let d):
            delta(&items, e, parent: d.parent, text: d.text) { id in
                .thought(Thought(id: id, text: d.text, parent: d.parent, startedAt: e.ts, endedAt: e.ts, done: false))
            } merge: { it in
                if case .thought(var t) = it {
                    it = Self.hole
                    t.text += d.text
                    if e.ts != 0 { t.endedAt = e.ts }
                    it = .thought(t)
                    return true
                }
                return false
            }
        case .toolCall(let u), .toolUpdate(let u):
            tool(&items, e, u)
        case .filesChanged(let f):
            if let tc = f.toolCallId {
                if let p = toolsByKey["\(epoch)/\(tc)"] ?? byId[tc] {
                    Self.update(&items, p) { it in
                        if case .tool(var t) = it { t.files = f; it = .tool(t) }
                    }
                }
                return
            }
            let blk = TranscriptItem.changes(TurnChanges(id: "changes\(e.seq > 0 ? e.seq : UInt64(items.count))", turn: f.turn, files: f))
            let at = items.lastIndex { if case .turnEnd(let d) = $0 { return d.turn == f.turn }; return false }
            if let at {
                items.insert(blk, at: at)
                shift(from: at)
            } else {
                items.append(blk)
            }
        case .plan(let entries):
            setCur(&items, box: nil, to: nil)
            if plan == nil {
                items.append(.plan(PlanChecklist(id: "plan\(e.seq)", entries: [])))
                plan = [items.count - 1]
            }
            Self.update(&items, plan!) { it in
                if case .plan(var p) = it { p.entries = entries; it = .plan(p) }
            }
        case .permissionRequest(let p):
            setCur(&items, box: nil, to: nil)
            items.append(.permission(PermissionCard(id: "perm:" + p.pid, request: p, resolution: nil)))
            perms[p.pid] = [items.count - 1]
        case .permissionResolved(let res):
            if let p = perms[res.pid] {
                Self.update(&items, p) { it in
                    if case .permission(var c) = it { c.resolution = res; it = .permission(c) }
                }
            }
        case .elicitationRequest(let q):
            setCur(&items, box: nil, to: nil)
            items.append(.question(QuestionCard(id: "ask:" + q.eid, request: q, fields: FormField.fields(schema: q.schema), resolution: nil)))
            asks[q.eid] = [items.count - 1]
        case .elicitationResolved(let res):
            if let p = asks[res.eid] {
                Self.update(&items, p) { it in
                    if case .question(var c) = it { c.resolution = res; it = .question(c) }
                }
            }
        case .turnEnd(let t):
            if isThought(items, cur) { endThought(&items, cur!, ts: nil) }
            setCur(&items, box: nil, to: nil)
            for (k, _) in boxCur { // a subagent's open text stops streaming with its turn
                let kp = k.split(separator: ".").compactMap { Int($0) }
                setCur(&items, box: kp, to: nil)
            }
            boxCur = [:]
            plan = nil // a new turn starts a fresh plan
            epoch += 1 // tool ids in the next turn don't collide with this one's
            items.append(.turnEnd(TurnDivider(id: "turn\(e.seq)", turn: e.data["turn"]?.int, stopReason: t.stopReason, usage: t.usage, error: t.error)))
            state.apply(t)
        case .gap(let before):
            setCur(&items, box: nil, to: nil)
            items.append(.gap(GapMarker(id: "gap\(before)", before: before)))
        case .status(let s):
            state.apply(s, ts: e.ts)
        case .unknown:
            break
        }
    }

    /// A message or thought delta: into its subagent's children when `parent`
    /// names one, merged into the open run when it continues it.
    mutating func delta(_ items: inout [TranscriptItem], _ e: AgentEvent, parent: String, text: String,
                        make: (String) -> TranscriptItem, merge: (inout TranscriptItem) -> Bool)
    {
        let bx = box(items, parent: parent)
        let c = bx.map { boxCur[Self.key($0)] } ?? cur
        if let c, e.type != "thought.delta", isThought(items, c) { endThought(&items, c, ts: e.ts) }
        var merged = false
        if let c { Self.update(&items, c) { merged = merge(&$0) } }
        if merged { return }
        let id = (e.type == "thought.delta" ? "th" : "m") + String(e.seq)
        let p = Self.append(&items, make(id), box: bx)
        setCur(&items, box: bx, to: p)
    }

    mutating func tool(_ items: inout [TranscriptItem], _ e: AgentEvent, _ u: ToolCallUpdate) {
        let tkey = "\(epoch)/\(u.id)"
        var tp = toolsByKey[tkey]
        var bx = box(items, parent: u.parent)
        if bx != nil, bx == tp { bx = nil }
        if let bx {
            let k = Self.key(bx)
            if let bc = boxCur[k], isThought(items, bc) { endThought(&items, bc, ts: nil) }
            setCur(&items, box: bx, to: nil)
        } else {
            setCur(&items, box: nil, to: nil)
        }
        if tp == nil {
            var t = ToolCall(id: "tool\(epoch):\(u.id)", toolCallId: u.id)
            t.startedAt = e.ts
            tp = Self.append(&items, .tool(t), box: bx)
            toolsByKey[tkey] = tp
        }
        Self.update(&items, tp!) { it in
            guard case .tool(var t) = it else { return }
            it = Self.hole
            t.fold(u)
            if e.ts != 0 { t.updatedAt = e.ts }
            if t.isSubagent, !t.status.isRunning {
                for i in t.children.indices { // a finished subagent thinks no more
                    if case .thought(var th) = t.children[i] { th.done = true; t.children[i] = .thought(th) }
                    if case .message(var m) = t.children[i] { m.open = false; t.children[i] = .message(m) }
                }
            }
            it = .tool(t)
        }
        byId[u.id] = tp
    }
}
