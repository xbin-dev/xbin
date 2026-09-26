// TermProtocol.swift — the /ws/term wire format (docs/protocol.md §/ws/term;
// the server half is internal/term/attach.go).
//
// Binary frames, both directions: raw PTY bytes. Text frames: JSON control.
//   server → client  {"op":"session",…}  first message: the session's id and scope
//                    {"op":"ack","n":N}  the client's Nth binary frame reached the PTY
//                                        ≥ 50 ms ago (covers every earlier frame, D70)
//                    {"op":"pong","t":…} answers a ping, t echoed verbatim
//                    {"op":"exit"}       the shell ended
//   client → server  {"op":"resize","cols":C,"rows":R}
//                    {"op":"ping","t":<any JSON>}
// Unknown ops, unknown fields and malformed frames are ignored, as on the
// server: decoding never throws, a frame it can't use is `.ignored`.

import Foundation

/// One WebSocket message, as a transport carries it.
public enum TermWireMessage: Equatable, Sendable {
    case text(String)
    case binary([UInt8])
}

/// A network scope the caller may pick on this tile (D54/D65).
public struct TermScope: Hashable, Sendable, Codable {
    public var id: String
    public var label: String
    public var desc: String
    public init(id: String, label: String, desc: String = "") { self.id = id; self.label = label; self.desc = desc }

    enum K: String, CodingKey { case id, label, desc }
    /// `id` is required; a missing label reads as the id, a missing desc as "".
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: K.self)
        id = try c.decode(String.self, forKey: .id)
        label = (try? c.decodeIfPresent(String.self, forKey: .label)) ?? id
        desc = (try? c.decodeIfPresent(String.self, forKey: .desc)) ?? ""
    }
}

/// The `session` frame: the first message on every socket.
public struct TermSessionInfo: Equatable, Sendable {
    /// The session id (reattach with `?session=<id>`).
    public var id: String
    /// The effective network scope ("org", "set:<name>", "internet", "host", "none").
    public var net: String
    /// A human name for the effective scope.
    public var label: String
    /// What this caller may pick on this tile.
    public var scopes: [TermScope]
    /// Why the requested scope was clamped ("" = as asked).
    public var netNote: String
    /// This terminal's persistent layer was built on an older base image
    /// (offer a reset via `/ws/term/env`).
    public var baseOutdated: Bool
    /// A VM sandbox (D89/D90).
    public var vm: Bool
    /// This xbind sends `ack` and answers `ping` — predictive echo works (D70).
    public var echoAck: Bool

    public init(id: String, net: String = "", label: String = "", scopes: [TermScope] = [], netNote: String = "",
                baseOutdated: Bool = false, vm: Bool = false, echoAck: Bool = false) {
        self.id = id; self.net = net; self.label = label; self.scopes = scopes; self.netNote = netNote
        self.baseOutdated = baseOutdated; self.vm = vm; self.echoAck = echoAck
    }
}

/// A decoded server → client message.
public enum TermServerFrame: Equatable, Sendable {
    case session(TermSessionInfo)
    /// Echo ack: every binary input frame numbered ≤ n reached the PTY at least 50 ms ago.
    case ack(UInt64)
    /// A ping's answer; `t` is the ping's value when it was a number (else nil).
    case pong(t: Double?)
    /// The shell ended; the server closes the socket next.
    case exit
    /// PTY output.
    case output([UInt8])
    /// Anything else: an unknown op, a malformed frame, a known op missing what
    /// makes it usable (an `ack` without a numeric `n`). `op` when there was one.
    case ignored(op: String?)
}

/// A client → server message.
public enum TermClientFrame: Equatable, Sendable {
    /// Typed input (keys, paste, the terminal's own replies): one binary frame,
    /// counted for echo acks.
    case input([UInt8])
    case resize(cols: Int, rows: Int)
    /// `t` is echoed back in the pong; the session sends monotonic milliseconds.
    case ping(t: Double)
}

public enum TermCodec {
    /// Decodes one message from the server. Never throws.
    public static func decode(_ msg: TermWireMessage) -> TermServerFrame {
        switch msg {
        case .binary(let b): return .output(b)
        case .text(let s): return decodeControl(s)
        }
    }

    /// Decodes one JSON control frame.
    public static func decodeControl(_ text: String) -> TermServerFrame {
        guard let c = try? JSONDecoder().decode(Control.self, from: Data(text.utf8)) else { return .ignored(op: nil) }
        switch c.op {
        case "session":
            return .session(TermSessionInfo(
                id: c.id ?? "", net: c.net ?? "", label: c.label ?? "", scopes: c.scopes ?? [],
                netNote: c.netNote ?? "", baseOutdated: c.baseOutdated ?? false, vm: c.vm ?? false,
                echoAck: c.echoAck ?? false))
        case "ack":
            guard let n = c.n else { return .ignored(op: "ack") }
            return .ack(n)
        case "pong": return .pong(t: c.t)
        case "exit": return .exit
        default: return .ignored(op: c.op)
        }
    }

    /// Encodes one client message.
    public static func encode(_ frame: TermClientFrame) -> TermWireMessage {
        switch frame {
        case .input(let b): return .binary(b)
        case .resize(let cols, let rows): return .text(#"{"op":"resize","cols":\#(cols),"rows":\#(rows)}"#)
        case .ping(let t): return .text(#"{"op":"ping","t":\#(jsonNumber(t))}"#)
        }
    }

    /// A finite double as a JSON number (non-finite values become 0).
    static func jsonNumber(_ d: Double) -> String {
        guard d.isFinite else { return "0" }
        if d == d.rounded(), abs(d) < 1e15 { return String(Int64(d)) }
        return "\(d)"
    }

    /// A control frame, every field optional and leniently typed: a field of
    /// the wrong type reads as absent rather than failing the frame.
    private struct Control: Decodable {
        var op: String?
        var id, net, label, netNote: String?
        var scopes: [TermScope]?
        var baseOutdated, vm, echoAck: Bool?
        var n: UInt64?
        var t: Double?

        enum K: String, CodingKey { case op, id, net, label, netNote, scopes, baseOutdated, vm, echoAck, n, t }

        init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: K.self)
            op = try? c.decodeIfPresent(String.self, forKey: .op)
            id = try? c.decodeIfPresent(String.self, forKey: .id)
            net = try? c.decodeIfPresent(String.self, forKey: .net)
            label = try? c.decodeIfPresent(String.self, forKey: .label)
            netNote = try? c.decodeIfPresent(String.self, forKey: .netNote)
            scopes = (try? c.decodeIfPresent([LenientScope].self, forKey: .scopes))?.compactMap(\.scope)
            baseOutdated = try? c.decodeIfPresent(Bool.self, forKey: .baseOutdated)
            vm = try? c.decodeIfPresent(Bool.self, forKey: .vm)
            echoAck = try? c.decodeIfPresent(Bool.self, forKey: .echoAck)
            if let u = try? c.decodeIfPresent(UInt64.self, forKey: .n) { n = u }
            else if let d = try? c.decodeIfPresent(Double.self, forKey: .n), d.isFinite, d >= 0, d < 1.8e19 { n = UInt64(d) }
            t = try? c.decodeIfPresent(Double.self, forKey: .t)
        }
    }

    /// One scope, or nil when it is unusable (no id) — the others still count.
    private struct LenientScope: Decodable {
        var scope: TermScope?
        init(from decoder: any Decoder) throws { scope = try? TermScope(from: decoder) }
    }
}

// MARK: - Opening a session

/// What a socket asks for: a new session on a tile, or a reattach.
public enum TermTarget: Equatable, Sendable {
    /// A new session on the tile at `cwd` (a component path).
    case new(TermNewSession)
    /// Reattach to a session by id; the server replays its scrollback first.
    case reattach(id: String)
}

/// New-session options (docs/protocol.md §/ws/term query params). The server
/// may clamp them; the `session` frame reports what it granted.
public struct TermNewSession: Equatable, Sendable {
    public var cwd: String
    /// nil = the tile's default scope.
    public var net: String?
    /// "none", "all", an index or a UUID.
    public var gpu: String
    /// false mints no terminal token (`api=0`).
    public var api: Bool
    /// A VM sandbox (D89).
    public var vm: Bool

    public init(cwd: String, net: String? = nil, gpu: String = "none", api: Bool = true, vm: Bool = false) {
        self.cwd = cwd; self.net = net; self.gpu = gpu; self.api = api; self.vm = vm
    }
}

public enum TermPaths {
    /// The socket's path and query, relative to the workspace origin:
    /// `/ws/term?cwd=…&net=…&gpu=…&api=…[&vm=1]` or `/ws/term?session=…`
    /// (the same parameters, in the same order, as bx-terminal sends).
    public static func socket(_ target: TermTarget) -> String {
        switch target {
        case .reattach(let id):
            return "/ws/term?session=\(encodeURIComponent(id))"
        case .new(let o):
            var q = "cwd=\(encodeURIComponent(o.cwd))"
            if let net = o.net, !net.isEmpty { q += "&net=\(encodeURIComponent(net))" }
            q += "&gpu=\(encodeURIComponent(o.gpu.isEmpty ? "none" : o.gpu))"
            q += "&api=\(o.api ? "1" : "0")"
            if o.vm { q += "&vm=1" }
            return "/ws/term?\(q)"
        }
    }

    /// `DELETE` this to end a session now (creator or admin) → 204, 404 unknown.
    public static func kill(session id: String) -> String { "/ws/term?session=\(encodeURIComponent(id))" }

    /// `GET` → TermEnvState; `DELETE` wipes the tile's persistent terminal layer → 204.
    public static func env(cwd: String) -> String { "/ws/term/env?cwd=\(encodeURIComponent(cwd))" }

    /// JavaScript's encodeURIComponent: everything but A–Z a–z 0–9 - _ . ! ~ * ' ( ).
    public static func encodeURIComponent(_ s: String) -> String {
        var out = ""
        for b in s.utf8 {
            switch b {
            case UInt8(ascii: "A")...UInt8(ascii: "Z"), UInt8(ascii: "a")...UInt8(ascii: "z"), UInt8(ascii: "0")...UInt8(ascii: "9"),
                 UInt8(ascii: "-"), UInt8(ascii: "_"), UInt8(ascii: "."), UInt8(ascii: "!"), UInt8(ascii: "~"),
                 UInt8(ascii: "*"), UInt8(ascii: "'"), UInt8(ascii: "("), UInt8(ascii: ")"):
                out.unicodeScalars.append(Unicode.Scalar(b))
            default:
                let hex = Array("0123456789ABCDEF".unicodeScalars)
                out += "%"
                out.unicodeScalars.append(hex[Int(b >> 4)])
                out.unicodeScalars.append(hex[Int(b & 0xf)])
            }
        }
        return out
    }
}

/// `GET /ws/term/env?cwd=` — the tile's persistent terminal layer and whether
/// a VM terminal would start there.
public struct TermEnvState: Equatable, Sendable, Decodable {
    public var exists: Bool
    public var baseOutdated: Bool
    public var vm: TermVMStatus?

    public init(exists: Bool = false, baseOutdated: Bool = false, vm: TermVMStatus? = nil) {
        self.exists = exists; self.baseOutdated = baseOutdated; self.vm = vm
    }
    enum K: String, CodingKey { case exists, baseOutdated, vm }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: K.self)
        exists = (try? c.decodeIfPresent(Bool.self, forKey: .exists)) ?? false
        baseOutdated = (try? c.decodeIfPresent(Bool.self, forKey: .baseOutdated)) ?? false
        vm = try? c.decodeIfPresent(TermVMStatus.self, forKey: .vm)
    }
}

/// Whether VM terminals can open here, and why not (D89/D90).
public struct TermVMStatus: Equatable, Sendable, Decodable {
    public var available: Bool
    public var reason: String
    /// No KVM: software emulation, several times slower (`note` says why).
    public var emulated: Bool
    public var note: String
    public var memMiB: Int
    public var vcpus: Int

    public init(available: Bool = false, reason: String = "", emulated: Bool = false, note: String = "", memMiB: Int = 0, vcpus: Int = 0) {
        self.available = available; self.reason = reason; self.emulated = emulated; self.note = note
        self.memMiB = memMiB; self.vcpus = vcpus
    }
    enum K: String, CodingKey { case available, reason, emulated, note, memMiB, vcpus }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: K.self)
        available = (try? c.decodeIfPresent(Bool.self, forKey: .available)) ?? false
        reason = (try? c.decodeIfPresent(String.self, forKey: .reason)) ?? ""
        emulated = (try? c.decodeIfPresent(Bool.self, forKey: .emulated)) ?? false
        note = (try? c.decodeIfPresent(String.self, forKey: .note)) ?? ""
        memMiB = (try? c.decodeIfPresent(Int.self, forKey: .memMiB)) ?? 0
        vcpus = (try? c.decodeIfPresent(Int.self, forKey: .vcpus)) ?? 0
    }
}
