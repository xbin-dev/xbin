// TermFind.swift — scrollback search's state (plans/native.md §12
// "scrollback search"): the find bar's query and options, what the last
// search found ("3 of 14"), and its keys. The searching itself is
// SwiftTerm's (TerminalView.findNext/findPrevious/searchMatchSummary, since
// 1.20: xterm.js's search addon over the whole buffer, scrollback included;
// the match is shown as the selection and scrolled into view); this is the
// part the app would otherwise decide in a view.

import Foundation

/// The find bar's options; SwiftTerm's `SearchOptions` field for field.
public struct TermFindOptions: Equatable, Sendable, Codable {
    public var caseSensitive = false
    public var regex = false
    public var wholeWord = false
    public init(caseSensitive: Bool = false, regex: Bool = false, wholeWord: Bool = false) {
        self.caseSensitive = caseSensitive; self.regex = regex; self.wholeWord = wholeWord
    }
}

/// What a key in the find bar does.
public enum TermFindAction: Equatable, Sendable { case next, previous, close }

public struct TermFind: Equatable, Sendable {
    /// Counting stops here (SwiftTerm's `searchMatchSummary` limit): "1000+".
    public static let countLimit = 1000

    public var visible = false
    public var query = "" {
        didSet { if query != oldValue { index = 0; total = 0; searched = nil } }
    }
    public var options = TermFindOptions() {
        didSet { if options != oldValue { index = 0; total = 0; searched = nil } }
    }
    /// The current match, 1-based (0: none).
    public private(set) var index = 0
    /// Matches in the buffer (at most `countLimit`).
    public private(set) var total = 0
    /// The query the counts are for (nil: not searched since it changed).
    public private(set) var searched: String?

    public init() {}

    /// The bar opens; the last query stays, for ⌘G.
    public mutating func open() { visible = true }

    /// The bar closes; the highlight goes (the app clears SwiftTerm's search).
    public mutating func close() {
        visible = false
        index = 0; total = 0; searched = nil
    }

    /// A regular expression that doesn't compile (shown instead of counts;
    /// nothing is searched).
    public var problem: String? {
        guard options.regex, !query.isEmpty else { return nil }
        let opts: NSRegularExpression.Options = options.caseSensitive ? [] : [.caseInsensitive]
        return (try? NSRegularExpression(pattern: query, options: opts)) == nil ? "Invalid pattern" : nil
    }

    public var canSearch: Bool { !query.isEmpty && problem == nil }

    /// What a search reported: whether it found a match, and SwiftTerm's
    /// summary (the match's 1-based position, the capped count).
    public mutating func record(found: Bool, index: Int, total: Int) {
        self.total = max(0, total)
        self.index = found ? min(max(0, index), self.total) : 0
        searched = query
    }

    /// The bar's counter: "" before a search, "No matches", "3 of 14",
    /// "3 of 1000+" when counting stopped, "14 matches" when the current one
    /// wasn't among the first `countLimit`.
    public var status: String {
        if let p = problem { return p }
        guard searched != nil, !query.isEmpty else { return "" }
        if total == 0 { return "No matches" }
        let count = total >= Self.countLimit ? "\(Self.countLimit)+" : "\(total)"
        if index == 0 { return total == 1 ? "1 match" : "\(count) matches" }
        return "\(index) of \(count)"
    }

    /// Keys in the find field: Return → next, ⇧Return → previous, Escape →
    /// close, ⌘G / ⌘⇧G → next / previous. nil: the field's own.
    public static func action(key: TermFindKey, shift: Bool, command: Bool) -> TermFindAction? {
        switch key {
        case .returnKey: return command ? nil : (shift ? .previous : .next)
        case .escape: return .close
        case .g: return command ? (shift ? .previous : .next) : nil
        }
    }
}

public enum TermFindKey: Equatable, Sendable { case returnKey, escape, g }
