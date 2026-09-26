import Foundation
import Testing
@testable import XbinCore

@Suite struct TreeTests {
    /// Every tree in plans/native.md §18 decodes, indexes, and round-trips.
    @Test(arguments: Resources.trees)
    func designTreesRoundTrip(_ name: String) throws {
        let raw = try Resources.json("trees/\(name).json")
        let fixture = try FixtureTree(json: raw)
        #expect(fixture.version == 1)

        // Wire round trip: the encoding is the input minus empty p/e/c.
        #expect(fixture.json == normalizeWire(raw))
        #expect(try FixtureTree(parsing: fixture.json.jsonString) == fixture)
        #expect(try Node(parsing: fixture.root.json.jsonString) == fixture.root)

        // Flat tree: every node indexed once, materializes back to the input.
        let tree = try Tree(root: fixture.root)
        #expect(tree.validate() == nil)
        #expect(tree.count == fixture.root.subtreeCount)
        #expect(tree.keysInOrder == fixture.root.subtreeKeys)
        #expect(tree.root == fixture.root)
        for key in fixture.root.subtreeKeys {
            let n = try #require(fixture.root.find(key))
            let e = try #require(tree[key])
            #expect(e.type == n.type && e.props == n.props && e.events == n.events)
            #expect(e.children == n.children.map(\.key))
            #expect(tree.node(key) == n)
        }

        // Codable goes through the same shape.
        let viaCodable = try JSONDecoder().decode(Node.self, from: JSONEncoder().encode(fixture.root))
        #expect(viaCodable.key == fixture.root.key && viaCodable.subtreeKeys == fixture.root.subtreeKeys)
    }

    @Test func counterDetails() throws {
        let t = try Tree(root: Resources.tree("18.1-counter-go").root)
        let button = try #require(t["r.0.1"])
        #expect(button.type == "button")
        #expect(button[prop: "busy"] == .bool(false)) // a boolean, not 0
        #expect(button[prop: "label"] == "+1")
        #expect(button.listens(to: "tap") && !button.listens(to: "input"))
        #expect(button.parent == "r.0")
        #expect(t.index(of: "r.0.1") == 1)
        #expect(t.ancestors(of: "r.0.1") == ["r.0", "r"])
        #expect(t.isWithin("r.0.1", subtreeOf: "r") && !t.isWithin("r", subtreeOf: "r.0"))
        #expect(t.rootEntry?.type == "screen")
    }

    @Test func numbersInPropsKeepTheirKinds() throws {
        let root = try Resources.tree("18.7-prometheus-viewer").root
        let chart = try #require(root.find("r.1:https://node.example/api/apps/node-exporter.0:process_cpu_seconds_total.1:process_cpu_seconds_total{}.0"))
        let points = try #require(chart.props["series"]?[0]?["points"])
        #expect(points[0] == [.int(1_790_000_000_000), .double(12.28)])
        #expect(points[1]?[1] == .double(12.3))
        // Keys holding URLs, dots, colons and braces are opaque and unique.
        #expect(chart.type == "chart")
    }

    @Test func fragmentRootAndEmptyChildren() throws {
        let raw = try Resources.json("trees/18.6-devbox.json")
        let root = try FixtureTree(json: raw).root
        #expect(root.type == "fragment")
        let tab = try #require(root.find("r.0.0.2.1"))
        #expect(tab.children.isEmpty)
        // "c":[] in the input is the same as no c.
        #expect(tab.json["c"] == nil)
    }

    @Test func unknownNodeFieldsSurvive() throws {
        let n = try Node(parsing: #"{"k":"r","t":"screen","x":{"src":"native.js:3"},"c":[{"k":"r.0","t":"text","p":{"text":"hi"},"future":1}]}"#)
        #expect(n.extra == ["x": ["src": "native.js:3"]])
        #expect(n.children[0].extra == ["future": 1])
        let t = try Tree(root: n)
        #expect(t.root == n)
        #expect(n.json["x"] == ["src": "native.js:3"])
    }

    @Test(arguments: [
        #"[]"#, #"{"t":"x"}"#, #"{"k":"","t":"x"}"#, #"{"k":"r"}"#, #"{"k":1,"t":"x"}"#, #"{"k":"r","t":""}"#,
        #"{"k":"r","t":"x","p":[]}"#, #"{"k":"r","t":"x","e":"tap"}"#, #"{"k":"r","t":"x","e":[1]}"#,
        #"{"k":"r","t":"x","c":{}}"#, #"{"k":"r","t":"x","c":[1]}"#, #"{"k":"r","t":"x","c":[{"k":"r.0"}]}"#,
    ])
    func rejectsMalformedNodes(_ text: String) {
        #expect(throws: NodeDecodingError.self) { try Node(parsing: text) }
    }

    @Test func nullFieldsAreAbsent() throws {
        let n = try Node(parsing: #"{"k":"r","t":"x","p":null,"e":null,"c":null}"#)
        #expect(n == Node(key: "r", type: "x"))
    }

    @Test func duplicateKeysRefused() throws {
        let n = Node(key: "r", type: "screen", children: [Node(key: "a", type: "text"), Node(key: "a", type: "text")])
        #expect(throws: PatchError.duplicateKey("a")) { try Tree(root: n) }
        let self_ = Node(key: "r", type: "screen", children: [Node(key: "r", type: "text")])
        #expect(throws: PatchError.duplicateKey("r")) { try Tree(root: self_) }
    }

    @Test func nodeKeyRules() {
        #expect(NodeKey.root == "r")
        #expect(NodeKey.child("r.0", 2) == "r.0.2")
        #expect(NodeKey.keyed("r.1", 0, "203.0.113.7") == "r.1.0:203.0.113.7")
        #expect(NodeKey.child(NodeKey.keyed("r.1", 0, "3"), 1) == "r.1.0:3.1")
    }

    @Test func emptyTree() {
        let t = Tree()
        #expect(t.isEmpty && t.count == 0 && t.root == nil && t.keysInOrder.isEmpty && t.validate() == nil)
    }

    /// native/fixtures/*/expected.json (the runtime's contract) decode and
    /// round-trip, whenever the directory exists in this checkout.
    @Test func repositoryFixtures() throws {
        guard let set = FixtureSet.locate(from: #filePath) else { return }
        for name in try set.names() {
            let raw = try JSONValue(parsing: Data(contentsOf: set.file(name, "expected.json")))
            let f = try set.expected(name)
            #expect(f.json == normalizeWire(raw), "\(name)")
            let t = try Tree(root: f.root)
            #expect(t.validate() == nil, "\(name)")
            #expect(t.root == f.root, "\(name)")
            _ = try set.data(name)
        }
    }

    @Test func fixtureSetOnADirectory() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("xbincore-fixtures-\(UUID().uuidString)")
        let fixtures = dir.appendingPathComponent("native/fixtures")
        let fm = FileManager.default
        try fm.createDirectory(at: fixtures.appendingPathComponent("counter"), withIntermediateDirectories: true)
        try fm.createDirectory(at: fixtures.appendingPathComponent("no-expected"), withIntermediateDirectories: true)
        defer { try? fm.removeItem(at: dir) }
        try Resources.data("trees/18.1-counter-go.json").write(to: fixtures.appendingPathComponent("counter/expected.json"))
        try Data(#"{"now":"2026-09-26T08:00:00Z","http":{}}"#.utf8).write(to: fixtures.appendingPathComponent("counter/data.json"))
        try Data().write(to: dir.appendingPathComponent("native/x.swift"))

        let set = try #require(FixtureSet.locate(from: dir.appendingPathComponent("native/x.swift").path))
        #expect(set.root.standardizedFileURL.path == fixtures.standardizedFileURL.path)
        #expect(try set.names() == ["counter"])
        #expect(try set.expected("counter") == Resources.tree("18.1-counter-go"))
        #expect(try set.data("counter")?["now"] == "2026-09-26T08:00:00Z")
        #expect(try set.data("no-expected") == nil)
        #expect(try set.expected("counter").mountMessage == .mount(version: 1, root: Resources.tree("18.1-counter-go").root))
    }
}
