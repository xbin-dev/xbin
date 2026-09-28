import Foundation
import Testing
@testable import XbinCore

@Suite struct ClientCatalogTests {
    /// Captured from a real xbind (Resources/server/*.json).
    @Test func componentsFromServer() throws {
        let c = Catalog(json: try Resources.json("server/components.json"))
        #expect(c.tiles.count == 7)
        #expect(c.listed.map(\.path) == ["apps/welcome", "tiles/admin", "tiles/apidocs", "tiles/manager", "tiles/organisations"])
        let org = try #require(c["tiles/organisations"])
        #expect(org.chrome && TileSurface.pick(org, serverRuntime: 1) == .web)   // signed in, in the app's web view
        let welcome = try #require(c["apps/welcome"])
        #expect(welcome.canOpenLinks && !welcome.chrome && welcome.title == "Welcome" && welcome.parent == "apps")
        #expect(!c["tiles/manager"]!.canOpenLinks)
        #expect(TileSurface.pick(welcome, serverRuntime: 1) == .web)
    }

    @Test func nativeDiscoveryAndSurface() {
        let n = TileInfo(json: ["path": "apps/counter-go", "hasIndex": true, "native": ["entry": "native.js"]])!
        #expect(n.nativeEntry == "native.js" && n.opensNatively)
        #expect(TileSurface.pick(n, serverRuntime: 1) == .native)
        #expect(TileSurface.pick(n, serverRuntime: nil) == .web)           // an xbind without runtime documents
        #expect(TileSurface.pick(n, serverRuntime: 1, forceWeb: true) == .web)
        #expect(TileSurface.pick(n, serverRuntime: 1, runtimeOff: true) == .web)
        let chromeNative = TileInfo(json: ["path": "x", "chrome": true, "native": ["entry": "n.js"]])!
        #expect(TileSurface.pick(chromeNative, serverRuntime: 1) == .web)   // chrome never opens natively
        #expect(TileInfo(json: ["hasIndex": true]) == nil)
        #expect(TileInfo(json: ["path": "a", "native": .null])?.nativeEntry == nil)
        #expect(TileInfo.humanize("apps/egress-approver") == "Egress approver")
        let personal = TileInfo(json: ["path": "home/alice/notes", "owner": "user:alice"])!
        #expect(personal.isPersonal(of: "alice") && !personal.isPersonal(of: "bob") && personal.orgOwner == nil)
    }

    @Test func search() {
        let c = Catalog(tiles: ["apps/calendar", "apps/egress-approver", "tiles/admin", "apps/s3-archiver", "root", "tools/cal-sync"]
            .map { TileInfo(path: $0) })
        #expect(c.search("").count == 5)
        #expect(c.search("cal").map(\.path) == ["apps/calendar", "tools/cal-sync"])
        #expect(c.search("approver").map(\.path) == ["apps/egress-approver"])
        #expect(c.search("apps arch").map(\.path) == ["apps/s3-archiver"])
        #expect(c.search("rchiv").map(\.path) == ["apps/s3-archiver"])
        #expect(c.search("nothing").isEmpty)
        #expect(c.search("root").isEmpty)                                  // shell internals never listed
    }

    @Test func layoutScreensAndFolders() throws {
        let layout = PersonalLayout(json: try Resources.json("server/layout.json"))
        #expect(layout.screens.map(\.name) == ["Home", "Ops"])
        #expect(layout.screens[0].tiles == ["apps/welcome", "tiles/manager"])
        #expect(layout.active == "s1")
        let shared = SharedScreens(json: try Resources.json("server/screens.json"))
        #expect(shared.workspaceDefault == nil && shared.org.isEmpty && shared.folders["ws"] == [])

        // Reading order: rows, then columns; floats last; duplicates once.
        let order = ScreenInfo.tilePaths([["path": "b", "x": 6, "y": 0], ["path": "c", "x": 0, "y": 4],
                                          ["path": "f", "x": 0, "y": 0, "float": ["x": 1]], ["path": "a", "x": 0, "y": 0],
                                          ["path": "a", "x": 3, "y": 9]])
        #expect(order == ["a", "b", "c", "f"])

        let side = PersonalLayout(json: ["screens": [], "side": ["folders": [
            ["id": "f1", "name": "Work", "items": ["apps/welcome", "gone/tile"], "open": true, "icon": "🛠"],
            ["id": "f2", "name": "Sub", "parent": "f1", "items": ["tiles/admin"]],
        ], "sharedOpen": ["w1": false, "bad": "yes"]]])
        #expect(side.folders[0].icon == "🛠" && side.folders[0].open && !side.folders[1].open && side.folders[1].icon == nil)
        #expect(side.sharedOpen == ["w1": false])
        let cat = Catalog(json: try Resources.json("server/components.json"))
        let nav = NavigatorModel(catalog: cat, layout: side, shared: SharedScreens(json: [
            "default": ["tiles": [["path": "tiles/manager"], ["path": "hidden/one"]]],
            "org": [["id": "o1", "org": "eng", "name": "Eng board", "tiles": [["path": "apps/welcome"]]]],
            "folders": ["ws": ["folders": [["id": "w1", "name": "Shared", "items": ["tiles/apidocs"]]]],
                        "org:eng": ["folders": []]],
        ]), whoami: Whoami(json: ["id": "admin", "orgs": [["id": "eng"]]]))
        #expect(nav.folders.map(\.name) == ["Work"] && nav.folders[0].open && nav.folders[0].icon == "🛠")
        #expect(nav.folders[0].items.map(\.id) == ["f:top/f2", "t:apps/welcome"])   // sub-folders first; unseen dropped
        #expect(nav.sections.map(\.id) == ["org:eng", "workspace"])                  // no tile of mine: no Mine
        #expect(nav.sections[0].items.map(\.id) == ["s:o1"])                         // an org's screens under it
        let ws = nav.sections[1]
        #expect(ws.items.map(\.id) == ["f:ws/w1", "t:tiles/manager", "t:tiles/organisations"])
        if case .folder(let w1)? = ws.items.first { #expect(!w1.open && w1.items.map(\.id) == ["t:tiles/apidocs"]) }
    }

    @Test func whoami() throws {
        let w = Whoami(json: try Resources.json("server/whoami.json"))
        #expect(w.userID == "admin" && w.displayName == "Dev Admin" && w.admin && w.terminal && w.termNet)
        #expect(w.nativeRuntime == nil)
        #expect(Whoami(json: ["id": "bob", "native": ["runtime": 1]]).nativeRuntime == 1)
    }
}

@Suite struct ClientTileLoadingTests {
    @Test func schemeURLs() throws {
        let u = try #require(TileScheme.pageURL(workspace: "WS-1", tile: "apps/dev box", subpath: "../../editor/", fragment: "x=1"))
        #expect(u.absoluteString == "xbin-ws://ws-1/c/apps/dev%20box/editor/#x=1")
        #expect(TileScheme.runtimeURL(workspace: "ws", tile: "apps/counter")?.absoluteString == "xbin-ws://ws/c/apps/counter/?native=1")
        #expect(TileScheme.serverPath(for: URL(string: "xbin-ws://ws/c/apps/a/x.js?v=2")!, workspace: "ws") == "/c/apps/a/x.js?v=2")
        #expect(TileScheme.serverPath(for: URL(string: "xbin-ws://ws/api/xbin/whoami")!, workspace: "WS") == "/api/xbin/whoami")
        #expect(TileScheme.serverPath(for: URL(string: "xbin-ws://other/c/a/")!, workspace: "ws") == nil)
        #expect(TileScheme.serverPath(for: URL(string: "https://ws/c/a/")!, workspace: "ws") == nil)
        let known = ["apps", "apps/a", "apps/ab"]
        #expect(TileScheme.tile(forPath: "/c/apps/a/x.js", known: known) == "apps/a")
        #expect(TileScheme.tile(forPath: "/c/apps/ab/", known: known) == "apps/ab")
        #expect(TileScheme.tile(forPath: "/c/apps/abc", known: known) == "apps")
        #expect(TileScheme.tile(forPath: "/api/x", known: known) == nil)
    }

    @Test func headerPolicy() {
        let fwd = TileScheme.forwardHeaders(["Cookie": "a=b", "X-XBin-Frame-Token": "someone-elses", "Accept": "*/*",
                                             "Content-Type": "application/json", "Connection": "keep-alive"],
                                            frameToken: "tile-token", client: "app/1.0")
        #expect(fwd == ["Accept": "*/*", "Content-Type": "application/json", "X-XBin-Frame-Token": "tile-token",
                        "X-XBin-Client": "app/1.0"])
        let back = TileScheme.pageResponseHeaders(["Content-Security-Policy": "sandbox allow-scripts", "Set-Cookie": "x=y",
                                                   "Content-Encoding": "gzip", "Content-Type": "text/html",
                                                   "Access-Control-Allow-Origin": "null"])
        #expect(back == ["Content-Security-Policy": "sandbox allow-scripts", "Content-Type": "text/html",
                         "Access-Control-Allow-Origin": "null"])
        #expect(TileScheme.allowsRedirect(to: URL(string: "https://xbin.example.com:443/c/a/")!, origin: testOrigin))
        #expect(!TileScheme.allowsRedirect(to: URL(string: "https://evil.example/c/a/")!, origin: testOrigin))
        #expect(!TileScheme.allowsRedirect(to: URL(string: "http://xbin.example.com/c/a/")!, origin: testOrigin))
        #expect(TileScheme.isReplayable(method: "get") && !TileScheme.isReplayable(method: "POST"))
        #expect(FrameTokenRoute.path(component: "apps/a b") == "/api/xbin/frame-token?component=apps%2Fa%20b")
    }

    @Test func frameTokenCache() async throws {
        let clock = TestClock()
        let mints = Box(0)
        let cache = FrameTokenCache(now: { clock.now }) { c in
            try await Task.sleep(nanoseconds: 10_000_000)
            mints.mutate { $0 += 1 }
            return "\(c)#\(mints.value)"
        }
        // Concurrent first uses share one mint.
        let got = try await withThrowingTaskGroup(of: String.self) { g in
            for _ in 0..<5 { g.addTask { try await cache.token(for: "apps/a") } }
            return try await g.reduce(into: Set<String>()) { $0.insert($1) }
        }
        #expect(got == ["apps/a#1"] && mints.value == 1)
        clock.advance(9 * 60)
        #expect(try await cache.token(for: "apps/a") == "apps/a#1")
        clock.advance(61)                                                   // 10 min: renewed before the 15-min expiry
        #expect(try await cache.token(for: "apps/a") == "apps/a#2")
        await cache.invalidate("apps/a", token: "apps/a#1")                 // stale refusal: keeps the newer one
        #expect(await cache.cached("apps/a") == "apps/a#2")
        await cache.invalidate("apps/a", token: "apps/a#2")
        #expect(await cache.cached("apps/a") == nil)
        #expect(try await cache.token(for: "apps/a") == "apps/a#3")
    }
}

@Suite struct ClientBridgeTests {
    @Test func parsesRelayedRequests() throws {
        let d = TileBridge.parse(.string(#"{"type":"xbin:dialog","id":"apps/a#1.x","spec":{"title":"Delete?","buttons":[{"label":"Delete","value":"del","danger":true},{"label":"Keep","value":null}]}}"#))
        guard case .dialog(let id, let spec)? = d else { Issue.record("not a dialog"); return }
        #expect(id == "apps/a#1.x" && spec.title == "Delete?" && spec.resolvedButtons.count == 2)
        #expect(spec.resolvedButtons[0].danger && spec.submitButton?.label == "Keep" && spec.isSimpleAlert)

        #expect(TileBridge.parse(["type": "xbin:window", "id": "w1", "spec": ["path": "editor", "title": "Edit"]])
            == .window(id: "w1", spec: WindowSpec(path: "editor", src: "", title: "Edit")))
        #expect(TileBridge.parse(["type": "xbin:window-close", "id": "w1"]) == .windowClose(id: "w1"))
        #expect(TileBridge.parse(["type": "xbin:contextmenu", "x": 10, "y": 20.5]) == .contextMenu(x: 10, y: 20.5, selection: ""))
        #expect(TileBridge.parse(["type": "xbin:dialog", "spec": [:]]) == nil)          // no id
        #expect(TileBridge.parse(["type": "xbin:resize", "height": 10]) == nil)          // not relayed
        #expect(TileBridge.parse(["type": "xbin:reply", "id": "1"]) == nil)
        #expect(TileBridge.parse(.string("{not json")) == nil)
        #expect(TileBridge.parse(.string(String(repeating: " ", count: TileBridge.maxMessageBytes + 1))) == nil)
    }

    @Test func dialogSpecMatchesBxDialog() {
        let s = DialogSpec(json: [
            "title": "New event", "message": 42, "fields": [
                ["name": "title", "placeholder": "Title"],
                ["name": "when", "type": "select", "options": ["today", ["value": 2, "label": "Tomorrow"]], "value": 2],
                ["name": "remind", "type": "checkbox", "value": 1],
                ["name": "notes", "type": "textarea", "label": "Notes", "value": "x"],
                ["label": "no name"],
                ["name": "weird", "type": "color"],
            ],
        ])
        #expect(s.message == "42")
        #expect(s.fields.map(\.name) == ["title", "when", "remind", "notes", "weird"])
        #expect(s.fields[0].label == "title" && s.fields[4].kind == .text)
        #expect(s.fields[1].options.map(\.value) == ["today", "2"] && s.fields[1].options.map(\.label) == ["today", "Tomorrow"])
        #expect(s.initialValues == ["title": "", "when": "2", "remind": true, "notes": "x", "weird": ""])
        #expect(s.resolvedButtons.map(\.label) == ["Cancel", "OK"] && s.resolvedButtons[0].value == .null)
        #expect(s.submitButton?.value == "ok" && !s.isSimpleAlert)
        #expect(DialogSpec.result(button: nil, values: ["a": true]) == ["button": .null, "values": ["a": true]])
    }

    @Test func windowTargets() {
        #expect(WindowSpec(path: "editor/").target(from: "apps/a") == "apps/a/editor")
        #expect(WindowSpec(path: "../../etc").target(from: "apps/a") == "apps/a/etc")
        #expect(WindowSpec(path: "").target(from: "apps/a") == "apps/a")
        #expect(WindowSpec(src: "/apps/b/").target(from: "apps/a") == "apps/b")
        #expect(WindowSpec.stripTraversal("..../x/..") == "..x")      // stricter than bx-shell's one-pass regex
        #expect(WindowSpec.stripTraversal("a/../b") == "a/b")
        #expect(WindowSpec(path: "x", title: "").displayTitle(from: "t") == "t/x")
    }

    @Test func spawnLimits() {
        var l = SpawnLimits()
        var got: [Bool] = []
        got.append(l.admitDialog("d1"))
        got.append(l.admitDialog("d2"))
        l.dialogClosed("d2")
        got.append(l.admitDialog("d3"))
        l.dialogClosed("d1")
        got.append(l.admitDialog("d3"))
        #expect(got == [true, false, false, true])
        var wins: [Bool] = []
        for i in 0..<7 { wins.append(l.admitWindow("w\(i)")) }
        #expect(wins == [true, true, true, true, true, true, false])
        let closed = [l.windowClosed("w0"), l.windowClosed("w0")]
        #expect(closed == [true, false])
        let again = l.admitWindow("w6")
        #expect(again)
    }

    @Test func replyScriptIsALiteral() {
        let js = TileBridge.replyScript(id: "a'</script>\u{2028}", result: ["button": "ok", "values": [:]])
        #expect(js.hasPrefix("window.postMessage({") && js.hasSuffix("}, '*');"))
        #expect(!js.contains("</script>") && !js.contains("\u{2028}"))
        #expect(TileBridge.userScript.contains("messageHandlers.xbin") && TileBridge.userScript.contains("e.source !== window"))
    }
}

/// The viewport script's behaviour is checked in a browser engine
/// (native/tools/viewport.test.mjs, which lifts it from the Swift source);
/// here: that it can be lifted verbatim and agrees with its constants.
@Suite struct ClientViewportTests {
    @Test func scriptIsAPlainLiteral() {
        let js = TileViewport.userScript
        #expect(!js.contains("\\"), "no escapes: the node test reads the literal as written")
        #expect(js.contains("MAX = \(TileViewport.maxWidth), STEPS = \(TileViewport.maxSteps), WATCH = \(TileViewport.watchMillis)"))
        #expect(js.contains("'width=device-width, initial-scale=1'"))
        #expect(!js.contains("user-scalable") && !js.contains("maximum-scale"), "pinch zoom stays the page's")
        #expect(js.contains("if (theirs() || !d.documentElement) return;"), "a page with its own viewport is never touched")
    }
}

@Suite struct ClientNativeTileTests {
    @Test func lifecycle() {
        let t0 = Date(timeIntervalSince1970: 100)
        var l = NativeTileLifecycle()
        l.start(at: t0)
        #expect(l.deadline == t0.addingTimeInterval(5))
        let early = l.check(at: t0.addingTimeInterval(4.9))
        #expect(!early)
        l.mounted()
        let late = l.check(at: t0.addingTimeInterval(60))
        #expect(l.phase == .live && !late)

        var slow = NativeTileLifecycle()
        slow.start(at: t0)
        let fired = slow.check(at: t0.addingTimeInterval(5))
        #expect(fired && slow.fallback == .timeout)
        slow.mounted()                                                     // too late: stays on the web tile
        #expect(slow.isFallback)

        var bad = NativeTileLifecycle()
        bad.start(at: t0)
        bad.fail(.store(.runtime(RuntimeError(kind: "unsupported", message: "chart.area"))))
        #expect(bad.fallback?.banner.contains("Update the app") == true)
        bad.fail(.crashed)                                                 // the first reason stays
        if case .store = bad.fallback! {} else { Issue.record("reason replaced") }
    }

    @Test func callPolicy() {
        func call(_ what: String, _ args: JSONValue) -> BridgeCall { BridgeCall(id: "c1", what: what, args: args) }
        #expect(NativeCallAction.decide(call("copy", ["text": "hi"]), canOpenLinks: false) == .copy("hi"))
        #expect(NativeCallAction.decide(call("copy", ["hi"]), canOpenLinks: false) == .copy("hi"))
        #expect(NativeCallAction.decide(call("open", ["url": "https://x.dev/a"]), canOpenLinks: true)
            == .open(URL(string: "https://x.dev/a")!))
        if case .refuse = NativeCallAction.decide(call("open", ["url": "https://x.dev"]), canOpenLinks: false) {} else { Issue.record("open without cap") }
        if case .refuse = NativeCallAction.decide(call("open", ["url": "javascript:alert(1)"]), canOpenLinks: true) {} else { Issue.record("js url") }
        #expect(NativeCallAction.decide(call("share", ["text": "t", "url": "http://insecure", "file": "out/r.pdf"]), canOpenLinks: false)
            == .share(text: "t", url: nil, file: "out/r.pdf"))
        if case .refuse = NativeCallAction.decide(call("share", ["file": "../other/secret"]), canOpenLinks: false) {} else { Issue.record("traversal") }
        if case .refuse = NativeCallAction.decide(call("camera", [:]), canOpenLinks: true) {} else { Issue.record("device API") }
        #expect(NativeStateBlob.accept(["a": 1]) != nil)
        #expect(NativeStateBlob.accept(.string(String(repeating: "x", count: 70_000))) == nil)
    }
}
