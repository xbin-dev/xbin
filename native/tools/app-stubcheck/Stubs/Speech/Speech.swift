@_exported import Foundation
import AVFoundation

// What the composer's dictation uses of Speech (D241; App/Shell/AgentDictation).

public enum SFSpeechRecognizerAuthorizationStatus: Int, Sendable { case notDetermined, denied, restricted, authorized }

open class SFSpeechRecognitionRequest: NSObject {
    open var requiresOnDeviceRecognition = false
    open var shouldReportPartialResults = true
}

open class SFSpeechAudioBufferRecognitionRequest: SFSpeechRecognitionRequest {
    public override init() {}
    open func append(_ audioPCMBuffer: AVAudioPCMBuffer) {}
    open func endAudio() {}
}

open class SFTranscription: NSObject {
    open var formattedString: String { "" }
}

open class SFSpeechRecognitionResult: NSObject {
    open var bestTranscription: SFTranscription { SFTranscription() }
    open var isFinal: Bool { false }
}

open class SFSpeechRecognitionTask: NSObject {
    open func cancel() {}
}

open class SFSpeechRecognizer: NSObject {
    public init?(locale: Locale) {}
    open var isAvailable: Bool { true }
    open var supportsOnDeviceRecognition: Bool { true }
    open class func authorizationStatus() -> SFSpeechRecognizerAuthorizationStatus { .notDetermined }
    open class func requestAuthorization(_ handler: @escaping @Sendable (SFSpeechRecognizerAuthorizationStatus) -> Void) {}
    open func recognitionTask(with request: SFSpeechRecognitionRequest,
                              resultHandler: @escaping (SFSpeechRecognitionResult?, (any Error)?) -> Void) -> SFSpeechRecognitionTask {
        SFSpeechRecognitionTask()
    }
}
