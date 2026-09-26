import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

@Suite struct MarkdownTests {
    func fixtureTokens(_ name: String, _ key: String) throws -> JSONValue? {
        let root = try fixtureSet().expected(name).root
        return try #require(root.find(key)).props["tokens"]
    }

    @Test func theMarkdownFixtureDecodesEveryBlock() throws {
        let blocks = Markdown.blocks(try fixtureTokens("markdown", "r.0"))
        #expect(blocks.count == 19)
        guard case .heading(1, let h1) = blocks[0] else { Issue.record("h1: \(blocks[0])"); return }
        #expect(MarkdownInline.plainText(h1) == "Rotate the database password")
        let levels = blocks.compactMap { if case .heading(let l, _) = $0 { return l }; return nil }
        #expect(levels == [1, 2, 2, 3, 3, 4, 5, 6])
        guard case .list(let steps) = blocks[5] else { Issue.record("steps: \(blocks[5])"); return }
        #expect(steps.ordered && steps.loose && steps.start == 1 && steps.marker(1) == "2.")
        guard case .code(let text, let lang) = steps.items[0].blocks[1] else { Issue.record("code"); return }
        #expect(text == "bx secret new db/app-password --length 32" && lang == "sh")
        guard case .list(let more) = blocks[7] else { Issue.record("list 2"); return }
        #expect(more.start == 3 && more.marker(0) == "3.")
        guard case .quote(let q) = blocks[8], case .paragraph(let qp) = q.first else { Issue.record("quote"); return }
        #expect(qp.contains(.lineBreak))
        guard case .list(let tasks) = blocks[10] else { Issue.record("tasks"); return }
        #expect(tasks.items.map(\.task) == [true, true, true, true] && tasks.items.map(\.checked) == [true, true, false, false])
        guard case .table(let table) = blocks[12] else { Issue.record("table"); return }
        #expect(table.columns == 4 && table.rows.count == 3)
        #expect(table.alignments == [.left, .center, .right, nil] && table.alignment(9) == nil)
        #expect(MarkdownInline.plainText(table.rows[1][3]) == "Chloé")
        #expect(blocks[13] == .rule)
    }

    @Test func runsFlattenStyles() throws {
        let blocks = Markdown.blocks(try fixtureTokens("markdown", "r.0"))
        guard case .paragraph(let p) = blocks[1] else { Issue.record("p"); return }
        let runs = Markdown.runs(p)
        #expect(runs.first == MarkdownRun(text: "Rotating is "))
        #expect(runs.contains(MarkdownRun(text: "safe during business hours", strong: true)))
        #expect(runs.contains(MarkdownRun(text: "two", emphasis: true)))
        #expect(runs.contains(MarkdownRun(text: "#ops", code: true)))
        #expect(runs.contains(MarkdownRun(text: "the credentials policy", link: "https://wiki.acme.dev/security/credentials")))
        #expect(runs.contains(MarkdownRun(text: "Restart the API by hand", strikethrough: true)))
        #expect(runs.map(\.text).joined() == MarkdownInline.plainText(p))
        // Neighbours of one style merge.
        #expect(Markdown.runs([.text("a"), .text("b"), .strong([.text("c")])]) == [
            MarkdownRun(text: "ab"), MarkdownRun(text: "c", strong: true),
        ])
        #expect(Markdown.runs([.strong([.emphasis([.code("x")])])]) == [MarkdownRun(text: "x", strong: true, emphasis: true, code: true)])
    }

    @Test func unknownAndUnsafeTokensDegradeToText() throws {
        let tokens = try JSONValue(parsing: """
        [{"t":"callout","text":"newer block"},{"t":"paragraph","c":[
          {"t":"link","href":"javascript:alert(1)","c":[{"t":"text","text":"x"}]},
          {"t":"sup","c":[{"t":"text","text":"2"}]},{"t":"kbd","text":"⌘K"}]},
         {"t":"nothing"},7]
        """)
        let blocks = Markdown.blocks(tokens)
        #expect(blocks == [
            .paragraph([.text("newer block")]),
            .paragraph([.span([.text("x")]), .span([.text("2")]), .text("⌘K")]),
        ])
        #expect(Markdown.runs([.span([.text("x")])]).first?.link == nil)
        #expect(Markdown.isOpenable("MAILTO:a@b") && !Markdown.isOpenable("file:///etc/passwd"))
        #expect(Markdown.blocks(nil).isEmpty && Markdown.blocks("x").isEmpty)
    }

    @Test func sourceWithoutTokensIsPlainText() {
        #expect(Markdown.blocks(props: Props(["source": "a **b**\n\n\nc"])) == [.paragraph([.text("a **b**")]), .paragraph([.text("c")])])
        #expect(Markdown.blocks(props: Props(["source": "x", "tokens": []])).isEmpty)
        #expect(ChatMessage(id: "m", props: Props(["markdown": true, "text": "*hi*"])).markdown == nil)
    }

    @Test func streamingListWithAnEmptyItem() throws {
        let blocks = Markdown.blocks(try fixtureTokens("chat-transcript", "r.0.2.1"))
        guard case .list(let l) = blocks.last else { Issue.record("list"); return }
        #expect(l.items.count == 2 && l.items[1].blocks.isEmpty)
        #expect(Markdown.plainText(blocks).contains("• the smoke test hangs"))
    }
}

@Suite struct ChartTests {
    /// [value, fmtY number, fmtY bytes, fmtY percent] from the reference
    /// renderer's own functions (web/xb/render-chart.js, run in node).
    static let reference: [(Double, String, String, String)] = [
        (0, "0", "0 B", "0%"), (1, "1", "1 B", "100%"), (-1, "-1", "-1 B", "-100%"),
        (0.004, "4.0e-3", "0 B", "0.4%"), (0.52, "0.52", "1 B", "52%"), (0.21, "0.21", "0 B", "21%"),
        (12.345, "12.3", "12 B", "1230%"), (999, "999", "999 B", "99900%"), (1000, "1K", "1000 B", "100000%"),
        (18422, "18.4K", "18 KB", "1840000%"), (1204, "1.2K", "1.18 KB", "120000%"), (391, "391", "391 B", "39100%"),
        (27, "27", "27 B", "2700%"), (4617089843, "4.62G", "4.3 GB", "462000000000%"),
        (1932735283, "1.93G", "1.8 GB", "193000000000%"), (5583769190, "5.58G", "5.2 GB", "558000000000%"),
        (1e12, "1T", "931 GB", "100000000000000%"), (2.5e9, "2.5G", "2.33 GB", "250000000000%"),
        (-3456, "-3.46K", "-3.38 KB", "-346000%"), (0.0001, "1.0e-4", "0 B", "0.01%"),
        (1023, "1.02K", "1023 B", "102000%"), (1024, "1.02K", "1 KB", "102000%"), (1536, "1.54K", "1.5 KB", "154000%"),
        (123456789, "123M", "118 MB", "12300000000%"),
    ]

    @Test func labelsMatchTheReferenceRenderer() {
        for (v, n, b, p) in Self.reference {
            #expect(ChartFormat.y(.number, v) == n, "number \(v)")
            #expect(ChartFormat.y(.bytes, v) == b, "bytes \(v)")
            #expect(ChartFormat.y(.percent, v) == p, "percent \(v)")
        }
    }

    @Test func theChartsFixture() throws {
        let root = try fixtureSet().expected("charts").root
        func chart(_ k: String) throws -> ChartModel { ChartModel(props: Props(try #require(root.find(k)).props)) }
        let cpu = try chart("r.1.0")
        #expect(cpu.kind == .line && cpu.x == .time && cpu.y == .percent && cpu.height == 160 && cpu.showsLegend)
        #expect(cpu.series.map(\.name) == ["user", "system"] && cpu.series[0].points.count == 13)
        #expect(cpu.series[0].points[0].x == .time(Date(timeIntervalSince1970: 1789996400)))
        let mem = try chart("r.2.0")
        #expect(mem.kind == .area && mem.y == .bytes && mem.height == 240)
        let bars = try chart("r.3.0")
        #expect(bars.kind == .bar && bars.series[0].points.map(\.x) == [.category("2xx"), .category("3xx"), .category("4xx"), .category("5xx")])
        #expect(!bars.showsLegend && bars.yRange == 27...18422)
        let spark = try chart("r.5.0:web-1.0")
        #expect(spark.kind == .spark && spark.height == 48 && !spark.showsLegend)
        #expect(try chart("r.4.0").height == 360)
    }

    /// The reference renderer's y ticks for the fixture's charts (its
    /// `nice()`/`frame()` run in node over the same points).
    @Test func yTicksMatchTheReferenceRenderer() throws {
        let root = try fixtureSet().expected("charts").root
        func ticks(_ k: String) throws -> [Double] { ChartModel(props: Props(try #require(root.find(k)).props)).yTicks }
        #expect(try ticks("r.1.0") == [0, 0.2, 0.4, 0.6])
        let gib = 1_073_741_824.0
        #expect(try ticks("r.2.0") == [0, 2 * gib, 4 * gib, 6 * gib])
        #expect(try ticks("r.3.0") == [0, 10000, 20000])
        #expect(try ticks("r.4.0") == [0, 5, 10, 15, 20])
        #expect(try ticks("r.5.0:web-1.0") == [])
        #expect(ChartModel.nice(3, 3) == [1, 2, 3, 4, 5])
        #expect(ChartModel(props: Props([:])).yTicks == [])
    }

    @Test func badPointsAreDropped() throws {
        let p = Props(try JSONValue(parsing: """
        {"series":[{"name":"a","points":[[1,2],[2],"x",[3,null],["4","5"],[null,1]]}]}
        """).objectValue!)
        let c = ChartModel(props: p)
        #expect(c.kind == .line && c.x == .number && c.height == 160)
        // As JavaScript's Number(): null → 0, "4" → 4; short and non-array points go.
        #expect(c.series[0].points == [.init(x: .number(1), y: 2), .init(x: .number(3), y: 0), .init(x: .number(4), y: 5),
                                       .init(x: .number(0), y: 1)])
        #expect(ChartModel(props: Props([:])).isEmpty)
        #expect(ChartModel(props: Props(["kind": "spark"])).height == 36)
    }
}

@Suite struct FlowLayoutTests {
    @Test func wrapsAndCentres() {
        let p = FlowLayoutMath.place([(40, 20), (40, 10), (40, 20), (100, 30)], maxWidth: 100, spacing: 4, lineSpacing: 6)
        // 40 + 4 + 40 = 84 fits; the third would end at 128 → new line; the
        // 100-wide one gets its own line.
        #expect(p.origins.map(\.x) == [0, 44, 0, 0])
        #expect(p.origins.map(\.y) == [0, 5, 26, 52])
        #expect(p.width == 100 && p.height == 82)
        let one = FlowLayoutMath.place([(10, 10), (10, 10)], maxWidth: nil, spacing: 2, lineSpacing: 2)
        #expect(one.width == 22 && one.height == 10)
        #expect(FlowLayoutMath.place([], maxWidth: 10, spacing: 1, lineSpacing: 1).height == 0)
        // Wider than the line: alone, never an empty line before it.
        let wide = FlowLayoutMath.place([(200, 10)], maxWidth: 100, spacing: 4, lineSpacing: 6)
        #expect(wide.origins.map(\.y) == [0] && wide.height == 10)
    }
}

@Suite struct NavAndFieldTests {
    @Test func navPath() {
        let s = ["a", "b", "c"]
        #expect(NavStack.path(s, popped: 0) == ["b", "c"])
        #expect(NavStack.path(s, popped: 1) == ["b"])
        #expect(NavStack.path(s, popped: 5) == [])
        #expect(NavStack.path(["a"], popped: 0) == [])
        // Back from c: path 2 → 1, two screens remain.
        let back = NavStack.pop(screens: 3, popped: 0, newPathCount: 1)
        #expect(back?.popped == 1 && back?.depth == 2)
        // Pop to root from c.
        #expect(NavStack.pop(screens: 3, popped: 0, newPathCount: 0).map { [$0.popped, $0.depth] } == [2, 1])
        // Not a pop.
        #expect(NavStack.pop(screens: 3, popped: 1, newPathCount: 1) == nil)
    }

    @Test func dateAndTimeValues() throws {
        let d = try #require(FieldValue.date("2026-09-21"))
        #expect(FieldValue.dateString(d) == "2026-09-21")
        #expect(FieldValue.date("21/09/2026") == nil && FieldValue.date("2026-13-01") == nil)
        let t = try #require(FieldValue.time("02:30"))
        #expect(FieldValue.timeString(t) == "02:30" && FieldValue.time("02:30:15").map(FieldValue.timeString) == "02:30")
        #expect(FieldValue.time("25:00") == nil)
    }

    @Test func props() {
        let p = Props(["s": "x", "n": 42, "d": 2.5, "b": true, "z": 0, "o": ["a": 1], "a": [1, "x", ["k": 1]]])
        #expect(p.string("n") == "42" && p.string("d") == "2.5" && p.string("o") == nil)
        #expect(p.bool("b") && !p.bool("z") && p.optionalBool("missing") == nil)
        #expect(p.number("n") == 42 && p.number("s") == nil)
        #expect(p.objects("a").count == 1 && p.array("a").count == 3)
        #expect(p.nonEmpty("s") == "x" && Props(["e": ""]).nonEmpty("e") == nil)
        #expect(Props.number(1e20) == "1e+20" && Props.number(-3) == "-3")
    }
}
