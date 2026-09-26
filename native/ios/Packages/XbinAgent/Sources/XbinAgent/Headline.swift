import Foundation

// What a tool card is called (D77) — a port of web/agent-tools.js's
// commandOf/headline/describeCommand, checked against the same cases as
// hack/agent-tools.test.mjs. The harness's own description comes first
// (label, rawInput.description, gemini's pending text), else a deterministic
// reading of the shell command (a heredoc → "Python script (6 lines) →
// main.go"; sed -i, cat >, tee, > name the file they write; rm/mkdir/mv say
// so), else the title's first line.

public enum ToolReading {
    /// The command of a shell call: rawInput.command (a string, or argv), else
    /// .cmd/.script; an execute call's title is the command otherwise.
    public static func command(rawInput: JSONValue?, kind: ToolKind?, title: String) -> String {
        if let r = rawInput, r.object != nil {
            let c = nonNull(r["command"]) ?? nonNull(r["cmd"]) ?? nonNull(r["script"])
            if let s = c?.string {
                if let args = r["args"]?.array { return s + " " + args.map { $0.text ?? "" }.joined(separator: " ") }
                return s
            }
            if let a = c?.array { return a.map { $0.text ?? "" }.joined(separator: " ") }
        }
        return kind == .execute ? title : ""
    }

    /// The first text content item that is not blank (a tool's prose: gemini's
    /// description, a plan).
    public static func contentText(_ content: [ToolContent]) -> String {
        for it in content {
            if case .text(let s) = it, !trim(s).isEmpty { return s }
        }
        return ""
    }

    /// A card's headline (see the file comment for the order).
    public static func headline(label: String, rawInput: JSONValue?, kind: ToolKind?, status: ToolStatus?, content: [ToolContent],
                                title: String, name: String, id: String) -> String
    {
        if !label.isEmpty { return label }
        if let d = rawInput?["description"]?.string, !d.isEmpty { return d }
        let cmd = command(rawInput: rawInput, kind: kind, title: title)
        if !cmd.isEmpty {
            if kind == .execute, status?.isRunning == true {
                let c = contentText(content) // gemini: "[in dir] (description)" until the output replaces it
                if !c.isEmpty, !hasNewline(c), c.utf16.count < 160 { return c }
            }
            return describeCommand(cmd)
        }
        let fl = firstLine(title)
        if !fl.isEmpty { return fl }
        if !name.isEmpty { return name }
        if !id.isEmpty { return id }
        return "a tool call"
    }

    /// The first line of a text, trimmed, with " …" when more lines follow.
    public static func firstLine(_ s: String) -> String {
        let t = trim(s)
        guard let nl = t.unicodeScalars.firstIndex(of: "\n") else { return t }
        return String(t.unicodeScalars[..<nl]) + " …"
    }

    /// Reads a shell command deterministically (the web's describeCommand).
    public static func describeCommand(_ cmd: String) -> String {
        var c = trim(cmd)
        if let m = try? RX.cd.firstMatch(in: c) { c = String(c[m.range.upperBound...]) }
        if let m = try? RX.heredoc.firstMatch(in: c) {
            let key = group(m, 2) ?? ""
            let lang = langName[key] ?? key
            let body = group(m, 5) ?? ""
            let n = splitLines(body).count
            return "\(lang) script (\(n) line\(n == 1 ? "" : "s"))\(targets(scriptTargets(body)))"
        }
        if let m = try? RX.catWrite.firstMatch(in: c) { return "Write \(group(m, 2) ?? "")" }
        if let m = try? RX.inPlace.firstMatch(in: c), (try? RX.inPlaceFlag.firstMatch(in: c)) != nil {
            return "Edit \(group(m, 3) ?? "") (\(group(m, 1) ?? ""))"
        }
        if let m = try? RX.tee.firstMatch(in: c) { return "Write \(group(m, 2) ?? "")" }
        if let m = try? RX.redirect.firstMatch(in: c), !hasNewline(c), group(m, 2) != "/dev/null" {
            return "Write \(group(m, 2) ?? "")"
        }
        if !hasNewline(c) {
            if let m = try? RX.rm.firstMatch(in: c) { return "Remove \(group(m, 1) ?? "")" }
            if let m = try? RX.mkdir.firstMatch(in: c) { return "Create \(group(m, 1) ?? "")" }
        }
        if let m = try? RX.mv.firstMatch(in: c) { return "Move \(group(m, 1) ?? "") → \(group(m, 2) ?? "")" }
        return firstLine(c)
    }

    /// The files a script body names (open('x'), Path('x'), p='x', read/writeFileSync…).
    public static func scriptTargets(_ body: String) -> [String] {
        var out: [String] = []
        for m in body.matches(of: RX.scriptTarget) {
            guard let f = group(m, 2) ?? group(m, 4) else { continue }
            if !f.isEmpty, f.contains(".") || f.contains("/"), !out.contains(f) { out.append(f) }
        }
        return out
    }

    /// A tool's raw input for display: a shell command verbatim, else JSON
    /// (indented by one, as the web shows it).
    public static func rawText(_ raw: JSONValue?) -> String {
        guard let raw, !raw.isNull else { return "" }
        if let s = raw.string { return s }
        if raw.object != nil {
            let c = nonNull(raw["command"]) ?? nonNull(raw["cmd"]) ?? nonNull(raw["script"])
            if let s = c?.string {
                if let args = raw["args"]?.array { return s + " " + args.map { $0.text ?? "" }.joined(separator: " ") }
                return s
            }
        }
        if raw.object != nil || raw.array != nil { return raw.prettyString(indent: 1) }
        return raw.text ?? ""
    }

    /// A plan approval: a mode switch (Claude's ExitPlanMode, Codex's
    /// "Implement this plan?"), Codex's planReview, or a request carrying a plan.
    public static func isPlanApproval(kind: String, planReview: Bool, rawInput: JSONValue?) -> Bool {
        kind == "switch_mode" || planReview || rawInput?["plan"]?.string != nil
    }

    /// The plan's markdown: Claude sends it as text content, Codex only in rawInput.plan.
    public static func planText(content: [ToolContent], rawInput: JSONValue?) -> String {
        let c = contentText(content)
        return c.isEmpty ? (rawInput?["plan"]?.string ?? "") : c
    }

    // MARK: -

    static let langName = ["python": "Python", "python3": "Python", "node": "Node", "deno": "Deno", "bun": "Bun", "ruby": "Ruby",
                           "perl": "Perl", "php": "PHP", "bash": "Shell", "sh": "Shell", "zsh": "Shell"]

    static func targets(_ fs: [String]) -> String {
        if fs.isEmpty { return "" }
        return " → " + fs.prefix(2).joined(separator: ", ") + (fs.count > 2 ? " +\(fs.count - 2)" : "")
    }

    static func nonNull(_ v: JSONValue?) -> JSONValue? { v.flatMap { $0.isNull ? nil : $0 } }
}

/// Splits on "\n" as JavaScript does ("\r\n" is one Swift Character, but two
/// characters on the wire: the "\r" stays on its line).
func splitLines(_ s: String) -> [String] {
    s.unicodeScalars.split(separator: "\n", omittingEmptySubsequences: false).map { String(Substring($0)) }
}

func hasNewline(_ s: String) -> Bool { s.unicodeScalars.contains("\n") }

/// JavaScript's String.prototype.trim (Unicode whitespace and line terminators).
func trim(_ s: String) -> String { s.trimmingCharacters(in: .whitespacesAndNewlines) }

func group(_ m: Regex<AnyRegexOutput>.Match, _ i: Int) -> String? {
    guard i < m.output.count, let s = m.output[i].substring else { return nil }
    return String(s)
}

/// The web's regular expressions, compiled once, with JavaScript's semantics
/// where Swift's defaults differ: matching per Unicode scalar (so "\r\n" is
/// two characters, as in JS), ASCII \w and simple \b.
enum RX {
    static func make(_ pattern: String, multiline: Bool = false) -> Regex<AnyRegexOutput> {
        // the patterns are literals in this file; a typo fails every test
        var r = try! Regex(pattern).matchingSemantics(.unicodeScalar).asciiOnlyWordCharacters().wordBoundaryKind(.simple)
        if multiline { r = r.anchorsMatchLineEndings() }
        return r
    }

    nonisolated(unsafe) static let cd = make(#"^cd\s+(\S+)\s*&&\s*"#)
    nonisolated(unsafe) static let heredoc = make(
        #"^([\w./-]*?(python3?|node|deno|bun|ruby|perl|php|bash|sh|zsh))\b[^\n]*?<<-?\s*(['"]?)(\w+)\3[^\n]*\n([\s\S]*?)\n\4\s*$"#)
    nonisolated(unsafe) static let catWrite = make(#"^cat\s+>>?\s*(['"]?)([^\s'"<>|;&]+)\1\s*<<"#)
    nonisolated(unsafe) static let inPlace = make(#"^(sed|perl)\b[^\n|;&]*\s-[a-zA-Z]*[ip][a-zA-Z]*\b[^\n|;&]*?\s(['"]?)([^\s'"|;&]+)\2\s*$"#)
    nonisolated(unsafe) static let inPlaceFlag = make(#"\s-[a-zA-Z]*i"#)
    nonisolated(unsafe) static let tee = make(#"\|\s*tee\s+(?:-a\s+)?(['"]?)([^\s'"|;&]+)\1\s*$"#)
    nonisolated(unsafe) static let redirect = make(#"^[^\n]*?[^2&0-9]>>?\s*(['"]?)([^\s'"|;&<>]+)\1\s*$"#)
    nonisolated(unsafe) static let rm = make(#"^rm\s+(?:-\w+\s+)*(.+)$"#)
    nonisolated(unsafe) static let mkdir = make(#"^mkdir\s+(?:-\w+\s+)*(.+)$"#)
    nonisolated(unsafe) static let mv = make(#"^mv\s+(?:-\w+\s+)*(\S+)\s+(\S+)$"#)
    nonisolated(unsafe) static let scriptTarget = make(
        #"(?:open|Path|readFileSync|writeFileSync|readFile|writeFile|read_text|write_text)\(\s*(['"])([^'"\n]+)\1|(?:^|[\s;(])(?:p|path|fn|file|filename|target)\s*=\s*(['"])([^'"\n]+)\3"#,
        multiline: true)
    nonisolated(unsafe) static let auth = make(#"(?i)sign[\s-]?in|authenticat|not logged in|-32000"#)
}
