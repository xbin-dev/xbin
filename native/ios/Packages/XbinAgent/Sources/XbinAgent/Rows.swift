import Foundation
import Observation

// The transcript as a screen observes it (D130, E3): one observable object
// per row, so an event re-renders the row it touched and nothing else. The
// feed publishes a value (AgentWindow) on every change; `AgentRows.apply`
// diffs it into the rows it holds — by id (ids derive from seqs, so a row
// keeps its object, and a card its open state, through an unload and a
// reload) and by value (a row whose item didn't change isn't touched). The
// session-wide facts a few views read (status, the pill's count, the
// activity line, the whole state for the composer and toolbar) are their own
// properties, each set only when it changed, so a row never depends on them
// unless it reads them (a streaming message reads `status`).

/// One transcript row.
@MainActor
@Observable
public final class AgentRow: Identifiable {
    public nonisolated let id: String
    public fileprivate(set) var item: TranscriptItem
    /// A tool card or thought the reader opened (kept by id across an unload).
    public fileprivate(set) var isOpen: Bool

    init(item: TranscriptItem, isOpen: Bool) {
        id = item.id
        self.item = item
        self.isOpen = isOpen
    }
}

/// The rows of one session's window, on the main actor.
@MainActor
@Observable
public final class AgentRows {
    /// The loaded items, in order (the list's ForEach reads only this).
    public private(set) var rows: [AgentRow] = []
    public private(set) var status: SessionStatus = .starting
    /// The session is over (its final status, or the feed says so).
    public private(set) var ended = false
    /// The agent's name as it reports it (a message's sender).
    public private(set) var agentName: String?
    /// Older pages can be loaded above the rows.
    public private(set) var hasOlder = false
    /// Rows below were unloaded (or the live tail): scrolling down or the
    /// pill brings them back.
    public private(set) var hasNewer = false
    /// Entries that arrived while the reader was away from the bottom.
    public private(set) var fresh = 0
    /// The line under a running turn.
    public private(set) var activity: String?
    /// Everything the status events say (the composer, the toolbar, the header).
    public private(set) var state = AgentSessionState()
    /// A window was applied (the first read came back).
    public private(set) var loaded = false

    @ObservationIgnored private var byID: [String: AgentRow] = [:]
    @ObservationIgnored private var indexByID: [String: Int] = [:]
    @ObservationIgnored private var opened: Set<String> = []
    /// The window last applied.
    @ObservationIgnored public private(set) var window = AgentWindow()

    public init() {}

    /// Takes the feed's window: rows by id, each touched only when its item
    /// changed; the session's facts only when they changed.
    public func apply(_ w: AgentWindow, ended feedEnded: Bool = false) {
        window = w
        let items = w.items
        var same = items.count == rows.count
        if same {
            for i in items.indices where rows[i].id != items[i].id {
                same = false
                break
            }
        }
        if same {
            for i in items.indices where rows[i].item != items[i] { rows[i].item = items[i] }
        } else {
            var next: [AgentRow] = []
            next.reserveCapacity(items.count)
            var keep: [String: AgentRow] = [:]
            var index: [String: Int] = [:]
            for (i, it) in items.enumerated() {
                let r: AgentRow
                if let old = byID[it.id] {
                    if old.item != it { old.item = it }
                    r = old
                } else {
                    r = AgentRow(item: it, isOpen: opened.contains(it.id))
                }
                keep[it.id] = r
                index[it.id] = i
                next.append(r)
            }
            byID = keep
            indexByID = index
            rows = next
        }
        let end = feedEnded || w.state.isEnded
        if status != w.state.status { status = w.state.status }
        if ended != end { ended = end }
        let name = w.state.agent.flatMap { $0.name.isEmpty ? nil : $0.name }
        if agentName != name { agentName = name }
        if hasOlder != w.hasOlder { hasOlder = w.hasOlder }
        if hasNewer != w.hasNewer { hasNewer = w.hasNewer }
        if fresh != w.fresh { fresh = w.fresh }
        let a = w.activity(ended: end)
        if activity != a { activity = a }
        if state != w.state { state = w.state }
        if !loaded, w.isOpen { loaded = true }
    }

    /// Opens or folds a row's card (remembered by id: an unloaded row comes
    /// back as the reader left it).
    public func setOpen(_ row: AgentRow, _ open: Bool) {
        guard row.isOpen != open else { return }
        row.isOpen = open
        if open { opened.insert(row.id) } else { opened.remove(row.id) }
    }

    /// A row's place (nil: not loaded).
    public func index(of id: String) -> Int? { indexByID[id] ?? rows.firstIndex { $0.id == id } }

    /// The first and last of these rows in order (the rows the reader sees).
    public func span(_ ids: some Sequence<String>) -> (first: String, last: String)? {
        var lo: (Int, String)?, hi: (Int, String)?
        for id in ids {
            guard let i = index(of: id) else { continue }
            if lo == nil || i < lo!.0 { lo = (i, id) }
            if hi == nil || i > hi!.0 { hi = (i, id) }
        }
        guard let lo, let hi else { return nil }
        return (lo.1, hi.1)
    }
}
