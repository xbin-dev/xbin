import Foundation

// A tile's live reload state and deployments as the app reads them
// (D119, D127, D131; docs/protocol.md §Tile deployments,
// `GET /api/xbin/deployments?tile=`): every field optional and
// type-tolerant, as the web's view reads it — a fact the state doesn't
// carry is left out, never guessed. DeployView (DeployView.swift) turns it
// into what the Deployments tool shows, ported from web/deploy-state.js,
// web/deploy-branch.js and web/deploy-panel.js.

/// A `{ok, why?, kind?}` judgement of one operation by the server.
public struct DeployCan: Equatable, Sendable {
    public var ok: Bool
    public var why: String
    public var kind: String

    public init(ok: Bool, why: String = "", kind: String = "") {
        self.ok = ok; self.why = why; self.kind = kind
    }

    init?(_ j: JSONValue?) {
        guard let o = j?.objectValue else { return nil }
        ok = o["ok"]?.boolValue == true
        why = o["why"]?.stringValue ?? ""
        kind = o["kind"]?.stringValue ?? ""
    }

    static func map(_ j: JSONValue?) -> [String: DeployCan] {
        var out: [String: DeployCan] = [:]
        for (k, v) in j?.objectValue ?? [:] { if let c = DeployCan(v) { out[k] = c } }
        return out
    }
}

/// Who did something, and when (`liveReloadSince`).
public struct DeployStamp: Equatable, Sendable {
    public var by: String
    public var at: String
    public var agent: Bool

    public init(by: String = "", at: String = "", agent: Bool = false) {
        self.by = by; self.at = at; self.agent = agent
    }

    init?(_ j: JSONValue?) {
        guard let o = j?.objectValue else { return nil }
        by = o["by"]?.stringValue ?? ""
        at = o["at"]?.stringValue ?? ""
        agent = o["agent"]?.boolValue == true
    }
}

/// One attempt of the deploy log (`DeployEntry`), or a deployment's
/// `lastDeploy`.
public struct DeployEntry: Equatable, Sendable, Identifiable {
    public var id: String
    public var deployment: String
    /// deploy | promote | rollback | reload-now | resume | pause | attach | add | protect | reassign | restart
    public var how: String
    public var by: String
    public var agent: Bool
    public var at: String
    public var requestedAt: String
    public var finishedAt: String
    /// ok | failed | running | queued | cancelled
    public var result: String
    public var error: String
    public var phase: String
    public var checkpoint: String
    public var previous: String
    public var from: String
    public var feed: String
    public var followsWorkTree: Bool
    /// The branch its capture was taken on (D131).
    public var branch: String

    public init(id: String = "", deployment: String = "", how: String = "", by: String = "", agent: Bool = false, at: String = "",
                requestedAt: String = "", finishedAt: String = "", result: String = "", error: String = "", phase: String = "",
                checkpoint: String = "", previous: String = "", from: String = "", feed: String = "", followsWorkTree: Bool = false,
                branch: String = "") {
        self.id = id; self.deployment = deployment; self.how = how; self.by = by; self.agent = agent; self.at = at
        self.requestedAt = requestedAt; self.finishedAt = finishedAt; self.result = result; self.error = error; self.phase = phase
        self.checkpoint = checkpoint; self.previous = previous; self.from = from; self.feed = feed
        self.followsWorkTree = followsWorkTree; self.branch = branch
    }

    public init?(json j: JSONValue?) {
        guard let o = j?.objectValue else { return nil }
        func s(_ k: String) -> String {
            if let v = o[k]?.stringValue { return v }
            if let n = o[k]?.intValue { return String(n) }
            return ""
        }
        self.init(id: s("id"), deployment: s("deployment"), how: s("how"), by: s("by"), agent: o["agent"]?.boolValue == true,
                  at: s("at"), requestedAt: s("requestedAt"), finishedAt: s("finishedAt"), result: s("result"), error: s("error"),
                  phase: s("phase"), checkpoint: s("checkpoint"), previous: s("previous"), from: s("from"), feed: s("feed"),
                  followsWorkTree: o["followsWorkTree"]?.boolValue == true, branch: s("branch"))
    }
}

/// One deployment of the tile.
public struct DeploymentInfo: Equatable, Sendable, Identifiable {
    public var name: String
    public var id: String { name }
    public var primary: Bool
    public var liveReload: Bool
    /// The checkpoint it is pinned to ("" while it follows the work tree).
    public var checkpoint: String
    public var status: Status
    public var url: String
    public var api: String
    public var lastDeploy: DeployEntry?
    public var can: [String: DeployCan]
    /// The branch the work tree must be on to feed it (D131), and the one
    /// it takes this time.
    public var branch: String
    public var branchOverride: String
    public var dataState: String

    public struct Status: Equatable, Sendable {
        /// healthy | static | crash-looping | stopped | …
        public var state: String
        /// What it serves now: a checkpoint id, or "work-tree"; "" unknown.
        public var serving: String
        public var deploying: Bool
        public var deployingCheckpoint: String
        public var queued: Int
        public var error: String

        public init(state: String = "", serving: String = "", deploying: Bool = false, deployingCheckpoint: String = "",
                    queued: Int = 0, error: String = "") {
            self.state = state; self.serving = serving; self.deploying = deploying
            self.deployingCheckpoint = deployingCheckpoint; self.queued = queued; self.error = error
        }
    }

    public init(name: String, primary: Bool = false, liveReload: Bool = false, checkpoint: String = "", status: Status = Status(),
                url: String = "", api: String = "", lastDeploy: DeployEntry? = nil, can: [String: DeployCan] = [:], branch: String = "",
                branchOverride: String = "", dataState: String = "") {
        self.name = name; self.primary = primary; self.liveReload = liveReload; self.checkpoint = checkpoint; self.status = status
        self.url = url; self.api = api; self.lastDeploy = lastDeploy; self.can = can; self.branch = branch
        self.branchOverride = branchOverride; self.dataState = dataState
    }

    init?(json j: JSONValue) {
        guard let o = j.objectValue, let name = o["name"]?.stringValue, !name.isEmpty else { return nil }
        let st = o["status"]?.objectValue ?? [:]
        let dep = st["deploying"]
        self.init(name: name, primary: o["primary"]?.boolValue == true, liveReload: o["liveReload"]?.boolValue == true,
                  checkpoint: o["checkpoint"]?["id"]?.stringValue ?? "",
                  status: Status(state: st["state"]?.stringValue ?? "", serving: st["serving"]?.stringValue ?? "",
                                 deploying: dep != nil && dep?.isNull == false && dep?.boolValue != false,
                                 deployingCheckpoint: dep?["checkpoint"]?.stringValue ?? "",
                                 queued: st["queued"]?.arrayValue?.count ?? 0, error: st["error"]?.stringValue ?? ""),
                  url: o["url"]?.stringValue ?? "", api: o["api"]?.stringValue ?? "",
                  lastDeploy: DeployEntry(json: o["lastDeploy"]), can: DeployCan.map(o["can"]),
                  branch: o["branch"]?.stringValue ?? "", branchOverride: o["branchOverride"]?.stringValue ?? "",
                  dataState: o["data"]?["state"]?.stringValue ?? "")
    }
}

/// The tile's state in the caller's view.
public struct DeploymentsState: Equatable, Sendable {
    public var tile: String
    /// false: the tile never opted in (the zero state).
    public var record: Bool
    /// full | deployment | reader
    public var view: String
    public var features: [String]
    public var seq: Int?
    public var primary: String
    /// The live reload target; "" = paused.
    public var liveReload: String
    public var lastLiveReload: String
    public var liveReloadSince: DeployStamp?
    public var workTree: WorkTree?
    public var protectedPrimary: Bool
    public var allowed: [String: DeployCan]
    public var deployments: [DeploymentInfo]
    public var caller: Caller

    public struct WorkTree: Equatable, Sendable {
        /// Files changed since `since` (while paused).
        public var changed: Int?
        public var since: String
        /// The work tree's branch ("" detached or none); nil when the state
        /// doesn't say (an older xbind, a reader).
        public var branch: String?

        public init(changed: Int? = nil, since: String = "", branch: String? = nil) {
            self.changed = changed; self.since = since; self.branch = branch
        }
    }

    public struct Caller: Equatable, Sendable {
        public var level: String
        public var manager: Bool
        public var readOnly: Bool
        public var can: [String: DeployCan]

        public init(level: String = "", manager: Bool = false, readOnly: Bool = false, can: [String: DeployCan] = [:]) {
            self.level = level; self.manager = manager; self.readOnly = readOnly; self.can = can
        }
    }

    public init(tile: String, record: Bool = true, view: String = "full", features: [String] = [], seq: Int? = nil,
                primary: String = "main", liveReload: String = "main", lastLiveReload: String = "", liveReloadSince: DeployStamp? = nil,
                workTree: WorkTree? = nil, protectedPrimary: Bool = false, allowed: [String: DeployCan] = [:],
                deployments: [DeploymentInfo] = [], caller: Caller = Caller()) {
        self.tile = tile; self.record = record; self.view = view; self.features = features; self.seq = seq
        self.primary = primary; self.liveReload = liveReload; self.lastLiveReload = lastLiveReload
        self.liveReloadSince = liveReloadSince; self.workTree = workTree; self.protectedPrimary = protectedPrimary
        self.allowed = allowed; self.deployments = deployments; self.caller = caller
    }

    public init?(json j: JSONValue) {
        guard let o = j.objectValue, let tile = o["tile"]?.stringValue else { return nil }
        var wt: WorkTree?
        if let w = o["workTree"]?.objectValue {
            wt = WorkTree(changed: w["changed"]?.intValue.map { Int($0) }, since: w["since"]?.stringValue ?? "",
                          branch: w["branch"]?.stringValue)
        }
        let c = o["caller"]?.objectValue ?? [:]
        self.init(tile: tile, record: o["record"]?.boolValue == true, view: o["view"]?.stringValue ?? "full",
                  features: o["features"]?.arrayValue?.compactMap(\.stringValue) ?? [], seq: o["seq"]?.intValue.map { Int($0) },
                  primary: o["primary"]?.stringValue ?? "main", liveReload: o["liveReload"]?.stringValue ?? "",
                  lastLiveReload: o["lastLiveReload"]?.stringValue ?? "", liveReloadSince: DeployStamp(o["liveReloadSince"]),
                  workTree: wt, protectedPrimary: o["protectedPrimary"]?.boolValue == true, allowed: DeployCan.map(o["allowed"]),
                  deployments: o["deployments"]?.arrayValue?.compactMap(DeploymentInfo.init(json:)) ?? [],
                  caller: Caller(level: c["level"]?.stringValue ?? "", manager: c["manager"]?.boolValue == true,
                                 readOnly: c["readOnly"]?.boolValue == true, can: DeployCan.map(c["can"])))
    }

    public func deployment(_ name: String) -> DeploymentInfo? { deployments.first { $0.name == name } }

    /// This xbind speaks `feature` (`live-reload/1`, `deployments/1`, `branches/1`).
    public func speaks(_ feature: String) -> Bool { features.contains(feature) }
}

/// A dry run's `impact`: what an operation will do. Only what it says is
/// shown.
public struct DeployImpact: Equatable, Sendable {
    public var code: Code?
    /// nobody | everyone | deployment
    public var affects: String
    public var pausesLiveReload: Bool
    public var reloads: [String]?
    public var stops: [String]
    public var branch: Branch?

    public struct Code: Equatable, Sendable {
        public var from: String
        public var to: String
        public var files: Int
        public var added: Int
        public var removed: Int

        public init(from: String = "", to: String = "", files: Int = 0, added: Int = 0, removed: Int = 0) {
            self.from = from; self.to = to; self.files = files; self.added = added; self.removed = removed
        }
    }

    /// `impact.branch` (D131): the deployment's branch and the work tree's.
    public struct Branch: Equatable, Sendable {
        public var deployment: String
        public var assigned: String
        public var workTree: String?
        /// The request takes the work tree's branch this time.
        public var other: Bool

        public init(deployment: String = "", assigned: String = "", workTree: String? = nil, other: Bool = false) {
            self.deployment = deployment; self.assigned = assigned; self.workTree = workTree; self.other = other
        }
    }

    public init(code: Code? = nil, affects: String = "", pausesLiveReload: Bool = false, reloads: [String]? = nil, stops: [String] = [],
                branch: Branch? = nil) {
        self.code = code; self.affects = affects; self.pausesLiveReload = pausesLiveReload; self.reloads = reloads
        self.stops = stops; self.branch = branch
    }

    public init(json j: JSONValue?) {
        let o = j?.objectValue ?? [:]
        var code: Code?
        if let c = o["code"]?.objectValue {
            func i(_ k: String) -> Int { c[k]?.intValue.map { Int($0) } ?? 0 }
            code = Code(from: c["from"]?.stringValue ?? "", to: c["to"]?.stringValue ?? "", files: i("files"), added: i("added"),
                        removed: i("removed"))
        }
        var b: Branch?
        if let x = o["branch"]?.objectValue {
            b = Branch(deployment: x["deployment"]?.stringValue ?? "", assigned: x["assigned"]?.stringValue ?? "",
                       workTree: x["workTree"]?.stringValue, other: x["other"]?.boolValue == true)
        }
        self.init(code: code, affects: o["affects"]?.stringValue ?? "", pausesLiveReload: o["pausesLiveReload"]?.boolValue == true,
                  reloads: o["reloads"]?.arrayValue?.compactMap(\.stringValue), stops: o["stops"]?.arrayValue?.compactMap(\.stringValue) ?? [],
                  branch: b)
    }
}

/// The routes the app uses (docs/protocol.md §Tile deployments). A query
/// names the tile by its path; a JSON body by its ref.
public enum DeploymentsRoute {
    public static let liveReload1 = "live-reload/1"
    public static let deployments1 = "deployments/1"
    public static let branches1 = "branches/1"

    public static func state(tile: String) -> String { "/api/xbin/deployments?tile=\(URLComponent.encode(tile))" }

    public static func log(tile: String, deployment: String? = nil, limit: Int = 20) -> String {
        var p = "/api/xbin/deployments/log?tile=\(URLComponent.encode(tile))&limit=\(limit)"
        if let deployment, !deployment.isEmpty { p += "&deployment=\(URLComponent.encode(deployment))" }
        return p
    }

    /// The POST route of an operation the app runs.
    public static func op(_ op: String) -> String {
        switch op {
        case "pause": return "/api/xbin/deployments/live-reload/pause"
        case "resume": return "/api/xbin/deployments/live-reload/resume"
        case "reloadNow": return "/api/xbin/deployments/live-reload/now"
        case "attach": return "/api/xbin/deployments/live-reload/attach"
        case "rollback", "undo": return "/api/xbin/deployments/rollback"
        case "branch": return "/api/xbin/deployments/branch"
        default: return "/api/xbin/deployments/\(op)"
        }
    }

    /// Whether an answer means "this xbind has no tile deployments": a
    /// plain 404 or 405 (the feature's detection, §Tile deployments).
    public static func absent(status: Int) -> Bool { status == 404 || status == 405 }

    /// A path the app opens signed in, in its own web view with the user's
    /// cookie session (D125's way for chrome): a deployment's URL,
    /// `<tile>+<name>` of a known tile (people with write, checked on every
    /// request — a frame token names a deployment by a parameter, not by
    /// its path), and the web shell, `root`.
    public static func opensSignedIn(_ path: String, known: (String) -> Bool) -> Bool {
        if path == "root" { return true }
        guard !known(path), let plus = path.lastIndex(of: "+") else { return false }
        let base = String(path[..<plus]), name = path[path.index(after: plus)...]
        return !name.isEmpty && !name.contains("/") && known(base)
    }
}
