import Foundation
import Testing
@testable import XbinCore

// The app tunnels' diagnostics and probe (D211, #42): the ring buffer that
// Settings → Connection shows and "Copy diagnostics" hands over — bounded,
// in every build, and never holding a key, ticket or token — and the probe
// request that proves a tunnel before it is called up.

@Suite struct TunnelLogTests {
    @Test func keepsTheNewestWithinItsCapacity() {
        let log = TunnelLog(capacity: 3)
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        for i in 0..<5 { log.add("event \(i)", at: t0.addingTimeInterval(Double(i))) }
        #expect(log.entries.map(\.message) == ["event 2", "event 3", "event 4"])   // oldest first
        #expect(log.droppedCount == 2)
        log.add("event 5", at: t0)
        #expect(log.entries.map(\.message) == ["event 3", "event 4", "event 5"])
        log.clear()
        #expect(log.entries.isEmpty && log.droppedCount == 0)
    }

    @Test func linesNameTheWorkspaceShortAndInUTC() {
        let log = TunnelLog()
        let at = Date(timeIntervalSince1970: 1_800_000_000.25)     // 2027-01-15 08:00:00.250 UTC
        log.add("app: background", at: at)
        log.add("tunnel up", workspace: "1A2B3C4D-0000-4000-8000-00000000AAAA", at: at)
        log.add("other workspace", workspace: "99999999-0000-4000-8000-00000000BBBB", at: at)
        #expect(log.lines() == [
            "2027-01-15 08:00:00.250Z app: background",
            "2027-01-15 08:00:00.250Z [1A2B3C4D] tunnel up",
            "2027-01-15 08:00:00.250Z [99999999] other workspace",
        ])
        // One workspace's view keeps the app's own lines.
        #expect(log.lines(workspace: "1A2B3C4D-0000-4000-8000-00000000AAAA").count == 2)
        let report = log.report(header: ["xbin 1.2 (3)", "status: Tunnel · relay"], workspace: "1A2B3C4D-0000-4000-8000-00000000AAAA")
        #expect(report.hasPrefix("xbin 1.2 (3)\nstatus: Tunnel · relay\n\n2027-01-15 08:00:00.250Z app: background\n"))
        #expect(!report.contains("other workspace"))
    }

    /// Nothing that looks like a key, a ticket, a psk, a token or a whole
    /// EndpointId survives, in the entries or the report's header; the
    /// words, numbers, paths and addresses of a line stay readable.
    @Test func scrubsSecrets() {
        let ticket = "endpoint" + base32(Data([0]) + Data(repeating: 0xAB, count: 32) + Data("https://relay.example.com/".utf8))
        let key = Data(repeating: 0x5A, count: 32).base64EncodedString()
            .replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
        let endpoint = String(repeating: "ab12", count: 16)
        let token = "s3cretRelayTok3nThatIsLong"
        for secret in [ticket, key, endpoint, token] {
            let log = TunnelLog()
            log.add("connect failed: relay refused token \(secret) (code 403)")
            let line = log.lines()[0]
            #expect(!line.contains(secret))
            #expect(line.contains("connect failed: relay refused token \(secret.prefix(6))… (code 403)"))
            #expect(!log.report(header: ["header \(secret)"]).contains(secret))
        }
        #expect(TunnelLog.redact("GET /api/xbin/app-tunnel/client → 502 after 1534 ms on 127.0.0.1:61234")
            == "GET /api/xbin/app-tunnel/client → 502 after 1534 ms on 127.0.0.1:61234")
        #expect(TunnelLog.redact("tunnel/1 failed: the tunnel's handshake took longer than 15 s")
            == "tunnel/1 failed: the tunnel's handshake took longer than 15 s")
        #expect(TunnelLog.shortID(endpoint) == "ab12ab12…")
    }

    @Test func isSafeFromManyThreads() async {
        let log = TunnelLog(capacity: 64)
        await withTaskGroup(of: Void.self) { g in
            for t in 0..<8 {
                g.addTask { for i in 0..<200 { log.add("t\(t) \(i)") } }
            }
        }
        #expect(log.entries.count == 64)
        #expect(log.droppedCount == 8 * 200 - 64)
    }
}

@Suite struct TunnelProbeTests {
    @Test func requestIsAPlainGETOnTheForwardersHost() {
        let r = String(decoding: TunnelProbe.request(host: "127.0.0.1:61234"), as: UTF8.self)
        #expect(r == "GET /api/xbin/login/methods HTTP/1.1\r\nHost: 127.0.0.1:61234\r\nAccept: application/json\r\nConnection: close\r\n\r\n")
    }

    @Test func readsTheStatusLine() {
        #expect(TunnelProbe.status(Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n".utf8)) == 200)
        #expect(TunnelProbe.status(Data("HTTP/1.1 403 Forbidden\r\n".utf8)) == 403)   // xbind answered: the tunnel works
        #expect(TunnelProbe.status(Data("HTTP/1.0 502\r\n".utf8)) == 502)
        #expect(TunnelProbe.status(Data("HTTP/1.1 200 OK".utf8)) == nil)             // not the whole line yet
        #expect(TunnelProbe.status(Data("SSH-2.0-x\r\n".utf8)) == nil)
        #expect(TunnelProbe.status(Data("HTTP/1.1 2000 Huh\r\n".utf8)) == nil)
        #expect(TunnelProbe.status(Data()) == nil)
    }
}
