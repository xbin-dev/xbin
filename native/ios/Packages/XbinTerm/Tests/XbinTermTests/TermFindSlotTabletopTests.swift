// TermFindSlotTabletopTests.swift — the find bar's state and keys, the
// accessory slot's storage and choices, and the Duo tabletop layout.

import Foundation
import Testing
@testable import XbinTerm

@Suite("find bar")
struct TermFindTests {
    @Test("counts follow the last search and reset when the query or options change")
    func counts() {
        var f = TermFind()
        #expect(!f.visible && f.status == "" && !f.canSearch)
        f.open()
        f.query = "err"
        #expect(f.canSearch && f.status == "")
        f.record(found: true, index: 3, total: 14)
        #expect(f.status == "3 of 14" && f.index == 3 && f.total == 14)
        f.record(found: true, index: 2, total: 1000)
        #expect(f.status == "2 of 1000+")
        f.record(found: true, index: 0, total: 1000)
        #expect(f.status == "1000+ matches")
        f.record(found: true, index: 0, total: 1)
        #expect(f.status == "1 match")
        f.record(found: false, index: 5, total: 0)
        #expect(f.status == "No matches" && f.index == 0)
        f.record(found: true, index: 9, total: 4)
        #expect(f.index == 4, "never past the count")
        f.query = "error"
        #expect(f.status == "" && f.total == 0 && f.searched == nil)
        f.record(found: true, index: 1, total: 2)
        f.options.wholeWord = true
        #expect(f.status == "")
        f.record(found: true, index: 1, total: 2)
        f.close()
        #expect(!f.visible && f.status == "" && f.query == "error", "the query stays for ⌘G")
    }

    @Test("a regular expression that doesn't compile is reported, not searched")
    func regex() {
        var f = TermFind()
        f.query = "a(b"
        #expect(f.problem == nil && f.canSearch, "a plain search takes any text")
        f.options.regex = true
        #expect(f.problem == "Invalid pattern" && !f.canSearch && f.status == "Invalid pattern")
        f.query = "a(b)+"
        #expect(f.problem == nil && f.canSearch)
    }

    @Test("keys: Return and ⌘G go up to older matches, ⇧Return and ⌘⇧G down, Escape closes")
    func keys() {
        #expect(TermFind.action(key: .returnKey, shift: false, command: false) == .older)
        #expect(TermFind.action(key: .returnKey, shift: true, command: false) == .newer)
        #expect(TermFind.action(key: .returnKey, shift: false, command: true) == nil)
        #expect(TermFind.action(key: .escape, shift: false, command: false) == .close)
        #expect(TermFind.action(key: .g, shift: false, command: true) == .older)
        #expect(TermFind.action(key: .g, shift: true, command: true) == .newer)
        #expect(TermFind.action(key: .g, shift: false, command: false) == nil, "a plain g is typed")
        #expect(TermFind.action(for: .findNext) == .older && TermFind.action(for: .findPrevious) == .newer)
        #expect(TermFind.action(for: .find) == nil)
    }
}

@Suite("accessory slot")
struct AccessorySlotTests {
    @Test("storage: missing → F1, null → none, the first build's JSON still reads")
    func storage() throws {
        #expect(AccessorySlot.decode(nil) == .key(.function(1)))
        #expect(AccessorySlot.decode(Data("null".utf8)) == nil)
        #expect(AccessorySlot.decode(Data("garbage".utf8)) == nil)
        #expect(AccessorySlot.encode(nil) == Data("null".utf8))
        for k: AccessoryKey in [.modifier(.alt), .key(.pageUp), .key(.function(12)), .text("sudo "), .text("make\n")] {
            #expect(AccessorySlot.decode(AccessorySlot.encode(k)) == k)
        }
        // what the app wrote before this screen existed: JSONEncoder of the key
        let old = try JSONEncoder().encode(AccessoryKey.key(.function(5)))
        #expect(AccessorySlot.decode(old) == .key(.function(5)))
    }

    @Test("the choices leave out what the row already has")
    func choices() {
        let all = AccessorySlot.groups.flatMap(\.keys)
        #expect(all.count == Set(all).count, "no duplicates")
        for k in AccessoryKey.defaultRow { #expect(!all.contains(k), "\(k) is on the row") }
        #expect(all.contains(AccessorySlot.defaultKey))
        #expect(AccessorySlot.isListed(.text("`")) && !AccessorySlot.isListed(.text("sudo ")))
    }

    @Test("snippets: kept as typed, Return as a newline (sent as CR), refused when empty, long or with control characters")
    func snippets() throws {
        #expect(try AccessorySlot.snippet("sudo ", pressReturn: false).get() == .text("sudo "))
        #expect(try AccessorySlot.snippet("git status", pressReturn: true).get() == .text("git status\n"))
        #expect(throws: AccessorySlot.SnippetProblem.empty) { try AccessorySlot.snippet("", pressReturn: true).get() }
        #expect(throws: AccessorySlot.SnippetProblem.tooLong) { try AccessorySlot.snippet(String(repeating: "x", count: 33), pressReturn: false).get() }
        #expect(throws: AccessorySlot.SnippetProblem.controlCharacters) { try AccessorySlot.snippet("a\tb", pressReturn: false).get() }
        #expect(throws: AccessorySlot.SnippetProblem.controlCharacters) { try AccessorySlot.snippet("a\nb", pressReturn: false).get() }
        #expect(AccessorySlot.snippetParts(.text("ls\n"))! == ("ls", true))
        #expect(AccessorySlot.snippetParts(.text("`")) == nil, "a listed character is not a snippet")
        #expect(AccessorySlot.snippetParts(.key(.home)) == nil)
        // what the key sends
        var kb = TermKeyboard()
        #expect(kb.accessory(.text("git status\n"), applicationCursor: false) == Array("git status\r".utf8))
    }

    @Test("key caps and descriptions")
    func labels() {
        #expect(AccessorySlot.capLabel(.key(.function(3))) == "F3")
        #expect(AccessorySlot.capLabel(.modifier(.alt)) == "alt")
        #expect(AccessorySlot.capLabel(.text("`")) == "`")
        #expect(AccessorySlot.capLabel(.text("sudo ")) == "sudo")
        #expect(AccessorySlot.capLabel(.text("   ")) == "␣")
        #expect(AccessorySlot.capLabel(.text("kubectl ")) == "kubec…")
        #expect(AccessorySlot.capLabel(.text("make\n")) == "make⏎")
        #expect(AccessorySlot.capLabel(.text("git status\n")) == "git …⏎")
        #expect(AccessorySlot.describe(.text("make\n")) == "Types “make” and presses Return")
        #expect(AccessorySlot.describe(.key(.pageDown)) == "Page Down")
        #expect(AccessorySlot.describe(.text("`")) == "Types “`”")
    }
}

@Suite("Duo tabletop")
struct TermTabletopTests {
    // The inner display in tabletop: 1000×760 pt, a horizontal fold at 380 with
    // 20 pt margins (a 40 pt band).
    let fold = TermRect(x: 0, y: 360, width: 1000, height: 40)

    @Test("the posture comes from the fold's shape")
    func posture() {
        #expect(TermPosture.of(fold: nil) == .flat)
        #expect(TermPosture.of(fold: TermRect(x: 0, y: 0, width: 0, height: 0)) == .flat)
        #expect(TermPosture.of(fold: fold) == .tabletop)
        #expect(TermPosture.of(fold: TermRect(x: 480, y: 0, width: 40, height: 760)) == .book)
    }

    @Test("tabletop: the terminal above the fold, keys between the fold and the keyboard")
    func tabletop() {
        let l = TermTabletopLayout.compute(width: 1000, height: 760, fold: fold, keyboardHeight: 250)
        #expect(l.posture == .tabletop && l.terminalHeight == 360)
        #expect(l.panel == TermRect(x: 0, y: 400, width: 1000, height: 110))
        #expect(l.keyRows == 2)
        let hidden = TermTabletopLayout.compute(width: 1000, height: 760, fold: fold, keyboardHeight: 0)
        #expect(hidden.panel.height == 360 && hidden.keyRows == 4, "capped at maxKeyRows")
        let over = TermTabletopLayout.compute(width: 1000, height: 760, fold: fold, keyboardHeight: 380)
        #expect(over.posture == .tabletop && over.panel.height == 0 && over.keyRows == 0 && over.terminalHeight == 360,
                "a keyboard over the fold's margin leaves no panel")
        let tall = TermTabletopLayout.compute(width: 1000, height: 760, fold: fold, keyboardHeight: 500)
        #expect(tall.posture == .flat && tall.terminalHeight == 260, "a keyboard above the fold: the terminal gives way")
    }

    @Test("book, flat, and a fold outside the screen: one full-height terminal")
    func others() {
        let book = TermTabletopLayout.compute(width: 1000, height: 760, fold: TermRect(x: 480, y: 0, width: 40, height: 760), keyboardHeight: 300)
        #expect(book.posture == .book && book.terminalHeight == 460 && book.keyRows == 0)
        let flat = TermTabletopLayout.compute(width: 400, height: 800, fold: nil, keyboardHeight: 300)
        #expect(flat.posture == .flat && flat.terminalHeight == 500 && flat.panel.height == 0)
        #expect(TermTabletopLayout.compute(width: 400, height: 800, fold: nil, keyboardHeight: 0).terminalHeight == 800)
        let off = TermTabletopLayout.compute(width: 1000, height: 300, fold: fold, keyboardHeight: 0)
        #expect(off.posture == .flat && off.terminalHeight == 300)
    }

    @Test("the simulated fold sits mid-screen and makes a tabletop")
    func simulated() {
        let f = TermTabletopLayout.simulatedFold(width: 390, height: 801)
        #expect(f == TermRect(x: 0, y: 380, width: 390, height: 40))
        #expect(TermTabletopLayout.compute(width: 390, height: 801, fold: f, keyboardHeight: 0).posture == .tabletop)
    }

    @Test("the panel's rows: the accessory row first when the keyboard is hidden")
    func keys() {
        let shown = TermTabletopKeys.rows(count: 2, keyboardShown: true, slot: .key(.function(1)))
        #expect(shown == [TermTabletopKeys.navigation, TermTabletopKeys.functionLow])
        let hidden = TermTabletopKeys.rows(count: 4, keyboardShown: false, slot: .text("sudo "))
        #expect(hidden.count == 4 && hidden[0] == AccessoryKey.defaultRow + [.text("sudo ")])
        #expect(TermTabletopKeys.rows(count: 9, keyboardShown: true, slot: nil).count == 3)
        #expect(TermTabletopKeys.rows(count: -1, keyboardShown: true, slot: nil).isEmpty)
        #expect(TermTabletopKeys.rows(count: 1, keyboardShown: false, slot: nil) == [AccessoryKey.defaultRow])
    }
}
