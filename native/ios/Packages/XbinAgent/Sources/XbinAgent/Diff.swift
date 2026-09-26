import Foundation

// Diffs for the native viewer: an ACP diff block's whole old/new text turned
// into lines by a line LCS (the port of web/agent-tools.js unifiedDiff — the
// same bound: past 400 000 cells it falls back to replace-all), grouped into
// hunks with context; a files.changed git patch parsed into files and hunks;
// and the web's +/- totals.

public enum DiffLineKind: Sendable, Hashable {
    case context, added, removed
    /// A "\ No newline at end of file" marker or another non-content line.
    case note
}

public struct DiffLine: Sendable, Hashable {
    public var kind: DiffLineKind
    public var text: String
    /// 1-based line numbers in the old/new file (nil where the line is absent).
    public var oldLine: Int?
    public var newLine: Int?

    public init(kind: DiffLineKind, text: String, oldLine: Int? = nil, newLine: Int? = nil) {
        self.kind = kind
        self.text = text
        self.oldLine = oldLine
        self.newLine = newLine
    }
}

public struct DiffHunk: Sendable, Hashable {
    public var oldStart: Int
    public var oldCount: Int
    public var newStart: Int
    public var newCount: Int
    /// The text after the second "@@" (git's function context), if any.
    public var section: String
    public var lines: [DiffLine]

    public var header: String {
        "@@ -\(oldStart),\(oldCount) +\(newStart),\(newCount) @@" + (section.isEmpty ? "" : " " + section)
    }
}

public struct FileDiff: Sendable, Hashable, Identifiable {
    /// The new path ("" for a deletion's /dev/null side: see oldPath).
    public var path: String
    public var oldPath: String
    public var status: FileChangeStatus
    public var binary: Bool
    public var hunks: [DiffHunk]
    public var id: String { path.isEmpty ? oldPath : path }

    public var added: Int { hunks.reduce(0) { $0 + $1.lines.filter { $0.kind == .added }.count } }
    public var removed: Int { hunks.reduce(0) { $0 + $1.lines.filter { $0.kind == .removed }.count } }
}

public enum LineDiff {
    /// Every line of old and new, aligned by an LCS: removed, added and context.
    public static func lines(old: String?, new: String?) -> [DiffLine] {
        let a = splitLines(old ?? "")
        let b = splitLines(new ?? "")
        if (old ?? "") == (new ?? "") { return a.enumerated().map { DiffLine(kind: .context, text: $1, oldLine: $0 + 1, newLine: $0 + 1) } }
        let n = a.count, m = b.count
        var out: [DiffLine] = []
        out.reserveCapacity(n + m)
        var oi = 1, ni = 1
        func del(_ s: String) { out.append(DiffLine(kind: .removed, text: s, oldLine: oi)); oi += 1 }
        func add(_ s: String) { out.append(DiffLine(kind: .added, text: s, newLine: ni)); ni += 1 }
        if n * m > 400_000 {
            a.forEach(del)
            b.forEach(add)
            return out
        }
        // dp[i][j] = LCS length of a[i...] and b[j...], flattened
        let w = m + 1
        var dp = [UInt32](repeating: 0, count: (n + 1) * w)
        if n > 0, m > 0 {
            for i in stride(from: n - 1, through: 0, by: -1) {
                for j in stride(from: m - 1, through: 0, by: -1) {
                    dp[i * w + j] = a[i] == b[j] ? dp[(i + 1) * w + j + 1] + 1 : max(dp[(i + 1) * w + j], dp[i * w + j + 1])
                }
            }
        }
        var i = 0, j = 0
        while i < n, j < m {
            if a[i] == b[j] {
                out.append(DiffLine(kind: .context, text: a[i], oldLine: oi, newLine: ni))
                oi += 1; ni += 1; i += 1; j += 1
            } else if dp[(i + 1) * w + j] >= dp[i * w + j + 1] {
                del(a[i]); i += 1
            } else {
                add(b[j]); j += 1
            }
        }
        while i < n { del(a[i]); i += 1 }
        while j < m { add(b[j]); j += 1 }
        return out
    }

    /// The changes with `context` unchanged lines around each, as hunks (what
    /// a phone shows; `lines` is the whole file).
    public static func hunks(old: String?, new: String?, context: Int = 3) -> [DiffHunk] {
        group(lines(old: old, new: new), context: context)
    }

    /// Groups aligned lines into hunks around the changes.
    public static func group(_ all: [DiffLine], context: Int) -> [DiffHunk] {
        let changed = all.indices.filter { all[$0].kind == .added || all[$0].kind == .removed }
        guard !changed.isEmpty else { return [] }
        var ranges: [ClosedRange<Int>] = []
        for k in changed {
            let r = max(0, k - context)...min(all.count - 1, k + context)
            if let last = ranges.last, r.lowerBound <= last.upperBound + 1 {
                ranges[ranges.count - 1] = last.lowerBound...max(last.upperBound, r.upperBound)
            } else {
                ranges.append(r)
            }
        }
        return ranges.map { r in
            let ls = Array(all[r])
            // a hunk's start: its first line's number on each side, else the line before it
            var oldStart = ls.first(where: { $0.oldLine != nil })?.oldLine
            var newStart = ls.first(where: { $0.newLine != nil })?.newLine
            if oldStart == nil { oldStart = (all[..<r.lowerBound].last(where: { $0.oldLine != nil })?.oldLine ?? 0) }
            if newStart == nil { newStart = (all[..<r.lowerBound].last(where: { $0.newLine != nil })?.newLine ?? 0) }
            return DiffHunk(oldStart: oldStart!, oldCount: ls.filter { $0.oldLine != nil }.count,
                            newStart: newStart!, newCount: ls.filter { $0.newLine != nil }.count, section: "", lines: ls)
        }
    }

    /// A git-style unified diff of old → new: the web's unifiedDiff, byte for
    /// byte (one hunk spanning the whole file).
    public static func unified(path: String, old: String?, new: String?) -> String {
        let p = path.isEmpty ? "file" : path
        if (old ?? "") == (new ?? "") { return "diff --git a/\(p) b/\(p)\n" }
        let ls = lines(old: old, new: new)
        let n = splitLines(old ?? "").count
        let m = splitLines(new ?? "").count
        let body = ls.map { l -> String in
            switch l.kind {
            case .added: return "+" + l.text
            case .removed: return "-" + l.text
            default: return " " + l.text
            }
        }
        return "diff --git a/\(p) b/\(p)\n--- a/\(p)\n+++ b/\(p)\n@@ -1,\(n) +1,\(m) @@\n" + body.joined(separator: "\n") + "\n"
    }

    /// +added/-removed and distinct files of a unified diff (bx-code's diffStats).
    public static func stats(_ diff: String) -> (added: Int, removed: Int, files: Int) {
        var add = 0, del = 0
        var files = Set<String>()
        for line in splitLines(diff) {
            if line.hasPrefix("diff --git") {
                if let r = line.range(of: " b/") {
                    let rest = line[r.upperBound...]
                    files.insert(String(rest.prefix { !$0.isWhitespace }))
                }
                continue
            }
            if line.hasPrefix("+++") || line.hasPrefix("---") { continue }
            if line.hasPrefix("+") { add += 1 } else if line.hasPrefix("-") { del += 1 }
        }
        return (add, del, files.count)
    }
}

public enum GitPatch {
    /// Parses a git patch (files.changed `patch.text`) into files and hunks
    /// with line numbers. A truncated patch parses up to where it stops.
    public static func parse(_ text: String) -> [FileDiff] {
        var files: [FileDiff] = []
        var cur: FileDiff?
        var hunk: DiffHunk?
        var oi = 0, ni = 0
        func closeHunk() {
            if let h = hunk { cur?.hunks.append(h) }
            hunk = nil
        }
        func closeFile() {
            closeHunk()
            if let f = cur { files.append(f) }
            cur = nil
        }
        for line in splitLines(text) {
            if line.hasPrefix("diff --git ") {
                closeFile()
                let (a, b) = gitPaths(String(line.dropFirst(11)))
                cur = FileDiff(path: b, oldPath: a, status: .modified, binary: false, hunks: [])
                continue
            }
            guard cur != nil else { continue }
            // a finished hunk takes only its trailing "\ No newline" notes
            if let h = hunk, hunkDone(h), !line.hasPrefix("\\") { closeHunk() }
            if hunk != nil, line.hasPrefix(" ") || line.hasPrefix("+") || line.hasPrefix("-") || line.hasPrefix("\\") {
                let body = String(line.dropFirst())
                switch line.first {
                case "+": hunk!.lines.append(DiffLine(kind: .added, text: body, newLine: ni)); ni += 1
                case "-": hunk!.lines.append(DiffLine(kind: .removed, text: body, oldLine: oi)); oi += 1
                case "\\": hunk!.lines.append(DiffLine(kind: .note, text: line))
                default: hunk!.lines.append(DiffLine(kind: .context, text: body, oldLine: oi, newLine: ni)); oi += 1; ni += 1
                }
                continue
            }
            if line.hasPrefix("@@") {
                closeHunk()
                if let h = hunkHeader(line) {
                    hunk = h
                    oi = h.oldStart
                    ni = h.newStart
                }
                continue
            }
            if line.hasPrefix("new file") { cur!.status = .added }
            else if line.hasPrefix("deleted file") { cur!.status = .deleted }
            else if line.hasPrefix("rename from ") { cur!.status = .renamed; cur!.oldPath = String(line.dropFirst(12)) }
            else if line.hasPrefix("rename to ") { cur!.status = .renamed; cur!.path = String(line.dropFirst(10)) }
            else if line.hasPrefix("old mode") || line.hasPrefix("new mode") { if cur!.status == .modified, cur!.hunks.isEmpty { cur!.status = .typechange } }
            else if line.hasPrefix("Binary files") || line.hasPrefix("GIT binary patch") { cur!.binary = true }
            else if line.hasPrefix("--- ") { let p = String(line.dropFirst(4)); if p != "/dev/null" { cur!.oldPath = strip(p, "a/") } }
            else if line.hasPrefix("+++ ") { let p = String(line.dropFirst(4)); if p != "/dev/null" { cur!.path = strip(p, "b/") } }
        }
        closeFile()
        return files
    }

    static func hunkDone(_ h: DiffHunk) -> Bool {
        let old = h.lines.filter { $0.kind == .context || $0.kind == .removed }.count
        let new = h.lines.filter { $0.kind == .context || $0.kind == .added }.count
        return old >= h.oldCount && new >= h.newCount
    }

    static func strip(_ p: String, _ prefix: String) -> String { p.hasPrefix(prefix) ? String(p.dropFirst(prefix.count)) : p }

    /// "a/x b/y" (git quotes paths with spaces only when they need it; this
    /// reads the unquoted form and splits on " b/").
    static func gitPaths(_ s: String) -> (String, String) {
        if let r = s.range(of: " b/", options: .backwards), s.hasPrefix("a/") {
            return (String(s[s.index(s.startIndex, offsetBy: 2)..<r.lowerBound]), String(s[r.upperBound...]))
        }
        return (s, s)
    }

    /// "@@ -a,b +c,d @@ section"
    static func hunkHeader(_ line: String) -> DiffHunk? {
        let parts = line.split(separator: " ", maxSplits: 4, omittingEmptySubsequences: true)
        guard parts.count >= 4, parts[0] == "@@", parts[3] == "@@" || parts[3].hasPrefix("@@") else { return nil }
        func range(_ s: Substring) -> (Int, Int)? {
            let t = s.dropFirst()
            let nums = t.split(separator: ",")
            guard let a = Int(nums.first ?? "") else { return nil }
            return (a, nums.count > 1 ? Int(nums[1]) ?? 1 : 1)
        }
        guard let o = range(parts[1]), let n = range(parts[2]) else { return nil }
        let section = parts.count > 4 ? String(parts[4]) : ""
        return DiffHunk(oldStart: o.0, oldCount: o.1, newStart: n.0, newCount: n.1, section: section, lines: [])
    }
}
