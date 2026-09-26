import Foundation
import Testing
@testable import XbinCore

/// The escape hatches' confinement (TileResources.swift): what a tile hands
/// the app — an upload target, a pty socket, an image, a canvas page —
/// resolves only to that tile's own paths.
@Suite struct TileResourceTests {
    @Test func uploadTargets() {
        // the agent template's own target, {name} filled in
        #expect(TileResource.apiPath("/api/agent/runs/5/upload?name={name}", tile: "agent", name: "IMG 1.jpg")
            == "/api/agent/runs/5/upload?name=IMG%201.jpg")
        // relative, root-relative (docs/native.md: under /api/<self>/ unless it starts with /api/), ./
        #expect(TileResource.apiPath("upload", tile: "apps/x") == "/api/apps/x/upload")
        #expect(TileResource.apiPath("/upload", tile: "apps/x") == "/api/apps/x/upload")
        #expect(TileResource.apiPath("./files/{name}", tile: "apps/x", name: "b?#.txt") == "/api/apps/x/files/b%3F%23.txt")
        #expect(TileResource.apiPath("up?name={name}&k=1", tile: "apps/x", name: "a&b=c.txt") == "/api/apps/x/up?name=a%26b%3Dc.txt&k=1")
        #expect(TileResource.apiPath("/c/x", tile: "apps/x") == "/api/apps/x/c/x")
        #expect(TileResource.apiPath("/api/apps/x", tile: "apps/x") == "/api/apps/x")
        #expect(TileResource.apiPath("/api/apps/x/", tile: "apps/x") == "/api/apps/x/")
        #expect(TileResource.apiPath("?q=1", tile: "apps/x") == "/api/apps/x/?q=1")
        #expect(TileResource.apiPath(".", tile: "apps/x") == "/api/apps/x/")
        // a tile path with characters that need encoding
        #expect(TileResource.apiPath("up", tile: "apps/my tile") == "/api/apps/my%20tile/up")
        #expect(TileResource.apiPath("/api/apps/my%20tile/up", tile: "apps/my tile") == "/api/apps/my%20tile/up")
        // raw characters a URL can't carry are encoded; escapes are kept
        #expect(TileResource.apiPath("dir/é x?n=a b&m=%41", tile: "t") == "/api/t/dir/%C3%A9%20x?n=a%20b&m=%41")
        // the fragment never reaches a server
        #expect(TileResource.apiPath("up#frag", tile: "t") == "/api/t/up")
    }

    @Test func uploadTargetsRefused() {
        let refused = [
            "/api/other/x",                 // another tile
            "/api/agent2/x",                // a sibling that merely shares the prefix
            "/api/xbin/frame-token",        // xbind's own API
            "/api/agent/../other/x",        // traversal
            "/api/agent/%2e%2e/other",      // encoded traversal
            "/api/agent/a%2fb",             // an encoded slash
            "/api/agent/a%5cb",             // an encoded backslash
            "../other/x", "./../x", "a/../../b", "a/./b",
            "https://evil.example/x", "http:x", "javascript:alert(1)", "data:text/plain,x", "a:b",
            "//evil.example/x", "\\\\evil\\x", "/api/agent\\..\\x",
            "/api//agent/x", "up//x",       // empty segments
            "/api/agent/%zz",               // a malformed escape
            "/api/agent/%ff",               // not UTF-8
            "up\nx", "up\u{7f}", "",
            "/api", "/api/",
        ]
        for r in refused {
            #expect(TileResource.apiPath(r, tile: "agent") == nil, "\(r.debugDescription) should be refused")
        }
        // {name} can't smuggle a traversal: it is one encoded component
        #expect(TileResource.apiPath("/api/agent/{name}", tile: "agent", name: "..") == nil)
        #expect(TileResource.apiPath("/api/agent/f/{name}", tile: "agent", name: "../../x") == nil)
        #expect(TileResource.apiPath("/api/agent/f?n={name}", tile: "agent", name: "../../x") == "/api/agent/f?n=..%2F..%2Fx")
        // a tile named like xbind's API, or a malformed tile path
        #expect(TileResource.apiPath("up", tile: "xbin") == nil)
        #expect(TileResource.apiPath("up", tile: "xbin/x") == nil)
        #expect(TileResource.apiPath("up", tile: "") == nil)
        #expect(TileResource.apiPath("up", tile: "a/../b") == nil)
    }

    @Test func nestedTilesOwnTheirPaths() {
        let known = ["apps", "apps/other", "apps/x"]
        #expect(TileResource.apiPath("other/up", tile: "apps", known: known) == nil)
        #expect(TileResource.apiPath("/api/apps/other/up", tile: "apps", known: known) == nil)
        #expect(TileResource.apiPath("/api/apps/other", tile: "apps", known: known) == nil)
        #expect(TileResource.apiPath("otherwise/up", tile: "apps", known: known) == "/api/apps/otherwise/up")
        #expect(TileResource.apiPath("up", tile: "apps/x", known: known) == "/api/apps/x/up")
        #expect(TileResource.assetPath("other/logo.png", tile: "apps", known: known) == nil)
        #expect(TileResource.pagePath("x/", tile: "apps", known: known) == nil)
    }

    @Test func frameParametersAreDropped() {
        #expect(TileResource.apiPath("pty?frame=abc&cols=80", tile: "t") == "/api/t/pty?cols=80")
        #expect(TileResource.apiPath("pty?fr%61me=abc", tile: "t") == "/api/t/pty")
        #expect(TileResource.apiPath("pty?frame", tile: "t") == "/api/t/pty")
        #expect(TileResource.apiPath("pty?framed=1", tile: "t") == "/api/t/pty?framed=1")
    }

    @Test func assetsAndPages() {
        #expect(TileResource.assetPath("logo.png", tile: "apps/x") == "/c/apps/x/logo.png")
        #expect(TileResource.assetPath("./img/a b.png", tile: "apps/x") == "/c/apps/x/img/a%20b.png")
        #expect(TileResource.assetPath("/c/apps/x/logo.png", tile: "apps/x") == "/c/apps/x/logo.png")
        // the agent template's thumbnails are its backend's
        #expect(TileResource.assetPath("/api/agent/runs/3/thumb?path=a%2Fb.png&w=480", tile: "agent")
            == "/api/agent/runs/3/thumb?path=a%2Fb.png&w=480")
        #expect(TileResource.assetPath("/logo.png", tile: "apps/x") == nil)
        #expect(TileResource.assetPath("/c/apps/y/logo.png", tile: "apps/x") == nil)
        #expect(TileResource.assetPath("../y/logo.png", tile: "apps/x") == nil)
        #expect(TileResource.assetPath("https://cdn.example/x.png", tile: "apps/x") == nil)

        #expect(TileResource.pagePath("chart.html", tile: "apps/x") == "/c/apps/x/chart.html")
        #expect(TileResource.pagePath("chart.html?d=1#dark", tile: "apps/x") == "/c/apps/x/chart.html?d=1#dark")
        #expect(TileResource.pagePath("/c/apps/x/sub/", tile: "apps/x") == "/c/apps/x/sub/")
        #expect(TileResource.pagePath("./", tile: "apps/x") == "/c/apps/x/")
        #expect(TileResource.pagePath("/api/apps/x/page", tile: "apps/x") == nil)
        #expect(TileResource.pagePath("/c/apps/y/", tile: "apps/x") == nil)
        #expect(TileResource.pagePath("//evil/", tile: "apps/x") == nil)
    }

    @Test func socketURL() throws {
        let o = try ServerOrigin(string: "https://ws.example.com")
        let path = try #require(TileResource.apiPath("pty?cols=80", tile: "apps/x"))
        #expect(TileResource.socketURL(origin: o, path: path, frameToken: "t+/=")?.absoluteString
            == "wss://ws.example.com/api/apps/x/pty?cols=80&frame=t%2B%2F%3D")
        let plain = try ServerOrigin(string: "http://127.0.0.1:9461")
        #expect(TileResource.socketURL(origin: plain, path: "/api/t/pty", frameToken: "abc")?.absoluteString
            == "ws://127.0.0.1:9461/api/t/pty?frame=abc")
    }

    @Test func methodsAndNames() {
        #expect(TileResource.uploadMethod(nil) == "PUT")
        #expect(TileResource.uploadMethod("") == "PUT")
        #expect(TileResource.uploadMethod("post") == "POST")
        #expect(TileResource.uploadMethod("PATCH") == "PATCH")
        #expect(TileResource.uploadMethod("DELETE") == nil)
        #expect(TileResource.uploadMethod("GET") == nil)

        #expect(TileResource.fileName("IMG_0001.HEIC", fallback: "f") == "IMG_0001.HEIC")
        #expect(TileResource.fileName("/private/var/tmp/report.pdf", fallback: "f") == "report.pdf")
        #expect(TileResource.fileName("a\\b\\c.txt", fallback: "f") == "c.txt")
        #expect(TileResource.fileName("..", fallback: "photo.jpg") == "photo.jpg")
        #expect(TileResource.fileName("  ", fallback: "photo.jpg") == "photo.jpg")
        #expect(TileResource.fileName("a\u{1}b.txt", fallback: "f") == "a_b.txt")
        let long = String(repeating: "é", count: 150) + ".jpeg"
        let n = TileResource.fileName(long, fallback: "f")
        #expect(n.utf8.count <= 200 && n.hasSuffix(".jpeg") && n.hasPrefix("éé"))
    }

    @Test func acceptFilter() {
        let any = AcceptFilter(nil)
        #expect(any.isAny && any.allowsImages && any.allowsFiles && any.accepts(name: "x.bin", mime: ""))
        let img = AcceptFilter("image/*")
        #expect(img.allowsImages && !img.allowsFiles && !img.allowsVideos)
        #expect(img.accepts(name: "a.jpg", mime: "image/jpeg") && !img.accepts(name: "a.pdf", mime: "application/pdf"))
        let mixed = AcceptFilter(" image/png , .PDF,text/plain")
        #expect(mixed.types == ["image/png", "text/plain"] && mixed.extensions == ["pdf"])
        #expect(mixed.allowsImages && mixed.allowsFiles)
        #expect(mixed.accepts(name: "x.pdf", mime: "") && mixed.accepts(name: "n", mime: "TEXT/PLAIN"))
        #expect(!mixed.accepts(name: "a.jpg", mime: "image/jpeg"))
        #expect(AcceptFilter("*/*").isAny)
        #expect(AcceptFilter(".csv").allowsFiles && !AcceptFilter(".csv").allowsImages)
    }

    @Test func uploadAnswers() throws {
        #expect(TileUpload.response(body: Data(#"{"path":"a.png","bytes":3}"#.utf8)) == ["path": "a.png", "bytes": 3])
        #expect(TileUpload.response(body: Data("stored".utf8)) == "stored")
        #expect(TileUpload.response(body: Data()) == "")
        let big = TileUpload.tooLarge(70 << 20)
        #expect(big["status"] == 413)
        // the agent template reads a refusal off the answer's text
        #expect(big.jsonString.contains("too large"))
    }

    @Test func canvasDocument() {
        let d = CanvasDocument.wrap("<p>hi</p><script>alert(1)</script>")
        #expect(d.hasPrefix("<!doctype html>"))
        // the CSP comes before anything the tile wrote
        let csp = d.range(of: "Content-Security-Policy")!.lowerBound
        #expect(csp < d.range(of: "<p>hi</p>")!.lowerBound)
        #expect(CanvasDocument.csp.contains("default-src 'none'") && !CanvasDocument.csp.contains("script-src"))
        #expect(CanvasDocument.csp.contains("img-src data:") && CanvasDocument.csp.contains("style-src 'unsafe-inline'"))
    }
}
