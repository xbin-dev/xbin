import Foundation
import Testing
@testable import XbinAgent

// The port against the web's own outputs: js-parity.json is what
// web/agent-tools.js, web/agent-slash.js and bx-agent.js's _blocks() return
// over a corpus and over every captured session (native/tools/agent-parity.mjs).

@Suite struct ParityTests {
    let p: JSONValue
    init() throws { p = try fixture("js-parity") }

    @Test func describeCommand() throws {
        let cases = try #require(p["describe"]?.array)
        #expect(cases.count > 30)
        for c in cases {
            let input = try #require(c["in"]?.string)
            #expect(ToolReading.describeCommand(input) == c["out"]?.string, "describeCommand(\(input.debugDescription))")
        }
    }

    @Test func headlinesAndFold() throws {
        for (k, c) in try #require(p["headlines"]?.array).enumerated() {
            var t = ToolCall(id: "x", toolCallId: "t\(k)")
            for d in c["events"]?.array ?? [] {
                var dd = d.object ?? JSONObject()
                dd["id"] = .string("t\(k)")
                t.fold(try #require(ToolCallUpdate(json: .object(dd))))
            }
            #expect(t.headline == c["headline"]?.string, "headline #\(k)")
            #expect(t.command == c["command"]?.string, "command #\(k)")
            #expect(t.isPlanApproval == c["plan"]?.bool, "plan #\(k)")
            #expect(t.planText == c["planText"]?.string, "planText #\(k)")
            #expect(t.output == c["output"]?.string, "output #\(k)")
            #expect(ToolReading.rawText(t.rawInput) == c["raw"]?.string, "rawText #\(k)")
        }
    }

    @Test func rawText() throws {
        for c in try #require(p["rawTexts"]?.array) {
            let input = c["in"]
            #expect(ToolReading.rawText(input) == c["out"]?.string, "rawText(\(input?.compactString ?? "nil"))")
        }
    }

    @Test func forms() throws {
        for (k, c) in try #require(p["forms"]?.array).enumerated() {
            let schema = c["schema"].flatMap { $0.isNull ? nil : $0 }
            let fields = FormField.fields(schema: schema)
            let want = c["fields"]?.array ?? []
            #expect(fields.count == want.count, "fields #\(k)")
            for (f, w) in zip(fields, want) {
                #expect(f.key == w["key"]?.string)
                #expect(f.kind.rawValue == w["kind"]?.string)
                #expect(f.title == w["title"]?.string)
                #expect(f.description == w["description"]?.string)
                #expect(f.required == w["required"]?.bool)
                #expect(JSONValue.of(f.other) == w["other"])
                #expect(f.otherHint == w["otherHint"]?.string)
                #expect(f.options.map(\.value) == (w["options"]?.array ?? []).map { $0["value"] ?? .null }, "options of \(f.key)")
                #expect(f.options.map(\.title) == (w["options"]?.array ?? []).compactMap { $0["title"]?.string })
            }
            var values: [String: JSONValue] = [:]
            for (key, v) in c["values"]?.object ?? JSONObject() { values[key] = v }
            let content = FormField.content(fields, values: values)
            #expect(content == c["content"], "content #\(k): \(content.compactString)")
            #expect(FormField.missingRequired(fields, content: content) == (c["missing"]?.array ?? []).compactMap(\.string), "missing #\(k)")
        }
    }

    @Test func unifiedDiff() throws {
        for c in try #require(p["diffs"]?.array) {
            let out = LineDiff.unified(path: "f.txt", old: c["old"]?.string, new: c["new"]?.string)
            #expect(out == c["out"]?.string, "unifiedDiff(\(c["old"]?.compactString ?? "") → \(c["new"]?.compactString ?? ""))")
        }
    }

    @Test func stripAnsi() throws {
        for c in try #require(p["ansi"]?.array) {
            #expect(stripANSI(c["in"]?.string ?? "") == c["out"]?.string, "stripAnsi(\(c["in"]?.string?.debugDescription ?? ""))")
        }
    }

    @Test func slash() throws {
        let cmds = try #require(p["slash"]?["commands"]?.list(SlashCommand.self))
        for c in try #require(p["slash"]?["cases"]?.array) {
            let draft = c["draft"]?.string ?? ""
            #expect(JSONValue.of(SlashCompletion.query(draft)) == c["query"], "query(\(draft.debugDescription))")
            #expect(SlashCompletion.menu(cmds, draft: draft).map(\.name) == (c["menu"]?.array ?? []).compactMap(\.string), "menu(\(draft.debugDescription))")
            let h = SlashCompletion.hint(cmds, draft: draft)
            #expect(h?.name == c["hint"]?["name"]?.string && h?.hint == c["hint"]?["hint"]?.string, "hint(\(draft.debugDescription))")
        }
    }

    /// Every captured session folds to the same transcript the web renders.
    @Test(arguments: ["basic", "permissions", "plan", "ask", "subagent", "shell", "cancel", "signedout", "attach"])
    func transcript(_ name: String) throws {
        let want = try #require(p["transcripts"]?[name]?.array)
        let t = AgentTranscript(events: try events(name))
        let got = t.items.map(summarize)
        #expect(got.count == want.count, "\(name): \(got.count) items, the web has \(want.count)")
        for (k, (g, w)) in zip(got, want).enumerated() {
            #expect(g == w, "\(name) item \(k):\n  swift \(g.compactString)\n  web   \(w.compactString)")
        }
    }
}
