import Foundation

// A change set reviewed a file at a time (D210, #45): what a turn or a tool
// call changed, as the Agent screen lists it and its review screen shows it.
// The transcript never holds a whole patch's lines: a card lists the files,
// and a file's diff is loaded when the reader opens it (the web's
// agent-diffs.js, in the same order):
//   1. the file's part of the files.changed event's own patch, when the
//      patch holds all of it (the event's patch is capped: 64 KiB a call,
//      192 KiB a turn);
//   2. else `GET …/diff?turn=|toolCallId=&path=` — the whole file's diff,
//      while the session lives;
//   3. else what the event's patch holds of it, marked cut.
// An edit tool's own ACP diffs (whole old and new texts) are diffed here,
// one file at a time. Each file is parsed once (ReviewLoader caches it), off
// the main actor, into numbered lines a lazy list draws.

/// Which change set the full-diff route serves (a files.changed event's
/// `turn` or `toolCallId`).
public enum ChangeSetKey: Sendable, Hashable {
    case turn(Int)
    case toolCall(String)

    var query: (String, String) {
        switch self {
        case .turn(let n): return ("turn", String(n))
        case .toolCall(let id): return ("toolCallId", id)
        }
    }
}

/// One file of a change set: a row of its list.
public struct ReviewFile: Sendable, Hashable, Identifiable {
    public var path: String
    /// A rename's source.
    public var oldPath: String?
    public var status: FileChangeStatus
    /// +/- lines (nil: not counted — an edit's diff is counted once opened).
    public var added: Int?
    public var removed: Int?
    public var binary: Bool
    public var id: String { path }

    public init(path: String, oldPath: String? = nil, status: FileChangeStatus, added: Int? = nil, removed: Int? = nil, binary: Bool = false) {
        self.path = path
        self.oldPath = oldPath
        self.status = status
        self.added = added
        self.removed = removed
        self.binary = binary
    }

    /// The status letter the web shows: A, M, D, R, T.
    public var letter: String {
        switch status {
        case .added: return "A"
        case .modified: return "M"
        case .deleted: return "D"
        case .renamed: return "R"
        case .typechange: return "T"
        case .other(let s): return s.first.map { String($0).uppercased() } ?? "?"
        }
    }

    /// The file's name and the folder it is in ("" at the top).
    public var name: String { path.split(separator: "/").last.map(String.init) ?? path }
    public var folder: String {
        guard let i = path.lastIndex(of: "/") else { return "" }
        return String(path[..<i])
    }
}

/// What a turn or a tool call changed: its files, and where each one's diff
/// comes from.
public struct ChangeSet: Sendable, Hashable, Identifiable {
    public enum Source: Sendable, Hashable {
        /// A files.changed event: its (capped) patch, and the route's key.
        case snapshot(key: ChangeSetKey?, patch: String, truncated: Bool)
        /// An edit tool's ACP diffs: each file's whole old and new text.
        case content([ToolDiff])
    }

    /// The transcript item it hangs on (a turn's changes, a tool call).
    public let id: String
    public var files: [ReviewFile]
    public var source: Source

    public init(id: String, files: [ReviewFile], source: Source) {
        self.id = id
        self.files = files
        self.source = source
    }

    /// A files.changed event's change set.
    public init(id: String, files f: FilesChanged) {
        let key: ChangeSetKey? = f.toolCallId.map { .toolCall($0) } ?? f.turn.map { .turn($0) }
        self.init(id: id, files: f.changes.map {
            ReviewFile(path: $0.path, oldPath: $0.oldPath, status: $0.status, added: $0.add, removed: $0.del, binary: $0.binary)
        }, source: .snapshot(key: key, patch: f.patch?.text ?? "", truncated: f.patch?.truncated ?? false))
    }

    /// What a whole turn changed.
    public init(turn c: TurnChanges) {
        var f = c.files
        if f.turn == nil, f.toolCallId == nil { f.turn = c.turn }
        self.init(id: c.id, files: f)
    }

    /// What a tool call changed: its snapshot diff, else its own ACP diffs;
    /// nil when it changed nothing. No diffing here (a card builds it on
    /// every render): an edit's files are counted only when one diff is
    /// (the call's totals).
    public init?(tool t: ToolCall) {
        if let f = t.files {
            var f = f
            if f.toolCallId == nil, f.turn == nil { f.toolCallId = t.toolCallId }
            self.init(id: t.id, files: f)
            return
        }
        let d = t.diffs
        guard !d.isEmpty else { return nil }
        let one = d.count == 1 ? t.contentDiffStat : nil
        self.init(id: t.id, files: d.map {
            ReviewFile(path: $0.path, status: $0.oldText == nil ? .added : .modified, added: one?.added, removed: one?.removed)
        }, source: .content(d))
    }

    /// Totals across the files (those counted).
    public var added: Int { files.reduce(0) { $0 + ($1.added ?? 0) } }
    public var removed: Int { files.reduce(0) { $0 + ($1.removed ?? 0) } }

    /// "3 files" (or "1 file").
    public var count: String { files.count == 1 ? "1 file" : "\(files.count) files" }
}

/// One line of a file's diff as the review draws it.
public struct ReviewLine: Sendable, Hashable, Identifiable {
    public enum Kind: Sendable, Hashable {
        /// "@@ -a,b +c,d @@ section"
        case hunk
        case added, removed, context
        /// "\ No newline at end of file"
        case note
        /// a rename or a mode change
        case meta
    }

    public let id: Int
    public var kind: Kind
    /// The line without its +/-/space marker (a hunk's or note's whole line).
    public var text: String
    /// Its line number: the new file's (the old file's for a removed line).
    public var number: Int?
}

/// One file's diff, ready to draw.
public struct FileReview: Sendable, Hashable {
    public enum Origin: Sendable, Hashable { case inline, server, content }

    /// The lines shown before "Show all".
    public static let shownLines = 400

    public var lines: [ReviewLine]
    public var added: Int
    public var removed: Int
    public var binary: Bool
    /// The diff stops early: the rest was past what the event (or the route) keeps.
    public var cut: Bool
    public var origin: Origin
    /// Why there is nothing to show (nil when there is).
    public var note: String?
    /// The digits of the largest line number (the gutter's width).
    public var numberDigits: Int

    public init(lines: [ReviewLine], added: Int, removed: Int, binary: Bool = false, cut: Bool = false, origin: Origin, note: String? = nil) {
        self.lines = lines
        self.added = added
        self.removed = removed
        self.binary = binary
        self.cut = cut
        self.origin = origin
        self.note = note
        numberDigits = String(lines.reduce(0) { max($0, $1.number ?? 0) }).count
    }

    /// Nothing to draw, and why.
    public static func unavailable(_ why: String, origin: Origin = .inline) -> FileReview {
        FileReview(lines: [], added: 0, removed: 0, origin: origin, note: why)
    }

    /// What the review says when it has no lines to draw (nil: it has some).
    public var emptyText: String? {
        if let note { return note }
        if binary { return "A binary file" }
        if lines.isEmpty { return "No line changes" }
        return nil
    }

    /// One file's part of a git patch (from "diff --git" to the next), as lines.
    public static func parse(_ part: Substring, origin: Origin, cut: Bool = false) -> FileReview {
        var out: [ReviewLine] = []
        var added = 0, removed = 0, binary = false
        var oldN = 0, newN = 0, oldLeft = 0, newLeft = 0
        var inHunk = false
        func add(_ k: ReviewLine.Kind, _ t: Substring, _ n: Int?) { out.append(ReviewLine(id: out.count, kind: k, text: String(t), number: n)) }
        for raw in part.split(separator: "\n", omittingEmptySubsequences: false) {
            if inHunk, oldLeft > 0 || newLeft > 0, let c = raw.first, c == "+" || c == "-" || c == " " {
                switch c {
                case "+": add(.added, raw.dropFirst(), newN); newN += 1; newLeft -= 1; added += 1
                case "-": add(.removed, raw.dropFirst(), oldN); oldN += 1; oldLeft -= 1; removed += 1
                default: add(.context, raw.dropFirst(), newN); oldN += 1; newN += 1; oldLeft -= 1; newLeft -= 1
                }
                continue
            }
            if inHunk, raw.hasPrefix("\\") { add(.note, raw, nil); continue }
            if raw.hasPrefix("@@"), let h = hunkHeader(raw) {
                (oldN, oldLeft, newN, newLeft) = h
                inHunk = true
                add(.hunk, raw, nil)
                continue
            }
            inHunk = false
            if raw.hasPrefix("Binary files") || raw.hasPrefix("GIT binary patch") {
                binary = true
            } else if raw.hasPrefix("rename from ") || raw.hasPrefix("rename to ") || raw.hasPrefix("old mode ") || raw.hasPrefix("new mode ") {
                add(.meta, raw, nil)
            }
        }
        return FileReview(lines: out, added: added, removed: removed, binary: binary, cut: cut, origin: origin)
    }

    /// An edit's whole old and new text, diffed (XbinAgent's LCS, as the
    /// web's unifiedDiff) and grouped into hunks with 3 lines of context.
    public static func content(_ d: ToolDiff) -> FileReview {
        var out: [ReviewLine] = []
        var added = 0, removed = 0
        for h in d.hunks(context: 3) {
            out.append(ReviewLine(id: out.count, kind: .hunk, text: h.header, number: nil))
            for l in h.lines {
                switch l.kind {
                case .added: added += 1; out.append(ReviewLine(id: out.count, kind: .added, text: l.text, number: l.newLine))
                case .removed: removed += 1; out.append(ReviewLine(id: out.count, kind: .removed, text: l.text, number: l.oldLine))
                case .context: out.append(ReviewLine(id: out.count, kind: .context, text: l.text, number: l.newLine))
                case .note: out.append(ReviewLine(id: out.count, kind: .note, text: l.text, number: nil))
                }
            }
        }
        return FileReview(lines: out, added: added, removed: removed, origin: .content)
    }

    /// "@@ -a[,b] +c[,d] @@…" → (old start, old count, new start, new count).
    static func hunkHeader(_ line: Substring) -> (Int, Int, Int, Int)? {
        let parts = line.split(separator: " ", maxSplits: 3, omittingEmptySubsequences: true)
        guard parts.count >= 3, parts[0] == "@@", parts[1].first == "-", parts[2].first == "+" else { return nil }
        func range(_ s: Substring) -> (Int, Int)? {
            let nums = s.dropFirst().split(separator: ",", omittingEmptySubsequences: false)
            guard let a = Int(nums[0]) else { return nil }
            return (a, nums.count > 1 ? Int(nums[1]) ?? 1 : 1)
        }
        guard let o = range(parts[1]), let n = range(parts[2]) else { return nil }
        return (o.0, o.1, n.0, n.1)
    }
}

/// A git patch cut into its files' parts, in order (the web's fileDiffs):
/// each part from its "diff --git" line to the next; its path the file's
/// after the change (before it, for a deletion). No copies: the parts are
/// slices of the patch.
public enum PatchParts {
    public static func split(_ patch: String) -> [(path: String, text: Substring)] {
        var out: [(path: String, text: Substring)] = []
        var start: String.Index?
        var path = "", from = ""
        func close(_ end: String.Index) {
            if let s = start { out.append((path, patch[s..<end])) }
        }
        var i = patch.startIndex
        while i < patch.endIndex {
            let eol = patch[i...].firstIndex(of: "\n") ?? patch.endIndex
            let line = patch[i..<eol]
            if line.hasPrefix("diff --git ") {
                close(i)
                start = i
                (from, path) = paths(line.dropFirst(11))
            } else if start != nil, line.hasPrefix("deleted file mode") {
                path = from
            }
            i = eol < patch.endIndex ? patch.index(after: eol) : eol
        }
        close(patch.endIndex)
        return out
    }

    /// "a/x b/y" → (x, y) — git's unquoted form; a quoted one stays whole
    /// (and matches no file: the route serves it instead).
    static func paths(_ s: Substring) -> (String, String) {
        if s.hasPrefix("a/"), let r = s.range(of: " b/") {
            return (String(s[s.index(s.startIndex, offsetBy: 2)..<r.lowerBound]), String(s[r.upperBound...]))
        }
        return (String(s), String(s))
    }
}

extension AgentClient {
    /// `GET …/diff?turn=|toolCallId=[&path=]` — the complete git patch behind
    /// a files.changed event, or one file of it (the event's own is capped);
    /// `truncated` when xbind cut it (16 MiB). 404 once the session ended.
    public func diff(_ id: String, _ key: ChangeSetKey, path: String? = nil) async throws -> (text: String, truncated: Bool) {
        var q = [key.query]
        if let path { q.append(("path", path)) }
        let r = try await transport.send(HTTPRequest(method: "GET", path: prefix + sessionPath(id) + "/diff", query: q))
        guard (200..<300).contains(r.status) else { throw AgentAPIError(r) }
        let cut = r.headers.first { $0.key.lowercased() == "x-truncated" }?.value == "true"
        return (String(decoding: r.body, as: UTF8.self), cut)
    }
}

/// Loads a session's file diffs for the review and keeps the latest ones
/// parsed (an actor: parsing runs off the main actor).
public actor ReviewLoader {
    public let client: AgentClient
    private let max: Int
    private var cache: [String: FileReview] = [:]
    private var order: [String] = []
    private var inflight: [String: Task<FileReview, Never>] = [:]
    /// A change set's inline patch split per file (the latest few sets).
    private var parts: [String: [String: (text: Substring, last: Bool)]] = [:]
    private var partsOrder: [String] = []
    /// The route's requests, in order (the tests read them).
    public private(set) var loads: [String] = []

    public static let notKept = "This file is past what the transcript keeps of the change, and the session no longer has it"

    public init(client: AgentClient, max: Int = 32) {
        self.client = client
        self.max = max
    }

    /// One file's diff. `session` and `live` say whether the route can
    /// answer (a running session's; not a past one's).
    public func review(_ set: ChangeSet, file: ReviewFile, session: String?, live: Bool) async -> FileReview {
        let key = "\(session ?? "")|\(set.id)|\(file.path)"
        if let r = cache[key] {
            touch(key)
            return r
        }
        if let t = inflight[key] { return await t.value }
        let t = Task { await self.load(set, file: file, session: session, live: live) }
        inflight[key] = t
        let r = await t.value
        inflight[key] = nil
        if r.note == nil { // "not kept" may be a moment's failure: asked again next time
            cache[key] = r
            touch(key)
        }
        return r
    }

    private func touch(_ key: String) {
        order.removeAll { $0 == key }
        order.append(key)
        while order.count > max { cache[order.removeFirst()] = nil }
    }

    private func load(_ set: ChangeSet, file: ReviewFile, session: String?, live: Bool) async -> FileReview {
        if file.binary { return FileReview(lines: [], added: 0, removed: 0, binary: true, origin: .inline) }
        switch set.source {
        case .content(let diffs):
            guard let d = diffs.first(where: { $0.path == file.path }) else { return .unavailable("No diff for this file", origin: .content) }
            return FileReview.content(d)
        case .snapshot(let key, let patch, let truncated):
            let part = inlinePart("\(session ?? "")|\(set.id)", patch: patch, path: file.path)
            if let part, !(truncated && part.last) { return FileReview.parse(part.text, origin: .inline) }
            if live, let session, let key {
                loads.append("\(key.query.0)=\(key.query.1)&path=\(file.path)")
                if let r = try? await client.diff(session, key, path: file.path) {
                    return FileReview.parse(Substring(r.text), origin: .server, cut: r.truncated)
                }
            }
            if let part { return FileReview.parse(part.text, origin: .inline, cut: true) }
            return .unavailable(Self.notKept)
        }
    }

    private func inlinePart(_ k: String, patch: String, path: String) -> (text: Substring, last: Bool)? {
        if parts[k] == nil {
            let split = PatchParts.split(patch)
            var m: [String: (text: Substring, last: Bool)] = [:]
            for (i, p) in split.enumerated() where m[p.path] == nil { m[p.path] = (p.text, i == split.count - 1) }
            parts[k] = m
            partsOrder.append(k)
            while partsOrder.count > 8 { parts[partsOrder.removeFirst()] = nil }
        }
        return parts[k]?[path]
    }
}
