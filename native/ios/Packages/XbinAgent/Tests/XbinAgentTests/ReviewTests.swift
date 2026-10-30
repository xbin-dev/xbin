import Foundation
import Testing
@testable import XbinAgent

// D210: a change set reviewed a file at a time — the per-file split of the
// event's patch, a file's numbered lines, the route when the patch does not
// hold the file, what an ended session can still show, one load per file.
@Suite struct ReviewTests {
    static let a = "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@ func main()\n package main\n-x\n+y\n z\n"
    static let gone = "diff --git a/old.txt b/old.txt\ndeleted file mode 100644\n--- a/old.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-bye\n\\ No newline at end of file\n"
    static let big = "diff --git a/b/big.txt b/b/big.txt\nnew file mode 100644\n--- /dev/null\n+++ b/b/big.txt\n@@ -0,0 +1,3 @@\n+1\n+2\n"

    static func files(truncated: Bool, turn: Int? = 4) -> FilesChanged {
        FilesChanged(changes: [
            fc("a.go", "modified", 1, 1), fc("old.txt", "deleted", 0, 1), fc("b/big.txt", "added", 3, 0), fc("c.txt", "added", 9, 0),
        ], patch: FilesPatch(format: "git_patch", text: a + gone + big, truncated: truncated), turn: turn)
    }

    static func fc(_ path: String, _ status: String, _ add: Int, _ del: Int) -> FileChange {
        FileChange(json: ["path": .string(path), "status": .string(status), "add": .number(Double(add)), "del": .number(Double(del))])!
    }

    @Test func splitsAPatchPerFile() {
        let p = PatchParts.split(Self.a + Self.gone + Self.big)
        #expect(p.map(\.path) == ["a.go", "old.txt", "b/big.txt"])
        #expect(String(p[0].text) == Self.a)
        #expect(String(p[2].text) == Self.big)
        #expect(PatchParts.split("").isEmpty)
        #expect(PatchParts.split("garbage\n").isEmpty)
    }

    @Test func parsesAFileIntoNumberedLines() {
        let r = FileReview.parse(Substring(Self.a), origin: .inline)
        #expect(r.lines.map(\.kind) == [.hunk, .context, .removed, .added, .context])
        #expect(r.lines.map(\.number) == [nil, 1, 2, 2, 3])
        #expect(r.lines[0].text == "@@ -1,3 +1,3 @@ func main()")
        #expect(r.lines[2].text == "x" && r.lines[3].text == "y")
        #expect(r.lines.map(\.id) == Array(0..<5))
        #expect(r.added == 1 && r.removed == 1 && r.numberDigits == 1 && r.emptyText == nil)
        let d = FileReview.parse(Substring(Self.gone), origin: .inline)
        #expect(d.lines.map(\.kind) == [.hunk, .removed, .note])
        // a "+++" line inside a hunk is content, not a header
        let tricky = FileReview.parse("diff --git a/x b/x\n@@ -0,0 +1,2 @@\n+++ two pluses\n+--- two minuses\n", origin: .inline)
        #expect(tricky.lines.map(\.kind) == [.hunk, .added, .added] && tricky.lines[1].text == "++ two pluses")
        let bin = FileReview.parse("diff --git a/p.png b/p.png\nBinary files a/p.png and b/p.png differ\n", origin: .inline)
        #expect(bin.binary && bin.emptyText == "A binary file")
        let mode = FileReview.parse("diff --git a/s b/s\nold mode 100644\nnew mode 100755\n", origin: .inline)
        #expect(mode.lines.map(\.kind) == [.meta, .meta])
    }

    @Test func aBigFileCountsItsLines() {
        let body = (1...1200).map { "+line \($0)" }.joined(separator: "\n")
        let r = FileReview.parse(Substring("diff --git a/f b/f\n@@ -0,0 +1,1200 @@\n" + body + "\n"), origin: .server)
        #expect(r.lines.count == 1201 && r.added == 1200 && r.numberDigits == 4)
        #expect(FileReview.shownLines == 400)
    }

    @Test func changeSetsFromTheTranscript() throws {
        let turn = ChangeSet(turn: TurnChanges(id: "c9", turn: 4, files: Self.files(truncated: false)))
        #expect(turn.id == "c9" && turn.count == "4 files" && turn.added == 13 && turn.removed == 2)
        #expect(turn.files.map(\.letter) == ["M", "D", "A", "A"])
        guard case .snapshot(let key, _, false) = turn.source else { Issue.record("not a snapshot"); return }
        #expect(key == .turn(4))
        var t = ToolCall(id: "x1", toolCallId: "call-1")
        #expect(ChangeSet(tool: t) == nil)
        t.content = [.diff(path: "n.txt", oldText: nil, newText: "a\nb\n")]
        let c = try #require(ChangeSet(tool: t))
        #expect(c.files.map(\.path) == ["n.txt"] && c.files[0].status == .added)
        t.files = Self.files(truncated: true, turn: nil)
        let s = try #require(ChangeSet(tool: t))
        guard case .snapshot(let k2, _, true) = s.source else { Issue.record("not a snapshot"); return }
        #expect(k2 == .toolCall("call-1"))
        #expect(ReviewFile(path: "a/b/c.go", status: .modified).folder == "a/b" && ReviewFile(path: "c.go", status: .modified).name == "c.go")
    }

    @Test func aWholeInlinePartNeedsNoRequest() async {
        let t = FakeTransport { _ in refuse(500, "no") }
        let l = ReviewLoader(client: AgentClient(transport: t))
        let set = ChangeSet(id: "c1", files: Self.files(truncated: false))
        let r = await l.review(set, file: set.files[0], session: "s", live: true)
        #expect(r.origin == .inline && !r.cut && r.added == 1)
        let d = await l.review(set, file: set.files[1], session: "s", live: true)
        #expect(d.removed == 1 && d.lines.last?.kind == .note)
        #expect(t.requests.isEmpty)
    }

    @Test func pastTheCapTheRouteOnce() async {
        let full = "diff --git a/c.txt b/c.txt\nnew file mode 100644\n--- /dev/null\n+++ b/c.txt\n@@ -0,0 +1,2 @@\n+1\n+2\n"
        let t = FakeTransport { r in
            r.path == "/api/xbin/term/sessions/s%201/diff" && r.q("turn") == "4" && r.q("path") == "c.txt"
                ? HTTPResponse(status: 200, headers: ["x-truncated": "true"], body: Data(full.utf8)) : refuse(404, "gone")
        }
        let l = ReviewLoader(client: AgentClient(transport: t))
        let set = ChangeSet(id: "c1", files: Self.files(truncated: true))
        let r = await l.review(set, file: set.files[3], session: "s 1", live: true)
        #expect(r.origin == .server && r.cut && r.added == 2)
        _ = await l.review(set, file: set.files[3], session: "s 1", live: true)
        #expect(t.requests.count == 1, "parsed once, kept")
        #expect(await l.loads == ["turn=4&path=c.txt"])
        // the patch cut inside a file: the route (gone now), then the part, cut
        let b = await l.review(set, file: set.files[2], session: "s 1", live: true)
        #expect(b.origin == .inline && b.cut && b.added == 2)
        #expect(t.requests.count == 2)
    }

    @Test func anEndedSessionShowsWhatTheEventHolds() async {
        let t = FakeTransport { _ in ok("") }
        let l = ReviewLoader(client: AgentClient(transport: t))
        let set = ChangeSet(id: "c1", files: Self.files(truncated: true))
        let b = await l.review(set, file: set.files[2], session: "s", live: false)
        #expect(b.cut && b.origin == .inline)
        let none = await l.review(set, file: set.files[3], session: "s", live: false)
        #expect(none.emptyText == ReviewLoader.notKept)
        #expect(t.requests.isEmpty)
    }

    @Test func anEditsOwnDiff() async {
        let l = ReviewLoader(client: AgentClient(transport: FakeTransport()))
        var t = ToolCall(id: "x1", toolCallId: "e1")
        t.content = [.diff(path: "m.txt", oldText: "a\nb\nc", newText: "a\nB\nc")]
        let set = ChangeSet(tool: t)!
        let r = await l.review(set, file: set.files[0], session: "s", live: true)
        #expect(r.origin == .content && r.added == 1 && r.removed == 1)
        #expect(r.lines.map(\.kind) == [.hunk, .context, .removed, .added, .context])
        #expect(r.lines.map(\.number) == [nil, 1, 2, 2, 3])
    }
}
