import Foundation

// Branch-assigned deployments in the app (D131, feature `branches/1`): a
// port of web/deploy-branch.js — the words when the work tree's branch and
// a deployment's differ, the offers to follow a switch, the Branch row and
// its confirmation, a mismatch's "use it this time" question.

extension DeployView {
    /// The branch facts every surface reads, for the deployment live reload
    /// follows (or last followed).
    public struct BranchFacts: Equatable, Sendable {
        /// Its assigned branch ("" none).
        public var need: String
        /// The branch it takes this time.
        public var other: String
        /// The work tree's branch; nil when the state doesn't say.
        public var workTreeBranch: String?
        /// The work tree doesn't feed it.
        public var off: Bool
        /// The deployment the work tree's branch is assigned to.
        public var related: String
        /// Live reload was paused by a branch switch (xbind's own pause).
        public var switched: Bool

        public static let none = BranchFacts(need: "", other: "", workTreeBranch: nil, off: false, related: "", switched: false)
    }

    static func branchFacts(_ s: DeploymentsState, zero: Bool, reader: Bool, attached: String, last: String) -> BranchFacts {
        if zero || reader { return .none }
        let cur = attached.isEmpty ? last : attached
        let d = s.deployment(cur)
        let lastEntry = s.deployment(last)?.lastDeploy
        let need = d?.branch ?? "", other = d?.branchOverride ?? ""
        let wtb = s.workTree?.branch
        let off = !need.isEmpty && wtb != nil && wtb != need && !(!other.isEmpty && wtb == other)
        var related = ""
        if let w = wtb, !w.isEmpty { related = s.deployments.first { $0.name != cur && $0.branch == w }?.name ?? "" }
        return BranchFacts(need: need, other: other, workTreeBranch: wtb, off: off, related: related,
                           switched: attached.isEmpty && lastEntry?.how == "pause" && lastEntry?.by == "xbind")
    }

    public enum Branch {
        public static func speaks(_ s: DeploymentsState?) -> Bool { s?.speaks(DeployView.branchesFeature) ?? false }

        static func shown(_ b: String?) -> String { (b ?? "").isEmpty ? "no branch" : b! }

        /// The sentence while a branch switch holds live reload paused.
        static func pausedSentence(_ f: BranchFacts, _ L: String, _ pin: String) -> String {
            if f.off {
                let where_ = (f.workTreeBranch ?? "").isEmpty ? "isn't on a branch" : "is on \(f.workTreeBranch!)"
                return "Live reload paused — the work tree \(where_), and \(L) requires \(f.need): \(L) keeps running \(pin)."
            }
            return "Live reload paused when the work tree left \(f.need.isEmpty ? "\(L)'s branch" : f.need); it is on \((f.workTreeBranch ?? "").isEmpty ? f.need : f.workTreeBranch!) again — resume live reload on \(L) to follow saves."
        }

        /// While live reload still follows A with the work tree off its
        /// branch: what the next save does.
        static func offSentence(_ f: BranchFacts, _ A: String) -> String {
            f.off ? " The work tree is on \(shown(f.workTreeBranch)), and \(A) requires \(f.need): the next save pauses live reload." : ""
        }

        static func launcherText(_ f: BranchFacts, _ L: String) -> String {
            f.off ? "Live reload is paused: the work tree is on \(shown(f.workTreeBranch)), and \(L) requires \(f.need)."
                : "Live reload is paused: the work tree is on \(f.need) again — resume live reload on \(L)."
        }

        /// The offers to follow a branch switch: attach to (or, paused,
        /// resume on) the deployment the work tree's branch is assigned
        /// to; resume the one that left its branch, back on it; with no
        /// deployment for the branch, keep the target on it this time
        /// (resume with `confirm: "other-branch"`) or add a deployment for
        /// it. `ctl(op, deployment)`: the operation's control.
        static func offers(_ s: DeploymentsState, _ base: Facts, _ ctl: (String, String?) -> Control) -> [Action] {
            let f = base.branch
            guard speaks(s), let w = f.workTreeBranch, !(f.need.isEmpty && f.related.isEmpty), !(!f.other.isEmpty && w == f.other) else { return [] }
            var out: [Action] = []
            let C = base.attached.isEmpty ? base.last : base.attached
            func item(_ label: String, _ op: String, _ name: String, _ title: String, other: Bool = false, branch: String = "") {
                let c = op == "attach" ? ctl("attach", name) : ctl(op == "addFor" ? "add" : op, nil)
                let id = op == "addFor" ? "addFor/\(branch)" : "\(other ? "keep" : "follow")/\(name)"
                out.append(Action(id: id, op: op, label: label, enabled: c.enabled, why: c.why, title: title, deployment: name, other: other, branch: branch))
            }
            if !f.related.isEmpty, f.related != base.attached {
                if !base.attached.isEmpty {
                    item("Attach live reload to \(f.related) (\(w))", "attach", f.related,
                         "\(f.related) requires \(w), the work tree's branch: saves reach it, and \(base.attached) is pinned to its current code.")
                } else {
                    item("Resume live reload on \(f.related) (\(w))", "resume", f.related,
                         "\(f.related) requires \(w), the work tree's branch: it follows every save.")
                }
            }
            if base.attached.isEmpty, f.switched, !f.need.isEmpty, !f.off {
                item("Resume live reload on \(C)", "resume", C, "The work tree is on \(f.need) again: \(C) follows every save.")
            }
            if f.off, !w.isEmpty, f.related.isEmpty {
                if base.attached.isEmpty {
                    item("Keep \(C) on \(w) this time", "resume", C,
                         "\(C) follows the work tree on \(w) until live reload moves or the branch changes again; it still requires \(f.need).", other: true)
                }
                item("Add a deployment for \(w)…", "addFor", "", "A new deployment that requires \(w), live reload attached to it.", branch: w)
            }
            return out
        }

        /// A confirmation's Branch line (`impact.branch`).
        static func branchLine(_ b: DeployImpact.Branch?, _ op: String) -> String {
            guard let b, !b.assigned.isEmpty else { return "" }
            if !b.other || b.workTree == b.assigned { return "Branch: \(b.deployment) requires \(b.assigned) — the work tree is on it." }
            let lasting = ["resume", "attach", "add"].contains(op) ? ", until live reload moves or the work tree's branch changes again" : ""
            return "Branch: \(b.deployment) requires \(b.assigned); it takes the work tree's \(shown(b.workTree)) this time\(lasting)."
        }

        /// The branch route's confirmation (Set branch…, Clear branch).
        static func row(_ s: DeploymentsState, _ im: DeployImpact?, _ Y: String, _ b: String?) -> (title: String, ok: String, lines: [String]) {
            guard let b, !b.isEmpty else { return ("Clear \(Y)'s branch?", "Clear branch", ["The work tree feeds \(Y) on any branch again."]) }
            var now = ""
            if let w = im?.branch?.workTree, w != b {
                now = "The work tree is on \(shown(w)) now\(im?.pausesLiveReload == true ? ": live reload is on \(Y), so the next save pauses it" : "")."
            }
            return ("Assign \(Y) branch \(b)?", "Assign branch", [
                "\(Y) requires \(b): the work tree feeds it — saves while live reload follows it, attach, resume, Reload now, a deploy of the work tree — only while \(b) is checked out.",
                now, "Nothing is deployed now."])
        }

        static func result(_ s: DeploymentsState, _ X: String) -> String {
            if let b = s.deployment(X)?.branch, !b.isEmpty { return "\(X) requires \(b)." }
            return "\(X) takes the work tree on any branch."
        }

        /// The overview's Branch row, or nil (the primary, main, an xbind
        /// without branches/1).
        public static func overviewLine(_ s: DeploymentsState, _ name: String) -> String? {
            guard let d = s.deployment(name), !d.primary, name != "main", speaks(s) else { return nil }
            if d.branch.isEmpty { return "none — the work tree feeds it on any branch" }
            return d.branch + (d.branchOverride.isEmpty ? "" : " · takes \(d.branchOverride) this time")
        }

        /// The Branch row's actions: Set branch… (or Branch: x…) and Clear branch.
        public static func actions(_ s: DeploymentsState, _ name: String) -> [Action] {
            guard overviewLine(s, name) != nil, let d = s.deployment(name) else { return [] }
            let c = DeployView.control(s, "branch", name)
            var out = [Action(id: "branch", op: "branch", label: d.branch.isEmpty ? "Set branch…" : "Branch: \(d.branch)…", enabled: c.enabled,
                              why: c.why, title: "The work tree's branch \(name) requires: it feeds \(name) only on that branch.", deployment: name)]
            if !d.branch.isEmpty {
                out.append(Action(id: "clearBranch", op: "branch", label: "Clear branch", enabled: c.enabled, why: c.why,
                                  title: "\(name) takes the work tree on any branch again.", deployment: name))
            }
            return out
        }

        /// The Set branch… form: its message and the field's first value.
        public static func dialog(_ s: DeploymentsState, _ name: String) -> (title: String, message: String, value: String) {
            let w = s.workTree?.branch ?? ""
            return ("The branch \(name) requires",
                    "The work tree feeds \(name) only while it has this branch checked out.\(w.isEmpty ? "" : " The work tree is on \(w).")",
                    (s.deployment(name)?.branch).flatMap { $0.isEmpty ? nil : $0 } ?? w)
        }

        /// The names xbind takes (checkpoint.BranchNameOK's rule).
        public static func nameOK(_ b: String) -> Bool {
            guard !b.isEmpty, b.count <= 200, b != "HEAD", b.range(of: #"^[A-Za-z0-9._+/-]+$"#, options: .regularExpression) != nil else { return false }
            return b.range(of: #"^[-.]|\.$|\.\.|//|/\.|/$|\.lock$"#, options: .regularExpression) == nil
        }

        public static let badName = "A branch name is letters, digits and . _ + / -, not starting with - or ., with no \"..\"."

        public struct Mismatch: Equatable, Sendable {
            public var deployment: String
            public var assigned: String
            public var workTree: String
        }

        /// The 409 of an op on a work tree off the deployment's branch
        /// that `confirm: "other-branch"` takes; nil for anything else.
        public static func mismatch(_ error: String) -> Mismatch? {
            let pattern = #"^(\S+) is assigned branch (\S+), and the work tree is on (\S+): check out .* send confirm:"other-branch""#
            guard let re = try? NSRegularExpression(pattern: pattern),
                  let m = re.firstMatch(in: error, range: NSRange(error.startIndex..., in: error)), m.numberOfRanges == 4 else { return nil }
            func g(_ i: Int) -> String { Range(m.range(at: i), in: error).map { String(error[$0]) } ?? "" }
            return Mismatch(deployment: g(1), assigned: g(2), workTree: g(3))
        }

        /// The question a mismatch asks instead of a bare refusal.
        public static func mismatchDialog(_ m: Mismatch, error: String) -> (title: String, message: String, ok: String) {
            ("\(m.deployment) requires branch \(m.assigned)",
             "\(error)\n\nUse \(m.workTree) for \(m.deployment) this time? It lasts until live reload moves or the work tree's branch changes again; check out \(m.assigned) instead to keep \(m.deployment) on its branch.",
             "Use \(m.workTree) this time")
        }
    }
}
