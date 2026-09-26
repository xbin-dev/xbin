// Keyboard.swift — what the terminal sends for a key (plans/native.md §12
// "Input made for a phone"): the accessory row (esc, a sticky ctrl, tab,
// arrows, | ~ / -, a customizable slot), the software keyboard's text, and a
// hardware keyboard's keys and ⌘ shortcuts. Byte sequences follow xterm (what
// xterm.js sends in the browser), including DECCKM: in application cursor
// mode unmodified arrows, Home and End are SS3 (ESC O x) instead of CSI.

import Foundation

/// A non-text key.
public enum TermKey: Hashable, Sendable, Codable {
    case escape, tab, backtab, enter, backspace
    case up, down, left, right, home, end, pageUp, pageDown, insert, delete
    /// F1–F12.
    case function(Int)
}

/// xterm's modifier set; its parameter is 1 + shift·1 + alt·2 + control·4.
public struct TermModifiers: OptionSet, Hashable, Sendable {
    public let rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }
    public static let shift = TermModifiers(rawValue: 1)
    public static let alt = TermModifiers(rawValue: 2)
    public static let control = TermModifiers(rawValue: 4)
    /// The CSI modifier parameter (1 = none).
    public var xtermParam: Int { 1 + rawValue }
}

public enum TermKeyEncoder {
    private static let ESC: UInt8 = 0x1b

    /// The bytes for a key. `applicationCursor`: the emulator's DECCKM mode.
    public static func encode(_ key: TermKey, _ mods: TermModifiers = [], applicationCursor: Bool = false) -> [UInt8] {
        let m = mods.xtermParam
        func csi(_ s: String) -> [UInt8] { [ESC, 0x5b] + Array(s.utf8) }
        func ss3(_ c: Character) -> [UInt8] { [ESC, 0x4f, c.asciiValue!] }
        /// A cursor-style key: SS3 x in application mode, CSI x otherwise, CSI 1;m x modified.
        func cursor(_ c: Character) -> [UInt8] {
            if mods.isEmpty { return applicationCursor ? ss3(c) : csi(String(c)) }
            return csi("1;\(m)\(c)")
        }
        /// A tilde key: CSI n ~, CSI n;m ~ modified.
        func tilde(_ n: Int) -> [UInt8] { mods.isEmpty ? csi("\(n)~") : csi("\(n);\(m)~") }
        let alt = mods.contains(.alt)
        switch key {
        case .up: return cursor("A")
        case .down: return cursor("B")
        case .right: return cursor("C")
        case .left: return cursor("D")
        case .home: return cursor("H")
        case .end: return cursor("F")
        case .insert: return tilde(2)
        case .delete: return tilde(3)
        case .pageUp: return tilde(5)
        case .pageDown: return tilde(6)
        case .function(let n):
            switch n {
            case 1...4:
                let c: Character = ["P", "Q", "R", "S"][n - 1]
                return mods.isEmpty ? ss3(c) : csi("1;\(m)\(c)")
            case 5...12: return tilde([15, 17, 18, 19, 20, 21, 23, 24][n - 5])
            default: return []
            }
        case .escape: return alt ? [ESC, ESC] : [ESC]
        case .tab:
            if mods.contains(.shift) { return csi("Z") }
            return alt ? [ESC, 0x09] : [0x09]
        case .backtab: return csi("Z")
        case .enter: return alt ? [ESC, 0x0d] : [0x0d]
        case .backspace:
            let b: UInt8 = mods.contains(.control) ? 0x08 : 0x7f
            return alt ? [ESC, b] : [b]
        }
    }

    /// Text typed with modifiers: control maps one character to its control
    /// code (ctrl-a → 0x01, ctrl-[ → ESC, ctrl-/ → 0x1f, ctrl-? → DEL…; a
    /// character with none is sent as is), alt prefixes ESC. Longer text
    /// (dictation, a snippet) is sent as is. Newlines become CR, as a
    /// terminal's Enter and xterm's paste send them.
    public static func encode(text: String, _ mods: TermModifiers = []) -> [UInt8] {
        let scalars = Array(text.unicodeScalars)
        if scalars.count == 1, !mods.intersection([.control, .alt]).isEmpty {
            var out: [UInt8]
            if mods.contains(.control), let c = controlByte(scalars[0]) { out = [c] }
            else { out = crlf(text) }
            if mods.contains(.alt) { out.insert(ESC, at: 0) }
            return out
        }
        return crlf(text)
    }

    /// The control code for ctrl + this character (nil: none).
    public static func controlByte(_ s: Unicode.Scalar) -> UInt8? {
        switch s {
        case "a"..."z": return UInt8(s.value - 0x60)
        case "A"..."Z": return UInt8(s.value - 0x40)
        case "@", " ", "2", "`": return 0x00
        case "[", "3": return 0x1b
        case "\\", "4", "|": return 0x1c
        case "]", "5": return 0x1d
        case "^", "6", "~": return 0x1e
        case "_", "7", "/", "-": return 0x1f
        case "8", "?": return 0x7f
        default: return nil
        }
    }

    /// "\r\n" and "\n" → "\r".
    static func crlf(_ s: String) -> [UInt8] {
        var out: [UInt8] = []
        out.reserveCapacity(s.utf8.count)
        var prevCR = false
        for b in s.utf8 {
            if b == 0x0a { if !prevCR { out.append(0x0d) } }
            else { out.append(b) }
            prevCR = b == 0x0d
        }
        return out
    }
}

// MARK: - Sticky modifiers (the accessory row's ctrl and alt)

public enum StickyModifier: String, Hashable, Sendable, Codable, CaseIterable {
    case control, alt
    var mod: TermModifiers { self == .control ? .control : .alt }
}

/// A tap arms a modifier for the next key; a second tap locks it; a third
/// releases it.
public struct StickyModifiers: Equatable, Sendable {
    public enum State: Equatable, Sendable { case off, once, locked }
    public private(set) var control = State.off
    public private(set) var alt = State.off
    public init() {}

    public func state(_ m: StickyModifier) -> State { m == .control ? control : alt }

    public mutating func tap(_ m: StickyModifier) {
        let next: State
        switch state(m) { case .off: next = .once; case .once: next = .locked; case .locked: next = .off }
        if m == .control { control = next } else { alt = next }
    }

    public mutating func release() { control = .off; alt = .off }

    /// The modifiers on now.
    public var active: TermModifiers {
        var m: TermModifiers = []
        if control != .off { m.insert(.control) }
        if alt != .off { m.insert(.alt) }
        return m
    }

    /// The modifiers for the key being sent; one-shot ones are used up.
    public mutating func consume() -> TermModifiers {
        let m = active
        if control == .once { control = .off }
        if alt == .once { alt = .off }
        return m
    }
}

// MARK: - The accessory row

public enum AccessoryKey: Hashable, Sendable, Codable {
    case modifier(StickyModifier)
    case key(TermKey)
    /// A character or a snippet ("|", "~", "sudo ").
    case text(String)

    /// The row as designed (plans/native.md §12): esc, ctrl, tab, arrows,
    /// | ~ / -. The app appends the user's customizable slot.
    public static let defaultRow: [AccessoryKey] = [
        .key(.escape), .modifier(.control), .key(.tab),
        .key(.left), .key(.down), .key(.up), .key(.right),
        .text("|"), .text("~"), .text("/"), .text("-"),
    ]

    /// A short label for the key cap.
    public var label: String {
        switch self {
        case .modifier(.control): return "ctrl"
        case .modifier(.alt): return "alt"
        case .key(let k):
            switch k {
            case .escape: return "esc"
            case .tab: return "tab"
            case .backtab: return "⇤"
            case .enter: return "⏎"
            case .backspace: return "⌫"
            case .up: return "↑"
            case .down: return "↓"
            case .left: return "←"
            case .right: return "→"
            case .home: return "home"
            case .end: return "end"
            case .pageUp: return "pgup"
            case .pageDown: return "pgdn"
            case .insert: return "ins"
            case .delete: return "del"
            case .function(let n): return "F\(n)"
            }
        case .text(let s): return s
        }
    }

    /// Arrow keys repeat while held or swiped along the row.
    public var repeats: Bool {
        if case .key(let k) = self { return [.up, .down, .left, .right, .backspace].contains(k) }
        return false
    }
}

// MARK: - Hardware keyboards

/// USB HID usage IDs (page 7) — UIKeyboardHIDUsage's raw values.
public enum HIDUsage {
    public static let a: UInt16 = 0x04, k: UInt16 = 0x0e, t: UInt16 = 0x17, c: UInt16 = 0x06, v: UInt16 = 0x19, f: UInt16 = 0x09
    public static let digit0: UInt16 = 0x27
    public static let returnOrEnter: UInt16 = 0x28, escape: UInt16 = 0x29, deleteOrBackspace: UInt16 = 0x2a
    public static let tab: UInt16 = 0x2b, spacebar: UInt16 = 0x2c, hyphen: UInt16 = 0x2d, equalSign: UInt16 = 0x2e
    public static let openBracket: UInt16 = 0x2f, closeBracket: UInt16 = 0x30
    public static let f1: UInt16 = 0x3a, f12: UInt16 = 0x45
    public static let insert: UInt16 = 0x49, home: UInt16 = 0x4a, pageUp: UInt16 = 0x4b, deleteForward: UInt16 = 0x4c
    public static let end: UInt16 = 0x4d, pageDown: UInt16 = 0x4e
    public static let rightArrow: UInt16 = 0x4f, leftArrow: UInt16 = 0x50, downArrow: UInt16 = 0x51, upArrow: UInt16 = 0x52
    public static let keypadEnter: UInt16 = 0x58
}

/// A hardware key press, platform-neutral (UIKey's fields).
public struct HardwareKeyEvent: Equatable, Sendable {
    public struct Modifiers: OptionSet, Hashable, Sendable {
        public let rawValue: Int
        public init(rawValue: Int) { self.rawValue = rawValue }
        public static let shift = Modifiers(rawValue: 1)
        public static let control = Modifiers(rawValue: 2)
        public static let option = Modifiers(rawValue: 4)
        public static let command = Modifiers(rawValue: 8)
    }
    /// UIKey.keyCode.rawValue (a HID usage).
    public var usage: UInt16
    /// UIKey.characters: what the layout produced with the modifiers applied.
    public var characters: String
    /// UIKey.charactersIgnoringModifiers.
    public var charactersIgnoringModifiers: String
    public var modifiers: Modifiers

    public init(usage: UInt16, characters: String = "", charactersIgnoringModifiers: String = "", modifiers: Modifiers = []) {
        self.usage = usage; self.characters = characters
        self.charactersIgnoringModifiers = charactersIgnoringModifiers; self.modifiers = modifiers
    }
}

/// The terminal's ⌘ shortcuts.
public enum TermShortcut: Equatable, Sendable {
    /// ⌘K
    case clear
    /// ⌘T
    case newSession
    /// ⌘⇧[
    case previousSession
    /// ⌘⇧]
    case nextSession
    /// ⌘C, ⌘V
    case copy, paste
    /// ⌘+ / ⌘= , ⌘-, ⌘0
    case fontBigger, fontSmaller, fontReset
    /// ⌘F: scrollback search
    case find
}

public enum HardwareKeyResult: Equatable, Sendable {
    /// Consume the press and send these bytes.
    case send([UInt8])
    /// Consume the press and run the shortcut.
    case shortcut(TermShortcut)
    /// Not ours: let the system have it (plain text arrives through the text
    /// input path, where IME, dead keys and compose work; other ⌘ keys stay
    /// the system's).
    case passthrough
}

public struct TermKeyboardSettings: Equatable, Sendable, Codable {
    /// ⌥ as Meta (ESC prefix) instead of the layout's ⌥ characters (é, @ on
    /// many layouts). Off by default: typing those characters must work.
    public var optionAsMeta = false
    /// ⌥← / ⌥→ send ESC b / ESC f (word left/right in shells), as macOS
    /// Terminal does; off sends xterm's CSI 1;3 D/C.
    public var optionArrowsMoveByWord = true
    public init() {}
}

// MARK: - The keyboard model

/// Everything that turns a user's key into bytes, with the sticky modifiers'
/// state. The app keeps one per terminal and passes the emulator's DECCKM
/// (application cursor) mode with each key.
public struct TermKeyboard: Equatable, Sendable {
    public var sticky = StickyModifiers()
    public var settings = TermKeyboardSettings()
    public init(settings: TermKeyboardSettings = TermKeyboardSettings()) { self.settings = settings }

    /// A tap on the accessory row: the bytes to send, or nil for a modifier
    /// (which only changes `sticky`).
    public mutating func accessory(_ k: AccessoryKey, applicationCursor: Bool) -> [UInt8]? {
        switch k {
        case .modifier(let m):
            sticky.tap(m)
            return nil
        case .key(let key):
            return TermKeyEncoder.encode(key, sticky.consume(), applicationCursor: applicationCursor)
        case .text(let s):
            return TermKeyEncoder.encode(text: s, sticky.consume())
        }
    }

    /// Text from the software keyboard (or a hardware key that passed through
    /// to the text system). One armed ctrl/alt applies to a single character.
    public mutating func text(_ s: String) -> [UInt8] {
        TermKeyEncoder.encode(text: s, sticky.consume())
    }

    /// The software keyboard's delete key.
    public mutating func deleteBackward() -> [UInt8] {
        TermKeyEncoder.encode(.backspace, sticky.consume())
    }

    /// A hardware key press.
    public mutating func hardware(_ e: HardwareKeyEvent, applicationCursor: Bool) -> HardwareKeyResult {
        let hm = e.modifiers
        if hm.contains(.command) { return Self.shortcut(e) }
        // special keys, by usage
        if let key = Self.specialKey(e.usage) {
            var mods = sticky.active
            if hm.contains(.shift) { mods.insert(.shift) }
            if hm.contains(.control) { mods.insert(.control) }
            if hm.contains(.option) { mods.insert(.alt) }
            _ = sticky.consume()
            if settings.optionArrowsMoveByWord, mods == [.alt], key == .left || key == .right {
                return .send([0x1b, key == .left ? 0x62 : 0x66])
            }
            return .send(TermKeyEncoder.encode(key, mods, applicationCursor: applicationCursor))
        }
        let ctrl = hm.contains(.control) || sticky.control != .off
        let meta = (hm.contains(.option) && settings.optionAsMeta) || sticky.alt != .off
        guard ctrl || meta else { return .passthrough } // plain text: the text input path sends it
        var base = e.charactersIgnoringModifiers
        if hm.contains(.shift), base.count == 1, base.lowercased() == base { base = base.uppercased() }
        if base.isEmpty { base = e.characters }
        var mods: TermModifiers = []
        if ctrl { mods.insert(.control) }
        if meta { mods.insert(.alt) }
        // ⌥ not as Meta: the layout's ⌥ character is the text (with ctrl on top)
        let text = hm.contains(.option) && !settings.optionAsMeta && !e.characters.isEmpty ? e.characters : base
        _ = sticky.consume()
        return .send(TermKeyEncoder.encode(text: text, mods))
    }

    static func specialKey(_ usage: UInt16) -> TermKey? {
        switch usage {
        case HIDUsage.upArrow: return .up
        case HIDUsage.downArrow: return .down
        case HIDUsage.leftArrow: return .left
        case HIDUsage.rightArrow: return .right
        case HIDUsage.home: return .home
        case HIDUsage.end: return .end
        case HIDUsage.pageUp: return .pageUp
        case HIDUsage.pageDown: return .pageDown
        case HIDUsage.insert: return .insert
        case HIDUsage.deleteForward: return .delete
        case HIDUsage.escape: return .escape
        case HIDUsage.tab: return .tab
        case HIDUsage.returnOrEnter, HIDUsage.keypadEnter: return .enter
        case HIDUsage.deleteOrBackspace: return .backspace
        case HIDUsage.f1...HIDUsage.f12: return .function(Int(usage - HIDUsage.f1) + 1)
        default: return nil
        }
    }

    static func shortcut(_ e: HardwareKeyEvent) -> HardwareKeyResult {
        let shift = e.modifiers.contains(.shift)
        let other = e.modifiers.intersection([.control, .option])
        guard other.isEmpty else { return .passthrough }
        switch e.usage {
        case HIDUsage.k where !shift: return .shortcut(.clear)
        case HIDUsage.t where !shift: return .shortcut(.newSession)
        case HIDUsage.openBracket where shift: return .shortcut(.previousSession)
        case HIDUsage.closeBracket where shift: return .shortcut(.nextSession)
        case HIDUsage.c where !shift: return .shortcut(.copy)
        case HIDUsage.v where !shift: return .shortcut(.paste)
        case HIDUsage.equalSign: return .shortcut(.fontBigger) // ⌘= and ⌘+ (⌘⇧=)
        case HIDUsage.hyphen where !shift: return .shortcut(.fontSmaller)
        case HIDUsage.digit0 where !shift: return .shortcut(.fontReset)
        case HIDUsage.f where !shift: return .shortcut(.find)
        default: return .passthrough
        }
    }
}
