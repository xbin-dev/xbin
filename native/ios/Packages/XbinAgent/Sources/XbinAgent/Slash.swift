import Foundation

// Slash-command completion for the composer (D77) — the port of
// web/agent-slash.js. The agent advertises its commands (`status.commands`);
// typing "/" at the start of the draft offers them; a command goes to the
// agent as plain prompt text ("/name args"), ACP's own model.

public enum SlashCompletion {
    /// The command name being typed ("/rev" → "rev", "/" → ""), or nil when
    /// the draft is not a bare slash command in progress.
    public static func query(_ draft: String) -> String? {
        let u = draft.unicodeScalars
        guard u.first == "/" else { return nil }
        let rest = u.dropFirst()
        guard rest.allSatisfy(isNameScalar) else { return nil }
        return String(String.UnicodeScalarView(rest)).lowercased()
    }

    /// Prefix matches first (in the agent's order), then names or
    /// descriptions containing the query; at most `max`.
    public static func matches(_ commands: [SlashCommand], query: String, max: Int = 8) -> [SlashCommand] {
        let list = commands.filter { !$0.name.isEmpty }
        let q = query.lowercased()
        let pre = list.filter { $0.name.lowercased().hasPrefix(q) }
        let preNames = Set(pre.map(\.name))
        let sub = q.isEmpty ? [] : list.filter {
            !preNames.contains($0.name) && ($0.name.lowercased().contains(q) || $0.description.lowercased().contains(q))
        }
        return Array((pre + sub).prefix(max))
    }

    /// The menu for a draft: [] when closed.
    public static func menu(_ commands: [SlashCommand], draft: String, max: Int = 8) -> [SlashCommand] {
        guard let q = query(draft) else { return [] }
        return matches(commands, query: q, max: max)
    }

    /// What to type after a completed "/name " whose command takes input
    /// (its hint), while nothing is typed yet.
    public static func hint(_ commands: [SlashCommand], draft: String) -> SlashCommand? {
        let u = Array(draft.unicodeScalars)
        guard u.first == "/" else { return nil }
        var i = 1
        while i < u.count, isNameScalar(u[i]) { i += 1 }
        guard i > 1, i < u.count else { return nil }
        let name = String(String.UnicodeScalarView(u[1..<i]))
        guard u[i...].allSatisfy({ $0.properties.isWhitespace }) else { return nil }
        guard let c = commands.first(where: { $0.name == name }), !c.hint.isEmpty else { return nil }
        return c
    }

    /// The draft after picking a command from the menu.
    public static func complete(_ c: SlashCommand) -> String { "/" + c.name + " " }

    /// JavaScript's [\w:.-] (ASCII word characters, ':', '.', '-').
    static func isNameScalar(_ s: Unicode.Scalar) -> Bool {
        switch s {
        case "a"..."z", "A"..."Z", "0"..."9", "_", ":", ".", "-": return true
        default: return false
        }
    }
}
