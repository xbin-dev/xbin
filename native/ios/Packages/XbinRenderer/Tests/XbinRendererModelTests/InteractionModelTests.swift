import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

/// Drawers, split and toolbar grouping, against the fixtures.
@MainActor
@Suite struct DrawerAndLayoutTests {
    func model(_ fixture: String) throws -> XbinTreeModel {
        XbinTreeModel(store: try XbinFixtures.store(fixture, in: fixtureSet()))
    }

    @Test func aLeadingSheetIsADrawerOverTheFragment() throws {
        let m = try model("drawer")
        let root = try #require(m.root)
        let layout = FragmentLayout(root.children)
        #expect(layout.content.map(\.type) == ["screen"])
        #expect(layout.sheets.isEmpty)
        #expect(layout.drawers.map(\.key) == ["r.1"])
        let props = SheetProps(layout.drawers[0])
        #expect(props.isDrawer && props.edge == .leading && props.isOpen && props.isWhole)
        #expect(props.title == "Conversations")
        // The chat screen under it keeps no sheets of its own.
        let screen = ScreenLayout(layout.content[0])
        #expect(screen.sheets.isEmpty && screen.drawers.isEmpty && screen.isChat)
    }

    @Test func sheetOpenSplitsBottomSheetsFromItsClosedDrawer() throws {
        let m = try model("sheet-open")
        let layout = FragmentLayout(try #require(m.root).children)
        #expect(layout.sheets.count == 2)
        #expect(layout.sheets.map { SheetProps($0).edge } == [.bottom, .bottom])
        #expect(layout.drawers.count == 1)
        let drawer = SheetProps(layout.drawers[0])
        #expect(drawer.isDrawer && !drawer.isOpen)
        #expect(SheetProps(layout.sheets[0]).detents == [.medium, .large])
    }

    @Test func drawersInsideAScreenOrANavAreFound() throws {
        let tree = try node("""
        {"k":"r","t":"nav","c":[
          {"k":"a","t":"screen","c":[{"k":"a.d","t":"sheet","p":{"edge":"leading"}},{"k":"a.s","t":"sheet"}]},
          {"k":"b","t":"screen","c":[{"k":"b.d","t":"sheet","p":{"edge":"leading","open":false}},{"k":"b.x","t":"sheet","p":{"edge":"top"}}]}
        ]}
        """)
        let (_, m, _) = mounted(tree)
        let screens = try #require(m.root).children
        #expect(ScreenLayout(screens[0]).drawers.map(\.key) == ["a.d"])
        #expect(ScreenLayout(screens[0]).sheets.map(\.key) == ["a.s"])
        // An unknown edge is a bottom sheet.
        #expect(ScreenLayout(screens[1]).sheets.map(\.key) == ["b.x"])
        #expect(SheetProps(screens[1].children[1]).edge == .bottom)
        #expect(FragmentLayout.drawers(ofScreens: screens).map(\.key) == ["a.d", "b.d"])
    }

    @Test func drawerGeometryAndGestures() {
        #expect(DrawerMetrics.width(container: 390) == 390 * 0.86)
        #expect(DrawerMetrics.width(container: 1024) == 400)
        #expect(DrawerMetrics.scrim(offset: 0, width: 335) == 0.28)
        #expect(DrawerMetrics.scrim(offset: 335, width: 335) == 0)
        #expect(DrawerMetrics.scrim(offset: 500, width: 335) == 0)
        #expect(DrawerMetrics.scrim(offset: 10, width: 0) == 0)
        // Dragging towards the leading edge: left in LTR, right in RTL;
        // the other way never opens it further.
        #expect(DrawerMetrics.offset(translation: -60, rightToLeft: false) == 60)
        #expect(DrawerMetrics.offset(translation: 60, rightToLeft: false) == 0)
        #expect(DrawerMetrics.offset(translation: 60, rightToLeft: true) == 60)
        #expect(!DrawerMetrics.dismisses(offset: 100, predicted: 120, width: 335))
        #expect(DrawerMetrics.dismisses(offset: 120, predicted: 120, width: 335))
        #expect(DrawerMetrics.dismisses(offset: 40, predicted: 200, width: 335))
    }

    @Test func splitColumnsOnlyWhenWideAndAsked() {
        #expect(SplitLayout(children: 2, regularWidth: true, prefer: nil) == .columns)
        #expect(SplitLayout(children: 2, regularWidth: true, prefer: "auto") == .columns)
        #expect(SplitLayout(children: 2, regularWidth: true, prefer: "single") == .stacked)
        #expect(SplitLayout(children: 2, regularWidth: false, prefer: nil) == .stacked)
        #expect(SplitLayout(children: 1, regularWidth: true, prefer: nil) == .stacked)
    }

    @Test func theChatToolbarGroupsStatusApartFromActions() throws {
        let m = try model("tile-chat")
        let toolbar = ScreenLayout(try #require(m.root)).toolbar
        let groups = ToolbarGroups(toolbar)
        #expect(groups.status.map(\.type) == ["picker", "badge"])
        #expect(groups.actions.map(\.type) == ["button"])
        #expect(ToolbarGroups(nil).status.isEmpty && ToolbarGroups(nil).actions.isEmpty)
    }
}

/// Folded actions and message files, against the fixtures.
@MainActor
@Suite struct FoldedActionsAndFilesTests {
    @Test func everyRowAndMessageOfFoldedActionsCarriesSeveralActions() throws {
        let m = XbinTreeModel(store: try XbinFixtures.store("folded-actions", in: fixtureSet()))
        let root = try #require(m.root)
        #expect(root.type == "split")
        var rows = 0, messages = 0
        var stack = [root]
        while let n = stack.popLast() {
            stack.append(contentsOf: n.children)
            guard n.type == "row" || n.type == "message" else { continue }
            let actions = n.children(of: "actions").first
            #expect((actions?.children.count ?? 0) >= 3, "\(n.key) has few actions")
            if n.type == "row" { rows += 1 } else { messages += 1 }
        }
        #expect(rows == 4 && messages == 3)
    }

    @Test func messageFilesSplitIntoThumbnailsAndChips() throws {
        let m = XbinTreeModel(store: try XbinFixtures.store("message-files", in: fixtureSet()))
        let transcript = try #require(m.root?.children.first)
        let msgs = transcript.children.map { ChatMessage(id: $0.key, props: $0.props) }
        #expect(msgs.count == 5)
        #expect(msgs[0].files.map(\.thumbnailSource) == ["api/apps/support/files/f-81/thumb", "api/apps/support/files/f-82/thumb"])
        let label = try #require(msgs[2].files.first?.thumbnailSource)
        #expect(label.hasPrefix("data:image/png;base64,"))
        let png = try #require(DataURL.decode(label))
        #expect(PreviewFile.imageExtension(png) == "png")
        #expect(msgs[2].text.isEmpty)
        // Documents and an image without a source are chips.
        #expect(msgs[3].files.map(\.thumbnailSource) == [nil, nil])
        #expect(msgs[4].files.map(\.isImage) == [true] && msgs[4].files[0].thumbnailSource == nil)
        #expect(ChatFile(name: "x.png", mime: "image/png", src: "").thumbnailSource == nil)
    }
}

/// Quick Look file names.
@Suite struct PreviewFileTests {
    @Test func sniffsTheImageFormat() {
        func d(_ b: [UInt8]) -> Data { Data(b) }
        #expect(PreviewFile.imageExtension(d([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0])) == "png")
        #expect(PreviewFile.imageExtension(d([0xFF, 0xD8, 0xFF, 0xE0])) == "jpg")
        #expect(PreviewFile.imageExtension(Data("GIF89a".utf8)) == "gif")
        #expect(PreviewFile.imageExtension(Data("RIFF\u{0}\u{0}\u{0}\u{0}WEBPVP8 ".utf8)) == "webp")
        #expect(PreviewFile.imageExtension(Data("\u{0}\u{0}\u{0}\u{18}ftypheic".utf8)) == "heic")
        #expect(PreviewFile.imageExtension(Data("\u{0}\u{0}\u{0}\u{18}ftypavif".utf8)) == "avif")
        #expect(PreviewFile.imageExtension(Data("BM....".utf8)) == "bmp")
        #expect(PreviewFile.imageExtension(d([0x4D, 0x4D, 0x00, 0x2A])) == "tiff")
        #expect(PreviewFile.imageExtension(Data("<svg".utf8)) == nil)
        #expect(PreviewFile.imageExtension(Data()) == nil)
    }

    @Test func namesAreSafeAndCarryTheBytesExtension() {
        let png = Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A])
        #expect(PreviewFile.name("label.png", data: png) == "label.png")
        #expect(PreviewFile.name("box-front.jpg", data: png) == "box-front.png")
        #expect(PreviewFile.name("Latency over 24 h", data: png) == "Latency over 24 h.png")
        #expect(PreviewFile.name("../../etc/passwd", data: png) == "-..-etc-passwd.png")
        #expect(PreviewFile.name(".hidden", data: png) == "hidden.png")
        #expect(PreviewFile.name("a\u{0}b\nc", data: png) == "a b c.png")
        #expect(PreviewFile.name(nil, data: png) == "image.png")
        #expect(PreviewFile.name("  ", data: Data()) == "image.png")
        #expect(PreviewFile.name("scan.heic", data: Data()) == "scan.heic")
        #expect(PreviewFile.name(String(repeating: "x", count: 200), data: png).count == 84)
    }
}

/// The IME-aware controlled text rules (tree.md §6).
@Suite struct TextInputGateTests {
    @Test func plainTypingReportsEachChangeOnce() {
        var g = TextInputGate(value: "")
        #expect(g.changed(text: "a", composing: false) == .report("a"))
        #expect(g.changed(text: "ab", composing: false) == .report("ab"))
        // A selection move (the same text) reports nothing.
        #expect(g.changed(text: "ab", composing: false) == .none)
        // The tile echoes what was reported: nothing to do.
        #expect(g.tile("ab", shown: "ab", composing: false) == .none)
        #expect(g.reported == "ab")
    }

    @Test func aCompositionReportsOnlyItsCommit() {
        // Typing とうきょう, then choosing 東京 (and a Korean syllable
        // being built works the same: ㅎ → 하 → 한, committed as 한).
        var g = TextInputGate(value: "")
        for step in ["t", "と", "とう", "とうk", "とうきょう", "東京"] {
            #expect(g.changed(text: step, composing: true) == .none)
        }
        #expect(g.changed(text: "東京", composing: false) == .report("東京"))
        // Composing the next word after committed text.
        #expect(g.changed(text: "東京と", composing: true) == .none)
        #expect(g.changed(text: "東京都", composing: false) == .report("東京都"))
        // A cancelled composition (marked text deleted) back to what was
        // reported: nothing.
        #expect(g.changed(text: "東京都x", composing: true) == .none)
        #expect(g.changed(text: "東京都", composing: false) == .none)
    }

    @Test func aTileSetAppliesAtOnceOutsideAComposition() {
        // The postal code arrives full-width; the tile normalizes it.
        var g = TextInputGate(value: "")
        #expect(g.changed(text: "１５０－０００１", composing: false) == .report("１５０－０００１"))
        #expect(g.tile("150-0001", shown: "１５０－０００１", composing: false) == .replace("150-0001"))
        #expect(g.reported == "150-0001")
        // The replacement doesn't come back as a report.
        #expect(g.changed(text: "150-0001", composing: false) == .none)
        // A reset of a focused field (the tile clears the draft).
        #expect(g.changed(text: "150-00012", composing: false) == .report("150-00012"))
        #expect(g.tile("", shown: "150-00012", composing: false) == .replace(""))
        // A set to what the control already shows needs no replace.
        #expect(g.tile("x", shown: "x", composing: false) == .none)
        #expect(g.reported == "x")
    }

    @Test func aTileSetDuringACompositionWaitsForItsEnd() {
        var g = TextInputGate(value: "draft")
        #expect(g.changed(text: "draftか", composing: true) == .none)
        #expect(g.tile("", shown: "draftか", composing: true) == .none)
        #expect(g.deferred == "")
        #expect(g.changed(text: "draftかな", composing: true) == .none)
        // The composition commits: the tile's value replaces it, and the
        // composed text is not reported.
        #expect(g.changed(text: "draft仮名", composing: false) == .replace(""))
        #expect(g.deferred == nil && g.reported == "")
        #expect(g.changed(text: "", composing: false) == .none)
    }

    @Test func aDeferredSetTheTileTakesBackIsDropped() {
        var g = TextInputGate(value: "a")
        #expect(g.changed(text: "aか", composing: true) == .none)
        #expect(g.tile("b", shown: "aか", composing: true) == .none)
        #expect(g.tile("a", shown: "aか", composing: true) == .none)
        #expect(g.deferred == nil)
        #expect(g.changed(text: "a化", composing: false) == .report("a化"))
    }

    @Test func aDeferredSetEqualToTheCommitReportsNothing() {
        var g = TextInputGate(value: "")
        #expect(g.changed(text: "ｘ", composing: true) == .none)
        #expect(g.tile("x", shown: "ｘ", composing: true) == .none)
        #expect(g.changed(text: "x", composing: false) == .none)
        #expect(g.reported == "x")
    }
}

/// Wrapping long identifiers at characters instead of hyphenating them.
@Suite struct TextBreaksTests {
    let zw = String(TextBreaks.breakOpportunity)

    @Test func identifiersWrapAnywhereProseDoesNot() {
        #expect(TextBreaks.anywhere("llmgw_active_requests", mono: false) == "llmgw_active_requests".map(String.init).joined(separator: zw))
        #expect(TextBreaks.anywhere("Nightly backup finished", mono: false) == "Nightly backup finished")
        #expect(TextBreaks.anywhere("Notifications", mono: false) == "Notifications")
        #expect(TextBreaks.needsBreaks("checkoutLatencyP95", mono: false))
        #expect(TextBreaks.needsBreaks("docker.io/library/ubuntu:24.04", mono: false))
        #expect(!TextBreaks.needsBreaks("well-established", mono: false))
        #expect(!TextBreaks.needsBreaks("a_b", mono: false))
        // Only the long word of a sentence changes; spaces stay.
        let s = TextBreaks.anywhere("rate of llmgw_tokens_out_total now", mono: false)
        #expect(s.hasPrefix("rate of l\(zw)l") && s.hasSuffix("l now"))
        #expect(s.replacingOccurrences(of: zw, with: "") == "rate of llmgw_tokens_out_total now")
    }

    @Test func monospacedWordsWrapFromTenCharacters() {
        #expect(TextBreaks.needsBreaks("xbin-prod-1.acme.dev", mono: true))
        #expect(TextBreaks.needsBreaks("q7Lw0d3k9v", mono: true))
        #expect(!TextBreaks.needsBreaks("12 d 4 h", mono: true))
        #expect(!TextBreaks.applies(to: "12 d 4 h", mono: true))
        #expect(TextBreaks.applies(to: "SHA256:q7L-w0d3k9vXcN2mB", mono: true))
        // Grapheme clusters stay whole.
        let flags = "🇵🇱🇯🇵🇺🇸🇫🇷🇩🇪🇮🇹🇪🇸🇬🇧🇳🇱🇸🇪"
        let broken = TextBreaks.anywhere(flags, mono: true)
        #expect(broken.split(separator: TextBreaks.breakOpportunity).count == 10)
        #expect(TextBreaks.anywhere("", mono: true) == "")
        #expect(TextBreaks.anywhere("a\nb  c", mono: true) == "a\nb  c")
    }
}
