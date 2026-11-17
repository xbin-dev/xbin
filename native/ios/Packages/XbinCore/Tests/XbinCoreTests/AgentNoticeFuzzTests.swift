import Foundation
import Testing
@testable import XbinCore

/// Review finding 3: a paired xbind is not trusted to send sane numbers. A
/// badge of Int64.max crashed the extension, and the count it stored then
/// crashed the app's badge for every workspace. Every count is held to
/// 0…9999 where it is read and where it is stored, sums saturate, and no
/// payload — wrong types, negative or huge numbers, long arrays, deep
/// nesting — traps the parser or the drawing.
@Suite struct AgentNoticeFuzzTests {
    @Test func hugeAndNegativeCounts() throws {
        let huge = try JSONValue(parsing: Data(#"{"v":1,"badge":9223372036854775807,"changeset":{"id":"89abcdef0123456789abcdef","items":9223372036854775807,"warned":-9223372036854775808,"plan":true}}"#.utf8))
        let d = try #require(AgentNoticeData(json: huge))
        #expect(d.badge == 9999)
        #expect(d.changeset?.items == 9999 && d.changeset?.warned == 0)
        let negative = try JSONValue(parsing: Data(#"{"v":1,"badge":-5}"#.utf8))
        #expect(AgentNoticeData(json: negative)?.badge == 0)
        // a stored count from an older build, or a hostile one, sums without trapping
        #expect(AgentBadge.total(["a": Int.max, "b": Int.max, "c": -1]) == 9999)
        #expect(AgentBadge.total(["a": 3, "b": 4]) == 7)
        #expect(AgentBadge.clamp(Int64.min) == 0 && AgentBadge.clamp(Int64.max) == 9999)
    }

    @Test func generatedPayloads() {
        var rng = SplitMix(seed: 3)
        func value(_ depth: Int) -> JSONValue {
            switch rng.next() % (depth > 6 ? 5 : 8) {
            case 0: return .int(Int64(bitPattern: rng.next()))
            case 1: return .int([Int64.max, Int64.min, -1, 0, 1, 9999, 10000][Int(rng.next() % 7)])
            case 2: return .string(String(repeating: "x\u{2028}", count: Int(rng.next() % 300)))
            case 3: return .bool(rng.next() % 2 == 0)
            case 4: return .null
            case 5: return .array((0..<Int(rng.next() % 2000)).map { _ in .string("agent-approval:\(rng.next())") })
            case 6: return .object(["v": 1, "id": .string("0123456789abcdef01234567"), "x": value(depth + 1)])
            default: return .array([value(depth + 1), value(depth + 1)])
            }
        }
        let keys = ["badge", "request", "changeset", "conversation", "ask", "clear"]
        let reqKeys = ["id", "kind", "tile", "tileName", "expires", "offer", "method", "target", "contentType", "body", "bodySize", "bodySha256"]
        for i in 0..<4000 {
            var o: [String: JSONValue] = ["v": 1]
            for k in keys where rng.next() % 2 == 0 { o[k] = value(0) }
            if i % 3 == 0 {
                var r: [String: JSONValue] = ["id": "0123456789abcdef01234567", "kind": "call", "offer": "once"]
                for k in reqKeys where rng.next() % 2 == 0 { r[k] = value(0) }
                o["request"] = .object(r)
            }
            let p = PushPayload(ws: "ws", kind: ["agent.approval", "agent.changeset", "agent.ask", "agent.done", "agent.clear"][i % 5],
                                title: "t", body: "b", link: "xbin/agent", collapseID: "c", agent: .object(o))
            let d = AgentNoticeData(json: .object(o))
            if let b = d?.badge { #expect((0...9999).contains(b)) }
            #expect((d?.clear.count ?? 0) <= AgentNoticeData.maxClear)
            _ = AgentNotice.content(p, appWorkspace: "a", prefs: AgentNotifyPrefs(), device: AgentNoticeDevice())
        }
    }
}
