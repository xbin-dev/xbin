import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

/// Checks on the SwiftUI sources that hold on Linux, where they don't
/// compile: every fixture has a `#Preview`, and every primitive of the
/// vocabulary has a branch in NodeView's dispatch.
@Suite struct SourceCoverageTests {
    func source(_ file: String) throws -> String {
        let dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("Sources/XbinRenderer")
        return try String(contentsOf: dir.appendingPathComponent(file), encoding: .utf8)
    }

    /// The string literals of `static let <name>: Set<String> = [ … ]`.
    func literals(_ src: String, set name: String) -> Set<String> {
        guard let start = src.range(of: "static let \(name): Set<String> = ["),
              let end = src.range(of: "]", range: start.upperBound..<src.endIndex) else { return [] }
        let body = src[start.upperBound..<end.lowerBound]
        return Set(body.split(separator: "\"", omittingEmptySubsequences: false).enumerated().filter { $0.offset % 2 == 1 }.map { String($0.element) })
    }

    @Test func everyFixtureHasAPreview() throws {
        let src = try source("Previews.swift")
        for name in try fixtureSet().names() {
            #expect(src.contains("#Preview(\"\(name)\") { XbinFixturePreview(\"\(name)\") }"), "no #Preview for \(name)")
        }
    }

    @Test func everyPrimitiveIsDispatched() throws {
        let src = try source("XbinTreeView.swift")
        let groups = ["structure", "content", "controls", "chat"].map { literals(src, set: $0) }
        let all = groups.reduce(Set<String>()) { $0.union($1) }
        #expect(all == Set(XbinVocabulary.primitives.keys), "missing \(Set(XbinVocabulary.primitives.keys).subtracting(all).sorted()), extra \(all.subtracting(XbinVocabulary.primitives.keys).sorted())")
        #expect(groups.map(\.count).reduce(0, +) == all.count, "a primitive is in two groups")
        // Each group's switch names every member but its default branch.
        for (group, view) in [(groups[0], "StructureNode"), (groups[1], "ContentNode"), (groups[2], "ControlNode"), (groups[3], "ChatNode")] {
            guard let start = src.range(of: "private struct \(view): View {"),
                  let end = src.range(of: "\n}\n", range: start.upperBound..<src.endIndex) else {
                Issue.record("\(view) not found")
                continue
            }
            let body = String(src[start.upperBound..<end.lowerBound])
            let cased = Set(group.filter { body.contains("case \"\($0)\":") })
            #expect(group.subtracting(cased).count <= 1, "\(view) misses \(group.subtracting(cased).sorted())")
        }
    }
}
