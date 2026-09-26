import Foundation

/// A `diff` node (or an agent's changed files): the file list and an
/// optional unified patch, split into coloured lines.
public struct ChatDiff: Sendable, Equatable {
    public struct File: Sendable, Hashable, Identifiable {
        public var path: String
        public var status: String
        public var added: Int
        public var removed: Int

        public init(path: String, status: String = "modified", added: Int = 0, removed: Int = 0) {
            self.path = path
            self.status = status
            self.added = added
            self.removed = removed
        }

        public var id: String { path }

        /// The status letter the reference renderer shows: A, D, R or M.
        public var letter: String {
            switch status.lowercased() {
            case "added", "add", "a", "new": return "A"
            case "deleted", "delete", "removed", "d": return "D"
            case "renamed", "r": return "R"
            case "modified", "m", "": return "M"
            default: return String(status.prefix(1)).uppercased()
            }
        }
    }

    public struct Line: Sendable, Hashable {
        public enum Kind: Sendable, Hashable { case file, hunk, added, removed, context }
        public var kind: Kind
        public var text: String
    }

    public var files: [File]
    public var lines: [Line]

    public init(files: [File], patch: String? = nil) {
        self.files = files
        lines = ChatDiff.lines(patch ?? "")
    }

    public init(props p: Props) {
        self.init(files: p.objects("files").map {
            File(path: Props.text($0["path"]) ?? "", status: Props.text($0["status"]) ?? "",
                 added: $0["add"]?.intValue.map { Int(clamping: $0) } ?? 0,
                 removed: $0["del"]?.intValue.map { Int(clamping: $0) } ?? 0)
        }, patch: p.string("patch"))
    }

    /// Splits a unified patch (one trailing newline dropped) and classifies
    /// each line as the reference renderer does.
    public static func lines(_ patch: String) -> [Line] {
        guard !patch.isEmpty else { return [] }
        var text = patch
        if text.hasSuffix("\n") { text.removeLast() }
        return text.split(separator: "\n", omittingEmptySubsequences: false).map { raw in
            let l = String(raw)
            let kind: Line.Kind
            if l.hasPrefix("+++") || l.hasPrefix("---") || l.hasPrefix("diff ") || l.hasPrefix("index ") {
                kind = .file
            } else if l.hasPrefix("@@") {
                kind = .hunk
            } else if l.hasPrefix("+") {
                kind = .added
            } else if l.hasPrefix("-") {
                kind = .removed
            } else {
                kind = .context
            }
            return Line(kind: kind, text: l)
        }
    }
}
