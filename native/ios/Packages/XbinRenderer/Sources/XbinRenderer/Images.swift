#if canImport(UIKit)
import QuickLook
import SwiftUI
import UIKit
import XbinCore
import XbinRendererModel

// Images of a tree — `image` elements and message thumbnails — and their
// Quick Look previews (plans/native.md §8.2: the app loads tile-relative
// sources itself, with the tile's frame token; `data:` images are decoded
// here).

/// The images one tree shows: `data:` sources decoded here, anything else
/// fetched by the app (``XbinServices/imageData``) and kept in a small
/// cache, per tree — two tiles' `icons/logo.png` are different images.
@MainActor
public final class XbinImages {
    /// A loaded image and the bytes it came from (Quick Look previews the
    /// bytes, not a re-encoding).
    public struct Loaded {
        public let image: UIImage
        public let data: Data
    }

    private final class Box {
        let loaded: Loaded
        init(_ loaded: Loaded) { self.loaded = loaded }
    }

    private let load: (@MainActor (String) async throws -> Data)?
    private let cache = NSCache<NSString, Box>()

    /// `load` fetches a tile-relative source (nil: only `data:` images
    /// show).
    public init(load: (@MainActor (String) async throws -> Data)?) {
        self.load = load
        cache.countLimit = 64
    }

    /// The image at `src`, or nil when there is none or it can't be
    /// loaded or decoded.
    public func image(_ src: String) async -> Loaded? {
        if let d = Self.decode(src) { return d }
        guard !src.isEmpty, !src.hasPrefix("data:"), let load else { return nil }
        if let hit = cache.object(forKey: src as NSString) { return hit.loaded }
        guard let data = try? await load(src), let image = UIImage(data: data) else { return nil }
        let loaded = Loaded(image: image, data: data)
        cache.setObject(Box(loaded), forKey: src as NSString)
        return loaded
    }

    /// A `data:` image (≤ 256 KiB, ``DataURL``), decoded without the app.
    public static func decode(_ src: String) -> Loaded? {
        guard let data = DataURL.decode(src), let image = UIImage(data: data) else { return nil }
        return Loaded(image: image, data: data)
    }

    /// Loads `src` through `images` when there is one, else decodes it when
    /// it is a `data:` image.
    static func load(_ src: String, with images: XbinImages?) async -> Loaded? {
        if let images { return await images.image(src) }
        return decode(src)
    }
}

/// The files Quick Look previews: the loaded bytes written to a private
/// temporary directory under a name Quick Look can read
/// (``PreviewFile``), removed once the preview closes.
@MainActor
enum QuickLookFile {
    static var root: URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("xbin-quicklook", isDirectory: true)
    }

    /// Writes `data` for a preview; nil when it can't be written.
    static func write(_ data: Data, name: String?) -> URL? {
        let dir = root.appendingPathComponent(UUID().uuidString, isDirectory: true)
        let url = dir.appendingPathComponent(PreviewFile.name(name, data: data))
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            try data.write(to: url, options: [.atomic])
            return url
        } catch {
            return nil
        }
    }

    /// Removes a file written by ``write(_:name:)`` (and its directory).
    static func remove(_ url: URL) {
        let dir = url.deletingLastPathComponent()
        guard dir.deletingLastPathComponent().standardizedFileURL == root.standardizedFileURL else { return }
        try? FileManager.default.removeItem(at: dir)
    }
}

/// Opens the image in Quick Look (the system previewer: zoom, share, save,
/// markup) while `url` is set, and removes the temporary file afterwards.
struct QuickLookModifier: ViewModifier {
    @Binding var url: URL?

    func body(content: Content) -> some View {
        content
            .quickLookPreview($url)
            .onChange(of: url) { old, new in
                if new == nil, let old { QuickLookFile.remove(old) }
            }
    }
}

/// An image file of a message: a thumbnail (a placeholder while it loads
/// or when it can't), tapped to open in Quick Look.
struct FileThumbnail: View {
    let file: ChatFile
    @Environment(\.xbinImages) private var images
    @ScaledMetric(relativeTo: .body) private var maxHeight: CGFloat = 150
    @State private var loaded: XbinImages.Loaded?
    @State private var preview: URL?

    var body: some View {
        let src = file.thumbnailSource ?? ""
        Button {
            if let loaded { preview = QuickLookFile.write(loaded.data, name: file.name) }
        } label: {
            thumb
        }
        .buttonStyle(.plain)
        .disabled(loaded == nil)
        .accessibilityLabel(Text(verbatim: file.name))
        .accessibilityHint(loaded == nil ? "" : "Opens a preview")
        .task(id: src) { loaded = await XbinImages.load(src, with: images) }
        .modifier(QuickLookModifier(url: $preview))
    }

    /// The image at most `maxHeight` tall and 220 points wide, keeping its
    /// aspect ratio (a panorama is cropped to 2:1); a grey box before.
    @ViewBuilder
    private var thumb: some View {
        let shape = RoundedRectangle(cornerRadius: 12, style: .continuous)
        if let loaded {
            let size = Self.fit(loaded.image.size, maxHeight: maxHeight)
            Image(uiImage: loaded.image)
                .resizable()
                .scaledToFill()
                .frame(width: size.width, height: size.height)
                .clipShape(shape)
                .overlay { shape.strokeBorder(XbinColor.border, lineWidth: 0.5) }
        } else {
            shape
                .fill(XbinColor.fill)
                .frame(width: maxHeight * 4 / 3, height: maxHeight * 3 / 4)
                .overlay {
                    Image(systemName: XbinIcons.UI.image).foregroundStyle(XbinColor.muted)
                }
        }
    }

    static func fit(_ size: CGSize, maxHeight: CGFloat) -> CGSize {
        guard size.width > 0, size.height > 0 else { return CGSize(width: maxHeight, height: maxHeight) }
        let maxWidth: CGFloat = 220
        var h = min(maxHeight, size.height)
        var w = h * size.width / size.height
        if w > maxWidth {
            w = maxWidth
            h = max(w / 2, min(h, w * size.height / size.width))
        }
        return CGSize(width: max(44, w), height: max(44, h))
    }
}

/// A non-image file (or an image without a source): its name in a chip.
struct FileChip: View {
    let file: ChatFile

    var body: some View {
        Label(file.name, systemImage: file.isImage ? XbinIcons.UI.image : XbinIcons.UI.attachment)
            .font(.caption)
            .lineLimit(1)
            .truncationMode(.middle)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(XbinColor.fill, in: Capsule())
    }
}
#endif
