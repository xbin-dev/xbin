// KeyboardTests.swift — the keyboard model: xterm's byte sequences (what
// xterm.js sends in the browser), DECCKM, the sticky accessory modifiers and
// hardware-keyboard mappings.

import Foundation
import Testing
@testable import XbinTerm

private func b(_ s: String) -> [UInt8] { Array(s.utf8) }

@Suite("keyboard")
struct KeyboardTests {
    @Test("arrows, Home and End: CSI normally, SS3 in application cursor mode, CSI 1;m when modified")
    func cursorKeys() {
        let E = TermKeyEncoder.self
        #expect(E.encode(.up) == b("\u{1b}[A")); #expect(E.encode(.down) == b("\u{1b}[B"))
        #expect(E.encode(.right) == b("\u{1b}[C")); #expect(E.encode(.left) == b("\u{1b}[D"))
        #expect(E.encode(.home) == b("\u{1b}[H")); #expect(E.encode(.end) == b("\u{1b}[F"))
        #expect(E.encode(.up, applicationCursor: true) == b("\u{1b}OA"))
        #expect(E.encode(.left, applicationCursor: true) == b("\u{1b}OD"))
        #expect(E.encode(.home, applicationCursor: true) == b("\u{1b}OH"))
        #expect(E.encode(.left, [.control]) == b("\u{1b}[1;5D"))
        #expect(E.encode(.left, [.control], applicationCursor: true) == b("\u{1b}[1;5D"), "a modified key ignores DECCKM")
        #expect(E.encode(.right, [.shift]) == b("\u{1b}[1;2C"))
        #expect(E.encode(.up, [.alt]) == b("\u{1b}[1;3A"))
        #expect(E.encode(.down, [.shift, .alt, .control]) == b("\u{1b}[1;8B"))
    }

    @Test("tilde keys and function keys")
    func tildeAndFunction() {
        let E = TermKeyEncoder.self
        #expect(E.encode(.insert) == b("\u{1b}[2~")); #expect(E.encode(.delete) == b("\u{1b}[3~"))
        #expect(E.encode(.pageUp) == b("\u{1b}[5~")); #expect(E.encode(.pageDown) == b("\u{1b}[6~"))
        #expect(E.encode(.delete, [.control]) == b("\u{1b}[3;5~"))
        #expect(E.encode(.function(1)) == b("\u{1b}OP")); #expect(E.encode(.function(4)) == b("\u{1b}OS"))
        #expect(E.encode(.function(1), [.shift]) == b("\u{1b}[1;2P"))
        #expect((5...12).map { E.encode(.function($0)) } == [15, 17, 18, 19, 20, 21, 23, 24].map { b("\u{1b}[\($0)~") })
        #expect(E.encode(.function(5), [.control]) == b("\u{1b}[15;5~"))
        #expect(E.encode(.function(13)) == [])
    }

    @Test("escape, tab, enter, backspace")
    func basics() {
        let E = TermKeyEncoder.self
        #expect(E.encode(.escape) == [0x1b]); #expect(E.encode(.escape, [.alt]) == [0x1b, 0x1b])
        #expect(E.encode(.tab) == [0x09]); #expect(E.encode(.tab, [.shift]) == b("\u{1b}[Z")); #expect(E.encode(.backtab) == b("\u{1b}[Z"))
        #expect(E.encode(.tab, [.alt]) == [0x1b, 0x09])
        #expect(E.encode(.enter) == [0x0d]); #expect(E.encode(.enter, [.alt]) == [0x1b, 0x0d])
        #expect(E.encode(.backspace) == [0x7f]); #expect(E.encode(.backspace, [.control]) == [0x08])
        #expect(E.encode(.backspace, [.alt]) == [0x1b, 0x7f])
    }

    @Test("control codes for characters, xterm's table")
    func controlBytes() {
        let E = TermKeyEncoder.self
        #expect(E.encode(text: "a", [.control]) == [0x01]); #expect(E.encode(text: "Z", [.control]) == [0x1a])
        #expect(E.encode(text: "c", [.control]) == [0x03])
        for (ch, code) in [("@", 0), (" ", 0), ("2", 0), ("`", 0), ("[", 0x1b), ("3", 0x1b), ("\\", 0x1c), ("|", 0x1c), ("4", 0x1c),
                           ("]", 0x1d), ("5", 0x1d), ("^", 0x1e), ("~", 0x1e), ("6", 0x1e), ("_", 0x1f), ("/", 0x1f), ("-", 0x1f),
                           ("7", 0x1f), ("?", 0x7f), ("8", 0x7f)] as [(String, UInt8)] {
            #expect(E.encode(text: ch, [.control]) == [code], "ctrl-\(ch)")
        }
        #expect(E.encode(text: "ł", [.control]) == b("ł"), "no control code: sent as is")
        #expect(E.encode(text: "x", [.alt]) == [0x1b, 0x78])
        #expect(E.encode(text: "d", [.alt, .control]) == [0x1b, 0x04])
        #expect(E.encode(text: "ls -la", [.control]) == b("ls -la"), "a snippet ignores modifiers")
        #expect(E.encode(text: "a\nb\r\nc\rd") == b("a\rb\rc\rd"), "newlines become CR")
        #expect(E.encode(text: "\n") == [0x0d])
    }

    @Test("sticky modifiers: a tap arms for one key, a second locks, a third releases")
    func sticky() {
        var s = StickyModifiers()
        #expect(s.active == [])
        s.tap(.control); #expect(s.control == .once)
        #expect(s.consume() == [.control]); #expect(s.control == .off)
        s.tap(.control); s.tap(.control); #expect(s.control == .locked)
        #expect(s.consume() == [.control]); #expect(s.consume() == [.control], "locked stays")
        s.tap(.control); #expect(s.control == .off)
        s.tap(.alt); s.tap(.control)
        #expect(s.consume() == [.alt, .control]); #expect(s.active == [])
        s.tap(.alt); s.tap(.alt); s.release(); #expect(s.active == [])
    }

    @Test("the accessory row: esc, ctrl combos, tab, arrows under DECCKM, | ~ / -")
    func accessory() {
        var k = TermKeyboard()
        #expect(AccessoryKey.defaultRow.map(\.label) == ["esc", "ctrl", "tab", "←", "↓", "↑", "→", "|", "~", "/", "-"])
        #expect(k.accessory(.key(.escape), applicationCursor: false) == [0x1b])
        #expect(k.accessory(.key(.tab), applicationCursor: false) == [0x09])
        #expect(k.accessory(.key(.up), applicationCursor: false) == b("\u{1b}[A"))
        #expect(k.accessory(.key(.up), applicationCursor: true) == b("\u{1b}OA"))
        for (t, s) in [("|", "|"), ("~", "~"), ("/", "/"), ("-", "-")] {
            #expect(k.accessory(.text(t), applicationCursor: false) == b(s))
        }
        // ctrl (sticky) then a key
        #expect(k.accessory(.modifier(.control), applicationCursor: false) == nil)
        #expect(k.text("c") == [0x03], "ctrl, then c on the software keyboard")
        #expect(k.text("c") == b("c"), "one-shot: used up")
        _ = k.accessory(.modifier(.control), applicationCursor: false)
        #expect(k.accessory(.key(.left), applicationCursor: true) == b("\u{1b}[1;5D"), "ctrl-←")
        _ = k.accessory(.modifier(.control), applicationCursor: false)
        #expect(k.accessory(.text("|"), applicationCursor: false) == [0x1c])
        _ = k.accessory(.modifier(.control), applicationCursor: false)
        #expect(k.accessory(.text("-"), applicationCursor: false) == [0x1f])
        // locked ctrl
        _ = k.accessory(.modifier(.control), applicationCursor: false)
        _ = k.accessory(.modifier(.control), applicationCursor: false)
        #expect(k.text("a") == [0x01]); #expect(k.text("e") == [0x05])
        _ = k.accessory(.modifier(.control), applicationCursor: false)
        #expect(k.text("a") == b("a"))
        // the software keyboard's return and delete
        #expect(k.text("\n") == [0x0d])
        #expect(k.deleteBackward() == [0x7f])
        _ = k.accessory(.modifier(.alt), applicationCursor: false)
        #expect(k.deleteBackward() == [0x1b, 0x7f], "alt-backspace: delete a word")
        #expect(AccessoryKey.key(.left).repeats); #expect(!AccessoryKey.key(.escape).repeats)
    }

    @Test("hardware keys: special keys by usage, with modifiers and DECCKM")
    func hardwareSpecial() {
        var k = TermKeyboard()
        func hw(_ u: UInt16, _ m: HardwareKeyEvent.Modifiers = [], app: Bool = false) -> HardwareKeyResult {
            k.hardware(HardwareKeyEvent(usage: u, modifiers: m), applicationCursor: app)
        }
        #expect(hw(HIDUsage.upArrow) == .send(b("\u{1b}[A")))
        #expect(hw(HIDUsage.upArrow, app: true) == .send(b("\u{1b}OA")))
        #expect(hw(HIDUsage.leftArrow, [.control]) == .send(b("\u{1b}[1;5D")))
        #expect(hw(HIDUsage.leftArrow, [.option]) == .send(b("\u{1b}b")), "⌥←: word left")
        #expect(hw(HIDUsage.rightArrow, [.option]) == .send(b("\u{1b}f")))
        #expect(hw(HIDUsage.rightArrow, [.option, .shift]) == .send(b("\u{1b}[1;4C")))
        #expect(hw(HIDUsage.escape) == .send([0x1b]))
        #expect(hw(HIDUsage.tab) == .send([0x09])); #expect(hw(HIDUsage.tab, [.shift]) == .send(b("\u{1b}[Z")))
        #expect(hw(HIDUsage.returnOrEnter) == .send([0x0d])); #expect(hw(HIDUsage.keypadEnter) == .send([0x0d]))
        #expect(hw(HIDUsage.deleteOrBackspace) == .send([0x7f]))
        #expect(hw(HIDUsage.deleteOrBackspace, [.option]) == .send([0x1b, 0x7f]))
        #expect(hw(HIDUsage.deleteForward) == .send(b("\u{1b}[3~")))
        #expect(hw(HIDUsage.home) == .send(b("\u{1b}[H"))); #expect(hw(HIDUsage.end, app: true) == .send(b("\u{1b}OF")))
        #expect(hw(HIDUsage.pageUp) == .send(b("\u{1b}[5~"))); #expect(hw(HIDUsage.pageDown) == .send(b("\u{1b}[6~")))
        #expect(hw(HIDUsage.f1) == .send(b("\u{1b}OP"))); #expect(hw(HIDUsage.f12) == .send(b("\u{1b}[24~")))
        var x = TermKeyboard(); var s = TermKeyboardSettings(); s.optionArrowsMoveByWord = false; x.settings = s
        #expect(x.hardware(HardwareKeyEvent(usage: HIDUsage.leftArrow, modifiers: [.option]), applicationCursor: false) == .send(b("\u{1b}[1;3D")))
    }

    @Test("hardware keys: text passes through to the text system unless ctrl or Meta applies")
    func hardwareText() {
        var k = TermKeyboard()
        let a = HardwareKeyEvent(usage: HIDUsage.a, characters: "a", charactersIgnoringModifiers: "a")
        #expect(k.hardware(a, applicationCursor: false) == .passthrough)
        var ca = a; ca.modifiers = [.control]; ca.characters = "\u{01}"
        #expect(k.hardware(ca, applicationCursor: false) == .send([0x01]))
        var csa = ca; csa.modifiers = [.control, .shift]
        #expect(k.hardware(csa, applicationCursor: false) == .send([0x01]))
        let ctrlBracket = HardwareKeyEvent(usage: HIDUsage.openBracket, characters: "\u{1b}", charactersIgnoringModifiers: "[", modifiers: [.control])
        #expect(k.hardware(ctrlBracket, applicationCursor: false) == .send([0x1b]))
        let ctrlSpace = HardwareKeyEvent(usage: HIDUsage.spacebar, characters: " ", charactersIgnoringModifiers: " ", modifiers: [.control])
        #expect(k.hardware(ctrlSpace, applicationCursor: false) == .send([0x00]))
        // ⌥ is the layout's by default (German: ⌥L = @)
        let optL = HardwareKeyEvent(usage: 0x0f, characters: "@", charactersIgnoringModifiers: "l", modifiers: [.option])
        #expect(k.hardware(optL, applicationCursor: false) == .passthrough)
        var meta = TermKeyboard(); var s = TermKeyboardSettings(); s.optionAsMeta = true; meta.settings = s
        #expect(meta.hardware(optL, applicationCursor: false) == .send([0x1b, 0x6c]), "⌥ as Meta: ESC l")
        var optShiftL = optL; optShiftL.modifiers = [.option, .shift]
        #expect(meta.hardware(optShiftL, applicationCursor: false) == .send([0x1b, 0x4c]))
        // a sticky ctrl from the accessory row applies to a hardware key, once
        _ = k.accessory(.modifier(.control), applicationCursor: false)
        let c = HardwareKeyEvent(usage: HIDUsage.c, characters: "c", charactersIgnoringModifiers: "c")
        #expect(k.hardware(c, applicationCursor: false) == .send([0x03]))
        #expect(k.hardware(c, applicationCursor: false) == .passthrough)
        _ = k.accessory(.modifier(.control), applicationCursor: false)
        #expect(k.hardware(HardwareKeyEvent(usage: HIDUsage.upArrow), applicationCursor: false) == .send(b("\u{1b}[1;5A")))
        #expect(k.sticky.active == [])
    }

    @Test("⌘ shortcuts: ⌘K clear, ⌘T new, ⌘⇧[ ⌘⇧] switch, copy/paste, font, find; the rest is the system's")
    func shortcuts() {
        var k = TermKeyboard()
        func cmd(_ u: UInt16, _ m: HardwareKeyEvent.Modifiers = []) -> HardwareKeyResult {
            k.hardware(HardwareKeyEvent(usage: u, modifiers: m.union(.command)), applicationCursor: false)
        }
        #expect(cmd(HIDUsage.k) == .shortcut(.clear))
        #expect(cmd(HIDUsage.t) == .shortcut(.newSession))
        #expect(cmd(HIDUsage.openBracket, [.shift]) == .shortcut(.previousSession))
        #expect(cmd(HIDUsage.closeBracket, [.shift]) == .shortcut(.nextSession))
        #expect(cmd(HIDUsage.openBracket) == .passthrough, "⌘[ without shift is not ours")
        #expect(cmd(HIDUsage.c) == .shortcut(.copy)); #expect(cmd(HIDUsage.v) == .shortcut(.paste))
        #expect(cmd(HIDUsage.equalSign) == .shortcut(.fontBigger)); #expect(cmd(HIDUsage.equalSign, [.shift]) == .shortcut(.fontBigger))
        #expect(cmd(HIDUsage.hyphen) == .shortcut(.fontSmaller)); #expect(cmd(HIDUsage.digit0) == .shortcut(.fontReset))
        #expect(cmd(HIDUsage.f) == .shortcut(.find))
        #expect(cmd(0x14) == .passthrough, "⌘Q is the system's")
        #expect(cmd(HIDUsage.k, [.control]) == .passthrough)
        #expect(cmd(HIDUsage.leftArrow) == .passthrough)
    }

    @Test("accessory keys persist (the customizable slot is stored)")
    func codable() throws {
        let keys: [AccessoryKey] = [.modifier(.alt), .key(.function(5)), .text("sudo ")]
        let data = try JSONEncoder().encode(keys)
        #expect(try JSONDecoder().decode([AccessoryKey].self, from: data) == keys)
    }
}
