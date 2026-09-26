// app-live — the app's workspace client (XbinCore's WorkspaceAuth,
// Enrollment, Catalog, TileScheme, push) against a real xbind, with the
// app's own software device key (native/ios/Shared/SoftwareDeviceKey.swift,
// what the simulator uses; the Secure Enclave key signs the same bytes).
//
//   go build -o /tmp/xbind ./cmd/xbind && /tmp/xbind init /tmp/aws
//   /tmp/xbind --dev --workspace /tmp/aws --listen 127.0.0.1:9461 --external-url http://127.0.0.1:9461 &
//   cd native/tools/app-check && swift run app-live 127.0.0.1:9461 admin admin
//
// (--dev seeds admin/admin.) Exit 0 and "LIVE OK" when every check passes.
// Not run by CI — it needs a running xbind.
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import XbinCore

func say(_ s: String) { FileHandle.standardError.write(Data((s + "\n").utf8)) }
var failures = 0
@MainActor func check(_ ok: Bool, _ what: String) {
    say((ok ? "  ok   " : "  FAIL ") + what)
    if !ok { failures += 1 }
}

let args = CommandLine.arguments
let hostPort = args.count > 1 ? args[1] : "127.0.0.1:9461"
let user = args.count > 2 ? args[2] : "admin"
let password = args.count > 3 ? args[3] : "admin"
let origin = try ServerOrigin(string: "http://\(hostPort)")

/// URLSession with no cookies, no cache, redirects not followed — the app's
/// transport (AppTransport.swift) makes the same choices.
final class NoRedirect: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

struct Transport: APITransport {
    let session: URLSession = {
        let c = URLSessionConfiguration.ephemeral
        c.httpCookieStorage = nil
        c.httpShouldSetCookies = false
        c.urlCache = nil
        return URLSession(configuration: c, delegate: NoRedirect(), delegateQueue: nil)
    }()

    func send(_ r: APIRequest, to origin: ServerOrigin) async throws -> APIResponse {
        guard let url = r.url(on: origin) else { throw URLError(.badURL) }
        var req = URLRequest(url: url)
        req.httpMethod = r.method
        for (k, v) in r.headers { req.setValue(v, forHTTPHeaderField: k) }
        req.httpBody = r.body
        let (data, resp): (Data, URLResponse) = try await withCheckedThrowingContinuation { cont in
            session.dataTask(with: req) { d, resp, err in
                if let err { cont.resume(throwing: err) } else { cont.resume(returning: (d ?? Data(), resp!)) }
            }.resume()
        }
        let h = resp as! HTTPURLResponse
        var headers: [String: String] = [:]
        for (k, v) in h.allHeaderFields { headers["\(k)"] = "\(v)" }
        return APIResponse(status: h.statusCode, headers: headers, body: data)
    }
}

/// The simulator's key store: software P-256 keys in memory.
actor SoftwareKeys: DeviceKeyStore {
    var keys: [String: SoftwareDeviceKey] = [:]
    var prompts = 0
    func createKey(workspace: String) async throws -> Data {
        let k = SoftwareDeviceKey()
        keys[workspace] = k
        return k.publicKeySPKI
    }
    func hasKey(workspace: String) async -> Bool { keys[workspace] != nil }
    func sign(_ message: Data, workspace: String, reason: String) async throws -> Data {
        prompts += 1
        guard let k = keys[workspace] else { throw SignInError.keyUnavailable("no key") }
        return try k.sign(message)
    }
    func deleteKey(workspace: String) async { keys[workspace] = nil }
}

let transport = Transport()
let keys = SoftwareKeys()
let client = "app/0.1-live"
let enrollment = Enrollment(transport: transport, keys: keys, clientHeader: client, platform: "ios")

say("app-live against \(origin.origin)")

// 1. Password sign-in, then the in-app enrollment (a fresh sign-in needs no
// step-up), then the first device login.
let pw = try await enrollment.passwordLogin(server: origin, username: user, password: password)
check(pw.kind == .password && !pw.token.isEmpty && pw.userID == user, "password sign-in → a bearer session for \(pw.userID)")
let (record, deviceSession) = try await enrollment.enrollSignedIn(server: origin, session: pw, deviceName: "app-live")
check(record.deviceId?.hasPrefix("dev-") == true, "enrolled: \(record.deviceId ?? "-")")
check(record.deviceOrigin == origin.origin, "the enrollment origin is what logins sign (\(record.deviceOrigin ?? "-"))")
check(deviceSession.kind == .device && deviceSession.userID == user, "device login (challenge → sign → session)")
let staleProbe = try await transport.send(APIRequest("GET", "/api/xbin/whoami", headers: ["Authorization": pw.authorization]), to: origin)
check(staleProbe.status == 401, "the password session was ended after enrolling (\(staleProbe.status))")

// 2. The workspace client: session attached, catalog, layout via the shell's frame token.
let store = MemorySessionStore()
let auth = WorkspaceAuth(record: record, transport: transport, keys: keys, sessions: store, clientHeader: client)
await auth.adopt(deviceSession)
let who = Whoami(json: try await auth.json(APIRequest("GET", "/api/xbin/whoami")))
check(who.userID == user, "whoami as the device session: \(who.displayName)")
let catalog = Catalog(json: try await auth.json(APIRequest("GET", "/api/xbin/components")))
check(!catalog.listed.isEmpty && catalog.listed.allSatisfy { !$0.isShellInternal }, "components: \(catalog.listed.map(\.path))")
let tokens = FrameTokenCache { comp in
    let j = try await auth.json(APIRequest("GET", FrameTokenRoute.path(component: comp)))
    guard let t = FrameTokenRoute.token(from: j) else { throw URLError(.cannotParseResponse) }
    return t
}
let shellToken = try await tokens.token(for: "shell")
let layoutResp = try await transport.send(APIRequest("GET", "/api/xbin/prefs/layout",
                                                     headers: [TileScheme.frameTokenHeader: shellToken]), to: origin)
check(layoutResp.status == 200 || layoutResp.status == 404, "the shell's layout pref via a shell frame token (\(layoutResp.status))")
let shared = SharedScreens(json: try await auth.json(APIRequest("GET", "/api/xbin/screens")))
let nav = NavigatorModel(catalog: catalog, layout: PersonalLayout(json: layoutResp.status == 200 ? try layoutResp.json() : nil),
                         shared: shared, user: who.userID)
check(nav.sections.last?.id == "all", "navigator sections: \(nav.sections.map(\.title))")

// 3. A tile page as the scheme handler loads it: the frame token only.
if let tile = catalog.listed.first(where: { !$0.chrome }) {
    let url = TileScheme.pageURL(workspace: record.id, tile: tile.path)!
    let path = TileScheme.serverPath(for: url, workspace: record.id)!
    let ft = try await tokens.token(for: tile.path)
    let headers = TileScheme.forwardHeaders(["Accept": "text/html", "Cookie": "x=y"], frameToken: ft, client: client)
    let page = try await transport.send(APIRequest("GET", path, headers: headers), to: origin)
    let html = String(decoding: page.body, as: UTF8.self)
    check(page.status == 200 && html.contains("xbin-frame-token"), "\(path) with the tile's frame token → its page")
    check(page.header("content-security-policy")?.contains("sandbox") == true, "the page keeps its CSP sandbox")
    let kept = TileScheme.pageResponseHeaders(page.headers)
    check(kept["content-security-policy"] != nil && kept["set-cookie"] == nil, "response headers passed through (minus cookies)")
    if html.contains("xbin-ws-origin") { check(true, "xbind injected xbin-ws-origin for the app") }
    else { say("  note xbind predates the xbin-ws-origin meta (nat/srvB); tiles' WebSockets need it") }
    let api = try await transport.send(APIRequest("GET", FrameTokenRoute.path(component: tile.path),
                                                  headers: [TileScheme.frameTokenHeader: ft]), to: origin)
    check(api.status == 200, "the tile renews its own frame token with it (\(api.status))")
    let other = catalog.listed.first { $0.path != tile.path && !$0.chrome }
    if let other {
        let cross = try await transport.send(APIRequest("GET", FrameTokenRoute.path(component: other.path),
                                                        headers: [TileScheme.frameTokenHeader: ft]), to: origin)
        check(cross.status == 403 || cross.status == 401, "…but not another tile's (\(cross.status))")
    }
}

// 4. The session dies (sign-out here; an xbind restart does the same): the
// next request re-signs once with the device key and succeeds.
let before = await keys.prompts
_ = try await transport.send(APIRequest("POST", "/logout", headers: ["Authorization": deviceSession.authorization]), to: origin)
let dead = try await transport.send(APIRequest("GET", "/api/xbin/whoami", headers: ["Authorization": deviceSession.authorization]), to: origin)
check(dead.status == 401, "after logout the old session is refused (\(dead.status))")
async let r1 = auth.send(APIRequest("GET", "/api/xbin/whoami"))
async let r2 = auth.send(APIRequest("GET", "/api/xbin/components"))
async let r3 = auth.send(APIRequest("GET", "/api/xbin/screens"))
let statuses = try await [r1.status, r2.status, r3.status]
check(statuses == [200, 200, 200], "three requests on the dead session all succeed after one re-sign \(statuses)")
check(await keys.prompts == before + 1, "exactly one signature (Face ID prompt) for them")
check(await auth.credential?.token != deviceSession.token, "a new session was stored")

// 5. Devices and push registration.
let devices = DeviceInfo.list(json: try await auth.json(APIRequest("GET", AppAuthRoute.devices)))
check(devices.contains { $0.id == record.deviceId && $0.current }, "GET /devices lists this device as current")
let pushKey = PushCrypto.newKeyPair()
let reg = try await auth.send(PushAPI.register(deviceId: record.deviceId!, handle: Base64URL.encode(Data((0..<24).map { UInt8($0) })),
                                               publicKey: Base64URL.encode(pushKey.publicKey), kinds: ["agent", "tile"]))
check(reg.status == 200, "POST /devices/push registers (\(reg.status))")
let status = PushStatus(json: try await auth.json(APIRequest("GET", PushAPI.registrations)))
check(status.devices.contains { $0.deviceId == record.deviceId }, "GET /devices/push lists it (workspace \(status.workspace), enabled \(status.enabled))")
let st = PushState(handle: Base64URL.encode(Data((0..<24).map { UInt8($0) })), relay: "https://relay.test", apnsToken: "aa",
                   pushWorkspace: status.workspace, publicKey: Base64URL.encode(pushKey.publicKey), kinds: ["agent", "tile"])
check(PushMaintenance.plan(state: st, apnsToken: "aa", relay: "https://relay.test", publicKey: st.publicKey!, kinds: ["agent", "tile"],
                           deviceId: record.deviceId!, server: status) == .none, "push maintenance: nothing to do")
let unreg = try await auth.send(PushAPI.unregister(deviceId: record.deviceId!))
check(unreg.status == 204 || unreg.status == 200, "DELETE /devices/push/<id> (\(unreg.status))")

// 6. Removing the device: the next sign-in says so.
let del = try await auth.send(APIRequest("DELETE", "\(AppAuthRoute.devices)/\(record.deviceId!)"))
check(del.isSuccess, "DELETE /devices/<id> (\(del.status))")
do {
    let a2 = WorkspaceAuth(record: record, transport: transport, keys: keys, sessions: MemorySessionStore(), clientHeader: client)
    _ = try await a2.signIn()
    check(false, "sign-in after removal should fail")
} catch let e as SignInError {
    check(e == .deviceRemoved, "a removed device gets SignInError.deviceRemoved (\(e))")
}

say(failures == 0 ? "LIVE OK" : "LIVE FAILED (\(failures))")
exit(failures == 0 ? 0 : 1)
