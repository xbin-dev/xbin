import Foundation
import XbinCore

/// Markdown blocks per message, for a screen that lexes raw markdown itself
/// (the app's agent screen; D130/E3). A message that grows — an agent's
/// reply as it streams — re-lexes only its tail (``MarkdownLexer/Incremental``)
/// and converts only the tokens that changed; an unchanged text costs a
/// lookup. The least recently used entries go once there are more than
/// `capacity` (a long transcript's far rows are unloaded anyway).
public final class MarkdownMemo {
    private struct Entry {
        var lexer = MarkdownLexer.Incremental()
        /// One per token (nil: a token this version doesn't draw).
        var blocks: [MarkdownBlock?] = []
        var out: [MarkdownBlock] = []
        var used: UInt64 = 0
    }

    private var cache: [String: Entry] = [:]
    private var tick: UInt64 = 0
    public let capacity: Int

    public init(capacity: Int = 400) {
        self.capacity = max(1, capacity)
    }

    /// The blocks of `text`, the message `id`'s markdown now.
    public func blocks(id: String, text: String) -> [MarkdownBlock] {
        tick += 1
        var e = cache.removeValue(forKey: id) ?? Entry()
        if e.used == 0 || e.lexer.text != text {
            let tokens = e.lexer.update(text)
            let k = min(e.lexer.kept, e.blocks.count)
            e.blocks = Array(e.blocks.prefix(k)) + tokens[k...].map(Markdown.block)
            e.out = e.blocks.compactMap { $0 }
        }
        e.used = tick
        cache[id] = e
        if cache.count > capacity { evict() }
        return e.out
    }

    /// Entries held.
    public var count: Int { cache.count }

    /// Drops the least recently used quarter.
    private func evict() {
        let keep = capacity * 3 / 4
        guard cache.count > keep else { return }
        for (k, _) in cache.sorted(by: { $0.value.used < $1.value.used }).prefix(cache.count - keep) {
            cache[k] = nil
        }
    }
}
