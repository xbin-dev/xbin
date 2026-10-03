import Foundation
import Testing
@testable import XbinCore

// The person's theme (D185): the shell bucket's `theme`, as xbind reads it
// (internal/server/appearance.go): exactly "light" or "dark", anything else
// the system's.
@Suite struct AppearancePrefTests {
    @Test func storedValues() throws {
        #expect(AppearancePref(json: try JSONValue(parsing: #""light""#)) == .light)
        #expect(AppearancePref(json: try JSONValue(parsing: #""dark""#)) == .dark)
        #expect(AppearancePref(json: try JSONValue(parsing: #""system""#)) == .system)
        // Never chose (404), or a value the theme doesn't define.
        #expect(AppearancePref(json: nil) == .system)
        #expect(AppearancePref(json: try JSONValue(parsing: #""Dark""#)) == .system)
        #expect(AppearancePref(json: try JSONValue(parsing: "true")) == .system)
        #expect(AppearancePref(json: try JSONValue(parsing: #"{"theme":"dark"}"#)) == .system)
        #expect(AppearancePref.path == "/api/xbin/prefs/theme")
    }

    /// A `prefs` event for the shell bucket's theme, from any writer; not
    /// another key, not a tile's own bucket.
    @Test func events() {
        #expect(AppearancePref.concerns(component: "root", key: "theme"))
        #expect(AppearancePref.concerns(component: "", key: "theme"))
        #expect(!AppearancePref.concerns(component: "root", key: "density"))
        #expect(!AppearancePref.concerns(component: "apps/x", key: "theme"))
        #expect(!LayoutPref.concernsHome(component: "root", key: "theme", writer: "", me: "me"))
    }
}
