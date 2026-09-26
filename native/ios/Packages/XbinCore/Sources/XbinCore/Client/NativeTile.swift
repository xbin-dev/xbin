import Foundation

// A native tile's life in the app (plans/native.md §7.2, §7.6; native/spec/
// tree.md): the hidden runtime document loads, a tree must mount within 5 s,
// and anything fatal falls back to the web tile with a quiet banner. The
// WebKit side is app code; the decisions are here.

/// Loading → live, or → the web tile.
public struct NativeTileLifecycle: Sendable, Equatable {
    /// No tree within this long after the runtime document starts loading.
    public static let mountTimeout: TimeInterval = 5

    public enum Fallback: Sendable, Equatable {
        /// No `mount` in time.
        case timeout
        /// The runtime document didn't load (network, 404, a refused frame token).
        case loadFailed(String)
        /// The WebKit process for the runtime died.
        case crashed
        /// The store failed (a bad patch, a fatal runtime error, an unsupported primitive).
        case store(TreeStoreFailure)
        /// The remote kill switch, or an xbind without runtime documents.
        case disabled

        /// The banner over the web tile.
        public var banner: String {
            switch self {
            case .timeout: return "The native view didn't start in time — showing the web page."
            case .loadFailed(let m): return "The native view couldn't load (\(m)) — showing the web page."
            case .crashed: return "The native view stopped — showing the web page."
            case .store(let f):
                if case .runtime(let e) = f, e.kind == "unsupported" {
                    return "Update the app for this tile's native view — showing the web page."
                }
                if case .unsupportedVersion = f { return "Update the app for this tile's native view — showing the web page." }
                return "The native view hit an error — showing the web page."
            case .disabled: return "Native views are off — showing the web page."
            }
        }
    }

    public enum Phase: Sendable, Equatable {
        case idle
        case loading(since: Date)
        case live
        case fallback(Fallback)
    }

    public private(set) var phase: Phase = .idle

    public init() {}

    public mutating func start(at now: Date) {
        if case .fallback = phase { return }
        phase = .loading(since: now)
    }

    /// When the timeout fires, if the tile is still loading.
    public var deadline: Date? {
        if case .loading(let since) = phase { return since.addingTimeInterval(Self.mountTimeout) }
        return nil
    }

    /// The store mounted a tree.
    public mutating func mounted() {
        switch phase {
        case .loading, .idle: phase = .live
        default: break
        }
    }

    /// The timer fired: true when this was the fallback (still no tree).
    @discardableResult
    public mutating func check(at now: Date) -> Bool {
        guard let d = deadline, now >= d else { return false }
        phase = .fallback(.timeout)
        return true
    }

    public mutating func fail(_ why: Fallback) {
        if case .fallback = phase { return }
        phase = .fallback(why)
    }

    public var isFallback: Bool { if case .fallback = phase { return true } else { return false } }
    public var fallback: Fallback? { if case .fallback(let f) = phase { return f } else { return nil } }
}

/// What the app does with a runtime's `{op:"call"}` (tree.md §5 `call`).
/// None of these is a device API: each acts on data the tile hands over.
public enum NativeCallAction: Sendable, Equatable {
    /// Put text on the clipboard; resolve `true`.
    case copy(String)
    /// The share sheet; resolve `true` shared / `false` dismissed. `file` is
    /// a tile-relative path the app downloads with the frame token.
    case share(text: String?, url: URL?, file: String?)
    /// Open an https URL in Safari; resolve `true`.
    case open(URL)
    /// Reject with this message.
    case refuse(String)

    public static func decide(_ call: BridgeCall, canOpenLinks: Bool) -> NativeCallAction {
        switch call.what {
        case "copy":
            guard let t = call.stringArgument("text") else { return .refuse("copy: no text") }
            return .copy(String(t.prefix(1 << 20)))
        case "share":
            let text = call.argument("text")?.stringValue
            let url = call.argument("url")?.stringValue.flatMap(httpsURL)
            let file = call.argument("file")?.stringValue.flatMap(tileRelativeFile)
            if text == nil, url == nil, file == nil { return .refuse("share: nothing to share") }
            return .share(text: text, url: url, file: file)
        case "open":
            guard canOpenLinks else { return .refuse("open: this tile may not open links (cap:open-links)") }
            guard let s = call.stringArgument("url"), let u = httpsURL(s) else { return .refuse("open: https URLs only") }
            return .open(u)
        default:
            return .refuse("\(call.what): not supported by this app")
        }
    }

    static func httpsURL(_ s: String) -> URL? {
        guard let u = URL(string: s), u.scheme?.lowercased() == "https", u.host != nil else { return nil }
        return u
    }

    /// A tile-relative file path: no scheme, no `..`, not absolute.
    static func tileRelativeFile(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespaces)
        guard !t.isEmpty, !t.hasPrefix("/"), !t.contains("://"), !t.hasPrefix("data:") else { return nil }
        let parts = t.split(separator: "/")
        guard !parts.contains("..") else { return nil }
        return t
    }
}

/// Saved runtime state (`{op:"state"}`), ≤ 64 KiB per tile per workspace.
public enum NativeStateBlob {
    public static let maxBytes = 64 * 1024

    /// The blob to keep, or nil when it is too big (the old one stays).
    public static func accept(_ v: JSONValue) -> JSONValue? {
        v.jsonData.count <= maxBytes ? v : nil
    }
}
