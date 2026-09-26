import Foundation
import Testing
@testable import XbinCore

@Suite struct BridgeTests {
    @Test func designMountExample() throws {
        // plans/native.md §9, verbatim.
        let m = try BridgeMessage(parsing: #"{"op":"mount","v":1,"root":{"k":"r","t":"screen","p":{"title":"Counter","style":"form"},"c":[]}}"#)
        #expect(m == .mount(version: 1, root: Node(key: "r", type: "screen", props: ["title": "Counter", "style": "form"])))
        #expect(m.op == "mount")
    }

    @Test func mountWithoutVersionIsV1() throws {
        #expect(try BridgeMessage(parsing: #"{"op":"mount","root":{"k":"r","t":"text"}}"#) == .mount(version: 1, root: Node(key: "r", type: "text")))
        #expect(try BridgeMessage(parsing: #"{"op":"mount","v":2,"root":{"k":"r","t":"text"}}"#) == .mount(version: 2, root: Node(key: "r", type: "text")))
    }

    @Test func otherMessages() throws {
        #expect(try BridgeMessage(parsing: #"{"op":"meta","title":"Today","badge":3}"#)
            == .meta(NativeMeta(fields: ["title": "Today", "badge": 3])))
        let err = try BridgeMessage(parsing: #"{"op":"error","kind":"module","message":"x is not defined","where":"native.js:12:3"}"#)
        guard case .error(let e) = err else { Issue.record("not an error"); return }
        #expect(e.kind == "module" && e.message == "x is not defined" && e.location == "native.js:12:3")
        let call = try BridgeMessage(parsing: #"{"op":"call","id":7,"what":"copy","args":["hello"]}"#)
        #expect(call == .call(BridgeCall(id: 7, what: "copy", args: ["hello"])))
        let future = try BridgeMessage(parsing: #"{"op":"haptic","style":"light"}"#)
        #expect(future == .unknown(op: "haptic", body: ["op": "haptic", "style": "light"]))
    }

    @Test func runtimeMessageShapes() throws {
        // What web/xb/rt-runtime.js posts.
        #expect(try BridgeMessage(parsing: #"{"op":"mount","v":1,"n":1,"root":{"k":"r","t":"text"}}"#)
            == .mount(version: 1, root: Node(key: "r", type: "text"), n: 1))
        #expect(try BridgeMessage(parsing: #"{"op":"patch","n":7,"ops":[["events","r",["tap"]]]}"#)
            == .patch([.events(key: "r", events: ["tap"])], n: 7))
        #expect(try BridgeMessage(parsing: #"{"op":"diag","level":"warn","code":"unknown-prop","message":"row has no prop x","where":"native.js:3"}"#)
            == .diag(RuntimeDiagnostic(fields: ["level": "warn", "code": "unknown-prop", "message": "row has no prop x", "where": "native.js:3"])))
        #expect(try BridgeMessage(parsing: #"{"op":"state","state":{"tab":"keys"}}"#) == .state(["tab": "keys"]))
        #expect(try BridgeMessage(parsing: #"{"op":"state"}"#) == .state(.null))
        #expect(try BridgeMessage(parsing: #"{"op":"call","id":"c1","what":"copy","args":{"text":"hi"}}"#)
            == .call(BridgeCall(id: "c1", what: "copy", args: ["text": "hi"])))
        let err = try BridgeMessage(parsing: #"{"op":"error","kind":"module","message":"x","where":"","stack":"at y"}"#)
        guard case .error(let e) = err else { Issue.record("not an error"); return }
        #expect(e.isFatal && e.location == nil && e.fields["stack"] == "at y")
        #expect(throws: BridgeDecodingError.self) { try BridgeMessage(parsing: #"{"op":"patch","n":"2","ops":[]}"#) }
    }

    @Test func wireRoundTrip() throws {
        let messages: [BridgeMessage] = [
            .mount(version: 1, root: try Resources.tree("18.3-egress-approver").root),
            .patch([.set(key: "r.0", props: ["subtitle": "3 clients"]), .remove(key: "r.0.1")]),
            .meta(NativeMeta(fields: ["title": "x", "icon": "bolt"])),
            .error(RuntimeError(kind: "render", message: "timeout", where: "native.js")),
            .call(BridgeCall(id: "c1", what: "share", args: [["text": "hi", "url": "https://x"]])),
            .mount(version: 1, root: Node(key: "r", type: "text"), n: 3),
            .patch([.events(key: "r", events: [])], n: 4),
            .diag(RuntimeDiagnostic(fields: ["level": "info", "code": "c", "message": "m"])),
            .state(["a": [1, 2.5]]),
            .unknown(op: "later", body: ["op": "later", "n": 1]),
        ]
        for m in messages {
            #expect(try BridgeMessage(json: m.json) == m)
            #expect(try BridgeMessage(parsing: m.json.jsonString) == m)
        }
    }

    @Test func messageBodies() throws {
        let text = #"{"op":"patch","ops":[["set","r",{"busy":true,"n":1}]]}"#
        let expected = BridgeMessage.patch([.set(key: "r", props: ["busy": true, "n": 1])])
        #expect(try BridgeMessage(body: text) == expected)
        #expect(try BridgeMessage(body: Data(text.utf8)) == expected)
        // A Foundation object (what WebKit hands over for a posted object).
        let dict: [String: Any] = ["op": "meta", "title": "T"]
        #expect(try BridgeMessage(body: dict) == .meta(NativeMeta(fields: ["title": "T"])))
        #expect(throws: BridgeDecodingError.self) { try BridgeMessage(body: 42) }
    }

    @Test(arguments: [
        #"[]"#, #"{}"#, #"{"op":1}"#, #"{"op":"mount"}"#, #"{"op":"mount","root":{"k":"r"}}"#,
        #"{"op":"mount","v":"1","root":{"k":"r","t":"x"}}"#, #"{"op":"patch"}"#, #"{"op":"patch","ops":{}}"#,
        #"{"op":"patch","ops":[["nope"]]}"#, #"{"op":"call","what":"copy"}"#, #"{"op":"call","id":1}"#,
    ])
    func badMessages(_ text: String) {
        #expect(throws: BridgeDecodingError.self) { try BridgeMessage(parsing: text) }
    }

    @Test func metaMerging() {
        let a = NativeMeta(fields: ["title": "A", "badge": 2])
        #expect(a.title == "A" && a.badge == "2" && a.icon == nil)
        let b = a.merging(NativeMeta(fields: ["badge": .null, "icon": "bolt"]))
        #expect(b.fields == ["title": "A", "icon": "bolt"])
        #expect(NativeMeta(fields: ["badge": "new"]).badge == "new")
        #expect(NativeMeta(fields: ["badge": 2.5]).badge == "2.5")
        #expect(NativeMeta(fields: ["badge": 3.0]).badge == "3")
        #expect(NativeMeta(fields: ["op": "meta", "title": "x"]).fields == ["title": "x"])
    }

    @Test func runtimeErrorLocation() {
        #expect(RuntimeError(fields: ["where": ["line": 3]]).location == #"{"line":3}"#)
        #expect(RuntimeError(fields: ["where": .null]).location == nil)
        #expect(RuntimeError(fields: [:]).kind == "")
    }

    @Test func callArguments() {
        let positional = BridgeCall(id: 1, what: "copy", args: ["text to copy"])
        #expect(positional.stringArgument("text") == "text to copy")
        let named = BridgeCall(id: 2, what: "open", args: ["url": "https://example.com"])
        #expect(named.stringArgument("url") == "https://example.com")
        let share = BridgeCall(id: 3, what: "share", args: [["text": "t", "file": "out.pdf"]])
        #expect(share.argument("file") == "out.pdf" && share.stringArgument("text") == "t")
        #expect(BridgeCall(id: 4, what: "x").firstArgument == .null)
    }

    // MARK: - App → runtime

    @Test func runtimeCallStrings() {
        #expect(RuntimeCall.event(key: "r.0.1", type: "tap", payload: [:]).javaScript == #"xbn.event("r.0.1","tap",{})"#)
        #expect(RuntimeCall.event(key: "r.2.1", type: "input", payload: ["value": "hi"]).javaScript
            == #"xbn.event("r.2.1","input",{"value":"hi"})"#)
        #expect(RuntimeCall.visibility(.hidden).javaScript == #"xbn.visibility("hidden")"#)
        #expect(RuntimeCall.visibility(.visible).javaScript == #"xbn.visibility("visible")"#)
        #expect(RuntimeCall.resolve(id: 7, value: true).javaScript == "xbn.resolve(7,true)")
        #expect(RuntimeCall.resolve(id: "c1", value: .null).javaScript == #"xbn.resolve("c1",null)"#)
        #expect(RuntimeCall.frame.javaScript == "xbn.frame()")
        #expect(RuntimeCall.event(key: "r.0", type: "input", payload: ["value": "a"], n: 12).javaScript
            == #"xbn.event("r.0","input",{"value":"a"},12)"#)
        #expect(RuntimeCall.resolve(id: "c2", value: .null, error: "no https").javaScript == #"xbn.resolve("c2",null,"no https")"#)
        #expect(RuntimeCall.frame.functionBody == "return xbn.frame();")
    }

    @Test func runtimeCallEscaping() {
        // Keys (which may hold user data: IPs, URLs) and payloads can't break out.
        let hostile = "a\"),alert(1),(\"\\\n\u{2028}\u{2029}</script>😀"
        let js = RuntimeCall.event(key: hostile, type: "submit", payload: ["value": .string(hostile)]).javaScript
        let enc = #"a\"),alert(1),(\"\\\n"# + "\\u2028\\u2029" + "</script>😀"
        #expect(js == "xbn.event(\"\(enc)\",\"submit\",{\"value\":\"\(enc)\"})")
        #expect(!js.unicodeScalars.contains { $0.value < 0x20 || $0 == "\u{2028}" || $0 == "\u{2029}" })
    }

    /// Every argument of a built call parses back to what went in.
    @Test func randomRuntimeCallsParseBack() throws {
        var r = SeededRNG(seed: 0xB1D6E)
        for _ in 0..<500 {
            let key = Gen.string(&r, max: 12), type = Gen.string(&r, max: 6), payload = Gen.value(&r)
            let js = RuntimeCall.event(key: key, type: type, payload: payload).javaScript
            #expect(js.hasPrefix("xbn.event(") && js.hasSuffix(")"))
            // xbn.event(A,B,C): wrap the argument list as a JSON array and parse it.
            let args = try JSONValue(parsing: "[" + js.dropFirst("xbn.event(".count).dropLast() + "]")
            #expect(args == [.string(key), .string(type), payload])
        }
    }

    @Test func documentStartScript() throws {
        let caps = NativeCaps(renderer: "swiftui", app: "1.0", prims: ["screen": 1])
        let js = RuntimeScript.documentStart(caps: caps, state: ["note": "</script>"])
        #expect(js == #"window.xbin = {"native":{"caps":{"app":"1.0","features":[],"prims":{"screen":1},"renderer":"swiftui","v":1},"state":{"note":"\u003c/script\u003e"}}};"#)
        #expect(RuntimeScript.documentStart(caps: caps, state: nil).contains(#""state":null"#))
        // The injected value parses back.
        let literal = js.dropFirst("window.xbin = ".count).dropLast()
        #expect(try JSONValue(parsing: String(literal))["native"]?["state"] == ["note": "</script>"])
    }

    @Test func caps() throws {
        let caps = NativeCaps(renderer: "swiftui", app: "1.0 (1)", prims: ["screen": 1, "chart": 2], features: ["markdown.tables", "chart.area"])
        #expect(caps.json == ["v": 1, "renderer": "swiftui", "app": "1.0 (1)", "prims": ["screen": 1, "chart": 2],
                              "features": ["chart.area", "markdown.tables"]])
        let back = try NativeCaps(json: caps.json)
        #expect(back.prims == caps.prims && back.renderer == "swiftui" && back.v == 1)
        #expect(caps.supports("chart", revision: 2) && !caps.supports("chart", revision: 3))
        #expect(caps.supports("screen") && caps.supports("chart.area") && !caps.supports("terminal"))
    }
}
