import XCTest

/// App tunnels (D204; native/spec/tunnel.md), end to end against an e2e
/// xbind that runs its iroh sidecar with a relay the simulator reaches
/// (`e2e-xbind.sh --tunnel`: `iroh-relay --dev` with a token, the
/// workspace's custom relay set through the API, the relay's port carried
/// by mac-remote.sh's ssh like xbind's). Skipped unless XBIN_E2E_TUNNEL=1
/// reaches the test runner (TEST_RUNNER_XBIN_E2E_TUNNEL) as well as the
/// usual XBIN_E2E_URL / _USER / _PASSWORD.
///
/// DRAFT: written ahead of the server half (the sidecar, the pairing routes)
/// and of the app's iroh runtime (App/Model/TunnelEndpoint.swift); the
/// steps follow plans/app-tunnel.md Appendix B Phase 7. The simulator has no
/// camera, so the pairing link is pasted on Log in.
final class XbinTunnelTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
        try XCTSkipIf(ProcessInfo.processInfo.environment["XBIN_E2E_TUNNEL"] != "1",
                      "XBIN_E2E_TUNNEL unset: the e2e xbind runs no app tunnel (e2e-xbind.sh --tunnel)")
    }

    /// Pairs on Log in: paste the link, Pair, wait for the phone's code.
    /// Returns the pairing's id and the code the phone shows.
    @MainActor
    private func pairUntilCode(_ e: E2E, shots prefix: String? = nil) async throws -> (id: String, code: String) {
        let minted = try await e.server.mintPairing()
        XCTAssertTrue(e.app.buttons["Log in"].waitForExistence(timeout: 30))
        e.app.buttons["Log in"].tap()
        let field = e.element("Paste the link", in: e.app.textFields)
        XCTAssertTrue(field.waitForExistence(timeout: 10), "Log in's link field")
        field.tap()
        field.typeText(minted.link + "\n")
        let pair = e.app.alerts.buttons["Pair"]
        XCTAssertTrue(pair.waitForExistence(timeout: 10), "the pairing question")
        if let prefix { e.shot(prefix + "-question") }
        pair.tap()
        e.dismissSystemAlerts()   // iOS's Local Network question, when it comes
        let sas = e.app.staticTexts.matching(identifier: "pairing-sas").firstMatch
        XCTAssertTrue(sas.waitForExistence(timeout: 60), "the phone's confirmation code")
        if let prefix { e.shot(prefix + "-code") }
        return (minted.pairing, sas.value as? String ?? "")
    }

    /// Pair, confirm on "the computer" (the API), and use the workspace
    /// through the tunnel: Home, a web tile, the native counter, a
    /// terminal's WebSocket, a chrome tile's "Continue as" on the loopback
    /// origin, Settings' connection row, a background round trip.
    @MainActor
    func test01PairAndUse() async throws {
        let e = try E2E(self)
        e.launchFresh()
        let (id, code) = try await pairUntilCode(e, shots: "tunnel-01")
        // The computer shows the same code (it derives its own).
        let status = try await e.server.pairingStatus(id, until: "confirm")
        XCTAssertEqual(status.sas.map(Self.grouped), code, "both sides derive the same code")
        _ = try await e.server.send("POST", "/api/xbin/devices/pairing/\(id)/confirm", json: ["match": true])
        e.dismissSystemAlerts()
        XCTAssertTrue(e.app.buttons["Workspaces"].waitForExistence(timeout: 60), "the paired workspace opens")
        e.shot("tunnel-02-home")

        // Through the forwarder: the scheme handler, a native tile's
        // backend, a terminal's WebSocket.
        e.openTile("apps/welcome")
        e.shot("tunnel-03-welcome")
        e.goHome()
        let before = try await e.server.counter()
        e.openTile("apps/counter")
        let plus = e.app.buttons["+1"]
        XCTAssertTrue(plus.waitForExistence(timeout: 30))
        plus.tap()
        await e.eventually("the counter moved on the server", timeout: 20) { (try? await e.server.counter()) == before + 1 }
        e.goHome()

        // Settings: the connection says tunnel.
        e.app.buttons["Workspaces"].tap()
        let settings = e.app.buttons["Settings"]
        if settings.waitForExistence(timeout: 10) {
            settings.tap()
            XCTAssertTrue(e.containing("Tunnel ·").waitForExistence(timeout: 20), "Settings' connection row")
            e.shot("tunnel-04-settings")
        }

        // The background drops the endpoint (iroh #4289); the front makes
        // a new one and the workspace answers again.
        XCUIDevice.shared.press(.home)
        try await Task.sleep(nanoseconds: 3_000_000_000)
        e.app.activate()
        e.goHome()
        e.openTile("apps/welcome")
        e.shot("tunnel-05-after-background")
    }

    /// "They don't" on the computer: the phone says the pairing was
    /// declined, and nothing was added.
    @MainActor
    func test02Declined() async throws {
        let e = try E2E(self)
        e.launchFresh()
        let before = try await e.server.devices().count
        let (id, _) = try await pairUntilCode(e)
        _ = try await e.server.pairingStatus(id, until: "confirm")
        _ = try await e.server.send("POST", "/api/xbin/devices/pairing/\(id)/confirm", json: ["match": false])
        XCTAssertTrue(e.containing("Pairing declined").waitForExistence(timeout: 30))
        e.shot("tunnel-06-declined")
        let after = try await e.server.devices().count
        XCTAssertEqual(after, before, "no device record")
    }

    /// The admin refuses app connections outside the tunnel: a direct
    /// sign-in meets the pairing-only page (no password form, no SSO).
    @MainActor
    func test03PairingOnlyWorkspace() async throws {
        // The workspace goes back to accepting direct connections even when
        // a check fails here: the suites after this one sign in directly.
        continueAfterFailure = true
        let e = try E2E(self)
        _ = try await e.server.send("PUT", "/api/xbin/app-tunnel", json: ["tunnelOnly": true])
        e.launchFresh()
        if e.app.buttons["Log in"].waitForExistence(timeout: 30) {
            e.app.buttons["Log in"].tap()
            e.enterAddress(e.server.address)
            XCTAssertTrue(e.containing("only accepts paired devices").waitForExistence(timeout: 30))
            XCTAssertFalse(e.element("Username", in: e.app.textFields).exists, "no password form")
            e.shot("tunnel-07-pairing-only")
        } else {
            XCTFail("no Welcome")
        }
        _ = try await e.server.send("PUT", "/api/xbin/app-tunnel", json: ["tunnelOnly": false])
    }

    static func grouped(_ s: String) -> String {
        let d = s.filter(\.isNumber)
        guard d.count == 6 else { return s }
        return d.prefix(3) + " " + d.suffix(3)
    }
}

extension E2EServer {
    struct Minted: Sendable {
        let link: String
        let pairing: String
    }

    /// `POST /api/xbin/devices/enroll-code {tunnel: true}` with the checks'
    /// fresh password session: the pairing link and the pending pairing.
    func mintPairing() async throws -> Minted {
        let data = try await send("POST", "/api/xbin/devices/enroll-code", json: ["tunnel": true])
        let j = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        guard let link = j["url"] as? String, link.contains("&tv="),
              let pairing = (j["tunnel"] as? [String: Any])?["pairing"] as? String else {
            throw Failure(description: "enroll-code {tunnel:true} answered no pairing link: \(String(decoding: data.prefix(300), as: UTF8.self))")
        }
        return Minted(link: link, pairing: pairing)
    }

    struct PairingState: Sendable {
        let state: String
        let sas: String?
    }

    /// Polls `GET /api/xbin/devices/pairing/{id}` until `state`.
    func pairingStatus(_ id: String, until state: String, timeout: TimeInterval = 60) async throws -> PairingState {
        let end = Date().addingTimeInterval(timeout)
        var last = ""
        while Date() < end {
            let data = try await send("GET", "/api/xbin/devices/pairing/\(id)")
            let j = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
            last = j["state"] as? String ?? ""
            if last == state {
                let sas = (j["sas"] as? String) ?? (j["sas"] as? Int).map { String(format: "%06d", $0) }
                return PairingState(state: last, sas: sas)
            }
            try await Task.sleep(nanoseconds: 500_000_000)
        }
        throw Failure(description: "pairing \(id) never reached \(state) (last: \(last))")
    }
}
