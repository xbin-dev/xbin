import Foundation
import XbinCore

// A Debug build's `-XbinAgentDemo <state>` (D233; the precedent is
// `-XbinPairingDemo`): the workspace agent's chrome drawn from canned
// answers — what xbind would say, shaped as docs/protocol/delegation.md
// shapes it and taken from the corpus the delegation words' vectors are
// made of (AgentDemoData.swift, generated) — for the states a test xbind
// can't be put in: an admin before setup, an agent held after a restart or
// a downgrade, workspace agents turned off, no language model, a lost
// connection, the recent-sign-in step of an admin batch, a tile agent's
// start in a mode that never asks.
//
// With a demo on, WorkspaceAgentModel sends NOTHING of its own to the
// network: every read and every decision is answered here. (The rest of the
// app — sign-in, Home, tiles — is the workspace's, live.) A Release build
// has none of this.
//
//   setup       an admin before setup (workspace agents never on, no model)
//   setup-who   on, and nobody was given an agent yet (the third step)
//   off         turned off for the workspace
//   nomodel     no language model connected
//   locked      held: locked after a restart
//   older       held: waiting for a newer xbin
//   held        held, with xbind's own reason
//   none        an admin who gave others an agent and not themselves: nothing shown
//   paused      paused by the person
//   grants      a tile call, a start in a mode that never asks, a start in a
//               mode xbind doesn't vouch for, a start that asks, and a
//               proposed admin batch whose apply asks for the password
//               (`demo-password` is the right one) and then partly fails
//   offline     live until the app has been to the background once; from
//               then on xbind can't be reached

#if DEBUG
@MainActor
final class AgentDemo {
    /// The demo this launch runs, if any.
    static let shared: AgentDemo? = {
        guard let name = UserDefaults.standard.string(forKey: "XbinAgentDemo"), !name.isEmpty else { return nil }
        return AgentDemo(name)
    }()

    /// The password the batch's recent-sign-in step accepts.
    static let password = "demo-password"

    let name: String
    private let data: JSONValue
    private var state: [String: JSONValue]
    private var llm: JSONValue
    private var requests: [[String: JSONValue]] = []
    private var changesets: [[String: JSONValue]] = []
    private var approvals: [JSONValue] = []
    private var prefs: [String: JSONValue] = ["start": "ask"]
    /// `offline`: set once the app has been to the background.
    private var cut = false
    private var observer: NSObjectProtocol?

    private init(_ name: String) {
        self.name = name
        data = (try? JSONValue(parsing: AgentDemoData.json)) ?? .null
        let pick: [String: String] = ["setup": "never-on-admin", "setup-who": "nobody-chosen", "off": "off", "nomodel": "no-model",
                                      "locked": "locked", "older": "older", "held": "held-other", "none": "admin-without-one",
                                      "paused": "paused", "grants": "page"]
        let chosen = data["states"]?[pick[name] ?? "page"]
        state = chosen?["state"]?.objectValue ?? [:]
        llm = chosen?["llm"] ?? ["configured": true]
        if llm.isNull { llm = ["configured": true] }
        if name == "grants" {
            // times of now: asked a moment ago, a day (a week) to decide
            let now = Date()
            requests = (data["requests"]?.arrayValue ?? []).compactMap(\.objectValue).enumerated().map { i, r in
                var r = r
                r["created"] = .string(DelegateTime.rfc3339(now.addingTimeInterval(Double(-120 + i * 20))))
                r["expires"] = .string(DelegateTime.rfc3339(now.addingTimeInterval(86_400 - 120)))
                return r
            }
            if var cs = data["changeset"]?.objectValue {
                cs["created"] = .string(DelegateTime.rfc3339(now.addingTimeInterval(-300)))
                cs["expires"] = .string(DelegateTime.rfc3339(now.addingTimeInterval(7 * 86_400 - 300)))
                changesets = [cs]
            }
            approvals = data["approvals"]?.arrayValue ?? []
            state["canAdminChanges"] = true
            state["adminChanges"] = true
        }
        if let plan = data["plans"]?[name]?.objectValue {
            // a plan to review (D238): as an admin who never opted in, as a
            // person who is no admin, or as an admin asked by someone else
            var cs = plan
            let now = Date()
            cs["created"] = .string(DelegateTime.rfc3339(now.addingTimeInterval(-120)))
            cs["expires"] = .string(DelegateTime.rfc3339(now.addingTimeInterval(7 * 86_400 - 120)))
            changesets = [cs]
            state["canAdminChanges"] = .bool(name != "plan-user")
            state["adminChanges"] = false
        }
        if name == "offline" {
            observer = NotificationCenter.default.addObserver(forName: Notification.Name("UIApplicationDidEnterBackgroundNotification"),
                                                              object: nil, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.cut = true }
            }
        }
    }

    /// The answer to `req`, as xbind would give it. nil: not the demo's to
    /// answer (`offline`, before the connection is cut): the network's.
    func answer(_ req: APIRequest) throws -> APIResponse? {
        if name == "offline" {
            if cut { throw URLError(.notConnectedToInternet) }
            return nil
        }
        let path = req.path.split(separator: "?", maxSplits: 1).first.map(String.init) ?? req.path
        let body = (try? JSONValue(parsing: req.body ?? Data("{}".utf8))) ?? .object([:])
        let d = "/api/xbin/delegate"
        func ok(_ v: JSONValue) -> APIResponse { APIResponse(status: 200, headers: ["Content-Type": "application/json"], body: v.jsonData) }
        func err(_ status: Int, _ v: JSONValue) -> APIResponse { APIResponse(status: status, headers: ["Content-Type": "application/json"], body: v.jsonData) }
        let parts = path.hasPrefix(d + "/") ? path.dropFirst(d.count + 1).split(separator: "/").map(String.init) : []

        switch (req.method, path) {
        case ("GET", d + "/state"): return ok(stateNow)
        case ("PUT", d + "/state"):
            if let p = body["paused"]?.boolValue {
                state["paused"] = .bool(p)
                state["pausedAt"] = p ? .string(DelegateTime.rfc3339(Date())) : nil
            }
            if let a = body["adminChanges"]?.boolValue { state["adminChanges"] = .bool(a) }
            return ok(stateNow)
        case ("GET", "/api/xbin/llm"): return ok(llm)
        case ("GET", "/api/xbin/components"): return ok([])
        case ("GET", "/api/xbin/agent/providers"): return ok([])
        case ("GET", "/api/xbin/term/sessions"): return ok([])
        case ("GET", "/api/xbin/agent/prefs"): return ok(["agentSessions": .object(prefs)])
        case ("PUT", "/api/xbin/agent/prefs"):
            for (k, v) in body["agentSessions"]?.objectValue ?? [:] { prefs[k] = v }
            return ok(["agentSessions": .object(prefs)])
        case ("GET", d + "/requests"): return ok(["requests": .array(requests.filter { $0["state"] == "pending" }.map { .object($0) })])
        case ("GET", d + "/changesets"): return ok(["changesets": .array(changesets.map { .object($0) })])
        case ("GET", d + "/approvals"): return ok(["approvals": .array(approvals)])
        case ("GET", d + "/audit"): return ok(["calls": data["audit"] ?? []])
        default: break
        }
        guard parts.count >= 2 else { return err(404, ["error": "not in the demo"]) }
        let id = parts[1], verb = parts.count > 2 ? parts[2] : ""
        switch parts[0] {
        case "requests":
            guard let i = requests.firstIndex(where: { $0["id"]?.stringValue == id }) else { return err(404, ["error": "no such request"]) }
            if req.method == "GET" { return ok(.object(requests[i])) }
            guard requests[i]["state"] == "pending" else { return err(409, ["error": "no longer pending"]) }
            if verb == "deny" {
                requests[i]["state"] = "denied"
            } else if verb == "approve" {
                let grant = body["grant"]?.stringValue ?? "once"
                requests[i]["state"] = "approved"
                requests[i]["grant"] = .string(grant)
                if grant != "once" {
                    var a: [String: JSONValue] = ["id": .string("ap_\(id)"), "tile": requests[i]["tile"] ?? "", "scope": body["scope"] ?? "call",
                                                  "created": .string(DelegateTime.rfc3339(Date()))]
                    if body["scope"] != "tile" { a["method"] = requests[i]["method"]; a["path"] = requests[i]["target"] }
                    if grant == "hour" { a["until"] = .string(DelegateTime.rfc3339(Date().addingTimeInterval(3600))) }
                    approvals.append(.object(a))
                }
            } else {
                return err(404, ["error": "not in the demo"])
            }
            requests[i]["decided"] = .string(DelegateTime.rfc3339(Date()))
            return ok(["request": .object(requests[i])])
        case "approvals":
            approvals.removeAll { $0["id"]?.stringValue == id }
            return APIResponse(status: 204)
        case "changesets":
            guard let i = changesets.firstIndex(where: { $0["id"]?.stringValue == id }) else { return err(404, ["error": "no such change set"]) }
            if req.method == "GET" { return ok(.object(changesets[i])) }
            guard changesets[i]["state"] == "proposed" || verb == "undo" || verb == "ask" else { return err(409, ["error": "no longer proposed"]) }
            switch verb {
            case "reject":
                changesets[i]["state"] = "rejected"
                return ok(.object(changesets[i]))
            case "ask":
                // the lines that need an admin go to the admins: marked "asked"
                changesets[i]["items"] = .array((changesets[i]["items"]?.arrayValue ?? []).map { item in
                    guard var it = item.objectValue, it["needsAdmin"] == true, it["result"] == nil else { return item }
                    it["result"] = "asked"
                    return .object(it)
                })
                return ok(.object(changesets[i]))
            case "approve" where changesets[i]["plan"] == true:
                // a plan: an admin's line needs the recent sign-in — the admin's own tick is the consent for it —;
                // personal lines need nothing
                let ticked = (body["items"]?.arrayValue ?? []).compactMap { $0.intValue.map(Int.init) }
                let items = (changesets[i]["items"]?.arrayValue ?? []).compactMap(\.objectValue)
                let admin = items.contains { ticked.contains(Int($0["n"]?.intValue ?? 0)) && $0["admin"] == true }
                if admin, body["password"]?.stringValue != Self.password {
                    return err(403, ["error": body["password"] == nil ? "a recent sign-in is needed" : "wrong password", "stepUp": "password"])
                }
                changesets[i]["items"] = .array(items.map { item in
                    var it = item
                    if it["needsAdmin"] == true || it["result"] == "asked" { return .object(it) }
                    it["result"] = ticked.contains(Int(it["n"]?.intValue ?? 0)) ? "applied" : "rejected"
                    return .object(it)
                })
                changesets[i]["state"] = "applied"
                changesets[i]["decided"] = .string(DelegateTime.rfc3339(Date()))
                return ok(.object(changesets[i]))
            case "approve":
                // xbind's recent-sign-in rule: this sign-in is older than ten minutes
                guard body["password"]?.stringValue == Self.password else {
                    return err(403, ["error": body["password"] == nil ? "a recent sign-in is needed" : "wrong password", "stepUp": "password"])
                }
                changesets[i] = applied(changesets[i], ticked: (body["items"]?.arrayValue ?? []).compactMap { $0.intValue.map(Int.init) })
                return ok(.object(changesets[i]))
            case "undo":
                let items = (changesets[i]["items"]?.arrayValue ?? []).filter { $0["result"] == "applied" && !($0["inverse"]?.isNull ?? true) }
                guard !items.isEmpty else { return err(409, ["error": "nothing of it can be undone"]) }
                let undone: [JSONValue] = items.enumerated().map { n, it in
                    ["n": .int(Int64(n + 1)), "kind": "grant.revoke", "args": .object([:]),
                     "line": .string("Undo: \(it["line"]?.stringValue ?? "")"), "before": it["after"] ?? .null, "after": it["before"] ?? .null]
                }
                let now = Date()
                let set: [String: JSONValue] = ["id": .string("\(id)_undo"), "user": changesets[i]["user"] ?? "", "title": "", "undoOf": .string(id),
                                                "state": "proposed", "created": .string(DelegateTime.rfc3339(now)),
                                                "expires": .string(DelegateTime.rfc3339(now.addingTimeInterval(7 * 86_400))), "items": .array(undone)]
                changesets.append(set)
                return ok(.object(set))
            default:
                return err(404, ["error": "not in the demo"])
            }
        default:
            return err(404, ["error": "not in the demo"])
        }
    }

    /// The state with its counts as they are now.
    private var stateNow: JSONValue {
        var s = state
        if name == "grants" || name.hasPrefix("plan") {
            s["pendingRequests"] = .int(Int64(requests.filter { $0["state"] == "pending" }.count))
            s["openChangeSets"] = .int(Int64(changesets.filter { $0["state"] == "proposed" }.count))
            s["standingApprovals"] = .int(Int64(approvals.count))
        }
        return .object(s)
    }

    /// The batch after an apply, as xbind answers one that partly failed:
    /// the first ticked change applied, the second failed, the ones after it
    /// skipped; what the person left out, rejected; a blocked one, blocked.
    private func applied(_ set: [String: JSONValue], ticked: [Int]) -> [String: JSONValue] {
        var set = set
        var seen = 0
        let items: [JSONValue] = (set["items"]?.arrayValue ?? []).map { item in
            guard var it = item.objectValue else { return item }
            let n = Int(it["n"]?.intValue ?? 0)
            if !(it["blocked"]?.stringValue ?? "").isEmpty {
                it["result"] = "blocked"
            } else if !ticked.contains(n) {
                it["result"] = "rejected"
            } else {
                seen += 1
                if seen == 1 {
                    it["result"] = "applied"
                    it["inverse"] = ["method": "DELETE", "path": "/grants"]
                } else if seen == 2 {
                    it["result"] = "failed"
                    it["error"] = "the tile was changed since the agent proposed this"
                } else {
                    it["result"] = "skipped"
                }
            }
            return .object(it)
        }
        set["items"] = .array(items)
        set["state"] = seen > 1 ? "partial" : "applied"
        set["decided"] = .string(DelegateTime.rfc3339(Date()))
        return set
    }
}
#endif
