import Foundation
import Testing
@testable import XbinCore

@Suite struct WorkspaceRecordTests {
    func sample() throws -> WorkspaceList {
        WorkspaceList(workspaces: [
            WorkspaceRecord(id: "6F1C8E2A-3B4D-4E5F-8A9B-0C1D2E3F4A5B", server: try ServerOrigin(string: "https://xbin.example.com"),
                            user: WorkspaceUser(id: "alice", name: "Alice"), deviceId: "dev_7Hq2xK",
                            deviceOrigin: "https://xbin.example.com", deviceName: "Alice's iPhone",
                            branding: BrandingCache(title: "Acme", icon: "data:image/png;base64,iVBORw0KGgo=", fetchedAt: Date(unixMillis: 1_790_000_000_123)),
                            addedAt: Date(unixMillis: 1_780_000_000_000), lastUsedAt: Date(unixMillis: 1_790_000_000_000)),
            WorkspaceRecord(id: "b0b", server: try ServerOrigin(string: "http://10.0.0.5:8080"), user: WorkspaceUser(id: "bob"),
                            addedAt: Date(unixMillis: 1_785_000_000_000)),
        ], selected: "6f1c8e2a-3b4d-4e5f-8a9b-0c1d2e3f4a5b")
    }

    /// The persistence format, pinned: changing it needs a migration.
    @Test func pinnedFormat() throws {
        let data = try sample().encoded()
        let expected = """
        {
          "selected" : "6f1c8e2a-3b4d-4e5f-8a9b-0c1d2e3f4a5b",
          "v" : 1,
          "workspaces" : [
            {
              "addedAt" : 1780000000000,
              "branding" : {
                "fetchedAt" : 1790000000123,
                "icon" : "data:image/png;base64,iVBORw0KGgo=",
                "title" : "Acme"
              },
              "deviceId" : "dev_7Hq2xK",
              "deviceName" : "Alice's iPhone",
              "deviceOrigin" : "https://xbin.example.com",
              "id" : "6f1c8e2a-3b4d-4e5f-8a9b-0c1d2e3f4a5b",
              "lastUsedAt" : 1790000000000,
              "server" : "https://xbin.example.com",
              "user" : {
                "id" : "alice",
                "name" : "Alice"
              }
            },
            {
              "addedAt" : 1785000000000,
              "id" : "b0b",
              "server" : "http://10.0.0.5:8080",
              "user" : {
                "id" : "bob"
              }
            }
          ]
        }
        """
        // Compare as JSON values (whitespace style differs across Foundation versions).
        #expect(try JSONValue(parsing: data) == JSONValue(parsing: expected))
        #expect(try WorkspaceList.decode(data) == sample())
    }

    @Test func tolerantReading() throws {
        let old = #"{"workspaces":[{"id":"W1","server":"HTTPS://H","user":{"id":"u"},"future":{"x":1}}],"alsoFuture":true}"#
        let list = try WorkspaceList.decode(Data(old.utf8))
        let w = try #require(list.workspaces.first)
        #expect(w.id == "w1" && w.server.origin == "https://h" && w.user == WorkspaceUser(id: "u"))
        #expect(w.deviceId == nil && w.deviceOrigin == nil && w.branding == nil && w.lastUsedAt == nil && w.addedAt == Date(timeIntervalSince1970: 0))
        #expect(list.selected == nil)
        #expect(try WorkspaceList.decode(Data("{}".utf8)).workspaces.isEmpty)
    }

    @Test func newerVersionsAreRefused() {
        #expect(throws: WorkspaceList.NewerVersion(version: 2)) { try WorkspaceList.decode(Data(#"{"v":2,"workspaces":[]}"#.utf8)) }
    }

    @Test func badRecordsAreKeptAside() throws {
        let text = #"{"workspaces":[{"id":"x","server":"ftp://h","user":{"id":"u"}},{"id":"ok","server":"https://h","user":{"id":"u"},"addedAt":5},{"id":"y","server":"https://h"}]}"#
        let list = try WorkspaceList.decode(Data(text.utf8))
        #expect(list.workspaces.map(\.id) == ["ok"])
        #expect(list.unreadable.count == 2 && list.unreadable[0]["server"] == "ftp://h")
        // Written back verbatim, after the readable ones.
        let again = try JSONValue(parsing: list.encoded())
        #expect(again["workspaces"]?.arrayValue?.count == 3)
        #expect(again["workspaces"]?[1] == list.unreadable[0] && again["workspaces"]?[2] == list.unreadable[1])
        #expect(throws: (any Error).self) { try WorkspaceList.decode(Data("[]".utf8)) }
    }

    @Test func helpers() throws {
        let list = try sample()
        #expect(list[id: "b0b"]?.user.id == "bob")
        #expect(list.workspaces[0].displayTitle == "Acme")
        #expect(list.workspaces[1].displayTitle == "10.0.0.5")
        let b = BrandingCache(response: ["title": "Lab", "icon": "", "hasIcon": false])
        #expect(b.title == "Lab" && b.icon == nil)
        #expect(WorkspaceRecord(server: try ServerOrigin(string: "https://h"), user: .init(id: "u")).id.count == 36)
        #expect(Date(unixMillis: 1_790_000_000_123).unixMillis == 1_790_000_000_123)
    }
}
