import Foundation
import Observation
import PhotosUI
import SwiftUI
import UIKit
import UniformTypeIdentifiers
import XbinCore
import XbinRenderer

// Attachments (plans/native.md §8.4): the composer's attach button opens
// the APP's pickers — Photos, the camera, Files — and the app handles the
// bytes itself: for a native tile it uploads them to the tile's own
// `upload.path` with the tile's frame token (TileAttachFlow); for the ACP
// agent they ride the prompt (AgentScreen). No device API reaches tile code.

/// A file the user picked.
struct PickedFile: Sendable, Equatable {
    var name: String
    /// Its media type ("" when unknown).
    var mime: String
    var data: Data
}

/// The pickers behind an attach button: a source dialog, then Photos, the
/// camera or Files; `pick` returns what the user chose ([] when cancelled).
/// Present them with ``AttachPickers``.
@MainActor
@Observable
final class AttachPicker {
    enum Source: Equatable { case photos, camera, files }

    var choosing = false
    var showPhotos = false
    var showCamera = false
    var showFiles = false
    var photoItems: [PhotosPickerItem] = []
    /// Picked files are being read (a large video, many photos).
    var loading = false
    private(set) var filter = AcceptFilter(nil)
    private(set) var maxCount = 10
    /// A file that was left out (too big to read, not accepted), said once.
    var notice: String?

    @ObservationIgnored private var continuation: CheckedContinuation<[PickedFile], Never>?
    @ObservationIgnored private var presenting: Source?
    @ObservationIgnored private var maxBytes = TileUpload.maxBytes

    /// Shows the pickers `accept` allows and returns the chosen files.
    /// `maxBytes`: a bigger file is left out with a notice (never read into
    /// memory whole).
    func pick(accept: String?, maxCount: Int = 10, maxBytes: Int = TileUpload.maxBytes) async -> [PickedFile] {
        guard continuation == nil else { return [] }
        filter = AcceptFilter(accept)
        self.maxCount = max(1, maxCount)
        self.maxBytes = maxBytes
        notice = nil
        return await withCheckedContinuation { (c: CheckedContinuation<[PickedFile], Never>) in
            continuation = c
            let sources = availableSources
            if sources.count == 1 { present(sources[0]) } else if sources.isEmpty { finish([]) } else { choosing = true }
        }
    }

    var availableSources: [Source] {
        var s: [Source] = []
        if filter.allowsImages || filter.allowsVideos { s.append(.photos) }
        if filter.allowsImages, UIImagePickerController.isSourceTypeAvailable(.camera) { s.append(.camera) }
        if filter.allowsFiles || s.isEmpty { s.append(.files) }
        return s
    }

    /// A source from the dialog: presented once the dialog has gone.
    func choose(_ s: Source) {
        presenting = s
        choosing = false
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(350))
            self.present(s)
        }
    }

    /// The dialog went away: without a choice, the pick is over. Judged a
    /// beat later, so the order SwiftUI runs a button's action and clears
    /// `isPresented` in doesn't matter.
    func dialogDismissed() {
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(100))
            if self.presenting == nil, !self.choosing { self.finish([]) }
        }
    }

    private func present(_ s: Source) {
        presenting = s
        switch s {
        case .photos: photoItems = []; showPhotos = true
        case .camera: showCamera = true
        case .files: showFiles = true
        }
    }

    var photoFilter: PHPickerFilter {
        if filter.isAny || (filter.allowsImages && filter.allowsVideos) { return .any(of: [.images, .videos]) }
        return filter.allowsVideos ? .videos : .images
    }

    var fileTypes: [UTType] {
        if filter.isAny { return [.item] }
        var out: [UTType] = []
        for t in filter.types {
            if t.hasSuffix("/*") {
                switch t {
                case "image/*": out.append(.image)
                case "video/*": out.append(.movie)
                case "audio/*": out.append(.audio)
                case "text/*": out.append(.text)
                default: out.append(.item)
                }
            } else if let u = UTType(mimeType: t) {
                out.append(u)
            }
        }
        for e in filter.extensions { if let u = UTType(filenameExtension: e) { out.append(u) } }
        return out.isEmpty ? [.item] : out
    }

    // MARK: results

    /// The Photos sheet closed: read what was selected.
    func photosDismissed() {
        guard presenting == .photos else { return }
        Task { @MainActor in
            // The selection binding is set as the sheet goes; give it a beat.
            try? await Task.sleep(for: .milliseconds(150))
            let items = self.photoItems
            self.photoItems = []
            guard !items.isEmpty else { self.finish([]); return }
            self.loading = true
            var out: [PickedFile] = []
            for (i, item) in items.prefix(self.maxCount).enumerated() {
                if let f = await Self.load(item, index: i, maxBytes: self.maxBytes) {
                    out.append(f)
                } else {
                    self.notice = "A photo or video couldn't be read, or is over \(self.maxBytes >> 20) MiB."
                }
            }
            self.loading = false
            self.finish(out)
        }
    }

    /// A Photos item's bytes: JPEG for photos (HEIC converted), the movie
    /// as it is.
    private static func load(_ item: PhotosPickerItem, index: Int, maxBytes: Int) async -> PickedFile? {
        guard let data = try? await item.loadTransferable(type: Data.self), data.count <= maxBytes else { return nil }
        let type = item.supportedContentTypes.first ?? .data
        let stamp = Self.stamp()
        if type.conforms(to: .image) {
            if type.conforms(to: .jpeg) || type.conforms(to: .png) || type.conforms(to: .gif) {
                let ext = type.preferredFilenameExtension ?? "jpg"
                return PickedFile(name: "Photo-\(stamp)-\(index + 1).\(ext)", mime: type.preferredMIMEType ?? "image/jpeg", data: data)
            }
            // HEIC and friends: JPEG, which every backend and model reads.
            guard let img = UIImage(data: data), let jpeg = img.jpegData(compressionQuality: 0.85) else { return nil }
            return PickedFile(name: "Photo-\(stamp)-\(index + 1).jpg", mime: "image/jpeg", data: jpeg)
        }
        let ext = type.preferredFilenameExtension ?? "bin"
        return PickedFile(name: "Video-\(stamp)-\(index + 1).\(ext)", mime: type.preferredMIMEType ?? "", data: data)
    }

    /// The camera's photo (nil: cancelled).
    func shot(_ image: UIImage?) {
        showCamera = false
        guard let image, let jpeg = image.jpegData(compressionQuality: 0.85) else { finish([]); return }
        finish([PickedFile(name: "Photo-\(Self.stamp()).jpg", mime: "image/jpeg", data: jpeg)])
    }

    /// Files chose these (security-scoped URLs).
    func imported(_ result: Result<[URL], any Error>) {
        guard case .success(let urls) = result else { finish([]); return }
        var out: [PickedFile] = []
        for url in urls.prefix(maxCount) {
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            let size = (try? url.resourceValues(forKeys: [.fileSizeKey]))?.fileSize ?? 0
            let name = TileResource.fileName(url.lastPathComponent, fallback: "file")
            let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? ""
            guard size <= maxBytes, let data = try? Data(contentsOf: url), data.count <= maxBytes else {
                notice = "\(name) is over \(maxBytes >> 20) MiB — left out."
                continue
            }
            guard filter.accepts(name: name, mime: mime) else {
                notice = "\(name) isn't a kind of file this accepts — left out."
                continue
            }
            out.append(PickedFile(name: name, mime: mime, data: data))
        }
        finish(out)
    }

    func cancelled() { finish([]) }

    private func finish(_ files: [PickedFile]) {
        presenting = nil
        let c = continuation
        continuation = nil
        c?.resume(returning: files)
    }

    private static func stamp() -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyyMMdd-HHmmss"
        return f.string(from: Date())
    }
}

/// Presents an ``AttachPicker``'s dialog and pickers.
struct AttachPickers: ViewModifier {
    @Bindable var picker: AttachPicker

    func body(content: Content) -> some View {
        let sources = picker.availableSources
        content
            .confirmationDialog("Attach", isPresented: $picker.choosing, titleVisibility: .hidden) {
                if sources.contains(.photos) {
                    Button("Photo Library", systemImage: "photo.on.rectangle") { picker.choose(.photos) }
                }
                if sources.contains(.camera) {
                    Button("Take Photo", systemImage: "camera") { picker.choose(.camera) }
                }
                if sources.contains(.files) {
                    Button("Choose File", systemImage: "folder") { picker.choose(.files) }
                }
                Button("Cancel", role: .cancel) {}
            }
            .onChange(of: picker.choosing) { _, shown in
                if !shown { picker.dialogDismissed() }
            }
            .photosPicker(isPresented: $picker.showPhotos, selection: $picker.photoItems,
                          maxSelectionCount: picker.maxCount, matching: picker.photoFilter,
                          preferredItemEncoding: .compatible)
            .onChange(of: picker.showPhotos) { _, shown in
                if !shown { picker.photosDismissed() }
            }
            .fileImporter(isPresented: $picker.showFiles, allowedContentTypes: picker.fileTypes,
                          allowsMultipleSelection: picker.maxCount > 1,
                          onCompletion: { result in picker.imported(result) },
                          onCancellation: { picker.cancelled() })
            // A sheet, not a full-screen cover: a cover takes the presenting
            // screen off the hierarchy, and its onDisappear stops a native
            // tile's runtime or an agent session's feed.
            .sheet(isPresented: $picker.showCamera) {
                CameraPicker { image in picker.shot(image) }
                    .ignoresSafeArea()
                    .interactiveDismissDisabled()
            }
    }
}

/// The camera (UIImagePickerController): one photo.
struct CameraPicker: UIViewControllerRepresentable {
    let done: @MainActor (UIImage?) -> Void

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let c = UIImagePickerController()
        c.sourceType = .camera
        c.mediaTypes = [UTType.image.identifier]
        c.delegate = context.coordinator
        return c
    }

    func updateUIViewController(_ vc: UIImagePickerController, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(done: done) }

    @MainActor
    final class Coordinator: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
        let done: @MainActor (UIImage?) -> Void
        private var answered = false

        init(done: @escaping @MainActor (UIImage?) -> Void) { self.done = done }

        func imagePickerController(_ picker: UIImagePickerController,
                                   didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]) {
            guard !answered else { return }
            answered = true
            done(info[.originalImage] as? UIImage)
        }

        func imagePickerControllerDidCancel(_ picker: UIImagePickerController) {
            guard !answered else { return }
            answered = true
            done(nil)
        }
    }
}

// MARK: - A native tile's uploads

/// Uploads a picked file to the tile's own backend with the tile's frame
/// token (never the user's session), reporting progress; a 401 renews the
/// token once (the body is in memory, nothing happened server-side).
struct TileUploader {
    let origin: ServerOrigin
    let tile: String
    let frameTokens: FrameTokenCache

    /// The `response` the tile gets: the backend's answer (parsed JSON, else
    /// text), or `{error}` when nothing came back.
    func upload(_ file: PickedFile, to path: String, method: String,
                progress: @escaping @MainActor @Sendable (Double) -> Void) async -> JSONValue {
        for attempt in 0..<2 {
            let token: String
            do {
                token = attempt == 0 ? try await frameTokens.token(for: tile) : try await frameTokens.renew(tile)
            } catch {
                return TileUpload.failure("upload failed: no frame token (\(error.localizedDescription))")
            }
            guard let url = origin.url(path: path) else { return TileUpload.failure("upload failed: bad path") }
            var req = URLRequest(url: url)
            req.httpMethod = method
            req.timeoutInterval = 600
            req.setValue(token, forHTTPHeaderField: TileScheme.frameTokenHeader)
            req.setValue(AppInfo.clientHeader, forHTTPHeaderField: TileScheme.clientHeader)
            req.setValue(file.mime.isEmpty ? "application/octet-stream" : file.mime, forHTTPHeaderField: "Content-Type")
            req.setValue("application/json, */*;q=0.5", forHTTPHeaderField: "Accept")
            do {
                let delegate = UploadProgress(report: progress)
                let (body, resp) = try await AppTransport.shared.session.upload(for: req, from: file.data, delegate: delegate)
                guard let h = resp as? HTTPURLResponse else { return TileUpload.failure("upload failed: no answer") }
                if h.statusCode == 401, attempt == 0 {
                    await frameTokens.invalidate(tile, token: token)
                    continue
                }
                return TileUpload.response(body: body)
            } catch {
                return TileUpload.failure("upload failed: \(error.localizedDescription)")
            }
        }
        return TileUpload.failure("upload failed: signed out", status: 401)
    }
}

/// A task delegate forwarding upload progress to the main actor.
final class UploadProgress: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    private let report: @MainActor @Sendable (Double) -> Void

    init(report: @escaping @MainActor @Sendable (Double) -> Void) { self.report = report }

    func urlSession(_ session: URLSession, task: URLSessionTask, didSendBodyData bytesSent: Int64,
                    totalBytesSent: Int64, totalBytesExpectedToSend: Int64) {
        guard totalBytesExpectedToSend > 0 else { return }
        let p = min(1, Double(totalBytesSent) / Double(totalBytesExpectedToSend))
        let report = self.report
        Task { @MainActor in report(p) }
    }
}

/// A native tile's attach button (``XbinServices/attach``): the pickers,
/// then each file uploaded to the tile's own `upload.path` (confined to
/// `/api/<self>/`, TileResource) with progress shown over the tile; the
/// renderer hands the tile `uploaded {name, response}` per file.
@MainActor
@Observable
final class TileAttachFlow {
    struct Status: Equatable {
        var name: String
        var index: Int
        var count: Int
        var progress: Double
    }

    let picker = AttachPicker()
    /// The upload in progress (nil: none).
    var status: Status?
    /// A short message over the tile (a refused target, a file left out).
    var message: String?

    @ObservationIgnored private let workspace: WorkspaceModel
    @ObservationIgnored private let tile: String
    @ObservationIgnored private var busy = false

    init(workspace: WorkspaceModel, tile: String) {
        self.workspace = workspace
        self.tile = tile
    }

    func run(_ request: XbinAttachRequest) async -> [XbinUpload] {
        guard !busy else { return [] }
        busy = true
        defer { busy = false; status = nil }
        let known = workspace.catalog.tiles.map(\.path)
        guard let method = TileResource.uploadMethod(request.method),
              TileResource.apiPath(request.path, tile: tile, known: known, name: "file") != nil else {
            say("This tile's upload target is outside its own API — nothing was sent.")
            return []
        }
        let files = await picker.pick(accept: request.accept)
        if let n = picker.notice { say(n) }
        let uploader = TileUploader(origin: workspace.origin, tile: tile, frameTokens: workspace.frameTokens)
        var out: [XbinUpload] = []
        for (i, f) in files.enumerated() {
            let name = TileResource.fileName(f.name, fallback: "file")
            if f.data.count > TileUpload.maxBytes {
                out.append(XbinUpload(name: name, response: TileUpload.tooLarge(f.data.count)))
                continue
            }
            guard let path = TileResource.apiPath(request.path, tile: tile, known: known, name: name) else { continue }
            status = Status(name: name, index: i, count: files.count, progress: 0)
            let response = await uploader.upload(f, to: path, method: method) { [weak self] p in
                self?.status?.progress = p
            }
            out.append(XbinUpload(name: name, response: response))
        }
        return out
    }

    private func say(_ text: String) {
        message = text
        Task { @MainActor [weak self] in
            try? await Task.sleep(for: .seconds(4))
            if self?.message == text { self?.message = nil }
        }
    }
}

/// Upload progress and messages over a native tile.
struct AttachStatusView: View {
    let flow: TileAttachFlow

    var body: some View {
        VStack(spacing: 6) {
            if let m = flow.message {
                Text(verbatim: m)
                    .font(.footnote)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 12).padding(.vertical, 6)
                    .background(.thinMaterial, in: Capsule())
            }
            if let s = flow.status {
                HStack(spacing: 8) {
                    ProgressView(value: s.progress).frame(width: 72)
                    Text(verbatim: s.count > 1 ? "Uploading \(s.name) (\(s.index + 1) of \(s.count))" : "Uploading \(s.name)")
                        .font(.footnote)
                        .lineLimit(1)
                }
                .padding(.horizontal, 12).padding(.vertical, 6)
                .background(.thinMaterial, in: Capsule())
                .accessibilityElement(children: .combine)
            } else if flow.picker.loading {
                HStack(spacing: 8) {
                    ProgressView()
                    Text("Reading…").font(.footnote)
                }
                .padding(.horizontal, 12).padding(.vertical, 6)
                .background(.thinMaterial, in: Capsule())
            }
        }
        .padding(.bottom, 72)
        .animation(.default, value: flow.status?.name)
        .allowsHitTesting(false)
    }
}
