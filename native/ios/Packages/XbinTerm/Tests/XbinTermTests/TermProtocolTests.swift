// TermProtocolTests.swift — the /ws/term codec against the frames
// internal/term/attach.go writes and docs/protocol.md §/ws/term documents.

import Foundation
import Testing
@testable import XbinTerm

@Suite("/ws/term codec")
struct TermProtocolTests {
    @Test("the session frame, every field (as attach.go marshals it)")
    func sessionFrame() {
        // attach.go: json.Marshal of a map — keys sorted, scopes as []Scope
        let text = #"{"baseOutdated":true,"echoAck":true,"id":"a1b2","label":"org network (devs-net)","net":"org","netNote":"host networking is admin-only — using the org network","op":"session","scopes":[{"id":"org","label":"org network (devs-net)","desc":"the org's network sets"},{"id":"none","label":"no network"}],"vm":true}"#
        let f = TermCodec.decode(.text(text))
        #expect(f == .session(TermSessionInfo(
            id: "a1b2", net: "org", label: "org network (devs-net)",
            scopes: [TermScope(id: "org", label: "org network (devs-net)", desc: "the org's network sets"),
                     TermScope(id: "none", label: "no network", desc: "")],
            netNote: "host networking is admin-only — using the org network",
            baseOutdated: true, vm: true, echoAck: true)))
    }

    @Test("an older xbind's session frame: scopes null, no echoAck, no vm")
    func sessionFrameOld() {
        let f = TermCodec.decode(.text(#"{"op":"session","id":"x","net":"internet","baseOutdated":false,"label":"","scopes":null,"netNote":""}"#))
        #expect(f == .session(TermSessionInfo(id: "x", net: "internet")))
        if case .session(let i) = f { #expect(i.echoAck == false); #expect(i.vm == false); #expect(i.scopes.isEmpty) }
    }

    @Test("lenient fields: a wrong type reads as absent, a scope without an id is skipped")
    func lenient() {
        let f = TermCodec.decode(.text(#"{"op":"session","id":"x","vm":"yes","echoAck":1,"scopes":[{"label":"no id"},{"id":"none"}],"extra":{"a":[1,2]}}"#))
        #expect(f == .session(TermSessionInfo(id: "x", scopes: [TermScope(id: "none", label: "none")])))
    }

    @Test("ack, pong, exit")
    func controlFrames() {
        #expect(TermCodec.decode(.text(#"{"op":"ack","n":42}"#)) == .ack(42))
        #expect(TermCodec.decode(.text(#"{"n":18446744073709551615,"op":"ack"}"#)) == .ack(UInt64.max))
        #expect(TermCodec.decode(.text(#"{"op":"ack","n":7.0}"#)) == .ack(7))
        #expect(TermCodec.decode(.text(#"{"op":"ack"}"#)) == .ignored(op: "ack"), "an ack without a number is dropped, as bx-terminal does")
        #expect(TermCodec.decode(.text(#"{"op":"ack","n":"3"}"#)) == .ignored(op: "ack"))
        #expect(TermCodec.decode(.text(#"{"op":"pong","t":1234.5}"#)) == .pong(t: 1234.5))
        #expect(TermCodec.decode(.text(#"{"op":"pong","t":{"any":"json"}}"#)) == .pong(t: nil))
        #expect(TermCodec.decode(.text(#"{"op":"pong"}"#)) == .pong(t: nil))
        #expect(TermCodec.decode(.text(#"{"op":"exit"}"#)) == .exit)
    }

    @Test("unknown ops and malformed frames are ignored, never thrown")
    func ignored() {
        #expect(TermCodec.decode(.text(#"{"op":"future","x":1}"#)) == .ignored(op: "future"))
        #expect(TermCodec.decode(.text("not json")) == .ignored(op: nil))
        #expect(TermCodec.decode(.text("[1,2]")) == .ignored(op: nil))
        #expect(TermCodec.decode(.text("null")) == .ignored(op: nil))
        #expect(TermCodec.decode(.text(#"{"x":1}"#)) == .ignored(op: nil))
        #expect(TermCodec.decode(.text("")) == .ignored(op: nil))
    }

    @Test("binary frames are PTY output, byte for byte")
    func binary() {
        let b: [UInt8] = [0x1b, 0x5b, 0x48, 0xff, 0x00, 0xc5, 0x82]
        #expect(TermCodec.decode(.binary(b)) == .output(b))
    }

    @Test("client frames: input is binary; resize and ping are the documented JSON")
    func encode() throws {
        #expect(TermCodec.encode(.input([0x61, 0x0d])) == .binary([0x61, 0x0d]))
        #expect(TermCodec.encode(.resize(cols: 120, rows: 32)) == .text(#"{"op":"resize","cols":120,"rows":32}"#))
        #expect(TermCodec.encode(.ping(t: 1234)) == .text(#"{"op":"ping","t":1234}"#))
        #expect(TermCodec.encode(.ping(t: 1234.5)) == .text(#"{"op":"ping","t":1234.5}"#))
        #expect(TermCodec.encode(.ping(t: .nan)) == .text(#"{"op":"ping","t":0}"#))
        // what the server reads (attach.go `control`): valid JSON with op/cols/rows/t
        for f: TermClientFrame in [.resize(cols: 80, rows: 24), .ping(t: 0.001), .ping(t: 1e20), .ping(t: -3.25)] {
            guard case .text(let s) = TermCodec.encode(f) else { Issue.record("not text"); continue }
            let o = try #require(JSONSerialization.jsonObject(with: Data(s.utf8)) as? [String: Any])
            #expect(o["op"] as? String == (f == .resize(cols: 80, rows: 24) ? "resize" : "ping"))
        }
    }

    @Test("a pong echoes the ping's t verbatim: the round trip decodes to the same number")
    func pingPongRoundTrip() {
        for t in [0.0, 1, 1234.5678, 98765432.125, 1e-3] {
            guard case .text(let ping) = TermCodec.encode(.ping(t: t)) else { Issue.record("not text"); continue }
            // attach.go answers {"op":"pong","t":<the ping's t, raw>}
            let raw = ping.replacingOccurrences(of: #"{"op":"ping","t":"#, with: "").dropLast()
            #expect(TermCodec.decode(.text(#"{"op":"pong","t":\#(raw)}"#)) == .pong(t: t))
        }
    }

    @Test("socket paths: the query bx-terminal sends, encodeURIComponent-escaped")
    func paths() {
        #expect(TermPaths.socket(.new(TermNewSession(cwd: "apps/x"))) == "/ws/term?cwd=apps%2Fx&gpu=none&api=1")
        #expect(TermPaths.socket(.new(TermNewSession(cwd: "a b/ł", net: "set:devs-net", gpu: "0", api: false, vm: true)))
                == "/ws/term?cwd=a%20b%2F%C5%82&net=set%3Adevs-net&gpu=0&api=0&vm=1")
        #expect(TermPaths.socket(.new(TermNewSession(cwd: "x", net: ""))) == "/ws/term?cwd=x&gpu=none&api=1", "an empty net is the tile's default")
        #expect(TermPaths.socket(.reattach(id: "ab+c/=")) == "/ws/term?session=ab%2Bc%2F%3D")
        #expect(TermPaths.kill(session: "s1") == "/ws/term?session=s1")
        #expect(TermPaths.env(cwd: "apps/x") == "/ws/term/env?cwd=apps%2Fx")
        #expect(TermPaths.encodeURIComponent("-_.!~*'()az09") == "-_.!~*'()az09")
        #expect(TermPaths.encodeURIComponent("?&=#%+ ") == "%3F%26%3D%23%25%2B%20")
    }

    @Test("GET /ws/term/env: the layer's state and VM availability")
    func env() throws {
        let d = JSONDecoder()
        #expect(try d.decode(TermEnvState.self, from: Data(#"{"baseOutdated":false,"exists":false}"#.utf8)) == TermEnvState())
        let s = try d.decode(TermEnvState.self, from: Data(#"{"exists":true,"baseOutdated":true,"vm":{"available":true,"emulated":true,"note":"no KVM","memMiB":2048,"vcpus":2}}"#.utf8))
        #expect(s == TermEnvState(exists: true, baseOutdated: true, vm: TermVMStatus(available: true, emulated: true, note: "no KVM", memMiB: 2048, vcpus: 2)))
        let n = try d.decode(TermEnvState.self, from: Data(#"{"exists":true,"baseOutdated":false,"vm":{"available":false,"reason":"VM sandboxes need isolation (xbind --isolate)"}}"#.utf8))
        #expect(n.vm?.available == false)
        #expect(n.vm?.reason == "VM sandboxes need isolation (xbind --isolate)")
    }
}
