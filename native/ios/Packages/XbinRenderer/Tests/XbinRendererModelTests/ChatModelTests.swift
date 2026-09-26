import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

/// The chat view models built from the chat fixtures' nodes — what the
/// SwiftUI components draw.
@Suite struct ChatModelTests {
    func props(_ fixture: String, _ key: String) throws -> Props {
        Props(try #require(try fixtureSet().expected(fixture).root.find(key), "\(fixture) \(key)").props)
    }

    @Test func messages() throws {
        let m1 = ChatMessage(id: "m1", props: try props("chat-transcript", "r.0.1:m1"))
        #expect(m1.role == .user && m1.sender == "Ana" && m1.time == "18:02" && m1.markdown == nil)
        #expect(m1.files == [ChatFile(name: "latency.png", mime: "image/png", src: "api/apps/agent/files/f-31")] && m1.files[0].isImage)
        let m2 = ChatMessage(id: "m2", props: try props("chat-transcript", "r.0.1:m2.1"))
        #expect(m2.role == .assistant && m2.markdown?.count == 1 && !m2.streaming)
        let live = ChatMessage(id: "l", props: try props("chat-transcript", "r.0.2.1"))
        #expect(live.streaming && live.markdown?.count == 2)
        let q = ChatMessage(id: "q", props: try props("chat-transcript", "r.0.3:q1"))
        #expect(q.queued && q.role == .user)
        let sys = ChatMessage(id: "s", props: try props("chat-transcript", "r.0.1:s1"))
        #expect(sys.role == .system)
        #expect(ChatMessage(id: "x", props: Props(["role": "robot"])).role == .assistant)
        #expect(ChatFormat.time(.int(1789998800000), timeZone: TimeZone(identifier: "UTC")!) == "13:53")
        #expect(ChatFormat.time("") == nil && ChatFormat.time(nil) == nil)
    }

    @Test func thinking() throws {
        let t = ChatThinking(id: "t", props: try props("chat-transcript", "r.0.1:m2.0"))
        #expect(t.label == "Thought for 4s" && !t.live)
        #expect(ChatThinking(id: "t", props: try props("chat-transcript", "r.0.2.0")).label == "Thinking…")
        #expect(ChatThinking(id: "t", text: "", seconds: 65).label == "Thought for 1m 5s")
        #expect(ChatThinking(id: "t", text: "").label == "Thought")
    }

    @Test func toolCards() throws {
        let t2 = ChatToolCard(id: "t2", props: try props("chat-tools", "r.0.0:t2"))
        #expect(t2.title == "Edit internal/pay/client.go" && t2.icon == "pencil" && t2.family == "edit" && t2.state == .ok)
        #expect(t2.chips == [ChatChip(text: "+2", tone: .ok), ChatChip(text: "−1", tone: .danger)])
        let states = try ["t1", "t4", "t5", "t6", "t7"].map { ChatToolCard(id: $0, props: try props("chat-tools", "r.0.0:\($0)")).state }
        #expect(states == [.ok, .error, .canceled, .running, .writing])
        #expect(ToolState.running.isActive && !ToolState.ok.isActive)
    }

    @Test func approvals() throws {
        let a1 = ChatApproval(id: "a1", props: try props("chat-tools", "r.0.0:a1"))
        #expect(a1.options.map(\.id) == ["once", "always", "no"] && a1.feedback)
        #expect([0, 1, 2].map(a1.emphasis) == [.prominent, .standard, .destructive])
        #expect(a1.settledText == "Allow once · answered by Ana")
        let a2 = ChatApproval(id: "a2", props: try props("chat-tools", "r.0.0:a2"))
        #expect(a2.settled == nil && a2.settledText == nil && a2.note == "Restarts 3 replicas.")
        #expect([0, 1].map(a2.emphasis) == [.prominent, .destructive])
        // Only "always" options: the first is prominent.
        let always = ChatApproval(id: "x", title: "t", options: [ApprovalOption(id: "a", label: "A", kind: "allow_always")])
        #expect(always.emphasis(0) == .prominent && always.emphasis(5) == .standard)
        #expect(ChatApproval(id: "x", props: Props([:])).title == "Permission needed")
    }

    @Test func questions() throws {
        let q1 = ChatQuestion(id: "q1", props: try props("chat-tools", "r.0.0:q1"))
        #expect(q1.settled && q1.title == "Which staging cluster?")
        #expect(q1.form.fields.map(\.name) == ["cluster", "notify"])
        #expect(q1.form.fields[0].kind == .choice([.init(value: "staging-eu", label: "staging-eu"), .init(value: "staging-us", label: "staging-us")]))
        #expect(q1.form.fields[0].required && q1.form.fields[1].kind == .boolean && q1.form.fields[1].title == "Notify #deploys")
        #expect(q1.initialValues == ["cluster": "staging-eu", "notify": true])
        let y2 = ChatQuestion(id: "y2", props: try props("chat-tools", "r.0.0:t8.0:0.0:y2"))
        #expect(!y2.settled && y2.form.fields.map(\.name) == ["audience", "highlights"])
        #expect(y2.form.content([:]) == .failure(.init(titles: ["Audience"])))
        #expect(QuestionForm.Missing(titles: ["Audience"]).message == "Required: Audience")
        #expect(y2.form.content(["audience": "internal", "highlights": ""]) == .success(["audience": "internal"]))
    }

    @Test func questionFieldKinds() throws {
        let form = QuestionForm(schema: try JSONValue(parsing: """
        {"type":"object","title":"T","description":"D","x-order":["n","i","c","e"],
         "properties":{
           "e":{"type":"string","format":"email","default":"a@b.c"},
           "n":{"type":"number","title":"N"},
           "i":{"type":"integer","description":"whole"},
           "c":{"type":"string","oneOf":[{"const":"x","title":"Ex"},{"const":"y"}]},
           "z":{"type":"boolean","default":false},
           "names":{"type":"string","enum":["a","b"],"enumNames":["Alpha","Beta"]}},
         "required":["i"]}
        """))
        #expect(form.title == "T" && form.description == "D")
        // x-order first, then required, then by name.
        #expect(form.fields.map(\.name) == ["n", "i", "c", "e", "names", "z"])
        #expect(form.fields[0].kind == .number(integer: false) && form.fields[1].kind == .number(integer: true))
        #expect(form.fields[2].kind == .choice([.init(value: "x", label: "Ex"), .init(value: "y", label: "y")]))
        #expect(form.fields[3].kind == .text(format: "email"))
        #expect(form.fields[4].kind == .choice([.init(value: "a", label: "Alpha"), .init(value: "b", label: "Beta")]))
        #expect(form.defaults == ["e": "a@b.c", "z": false])
        #expect(form.content(["i": "42", "n": "2.5", "z": false]) == .success(["i": 42, "n": 2.5, "z": false]))
        #expect(form.content(["i": "4.0"]) == .success(["i": 4]))
        #expect(form.content(["i": "lots"]) == .success(["i": "lots"]))
        #expect(QuestionForm(schema: nil).fields.isEmpty)
    }

    @Test func planDiffActivityStep() throws {
        let plan = ChatPlan(props: try props("chat-tools", "r.0.0:e3"))
        #expect(plan.entries.map(\.status) == [.completed, .completed, .inProgress, .pending] && plan.summary == "2 of 4 done")
        let diff = ChatDiff(props: try props("chat-tools", "r.0.0:d1"))
        #expect(diff.files.map(\.letter) == ["M", "A", "D", "M"] && diff.files[1].added == 38 && diff.files[2].removed == 57)
        #expect(diff.lines.map(\.kind) == [.file, .file, .hunk, .context, .removed, .added, .added, .context])
        #expect(ChatDiff.File(path: "p", status: "renamed").letter == "R" && ChatDiff.File(path: "p", status: "copied").letter == "C")
        let act = ChatActivity(props: try props("chat-transcript", "r.0.2.2"))
        #expect(act.live && act.text == "Comparing configs…")
        let step = ChatStep(props: try props("chat-transcript", "r.0.1:st3"))
        #expect(step.glyph == "✗" && step.tone == .danger)
        #expect(ChatStep(props: Props([:])).glyph == "•")
    }

    @Test func composer() throws {
        let c = ChatComposer(props: try props("chat-transcript", "r.1"))
        #expect(c.busy && c.canAttach && c.accept == "image/*,.pdf,.txt,.csv" && c.placeholder == "Message Ops agent")
        #expect(c.attachments == [Attachment(id: "f-52", name: "staging.log", mime: "text/plain", progress: 1)])
        #expect(!c.attachments[0].isUploading && Attachment(id: "a", name: "a", progress: 0.4).isUploading)
        #expect(c.slashMatches("/").map(\.bare) == ["deploy", "logs", "compact"])
        #expect(c.slashMatches("/d").map(\.name) == ["/deploy"] && c.slashMatches("/deploy x").isEmpty && c.slashMatches("d").isEmpty)
        #expect(c.canSend(" hi ") && !c.canSend("  "))
        let off = ChatComposer(props: try props("chat-tools", "r.1"))
        #expect(off.disabled && !off.canSend("x") && !off.canAttach && off.placeholder == "Answer the approval above to continue")
    }
}

@Suite struct ValueTests {
    @Test func pickerOptions() throws {
        let root = try fixtureSet().expected("controls").root
        let keep = PickerOption.list(Props(try #require(root.find("r.1.3")).props))
        #expect(keep.map(\.value) == [7, 30, 90, 365] && keep[2].label == "90 days" && keep[0].icon == "archive")
        let p = Props(try JSONValue(parsing: #"{"options":[{"value":"a"},{"label":"no value"},3,{"value":true,"label":"Yes"}]}"#).objectValue!)
        #expect(PickerOption.list(p) == [PickerOption(value: "a", label: "a"), PickerOption(value: true, label: "Yes")])
    }

    @Test func dataURLs() throws {
        let sig = try #require(try fixtureSet().expected("media").root.find("r.0.11.2")?.props["src"]?.stringValue)
        let png = try #require(DataURL.decode(sig))
        #expect(png.starts(with: [0x89, 0x50, 0x4E, 0x47]) && DataURL.mime(sig) == "image/png")
        #expect(DataURL.decode("data:text/plain,a%20b") == Data("a b".utf8))
        #expect(DataURL.decode("https://x/y.png") == nil && DataURL.decode("data:image/png;base64") == nil)
        let big = "data:image/png;base64," + Data(count: DataURL.maxBytes + 1).base64EncodedString()
        #expect(DataURL.decode(big) == nil)
    }

    @MainActor @Test func rendererValuesOnlyForUnboundProps() throws {
        let (_, model, sent) = mounted(try Node(parsing: #"{"k":"r","t":"fragment","c":[{"k":"a","t":"composer","e":["send"]},{"k":"b","t":"composer","p":{"value":"x"}}]}"#))
        model.emit("a", "input", ["value": "hi"])
        #expect(model.node("a")?.value("value") == "hi" && sent.calls.isEmpty)
        model.setRendererValue("a", "value", "")
        model.setRendererValue("b", "value", "")
        #expect(model.node("a")?.value("value") == "" && model.node("b")?.value("value") == "x" && sent.calls.isEmpty)
    }
}
