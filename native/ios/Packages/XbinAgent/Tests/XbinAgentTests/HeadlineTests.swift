import Foundation
import Testing
@testable import XbinAgent

// hack/agent-tools.test.mjs, case for case (the parity corpus covers more).

@Suite struct HeadlineTests {
    @Test func heredocScripts() {
        let py = "python3 - <<'EOF'\np='backend/main.go'\ns=open(p).read()\ndef rep(old,new,count=1):\n    global s\n    s=s.replace(old,new)\nopen(p,'w').write(s)\nEOF"
        #expect(ToolReading.describeCommand(py) == "Python script (6 lines) → backend/main.go")
        #expect(ToolReading.describeCommand("cd /w/apps/x && node - <<EOF\nconst fs=require('fs');fs.writeFileSync('a.json','{}')\nEOF") == "Node script (1 line) → a.json")
        #expect(ToolReading.describeCommand("python3 - <<'PY'\nprint(1)\nPY") == "Python script (1 line)")
    }

    @Test func writesNameTheirFile() {
        #expect(ToolReading.describeCommand("sed -i 's/a/b/' src/x.go") == "Edit src/x.go (sed)")
        #expect(ToolReading.describeCommand("cd /w && perl -pi -e 's/x/y/' a.txt") == "Edit a.txt (perl)")
        #expect(ToolReading.describeCommand("cat > notes.md <<'EOF'\nhi\nEOF") == "Write notes.md")
        #expect(ToolReading.describeCommand("echo hi | tee out.txt") == "Write out.txt")
        #expect(ToolReading.describeCommand("echo hi > made.txt") == "Write made.txt")
        #expect(ToolReading.describeCommand("rm -rf build") == "Remove build")
        #expect(ToolReading.describeCommand("mv a.txt b.txt") == "Move a.txt → b.txt")
    }

    @Test func everythingElseIsItsFirstLine() {
        #expect(ToolReading.describeCommand("go test ./...") == "go test ./...")
        #expect(ToolReading.describeCommand("sed -n '1,5p' file.go") == "sed -n '1,5p' file.go")
        #expect(ToolReading.describeCommand("ls -la 2>/dev/null") == "ls -la 2>/dev/null")
        #expect(ToolReading.describeCommand("make build\nmake test") == "make build …")
        #expect(ToolReading.describeCommand("make build\r\nmake test") == "make build\r …") // as JS splits: the \r stays
    }

    @Test func foldReplacesAppendsAndReplacesOutput() {
        var t = ToolCall(id: "x", toolCallId: "t1")
        t.fold(ToolCallUpdate(json: j(#"{"id":"t1","title":"npm test","kind":"execute","status":"pending","name":"Bash","rawInput":{"command":"npm test"}}"#))!)
        t.fold(ToolCallUpdate(json: j(#"{"id":"t1","status":"in_progress","outputDelta":"a"}"#))!)
        t.fold(ToolCallUpdate(json: j(#"{"id":"t1","outputDelta":"b"}"#))!)
        #expect(t.output == "ab")
        t.fold(ToolCallUpdate(json: j(#"{"id":"t1","status":"completed","output":"whole","exitCode":1}"#))!)
        #expect(t.output == "whole" && t.exitCode == 1 && t.status == .completed && t.name == "Bash" && t.kind == .execute)
        #expect(t.command == "npm test")
        #expect(t.failedExit == 1)
    }

    @Test func headlineOrder() {
        func tool(_ s: String) -> ToolCall {
            var t = ToolCall(id: "x", toolCallId: "t")
            t.fold(ToolCallUpdate(json: j(s))!)
            return t
        }
        var t = tool(#"{"id":"t","kind":"execute","title":"python3 - <<'EOF'\nopen('x.py')\nEOF","rawInput":{"command":"python3 - <<'EOF'\nopen('x.py')\nEOF"}}"#)
        #expect(t.headline == "Python script (1 line) → x.py")
        t.fold(ToolCallUpdate(id: "t", label: "Rewrite the handler"))
        #expect(t.headline == "Rewrite the handler")
        #expect(tool(#"{"id":"d","kind":"execute","title":"ls","rawInput":{"command":"ls","description":"List files"}}"#).headline == "List files")
        #expect(tool(#"{"id":"g","kind":"execute","status":"pending","title":"ls -la","content":[{"type":"content","content":{"type":"text","text":"[in /w] (List the files)"}}]}"#).headline == "[in /w] (List the files)")
        #expect(tool(#"{"id":"r","kind":"read","title":"Read src/main.go\n(lines 1-40)"}"#).headline == "Read src/main.go …")
        #expect(tool(#"{"id":"n","kind":"other"}"#).headline == "t")
    }

    @Test func planApproval() {
        let claude = j(##"{"kind":"switch_mode","title":"Approve Plan","rawInput":{"plan":"# P","planFilePath":"/p.md"},"content":[{"type":"content","content":{"type":"text","text":"# P\n\n1. x"}}]}"##)
        let ref = ToolCallRef(json: claude)!
        #expect(ToolReading.isPlanApproval(kind: ref.kind, planReview: false, rawInput: ref.rawInput))
        #expect(ToolReading.planText(content: ref.content, rawInput: ref.rawInput) == "# P\n\n1. x")
        #expect(ToolReading.planText(content: [], rawInput: j(###"{"plan":"## Codex plan"}"###)) == "## Codex plan")
        #expect(ToolReading.isPlanApproval(kind: "other", planReview: false, rawInput: j(#"{"plan":"x"}"#)))
        var c = ToolCall(id: "x", toolCallId: "c")
        c.fold(ToolCallUpdate(id: "c", planReview: true))
        #expect(c.isPlanApproval)
        #expect(!ToolReading.isPlanApproval(kind: "execute", planReview: false, rawInput: j(#"{"command":"ls"}"#)))
    }

    @Test func stripAnsi() {
        #expect(stripANSI("\u{1B}[31mred\u{1B}[0m ok\u{1B}[2K") == "red ok")
    }

    @Test func askUserQuestionForm() {
        let schema = j(#"""
        {"type":"object","properties":{
          "question_0":{"type":"string","title":"DB","description":"Which database?","oneOf":[{"const":"Postgres","title":"Postgres","description":"default"},{"const":"SQLite","title":"SQLite"}]},
          "question_0_custom":{"type":"string","title":"Other","description":"Type your own","_meta":{"_askUserQuestionCustomAnswer":{"questionId":"question_0","isCustomAnswer":true}}},
          "question_1":{"type":"array","title":"Extras","items":{"anyOf":[{"const":"Metrics","title":"Metrics"},{"const":"Tracing","title":"Tracing"}]}},
          "question_1_custom":{"type":"string","title":"Other","description":"Type your own","_meta":{"_askUserQuestionCustomAnswer":{"questionId":"question_1","isCustomAnswer":true}}},
          "port":{"type":"integer","title":"Port"},"tls":{"type":"boolean","title":"TLS"},"name":{"type":"string","title":"Name"},
          "mode":{"type":"string","enum":["a","b"]}},"required":["name"]}
        """#)
        let f = FormField.fields(schema: schema)
        #expect(f.map { "\($0.key):\($0.kind.rawValue):\($0.other ?? "-")" } ==
            ["question_0:radio:question_0_custom", "question_1:check:question_1_custom", "port:number:-", "tls:bool:-", "name:text:-", "mode:radio:-"])
        #expect(f[0].options[0] == FormOption(value: "Postgres", title: "Postgres", description: "default"))
        #expect(f[5].options.map(\.value) == ["a", "b"])
        let c = FormField.content(f, values: ["question_0": "SQLite", "question_0_custom": "  ", "question_1": ["Metrics"],
                                              "question_1_custom": " Admin UI ", "port": "8080", "tls": false, "name": ""])
        #expect(c == j(#"{"question_0":"SQLite","question_1":["Metrics"],"question_1_custom":"Admin UI","port":8080,"tls":false}"#))
        #expect(FormField.missingRequired(f, content: c) == ["Name"])
        #expect(FormField.fields(schema: nil).isEmpty)
        #expect(f[1].answer(in: c) == "Metrics — Admin UI")
        #expect(f[0].answerLabel == "DB")
    }
}
