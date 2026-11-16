@_exported import Foundation

// What the composer's dictation uses of AVFAudio (D241; App/Shell/AgentDictation).

open class AVAudioFormat: NSObject {}
open class AVAudioTime: NSObject {}
open class AVAudioPCMBuffer: NSObject {
    open var frameLength: UInt32 { 0 }
    open var floatChannelData: UnsafePointer<UnsafeMutablePointer<Float>>? { nil }
}
public typealias AVAudioNodeTapBlock = (AVAudioPCMBuffer, AVAudioTime) -> Void
open class AVAudioNode: NSObject {
    open func installTap(onBus bus: Int, bufferSize: UInt32, format: AVAudioFormat?, block tapBlock: @escaping AVAudioNodeTapBlock) {}
    open func removeTap(onBus bus: Int) {}
    open func outputFormat(forBus bus: Int) -> AVAudioFormat { AVAudioFormat() }
}
open class AVAudioInputNode: AVAudioNode {}
open class AVAudioEngine: NSObject {
    public override init() {}
    open var inputNode: AVAudioInputNode { AVAudioInputNode() }
    open func prepare() {}
    open func start() throws {}
    open func stop() {}
}
open class AVAudioSession: NSObject {
    public struct Category: Sendable { public static let record = Category() }
    public struct Mode: Sendable { public static let measurement = Mode() }
    public struct CategoryOptions: OptionSet, Sendable {
        public let rawValue: UInt
        public init(rawValue: UInt) { self.rawValue = rawValue }
        public static let duckOthers = CategoryOptions(rawValue: 2)
    }
    public struct SetActiveOptions: OptionSet, Sendable {
        public let rawValue: UInt
        public init(rawValue: UInt) { self.rawValue = rawValue }
        public static let notifyOthersOnDeactivation = SetActiveOptions(rawValue: 1)
    }
    open class func sharedInstance() -> AVAudioSession { AVAudioSession() }
    open func setCategory(_ category: Category, mode: Mode, options: CategoryOptions = []) throws {}
    open func setActive(_ active: Bool, options: SetActiveOptions = []) throws {}
}
open class AVAudioApplication: NSObject {
    public enum recordPermission: Int, Sendable { case undetermined, denied, granted }
    public typealias RecordPermission = recordPermission
    open class var shared: AVAudioApplication { AVAudioApplication() }
    open var recordPermission: RecordPermission { .undetermined }
    open class func requestRecordPermission() async -> Bool { false }
}
