import Foundation
import Testing
@testable import XbinCore

@Suite struct JSONValueTests {
    @Test func scalarsKeepTheirKinds() throws {
        #expect(try JSONValue(parsing: "true") == .bool(true))
        #expect(try JSONValue(parsing: "false") == .bool(false))
        #expect(try JSONValue(parsing: "null") == .null)
        #expect(try JSONValue(parsing: "1") == .int(1))
        #expect(try JSONValue(parsing: "-0") == .int(0))
        #expect(try JSONValue(parsing: "1.0") == .double(1.0))
        #expect(try JSONValue(parsing: "1e3") == .double(1000))
        #expect(try JSONValue(parsing: "1E-2") == .double(0.01))
        #expect(try JSONValue(parsing: "9223372036854775807") == .int(.max))
        #expect(try JSONValue(parsing: "-9223372036854775808") == .int(.min))
        #expect(try JSONValue(parsing: "9223372036854775808") == .double(9_223_372_036_854_775_808))
        #expect(try JSONValue(parsing: " \t\r\n\"x\" \n") == .string("x"))
    }

    @Test func booleansAreNotNumbers() throws {
        let v = try JSONValue(parsing: #"[true, 1, 0, false, 1.0]"#)
        #expect(v == [.bool(true), .int(1), .int(0), .bool(false), .double(1)])
        #expect(JSONValue.bool(true) != .int(1))
        #expect(JSONValue.int(1) != .double(1))
        #expect(v[0]?.boolValue == true && v[1]?.boolValue == nil)
        #expect(v[1]?.intValue == 1 && v[4]?.intValue == 1 && v[0]?.intValue == nil)
        #expect(JSONValue.double(1.5).intValue == nil)
        #expect(v[4]?.doubleValue == 1.0 && v[1]?.doubleValue == 1.0)
    }

    @Test func strings() throws {
        #expect(try JSONValue(parsing: #""a\"b\\c\/d\b\f\n\r\t""#) == .string("a\"b\\c/d\u{08}\u{0C}\n\r\t"))
        #expect(try JSONValue(parsing: #""\u00e9\u4E2D""#) == .string("é中"))
        #expect(try JSONValue(parsing: #""\ud83d\ude00""#) == .string("😀"))
        #expect(try JSONValue(parsing: "\"é中😀\"") == .string("é中😀"))
        // Lone surrogates (JSON.stringify emits them escaped) become U+FFFD.
        #expect(try JSONValue(parsing: #""a\ud800b""#) == .string("a\u{FFFD}b"))
        #expect(try JSONValue(parsing: #""\udc00""#) == .string("\u{FFFD}"))
        #expect(try JSONValue(parsing: #""\ud800\u0041""#) == .string("\u{FFFD}A"))
        #expect(try JSONValue(parsing: #""\ud800\ud83d\ude00""#) == .string("\u{FFFD}😀"))
    }

    @Test func objectsAndArrays() throws {
        let v = try JSONValue(parsing: #"{"a": [1, {"b": null}], "c": {}, "d": [], "a2": "x"}"#)
        #expect(v["a"]?[1]?["b"] == .null)
        #expect(v["c"] == .object([:]) && v["d"] == .array([]))
        #expect(v["missing"] == nil)
        // Presence matters: {"x": null} is not {}.
        #expect(try JSONValue(parsing: #"{"x":null}"#) != JSONValue.object([:]))
        // Duplicate keys: the last wins, as in JavaScript.
        #expect(try JSONValue(parsing: #"{"k":1,"k":2}"#) == ["k": 2])
    }

    @Test(arguments: [
        "", " ", "[1,]", "{\"a\":1,}", "01", "-", "1.", ".5", "1e", "+1", "NaN", "Infinity", "-Infinity",
        "tru", "nul", "[1 2]", "{\"a\" 1}", "{a:1}", "'x'", "\"abc", "\"\\x\"", "\"\\u12G4\"", "\"\\u12\"",
        "\"a\u{01}b\"", "\"tab\there\"", "1 2", "[] x", "// c\n1", "/* c */1", "1e400", "-1e400", "[", "{",
    ])
    func rejects(_ text: String) {
        #expect(throws: JSONParseError.self) { try JSONValue(parsing: text) }
    }

    @Test func rejectsInvalidUTF8() {
        #expect(throws: JSONParseError.self) { try JSONValue(parsing: [0x22, 0xC3, 0x28, 0x22] as [UInt8]) }
        #expect(throws: JSONParseError.self) { try JSONValue(parsing: Data([0x22, 0xFF, 0x22])) }
        #expect(throws: JSONParseError.self) { try JSONValue(parsing: [0x22, 0x5C, 0x6E, 0xED, 0xA0, 0x80, 0x22] as [UInt8]) }
    }

    @Test func depthLimit() throws {
        let ok = String(repeating: "[", count: JSONValue.maxParseDepth) + String(repeating: "]", count: JSONValue.maxParseDepth)
        #expect(throws: Never.self) { try JSONValue(parsing: ok) }
        let deep = String(repeating: "[", count: JSONValue.maxParseDepth + 1) + String(repeating: "]", count: JSONValue.maxParseDepth + 1)
        #expect(throws: JSONParseError.self) { try JSONValue(parsing: deep) }
        let hostile = String(repeating: "{\"a\":", count: 100_000)
        #expect(throws: JSONParseError.self) { try JSONValue(parsing: hostile) }
    }

    @Test func canonicalEncoding() throws {
        let v: JSONValue = ["b": [1, 2.5, true, .null], "a": "x", "B": 1.0, "é": 0.1, "_": -0.0]
        // Keys in UTF-8 byte order ("B" < "_" < "a" < "b" < "é"), no whitespace.
        #expect(v.jsonString == #"{"B":1.0,"_":-0.0,"a":"x","b":[1,2.5,true,null],"é":0.1}"#)
        #expect(JSONValue.double(1e16).jsonString == "1e+16")
        #expect(JSONValue.double(1e-7).jsonString == "1e-07")
        #expect(JSONValue.double(1_790_000_000_000).jsonString == "1790000000000.0")
        #expect(JSONValue.int(1_790_000_000_000).jsonString == "1790000000000")
        #expect(JSONValue.double(.nan).jsonString == "null")
        #expect(JSONValue.double(.infinity).jsonString == "null")
        #expect(JSONValue.string("q\"b\\n\n\u{01}\u{7F}\u{2028}\u{2029}é😀").jsonString
            == #""q\"b\\n\n\u0001\u007f\u2028\u2029é😀""#)
        #expect(JSONValue.string("</script><!--&").jsLiteral(htmlSafe: true) == #""\u003c/script\u003e\u003c!--\u0026""#)
        #expect(JSONValue.string("</script>").jsLiteral() == #""</script>""#)
        // Same value, same bytes, however it was built.
        var o1: [String: JSONValue] = [:]
        var o2: [String: JSONValue] = [:]
        for i in 0..<50 { o1["k\(i)"] = .int(Int64(i)) }
        for i in (0..<50).reversed() { o2["k\(i)"] = .int(Int64(i)) }
        #expect(JSONValue.object(o1).jsonString == JSONValue.object(o2).jsonString)
    }

    @Test func randomRoundTrips() throws {
        var r = SeededRNG(seed: 0x5EED_0001)
        for _ in 0..<2000 {
            let v = Gen.value(&r)
            let text = v.jsonString
            let back = try JSONValue(parsing: text)
            #expect(back == v, "\(text)")
            #expect(back.jsonString == text)
            #expect(try JSONValue(parsing: v.prettyJSONString) == v)
            #expect(try JSONValue(parsing: v.jsonData) == v)
            // JS-safe: no raw line/paragraph separators or control characters.
            #expect(!text.unicodeScalars.contains { $0 == "\u{2028}" || $0 == "\u{2029}" || $0.value < 0x20 })
        }
    }

    @Test func codableKeepsBooleans() throws {
        struct Box: Codable, Equatable { var v: JSONValue }
        let box = Box(v: ["flag": true, "n": 3, "x": 2.5, "s": "t", "nil": .null, "a": [false]])
        let data = try JSONEncoder().encode(box)
        let back = try JSONDecoder().decode(Box.self, from: data)
        #expect(back == box)
        #expect(try JSONValue(parsing: data)["v"] == box.v)
    }

    @Test func literalsAndAccessors() {
        let v: JSONValue = ["s": "x", "i": 3, "d": 0.5, "b": false, "n": .null, "a": [1], "o": ["k": "v"]]
        #expect(v["s"]?.stringValue == "x")
        #expect(v["i"]?.isNumber == true && v["s"]?.isNumber == false)
        #expect(v["n"]?.isNull == true)
        #expect(v["a"]?.arrayValue == [1])
        #expect(v["o"]?.objectValue == ["k": "v"])
        #expect(v["a"]?[5] == nil && v["s"]?[0] == nil && v["s"]?["k"] == nil)
        #expect(v.kindName == "object" && JSONValue.int(1).kindName == "integer")
        #expect(v.description == v.jsonString)
    }
}
