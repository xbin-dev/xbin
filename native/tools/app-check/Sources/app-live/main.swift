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
// With node on PATH the app's events socket is checked too (the device
// session as the bearer on /ws/events, through native/tools/events-live.mjs;
// APPLIVE_WORKSPACE=<the workspace directory> adds a file change → reload),
// then the web ticket (or its fallback) and minting a code that enrolls a
// second device.
// APPLIVE_RESTART=<command that restarts xbind> adds the restart checks (the
// app re-signs once; frame tokens outlive the restart). On an xbind that
// serves native runtime documents (whoami native.runtime), a tile with a
// native entry is also loaded as the app's hidden runtime document would be.
// Not run by CI — it needs a running xbind.
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import XbinCore
#if canImport(Glibc)
import Glibc
#endif

func say(_ s: String) { FileHandle.standardError.write(Data((s + "\n").utf8)) }
var failures = 0
@MainActor func check(_ ok: Bool, _ what: String) {
    say((ok ? "  ok   " : "  FAIL ") + what)
    if !ok { failures += 1 }
}

let args = CommandLine.arguments

// `app-live bridge-js`: the tile bridge's user script and a reply script
// (id "__ID__"), for native/tools/bridge-check.mjs to run in a real engine.
// `app-live runtime-js <vocab.json>`: the document-start script the app
// injects into a native tile's runtime (caps for the whole vocabulary).
// `app-live event-js <key> <type> <n>`: the callAsyncJavaScript body of a tap.
// `app-live tree-check <messages.json>`: runtime → app messages (JSON
// strings, as WebKit delivers them) through TreeStore; prints the tree.
// All three for native/tools/runtime-check.mjs.
if args.count > 2, args[1] == "runtime-js" {
    let vocab = try JSONValue(parsing: Data(contentsOf: URL(fileURLWithPath: args[2])))
    var prims: [String: Int] = [:]
    for (k, v) in vocab["prims"]?.objectValue ?? [:] { prims[k] = Int(v["rev"]?.intValue ?? 1) }
    let features = (vocab["features"]?.arrayValue ?? []).compactMap(\.stringValue)
    let caps = NativeCaps(v: 1, renderer: "ios", app: "app-live", prims: prims, features: features)
    print(RuntimeScript.documentStart(caps: caps, state: ["scroll": 3]))
    exit(0)
}
if args.count > 4, args[1] == "event-js" {
    print(RuntimeCall.event(key: args[2], type: args[3], payload: [:], n: Int(args[4])).functionBody)
    exit(0)
}
if args.count > 2, args[1] == "tree-check" {
    let msgs = try JSONValue(parsing: Data(contentsOf: URL(fileURLWithPath: args[2]))).arrayValue ?? []
    let store = TreeStore(savedState: nil)
    var out: [JSONValue] = []
    do {
        for m in msgs {
            guard let s = m.stringValue else { continue }
            let ev = store.receive(body: s)
            let what: String
            switch ev {
            case .tree(let rev, let delta)?: what = "tree rev \(rev) remounted=\(delta.remounted) updated=\(delta.updated.count)"
            case .failed(let f)?: what = "FAILED \(f)"
            case .some(let e): what = "\(e)".components(separatedBy: "(").first ?? "event"
            case nil: what = "-"
            }
            out.append(.string(what))
        }
        let root = store.isMounted ? store.tree.root?.json ?? .null : .null
        print(JSONValue.object(["events": .array(out), "failure": store.failure.map { .string("\($0)") } ?? .null,
                                "n": .int(Int64(store.treeSequence ?? 0)), "root": root]).jsonString)
    }
    exit(0)
}
if args.count > 1, args[1] == "bridge-js" {
    let reply = TileBridge.replyScript(id: "__ID__", result: DialogSpec.result(button: "ok", values: ["name": "Ada"]))
    let out: JSONValue = ["userScript": .string(TileBridge.userScript), "reply": .string(reply),
                          "closeReply": .string(TileBridge.replyScript(id: "__ID__", result: nil))]
    print(out.jsonString)
    exit(0)
}
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

/// The events driver's socket is played by events-live.mjs here.
@MainActor final class NoSocket: EventSocket { func close() {} }

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

// 2b. The app's events socket (App/Model/WorkspaceEvents.swift) with this
// device session as its bearer: native/tools/events-live.mjs opens
// /ws/events the way URLSessionWebSocketTask does (libcurl here can't) and
// causes a branding change and — with APPLIVE_WORKSPACE, the workspace
// directory — a file change in a tile; the frames it got go through the
// app's router: the tile's reload follower and the branding hook fire.
if let tile = catalog.listed.first(where: { !$0.chrome && !$0.isShellInternal }) {
    let script = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("events-live.mjs").path
    let bearer = await auth.credential?.token ?? ""
    var argv = ["node", script, origin.origin, bearer]
    let wsDir = ProcessInfo.processInfo.environment["APPLIVE_WORKSPACE"] ?? ""
    if !wsDir.isEmpty { argv += [wsDir, tile.path] }
    let p = Process()
    p.executableURL = URL(fileURLWithPath: "/usr/bin/env")
    p.arguments = argv
    let pipe = Pipe()
    p.standardOutput = pipe
    do {
        try p.run()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        let events = WorkspaceEvents(auth: auth, makeSocket: { _, _, _ in NoSocket() })
        events.userID = { who.userID }
        var brandings = 0
        var reloads = 0
        var frames = 0
        events.onBranding = { brandings += 1 }
        let follower = Task { @MainActor in await events.onReload(of: tile.path) { reloads += 1 } }
        while events.followedTiles.isEmpty { await Task.yield() }
        for line in String(decoding: data, as: UTF8.self).split(separator: "\n") {
            guard let j = try? JSONValue(parsing: String(line)) else { continue }
            if let name = j["check"]?.stringValue { check(j["ok"]?.boolValue == true, "events socket: \(name)") }
            if let f = j["frame"]?.stringValue { frames += 1; events.receive(f) }
        }
        for _ in 0..<100 where reloads == 0 && !wsDir.isEmpty { await Task.yield() }
        follower.cancel()
        check(p.terminationStatus == 0, "events-live.mjs finished (\(frames) frames)")
        check(brandings >= 1, "the branding frame reached the workspace's hook")
        if wsDir.isEmpty { say("  note APPLIVE_WORKSPACE unset: no file change, reload not checked") }
        else { check(reloads >= 1, "a file change in \(tile.path) reloaded its open screen (\(reloads))") }
    } catch {
        say("  note node not runnable (\(error)): events socket not checked")
    }
}

// 2c. Signed-in Safari: a one-shot ticket for the device session (POST
// /api/xbin/web-ticket); an xbind without the route gets the plain page.
if let tile = catalog.listed.first(where: { !$0.chrome }) {
    let next = "/c/\(URLComponent.encodePath(tile.path))/"
    let r = try? await auth.send(WebTicket.request(next: next))
    let dest = WebTicket.destination(r, origin: origin, next: next)
    if r?.status == 404 || r?.status == 405 {
        check(dest?.fellBack == true && dest?.url.absoluteString == origin.origin + next,
              "web-ticket: this xbind has no route (\(r?.status ?? 0)) → Safari gets the plain page")
    } else {
        check(dest?.fellBack == false && dest?.url.absoluteString.hasPrefix(origin.origin + "/") == true,
              "web-ticket → a same-origin ticket URL (\(r?.status ?? 0)): \(dest?.url.path ?? "-")")
        if let u = dest?.url, dest?.fellBack == false {
            let redeem = try await transport.send(APIRequest("GET", String(u.absoluteString.dropFirst(origin.origin.count))), to: origin)
            check(redeem.status == 302 || redeem.status == 303, "…which redeems into a redirect (\(redeem.status))")
        }
    }
}

// 2d. Add another device: the device session mints a code (a password
// step-up if xbind asks for one), and the link the QR code shows enrolls a
// second device.
do {
    var out = AddDevice.outcome(try await auth.send(AddDevice.request()), origin: origin)
    if out == .signInAgain {
        _ = try await auth.signIn()
        out = AddDevice.outcome(try await auth.send(AddDevice.request()), origin: origin)
        say("  …  enroll-code asked for a fresh sign-in")
    }
    if out == .needsPassword {
        say("  …  enroll-code asked for the password (step-up)")
        out = AddDevice.outcome(try await auth.send(AddDevice.request(password: password)), origin: origin)
    }
    if case .code(let link, let code, _) = out, case .enroll(let server, let c)? = try? DeepLink(string: link) {
        check(server == origin && c == code, "add another device: the QR link names this workspace (\(AddDevice.grouped(code).prefix(9))…)")
        let (second, _) = try await enrollment.enroll(server: server, code: c, deviceName: "app-live second")
        check(second.deviceId != nil && second.deviceId != record.deviceId, "…and enrolls a second device with it")
        if let d = second.deviceId { _ = try? await auth.send(APIRequest("DELETE", "\(AppAuthRoute.devices)/\(d)")) }
    } else {
        check(false, "add another device: \(out)")
    }
}

// 3. A tile page as the scheme handler loads it: the frame token only.
var tileToken: (String, String)?
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
    // Its subresources and API calls, as WebKit asks for them through the
    // scheme handler (no cookies, no Sec-Fetch headers, the frame token).
    let vendor = try await transport.send(APIRequest("GET", "/vendor/xbin-client.js",
                                                     headers: TileScheme.forwardHeaders([:], frameToken: ft, client: client)), to: origin)
    check(vendor.status == 200 && vendor.header("content-type")?.contains("javascript") == true, "/vendor/xbin-client.js through the handler")
    if let script = html.range(of: #"src="\./([^"]+\.js)""#, options: .regularExpression) {
        let rel = String(html[script]).dropFirst(7).dropLast(1)
        let asset = try await transport.send(APIRequest("GET", "/c/\(tile.path)/\(rel)",
                                                        headers: TileScheme.forwardHeaders([:], frameToken: ft, client: client)), to: origin)
        check(asset.status == 200, "the page's own script /c/\(tile.path)/\(rel) (\(asset.status))")
    }
    let whoTile = try await transport.send(APIRequest("GET", "/api/xbin/whoami",
                                                      headers: TileScheme.forwardHeaders(["Accept": "application/json"], frameToken: ft, client: client)), to: origin)
    let wt = (try? whoTile.json()) ?? .null
    check(whoTile.status == 200 && wt["kind"]?.stringValue != "user", "the tile's fetch('/api/xbin/whoami') runs as the tile, not the user (kind \(wt["kind"]?.stringValue ?? "-"))")
    tileToken = (tile.path, ft)
    let other = catalog.listed.first { $0.path != tile.path && !$0.chrome }
    if let other {
        let cross = try await transport.send(APIRequest("GET", FrameTokenRoute.path(component: other.path),
                                                        headers: [TileScheme.frameTokenHeader: ft]), to: origin)
        check(cross.status == 403 || cross.status == 401, "…but not another tile's (\(cross.status))")
    }
}

// 3b. Native tiles (an xbind with runtime documents, plans/native.md §7).
if who.nativeRuntime != nil, let nt = catalog.listed.first(where: { $0.opensNatively }) {
    let ft = try await tokens.token(for: nt.path)
    let url = TileScheme.runtimeURL(workspace: record.id, tile: nt.path)!
    let doc = try await transport.send(APIRequest("GET", TileScheme.serverPath(for: url, workspace: record.id)!,
                                                  headers: TileScheme.forwardHeaders([:], frameToken: ft, client: client)), to: origin)
    let html = String(decoding: doc.body, as: UTF8.self)
    check(doc.status == 200 && html.contains("xbin-native"), "\(nt.path)'s runtime document (?native=1) by frame token")
    check(html.contains("xbin-ws-origin"), "…with the xbin-ws-origin meta the app's requests get")
    check(html.contains(nt.nativeEntry ?? "native.js"), "…importing its native entry \(nt.nativeEntry ?? "")")
    check(TileSurface.pick(nt, serverRuntime: who.nativeRuntime) == .native, "the navigator opens it natively")
} else {
    say("  note no native runtime on this xbind (or no native tile): runtime document not checked")
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

// 4b. An xbind restart (APPLIVE_RESTART: a command that restarts it): every
// session dies, the app re-signs once; frame tokens minted before it keep
// working (they outlive a restart until their login would have expired).
if let cmd = ProcessInfo.processInfo.environment["APPLIVE_RESTART"], !cmd.isEmpty {
    // A token minted by the live session (the one from step 3 died with the
    // logout above — sign-out ends the frame tokens a session minted).
    var beforeRestart: (String, String)?
    if let (t, _) = tileToken { beforeRestart = (t, try await tokens.renew(t)) }
    #if canImport(Glibc)
    _ = system(cmd)
    #else
    let p = Process()
    p.executableURL = URL(fileURLWithPath: "/bin/sh")
    p.arguments = ["-c", cmd]
    try p.run()
    p.waitUntilExit()
    #endif
    say("  …  xbind restarted")
    var up = false
    for _ in 0..<80 {
        if let r = try? await transport.send(APIRequest("GET", "/login"), to: origin), r.status == 200 { up = true; break }
        try await Task.sleep(nanoseconds: 250_000_000)
    }
    check(up, "xbind is back after the restart")
    let prompts = await keys.prompts
    let stale = await auth.credential?.token ?? ""
    let r = try await auth.send(APIRequest("GET", "/api/xbin/whoami"))
    let fresh = await auth.credential?.token ?? ""
    check(r.status == 200 && fresh != stale, "after the restart the next request re-signs and succeeds (\(r.status))")
    let after = await keys.prompts
    check(after == prompts + 1, "…with one signature")
    if let (t, ft) = beforeRestart {
        let page = try await transport.send(APIRequest("GET", "/c/\(t)/", headers: TileScheme.forwardHeaders([:], frameToken: ft, client: client)), to: origin)
        check(page.status == 200, "a frame token from before the restart still loads its tile (\(page.status))")
    }
}

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
