import Foundation

// The agent-session routes (docs/protocol.md §/api/xbin: agent/providers,
// term/sessions[/…], agent/history) over an injected transport — the app
// supplies one backed by URLSession with the workspace's device session
// (plans/native.md §5); tests supply a fake. Paths are root-relative
// ("/api/xbin/…"): the transport owns the base URL, auth and cookies.

public struct HTTPRequest: Sendable, Hashable {
    public var method: String
    /// Root-relative, already percent-encoded ("/api/xbin/term/sessions/abc/events").
    public var path: String
    public var query: [(String, String)]
    public var headers: [String: String]
    public var body: Data?

    public init(method: String, path: String, query: [(String, String)] = [], headers: [String: String] = [:], body: Data? = nil) {
        self.method = method
        self.path = path
        self.query = query
        self.headers = headers
        self.body = body
    }

    /// path?query, percent-encoded.
    public var target: String {
        guard !query.isEmpty else { return path }
        return path + "?" + query.map { enc($0.0) + "=" + enc($0.1) }.joined(separator: "&")
    }

    public static func == (a: HTTPRequest, b: HTTPRequest) -> Bool {
        a.method == b.method && a.path == b.path && a.query.map { [$0.0, $0.1] } == b.query.map { [$0.0, $0.1] } && a.headers == b.headers && a.body == b.body
    }

    public func hash(into h: inout Hasher) {
        h.combine(method)
        h.combine(target)
        h.combine(body)
    }
}

public struct HTTPResponse: Sendable, Hashable {
    public var status: Int
    public var headers: [String: String]
    public var body: Data

    public init(status: Int, headers: [String: String] = [:], body: Data = Data()) {
        self.status = status
        self.headers = headers
        self.body = body
    }
}

/// How the client reaches xbind. `send` is a plain request/response;
/// `stream` opens a streaming response (the NDJSON follow) and yields its
/// body as it arrives — a non-2xx status throws `AgentAPIError` (with the
/// body's `error` when it has one) before any chunk.
public protocol AgentTransport: Sendable {
    func send(_ request: HTTPRequest) async throws -> HTTPResponse
    func stream(_ request: HTTPRequest) async throws -> AsyncThrowingStream<Data, any Error>
}

/// A refusal from xbind: the status and its `{error}` text.
public struct AgentAPIError: Error, Sendable, Hashable, CustomStringConvertible {
    public var status: Int
    public var message: String

    public init(status: Int, message: String) {
        self.status = status
        self.message = message
    }

    public init(_ r: HTTPResponse) {
        status = r.status
        let fromJSON = (try? JSONValue.parse(r.body))?["error"]?.string
        message = fromJSON ?? String(decoding: r.body.prefix(512), as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    public var description: String { message.isEmpty ? "HTTP \(status)" : message }
    /// 404: the session (or request) is gone — a permission already answered, a session ended.
    public var isNotFound: Bool { status == 404 }
    /// 409: a turn runs, the session ended, or a resume is impossible.
    public var isConflict: Bool { status == 409 }
    public var isForbidden: Bool { status == 403 }
    /// Reads like a sign-in problem (offer the provider's login).
    public var looksLikeAuth: Bool { Self.looksLikeAuth(message) }

    public static func looksLikeAuth(_ s: String) -> Bool { (try? RX.auth.firstMatch(in: s)) != nil }
}

/// The client for one workspace's agent sessions.
public struct AgentClient: Sendable {
    public let transport: any AgentTransport
    /// Where the xbin API lives ("/api/xbin").
    public var prefix: String

    public init(transport: any AgentTransport, prefix: String = "/api/xbin") {
        self.transport = transport
        self.prefix = prefix
    }

    // MARK: providers, sessions

    /// `GET /agent/providers`.
    public func providers() async throws -> [AgentProvider] {
        try await get("/agent/providers").list(AgentProvider.self) ?? []
    }

    /// `GET /term/sessions[?cwd=]` — the caller's sessions (shells too; see `SessionInfo.isAgent`).
    public func sessions(cwd: String? = nil) async throws -> [SessionInfo] {
        try await get("/term/sessions", query: cwd.map { [("cwd", $0)] } ?? []).list(SessionInfo.self) ?? []
    }

    /// `POST /term/sessions {kind:"agent", …}` → the new session (status starting).
    public func create(_ req: CreateAgentSession) async throws -> SessionInfo {
        let v = try await call("POST", "/term/sessions", body: req.body)
        guard let s = SessionInfo(json: v) else { throw AgentAPIError(status: 200, message: "unreadable session") }
        return s
    }

    /// `GET /term/sessions/<id>` → {session, permissions}.
    public func session(_ id: String) async throws -> SessionSnapshot {
        let v = try await get(sessionPath(id))
        guard let s = SessionSnapshot(json: v) else { throw AgentAPIError(status: 200, message: "unreadable session") }
        return s
    }

    /// `DELETE /term/sessions/<id>` — ends it.
    public func end(_ id: String) async throws {
        _ = try await call("DELETE", sessionPath(id))
    }

    /// `PATCH /term/sessions/<id> {name}` — names it (empty clears).
    public func rename(_ id: String, name: String) async throws {
        _ = try await call("PATCH", sessionPath(id), body: ["name": .string(name)])
    }

    // MARK: turns

    /// `POST …/prompt {text, attachments?}` → the turn number; 409 while a
    /// turn runs. With attachments `text` may be empty; they are checked
    /// against the server's limits first (``PromptAttachment/check(_:)``:
    /// a 413 or 400 `AgentAPIError` without a request, as xbind would
    /// answer). Without them the body is `{text}`, as before.
    public func prompt(_ id: String, text: String, attachments: [PromptAttachment] = []) async throws -> PromptAccepted {
        if attachments.isEmpty {
            let v = try await call("POST", sessionPath(id) + "/prompt", body: ["text": .string(text)])
            return PromptAccepted(turn: v["turn"]?.int ?? 0)
        }
        if let problem = PromptAttachment.check(attachments) {
            throw AgentAPIError(status: problem.status, message: problem.description)
        }
        let v = try await call("POST", sessionPath(id) + "/prompt", rawBody: PromptAttachment.body(text: text, attachments))
        return PromptAccepted(turn: v["turn"]?.int ?? 0)
    }

    /// `POST …/cancel` — the turn ends cancelled; pending requests are cancelled.
    public func cancel(_ id: String) async throws {
        _ = try await call("POST", sessionPath(id) + "/cancel")
    }

    /// `POST …/permissions/<pid>` — the first answer wins (404 after).
    public func answerPermission(_ id: String, pid: String, _ answer: PermissionAnswer) async throws {
        _ = try await call("POST", sessionPath(id) + "/permissions/" + seg(pid), body: answer.body)
    }

    /// `POST …/elicitations/<eid> {action, content?}` — content (the form's
    /// values, an object) only with accept.
    public func answerQuestion(_ id: String, eid: String, action: QuestionAction, content: JSONValue? = nil) async throws {
        var o = JSONObject([("action", .string(action.rawValue))])
        if action == .accept { o["content"] = content ?? .object(JSONObject()) }
        _ = try await call("POST", sessionPath(id) + "/elicitations/" + seg(eid), body: .object(o))
    }

    /// `POST …/options {id, value}` — a setting the agent advertised (or
    /// "mode"); applied to the next turn, the refreshed list rides a status.
    public func setOption(_ id: String, option: String, value: String) async throws {
        _ = try await call("POST", sessionPath(id) + "/options", body: ["id": .string(option), "value": .string(value)])
    }

    /// `POST …/restart {net?, api?, gpu?, vm?}` → the new session, resuming
    /// the conversation where the agent can.
    public func restart(_ id: String, _ opts: RestartOptions = RestartOptions()) async throws -> RestartResult {
        let v = try await call("POST", sessionPath(id) + "/restart", body: opts.body)
        guard let s = v["session"].flatMap(SessionInfo.init(json:)) else { throw AgentAPIError(status: 200, message: "unreadable session") }
        return RestartResult(session: s, resumed: v["resumed"]?.bool ?? false)
    }

    // MARK: events

    /// `GET …/events?since=` → a page.
    public func events(_ id: String, since: UInt64 = 0) async throws -> EventsPage {
        let v = try await get(sessionPath(id) + "/events", query: [("since", String(since))])
        guard let p = EventsPage(json: v) else { throw AgentAPIError(status: 200, message: "unreadable events") }
        return p
    }

    /// `GET …/events?since=&follow=1` — the log from the cursor as it grows,
    /// until the session ends (the stream finishes) or the caller stops
    /// iterating. A `gap` event comes first when the cursor predates the ring.
    public func follow(_ id: String, since: UInt64 = 0) async throws -> AsyncThrowingStream<AgentEvent, any Error> {
        let req = HTTPRequest(method: "GET", path: prefix + sessionPath(id) + "/events",
                              query: [("since", String(since)), ("follow", "1")], headers: ["Accept": "application/x-ndjson"])
        let chunks = try await transport.stream(req)
        return AsyncThrowingStream { cont in
            let task = Task {
                var lines = NDJSONLines()
                do {
                    for try await chunk in chunks {
                        for v in lines.feed(chunk) { if let e = AgentEvent(json: v) { cont.yield(e) } }
                    }
                    for v in lines.finish() { if let e = AgentEvent(json: v) { cont.yield(e) } }
                    cont.finish()
                } catch {
                    cont.finish(throwing: error)
                }
            }
            cont.onTermination = { _ in task.cancel() }
        }
    }

    /// `GET …/log` — the adapter's stderr and driver notes (debugging).
    public func log(_ id: String) async throws -> String {
        let r = try await transport.send(HTTPRequest(method: "GET", path: prefix + sessionPath(id) + "/log"))
        guard (200..<300).contains(r.status) else { throw AgentAPIError(r) }
        return String(decoding: r.body, as: UTF8.self)
    }

    // MARK: history

    /// `GET /agent/history[?cwd=]` — past sessions, newest first.
    public func history(cwd: String? = nil) async throws -> [HistoryEntry] {
        try await get("/agent/history", query: cwd.map { [("cwd", $0)] } ?? []).list(HistoryEntry.self) ?? []
    }

    /// `GET /agent/history/<id>/events` — a past session's transcript.
    public func historyTranscript(_ id: String) async throws -> HistoryTranscript {
        let v = try await get("/agent/history/" + seg(id) + "/events")
        guard let t = HistoryTranscript(json: v) else { throw AgentAPIError(status: 200, message: "unreadable history") }
        return t
    }

    /// `DELETE /agent/history/<id>` — forget it.
    public func deleteHistory(_ id: String) async throws {
        _ = try await call("DELETE", "/agent/history/" + seg(id))
    }

    // MARK: -

    func sessionPath(_ id: String) -> String { "/term/sessions/" + seg(id) }

    func get(_ path: String, query: [(String, String)] = []) async throws -> JSONValue {
        try await call("GET", path, query: query)
    }

    func call(_ method: String, _ path: String, query: [(String, String)] = [], body: JSONValue? = nil) async throws -> JSONValue {
        try await call(method, path, query: query, rawBody: body?.data)
    }

    /// A call whose JSON body is already encoded (a prompt's attachments:
    /// tens of MiB of base64 are appended as bytes, not built as a string).
    func call(_ method: String, _ path: String, query: [(String, String)] = [], rawBody: Data?) async throws -> JSONValue {
        var headers: [String: String] = ["Accept": "application/json"]
        if rawBody != nil { headers["Content-Type"] = "application/json" }
        let r = try await transport.send(HTTPRequest(method: method, path: prefix + path, query: query, headers: headers, body: rawBody))
        guard (200..<300).contains(r.status) else { throw AgentAPIError(r) }
        if r.body.isEmpty || r.status == 204 { return .null }
        return try JSONValue.parse(r.body)
    }
}

// MARK: - prompt attachments

/// A file sent with a prompt (`POST …/prompt {text, attachments:[{name,
/// mime, data}]}`, docs/protocol.md): a photo, a screenshot, a log, a PDF.
/// xbind writes each into the agent's sandbox and hands it over by path; a
/// png/jpeg/gif/webp — the bytes decide — also goes to the model inline
/// when it is small enough (``maxInlineImageBytes``, ``maxInlineImagesBytes``
/// per prompt), a text file ≤ ``maxInlineTextBytes`` as embedded context.
/// Downscale photos (``maxImageEdge``) and send HEIC as JPEG.
public struct PromptAttachment: Sendable, Hashable {
    /// A plain file name (xbind keeps the last path element and makes it unique).
    public var name: String
    /// Its media type; "" lets xbind decide (from the name, else the bytes).
    public var mime: String
    public var data: Data

    public init(name: String, mime: String = "", data: Data) {
        self.name = name
        self.mime = mime
        self.data = data
    }

    // xbind's limits (internal/agent/attachments.go, internal/server/agentapi.go).
    public static let maxCount = 10
    public static let maxFileBytes = 10 << 20
    public static let maxTotalBytes = 20 << 20
    /// The whole request body (base64 grows the bytes by 4/3).
    public static let maxBodyBytes = 32 << 20
    /// An image the model sees inline (5 MiB as base64); a bigger one is a file only.
    public static let maxInlineImageBytes = 15 << 18
    /// Inline images per prompt, together.
    public static let maxInlineImagesBytes = 4 << 20
    /// A text file sent inline as embedded context.
    public static let maxInlineTextBytes = 128 << 10
    /// All a model reads of a photo's long edge.
    public static let maxImageEdge = 2576
    /// The largest image the app reads to make it model-ready: a 48 MP
    /// photo, a panorama or a big PNG is redrawn to ``maxImageEdge`` and
    /// ≤ ``maxInlineImageBytes`` before it rides, so it may start well over
    /// ``maxFileBytes``.
    public static let maxPickedImageBytes = 64 << 20

    /// The most of a picked file the app reads for a prompt: an image up to
    /// ``maxPickedImageBytes`` (it is redrawn smaller first), anything else
    /// up to ``maxFileBytes`` — it goes as it is. A bigger file is left
    /// unread (``refusal(name:bytes:read:)``).
    public static func readLimit(image: Bool) -> Int { image ? maxPickedImageBytes : maxFileBytes }

    /// Why a picked file can't wait for the next prompt, or nil: `bytes` is
    /// its size once the app prepared it (a photo redrawn) — held to
    /// ``maxFileBytes`` only then — or, with `read` false, the size it was
    /// left unread at (over ``readLimit(image:)``).
    public static func refusal(name: String, bytes: Int, read: Bool = true) -> AttachmentProblem? {
        if !read { return .tooLargeToRead(name: name, bytes: bytes) }
        return bytes > maxFileBytes ? .fileTooLarge(name: name, bytes: bytes) : nil
    }

    /// The image type the bytes are, when it is one the model takes inline
    /// (png, jpeg, gif, webp — xbind sniffs the same way; the declared type
    /// doesn't count).
    public var inlineImageType: String? { Self.sniffImage(data) }

    /// Whether the model would get this image inline on its own (the
    /// prompt's total may still push it to a file).
    public var fitsInline: Bool { inlineImageType != nil && data.count <= Self.maxInlineImageBytes }

    public static func sniffImage(_ d: Data) -> String? {
        let b = [UInt8](d.prefix(12))
        if b.count >= 8, b[0...7] == [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A] { return "image/png" }
        if b.count >= 3, b[0...2] == [0xFF, 0xD8, 0xFF] { return "image/jpeg" }
        if b.count >= 6, b[0...5] == Array("GIF87a".utf8)[...] || b[0...5] == Array("GIF89a".utf8)[...] { return "image/gif" }
        if b.count >= 12, b[0...3] == Array("RIFF".utf8)[...], b[8...11] == Array("WEBP".utf8)[...] { return "image/webp" }
        return nil
    }

    /// What to do with a picked image before it rides a prompt.
    public enum ImagePlan: Sendable, Hashable {
        /// Send the bytes as they are.
        case keep
        /// Redraw at this size (never larger) and encode: PNG when `png`
        /// (a screenshot stays sharp; the app falls back to JPEG when the PNG
        /// is still over ``maxInlineImageBytes``), else JPEG.
        case reencode(width: Int, height: Int, png: Bool)
    }

    /// The plan for an image of `width`×`height` pixels, `bytes` long,
    /// whose bytes sniff as `type` (``sniffImage(_:)``; nil for HEIC,
    /// TIFF, …): a model-ready image (png/jpeg/webp, the long edge ≤
    /// ``maxImageEdge``, ≤ ``maxInlineImageBytes``) and any GIF (it may
    /// move) are kept; anything else is redrawn to fit.
    public static func imagePlan(width: Int, height: Int, bytes: Int, type: String?) -> ImagePlan {
        if type == "image/gif" { return .keep }
        let edge = max(width, height)
        if type != nil, edge <= maxImageEdge, bytes <= maxInlineImageBytes { return .keep }
        guard edge > 0 else { return .keep }
        let scale = min(1, Double(maxImageEdge) / Double(edge))
        let w = max(1, Int((Double(width) * scale).rounded()))
        let h = max(1, Int((Double(height) * scale).rounded()))
        return .reencode(width: w, height: h, png: type == "image/png")
    }

    /// Why xbind would refuse these, or nil: at most ``maxCount``, each ≤
    /// ``maxFileBytes``, together ≤ ``maxTotalBytes``.
    public static func check(_ list: [PromptAttachment]) -> AttachmentProblem? {
        if list.count > maxCount { return .tooMany(list.count) }
        var total = 0
        for a in list {
            if a.data.count > maxFileBytes { return .fileTooLarge(name: a.name, bytes: a.data.count) }
            total += a.data.count
            if total > maxTotalBytes { return .tooLargeTogether(bytes: total) }
        }
        return nil
    }

    /// The prompt's JSON body, the base64 appended as bytes.
    public static func body(text: String, _ list: [PromptAttachment]) -> Data {
        var out = Data()
        out.reserveCapacity(64 + list.reduce(0) { $0 + ($1.data.count + 2) / 3 * 4 + 96 })
        out.append(contentsOf: #"{"text":"#.utf8)
        out.append(JSONValue.string(text).data)
        out.append(contentsOf: #","attachments":["#.utf8)
        for (i, a) in list.enumerated() {
            if i > 0 { out.append(UInt8(ascii: ",")) }
            out.append(contentsOf: #"{"name":"#.utf8)
            out.append(JSONValue.string(a.name).data)
            if !a.mime.isEmpty {
                out.append(contentsOf: #","mime":"#.utf8)
                out.append(JSONValue.string(a.mime).data)
            }
            out.append(contentsOf: #","data":""#.utf8)
            out.append(a.data.base64EncodedData())
            out.append(contentsOf: #""}"#.utf8)
        }
        out.append(contentsOf: "]}".utf8)
        return out
    }
}

/// A prompt's attachments past xbind's limits.
public enum AttachmentProblem: Error, Sendable, Hashable, CustomStringConvertible {
    case tooMany(Int)
    case fileTooLarge(name: String, bytes: Int)
    case tooLargeTogether(bytes: Int)
    /// Picked, but too big to read at all (``PromptAttachment/readLimit(image:)``).
    case tooLargeToRead(name: String, bytes: Int)

    /// What xbind answers: 400 for too many, 413 past a size limit.
    public var status: Int {
        if case .tooMany = self { return 400 }
        return 413
    }

    public var description: String {
        switch self {
        case .tooMany(let n):
            return "\(n) attachments (at most \(PromptAttachment.maxCount))"
        case .fileTooLarge(let name, let bytes):
            return "\(name) is \(mib(bytes)) — the limit for a file is \(mib(PromptAttachment.maxFileBytes))"
        case .tooLargeTogether:
            return "the attachments are over \(mib(PromptAttachment.maxTotalBytes)) together"
        case .tooLargeToRead(let name, let bytes):
            return "\(name) is \(mib(bytes)) — too large to attach (a photo up to \(mib(PromptAttachment.maxPickedImageBytes)), "
                + "another file up to \(mib(PromptAttachment.maxFileBytes)))"
        }
    }

    private func mib(_ n: Int) -> String {
        let v = Double(n) / Double(1 << 20)
        return v == v.rounded() ? "\(Int(v)) MiB" : String(format: "%.1f MiB", v)
    }
}

/// Splits an NDJSON byte stream into JSON values: lines may arrive split
/// across chunks; blank and unparseable lines are skipped (counted).
public struct NDJSONLines: Sendable {
    private var buf = Data()
    public private(set) var skipped = 0

    public init() {}

    public mutating func feed(_ chunk: Data) -> [JSONValue] {
        buf.append(chunk)
        var out: [JSONValue] = []
        while let nl = buf.firstIndex(of: 0x0A) {
            let line = buf[buf.startIndex..<nl]
            buf = Data(buf[buf.index(after: nl)...])
            parse(line, into: &out)
        }
        return out
    }

    /// The last line when the stream ended without a newline.
    public mutating func finish() -> [JSONValue] {
        var out: [JSONValue] = []
        if !buf.isEmpty { parse(buf, into: &out) }
        buf = Data()
        return out
    }

    private mutating func parse(_ line: Data, into out: inout [JSONValue]) {
        let trimmed = line.filter { $0 != 0x0D }
        guard trimmed.contains(where: { $0 != 0x20 && $0 != 0x09 }) else { return }
        if let v = try? JSONValue.parse(Data(trimmed)) { out.append(v) } else { skipped += 1 }
    }
}

/// Percent-encodes a path segment (session ids are hex today; pids and eids
/// are "p1", "e1" — encoded anyway).
func seg(_ s: String) -> String { s.addingPercentEncoding(withAllowedCharacters: segAllowed) ?? s }

func enc(_ s: String) -> String { s.addingPercentEncoding(withAllowedCharacters: queryAllowed) ?? s }

private let unreserved = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"
private let segAllowed = CharacterSet(charactersIn: unreserved)
private let queryAllowed = CharacterSet(charactersIn: unreserved + "/:")
