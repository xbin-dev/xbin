import Foundation
import XbinCore

/// The fixtures (`native/fixtures/<name>/expected.json`, plans/native.md
/// §17) as trees the renderer can draw — for `#Preview`s, the snapshot
/// tests and a debug gallery in the app.
public enum XbinFixtures {
    /// `native/fixtures`: `FIXTURES_DIR` / `TEST_RUNNER_FIXTURES_DIR` from
    /// the environment when set (the Apple CI passes it), else found by
    /// walking up from `filePath` (pass `#filePath`).
    public static func locate(from filePath: String = #filePath) -> FixtureSet? {
        let env = ProcessInfo.processInfo.environment
        for name in ["FIXTURES_DIR", "TEST_RUNNER_FIXTURES_DIR"] {
            if let dir = env[name], !dir.isEmpty, FileManager.default.fileExists(atPath: dir) {
                return FixtureSet(root: URL(fileURLWithPath: dir))
            }
        }
        return FixtureSet.locate(from: filePath)
    }

    /// A store with fixture `name` mounted (as the runtime's first mount).
    @MainActor
    public static func store(_ name: String, in set: FixtureSet) throws -> TreeStore {
        let fixture = try set.expected(name)
        let store = TreeStore()
        store.apply(.mount(version: fixture.version, root: fixture.root, n: 1))
        if let failure = store.failure { throw failure }
        return store
    }

    /// Every primitive type used in a tree.
    public static func types(_ root: Node) -> Set<String> {
        var out: Set<String> = [root.type]
        for c in root.children { out.formUnion(types(c)) }
        return out
    }
}
