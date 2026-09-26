// AccessorySlot.swift — the accessory row's customizable slot (plans/native.md
// §12): what can go there, how the choice is stored, and its key cap. The
// app keeps it in UserDefaults under `defaultsKey` as the JSON of an
// `AccessoryKey` (the format the first app build already reads), `null` for
// "no extra key", and treats a missing value as F1.

import Foundation

public enum AccessorySlot {
    public static let defaultsKey = "termCustomKey"
    /// What an untouched install shows.
    public static let defaultKey: AccessoryKey = .key(.function(1))
    /// The longest snippet, in characters (the Return toggle not counted).
    public static let snippetLimit = 32

    // MARK: Storage

    /// The stored choice: nil data (never set) → the default; `null` or
    /// anything unreadable → no extra key.
    public static func decode(_ data: Data?) -> AccessoryKey? {
        guard let data else { return defaultKey }
        return try? JSONDecoder().decode(AccessoryKey.self, from: data)
    }

    /// What to store for a choice (nil: no extra key).
    public static func encode(_ key: AccessoryKey?) -> Data {
        guard let key, let d = try? JSONEncoder().encode(key) else { return Data("null".utf8) }
        return d
    }

    // MARK: The choices

    public struct Group: Equatable, Sendable, Identifiable {
        public var id: String { title }
        public var title: String
        public var keys: [AccessoryKey]
    }

    /// Everything the slot offers, grouped; none of it is on the row already
    /// (esc, ctrl, tab, the arrows, `| ~ / -`).
    public static let groups: [Group] = [
        Group(title: "Keys", keys: [
            .modifier(.alt), .key(.home), .key(.end), .key(.pageUp), .key(.pageDown),
            .key(.delete), .key(.insert), .key(.backtab), .key(.enter), .key(.backspace),
        ]),
        Group(title: "Function keys", keys: (1...12).map { .key(.function($0)) }),
        Group(title: "Characters", keys: ["`", "\\", "^", "$", "&", "*", "{", "}", "[", "]", "(", ")",
                                          "<", ">", ";", ":", "'", "\"", "#", "!", "%", "=", "_", "@"].map { .text($0) }),
    ]

    /// Whether `key` is one of the listed choices (else it is a snippet).
    public static func isListed(_ key: AccessoryKey) -> Bool {
        groups.contains { $0.keys.contains(key) }
    }

    public enum SnippetProblem: Error, Equatable, Sendable {
        case empty, tooLong, controlCharacters
        public var message: String {
            switch self {
            case .empty: return "Type what the key should send."
            case .tooLong: return "At most \(AccessorySlot.snippetLimit) characters."
            case .controlCharacters: return "Tabs, newlines and control characters can't go in a snippet — use the Return toggle for a newline."
            }
        }
    }

    /// A snippet key: the text as typed (spaces kept: "sudo "), plus a
    /// newline when it should press Return (the terminal gets CR).
    public static func snippet(_ text: String, pressReturn: Bool) -> Result<AccessoryKey, SnippetProblem> {
        if text.isEmpty { return .failure(.empty) }
        if text.count > snippetLimit { return .failure(.tooLong) }
        if text.unicodeScalars.contains(where: { $0.properties.generalCategory == .control || $0 == "\u{7f}" }) {
            return .failure(.controlCharacters)
        }
        return .success(.text(pressReturn ? text + "\n" : text))
    }

    /// A snippet key's parts (nil: not a snippet).
    public static func snippetParts(_ key: AccessoryKey) -> (text: String, pressReturn: Bool)? {
        guard case .text(let s) = key, !isListed(key) else { return nil }
        if s.hasSuffix("\n") { return (String(s.dropLast()), true) }
        return (s, false)
    }

    // MARK: Labels

    /// The key cap: short enough for the row. A snippet shows its text
    /// without edge spaces ("␣" when that leaves nothing), "⏎" for Return,
    /// cut to 6 characters with "…".
    public static func capLabel(_ key: AccessoryKey) -> String {
        guard let parts = snippetParts(key) else { return key.label }
        let ret = parts.pressReturn
        var t = parts.text.trimmingCharacters(in: .whitespaces)
        if t.isEmpty { t = "␣" }
        let budget = ret ? 5 : 6
        if t.count > budget { t = String(t.prefix(budget - 1)) + "…" }
        return ret ? t + "⏎" : t
    }

    /// A sentence for the settings list and VoiceOver.
    public static func describe(_ key: AccessoryKey) -> String {
        if let p = snippetParts(key) {
            return p.pressReturn ? "Types “\(p.text)” and presses Return" : "Types “\(p.text)”"
        }
        switch key {
        case .modifier(.alt): return "Alt (Meta), sticky like ctrl"
        case .modifier(.control): return "Control, sticky"
        case .key(let k):
            switch k {
            case .escape: return "Escape"
            case .tab: return "Tab"
            case .backtab: return "Shift-Tab"
            case .enter: return "Return"
            case .backspace: return "Delete backward"
            case .up: return "Up arrow"
            case .down: return "Down arrow"
            case .left: return "Left arrow"
            case .right: return "Right arrow"
            case .home: return "Home"
            case .end: return "End"
            case .pageUp: return "Page Up"
            case .pageDown: return "Page Down"
            case .insert: return "Insert"
            case .delete: return "Delete forward"
            case .function(let n): return "Function key F\(n)"
            }
        case .text(let s): return "Types “\(s)”"
        }
    }
}
