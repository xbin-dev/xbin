import AVFoundation
import Foundation
import Speech
import XbinRenderer

/// The workspace agent's composer mic (D241; plans/wsagent/ios-stage2.md
/// §8.1): the system's speech recognition, on this device only
/// (`requiresOnDeviceRecognition` — no audio leaves the phone, no
/// third-party speech), into the draft. The renderer draws it
/// (XbinDictationModel: the mic, the level, the time, the words so far);
/// nothing is sent by voice — ✓ puts the words in the composer to edit or
/// send. Where on-device recognition isn't there, or the person refused the
/// permissions, the mic is hidden and the keyboard's own dictation stays.
/// Given only to the person's agent's view (NativeTileRuntime.services): no
/// tile gets a microphone.
@MainActor
final class AgentDictation: XbinDictationModel {
    static let shared = AgentDictation()

    private let recognizer = SFSpeechRecognizer(locale: Locale.current)
    private var engine: AVAudioEngine?
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?

    override init() {
        super.init()
        refreshAvailable()
    }

    func refreshAvailable() {
        available = recognizer?.supportsOnDeviceRecognition == true
            && SFSpeechRecognizer.authorizationStatus() != .denied && SFSpeechRecognizer.authorizationStatus() != .restricted
            && AVAudioApplication.shared.recordPermission != .denied
    }

    override func start() async {
        guard !listening else { return }
        problem = nil
        let speech = await withCheckedContinuation { (c: CheckedContinuation<SFSpeechRecognizerAuthorizationStatus, Never>) in
            SFSpeechRecognizer.requestAuthorization { c.resume(returning: $0) }
        }
        guard speech == .authorized else {
            problem = "Dictation needs speech recognition, which is off for xbin in Settings."
            refreshAvailable()
            return
        }
        guard await AVAudioApplication.requestRecordPermission() else {
            problem = "Dictation needs the microphone, which is off for xbin in Settings."
            refreshAvailable()
            return
        }
        guard let recognizer, recognizer.isAvailable, recognizer.supportsOnDeviceRecognition else {
            problem = "Dictation on this device isn't available now. The keyboard's own dictation still is."
            return
        }
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.record, mode: .measurement, options: .duckOthers)
            try session.setActive(true, options: .notifyOthersOnDeactivation)
            let req = SFSpeechAudioBufferRecognitionRequest()
            req.requiresOnDeviceRecognition = true
            req.shouldReportPartialResults = true
            let engine = AVAudioEngine()
            let input = engine.inputNode
            let format = input.outputFormat(forBus: 0)
            input.installTap(onBus: 0, bufferSize: 1024, format: format, block: Self.tap(feeding: req))
            engine.prepare()
            try engine.start()
            self.engine = engine
            request = req
            transcript = ""
            startedAt = Date()
            listening = true
            task = recognizer.recognitionTask(with: req, resultHandler: Self.results())
        } catch {
            problem = "Dictation couldn't start: the microphone is busy."
            teardown()
        }
    }

    override func finish() -> String {
        let t = transcript
        teardown()
        return t
    }

    override func cancel() {
        teardown()
        transcript = ""
    }

    private func teardown() {
        engine?.stop()
        engine?.inputNode.removeTap(onBus: 0)
        request?.endAudio()
        task?.cancel()
        engine = nil
        request = nil
        task = nil
        listening = false
        level = 0
        startedAt = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    // The audio thread's and the recognizer's callbacks, made where nothing
    // is isolated: they run off the main thread, and hop to it.

    nonisolated static func tap(feeding req: SFSpeechAudioBufferRecognitionRequest) -> AVAudioNodeTapBlock {
        nonisolated(unsafe) let feed = req
        return { buffer, _ in
            feed.append(buffer)
            let level = level(of: buffer)
            Task { @MainActor in AgentDictation.shared.level = level }
        }
    }

    nonisolated static func results() -> (SFSpeechRecognitionResult?, (any Error)?) -> Void {
        return { result, error in
            let text = result?.bestTranscription.formattedString
            let failed = error != nil
            Task { @MainActor in
                let d = AgentDictation.shared
                if let text, d.listening { d.transcript = text }
                if failed, d.listening, d.transcript.isEmpty { d.problem = "Nothing was heard." }
            }
        }
    }

    /// The buffer's loudness, 0…1, for the meter.
    nonisolated static func level(of buffer: AVAudioPCMBuffer) -> Double {
        guard let ch = buffer.floatChannelData?[0], buffer.frameLength > 0 else { return 0 }
        var sum: Float = 0
        for i in 0..<Int(buffer.frameLength) { sum += ch[i] * ch[i] }
        let rms = (sum / Float(buffer.frameLength)).squareRoot()
        return min(1, max(0, Double(rms) * 8))
    }
}
