import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import Testing
@testable import XbinCore

/// A transport that fails every request as URLSession would.
struct FailingTransport: APITransport {
    let error: any Error & Sendable
    func send(_ request: APIRequest, to origin: ServerOrigin) async throws -> APIResponse { throw error }
}

/// Wraps a FakeServer and records the origin each request went to.
actor OriginLog: APITransport {
    let server: FakeServer
    private(set) var origins: [String] = []
    init(_ server: FakeServer) { self.server = server }
    nonisolated func send(_ request: APIRequest, to origin: ServerOrigin) async throws -> APIResponse {
        await note(origin)
        return try await server.send(request, to: origin)
    }
    private func note(_ o: ServerOrigin) { origins.append(o.origin) }
}

func text(_ status: Int, _ s: String, _ contentType: String = "text/plain; charset=utf-8") -> APIResponse {
    APIResponse(status: status, headers: ["Content-Type": contentType], body: Data(s.utf8))
}

let urlDomain = "NSURLErrorDomain"

/// The heart of an older xbind's sign-in page (internal/server/login.go).
let olderLoginPage = """
<!doctype html><html><head><title>xbin</title></head><body>
<form class="card" method="post" action="/login">
  <label for="u">Username</label><input id="u" name="username" autocomplete="username" autofocus required>
  <label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password" required>
  <button>Sign in</button>
</form></body></html>
"""
let aCode = String(repeating: "A", count: 26)

@Suite struct ConnectProblemTests {
    let lan = try! ServerOrigin(string: "http://192.168.1.20:9871")

    @Test func transportErrorsByCode() {
        func kind(_ code: Int) -> ConnectProblem { ConnectProblem(NSError(domain: urlDomain, code: code), server: lan) }
        #expect(kind(-1004) == .cantConnect(host: "192.168.1.20:9871", detail: "connection refused"))
        #expect(kind(-1003) == .cantConnect(host: "192.168.1.20:9871", detail: "no such host"))
        #expect(kind(-1001).kind == .cantConnect && kind(-1009).kind == .cantConnect && kind(-1005).kind == .cantConnect)
        #expect(kind(-1202) == .certificate(host: "192.168.1.20:9871", detail: "the certificate isn't trusted"))
        #expect(kind(-1201).kind == .certificate && kind(-1200).kind == .certificate && kind(-1203).kind == .certificate)
        #expect(kind(-1017).kind == .notXbin && kind(-1011).kind == .notXbin)
        #expect(kind(-999) == .other("Cancelled."))
        #expect(kind(-1100).kind == .other) // fileDoesNotExist: not ours to explain
        // A socket error (Linux's number and Darwin's).
        #expect(ConnectProblem(NSError(domain: NSPOSIXErrorDomain, code: 111), server: lan).kind == .cantConnect)
        #expect(ConnectProblem(NSError(domain: NSPOSIXErrorDomain, code: 61), server: lan).kind == .cantConnect)
    }

    /// URLError itself (FoundationNetworking's on Linux, Foundation's on
    /// Apple platforms) reads the same through its NSError bridge.
    @Test func urlErrorBridges() {
        #expect(ConnectProblem(URLError(.cannotConnectToHost), server: lan).kind == .cantConnect)
        #expect(ConnectProblem(URLError(.timedOut), server: lan).kind == .cantConnect)
        #expect(ConnectProblem(URLError(.serverCertificateUntrusted), server: lan).kind == .certificate)
        #expect(ConnectProblem(URLError(.badServerResponse), server: lan).kind == .notXbin)
    }

    @Test func routeAnswers() {
        let p = { (e: any Error) in ConnectProblem(e, server: testOrigin) }
        #expect(p(Enrollment.Failure.codeRefused("used")) == .codeRefused(detail: "used"))
        #expect(p(Enrollment.Failure.badCode).kind == .codeRefused)
        #expect(p(Enrollment.Failure.invalidCredentials) == .account("Wrong username or password."))
        #expect(p(Enrollment.Failure.inviteInvalid("x")).kind == .account)
        #expect(p(Enrollment.Failure.passwordRejected("too short")) == .account("The workspace refused that password: too short."))
        #expect(p(Enrollment.Failure.deviceLimit("32 devices")).kind == .account)
        #expect(p(Enrollment.Failure.noAccounts).kind == .account)
        #expect(p(Enrollment.Failure.inviteNeedsBrowser).kind == .notXbin)
        #expect(p(Enrollment.Failure.server(APIError(status: 502, message: "bad gateway"))) == .server(status: 502, message: "bad gateway"))
        #expect(p(SignInError.throttled) == .throttled)
        #expect(p(SignInError.disabled("off")).kind == .account)
        #expect(p(SignInError.ssoRequired).kind == .account)
        #expect(p(APIError(status: 429, message: "slow down")) == .throttled)
        #expect(p(APIError(status: 403, message: "password sign-in is disabled")) == .account("password sign-in is disabled"))
        #expect(p(APIError(status: 404, message: "not found")).kind == .notXbin)
        #expect(p(APIError(status: 418, message: "teapot")) == .other("418: teapot"))
        #expect(p(JSONParseError(offset: 0, reason: "x")).kind == .notXbin)
        #expect(p(DeviceLogin.Invalid(reason: "no token")).kind == .notXbin)
        #expect(p(ConnectProblem.throttled) == .throttled)
        #expect(p(ServerOrigin.Invalid(reason: "no host")) == .other("That isn't a workspace address: no host."))
    }

    @Test func ssoErrors() {
        #expect(ConnectProblem.sso(error: "denied") == .account("Sign-in was cancelled at the identity provider."))
        #expect(ConnectProblem.sso(error: "noaccount").message.contains("allow-list"))
        #expect(ConnectProblem.sso(error: "weird") == .account("Single sign-on failed (weird)."))
    }

    @Test func wording() {
        let c = ConnectProblem(NSError(domain: urlDomain, code: -1004), server: lan)
        #expect(c.title == "Can't connect")
        #expect(c.message.contains("192.168.1.20:9871 (connection refused)"))
        #expect(c.message.contains("address your phone uses") && c.message.contains("settings → add a device"))
        #expect(ConnectProblem.codeRefused(detail: "").title == "Code refused")
        #expect(ConnectProblem.codeRefused(detail: "").message.contains("settings → add a device"))
        #expect(ConnectProblem.notXbin(host: "h", detail: "HTTP 404").message.hasPrefix("h didn't answer like an xbin workspace (HTTP 404)."))
        for k in [ConnectProblem.cantConnect(host: "h", detail: ""), .certificate(host: "h", detail: ""), .notXbin(host: "h", detail: ""),
                  .codeRefused(detail: ""), .account("a"), .throttled, .server(status: 500, message: ""), .other("o")] {
            #expect(!k.title.isEmpty && !k.message.isEmpty && !k.message.contains("()"))
        }
    }
}

@Suite struct LoginMethodsTests {
    @Test func decodesTheRoute() throws {
        let m = try LoginMethods(json: Resources.json("server/login-methods.json"))
        #expect(m == LoginMethods(api: 1, title: "Acme", auth: true, password: true, passwordAdminOnly: false, sso: true,
                                  ssoLabel: "Sign in with Acme", invites: true))
        #expect(throws: DeviceLogin.Invalid.self) { try LoginMethods(json: ["title": "x"]) }
        let bare = try LoginMethods(json: ["api": 1])
        #expect(bare.auth && !bare.password && !bare.sso && bare.ssoLabel == "Sign in with SSO" && !bare.invites)
    }

    @Test func options() {
        let both = SignInOptions(LoginMethods(password: true, sso: true, ssoLabel: "Sign in with Acme"))
        #expect(both.methods == [.password(header: nil), .sso(label: "Sign in with Acme")] && both.explanation == nil)
        let ssoOnly = SignInOptions(LoginMethods(password: true, passwordAdminOnly: true, sso: true, ssoLabel: "Okta"))
        #expect(ssoOnly.methods == [.sso(label: "Okta"), .password(header: "Workspace admin")])
        #expect(SignInOptions(LoginMethods(password: true, sso: false)).methods == [.password(header: nil)])
        #expect(SignInOptions(LoginMethods(password: false, sso: true)).methods == [.sso(label: "Sign in with SSO")])
        let none = SignInOptions(LoginMethods(password: false, sso: false))
        #expect(none.methods.isEmpty && none.explanation?.contains("no accounts yet") == true)
        let noAuth = SignInOptions(LoginMethods(auth: false, password: false, sso: false))
        #expect(noAuth.methods.isEmpty && noAuth.explanation?.contains("without sign-in") == true)
        #expect(SignInOptions(.legacy).methods == [.password(header: nil), .sso(label: "Sign in with SSO")])
        #expect(both.hasPassword && both.ssoLabel == "Sign in with Acme" && !none.hasPassword && none.ssoLabel == nil)
    }

    @Test func probe() async throws {
        func probe(_ r: APIResponse, login: APIResponse? = nil) async throws -> LoginMethods {
            let s = FakeServer()
            await s.on("GET /api/xbin/login/methods") { _ in r }
            if let login { await s.on("GET /login") { _ in login } }
            return try await Enrollment(transport: s, keys: FakeKeys(), clientHeader: "app/1", platform: "ios").loginMethods(server: testOrigin)
        }
        #expect(try await probe(json(200, try Resources.json("server/login-methods.json"))).ssoLabel == "Sign in with Acme")
        // An xbind older than the route: its /api/ gate's 401 (404 without
        // sign-in), and its sign-in page at /login (a redirect to / without).
        let page = text(200, olderLoginPage, "text/html; charset=utf-8")
        #expect(try await probe(text(401, "unauthorized — sign in at /login\n"), login: page) == .legacy)
        let open = try await probe(text(404, "404 page not found\n"), login: APIResponse(status: 302, headers: ["Location": "/"]))
        #expect(open.isLegacy && !open.auth && SignInOptions(open).methods.isEmpty)
        // Not an xbin workspace: the same statuses without xbind's /login…
        for (r, login) in [(text(401, "Authorization Required"), text(200, "<html><form action=\"/signin\"></form></html>", "text/html")),
                           (text(404, "<h1>Not Found</h1>", "text/html"), text(404, "<h1>Not Found</h1>", "text/html")),
                           (json(404, ["error": "nope"]), APIResponse(status: 302, headers: ["Location": "https://elsewhere.example/"]))] {
            do {
                _ = try await probe(r, login: login)
                Issue.record("\(r.status) taken for an older xbind")
            } catch let p as ConnectProblem { #expect(p.kind == .notXbin) }
        }
        // …and a 200 that isn't the route's JSON.
        for r in [text(200, "<html>hello</html>", "text/html"), json(200, ["ok": true])] {
            do {
                _ = try await probe(r)
                Issue.record("a 200 page taken for the route")
            } catch let p as ConnectProblem { #expect(p.kind == .notXbin) }
        }
        do {
            _ = try await probe(APIResponse(status: 302, headers: ["Location": "https://sso.example.com/"]))
            Issue.record("a redirect is not the route")
        } catch let p as ConnectProblem {
            #expect(p == .notXbin(host: "xbin.example.com", detail: "it redirects to https://sso.example.com/"))
        }
        await #expect(throws: ConnectProblem.server(status: 503, message: "the server had a problem (503)")) {
            try await probe(APIResponse(status: 503))
        }
    }

    /// The probe carries no credential and asks for JSON.
    @Test func probeRequest() async throws {
        let s = FakeServer()
        await s.on("GET /api/xbin/login/methods") { _ in json(200, ["api": 1]) }
        _ = try await Enrollment(transport: s, keys: FakeKeys(), clientHeader: "app/1", platform: "ios").loginMethods(server: testOrigin)
        let r = try #require(await s.log.first)
        #expect(r.header("Authorization") == nil && r.header("Accept") == "application/json" && r.header("X-XBin-Client") == "app/1")
    }
}

@Suite struct EnrollCheckedTests {
    /// The QR path spends the code only on a reachable xbin workspace with
    /// sign-in.
    @Test func probesBeforeSpendingTheCode() async throws {
        let keys = FakeKeys()
        // Unreachable: the transport's error, nothing sent after it.
        let down = Enrollment(transport: FailingTransport(error: NSError(domain: urlDomain, code: -1004)), keys: keys,
                              clientHeader: "x", platform: "ios")
        do {
            _ = try await down.enrollChecked(server: testOrigin, code: aCode, deviceName: "p", workspaceID: "w1")
            Issue.record("enrolled without a server")
        } catch {
            #expect(ConnectProblem(error, server: testOrigin).kind == .cantConnect)
        }
        #expect(await keys.keys.isEmpty)

        // Not xbin: no enrollment request.
        let web = FakeServer()
        await web.on("GET /api/xbin/login/methods") { _ in text(200, "<html></html>", "text/html") }
        let e = Enrollment(transport: web, keys: keys, clientHeader: "x", platform: "ios")
        await #expect(throws: ConnectProblem.notXbin(host: "xbin.example.com", detail: "its answer isn't what an xbin workspace sends")) {
            try await e.enrollChecked(server: testOrigin, code: aCode, deviceName: "p", workspaceID: "w2")
        }
        #expect(await web.count("POST /api/xbin/devices/enroll") == 0)

        // No sign-in on that workspace.
        let open = FakeServer()
        await open.on("GET /api/xbin/login/methods") { _ in json(200, ["api": 1, "auth": false]) }
        await #expect(throws: Enrollment.Failure.noAccounts) {
            try await Enrollment(transport: open, keys: keys, clientHeader: "x", platform: "ios")
                .enrollChecked(server: testOrigin, code: aCode, deviceName: "p", workspaceID: "w3")
        }
        #expect(await open.count("POST /api/xbin/devices/enroll") == 0)

        // A code of the wrong shape: refused before any request.
        let none = FakeServer()
        await #expect(throws: Enrollment.Failure.badCode) {
            try await Enrollment(transport: none, keys: keys, clientHeader: "x", platform: "ios")
                .enrollChecked(server: testOrigin, code: "short", deviceName: "p")
        }
        #expect(await none.log.isEmpty)
    }

    /// A refused code after a good probe is "code refused"; an older xbind
    /// (no route) still enrolls.
    @Test func refusedCodeAndOlderServer() async throws {
        let s = FakeServer()
        await s.on("GET /api/xbin/login/methods") { _ in text(401, "unauthorized — sign in at /login\n") }
        await s.on("GET /login") { _ in text(200, olderLoginPage, "text/html") }
        await s.on("POST /api/xbin/devices/enroll") { r in
            body(r)["code"] == "GOODGOODGOODGOODGOODGOODGO"
                ? json(200, ["deviceId": "dev-1", "user": "alice", "origin": "https://xbin.example.com", "name": "P"])
                : json(401, ["error": "invalid, expired or already used enrollment code"])
        }
        await deviceLoginRoutes(s)
        let e = Enrollment(transport: s, keys: FakeKeys(), clientHeader: "x", platform: "ios")
        do {
            _ = try await e.enrollChecked(server: testOrigin, code: aCode, deviceName: "P")
            Issue.record("a refused code enrolled")
        } catch {
            #expect(ConnectProblem(error, server: testOrigin) == .codeRefused(detail: "invalid, expired or already used enrollment code"))
        }
        let (rec, session) = try await e.enrollChecked(server: testOrigin, code: "GOODGOODGOODGOODGOODGOODGO", deviceName: "P")
        #expect(rec.deviceId == "dev-1" && session.kind == .device)
    }

    /// The QR code's address (what the phone talks to) and the origin the
    /// enrollment returns (what logins sign) differ — the "address your
    /// phone uses" case: requests keep going to the address, the signature
    /// covers the origin.
    @Test func signsTheEnrollmentOriginTalksToTheAddress() async throws {
        let s = FakeServer()
        await s.on("GET /api/xbin/login/methods") { _ in json(200, ["api": 1, "password": ["enabled": true]]) }
        await s.on("POST /api/xbin/devices/enroll") { _ in
            json(200, ["deviceId": "dev-1", "user": "alice", "origin": "https://xbin.example.com", "name": "P"])
        }
        await deviceLoginRoutes(s, origin: "https://xbin.example.com")
        let log = OriginLog(s)
        let tunnel = try ServerOrigin(string: "http://127.0.0.1:9875")
        let (rec, session) = try await Enrollment(transport: log, keys: FakeKeys(), clientHeader: "x", platform: "ios")
            .enrollChecked(server: tunnel, code: aCode, deviceName: "P", workspaceID: "w")
        #expect(session.kind == .device)
        #expect(rec.server == tunnel && rec.signedOrigin == "https://xbin.example.com")
        let origins = await log.origins
        #expect(origins.allSatisfy { $0 == "http://127.0.0.1:9875" } && origins.count == 4)
    }
}

@Suite struct InviteTests {
    func server() async -> FakeServer {
        let s = FakeServer()
        let spent = Box(false)
        await s.on("POST /api/xbin/invite/check") { r in
            guard body(r)["invite"] == "inv-ok", !spent.value else { return json(403, ["error": "invalid or expired invite"]) }
            return json(200, try! Resources.json("server/invite-check.json"))
        }
        await s.on("POST /api/xbin/invite/redeem") { r in
            let b = body(r)
            guard b["invite"] == "inv-ok", !spent.value else { return json(403, ["error": "invalid or expired invite"]) }
            guard (b["password"]?.stringValue ?? "").count >= 8 else {
                return json(400, ["error": "password too short (min 8 characters)"])
            }
            spent.value = true
            return json(200, ["token": "tok-carol", "tokenType": "Bearer", "user": ["id": "carol", "name": "Carol Example", "role": "user"],
                              "expiresIdle": 1_790_043_200, "expiresMax": 1_792_592_000])
        }
        await s.on("POST /api/xbin/devices/enroll-code") { r in
            r.header("Authorization") == "Bearer tok-carol" ? json(200, try! Resources.json("server/enroll-code.json")) : json(401, [:])
        }
        await s.on("POST /api/xbin/devices/enroll") { _ in
            json(200, ["deviceId": "dev-1", "user": "carol", "origin": "https://xbin.example.com", "name": "P"])
        }
        await s.on("POST /logout") { _ in APIResponse(status: 204) }
        await deviceLoginRoutes(s)
        return s
    }

    @Test func checkRedeemEnroll() async throws {
        let s = await server()
        let e = Enrollment(transport: s, keys: FakeKeys(), clientHeader: "x", platform: "ios")
        let info = try await e.checkInvite(server: testOrigin, invite: "inv-ok")
        #expect(info == InviteInfo(userID: "carol", userName: "Carol Example", expires: Date(timeIntervalSince1970: 1_790_259_200), title: "Acme"))
        await #expect(throws: Enrollment.Failure.passwordRejected("password too short (min 8 characters)")) {
            try await e.redeemInvite(server: testOrigin, invite: "inv-ok", password: "short")
        }
        let session = try await e.redeemInvite(server: testOrigin, invite: "inv-ok", password: "a long password")
        #expect(session.kind == .password && session.userID == "carol")
        let (rec, dev) = try await e.enrollSignedIn(server: testOrigin, session: session, deviceName: "P", password: "a long password")
        #expect(rec.deviceId == "dev-1" && dev.kind == .device)
        // Spent.
        await #expect(throws: Enrollment.Failure.inviteInvalid("invalid or expired invite")) {
            try await e.checkInvite(server: testOrigin, invite: "inv-ok")
        }
    }

    @Test func olderAndOtherServers() async throws {
        let old = FakeServer()
        await old.on("POST /api/xbin/invite/check") { _ in text(401, "unauthorized — sign in at /login\n") }
        await old.on("GET /login") { _ in text(200, olderLoginPage, "text/html") }
        await #expect(throws: Enrollment.Failure.inviteNeedsBrowser) {
            try await Enrollment(transport: old, keys: FakeKeys(), clientHeader: "x", platform: "ios").checkInvite(server: testOrigin, invite: "i")
        }
        let web = FakeServer()
        await web.on("POST /api/xbin/invite/check") { _ in text(404, "<h1>nope</h1>", "text/html") }
        await #expect(throws: ConnectProblem.notXbin(host: "xbin.example.com", detail: "HTTP 404")) {
            try await Enrollment(transport: web, keys: FakeKeys(), clientHeader: "x", platform: "ios").checkInvite(server: testOrigin, invite: "i")
        }
        let busy = FakeServer()
        await busy.on("POST /api/xbin/invite/check") { _ in json(429, ["error": "too many attempts"]) }
        do {
            _ = try await Enrollment(transport: busy, keys: FakeKeys(), clientHeader: "x", platform: "ios").checkInvite(server: testOrigin, invite: "i")
        } catch {
            #expect(ConnectProblem(error, server: testOrigin) == .throttled)
        }
        let open = FakeServer()
        await open.on("POST /api/xbin/invite/check") { _ in json(403, ["error": "this workspace runs without sign-in — open it directly"]) }
        await #expect(throws: Enrollment.Failure.noAccounts) {
            try await Enrollment(transport: open, keys: FakeKeys(), clientHeader: "x", platform: "ios").checkInvite(server: testOrigin, invite: "i")
        }
    }
}

@Suite struct SignInLinkTests {
    @Test func kinds() throws {
        #expect(try SignInLink("xbin://enroll?u=https%3A%2F%2Fxbin.example.com&c=K3D") == .enroll(server: testOrigin, code: "K3D"))
        #expect(try SignInLink("  https://xbin.example.com/login?invite=abc_DEF-1  ") == .invite(server: testOrigin, token: "abc_DEF-1"))
        let local = try ServerOrigin(string: "http://127.0.0.1:9875")
        #expect(try SignInLink("http://127.0.0.1:9875/login/?invite=t") == .invite(server: local, token: "t"))
        #expect(try SignInLink("xbin.example.com") == .address(testOrigin))
        #expect(try SignInLink("https://xbin.example.com/c/root/") == .address(testOrigin))
        #expect(try SignInLink("http://127.0.0.1:9875").server == local)
    }

    @Test func refusals() {
        for bad in ["", "xbin://sso?ticket=x", "xbin://abc/c/apps/x", "https://xbin.example.com/other?invite=t",
                    "https://u:p@xbin.example.com/login?invite=t", "not a link", "ftp://x/login?invite=t"] {
            #expect(throws: SignInLink.Invalid.self, "\(bad)") { try SignInLink(bad) }
        }
        #expect(throws: SignInLink.Invalid.self) { try SignInLink("https://xbin.example.com", allowAddress: false) }
        #expect(throws: SignInLink.Invalid.self) { try SignInLink("xbin.example.com", allowAddress: false) }
    }
}

@Suite struct SignedOriginTests {
    @Test func oldRecordsSignTheirServer() throws {
        let old = #"{"workspaces":[{"id":"w","server":"https://xbin.example.com:8443","user":{"id":"u"},"deviceId":"dev-1"}]}"#
        let w = try #require(try WorkspaceList.decode(Data(old.utf8)).workspaces.first)
        #expect(w.deviceOrigin == nil && w.signedOrigin == "https://xbin.example.com:8443")
        #expect(try w.deviceLoginMessage(nonce: "n") == DeviceLogin.message(origin: "https://xbin.example.com:8443", deviceId: "dev-1", nonce: "n"))
        // Encoding keeps the absent field absent (no migration needed).
        #expect(try JSONValue(parsing: JSONEncoder().encode(w))["deviceOrigin"] == nil)
    }
}
