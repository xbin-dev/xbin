import Foundation
import Testing
@testable import XbinCore

@Suite struct DeepLinkTests {
    @Test func tileLinks() throws {
        #expect(try DeepLink(string: "xbin://3f2a9c1e-0000-4000-8000-000000000001/c/apps/devbox")
            == .tile(workspace: "3f2a9c1e-0000-4000-8000-000000000001", tile: "apps/devbox", fragment: nil))
        #expect(try DeepLink(string: "xbin://ws1/c/apps/agent/#c=abc%20d&x=1")
            == .tile(workspace: "ws1", tile: "apps/agent", fragment: "c=abc%20d&x=1"))
        #expect(try DeepLink(string: "xbin://ws1/c/apps/my%20tile#") == .tile(workspace: "ws1", tile: "apps/my tile", fragment: nil))
        #expect(try DeepLink(string: "XBIN://WS1/c/a") == .tile(workspace: "ws1", tile: "a", fragment: nil))
        #expect(try DeepLink(string: "xbin://xbin.example.com:8443/c/apps/x")
            == .tile(workspace: "xbin.example.com:8443", tile: "apps/x", fragment: nil))
    }

    @Test func sessionAndWorkspaceLinks() throws {
        #expect(try DeepLink(string: "xbin://ws1/term/s-9f3") == .terminal(workspace: "ws1", session: "s-9f3"))
        #expect(try DeepLink(string: "xbin://ws1/agent/a1/") == .agent(workspace: "ws1", session: "a1"))
        #expect(try DeepLink(string: "xbin://ws1") == .workspace("ws1"))
        #expect(try DeepLink(string: "xbin://ws1/") == .workspace("ws1"))
        #expect(try DeepLink(string: "xbin://ws1/term/s1").workspace == "ws1")
    }

    @Test func enrollAndSSO() throws {
        let e = try DeepLink(string: "xbin://enroll?u=https%3A%2F%2FXbin.Example.com%2F&c=K7QX-22PM")
        #expect(e == .enroll(server: try ServerOrigin(string: "https://xbin.example.com"), code: "K7QX-22PM"))
        #expect(e.workspace == nil)
        // An unencoded u works as long as it has no query of its own.
        #expect(try DeepLink(string: "xbin://enroll?u=http://10.0.0.5:8080&c=abc")
            == .enroll(server: try ServerOrigin(string: "http://10.0.0.5:8080"), code: "abc"))
        #expect(try DeepLink(string: "xbin://sso?ticket=t_abc-123") == .sso(ticket: "t_abc-123"))
        #expect(try DeepLink(url: URL(string: "xbin://sso?ticket=x&state=y")!) == .sso(ticket: "x"))
        #expect(try DeepLink(string: "xbin://sso?error=noaccount") == .ssoError(code: "noaccount"))
        #expect(try DeepLink(string: "xbin://sso?error=denied").workspace == nil)
        // What xbind's enroll-code route builds (Go's url.QueryEscape of the origin).
        #expect(try DeepLink(string: "xbin://enroll?u=https%3A%2F%2Fxbin.example.com%3A8443&c=K3DQ7XYZABCDEFGHIJKLMNOPQR")
            == .enroll(server: try ServerOrigin(string: "https://xbin.example.com:8443"), code: "K3DQ7XYZABCDEFGHIJKLMNOPQR"))
    }

    @Test(arguments: [
        "https://ws1/c/apps/x", "xbin:", "xbin://", "xbin:///c/x", "xbin://ws1/c", "xbin://ws1/c/", "xbin://ws1/x/y",
        "xbin://ws1/term", "xbin://ws1/term/a/b", "xbin://ws1/agent/", "xbin://ws1/c/a//b", "xbin://ws1/c/a/../b",
        "xbin://ws1/c/./a", "xbin://enroll?c=abc", "xbin://enroll?u=https://x", "xbin://enroll?u=&c=x",
        "xbin://enroll?u=javascript:alert(1)&c=x", "xbin://enroll?u=ftp%3A%2F%2Fx&c=x",
        "xbin://enroll?u=https%3A%2F%2Fgood%40evil.com&c=x", "xbin://enroll?u=https%3A%2F%2Fx%2Fpath&c=x",
        "xbin://sso", "xbin://sso?ticket=", "xbin://sso?error=", "xbin://ws1/c/a%2Fb",
    ])
    func rejects(_ s: String) {
        #expect(throws: DeepLink.Invalid.self) { try DeepLink(string: s) }
    }

    @Test func roundTrips() throws {
        let links: [DeepLink] = [
            .workspace("ws1"),
            .tile(workspace: "ws1", tile: "apps/devbox", fragment: nil),
            .tile(workspace: "xbin.example.com:8443", tile: "orgs/acme/agent", fragment: "c=abc&auto=kind:id"),
            .tile(workspace: "ws1", tile: "apps/my tile+ü", fragment: "join=t%20k"),
            .terminal(workspace: "ws1", session: "s 1?#%"),
            .agent(workspace: "ws1", session: "a1"),
            .enroll(server: try ServerOrigin(string: "https://xbin.example.com:8443"), code: "a&b=c d+e"),
            .enroll(server: try ServerOrigin(string: "http://[::1]:9000"), code: "x"),
            .sso(ticket: "t+/=?&#"),
            .ssoError(code: "denied"),
        ]
        for link in links {
            #expect(try DeepLink(string: link.string) == link, "\(link.string)")
            #expect(try DeepLink(url: link.url) == link)
        }
        #expect(DeepLink.tile(workspace: "ws1", tile: "apps/x", fragment: "c=1").string == "xbin://ws1/c/apps/x#c=1")
        #expect(DeepLink.enroll(server: try ServerOrigin(string: "https://h"), code: "c1").string == "xbin://enroll?u=https%3A%2F%2Fh&c=c1")
    }

    @Test func workspaceResolution() throws {
        let a = WorkspaceRecord(id: "AAAA-1", server: try ServerOrigin(string: "https://xbin.example.com"), user: .init(id: "alice"),
                                addedAt: Date(unixMillis: 1_000), lastUsedAt: Date(unixMillis: 5_000))
        let b = WorkspaceRecord(id: "bbbb-2", server: try ServerOrigin(string: "https://xbin.example.com"), user: .init(id: "bob"),
                                addedAt: Date(unixMillis: 2_000), lastUsedAt: Date(unixMillis: 9_000))
        let c = WorkspaceRecord(id: "cccc-3", server: try ServerOrigin(string: "http://10.0.0.5:8080"), user: .init(id: "alice"))
        let list = WorkspaceList(workspaces: [a, b, c])
        #expect(a.id == "aaaa-1")
        #expect(a.matches(linkWorkspace: "AAAA-1") && a.matches(linkWorkspace: "xbin.example.com") && !a.matches(linkWorkspace: "cccc-3"))
        #expect(list.resolve(linkWorkspace: "aaaa-1")?.user.id == "alice")
        #expect(list.resolve(linkWorkspace: "Xbin.Example.com")?.user.id == "bob") // most recently used
        #expect(list.resolve(linkWorkspace: "10.0.0.5:8080")?.id == "cccc-3")
        #expect(list.resolve(linkWorkspace: "10.0.0.5") == nil)
        let link = try DeepLink(string: "xbin://10.0.0.5:8080/term/s1")
        #expect(list.resolve(linkWorkspace: try #require(link.workspace))?.id == "cccc-3")
    }
}
