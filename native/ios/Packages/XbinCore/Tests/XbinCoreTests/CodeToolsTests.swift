import Foundation
import Testing
@testable import XbinCore

/// The sessions screen's Code, Logs and PRs tools (D132): routes, the
/// answers' shapes (docs/protocol.md), the streamed log's lines and the
/// proposals as web/bx-prs.js shows them.
@Suite struct CodeToolsTests {
    @Test func routesEscapeATileAndItsFile() {
        #expect(CodeRoute.tree("apps/crm") == "/api/xbin/code/tree?component=apps%2Fcrm")
        #expect(CodeRoute.tree("apps/a+b") == "/api/xbin/code/tree?component=apps%2Fa%2Bb", "a + in a query reads as a space: escaped")
        #expect(CodeRoute.file("apps/crm", "web/main app.js") == "/api/xbin/code/file?component=apps%2Fcrm&file=web%2Fmain%20app.js")
        #expect(CodeRoute.log("apps/crm", limit: 20) == "/api/xbin/git/log?component=apps%2Fcrm&limit=20")
        #expect(CodeRoute.diff("apps/crm", rev: "") == "/api/xbin/git/diff?component=apps%2Fcrm&rev=")
        #expect(CodeRoute.logs("apps/crm", deployment: "dev") == "/api/xbin/logs?component=apps%2Fcrm&tail=65536&deployment=dev&follow=1")
        #expect(CodeRoute.logs("apps/crm", follow: false) == "/api/xbin/logs?component=apps%2Fcrm&tail=65536")
        #expect(ProposalRoute.one(target: "apps/crm", n: 3) == "/api/xbin/code/pr?target=apps%2Fcrm&n=3")
        #expect(ProposalRoute.applyCommand(3) == "bx code pr fetch 3 | git am --3way")
    }

    @Test func theTreeFoldersFirst() throws {
        let j = try JSONValue(parsing: #"{"component":"apps/crm","files":[{"path":"xbin.json","size":10},{"path":"web/b.js","size":3},{"path":"web/a.js","size":2},{"path":"main.go","size":99},{"path":"web/lib/x.js","size":1}]}"#)
        let t = try #require(CodeTree(json: j))
        #expect(t.fileCount == 5)
        #expect(t.root.folders.map(\.name) == ["web"] && t.root.files.map(\.name) == ["main.go", "xbin.json"])
        let web = t.root.folders[0]
        #expect(web.path == "web" && web.files.map(\.path) == ["web/a.js", "web/b.js"] && web.count == 3)
        #expect(web.folders.map(\.path) == ["web/lib"])
        #expect(CodeTree(json: .object([:])) == nil)
    }

    @Test func aFileItsLogAndActivity() throws {
        #expect(CodeFile(json: try JSONValue(parsing: #"{"path":"a","content":"hi\n"}"#)) == .text("hi\n", truncated: false))
        #expect(CodeFile(json: try JSONValue(parsing: #"{"path":"a","content":"hi","truncated":true}"#)) == .text("hi", truncated: true))
        #expect(CodeFile(json: try JSONValue(parsing: #"{"path":"a.png","binary":true}"#)) == .binary)
        let log = GitCommit.list(try JSONValue(parsing: #"{"repo":"apps/crm","commits":[{"hash":"3f2a1c9e00","short":"3f2a1c9","author":"ana","date":1790000000,"subject":"fix","add":4,"del":1,"files":2},{"subject":"no hash"}]}"#))
        #expect(log == [GitCommit(hash: "3f2a1c9e00", short: "3f2a1c9", author: "ana", date: 1_790_000_000, subject: "fix", added: 4, removed: 1, files: 2)])
        let now = Date(timeIntervalSince1970: 1_790_000_000)
        let a = GitActivity(json: try JSONValue(parsing: #"{"local":[{"t":1790000000,"a":"x"},{"t":1789990000},{"t":1789900000},{"t":1700000000}]}"#))
        let days = a.perDay(days: 3, now: now)
        #expect(days == [0, 1, 2], "two today, one yesterday, the old one out of range")
    }

    @Test func aStreamedLogInWholeLines() {
        var l = LogLines(limit: 3)
        let first = l.append(Data("start".utf8))
        #expect(!first)
        #expect(l.pending == "start")
        let more = l.append(Data("ed\r\nsecond\n\u{1B}[31mred\u{1B}[0m\n".utf8))
        #expect(more)
        #expect(l.lines == ["started", "second", "red"])
        // A multibyte character split across chunks waits for its line.
        let e = Array("é\n".utf8)
        l.append(Data(e[0..<1]))
        l.append(Data(e[1...]))
        #expect(l.lines == ["second", "red", "é"] && l.dropped == 1, "the last three lines, one dropped")
        #expect(LogLines.plain("\u{1B}]0;title\u{07}ok") == "ok")
        #expect(LogLines.plain("plain") == "plain")
    }

    @Test func proposalsAsTheWebShowsThem() throws {
        let j = try JSONValue(parsing: #"""
        {"target":"apps/crm","prs":[
          {"number":1,"target":"apps/crm","from":{"user":"bo"},"title":"old","state":"merged","created":"2026-09-27T10:00:00Z"},
          {"number":2,"target":"apps/crm","from":{"component":"apps/helper","user":"ana","via":"terminal"},"title":"Fix the chart",
           "message":"why","base":"3f2a1c9e","state":"open","created":"2026-09-28T10:00:00Z","updated":"2026-09-28T11:00:00Z",
           "events":[{"ts":"2026-09-28T10:30:00Z","who":"user:e2e","type":"comment","body":"looks good"},
                     {"ts":"2026-09-28T11:00:00Z","who":"user:e2e","type":"state","state":"rejected","body":"no"}]}]}
        """#)
        let prs = Proposal.list(j)
        #expect(prs.map(\.number) == [2, 1], "newest first")
        #expect(Proposal.openCount(prs) == 1)
        let p = prs[0]
        #expect(p.isOpen && p.from == "apps/helper (ana)" && p.fromShort == "apps/helper" && p.base == "3f2a1c9e")
        #expect(prs[1].from == "user:bo")
        #expect(p.events.map(Proposal.text) == ["looks good", "→ rejected: no"])
        #expect(ProposalRoute.stateBody(target: "apps/crm", n: 2, state: "merged", comment: "")
                == .object(["target": .string("apps/crm"), "n": .int(2), "state": .string("merged")]))
        #expect(ProposalRoute.stateBody(target: "apps/crm", n: 2, state: "rejected", comment: "no")["comment"] == .string("no"))
    }
}
