import Foundation
import Testing
@testable import XbinCore

@Suite struct ClientAuthTests {
    @Test func apiErrorReadsSignInFields() {
        let e = APIError(json(403, ["error": "sign-in too old", "stepUp": "password"]))
        #expect(e.status == 403 && e.message == "sign-in too old" && e.stepUp == "password" && e.reauth == nil)
        let s = APIError(json(403, ["error": "sso", "reauth": "sso"]))
        #expect(s.reauth == "sso")
        let plain = APIError(APIResponse(status: 502, body: Data("bad gateway\n".utf8)))
        #expect(plain.message == "bad gateway")
        let empty = APIError(APIResponse(status: 429))
        #expect(empty.message.contains("too many"))
    }

    @Test func requestHeadersAreCaseInsensitive() {
        var r = APIRequest("get", "/x", headers: ["authorization": "a"])
        r.setHeader("Authorization", "b")
        #expect(r.method == "GET" && r.headers == ["Authorization": "b"] && r.header("AUTHORIZATION") == "b")
        #expect(r.url(on: testOrigin)?.absoluteString == "https://xbin.example.com/x")
        #expect(URLComponent.encode("apps/dev box?&") == "apps%2Fdev%20box%3F%26")
        #expect(URLComponent.encodePath("/apps/dev box/") == "apps/dev%20box")
    }

    /// No session yet: the first request signs in with the device, attaches
    /// the bearer and the client header, and remembers the user.
    @Test func firstRequestSignsIn() async throws {
        let s = FakeServer()
        await deviceLoginRoutes(s)
        await s.on("GET /api/xbin/whoami") { r in
            r.header("Authorization") == "Bearer tok-1" ? json(200, ["id": "alice"]) : json(401, ["error": "no"])
        }
        let keys = FakeKeys()
        await keys.add(enrolledRecord().id)
        let store = MemorySessionStore()
        let auth = WorkspaceAuth(record: enrolledRecord(), transport: s, keys: keys, sessions: store, clientHeader: "app/1.0")
        let r = try await auth.call(APIRequest("GET", "/api/xbin/whoami"))
        #expect(try r.json()["id"] == "alice")
        let log = await s.log
        #expect(log.map(\.path) == ["/login/device/challenge", "/login/device", "/api/xbin/whoami"])
        #expect(log.allSatisfy { $0.header("X-XBin-Client") == "app/1.0" })
        #expect(log[0].header("Authorization") == nil && log[1].header("Authorization") == nil)
        #expect(await store.loadSession(workspace: enrolledRecord().id)?.token == "tok-1")
        #expect(await auth.record.user.name == "Alice")
        #expect(await keys.prompts == 1)
    }

    /// Many requests hit the 401 of a dead session together (an xbind
    /// restart): one sign-in, one prompt, every request retried once.
    @Test func concurrent401sShareOneSignIn() async throws {
        let s = FakeServer()
        let logins = Box(0)
        await deviceLoginRoutes(s, logins: logins, delay: 20_000_000)
        await s.on("GET /api/xbin/components") { r in
            r.header("Authorization") == "Bearer tok-1" ? json(200, []) : json(401, ["error": "signed out"])
        }
        let keys = FakeKeys()
        await keys.add(enrolledRecord().id)
        let store = MemorySessionStore()
        await store.saveSession(SessionCredential(token: "dead", kind: .device), workspace: enrolledRecord().id)
        let auth = WorkspaceAuth(record: enrolledRecord(), transport: s, keys: keys, sessions: store, clientHeader: "app/1")
        let statuses = try await withThrowingTaskGroup(of: Int.self) { g in
            for _ in 0..<8 { g.addTask { try await auth.send(APIRequest("GET", "/api/xbin/components")).status } }
            return try await g.reduce(into: [Int]()) { $0.append($1) }
        }
        #expect(statuses == Array(repeating: 200, count: 8))
        #expect(logins.value == 1)
        #expect(await keys.prompts == 1)
        #expect(await s.count("POST /login/device/challenge") == 1)
    }

    /// A 401 after a successful re-sign is returned, not retried forever.
    @Test func noRetryLoop() async throws {
        let s = FakeServer()
        await deviceLoginRoutes(s)
        await s.on("GET /api/xbin/secret") { _ in json(401, ["error": "nope"]) }
        let keys = FakeKeys()
        await keys.add(enrolledRecord().id)
        let store = MemorySessionStore()
        await store.saveSession(SessionCredential(token: "stale", kind: .device), workspace: enrolledRecord().id)
        let auth = WorkspaceAuth(record: enrolledRecord(), transport: s, keys: keys, sessions: store, clientHeader: "app/1")
        let r = try await auth.send(APIRequest("GET", "/api/xbin/secret"))
        #expect(r.status == 401)
        #expect(await s.count("GET /api/xbin/secret") == 2)            // stale → sign in → one retry
        #expect(await keys.prompts == 1)
        // A request that signs in first and still gets 401 isn't retried.
        let fresh = WorkspaceAuth(record: enrolledRecord(), transport: s, keys: keys, sessions: MemorySessionStore(),
                                  clientHeader: "app/1")
        #expect(try await fresh.send(APIRequest("GET", "/api/xbin/secret")).status == 401)
        #expect(await s.count("GET /api/xbin/secret") == 3)
        #expect(await keys.prompts == 2)
    }

    @Test func signInFailuresAreTyped() async throws {
        let keys = FakeKeys()
        await keys.add(enrolledRecord().id)
        // 404 on the challenge: the device was removed.
        let gone = FakeServer()
        await deviceLoginRoutes(gone, deviceId: "dev-other")
        let a = WorkspaceAuth(record: enrolledRecord(), transport: gone, keys: keys, sessions: MemorySessionStore(), clientHeader: "x")
        await #expect(throws: SignInError.deviceRemoved) { try await a.signIn() }

        // 403 {reauth:"sso"} and a plain 403.
        for (resp, want) in [(json(403, ["error": "sso", "reauth": "sso"]), SignInError.ssoRequired),
                             (json(403, ["error": "account disabled"]), SignInError.disabled("account disabled")),
                             (json(401, ["error": "signature"]), SignInError.rejected("signature")),
                             (json(429, ["error": "slow down"]), SignInError.throttled)] {
            let s = FakeServer()
            await deviceLoginRoutes(s)
            await s.on("POST /login/device") { _ in resp }
            let b = WorkspaceAuth(record: enrolledRecord(), transport: s, keys: keys, sessions: MemorySessionStore(), clientHeader: "x")
            await #expect(throws: want) { try await b.signIn() }
        }

        // The user cancels Face ID.
        let s = FakeServer()
        await deviceLoginRoutes(s)
        await keys.setFailNext(.cancelled)
        let c = WorkspaceAuth(record: enrolledRecord(), transport: s, keys: keys, sessions: MemorySessionStore(), clientHeader: "x")
        await #expect(throws: SignInError.cancelled) { try await c.signIn() }
        #expect(SignInError.deviceRemoved.needsEnrollment && !SignInError.cancelled.needsEnrollment)
    }

    /// Without a device (a development token), a 401 ends the session.
    @Test func tokenSessionCannotResign() async throws {
        let s = FakeServer()
        await s.on("GET /api/xbin/whoami") { _ in json(401, ["error": "bad token"]) }
        var rec = enrolledRecord()
        rec.deviceId = nil
        let store = MemorySessionStore()
        await store.saveSession(SessionCredential(token: "t", kind: .token), workspace: rec.id)
        let auth = WorkspaceAuth(record: rec, transport: s, keys: FakeKeys(), sessions: store, clientHeader: "x")
        #expect(await auth.canResign == false)
        let r = try await auth.send(APIRequest("GET", "/api/xbin/whoami"))
        #expect(r.status == 401)
        #expect(await auth.credential == nil)
        await #expect(throws: SignInError.notEnrolled) { try await auth.signIn() }
    }

    @Test func expiredSessionIsRenewedBeforeUse() async throws {
        let s = FakeServer()
        await deviceLoginRoutes(s)
        await s.on("GET /x") { r in json(r.header("Authorization") == "Bearer tok-1" ? 200 : 401, [:]) }
        let keys = FakeKeys()
        await keys.add(enrolledRecord().id)
        let store = MemorySessionStore()
        let clock = TestClock()
        await store.saveSession(SessionCredential(token: "old", kind: .device, expiresIdle: clock.now.addingTimeInterval(-1)),
                                workspace: enrolledRecord().id)
        let auth = WorkspaceAuth(record: enrolledRecord(), transport: s, keys: keys, sessions: store, clientHeader: "x",
                                 now: { clock.now })
        #expect(try await auth.send(APIRequest("GET", "/x")).status == 200)
        #expect(await s.count("GET /x") == 1) // no wasted 401 round trip
    }

    @Test func signOutPostsLogoutAndForgets() async throws {
        let s = FakeServer()
        let seen = Box<String?>(nil)
        await s.on("POST /logout") { r in seen.value = r.header("Authorization"); return APIResponse(status: 204) }
        let store = MemorySessionStore()
        await store.saveSession(SessionCredential(token: "t1", kind: .device), workspace: enrolledRecord().id)
        let auth = WorkspaceAuth(record: enrolledRecord(), transport: s, keys: FakeKeys(), sessions: store, clientHeader: "x")
        await auth.signOut()
        #expect(seen.value == "Bearer t1")
        #expect(await store.loadSession(workspace: enrolledRecord().id) == nil)
    }

    // MARK: Enrollment

    @Test func enrollRedeemsTheCodeThenSignsIn() async throws {
        let s = FakeServer()
        let seen = Box<JSONValue>(.null)
        await s.on("POST /api/xbin/devices/enroll") { r in
            seen.value = body(r)
            return json(200, ["deviceId": "dev-1", "user": "alice", "origin": "https://xbin.example.com", "name": "Phone"])
        }
        await deviceLoginRoutes(s)
        let keys = FakeKeys()
        let e = Enrollment(transport: s, keys: keys, clientHeader: "app/1", platform: "ios")
        let (rec, session) = try await e.enroll(server: testOrigin, code: "k3dq-aaaa bbbb cccc dddd eeee ff",
                                                deviceName: "Phone", workspaceID: "abc")
        #expect(seen.value["code"] == "K3DQAAAABBBBCCCCDDDDEEEEFF")
        #expect(seen.value["platform"] == "ios" && seen.value["name"] == "Phone")
        #expect(seen.value["publicKey"]?.stringValue.flatMap(Base64URL.decode) == keys.spki)
        #expect(rec.id == "abc" && rec.deviceId == "dev-1" && rec.deviceOrigin == "https://xbin.example.com")
        #expect(session.token == "tok-1" && session.kind == .device && session.userName == "Alice")
        #expect(await keys.hasKey(workspace: "abc"))
    }

    @Test func enrollFailuresDropTheKey() async throws {
        let keys = FakeKeys()
        let e0 = Enrollment(transport: FakeServer(), keys: keys, clientHeader: "x", platform: "ios")
        await #expect(throws: Enrollment.Failure.badCode) {
            try await e0.enroll(server: testOrigin, code: "short", deviceName: "p")
        }
        for (status, want) in [(401, Enrollment.Failure.codeRefused("used")), (400, .badKey("used")), (409, .deviceLimit("used"))] {
            let s = FakeServer()
            await s.on("POST /api/xbin/devices/enroll") { _ in json(status, ["error": "used"]) }
            let e = Enrollment(transport: s, keys: keys, clientHeader: "x", platform: "ios")
            await #expect(throws: want) {
                try await e.enroll(server: testOrigin, code: String(repeating: "A", count: 26), deviceName: "p", workspaceID: "w\(status)")
            }
            #expect(await keys.hasKey(workspace: "w\(status)") == false)
        }
    }

    @Test func enrollAfterPasswordSignIn() async throws {
        let s = FakeServer()
        await s.on("POST /api/xbin/login") { r in
            body(r)["password"] == "pw" ? json(200, try! Resources.json("server/login.json")) : json(401, ["error": "invalid"])
        }
        let minted = Box(0)
        await s.on("POST /api/xbin/devices/enroll-code") { r in
            guard r.header("Authorization") == "Bearer 0123456789abcdef-session-token" else { return json(401, [:]) }
            minted.mutate { $0 += 1 }
            if minted.value == 1 { return json(403, ["error": "too old", "stepUp": "password"]) }
            guard body(r)["password"] == "pw" else { return json(403, ["error": "wrong", "stepUp": "password"]) }
            return json(200, try! Resources.json("server/enroll-code.json"))
        }
        await s.on("POST /api/xbin/devices/enroll") { r in
            body(r)["code"] == "5LFWAATWHIMB7TQTXQUOCXCR7U"
                ? json(200, ["deviceId": "dev-1", "user": "admin", "origin": "https://xbin.example.com", "name": "P"])
                : json(401, ["error": "code"])
        }
        await deviceLoginRoutes(s)
        let loggedOut = Box(false)
        await s.on("POST /logout") { _ in loggedOut.value = true; return APIResponse(status: 204) }
        let e = Enrollment(transport: s, keys: FakeKeys(), clientHeader: "x", platform: "ios")
        await #expect(throws: Enrollment.Failure.invalidCredentials) {
            try await e.passwordLogin(server: testOrigin, username: "admin", password: "no")
        }
        let pw = try await e.passwordLogin(server: testOrigin, username: "admin", password: "pw")
        #expect(pw.kind == .password && pw.userID == "admin" && pw.role == "admin")
        await #expect(throws: Enrollment.Failure.stepUp("password")) {
            try await e.enrollSignedIn(server: testOrigin, session: pw, deviceName: "P")
        }
        let (rec, dev) = try await e.enrollSignedIn(server: testOrigin, session: pw, deviceName: "P", password: "pw")
        #expect(rec.deviceId == "dev-1" && dev.kind == .device)
        #expect(loggedOut.value)
    }

    @Test func tokenLoginReadsWhoami() async throws {
        let s = FakeServer()
        await s.on("GET /api/xbin/whoami") { r in
            r.header("Authorization") == "Bearer owner-token" ? json(200, try! Resources.json("server/whoami.json")) : json(401, [:])
        }
        let e = Enrollment(transport: s, keys: FakeKeys(), clientHeader: "x", platform: "ios")
        let (rec, c) = try await e.tokenLogin(server: testOrigin, token: "owner-token", workspaceID: "w")
        #expect(rec.user.id == "admin" && rec.user.name == "Dev Admin" && rec.deviceId == nil)
        #expect(c.kind == .token && c.role == "admin")
        await #expect(throws: Enrollment.Failure.invalidCredentials) {
            try await e.tokenLogin(server: testOrigin, token: "x")
        }
    }

    @Test func credentialExpiry() {
        let now = Date(timeIntervalSince1970: 1000)
        #expect(!SessionCredential(token: "t", kind: .device).isExpired(at: now))
        #expect(SessionCredential(token: "t", kind: .device, expiresIdle: now).isExpired(at: now))
        #expect(SessionCredential(token: "t", kind: .device, expiresMax: now.addingTimeInterval(-1)).isExpired(at: now))
        let s = SessionCredential(try! AppSession(json: Resources.json("server/login.json")), kind: .password)
        #expect(s.expiresIdle == Date(timeIntervalSince1970: 1_790_451_918) && s.userName == "Dev Admin")
        let round = try! JSONDecoder().decode(SessionCredential.self, from: JSONEncoder().encode(s))
        #expect(round == s)
    }
}
