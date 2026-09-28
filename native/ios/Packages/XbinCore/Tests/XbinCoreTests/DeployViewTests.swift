import Foundation
import Testing
@testable import XbinCore

/// The app's Deployments tool (D132), held to the web's own words: the
/// states are hack/deploy-fixtures.mjs's, the expectations
/// hack/deploy-state.test.mjs's, hack/deploy-branch.test.mjs's and
/// hack/deploy-panel.test.mjs's.
@Suite struct DeployViewTests {
    static let T = "apps/crm"
    static let now = ISO8601DateFormatter().date(from: "2026-09-27T12:00:00Z")!
    static func at(_ min: Double) -> String { ISO8601DateFormatter().string(from: now.addingTimeInterval(-min * 60)) }
    static let yes = DeployCan(ok: true)
    static func no(_ why: String, _ kind: String = "authority") -> DeployCan { DeployCan(ok: false, why: why, kind: kind) }
    static let features = ["live-reload/1", "deployments/1", "branches/1"]

    static func terminalCaller(_ can: [String: DeployCan]? = nil) -> DeploymentsState.Caller {
        .init(level: "terminal", can: can ?? ["pause": yes, "resume": yes, "reloadNow": yes, "add": yes, "edges": no("tile managers only")])
    }
    static func depCan(_ over: [String: DeployCan] = [:]) -> [String: DeployCan] {
        ["open": yes, "deploy": yes, "restart": yes, "promoteTo": yes, "rollback": yes, "attach": yes, "remove": no("main can't be removed", "state")]
            .merging(over) { $1 }
    }
    static func mainLive() -> DeploymentInfo {
        DeploymentInfo(name: "main", primary: true, liveReload: true, status: .init(state: "static", serving: "work-tree"), url: "/c/\(T)/", can: depCan())
    }
    static func mainPinned(how: String = "pause") -> DeploymentInfo {
        DeploymentInfo(name: "main", primary: true, checkpoint: "c:3f2a1c9", status: .init(state: "static", serving: "c:3f2a1c9"), url: "/c/\(T)/",
                       lastDeploy: DeployEntry(id: "2", how: how, by: "user:ana", at: at(12), result: "ok"), can: depCan())
    }
    static func devLive(branch: String = "", branchOverride: String = "", can: [String: DeployCan]? = nil) -> DeploymentInfo {
        DeploymentInfo(name: "dev", liveReload: true, status: .init(state: "static", serving: "work-tree"), url: "/c/\(T)+dev/",
                       can: can ?? depCan(["remove": yes]), branch: branch, branchOverride: branchOverride)
    }
    static func pinned(_ name: String, _ id: String, branch: String = "", lastDeploy: DeployEntry? = nil) -> DeploymentInfo {
        DeploymentInfo(name: name, checkpoint: id, status: .init(state: "static", serving: id), url: "/c/\(T)+\(name)/",
                       lastDeploy: lastDeploy, can: depCan(["remove": yes]), branch: branch)
    }
    static func zero() -> DeploymentsState {
        DeploymentsState(tile: T, record: false, features: ["live-reload/1"], primary: "main", liveReload: "main", lastLiveReload: "main",
                         allowed: ["pause": yes, "deployments": yes], deployments: [mainLive()], caller: terminalCaller())
    }
    static func paused(features: [String] = ["live-reload/1"], lastLiveReload: String = "main", since: DeployStamp? = DeployStamp(by: "user:ana", at: at(12)),
                       workTree: DeploymentsState.WorkTree? = .init(changed: 3, since: "c:3f2a1c9"), deployments: [DeploymentInfo] = [mainPinned()],
                       caller: DeploymentsState.Caller = terminalCaller()) -> DeploymentsState {
        DeploymentsState(tile: T, record: true, features: features, seq: 3, primary: "main", liveReload: "", lastLiveReload: lastLiveReload,
                         liveReloadSince: since, workTree: workTree, allowed: ["pause": yes, "deployments": yes], deployments: deployments, caller: caller)
    }
    static func onDev(features: [String] = ["live-reload/1"], workTree: DeploymentsState.WorkTree? = nil,
                      deployments: [DeploymentInfo]? = nil) -> DeploymentsState {
        var s = paused(features: features, lastLiveReload: "dev", workTree: workTree,
                       deployments: deployments ?? [mainPinned(how: "attach"), devLive()])
        s.liveReload = "dev"
        return s
    }
    // dev requires feature, qa requires release; live reload on dev
    static func attached(_ wt: String) -> DeploymentsState {
        onDev(features: features, workTree: .init(branch: wt),
              deployments: [mainPinned(), devLive(branch: "feature"), pinned("qa", "c:9a9a9a9", branch: "release")])
    }
    // xbind paused live reload on dev when the work tree left feature
    static func switched(_ wt: String, caller: DeploymentsState.Caller = terminalCaller()) -> DeploymentsState {
        paused(features: features, lastLiveReload: "dev", since: DeployStamp(by: "xbind", at: at(1)),
               workTree: .init(changed: 2, since: "c:5e5e5e5", branch: wt),
               deployments: [mainPinned(), pinned("dev", "c:5e5e5e5", branch: "feature", lastDeploy: DeployEntry(id: "7", how: "pause", by: "xbind", at: at(1), result: "ok")),
                             pinned("qa", "c:9a9a9a9", branch: "release")], caller: caller)
    }

    @Test func decodesTheServersState() throws {
        let json = #"""
        {"tile":"apps/wide","record":true,"schema":1,"seq":4,"features":["live-reload/1","deployments/1","branches/1"],"view":"full",
         "primary":"main","liveReload":"dev","lastLiveReload":"dev","liveReloadSince":{"at":"2026-09-27T11:48:00Z","by":"user:e2e"},
         "workTree":{"branch":"main"},"protectedPrimary":false,"allowed":{"pause":{"ok":true},"deployments":{"ok":true}},
         "deployments":[{"name":"main","primary":true,"liveReload":false,"checkpoint":{"id":"c:3f2a1c9","hash":"3f"},
                         "status":{"state":"static","gen":2,"serving":"c:3f2a1c9"},"url":"/c/apps/wide/","can":{"deploy":{"ok":true}},
                         "lastDeploy":{"id":2,"how":"attach","at":"2026-09-27T11:48:00Z","by":"user:e2e","result":"ok"}},
                        {"name":"dev","primary":false,"liveReload":true,"checkpoint":null,"branch":"feature/x",
                         "status":{"state":"static","gen":1,"serving":"work-tree","deploying":{"checkpoint":"c:1"},"queued":[{}]},
                         "url":"/c/apps/wide+dev/","can":{"attach":{"ok":false,"why":"nope","kind":"state"}},"extra":{"x":1}}],
         "caller":{"level":"admin","manager":true,"humanSession":true,"readOnly":false,"can":{"pause":{"ok":true},"resume":{"ok":true}}}}
        """#
        let s = try #require(DeploymentsState(json: try JSONValue(parsing: json)))
        #expect(s.tile == "apps/wide" && s.record && s.seq == 4 && s.speaks("branches/1") && Branch.speaks(s))
        #expect(s.liveReload == "dev" && s.workTree?.branch == "main" && s.workTree?.changed == nil)
        let main = try #require(s.deployment("main")), dev = try #require(s.deployment("dev"))
        #expect(main.checkpoint == "c:3f2a1c9" && main.lastDeploy?.id == "2" && main.lastDeploy?.how == "attach")
        #expect(dev.checkpoint == "" && dev.branch == "feature/x" && dev.status.deploying && dev.status.deployingCheckpoint == "c:1" && dev.status.queued == 1)
        #expect(dev.can["attach"] == DeployCan(ok: false, why: "nope", kind: "state"))
        #expect(s.caller.manager && s.caller.can["resume"]?.ok == true)
        #expect(DeploymentsState(json: .object([:])) == nil)
        // An older xbind has no route: a plain 404 or 405.
        #expect(DeploymentsRoute.absent(status: 404) && DeploymentsRoute.absent(status: 405) && !DeploymentsRoute.absent(status: 403))
        #expect(DeploymentsRoute.state(tile: "apps/crm") == "/api/xbin/deployments?tile=apps%2Fcrm")
        #expect(DeploymentsRoute.log(tile: "apps/crm", deployment: "dev") == "/api/xbin/deployments/log?tile=apps%2Fcrm&limit=20&deployment=dev")
        let known: (String) -> Bool = { ["apps/crm", "apps/a+b"].contains($0) }
        #expect(DeploymentsRoute.opensSignedIn("apps/crm+dev", known: known))
        #expect(!DeploymentsRoute.opensSignedIn("apps/crm", known: known))
        #expect(!DeploymentsRoute.opensSignedIn("apps/a+b", known: known), "a tile whose name holds + is a tile")
        #expect(!DeploymentsRoute.opensSignedIn("apps/x+dev", known: known) && !DeploymentsRoute.opensSignedIn("apps/crm+", known: known))
        #expect(DeploymentsRoute.opensSignedIn("root", known: known))
    }

    typealias Branch = DeployView.Branch

    @Test func theZeroState() {
        let s = Self.zero()
        #expect(DeployView.chip(s) == nil)
        #expect(DeployView.launcher(s) == nil)
        #expect(DeployView.header(s) == "Live reload: main — every save reaches everyone using apps/crm.")
        let a = DeployView.actions(s)
        #expect(a.map(\.label) == ["Pause live reload"])
        #expect(a[0].enabled && a[0].title == "Keep main on the code it runs now; saves stop reaching it until Reload now or Resume live reload.")
        let c = DeployView.confirmation("pause", state: s, impact: DeployImpact(affects: "nobody", pausesLiveReload: true))
        #expect(c.title == "Pause live reload on apps/crm?")
        #expect(c.message == [
            "Code: main keeps running the code it runs now, pinned to a checkpoint of the work tree taken when you confirm.",
            "Data: nothing moves.",
            "Pauses: live reload — saves stop reaching main until Reload now or Resume live reload.",
            "Affects: nobody now.",
        ].joined(separator: "\n"))
        #expect(c.send.isEmpty)
    }

    @Test func pausedWithChanges() {
        let s = Self.paused()
        let c = DeployView.chip(s, now: Self.now)
        #expect(c?.text == "📌 Live reload paused · 3")
        #expect(c?.title == "Live reload paused by ana 12m ago — 3 files changed since c:3f2a1c9, which main runs. Reload now ships them to main once.")
        #expect(DeployView.header(s, now: Self.now) == "Live reload paused by ana 12m ago — 3 files changed since c:3f2a1c9, the checkpoint main runs.")
        let a = DeployView.actions(s)
        #expect(a.map(\.label) == ["⇡ Reload now · 3", "Resume live reload on ▸"])
        #expect(a[0].title == "Ship the work tree to main once (3 files); main stays pinned.")
        #expect(a[1].items.map(\.label) == ["main"] && a[1].items[0].title == "main follows the work tree again; the 3 changed files ship now.")
        let l = DeployView.launcher(s)
        #expect(l?.banner == DeployView.Banner(text: "Live reload is paused: 3 files changed since c:3f2a1c9, the checkpoint main runs.", tone: "paused", reloadNow: true))
        #expect(l?.subtitle == "· target: main" && l?.note == nil)
        // Nothing changed: Reload now says why it can't.
        var none = s
        none.workTree = .init(changed: 0, since: "c:3f2a1c9")
        #expect(DeployView.actions(none)[0].enabled == false && DeployView.actions(none)[0].why == "No changes since c:3f2a1c9.")
        #expect(DeployView.chip(none, now: Self.now)?.title == "Live reload paused by ana 12m ago — no changes since c:3f2a1c9.")
    }

    @Test func liveReloadOnDev() {
        let s = Self.onDev()
        #expect(DeployView.chip(s)?.text == "● Live reload: dev")
        #expect(DeployView.header(s) == "Live reload: dev — saves reach apps/crm+dev. The primary, main, is pinned to c:3f2a1c9.")
        let a = DeployView.actions(s)
        #expect(a.map(\.label) == ["Pause live reload", "Attach live reload to ▸"] && a[1].items.map(\.deployment) == ["main"])
        #expect(DeployView.launcher(s)?.banner?.text == "Live reload: dev — saves reach apps/crm+dev; main is pinned to c:3f2a1c9.")
        #expect(DeployView.launcher(s)?.note == "Saves reach dev; new sessions call main. Switch in the tile API select after starting.")
        // The rows: primary first, the Dev API tag on the session's target, live reload's.
        let rows = DeployView.rows(s, target: "primary")
        #expect(rows.map(\.name) == ["main", "dev"] && rows.map(\.target) == [true, false] && rows.map(\.liveReload) == [false, true])
        #expect(rows.map(\.code) == ["📌 c:3f2a1c9", "● work tree"] && rows[1].url == "/c/apps/crm+dev/")
        #expect(DeployView.rows(s, target: "dev").map(\.target) == [false, true])
        #expect(DeployView.devAPITitle(s, "dev").hasPrefix("Dev API: this tab's API calls and bx commands reach apps/crm+dev (XBIN_DEPLOYMENT=dev)."))
        #expect(DeployView.sessionTarget(api: true, deployment: "") == "primary" && DeployView.sessionTarget(api: false, deployment: "dev") == "off")
    }

    @Test func confirmationsFromTheDryRun() {
        let code = DeployImpact.Code(from: "c:3f2a1c9", to: "c:7b19e02", files: 3, added: 40, removed: 12)
        let s = Self.paused()
        let r = DeployView.confirmation("reloadNow", state: s, impact: DeployImpact(code: code, affects: "everyone"))
        #expect(r.title == "Reload main now?" && r.ok == "Reload now" && r.send == ["expect": .string("c:7b19e02")])
        #expect(r.message.split(separator: "\n").first == "Code: ships the work tree to main once, as c:7b19e02 (3 files, +40 −12 against c:3f2a1c9). main stays pinned; later saves wait for the next Reload now.")
        #expect(r.message.hasSuffix("Affects: everyone using apps/crm: frames reload once."))
        let res = DeployView.confirmation("resume", state: s, impact: DeployImpact(code: code, affects: "everyone"), deployment: "main")
        #expect(res.title == "Resume live reload on main?")
        #expect(res.message.split(separator: "\n").first == "Code: main switches to the work tree now: 3 files (+40 −12) changed since main's c:3f2a1c9 ship at once, then every save reaches main.")
        let att = DeployView.confirmation("attach", state: Self.onDev(), impact: DeployImpact(code: code, affects: "everyone", reloads: ["main"]), deployment: "main")
        #expect(att.title == "Attach live reload to main?" && att.ok == "Attach to main")
        #expect(att.message.split(separator: "\n").first == "Code: dev is pinned to a fresh checkpoint of the work tree, c:7b19e02, and stops following saves. main switches to the work tree now: 3 files (+40 −12) against main's c:3f2a1c9 ship at once, and main follows every save.")
        let entry = DeployEntry(id: "5", deployment: "dev", how: "deploy", by: "user:ana", finishedAt: Self.at(60), result: "ok", checkpoint: "c:1111111")
        let rb = DeployView.confirmation("rollback", state: Self.onDev(), impact: DeployImpact(affects: "deployment", pausesLiveReload: true),
                                         deployment: "dev", checkpoint: "c:1111111", entry: entry, now: Self.now)
        #expect(rb.title == "Roll back dev to c:1111111?" && rb.send == ["checkpoint": .string("c:1111111")])
        #expect(rb.message == [
            "Code: dev runs c:1111111 again (deployed 1h ago by ana).",
            "Data: stays as it is — a roll back moves code, not state; resources the newer code created are kept.",
            "Pauses: live reload — later saves won't reach dev until you resume. The work tree still holds the code you roll back from.",
            "Affects: only people using apps/crm+dev.",
        ].joined(separator: "\n"))
    }

    @Test func resultsRefusalsConflicts() {
        let s = Self.paused()
        #expect(DeployView.result("pause", state: s, deploy: nil, unchanged: false) == "Live reload paused — main is pinned to c:3f2a1c9.")
        #expect(DeployView.result("resume", state: Self.onDev(), deploy: nil, unchanged: false) == "Live reload: dev — saves reach dev again.")
        #expect(DeployView.result("attach", state: Self.onDev(), deploy: nil, unchanged: false, prev: Self.attached("feature").with { $0.liveReload = "main" })
                == "Live reload: dev — main is pinned to c:3f2a1c9.")
        #expect(DeployView.result("reloadNow", state: s, deploy: nil, unchanged: true) == "No changes since c:3f2a1c9.")
        let ok = DeployEntry(deployment: "main", how: "reload-now", result: "ok", checkpoint: "c:7b19e02")
        #expect(DeployView.result("reloadNow", state: s, deploy: ok, unchanged: false) == "main now runs c:7b19e02 (Reload now).")
        let bad = DeployEntry(deployment: "main", how: "deploy", result: "failed", error: "build failed", previous: "c:3f2a1c9")
        #expect(DeployView.result("rollback", state: s, deploy: bad, unchanged: false) == "Deploy to main failed — main keeps running c:3f2a1c9. build failed")
        #expect(DeployView.refusal("reloadNow", error: "x").title == "Reload now was refused")
        #expect(DeployView.conflict(status: 409, error: "the deployments of apps/crm changed (seq 4) — reload and try again") == "seq")
        #expect(DeployView.conflict(status: 409, error: "the code changed since you reviewed it") == "expect")
        #expect(DeployView.conflict(status: 400, error: "the code changed since you reviewed it") == nil)
        // Events: a save moves the count in place; a record change refetches.
        let e = DeployView.apply(event: "work-tree", changed: 5, to: s)
        #expect(e.state?.workTree?.changed == 5 && !e.refetch)
        #expect(DeployView.apply(event: "branch", changed: nil, to: s).refetch)
        #expect(DeployView.apply(event: "reload", changed: nil, to: s).refetch == false)
    }

    // hack/deploy-branch.test.mjs

    @Test func theBranchFacts() {
        let f = DeployView.facts(Self.attached("release")).branch
        #expect(f.need == "feature" && f.workTreeBranch == "release" && f.off && f.related == "qa" && !f.switched)
        #expect(!DeployView.facts(Self.attached("feature")).branch.off)
        var s = Self.attached("hotfix")
        s.deployments = [Self.mainPinned(), Self.devLive(branch: "feature", branchOverride: "hotfix"), Self.pinned("qa", "c:9a9a9a9", branch: "hotfix")]
        #expect(!DeployView.facts(s).branch.off)
        #expect(DeployView.offers(s).isEmpty, "the user chose it")
        #expect(DeployView.facts(Self.onDev(deployments: [Self.mainPinned(), Self.devLive(branch: "feature")])).branch.workTreeBranch == nil)
    }

    @Test func aBranchSwitchOnTheHeaderAndTheOffers() {
        let s = Self.switched("hotfix")
        #expect(DeployView.chip(s)?.text == "📌 Live reload paused · ⎇ hotfix")
        #expect(DeployView.chip(s)?.title == "Live reload paused — the work tree is on hotfix, and dev requires feature: dev keeps running c:5e5e5e5.")
        let o = DeployView.offers(s)
        #expect(o.map(\.label) == ["Keep dev on hotfix this time", "Add a deployment for hotfix…"])
        #expect(o.map(\.id) == ["keep/dev", "addFor/hotfix"])
        #expect(o[0].op == "resume" && o[0].deployment == "dev" && o[0].other && o[0].enabled)
        #expect(o[1].op == "addFor" && o[1].branch == "hotfix")
        #expect(DeployView.launcher(s)?.banner?.text == "Live reload is paused: the work tree is on hotfix, and dev requires feature.")
        #expect(DeployView.offers(Self.switched("release")).map { [$0.label, $0.op, $0.deployment] } == [["Resume live reload on qa (release)", "resume", "qa"]])
        let back = Self.switched("feature")
        #expect(DeployView.chip(back)?.text == "📌 Live reload paused · 2")
        #expect(DeployView.chip(back)?.title == "Live reload paused when the work tree left feature; it is on feature again — resume live reload on dev to follow saves.")
        #expect(DeployView.offers(back).map { [$0.label, $0.op, $0.deployment] } == [["Resume live reload on dev", "resume", "dev"]])
        let a = Self.attached("release")
        #expect(DeployView.chip(a)?.title.hasSuffix("The work tree is on release, and dev requires feature: the next save pauses live reload.") == true)
        #expect(DeployView.offers(a).map { [$0.label, $0.op, $0.deployment] } == [["Attach live reload to qa (release)", "attach", "qa"]])
        #expect(DeployView.offers(Self.attached("feature")).isEmpty)
        var old = Self.switched("hotfix")
        old.features = ["live-reload/1"]
        #expect(DeployView.offers(old).isEmpty, "no offers without branches/1")
        // The offers take the permission of what they start.
        let denied = Self.switched("hotfix", caller: Self.terminalCaller(["pause": Self.yes, "resume": Self.no("resuming live reload needs terminal-level access on apps/crm"),
                                                                            "reloadNow": Self.yes, "add": Self.no("nope")]))
        #expect(DeployView.offers(denied).map { "\($0.enabled) \($0.why)" } == ["false resuming live reload needs terminal-level access on apps/crm", "false nope"])
    }

    @Test func theBranchRowItsActionsAndTheLog() {
        var s = Self.attached("feature")
        s.deployments = [Self.mainPinned(), Self.devLive(branch: "feature", branchOverride: "hotfix", can: Self.depCan(["remove": Self.yes, "branch": Self.yes]))]
        #expect(DeployView.overview(s, "dev").first { $0.0 == "branch" }?.1 == "feature · takes hotfix this time")
        #expect(DeployView.overview(s, "main").contains { $0.0 == "branch" } == false)
        var none = Self.attached("x")
        none.deployments = [Self.mainPinned(), Self.devLive()]
        #expect(DeployView.overview(none, "dev").first { $0.0 == "branch" }?.1 == "none — the work tree feeds it on any branch")
        #expect(Branch.actions(s, "dev").map { "\($0.id) \($0.label) \($0.enabled)" } == ["branch Branch: feature… true", "clearBranch Clear branch true"])
        #expect(Branch.actions(Self.onDev(deployments: [Self.mainPinned(), Self.devLive(branch: "feature")]), "dev").isEmpty, "no branch actions without branches/1")
        #expect(DeployView.rows(s).first { $0.name == "dev" }?.branch == "feature")
        let log = DeployView.logRows(s, "dev", [DeployEntry(id: "3", how: "attach", by: "user:ana", requestedAt: Self.at(3), result: "ok",
                                                           checkpoint: "c:5e5e5e5", followsWorkTree: true, branch: "feature")], now: Self.now)
        #expect(log[0].branch == "feature" && log[0].state == "running" && log[0].rollback == nil && log[0].who == "ana · 3m ago")
        #expect(Branch.dialog(s, "dev").value == "feature")
        // The deploy log: the running entry, Roll back on the older ones.
        let p = Self.onDev(deployments: [Self.mainPinned(), Self.pinned("dev", "c:2222222")])
        let rows = DeployView.logRows(p, "dev", [
            DeployEntry(id: "9", deployment: "dev", how: "deploy", by: "user:ana", finishedAt: Self.at(2), result: "ok", checkpoint: "c:2222222"),
            DeployEntry(id: "8", deployment: "dev", how: "deploy", by: "user:bo", agent: true, finishedAt: Self.at(90), result: "ok", checkpoint: "c:1111111", branch: "feature"),
            DeployEntry(id: "7", deployment: "dev", how: "deploy", finishedAt: Self.at(95), result: "failed", error: "build failed", checkpoint: "c:0000000"),
        ], now: Self.now)
        #expect(rows.map(\.state) == ["running", "", ""])
        #expect(rows.map(\.rollbackLabel) == ["", "Roll back to c:1111111", ""])
        #expect(rows[1].rollback?.enabled == true && rows[1].who == "bo (agent) · 1h ago" && rows[1].how == "deploy")
        #expect(rows[2].result == "failed — build failed" && rows[2].failed)
    }

    @Test func branchConfirmationsAndResults() {
        let s = Self.switched("hotfix")
        let c = DeployView.confirmation("resume", state: s, impact: DeployImpact(affects: "deployment", reloads: ["dev"],
                                                                                branch: .init(deployment: "dev", assigned: "feature", workTree: "hotfix", other: true)),
                                        deployment: "dev")
        #expect(c.message.split(separator: "\n")[1] == "Branch: dev requires feature; it takes the work tree's hotfix this time, until live reload moves or the work tree's branch changes again.")
        let same = DeployView.confirmation("reloadNow", state: s, impact: DeployImpact(branch: .init(deployment: "dev", assigned: "feature", workTree: "feature")))
        #expect(same.message.split(separator: "\n")[1] == "Branch: dev requires feature — the work tree is on it.")
        let set = DeployView.confirmation("branch", state: s, impact: DeployImpact(branch: .init(deployment: "qa", assigned: "release", workTree: "hotfix")),
                                          deployment: "qa", branch: "release")
        #expect(set.title == "Assign qa branch release?" && set.message.contains("The work tree is on hotfix now.") && set.send.isEmpty)
        #expect(DeployView.confirmation("branch", state: s, impact: nil, deployment: "qa", branch: nil).title == "Clear qa's branch?")
        #expect(DeployView.result("branch", state: s, deploy: nil, unchanged: false, deployment: "qa") == "qa requires release.")
    }

    @Test func aMismatchsQuestion() {
        let err = #"dev is assigned branch feature, and the work tree is on main: check out feature, or send confirm:"other-branch" to use main this time"#
        #expect(Branch.mismatch(err) == Branch.Mismatch(deployment: "dev", assigned: "feature", workTree: "main"))
        #expect(Branch.mismatch("dev is assigned branch feature, and the work tree isn't on a branch (a detached HEAD, or no repository xbind can read): check out feature") == nil)
        #expect(Branch.mismatch("live reload is paused: resume it onto dev instead") == nil)
        let d = Branch.mismatchDialog(Branch.mismatch(err)!, error: err)
        #expect(d.title == "dev requires branch feature" && d.ok == "Use main this time")
        for bad in ["-x", ".x", "a..b", "a//b", "a/", "x.lock", "HEAD", "a b", ""] { #expect(!Branch.nameOK(bad), "\(bad)") }
        for good in ["feature", "feat/x", "release-1.2+x", "A_b"] { #expect(Branch.nameOK(good), "\(good)") }
    }
}

extension DeploymentsState {
    func with(_ f: (inout DeploymentsState) -> Void) -> DeploymentsState {
        var s = self
        f(&s)
        return s
    }
}
