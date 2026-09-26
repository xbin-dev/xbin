import Foundation
import Testing
@testable import XbinAgent

/// Prompt attachments (POST …/prompt {text, attachments:[{name, mime, data}]}):
/// the body xbind decodes, its limits checked before any request, the
/// image sniffing that decides what the model sees inline.
@Suite struct PromptAttachmentTests {
    static let png = Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 13])
    static let jpeg = Data([0xFF, 0xD8, 0xFF, 0xE0, 0, 16])

    @Test func bodyAndRoute() async throws {
        let t = FakeTransport { _ in ok(#"{"ok":true,"turn":3}"#) }
        let c = AgentClient(transport: t)
        let files = [
            PromptAttachment(name: "shot.png", mime: "image/png", data: Self.png),
            PromptAttachment(name: "notes \"v2\".txt", data: Data("héllo\n".utf8)),
        ]
        #expect(try await c.prompt("s 1", text: "what's in \"these\"?\n", attachments: files).turn == 3)
        let r = try #require(t.requests.first)
        #expect(r.method == "POST" && r.target == "/api/xbin/term/sessions/s%201/prompt")
        #expect(r.headers["Content-Type"] == "application/json")
        let body = try #require(r.json)
        #expect(body["text"]?.string == "what's in \"these\"?\n")
        let atts = try #require(body["attachments"]?.array)
        #expect(atts.count == 2)
        #expect(atts[0]["name"]?.string == "shot.png" && atts[0]["mime"]?.string == "image/png")
        #expect(atts[0]["data"]?.string.flatMap { Data(base64Encoded: $0) } == Self.png)
        // no declared type: the key is left out (xbind guesses from the name, then the bytes)
        #expect(atts[1]["name"]?.string == "notes \"v2\".txt" && atts[1]["mime"] == nil)
        #expect(atts[1]["data"]?.string.flatMap { Data(base64Encoded: $0) } == Data("héllo\n".utf8))
        // files only: the text may be empty
        _ = try await c.prompt("s", text: "", attachments: [files[0]])
        #expect(t.requests[1].json?["text"]?.string == "")
        // no files: the old body, exactly
        _ = try await c.prompt("s", text: "hi", attachments: [])
        #expect(t.requests[2].json?.compactString == #"{"text":"hi"}"#)
    }

    @Test func limitsRefuseWithoutARequest() async throws {
        let t = FakeTransport { _ in ok(#"{"ok":true,"turn":1}"#) }
        let c = AgentClient(transport: t)
        let one = PromptAttachment(name: "a.bin", data: Data(count: 1))
        await #expect(throws: AgentAPIError(status: 400, message: "11 attachments (at most 10)")) {
            _ = try await c.prompt("s", text: "x", attachments: Array(repeating: one, count: 11))
        }
        let big = PromptAttachment(name: "big.mov", data: Data(count: PromptAttachment.maxFileBytes + 1))
        await #expect(throws: AgentAPIError(status: 413, message: "big.mov is 10.0 MiB — the limit for a file is 10 MiB")) {
            _ = try await c.prompt("s", text: "x", attachments: [big])
        }
        let nine = PromptAttachment(name: "n.bin", data: Data(count: 9 << 20))
        let e = PromptAttachment.check([nine, nine, nine])
        #expect(e == .tooLargeTogether(bytes: 27 << 20) && e?.status == 413)
        #expect(e?.description == "the attachments are over 20 MiB together")
        #expect(t.requests.isEmpty)
        // exactly at the limits is fine
        let ten = PromptAttachment(name: "t.bin", data: Data(count: PromptAttachment.maxFileBytes))
        #expect(PromptAttachment.check([ten, ten]) == nil)
        #expect(PromptAttachment.check(Array(repeating: one, count: 10)) == nil)
        // the largest allowed prompt fits the server's body cap as base64
        let body = PromptAttachment.body(text: "x", [ten, ten])
        #expect(body.count < PromptAttachment.maxBodyBytes)
    }

    @Test func feedSendsFiles() async throws {
        let t = FakeTransport { r in
            if r.path.hasSuffix("/prompt") { return ok(#"{"ok":true,"turn":1}"#) }
            return page([])
        }
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s")
        let r = try await feed.send("look", attachments: [PromptAttachment(name: "a.jpg", data: Self.jpeg)])
        #expect(r.turn == 1)
        let p = try #require(t.requests.first { $0.path.hasSuffix("/prompt") })
        #expect(p.json?["attachments"]?.array?.first?["name"]?.string == "a.jpg")
        // a refusal is the feed's lastError, as the server's would be
        await #expect(throws: AgentAPIError.self) {
            _ = try await feed.send("", attachments: Array(repeating: PromptAttachment(name: "x", data: Data(count: 1)), count: 12))
        }
    }

    @Test func imageSniffing() {
        #expect(PromptAttachment.sniffImage(Self.png) == "image/png")
        #expect(PromptAttachment.sniffImage(Self.jpeg) == "image/jpeg")
        #expect(PromptAttachment.sniffImage(Data("GIF89a....".utf8)) == "image/gif")
        #expect(PromptAttachment.sniffImage(Data("RIFF\u{0}\u{0}\u{0}\u{0}WEBPVP8 ".utf8)) == "image/webp")
        #expect(PromptAttachment.sniffImage(Data("RIFF\u{0}\u{0}\u{0}\u{0}WAVEfmt ".utf8)) == nil)
        #expect(PromptAttachment.sniffImage(Data("%PDF-1.7".utf8)) == nil)
        #expect(PromptAttachment.sniffImage(Data()) == nil)
        // the declared type doesn't count: xbind sniffs the bytes
        let heic = PromptAttachment(name: "IMG.HEIC", mime: "image/jpeg", data: Data("\u{0}\u{0}\u{0}\u{18}ftypheic".utf8))
        #expect(heic.inlineImageType == nil && !heic.fitsInline)
        #expect(PromptAttachment(name: "a.png", data: Self.png).fitsInline)
        var huge = Self.jpeg
        huge.append(Data(count: PromptAttachment.maxInlineImageBytes))
        #expect(!PromptAttachment(name: "a.jpg", data: huge).fitsInline)
    }
}
