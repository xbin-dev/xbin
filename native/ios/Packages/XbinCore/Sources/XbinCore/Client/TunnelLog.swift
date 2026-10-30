import Foundation

// The app tunnels' diagnostics (D211): a bounded ring of what the tunnels
// did — the app's lifecycle, bring-ups, connects and their failures, the
// probes, retries, the forwarder's port and its 502s, streams and
// handshakes that failed — kept in memory in every build (release builds
// included) so Settings → Connection can show it and "Copy diagnostics" can
// hand it over. Nothing secret goes in: entries are scrubbed of anything
// that looks like a key, a ticket or a token (``redact(_:)``), and ids are
// cut short. Foundation only: XbinCore's tests hold it.

public final class TunnelLog: @unchecked Sendable {
    public struct Entry: Sendable, Equatable {
        public var at: Date
        /// The workspace the line is about (its id, cut short); nil: the app.
        public var workspace: String?
        public var message: String

        public init(at: Date, workspace: String?, message: String) {
            self.at = at
            self.workspace = workspace
            self.message = message
        }
    }

    /// The app's one log.
    public static let shared = TunnelLog()

    public let capacity: Int
    private let lock = NSLock()
    private var ring: [Entry] = []
    /// Where the next entry goes once the ring is full.
    private var next = 0
    private var dropped = 0

    public init(capacity: Int = 500) {
        self.capacity = max(1, capacity)
    }

    /// Records `message` (scrubbed: ``redact(_:)``) about `workspace`.
    public func add(_ message: String, workspace: String? = nil, at: Date = Date()) {
        let e = Entry(at: at, workspace: workspace.map(Self.shortWorkspace), message: Self.redact(message))
        lock.lock()
        if ring.count < capacity {
            ring.append(e)
        } else {
            ring[next] = e
            next = (next + 1) % capacity
            dropped += 1
        }
        lock.unlock()
    }

    /// Oldest first.
    public var entries: [Entry] {
        lock.lock(); defer { lock.unlock() }
        return Array(ring[next...] + ring[..<next])
    }

    /// How many entries the ring let go of (it holds the newest).
    public var droppedCount: Int { lock.lock(); defer { lock.unlock() }; return dropped }

    public func clear() {
        lock.lock()
        ring = []
        next = 0
        dropped = 0
        lock.unlock()
    }

    /// One line per entry, oldest first: `21:04:05.123 [1a2b3c4d] message`.
    public func lines(workspace: String? = nil) -> [String] {
        let only = workspace.map(Self.shortWorkspace)
        return entries.filter { only == nil || $0.workspace == nil || $0.workspace == only }.map(Self.line)
    }

    /// The text "Copy diagnostics" hands over: `header` lines first, then
    /// every entry (those about other workspaces left out when `workspace`
    /// is given).
    public func report(header: [String], workspace: String? = nil) -> String {
        var out = header.map(Self.redact)
        let n = droppedCount
        if n > 0 { out.append("(\(n) older entries dropped)") }
        out.append("")
        out += lines(workspace: workspace)
        return out.joined(separator: "\n") + "\n"
    }

    // MARK: Formatting and scrubbing

    public static func line(_ e: Entry) -> String {
        let ws = e.workspace.map { "[\($0)] " } ?? ""
        return "\(timestamp(e.at)) \(ws)\(e.message)"
    }

    /// `2026-10-30 21:04:05.123Z` (UTC).
    public static func timestamp(_ d: Date) -> String {
        let ms = Int((d.timeIntervalSince1970 * 1000).rounded(.down))
        let secs = ms / 1000
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        let p = c.dateComponents([.year, .month, .day, .hour, .minute, .second], from: Date(timeIntervalSince1970: Double(secs)))
        func two(_ n: Int?) -> String { let v = n ?? 0; return v < 10 ? "0\(v)" : "\(v)" }
        let frac = ms % 1000
        let f = frac < 10 ? "00\(frac)" : frac < 100 ? "0\(frac)" : "\(frac)"
        return "\(p.year ?? 0)-\(two(p.month))-\(two(p.day)) \(two(p.hour)):\(two(p.minute)):\(two(p.second)).\(f)Z"
    }

    /// A workspace id (a UUID) cut to its first 8 characters.
    public static func shortWorkspace(_ id: String) -> String { String(id.prefix(8)) }

    /// An EndpointId or key, enough to compare by eye: `1a2b3c4d…`.
    public static func shortID(_ hex: String) -> String { hex.count > 8 ? "\(hex.prefix(8))…" : hex }

    /// The longest run of key-like characters kept whole.
    static let longestKept = 20

    /// Cuts every long run of key-like characters — base64url, base32 or
    /// hex: what keys, tickets, psks, tokens and EndpointIds are made of —
    /// to its first 6 characters and "…". Words, numbers,
    /// addresses and error texts stay readable; nothing that long survives.
    public static func redact(_ s: String) -> String {
        var out = ""
        var run = ""
        func flush() {
            if run.count > longestKept { out += run.prefix(6) + "…" } else { out += run }
            run = ""
        }
        for ch in s {
            if isKeyChar(ch) { run.append(ch) } else { flush(); out.append(ch) }
        }
        flush()
        return out
    }

    private static func isKeyChar(_ c: Character) -> Bool {
        guard c.isASCII, let a = c.asciiValue else { return false }
        switch a {
        case UInt8(ascii: "a")...UInt8(ascii: "z"), UInt8(ascii: "A")...UInt8(ascii: "Z"), UInt8(ascii: "0")...UInt8(ascii: "9"):
            return true
        // Not "/": paths stay readable (the tunnel's secrets are base64url,
        // base32, hex or plain tokens).
        case UInt8(ascii: "+"), UInt8(ascii: "-"), UInt8(ascii: "_"), UInt8(ascii: "="):
            return true
        default:
            return false
        }
    }
}
