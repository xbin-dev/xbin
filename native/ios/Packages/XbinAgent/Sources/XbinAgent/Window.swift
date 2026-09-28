import Foundation

// AgentWindow is the part of a session's log the app holds (D130, the port
// of web/agent-pages.js). The server pages the log (`?limit=` the tail,
// `?before=<seq>` the page before a seq; docs/protocol.md §Agent session
// events → Pages), each page cut where no card spans the cut. The window
// holds a run of consecutive SEGMENTS — each a page (or a piece split off the
// live tail at a cut the server gave) folded on its own from its state
// header (`AgentTranscript(seed:)`) — so it can load pages above the reader,
// drop whole segments far from them in either direction, and fetch them
// back, with the same items and ids every time (ids derive from seqs).
//
// The last segment is the live tail while `detached` is false: live events
// fold into it. Once it is dropped (the reader is far up), live events only
// move the session's digest (`state`, the pending requests) and the `fresh`
// count, until the tail is read again. A late event (a seq below the newest
// applied, never seen) refolds only the segment it belongs to.
//
// Against an xbind that does not page (it ignores the parameters and answers
// the whole replay, without `hasOlder`), the window is one segment holding
// everything, and nothing unloads. Pure and a value: the feed owns one and
// publishes it; `AgentRows` turns it into observable rows on the main actor.

public struct AgentWindow: Sendable {
    /// Events per page asked for (xbind's default).
    public static let pageLimit = 200
    /// Items kept loaded beyond the visible rows in either direction (whole
    /// segments beyond it go).
    public static let keepMargin = 100

    struct Segment: Sendable {
        /// The seq it starts at.
        var first: UInt64
        var fold: AgentTranscript
        /// What its fold started from (nil: the start of the log, or an xbind that does not page).
        var state: PageState?
    }

    /// A dropped segment below the loaded ones: where it starts and what its
    /// fold started from (fetched back from there).
    struct Dropped: Sendable, Hashable {
        var first: UInt64
        var state: PageState?
    }

    private(set) var segs: [Segment] = []
    /// Every loaded item, in order.
    public private(set) var items: [TranscriptItem] = []
    /// The session as a whole — every status, prompt and turn end applied
    /// (the tail page's and every live event's), whatever is loaded.
    public private(set) var state = AgentSessionState()
    /// The newest seq applied: the follow cursor, `?since=`.
    public private(set) var lastSeq: UInt64 = 0
    /// The server holds events before the first loaded one.
    public private(set) var hasOlder = false
    /// The server pages (false: an xbind that does not page answered the
    /// whole log; nothing unloads).
    public private(set) var paged = true
    /// The live tail is not loaded (the reader is far up).
    public private(set) var detached = false
    /// Entries that arrived while the reader was away from the bottom (the
    /// "↓ N new" pill).
    public private(set) var fresh = 0
    /// The reader is at the bottom (the screen says so).
    public private(set) var following = true
    /// A tail (or the whole log) was read.
    public private(set) var isOpen = false
    /// Moves on every change (a publish is due).
    public private(set) var version = 0

    private(set) var below: [Dropped] = []
    private var statusSeq: UInt64 = 0
    private var pendingPIDs: Set<String> = []
    private var pendingEIDs: Set<String> = []
    private var allowJump = false
    private var lastLive: AgentEvent?

    public init() {}

    /// A whole log (a past session, an xbind that does not page): one segment.
    public init(events: [AgentEvent], truncated: Bool = false) {
        openWhole(events, truncated: truncated, next: 0)
    }

    // MARK: loading

    /// Starts over from the tail page — on open, on a resume's replay, on
    /// "jump to latest". An xbind that does not page answered the whole log.
    public mutating func open(tail p: EventsPage) {
        let follow = following
        self = AgentWindow()
        following = follow
        guard let more = p.hasOlder else {
            openWhole(p.events, truncated: p.truncated, next: p.next)
            return
        }
        isOpen = true
        hasOlder = more
        let st = p.state ?? PageState()
        state.seed(st)
        pendingPIDs = Set(st.permissions)
        pendingEIDs = Set(st.elicitations)
        let evs = Self.sorted(p.events)
        for e in evs { digest(e) }
        // A turn running since before the page: its start is not in it — the
        // page's first event is the earliest this client knows of.
        if state.isBusy, state.turnStartedAt == nil, let t = evs.first(where: { $0.ts != 0 })?.ts { state.turnStartedAt = t }
        let fold = AgentTranscript(seed: st, events: evs, truncated: p.truncated && !more)
        segs = [Segment(first: evs.first?.seq ?? p.next + 1, fold: fold, state: st)]
        lastSeq = max(p.next, evs.last?.seq ?? 0)
        lastLive = evs.last
        changed()
    }

    private mutating func openWhole(_ events: [AgentEvent], truncated: Bool, next: UInt64) {
        isOpen = true
        paged = false
        let evs = Self.sorted(events)
        for e in evs { digest(e) }
        segs = [Segment(first: evs.first?.seq ?? 1, fold: AgentTranscript(events: evs, truncated: truncated), state: nil)]
        lastSeq = max(next, evs.last?.seq ?? 0)
        lastLive = evs.last
        changed()
    }

    /// The seq the first loaded segment starts at: `before=` for the page above it.
    public var firstSeq: UInt64 { segs.first?.first ?? 0 }

    /// The page before `before` (asked for with the `firstSeq` it answers),
    /// prepended. False when it no longer fits (the window moved meanwhile).
    @discardableResult
    public mutating func prepend(_ p: EventsPage, before: UInt64) -> Bool {
        guard paged, hasOlder, !segs.isEmpty, segs[0].first == before else { return false }
        let evs = Self.sorted(p.events).filter { $0.seq > 0 && $0.seq < before }
        hasOlder = (p.hasOlder ?? false) && !evs.isEmpty
        if !evs.isEmpty {
            var fold = AgentTranscript(seed: p.state, events: evs, truncated: !hasOlder && p.truncated)
            fold.seal()
            segs.insert(Segment(first: evs[0].seq, fold: fold, state: p.state), at: 0)
        }
        changed()
        return true
    }

    /// Whether segments below the loaded ones were dropped (or the live
    /// tail): scrolling down fetches them back, or the pill re-reads the tail.
    public var hasNewer: Bool { detached || !below.isEmpty }

    /// How to fetch the first dropped segment below the loaded ones.
    public enum NewerRequest: Sendable, Hashable {
        /// A page ending where the next dropped segment starts.
        case page(before: UInt64, limit: Int)
        /// Everything after the old tail's start (the tail is live again).
        case since(UInt64)
    }

    public var newerRequest: NewerRequest? {
        guard let from = below.first?.first else { return nil }
        if below.count > 1 { return .page(before: below[1].first, limit: max(1, Int(below[1].first - from))) }
        return .since(from > 0 ? from - 1 : 0)
    }

    /// The answer to `newerRequest` (asked as `req`), appended. False when
    /// the window moved meanwhile.
    @discardableResult
    public mutating func appendNewer(_ events: [AgentEvent], for req: NewerRequest) -> Bool {
        guard req == newerRequest, let d = below.first, !segs.isEmpty else { return false }
        let until: UInt64? = below.count > 1 ? below[1].first : nil
        let evs = Self.sorted(events).filter { e in e.seq >= d.first && (until.map { e.seq < $0 } ?? true) }
        // the ring lost some of it meanwhile: say so where
        let lost = (evs.first?.seq ?? d.first) > d.first
        var fold = AgentTranscript(seed: d.state, events: evs, truncated: lost, since: d.first - 1)
        below.removeFirst()
        if until == nil { // the old tail: live again
            detached = false
            below = []
            for e in evs where e.seq > lastSeq { digest(e) }
            if let last = evs.last?.seq, last > lastSeq { lastSeq = last; lastLive = evs.last }
        } else {
            fold.seal()
        }
        segs.append(Segment(first: d.first, fold: fold, state: d.state))
        changed()
        return true
    }

    /// The live tail's events (split it once it grows past a few pages).
    public var tailEvents: Int { detached ? 0 : segs.last?.fold.eventCount ?? 0 }

    /// Splits the live tail where the server's tail page starts (`cut`, with
    /// that page's state header), so its older part can unload like any
    /// page. Nothing's id changes.
    @discardableResult
    public mutating func split(at cut: UInt64, state st: PageState?) -> Bool {
        guard paged, !detached, let tail = segs.last, cut > tail.first, tail.fold.lastSeq >= cut else { return false }
        let (older, newer) = tail.fold.split(at: cut, newerSeed: st)
        segs[segs.count - 1] = Segment(first: tail.first, fold: older, state: tail.state)
        segs.append(Segment(first: cut, fold: newer, state: st))
        changed()
        return true
    }

    // MARK: live events

    /// A live event (a `/ws/events` `session` frame): the next seq applies;
    /// an older one is ignored; a skipped seq asks for a refetch.
    @discardableResult
    public mutating func receiveLive(_ e: AgentEvent) -> SyncAction {
        if e.type == AgentEventType.gap.rawValue { return receiveStream(e) }
        if e.seq <= lastSeq { return .none }
        if e.seq == lastSeq + 1 {
            applyNew(e)
            changed()
            return .none
        }
        return .refetch(since: lastSeq)
    }

    /// A line of a `?follow=1` stream: consecutive from the cursor, except
    /// right after a `gap` line (the ring dropped what came before).
    @discardableResult
    public mutating func receiveStream(_ e: AgentEvent) -> SyncAction {
        if e.type == AgentEventType.gap.rawValue {
            addGap(before: e.data["before"]?.uint64 ?? lastSeq)
            allowJump = true
            changed()
            return .none
        }
        if e.seq <= lastSeq { return .none }
        if e.seq == lastSeq + 1 || allowJump {
            allowJump = false
            if e.seq != lastSeq + 1 { addGap(before: lastSeq) }
            applyNew(e)
            changed()
            return .none
        }
        return .refetch(since: lastSeq)
    }

    /// A page of `GET …/events?since=<since>`.
    public mutating func apply(page: EventsPage, since: UInt64) {
        apply(events: page.events, since: since, truncated: page.truncated)
    }

    /// New events fold into the tail (detached: they only move the digest
    /// and the count); an unseen older one in a loaded segment refolds that
    /// segment; one in no loaded segment waits for its page. `truncated`:
    /// the cursor predated the ring — a gap after `since`.
    public mutating func apply(events: [AgentEvent], since: UInt64 = 0, truncated: Bool = false) {
        var moved = false
        if truncated, !events.isEmpty {
            addGap(before: since)
            moved = true
        }
        var late: [Int: [AgentEvent]] = [:]
        for e in Self.sorted(events) {
            if e.type == AgentEventType.gap.rawValue {
                addGap(before: e.data["before"]?.uint64 ?? since)
                moved = true
                continue
            }
            guard e.seq > 0 else { continue }
            if e.seq > lastSeq {
                applyNew(e)
                moved = true
            } else if let i = segment(of: e.seq), !segs[i].fold.has(e.seq) {
                late[i, default: []].append(e)
            }
        }
        for (i, evs) in late {
            segs[i].fold.apply(events: evs)
            moved = true
        }
        if moved { changed() }
    }

    private mutating func applyNew(_ e: AgentEvent) {
        lastSeq = e.seq
        digest(e)
        if detached || segs.isEmpty {
            if Self.opens(e, after: lastLive) { fresh += 1 }
        } else {
            let k = segs.count - 1
            let n = segs[k].fold.items.count
            segs[k].fold.apply(events: [e])
            if !following { fresh += max(0, segs[k].fold.items.count - n) }
        }
        lastLive = e
    }

    private mutating func addGap(before: UInt64) {
        guard !detached, !segs.isEmpty else { return }
        let g = AgentEvent(seq: 0, type: AgentEventType.gap.rawValue, data: ["before": .number(Double(before))])
        segs[segs.count - 1].fold.apply(events: [g])
    }

    // MARK: the reader

    /// The reader reached the bottom (nothing is new any more), or left it.
    public mutating func setFollowing(_ on: Bool) {
        guard on != following || (on && fresh > 0) else { return }
        following = on
        if on { fresh = 0 }
        changed()
    }

    /// Drops the segments whose items all lie outside `[lo, hi)` (indices
    /// into `items`); the live tail only when `canDetach` (the reader is
    /// not at the bottom). True when anything went.
    @discardableResult
    public mutating func keep(_ lo: Int, _ hi: Int, canDetach: Bool) -> Bool {
        guard paged, segs.count > 1 else { return false }
        var ends: [Int] = []
        var n = 0
        for s in segs {
            n += s.fold.items.count
            ends.append(n)
        }
        var dropTop = 0, dropBottom = segs.count
        while dropTop < segs.count - 1, ends[dropTop] <= lo { dropTop += 1 }
        while dropBottom - 1 > dropTop, ends[dropBottom - 2] >= hi { dropBottom -= 1 }
        if dropBottom < segs.count, !canDetach, !detached { dropBottom = segs.count }
        if dropTop == 0, dropBottom == segs.count { return false }
        if dropBottom < segs.count {
            below = segs[dropBottom...].map { Dropped(first: $0.first, state: $0.state) } + below
            detached = true
        }
        if dropTop > 0 { hasOlder = true }
        segs = Array(segs[dropTop..<dropBottom])
        changed()
        return true
    }

    /// `keep` around the rows the reader sees (by id): `margin` items beyond
    /// them either way stay loaded.
    @discardableResult
    public mutating func keep(visible first: String, _ last: String, margin: Int = AgentWindow.keepMargin, canDetach: Bool) -> Bool {
        guard paged, segs.count > 1, let a = index(of: first), let b = index(of: last) else { return false }
        return keep(min(a, b) - margin, max(a, b) + 1 + margin, canDetach: canDetach)
    }

    /// Where an item is in `items`.
    public func index(of id: String) -> Int? { items.firstIndex { $0.id == id } }

    // MARK: reading

    /// Every loaded event, in order (gap markers included).
    public var events: [AgentEvent] { segs.flatMap(\.fold.events) }
    /// Events were dropped somewhere in what is loaded.
    public var truncated: Bool { segs.contains { $0.fold.truncated } }
    /// Loaded segments (tests, diagnostics).
    public var segmentCount: Int { segs.count }

    /// Unanswered permission requests among the loaded items, oldest first.
    public var pendingPermissions: [PermissionCard] {
        items.compactMap { if case .permission(let p) = $0, p.isPending { return p }; return nil }
    }

    /// Unanswered questions among the loaded items, oldest first.
    public var pendingQuestions: [QuestionCard] {
        items.compactMap { if case .question(let q) = $0, q.isPending { return q }; return nil }
    }

    /// Requests waiting for an answer, loaded or not.
    public var pendingCount: Int { pendingPIDs.count + pendingEIDs.count }

    /// The line under a running turn (the tail's in-flight tool, else
    /// "Working…"); nil when idle or while a thought streams.
    public func activity(ended: Bool = false) -> String? {
        guard !ended, state.status == .running else { return nil }
        guard !detached, let tail = segs.last else { return "Working…" }
        return tail.fold.activity(ended: ended, status: state.status)
    }

    /// The last status reported after `seq`.
    public func lastStatus(after seq: UInt64) -> SessionStatus? { statusSeq > seq ? state.status : nil }

    /// Finds a loaded item anywhere (subagents included) by id.
    public func item(id: String) -> TranscriptItem? {
        for s in segs { if let it = s.fold.item(id: id) { return it } }
        return nil
    }

    // MARK: inside

    /// The loaded segment a seq belongs to (nil: before them, or in a
    /// dropped one below).
    private func segment(of seq: UInt64) -> Int? {
        for i in segs.indices.reversed() where seq >= segs[i].first {
            let end = i < segs.count - 1 ? segs[i + 1].first : (below.first?.first ?? .max)
            return seq < end ? i : nil
        }
        return nil
    }

    /// What an event says about the session as a whole.
    private mutating func digest(_ e: AgentEvent) {
        state.absorb(e)
        switch e.type {
        case AgentEventType.status.rawValue:
            if let s = e.data["status"]?.string, !s.isEmpty, e.seq > 0 { statusSeq = e.seq }
        case AgentEventType.permissionRequest.rawValue:
            if let p = e.data["pid"]?.string { pendingPIDs.insert(p) }
        case AgentEventType.permissionResolved.rawValue:
            if let p = e.data["pid"]?.string { pendingPIDs.remove(p) }
        case AgentEventType.elicitationRequest.rawValue:
            if let q = e.data["eid"]?.string { pendingEIDs.insert(q) }
        case AgentEventType.elicitationResolved.rawValue:
            if let q = e.data["eid"]?.string { pendingEIDs.remove(q) }
        default:
            break
        }
    }

    private mutating func changed() {
        items = segs.count == 1 ? segs[0].fold.items : segs.flatMap(\.fold.items)
        version &+= 1
    }

    private static func sorted(_ evs: [AgentEvent]) -> [AgentEvent] {
        var sorted = true
        for i in evs.indices.dropFirst() where evs[i].seq < evs[i - 1].seq {
            sorted = false
            break
        }
        return sorted ? evs : evs.sorted { $0.seq < $1.seq }
    }

    /// Whether an event opens a top-level entry (the "N new" count while
    /// the tail is not loaded; a message run counts once).
    static func opens(_ e: AgentEvent, after prev: AgentEvent?) -> Bool {
        let parent = e.data["parent"]?.string ?? ""
        switch e.type {
        case AgentEventType.toolCall.rawValue, AgentEventType.permissionRequest.rawValue,
             AgentEventType.elicitationRequest.rawValue, AgentEventType.turnEnd.rawValue:
            return parent.isEmpty
        case AgentEventType.messageDelta.rawValue, AgentEventType.thoughtDelta.rawValue:
            guard parent.isEmpty else { return false }
            guard let prev else { return true }
            return prev.type != e.type || (prev.data["role"]?.string ?? "agent") != (e.data["role"]?.string ?? "agent")
                || (prev.data["messageId"]?.string ?? "") != (e.data["messageId"]?.string ?? "")
        default:
            return false
        }
    }
}

extension AgentWindow {
    /// What a Live Activity shows now (see `AgentTranscript.activityState`):
    /// the waiting requests counted whether their cards are loaded or not.
    public var activityState: AgentActivityState? {
        guard state.isBusy, let started = state.turnStartedAt else { return nil }
        let n = min(pendingCount, 99)
        let phase: AgentActivityPhase = (state.status == .waitingPermission || n > 0) ? .waiting : .running
        return AgentActivityState(phase: phase, since: started / 1000, pending: n)
    }
}
