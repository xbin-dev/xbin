import Foundation

/// A failure of the way to a workspace as everything above the transport
/// sees it (D213): a URL error, what URLSession gives when the network is
/// down — whatever carries the requests underneath. The app tunnel (D204:
/// iroh, its relay, the Noise layer) is a transport like a direct
/// connection: when it can't come up, the callers get one of these, with
/// the tunnel's own reason under `NSUnderlyingErrorKey` (and in its
/// diagnostics, TunnelLog), so they show, retry and recover exactly as for
/// a dropped network.
///
/// Foundation only — an `NSError` in `NSURLErrorDomain` (a literal, as in
/// ``ConnectProblem``), which bridges to `URLError` on Apple's platforms —
/// so this is tested on Linux.
public enum NetworkFailure {
    /// The URL error codes a transport's failure becomes.
    public enum Code: Int, Sendable, CaseIterable {
        case timedOut = -1001
        case cannotConnectToHost = -1004
        case networkConnectionLost = -1005
        case notConnectedToInternet = -1009

        /// URLSession's own words for it, so the text reads as a direct
        /// connection's failure does (an `NSError` made here gets none from
        /// the system).
        public var text: String {
            switch self {
            case .timedOut: return "The request timed out."
            case .cannotConnectToHost: return "Could not connect to the server."
            case .networkConnectionLost: return "The network connection was lost."
            case .notConnectedToInternet: return "The Internet connection appears to be offline."
            }
        }
    }

    /// A URL error `code`, with `underlying` (the transport's reason) under it.
    public static func error(_ code: Code, underlying: (any Error)? = nil) -> NSError {
        var info: [String: Any] = [NSLocalizedDescriptionKey: code.text]
        if let underlying {
            info[NSUnderlyingErrorKey] = underlying
            // its words, kept apart: a Swift error bridged into the
            // dictionary may come back as a box that has lost them
            info[NSLocalizedFailureReasonErrorKey] = ConnectProblem.text(underlying)
        }
        return NSError(domain: ConnectProblem.urlErrorDomain, code: code.rawValue, userInfo: info)
    }

    /// The transport's own words for a failure ``wrap(_:as:)`` made
    /// (diagnostics, pairing); nil for any other error.
    public static func reason(_ error: any Error) -> String? {
        let ns = error as NSError
        guard ns.domain == ConnectProblem.urlErrorDomain else { return nil }
        return ns.userInfo[NSLocalizedFailureReasonErrorKey] as? String
    }

    /// Already what a network failure is: a URL error (cancellation
    /// included), a socket's POSIX error, or a task's cancellation.
    public static func isNetwork(_ error: any Error) -> Bool {
        if error is CancellationError { return true }
        let ns = error as NSError
        return ns.domain == ConnectProblem.urlErrorDomain || ns.domain == NSPOSIXErrorDomain
    }

    /// `error` as the layers above see it: a network failure as it is,
    /// anything else (a transport's own reason) as `code` with it under.
    public static func wrap(_ error: any Error, as code: Code) -> any Error {
        isNetwork(error) ? error : self.error(code, underlying: error)
    }

    /// The transport's own reason under a failure ``wrap(_:as:)`` made
    /// (diagnostics); nil for any other error.
    public static func underlying(_ error: any Error) -> (any Error)? {
        (error as NSError).userInfo[NSUnderlyingErrorKey] as? any Error
    }
}

/// When "the workspace is reachable again" goes out to its open pages
/// (D213; a page that failed reloads, the others hear `online`): at once,
/// then at most once per ``quiet`` seconds — signals in between become one
/// at its end. A tunnel coming up and its events socket reconnecting right
/// after are one recovery; a flapping socket doesn't reload a page on every
/// open.
public struct ReachableGate: Sendable, Equatable {
    public static let quiet = 2.0

    public enum Action: Sendable, Equatable {
        /// Signal now.
        case now
        /// Signal after this many seconds (then call ``fired(at:)``).
        case after(Double)
        /// One is already due: nothing more.
        case none
    }

    private var last: Date?
    private var due = false

    public init() {}

    public mutating func signal(at now: Date) -> Action {
        if let l = last, now.timeIntervalSince(l) < Self.quiet {
            if due { return .none }
            due = true
            return .after(Self.quiet - now.timeIntervalSince(l))
        }
        last = now
        return .now
    }

    /// The one that was due went out.
    public mutating func fired(at now: Date) {
        due = false
        last = now
    }
}
