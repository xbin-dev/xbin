import Testing
@testable import XbinCore

/// Which addresses the sign-in page warns about: plain http that may
/// travel in the clear (ServerOrigin.isEncryptedInTransit).
@Suite struct ServerOriginTransportTests {
    private func enc(_ s: String) throws -> Bool { try ServerOrigin(userInput: s).isEncryptedInTransit }

    @Test func httpsAndLoopbackAndTailnetAreEncrypted() throws {
        #expect(try enc("https://xbin.example.com"))
        #expect(try enc("xbin.example.com")) // no scheme = https
        #expect(try enc("http://127.0.0.1:9874"))
        #expect(try enc("http://localhost:8642"))
        #expect(try enc("http://[::1]:8642"))
        #expect(try enc("http://100.113.235.121:9874")) // Tailscale's 100.64.0.0/10
        #expect(try enc("http://100.64.0.1"))
        #expect(try enc("http://100.127.255.254"))
        #expect(try enc("http://biryani.tailed12e.ts.net:9874"))
        #expect(try enc("http://[fd7a:115c:a1e0::7301:eb79]:9874"))
    }

    @Test func otherHTTPIsNot() throws {
        #expect(try !enc("http://192.168.1.20:8642"))
        #expect(try !enc("http://10.0.0.5"))
        #expect(try !enc("http://biryani:9874")) // an unqualified name may be anything
        #expect(try !enc("http://xbin.example.com"))
        #expect(try !enc("http://100.63.0.1")) // just outside 100.64.0.0/10
        #expect(try !enc("http://100.128.0.1"))
        #expect(try !enc("http://100.64.example.com"))
    }
}
