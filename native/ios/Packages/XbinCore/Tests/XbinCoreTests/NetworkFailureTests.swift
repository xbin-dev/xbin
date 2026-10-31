import Foundation
import Testing
@testable import XbinCore

// A transport's failure as the layers above see it (D213): the app tunnel
// that can't come up is a URL error like a dropped network's — same
// domain, codes and words — with the tunnel's own reason kept under it.

@Suite struct NetworkFailureTests {
    struct TunnelReason: Error, CustomStringConvertible { var description: String { "the relay didn't answer" } }

    @Test func aTransportsOwnReasonBecomesAURLError() {
        let e = NetworkFailure.wrap(TunnelReason(), as: .cannotConnectToHost) as NSError
        #expect(e.domain == "NSURLErrorDomain" && e.code == -1004)
        #expect(e.localizedDescription == "Could not connect to the server.")
        #expect(NetworkFailure.underlying(e) != nil)
        #expect(NetworkFailure.reason(e) == "the relay didn't answer")   // its words, kept
        // …and the onboarding screens and every caller read it as a network failure
        #expect(ConnectProblem(e, server: nil).kind == .cantConnect)
        #expect(NetworkFailure.isNetwork(e))
    }

    @Test func aNetworkFailureStaysAsItIs() {
        let lost = NSError(domain: "NSURLErrorDomain", code: -1005)
        #expect(NetworkFailure.wrap(lost, as: .cannotConnectToHost) as NSError === lost)
        let reset = NSError(domain: NSPOSIXErrorDomain, code: 54)
        #expect(NetworkFailure.wrap(reset, as: .timedOut) as NSError === reset)
        #expect(NetworkFailure.wrap(CancellationError(), as: .timedOut) is CancellationError)
        #expect(NetworkFailure.underlying(lost) == nil && NetworkFailure.reason(lost) == nil)
    }

    @Test func reachableAgainGoesOutOncePerQuietSpell() {
        var g = ReachableGate()
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(g.signal(at: t0) == .now)                                  // the tunnel came up
        #expect(g.signal(at: t0.addingTimeInterval(0.5)) == .after(1.5))   // its socket reopened: one more at the end
        #expect(g.signal(at: t0.addingTimeInterval(0.75)) == .none)        // a flap meanwhile: nothing more
        g.fired(at: t0.addingTimeInterval(2))
        #expect(g.signal(at: t0.addingTimeInterval(3)) == .after(1))
        g.fired(at: t0.addingTimeInterval(4))
        #expect(g.signal(at: t0.addingTimeInterval(9)) == .now)            // quiet since: at once
    }

    @Test func eachCodeSpeaksURLSessionsWords() {
        for c in NetworkFailure.Code.allCases {
            let e = NetworkFailure.error(c)
            #expect(e.code == c.rawValue && e.localizedDescription == c.text)
            #expect(ConnectProblem(e, server: nil).kind == .cantConnect)
        }
        #expect(NetworkFailure.error(.notConnectedToInternet).localizedDescription == "The Internet connection appears to be offline.")
    }
}
