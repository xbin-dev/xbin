import Foundation
import Testing
@testable import XbinAgent

@Suite struct DiffTests {
    @Test func linesAreNumbered() {
        let ls = LineDiff.lines(old: "a\nb\nc", new: "a\nc\nd")
        #expect(ls.map { "\($0.kind)|\($0.text)|\($0.oldLine ?? 0)|\($0.newLine ?? 0)" } ==
            ["context|a|1|1", "removed|b|2|0", "context|c|3|2", "added|d|0|3"])
        let nf = LineDiff.lines(old: nil, new: "x\ny")
        #expect(nf.map(\.kind) == [.removed, .added, .added]) // the web's diff of "" → "x\ny": one empty line out
        #expect(LineDiff.lines(old: "same", new: "same").map(\.kind) == [.context])
    }

    @Test func hunksKeepContext() {
        let old = (1...20).map { "l\($0)" }.joined(separator: "\n")
        var new = (1...20).map { "l\($0)" }
        new[1] = "L2"
        new[17] = "L18"
        let hs = LineDiff.hunks(old: old, new: new.joined(separator: "\n"), context: 2)
        #expect(hs.count == 2)
        #expect(hs[0].header == "@@ -1,4 +1,4 @@")
        #expect(hs[0].lines.map(\.text) == ["l1", "l2", "L2", "l3", "l4"])
        #expect(hs[1].oldStart == 16 && hs[1].newStart == 16)
        let one = LineDiff.hunks(old: old, new: new.joined(separator: "\n"), context: 8)
        #expect(one.count == 1)
        #expect(LineDiff.hunks(old: "a", new: "a").isEmpty)
    }

    @Test func toolDiffStatOnTheCard() {
        var t = ToolCall(id: "x", toolCallId: "e")
        t.fold(ToolCallUpdate(json: j(#"{"id":"e","kind":"edit","content":[{"type":"diff","path":"a.go","oldText":"x\ny\n","newText":"x\nz\nw\n"}]}"#))!)
        #expect(t.diffStat == DiffStat(added: 2, removed: 1))
        #expect(t.diffs.first?.path == "a.go")
        t.files = FilesChanged(json: j(#"{"changes":[{"path":"b","status":"added","add":3,"del":0}]}"#))
        #expect(t.diffStat == DiffStat(added: 5, removed: 1))
    }

    @Test func gitPatchFromTheSnapshotter() throws {
        let evs = try events("shell")
        let fc = try #require(evs.first { $0.type == "files.changed" })
        guard case .filesChanged(let f) = fc.payload else { Issue.record("not files.changed"); return }
        #expect(f.toolCallId == "run1")
        let files = GitPatch.parse(try #require(f.patch?.text))
        #expect(files.map(\.path) == ["made.txt", "main.go"])
        #expect(files[0].status == .added && files[0].added == 1)
        let h = try #require(files[1].hunks.first)
        #expect(h.oldStart == 1 && h.newStart == 1 && h.oldCount == 3)
        #expect(h.lines.map { "\($0.kind)|\($0.oldLine ?? 0)|\($0.newLine ?? 0)" } ==
            ["context|1|1", "context|2|2", "removed|3|0", "added|0|3"])
        #expect(files[1].added == 1 && files[1].removed == 1)
        #expect(LineDiff.stats(f.patch!.text) == (added: 2, removed: 1, files: 2))
    }

    @Test func gitPatchEdgeCases() {
        let p = """
        diff --git a/old name.txt b/new name.txt
        similarity index 90%
        rename from old name.txt
        rename to new name.txt
        index 1..2 100644
        --- a/old name.txt
        +++ b/new name.txt
        @@ -1,2 +1,2 @@ func x()
         keep
        -gone
        \\ No newline at end of file
        +came
        \\ No newline at end of file
        diff --git a/gone.bin b/gone.bin
        deleted file mode 100644
        Binary files a/gone.bin and /dev/null differ
        diff --git a/t.go b/t.go
        --- a/t.go
        +++ b/t.go
        @@ -10,5 +10,6 @@
         a
        +b
        """
        let fs = GitPatch.parse(p)
        #expect(fs.count == 3)
        #expect(fs[0].status == .renamed && fs[0].oldPath == "old name.txt" && fs[0].path == "new name.txt")
        #expect(fs[0].hunks.first?.section == "func x()")
        #expect(fs[0].hunks.first?.lines.filter { $0.kind == .note }.count == 2)
        #expect(fs[1].status == .deleted && fs[1].binary && fs[1].hunks.isEmpty)
        #expect(fs[2].hunks.first?.lines.count == 2) // a truncated patch parses up to where it stops
        #expect(fs[2].hunks.first?.lines.last?.newLine == 11)
    }
}
