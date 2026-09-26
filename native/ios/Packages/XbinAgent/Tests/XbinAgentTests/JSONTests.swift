import Foundation
import Testing
@testable import XbinAgent

@Suite struct JSONTests {
    @Test func parsesAndKeepsKeyOrder() throws {
        let v = j(#"{"z":1,"a":[true,false,null,"s",-1.5e2],"m":{"k":"v"}}"#)
        #expect(v.object?.keys == ["z", "a", "m"])
        #expect(v["a"]?.array?.count == 5)
        #expect(v["a"]?.array?[4].double == -150)
        #expect(v.compactString == #"{"z":1,"a":[true,false,null,"s",-150],"m":{"k":"v"}}"#)
        // equality ignores order, not presence
        #expect(j(#"{"a":1,"b":2}"#) == j(#"{"b":2,"a":1}"#))
        #expect(j(#"{"a":1}"#) != j(#"{"a":1,"b":null}"#))
        // a duplicate key: the last value wins, in the first one's place (JSON.parse)
        #expect(j(#"{"a":1,"b":2,"a":3}"#).compactString == #"{"a":3,"b":2}"#)
    }

    @Test func booleansAreNotNumbers() {
        #expect(j("true") != j("1"))
        #expect(j("true").int == nil)
        #expect(j("1").bool == nil)
        #expect(j("0").truthy == false && j("\"\"").truthy == false && j("[]").truthy == true)
    }

    @Test func strings() throws {
        #expect(j(#""a\"b\\c\/d\n\t\u00e9\ud83d\ude00""#).string == "a\"b\\c/d\n\té😀")
        #expect(j(#""\ud800x""#).string == "\u{FFFD}x") // a lone surrogate
        #expect(JSONValue.string("\u{1}\"\n").compactString == #""\u0001\"\n""#)
        #expect(j(#""héllo""#).string == "héllo")
    }

    @Test func numbers() {
        #expect(JSONValue.number(1).compactString == "1")
        #expect(JSONValue.number(1.5).compactString == "1.5")
        #expect(j("1790403290742").int64 == 1_790_403_290_742)
        #expect(j("3.5").int == nil)
        #expect(j("-2").uint64 == nil)
    }

    @Test(arguments: ["", "{", "[1,]", "{\"a\" 1}", "01", "tru", "\"\u{1}\"", "1 2", "{\"a\":1,}", "\"\\x\""])
    func rejectsMalformed(_ s: String) {
        #expect(throws: JSONParseError.self) { try JSONValue.parse(s) }
    }

    @Test func pretty() {
        #expect(j(#"{"a":[1,{"b":null}],"c":{}}"#).prettyString(indent: 1) == "{\n \"a\": [\n  1,\n  {\n   \"b\": null\n  }\n ],\n \"c\": {}\n}")
    }

    @Test func codableRoundTrip() throws {
        let e = try events("basic")[1]
        let data = try JSONEncoder().encode(e)
        let back = try JSONDecoder().decode(AgentEvent.self, from: data)
        #expect(back == e)
        #expect(back.payload == e.payload)
        let s = try #require(SessionInfo(json: try fixture("basic")["session"]!))
        #expect(try JSONDecoder().decode(SessionInfo.self, from: JSONEncoder().encode(s)) == s)
        #expect(try JSONDecoder().decode(JSONValue.self, from: Data("[1,true,\"x\",null]".utf8)) == j("[1,true,\"x\",null]"))
    }

    @Test func ndjsonLines() {
        var l = NDJSONLines()
        #expect(l.feed(Data(#"{"seq":1,"type":"a"}"#.utf8)).isEmpty) // no newline yet
        let got = l.feed(Data("\r\n\n{\"seq\":2,\"ty".utf8))
        #expect(got.map { $0["seq"]?.int } == [1])
        let more = l.feed(Data("pe\":\"b\"}\nnot json\n   \n".utf8))
        #expect(more.map { $0["seq"]?.int } == [2])
        #expect(l.skipped == 1)
        #expect(l.feed(Data(#"{"seq":3,"type":"c"}"#.utf8)).isEmpty)
        #expect(l.finish().map { $0["seq"]?.int } == [3])
    }
}
