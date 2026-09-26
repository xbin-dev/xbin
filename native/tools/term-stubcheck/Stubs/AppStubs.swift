// The app types the terminal uses, as declared in App/Model (signatures only).
import Foundation
import Observation
import XbinCore
import XbinTerm

@MainActor @Observable
final class WorkspaceModel {
    let auth: WorkspaceAuth
    var sessions: [TermDirectoryEntry] = []
    var origin: ServerOrigin { fatalError() }
    init() { fatalError() }
    func refreshSessions() async {}
    func describe(_ error: any Error) -> String { "" }
}

enum AppSettings {
    static var predictMode: String { get { "auto" } set {} }
    static var terminalFontSize: Double { get { 13 } set {} }
}

@MainActor
final class WebSocketTransport: TermTransport {
    init(origin: ServerOrigin, path: String, token: String, events: @escaping @MainActor (TermTransportEvent) -> Void) {}
    func start() {}
    func send(_ m: TermWireMessage) {}
    func close() {}
}
