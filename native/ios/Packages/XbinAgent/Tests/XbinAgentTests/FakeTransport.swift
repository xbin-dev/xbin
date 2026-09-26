import Foundation
@testable import XbinAgent

/// A scripted xbind: records every request; answers from a handler; a
/// follow request gets its chunks from `streams` (one script per open).
final class FakeTransport: AgentTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var _requests: [HTTPRequest] = []
    private var handler: @Sendable (HTTPRequest) -> HTTPResponse
    private var streams: [(HTTPRequest) throws -> [Data]] = []

    init(_ handler: @escaping @Sendable (HTTPRequest) -> HTTPResponse = { _ in HTTPResponse(status: 200, body: Data("{}".utf8)) }) {
        self.handler = handler
    }

    var requests: [HTTPRequest] { lock.withLock { _requests } }

    func setHandler(_ h: @escaping @Sendable (HTTPRequest) -> HTTPResponse) { lock.withLock { handler = h } }

    /// Queues the chunks the next follow request streams (thrown errors refuse the open).
    func queueStream(_ s: @escaping (HTTPRequest) throws -> [Data]) { lock.withLock { streams.append(s) } }

    func send(_ request: HTTPRequest) async throws -> HTTPResponse {
        let h = lock.withLock { _requests.append(request); return handler }
        return h(request)
    }

    func stream(_ request: HTTPRequest) async throws -> AsyncThrowingStream<Data, any Error> {
        let s: ((HTTPRequest) throws -> [Data])? = lock.withLock {
            _requests.append(request)
            return streams.isEmpty ? nil : streams.removeFirst()
        }
        guard let s else { throw AgentAPIError(status: 404, message: "no such session") }
        let chunks = try s(request)
        return AsyncThrowingStream { c in
            for ch in chunks { c.yield(ch) }
            c.finish()
        }
    }
}

func ok(_ json: String) -> HTTPResponse { HTTPResponse(status: 200, body: Data(json.utf8)) }
func ok(_ v: JSONValue) -> HTTPResponse { HTTPResponse(status: 200, body: v.data) }
func refuse(_ status: Int, _ msg: String) -> HTTPResponse { HTTPResponse(status: status, body: Data(#"{"error":"\#(msg)"}"#.utf8)) }

extension HTTPRequest {
    var json: JSONValue? { body.flatMap { try? JSONValue.parse($0) } }
    func q(_ k: String) -> String? { query.first { $0.0 == k }?.1 }
}

/// A page of these events, as GET …/events answers.
func page(_ evs: [AgentEvent], truncated: Bool = false) -> HTTPResponse {
    ok(["events": .array(evs.map(\.json)), "next": .number(Double(evs.last?.seq ?? 0)), "truncated": .bool(truncated)])
}
