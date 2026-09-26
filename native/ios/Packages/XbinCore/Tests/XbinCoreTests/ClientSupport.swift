import Foundation
import Testing
@testable import XbinCore

/// A scripted workspace server for the client tests: routes by
/// "METHOD path", records every request, and can hold a response until
/// released (to line up concurrent requests).
actor FakeServer: APITransport {
    typealias Handler = @Sendable (APIRequest) async -> APIResponse
    private var routes: [String: Handler] = [:]
    private(set) var log: [APIRequest] = []

    func on(_ route: String, _ h: @escaping Handler) { routes[route] = h }

    nonisolated func send(_ request: APIRequest, to origin: ServerOrigin) async throws -> APIResponse {
        await record(request)
        let path = request.path.split(separator: "?", maxSplits: 1).first.map(String.init) ?? request.path
        guard let h = await route("\(request.method) \(path)") else {
            return APIResponse(status: 404, body: Data("{\"error\":\"no route \(path)\"}".utf8))
        }
        return await h(request)
    }

    private func record(_ r: APIRequest) { log.append(r) }
    private func route(_ k: String) -> Handler? { routes[k] }

    func count(_ route: String) -> Int {
        log.filter { "\($0.method) \($0.path.split(separator: "?").first.map(String.init) ?? $0.path)" == route }.count
    }
}

func json(_ status: Int, _ v: JSONValue) -> APIResponse {
    APIResponse(status: status, headers: ["Content-Type": "application/json"], body: v.jsonData)
}

func body(_ r: APIRequest) -> JSONValue {
    (try? JSONValue(parsing: r.body ?? Data())) ?? .null
}

/// A key store that "signs" by prefixing the message with the key id, so a
/// test can check exactly what was signed. Counts prompts.
actor FakeKeys: DeviceKeyStore {
    private(set) var keys: Set<String> = []
    private(set) var prompts = 0
    var failNext: SignInError?
    let spki = Data(DeviceLogin.p256SPKIPrefix + [0x04] + [UInt8](repeating: 7, count: 64))

    func createKey(workspace: String) async throws -> Data {
        keys.insert(workspace)
        return spki
    }

    func hasKey(workspace: String) async -> Bool { keys.contains(workspace) }

    func sign(_ message: Data, workspace: String, reason: String) async throws -> Data {
        prompts += 1
        if let f = failNext { failNext = nil; throw f }
        guard keys.contains(workspace) else { throw SignInError.keyUnavailable("no key") }
        return Data("sig:".utf8) + message
    }

    func deleteKey(workspace: String) async { keys.remove(workspace) }
    func setFailNext(_ e: SignInError?) { failNext = e }
    func add(_ ws: String) { keys.insert(ws) }
}

/// A thread-safe counter/box for closures.
final class Box<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var v: T
    init(_ v: T) { self.v = v }
    var value: T {
        get { lock.lock(); defer { lock.unlock() }; return v }
        set { lock.lock(); v = newValue; lock.unlock() }
    }
    func mutate(_ f: (inout T) -> Void) { lock.lock(); f(&v); lock.unlock() }
}

/// A settable clock.
final class TestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var t: Date
    init(_ t: Date = Date(timeIntervalSince1970: 1_790_000_000)) { self.t = t }
    var now: Date { lock.lock(); defer { lock.unlock() }; return t }
    func advance(_ s: TimeInterval) { lock.lock(); t = t.addingTimeInterval(s); lock.unlock() }
}

let testOrigin = try! ServerOrigin(string: "https://xbin.example.com")

/// A device-login server: challenge + login that checks the signed bytes.
func deviceLoginRoutes(_ s: FakeServer, deviceId: String = "dev-1", origin: String = "https://xbin.example.com",
                       logins: Box<Int> = Box(0), delay: UInt64 = 0) async {
    let nonces = Box<[String]>([])
    await s.on("POST /login/device/challenge") { r in
        guard body(r)["deviceId"]?.stringValue == deviceId else { return json(404, ["error": "unknown device"]) }
        let n = "nonce-\(nonces.value.count)"
        nonces.mutate { $0.append(n) }
        return json(200, ["nonce": .string(n), "expires": 1_790_000_060])
    }
    await s.on("POST /login/device") { r in
        if delay > 0 { try? await Task.sleep(nanoseconds: delay) }
        let b = body(r)
        guard let n = b["nonce"]?.stringValue, nonces.value.contains(n),
              let sig = b["signature"]?.stringValue.flatMap(Base64URL.decode) else { return json(401, ["error": "bad nonce"]) }
        let expected = Data("sig:".utf8) + (try! DeviceLogin.message(origin: origin, deviceId: deviceId, nonce: n))
        guard sig == expected else { return json(401, ["error": "signature"]) }
        logins.mutate { $0 += 1 }
        return json(200, ["token": .string("tok-\(logins.value)"), "tokenType": "Bearer",
                          "user": ["id": "alice", "name": "Alice", "role": "user"], "deviceId": .string(deviceId),
                          "expiresIdle": 1_790_043_200, "expiresMax": 1_792_592_000])
    }
}

func enrolledRecord(deviceId: String = "dev-1") -> WorkspaceRecord {
    var r = WorkspaceRecord(id: "11111111-2222-3333-4444-555555555555", server: testOrigin, user: WorkspaceUser(id: "alice"))
    r.deviceId = deviceId
    r.deviceOrigin = "https://xbin.example.com"
    return r
}
