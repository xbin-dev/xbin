import Foundation
import Testing
@testable import XbinCore

@Suite struct ClientHandoffTests {
    let origin = try! ServerOrigin(string: "https://ws.example.com")

    @Test func nextIsASameOriginPath() {
        #expect(WebTicket.cleanNext("/c/apps/x/") == "/c/apps/x/")
        #expect(WebTicket.cleanNext("  /c/apps/x/#frag ") == "/c/apps/x/#frag")
        for bad in ["//evil.example/", "https://evil.example/", "c/apps/x", "", "/\\evil", "/a\nb", "javascript:alert(1)"] {
            #expect(WebTicket.cleanNext(bad) == "/", "\(bad)")
        }
        let r = WebTicket.request(next: "/c/apps/x/")
        #expect(r.method == "POST" && r.path == "/api/xbin/web-ticket" && body(r) == ["next": "/c/apps/x/"])
        #expect(r.header("content-type") == "application/json")
        #expect(body(WebTicket.request(next: "//evil")) == ["next": "/"])
    }

    @Test func ticketURLOnTheWorkspaceOrigin() {
        let next = "/c/apps/x/"
        // Relative and absolute same-origin answers are used as they are.
        let rel = WebTicket.destination(json(200, ["url": "/login?ticket=T1&next=%2Fc%2Fapps%2Fx%2F"]), origin: origin, next: next)
        #expect(rel == .init(url: URL(string: "https://ws.example.com/login?ticket=T1&next=%2Fc%2Fapps%2Fx%2F")!, fellBack: false))
        let abs = WebTicket.destination(json(200, ["url": "https://WS.example.com:443/login?ticket=T2"]), origin: origin, next: next)
        #expect(abs?.url.absoluteString == "https://WS.example.com:443/login?ticket=T2" && abs?.fellBack == false)
        // A bare ticket is redeemed at /login.
        let t = WebTicket.destination(json(200, ["ticket": "a b"]), origin: origin, next: next)
        #expect(t == .init(url: URL(string: "https://ws.example.com/login?ticket=a%20b&next=%2Fc%2Fapps%2Fx%2F")!, fellBack: false))
    }

    /// Anything but a same-origin ticket: the plain page (the user signs in
    /// in Safari, as before the route existed).
    @Test func fallsBackToThePlainPage() {
        let plain = WebTicket.Destination(url: URL(string: "https://ws.example.com/c/apps/x/")!, fellBack: true)
        let next = "/c/apps/x/"
        #expect(WebTicket.destination(nil, origin: origin, next: next) == plain)                         // network error
        #expect(WebTicket.destination(json(404, ["error": "no route"]), origin: origin, next: next) == plain)  // older xbind
        #expect(WebTicket.destination(APIResponse(status: 405), origin: origin, next: next) == plain)
        #expect(WebTicket.destination(json(403, ["error": "device sessions only"]), origin: origin, next: next) == plain)
        #expect(WebTicket.destination(json(200, [:]), origin: origin, next: next) == plain)
        #expect(WebTicket.destination(APIResponse(status: 200, body: Data("nope".utf8)), origin: origin, next: next) == plain)
        for evil in ["https://evil.example/login?ticket=x", "//evil.example/login", "http://ws.example.com/login?ticket=x",
                     "https://ws.example.com:8443/login", "https://user@ws.example.com/login", "javascript:alert(1)"] {
            #expect(WebTicket.destination(json(200, ["url": .string(evil)]), origin: origin, next: next) == plain, "\(evil)")
        }
        // A bad next falls back to the workspace's root.
        #expect(WebTicket.destination(nil, origin: origin, next: "https://evil")?.url.absoluteString == "https://ws.example.com/")
    }

    @Test func handoffPagesAndLinks() throws {
        #expect(HandoffLink.tilePage(origin: origin, tile: "apps/dev box")?.absoluteString == "https://ws.example.com/c/apps/dev%20box/")
        #expect(HandoffLink.tilePage(origin: origin, tile: "apps/x", subpath: "../../admin/", fragment: "day%3D2")?.absoluteString
            == "https://ws.example.com/c/apps/x/admin/#day%3D2")
        #expect(HandoffLink.shell(origin: origin)?.absoluteString == "https://ws.example.com/")
        let lan = try ServerOrigin(string: "http://192.168.1.4:9461")
        let link = HandoffLink.tile(origin: lan, tile: "apps/x", fragment: "a")
        #expect(link.string == "xbin://192.168.1.4:9461/c/apps/x#a")
        // Another device's app resolves the workspace by host[:port].
        let rec = WorkspaceRecord(id: "4f1c", server: lan, user: WorkspaceUser(id: "alice"))
        #expect(rec.matches(linkWorkspace: link.workspace!))
        let info = HandoffLink.userInfo(HandoffLink.agent(origin: lan, session: "s-1"))
        #expect(info == ["link": "xbin://192.168.1.4:9461/agent/s-1"])
        #expect(HandoffLink.link(fromUserInfo: info) == .agent(workspace: "192.168.1.4:9461", session: "s-1"))
        #expect(HandoffLink.link(fromUserInfo: HandoffLink.userInfo(HandoffLink.terminal(origin: origin, session: "t")))
            == .terminal(workspace: "ws.example.com", session: "t"))
        // Never an enrollment or SSO link from an activity; junk is nothing.
        #expect(HandoffLink.link(fromUserInfo: ["link": "xbin://enroll?u=https%3A%2F%2Fa.b&c=AAAA"]) == nil)
        #expect(HandoffLink.link(fromUserInfo: ["link": "xbin://sso?ticket=t"]) == nil)
        #expect(HandoffLink.link(fromUserInfo: ["link": "https://ws.example.com/"]) == nil)
        #expect(HandoffLink.link(fromUserInfo: ["link": 7]) == nil)
        #expect(HandoffLink.link(fromUserInfo: nil) == nil)
    }

    // MARK: Add another device

    static let code = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

    @Test func mintsAnEnrollmentCode() {
        #expect(AddDevice.request().path == "/api/xbin/devices/enroll-code")
        #expect(body(AddDevice.request()) == [:])
        #expect(body(AddDevice.request(password: "pw")) == ["password": "pw"])
        let link = "xbin://enroll?u=https%3A%2F%2Fws.example.com&c=\(Self.code)"
        let ok = AddDevice.outcome(json(200, ["code": .string(Self.code), "url": .string(link), "origin": "https://ws.example.com",
                                              "expires": 1_790_000_600]), origin: origin)
        #expect(ok == .code(link: link, code: Self.code, expires: Date(timeIntervalSince1970: 1_790_000_600)))
        // The QR code's text is an enroll link the new device's app accepts.
        if case .code(let l, _, _) = ok { #expect(try! DeepLink(string: l) == .enroll(server: origin, code: Self.code)) }
    }

    @Test func buildsTheLinkWhenTheServerSentNone() {
        let noURL = AddDevice.outcome(json(200, ["code": .string(Self.code), "origin": "https://ws.example.com"]), origin: origin)
        #expect(noURL == .code(link: "xbin://enroll?u=https%3A%2F%2Fws.example.com&c=\(Self.code)", code: Self.code, expires: nil))
        // A url that isn't an enroll link is not trusted as the QR text.
        let odd = AddDevice.outcome(json(200, ["code": .string(Self.code), "url": "https://evil.example/"]), origin: origin)
        #expect(odd == .code(link: "xbin://enroll?u=https%3A%2F%2Fws.example.com&c=\(Self.code)", code: Self.code, expires: nil))
    }

    @Test func stepUpAndRefusals() {
        #expect(AddDevice.outcome(json(403, ["error": "sign in again", "stepUp": "signin"]), origin: origin) == .signInAgain)
        #expect(AddDevice.outcome(json(403, ["error": "confirm", "stepUp": "password"]), origin: origin) == .needsPassword)
        #expect(AddDevice.outcome(json(403, ["error": "not a user"]), origin: origin) == .refused("not a user"))
        if case .failed(let e) = AddDevice.outcome(json(429, ["error": "slow down"]), origin: origin) {
            #expect(e.status == 429)
        } else { Issue.record("429 should fail") }
        if case .failed(let e) = AddDevice.outcome(json(200, ["nope": 1]), origin: origin) {
            #expect(e.status == 500)
        } else { Issue.record("no code should fail") }
    }

    @Test func groupsTheCodeForReading() {
        #expect(AddDevice.grouped(Self.code) == "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ")
        #expect(AddDevice.grouped("abcd efgh") == "ABCD-EFGH")
        #expect(AddDevice.grouped("") == "")
    }
}
