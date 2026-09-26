import Foundation
import Testing
@testable import XbinCore

// MARK: - Deterministic randomness

/// SplitMix64 — seeded, so a failing randomized case can be replayed.
struct SeededRNG: RandomNumberGenerator {
    var state: UInt64
    init(seed: UInt64) { state = seed }
    mutating func next() -> UInt64 {
        state &+= 0x9E37_79B9_7F4A_7C15
        var z = state
        z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
        z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
        return z ^ (z >> 31)
    }
}

enum Gen {
    static let alphabet: [String] = ["a", "b", "Z", "0", " ", "\"", "\\", "/", "\n", "\t", "\u{01}", "\u{1F}",
                                     "\u{7F}", "é", "ß", "中", "😀", "\u{2028}", "\u{2029}", "<", "&", "'", "`", "$", "{"]

    static func string(_ r: inout SeededRNG, max: Int = 8) -> String {
        (0..<Int.random(in: 0...max, using: &r)).map { _ in alphabet.randomElement(using: &r)! }.joined()
    }

    static func number(_ r: inout SeededRNG) -> JSONValue {
        switch Int.random(in: 0..<6, using: &r) {
        case 0: return .int(Int64.random(in: -1000...1000, using: &r))
        case 1: return .int([Int64.max, Int64.min, 0, -1, 1_790_000_000_000].randomElement(using: &r)!)
        case 2: return .double(Double.random(in: -1e6...1e6, using: &r))
        case 3: return .double([0.1, 1.0, -0.0, 1e16, .leastNonzeroMagnitude, .greatestFiniteMagnitude, 12.28, 1e-7].randomElement(using: &r)!)
        case 4: return .double(Double(Int.random(in: -100...100, using: &r)))
        default: return .double(Double.random(in: 0...1, using: &r) * pow(10, Double(Int.random(in: -30...30, using: &r))))
        }
    }

    static func value(_ r: inout SeededRNG, depth: Int = 0) -> JSONValue {
        let leafOnly = depth >= 3
        switch Int.random(in: 0..<(leafOnly ? 5 : 7), using: &r) {
        case 0: return .null
        case 1: return .bool(Bool.random(using: &r))
        case 2: return number(&r)
        case 3, 4: return .string(string(&r))
        case 5: return .array((0..<Int.random(in: 0...4, using: &r)).map { _ in value(&r, depth: depth + 1) })
        default:
            var o: [String: JSONValue] = [:]
            for _ in 0..<Int.random(in: 0...4, using: &r) { o[string(&r, max: 4)] = value(&r, depth: depth + 1) }
            return .object(o)
        }
    }
}

