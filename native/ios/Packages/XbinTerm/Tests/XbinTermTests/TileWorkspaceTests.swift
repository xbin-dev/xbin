import Foundation
import Testing
@testable import XbinTerm

/// A tile's sessions screen (D132): its tabs kept in step with the session
/// directory, and the launcher — held to web/frame-launcher.js `launcher`.
@Suite struct TileWorkspaceTests {
    static func row(_ id: String, _ kind: TermDirectoryEntry.Kind = .shell, cwd: String = "apps/crm", name: String = "",
                    provider: String = "") -> TermDirectoryEntry {
        TermDirectoryEntry(id: id, cwd: cwd, name: name, kind: kind, provider: provider)
    }

    @Test func everyLiveSessionOnTheTileIsATab() {
        var t = SessionTabs()
        t.sync([Self.row("s1"), Self.row("a1", .agent, provider: "claude"), Self.row("x", cwd: "apps/other")], cwd: "apps/crm")
        #expect(t.tabs.map(\.session) == ["s1", "a1"])
        #expect(t.tabs.map { $0.label() } == ["Bash", "claude"])
        #expect(t.tabs[1].label { $0 == "claude" ? "Claude Code" : nil } == "Claude Code")
        // A rename elsewhere reaches the tab; a later session is appended.
        t.sync([Self.row("s1", name: "server"), Self.row("a1", .agent, provider: "claude"), Self.row("s2")], cwd: "apps/crm")
        #expect(t.tabs.map { $0.label() } == ["server", "claude", "Bash"])
        let keys = t.tabs.map(\.key)
        t.sync([Self.row("s1", name: "server"), Self.row("a1", .agent, provider: "claude"), Self.row("s2")], cwd: "apps/crm")
        #expect(t.tabs.map(\.key) == keys, "keys are stable")
    }

    @Test func aSessionThatGoesAwayLeavesItsTabEnded() {
        var t = SessionTabs()
        t.sync([Self.row("s1"), Self.row("a1", .agent)], cwd: "apps/crm")
        t.sync([Self.row("s1")], cwd: "apps/crm")
        #expect(t.tab(session: "a1")?.ended == true && t.tab(session: "s1")?.ended == false)
        // Closing an ended tab doesn't keep it among the running ones.
        let a = t.tab(session: "a1")!.key
        #expect(t.close(a) == t.tab(session: "s1")?.key)
        #expect(t.closed.isEmpty)
    }

    @Test func aNewShellWaitsForItsIdAndAbsorbsTheRowAListingMadeMeanwhile() {
        var t = SessionTabs()
        let k = t.newShell(vm: true)
        #expect(t.tab(k)?.session == nil && t.tab(k)?.vm == true)
        // Not listed yet: a listing without it doesn't end it.
        t.sync([], cwd: "apps/crm")
        #expect(t.tab(k)?.ended == false)
        // The listing raced the session frame: a second tab for it…
        t.sync([Self.row("s9")], cwd: "apps/crm")
        #expect(t.tabs.count == 2)
        // …goes once the new tab learns its id.
        t.bind(k, session: "s9")
        #expect(t.tabs.map(\.key) == [k] && t.tab(k)?.session == "s9")
        // Bound but not listed yet: a stale listing doesn't end it either.
        var u = SessionTabs()
        let j = u.newShell(vm: false)
        u.bind(j, session: "s3")
        u.sync([], cwd: "apps/crm")
        #expect(u.tab(j)?.ended == false)
    }

    @Test func closingALiveTabKeepsItsSessionRunningOffTheStrip() {
        var t = SessionTabs()
        t.sync([Self.row("s1"), Self.row("s2")], cwd: "apps/crm")
        let k1 = t.tab(session: "s1")!.key
        let next = t.close(k1)
        #expect(next == t.tab(session: "s2")?.key)
        #expect(t.closed == ["s1"])
        t.sync([Self.row("s1"), Self.row("s2")], cwd: "apps/crm")
        #expect(t.tabs.map(\.session) == ["s2"], "a closed tab doesn't come back with the next listing")
        #expect(t.running([Self.row("s1"), Self.row("s2")], cwd: "apps/crm").map(\.id) == ["s1"])
        // Opened again from the launcher's Running list.
        let k = t.open(session: "s1", kind: .shell)
        #expect(t.tab(k)?.session == "s1" && t.closed.isEmpty)
        #expect(t.open(session: "s1", kind: .shell) == k, "one tab per session")
        // It ended while closed: forgotten.
        _ = t.close(k)
        t.sync([Self.row("s2")], cwd: "apps/crm")
        #expect(t.closed.isEmpty)
        #expect(t.close(t.tab(session: "s2")!.key) == nil, "the last tab: nothing left to show")
    }

    @Test func neighboursWrapAround() {
        var t = SessionTabs()
        t.sync([Self.row("a"), Self.row("b"), Self.row("c")], cwd: "apps/crm")
        let k = t.tabs.map(\.key)
        #expect(t.neighbour(of: k[0], step: 1) == k[1])
        #expect(t.neighbour(of: k[0], step: -1) == k[2])
        #expect(t.neighbour(of: k[2], step: 1) == k[0])
    }

    static let now = ISO8601DateFormatter().date(from: "2026-09-28T12:00:00Z")!

    @Test func theLauncherAsTheWebShowsIt() {
        let history = [
            TileLauncher.Past(id: "h1", provider: "claude", name: "fix the chart", turns: 3, ended: "2026-09-28T11:57:00Z", loadable: true),
            TileLauncher.Past(id: "h2", provider: "gemini", preview: "why is it slow", turns: 1, ended: "2026-09-27T10:00:00Z"),
            TileLauncher.Past(id: "h3", provider: "codex", ended: "2026-09-28T11:59:30Z"),
        ]
        let l = TileLauncher(tile: "apps/crm", providers: [("claude", "Claude Code"), ("codex", "Codex")], history: history,
                             vmStatus: nil, vmPref: true, now: Self.now)
        #expect(l.heading == "Start a session in apps/crm")
        #expect(l.boxes.map(\.title) == ["Bash", "Claude Code", "Codex"])
        #expect(l.boxes.map(\.subtitle) == ["a shell in the sandbox", "coding agent", "coding agent"], "no VM here: the pref doesn't count")
        #expect(l.boxes.map(\.id) == ["shell", "agent:claude", "agent:codex"])
        #expect(!l.loadingAgents && l.vm == nil)
        #expect(l.recent.map(\.title) == ["fix the chart", "why is it slow", "codex session"])
        #expect(l.recent.map(\.subtitle) == ["Claude Code · 3 turns · 3m ago", "gemini · 1 turn · 1d ago", "Codex · 0 turns · now"])
        #expect(l.recent.map(\.resumable) == [true, false, false])
    }

    @Test func theVMSwitchAndTheTarget() {
        let vm = TermVMStatus(available: true, emulated: true, memMiB: 2048, vcpus: 2)
        let on = TileLauncher(tile: "apps/crm", providers: [], history: [], vmStatus: vm, vmPref: true,
                              target: "· target: dev", now: Self.now)
        #expect(on.loadingAgents)
        #expect(on.boxes.map(\.subtitle) == ["a shell in a VM sandbox · target: dev"])
        #expect(on.vm == TileLauncher.VMSwitch(on: true, title: "VM sandbox: on",
                                                subtitle: "root in its own kernel · 2048 MiB, 2 vCPU · emulated — several times slower"))
        let off = TileLauncher(tile: "apps/crm", providers: [("fake", "Fake agent")], history: [],
                               vmStatus: TermVMStatus(available: true), vmPref: false, now: Self.now)
        #expect(off.vm?.title == "VM sandbox: off" && off.vm?.subtitle == "root in its own kernel")
        #expect(off.boxes[1].subtitle == "coding agent")
        #expect(TileLauncher.wantVM(pref: true, status: TermVMStatus(available: false)) == false)
        #expect(TileLauncher.vmPrefKey("apps/crm/sub") == "termvm:apps:crm:sub")
        #expect(TileLauncher.ago("nonsense", now: Self.now) == "")
        #expect(TileLauncher.ago("2026-09-28T10:00:00Z", now: Self.now) == "2h ago")
    }

    @Test func theDirectoryCarriesTheSessionsTarget() {
        let json = #"[{"id":"s1","cwd":"apps/crm","kind":"shell","deployment":"dev"},{"id":"s2","cwd":"apps/crm"}]"#
        let rows = TermDirectory.decode(Data(json.utf8))
        #expect(rows.map(\.deployment) == ["dev", ""])
    }
}
