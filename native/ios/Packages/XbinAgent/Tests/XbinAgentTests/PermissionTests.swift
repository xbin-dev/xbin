import Foundation
import Testing
@testable import XbinAgent

@Suite struct PermissionTests {
    func cards(_ name: String) throws -> [PermissionCard] {
        AgentTranscript(events: try events(name)).items.compactMap { if case .permission(let c) = $0 { return c }; return nil }
    }

    @Test func toolPermissionsFromTheFake() throws {
        let cs = try cards("permissions")
        #expect(cs.count == 4)
        let r = cs[0].request
        #expect(!cs[0].isPlanApproval)
        #expect(cs[0].heading == "run ls")
        #expect(cs[0].choices.map(\.label) == ["Allow once", "Allow always", "Reject"])
        #expect(cs[0].choices.map(\.answer) == [.option("once"), .option("always"), .option("no")])
        #expect(PermissionRules.ruleNote(r) == "“Allow for the session” auto-approves later execute calls titled “run ls”.")
        #expect(PermissionRules.detail(r) == "run ls")
        #expect(cs.map { $0.settlement?.verb } == ["allowed", "denied", "allowed", "allowed"])
        #expect(cs.map { $0.settlement?.by.displayName } == ["alice", "bob", "owner", "a session rule"])
        let cancelled = try cards("cancel")
        #expect(cancelled.first?.settlement?.verb == "cancelled" && cancelled.first?.settlement?.option == nil)
    }

    @Test func defaultToNoPutsRejectsFirst() {
        let r = PermissionRequest(json: j(#"""
        {"pid":"p","toolCall":{"id":"t","kind":"edit","title":"Write x"},"rule":{"kind":"edit","title":"Write x","scoped":true},
         "meta":{"defaultToNo":true},
         "options":[{"optionId":"a1","name":"Yes","kind":"allow_once"},{"optionId":"r1","name":"No","kind":"reject_once"},
                    {"optionId":"a2","name":"Always","kind":"allow_always"},{"optionId":"r2","name":"Never","kind":"reject_always"}]}
        """#))!
        #expect(PermissionRules.orderedOptions(r).map(\.optionId) == ["r1", "r2", "a1", "a2"])
        #expect(PermissionRules.choices(r).first?.isReject == true)
    }

    @Test func unscopedHidesAllowForTheSession() {
        var r = PermissionRequest(json: j(#"""
        {"pid":"p","toolCall":{"id":"t"},"rule":{"kind":"","title":"","scoped":false},
         "options":[{"optionId":"a1","name":"Yes","kind":"allow_once"},{"optionId":"a2","name":"Always","kind":"allow_always"},{"optionId":"r1","name":"No","kind":"reject_once"}]}
        """#))!
        #expect(PermissionRules.choices(r).map(\.id) == ["a1", "r1"])
        #expect(PermissionRules.ruleNote(r) == nil)
        r.options = []
        #expect(PermissionRules.choices(r).map(\.answer) == [.decision(.allowOnce), .decision(.rejectOnce)])
        r.rule = PermissionRule(kind: "execute", title: "", scoped: true)
        #expect(PermissionRules.choices(r).map(\.label) == ["Allow once", "Allow for the session", "Deny"])
        #expect(PermissionRules.ruleNote(r) == "“Allow for the session” auto-approves later execute calls.")
        // a snapshot's pending request derives the rule the daemon's way
        let pend = PendingPermission(json: j(#"{"pid":"p9","toolCall":{"id":"x","kind":"switch_mode","title":"Approve"},"options":[]}"#))!
        #expect(pend.request.rule?.scoped == false)
    }

    @Test func headingRule() {
        // Claude titles a Bash ask with the command itself: the headline (its description) wins
        let bash = PermissionRequest(json: j(#"""
        {"pid":"p","toolCall":{"id":"t","kind":"execute","title":"npm test","rawInput":{"command":"npm test","description":"Run the tests"}},
         "meta":{"title":" npm test "},"options":[]}
        """#))!
        #expect(PermissionRules.heading(bash) == "Run the tests")
        let other = PermissionRequest(json: j(#"""
        {"pid":"p","toolCall":{"id":"t","kind":"execute","title":"rm -rf x","rawInput":{"command":"rm -rf x"}},
         "meta":{"title":"Delete the build?","description":"This removes x"},"options":[]}
        """#))!
        #expect(PermissionRules.heading(other) == "Delete the build?")
        #expect(PermissionRules.description(other) == "This removes x")
        #expect(PermissionRules.heading(PermissionRequest(json: j(#"{"pid":"p","toolCall":{"id":"t","kind":"execute","title":"rm -rf build"},"options":[]}"#))!) == "Remove build")
    }

    @Test func planApprovalCard() throws {
        let cs = try cards("plan")
        #expect(cs.count == 2 && cs.allSatisfy(\.isPlanApproval))
        let r = cs[0].request
        #expect(cs[0].heading == "Ready to code?")
        #expect(PermissionRules.planText(r).hasPrefix("# Fake plan"))
        #expect(PermissionRules.planFilePath(r) == "/tmp/fake-plan.md")
        let ok = PermissionRules.planApprovals(r)
        #expect(ok.map(\.id) == ["exit-plan-default", "exit-plan-clear-auto", "auto"]) // allow_always kept: they are modes
        #expect(ok.map(\.primary) == [true, false, false])
        #expect(PermissionRules.planRejections(r).map(\.label) == ["No, keep planning"])
        #expect(r.rule?.scoped == false)
        let first = try #require(cs[0].settlement)
        #expect(first.verb == "Plan approved" && first.option == "Yes, and use auto mode" && first.by.userID == "alice")
        let second = try #require(cs[1].settlement)
        #expect(second.keptPlanning && second.verb == "Kept planning" && second.by.displayName == "bob")
    }
}
