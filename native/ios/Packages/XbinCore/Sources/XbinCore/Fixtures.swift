import Foundation

/// A rendered tree as a fixture's `expected.json` holds it:
/// `{"v":1,"root":node}` (plans/native.md §17) — also the shape of a mount
/// message without its `op`.
public struct FixtureTree: Sendable, Equatable {
    public var version: Int
    public var root: Node

    public init(version: Int = 1, root: Node) {
        self.version = version
        self.root = root
    }

    public init(json: JSONValue) throws {
        guard case .object(let o) = json else { throw BridgeDecodingError("fixture: expected an object") }
        guard let rootJSON = o["root"] else { throw BridgeDecodingError("fixture: root missing") }
        version = o["v"]?.intValue.flatMap { Int(exactly: $0) } ?? 1
        root = try Node(json: rootJSON)
    }

    public init(parsing text: String) throws { try self.init(json: JSONValue(parsing: text)) }

    public init(data: Data) throws { try self.init(json: JSONValue(parsing: data)) }

    public var json: JSONValue { ["v": .int(Int64(version)), "root": root.json] }

    /// The fixture as the mount message the runtime would send.
    public var mountMessage: BridgeMessage { .mount(version: version, root: root) }
}

/// The fixture directory `native/fixtures/<name>/{native.js, data.json,
/// expected.json}` — the contract every renderer is tested against.
public struct FixtureSet: Sendable {
    /// The `native/fixtures` directory.
    public let root: URL

    public init(root: URL) { self.root = root }

    /// Finds `native/fixtures` by walking up from `path` (pass `#filePath`
    /// from a test). Nil when no ancestor holds one.
    public static func locate(from path: String) -> FixtureSet? {
        var dir = URL(fileURLWithPath: path).deletingLastPathComponent()
        let fm = FileManager.default
        while dir.path != "/" && !dir.path.isEmpty {
            let candidate = dir.appendingPathComponent("native").appendingPathComponent("fixtures")
            var isDir: ObjCBool = false
            if fm.fileExists(atPath: candidate.path, isDirectory: &isDir), isDir.boolValue {
                return FixtureSet(root: candidate)
            }
            if dir.lastPathComponent == "fixtures", dir.deletingLastPathComponent().lastPathComponent == "native" {
                return FixtureSet(root: dir)
            }
            dir = dir.deletingLastPathComponent()
        }
        return nil
    }

    /// The fixtures that have an `expected.json`, sorted by name.
    public func names() throws -> [String] {
        let fm = FileManager.default
        return try fm.contentsOfDirectory(atPath: root.path)
            .filter { fm.fileExists(atPath: file($0, "expected.json").path) }
            .sorted()
    }

    /// `<root>/<name>/<file>`.
    public func file(_ name: String, _ file: String) -> URL {
        root.appendingPathComponent(name).appendingPathComponent(file)
    }

    /// A fixture's `expected.json`.
    public func expected(_ name: String) throws -> FixtureTree {
        try FixtureTree(data: Data(contentsOf: file(name, "expected.json")))
    }

    /// A fixture's `data.json` (the scripted `xbin` stub), when it has one.
    public func data(_ name: String) throws -> JSONValue? {
        let url = file(name, "data.json")
        guard FileManager.default.fileExists(atPath: url.path) else { return nil }
        return try JSONValue(parsing: Data(contentsOf: url))
    }
}
