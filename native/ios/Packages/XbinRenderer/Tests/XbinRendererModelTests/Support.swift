import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

/// native/fixtures, found from this file.
func fixtureSet() throws -> FixtureSet {
    try #require(XbinFixtures.locate(from: #filePath), "native/fixtures not found above \(#filePath)")
}

/// native/spec/vocab.json.
func vocabJSON() throws -> JSONValue {
    let url = try fixtureSet().root.deletingLastPathComponent().appendingPathComponent("spec/vocab.json")
    return try JSONValue(parsing: Data(contentsOf: url))
}

/// Records what a model sends to the runtime.
@MainActor
final class Sent {
    var calls: [RuntimeCall] = []

    var events: [(key: String, type: String, payload: JSONValue, n: Int?)] {
        calls.compactMap {
            if case .event(let k, let t, let p, let n) = $0 { return (k, t, p, n) }
            return nil
        }
    }
}

/// A model over a fresh store with `root` mounted as tree message `n`.
@MainActor
func mounted(_ root: Node, n: Int = 1) -> (TreeStore, XbinTreeModel, Sent) {
    let store = TreeStore()
    let sent = Sent()
    let model = XbinTreeModel(store: store) { sent.calls.append($0) }
    store.apply(.mount(version: 1, root: root, n: n))
    return (store, model, sent)
}

/// Parses a node from JSON text.
func node(_ json: String) throws -> Node { try Node(parsing: json) }
