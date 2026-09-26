import Foundation
import UIKit
import XbinAgent
import XbinCore

/// A file waiting in the agent composer for the next prompt (§13).
struct PendingAttachment: Identifiable, Equatable {
    let id = UUID().uuidString
    var file: PickedFile

    var prompt: PromptAttachment { PromptAttachment(name: file.name, mime: file.mime, data: file.data) }
}

/// Photos made model-ready before they ride a prompt: xbind sends an image
/// inline only when it is png/jpeg/gif/webp and ≤ 3.75 MiB, and a model
/// reads no more than 2576 px of its long edge — so HEIC becomes JPEG and a
/// big photo is redrawn smaller (the decision is XbinAgent's
/// PromptAttachment.imagePlan, tested on Linux). The pickers read an image
/// up to 64 MiB for this (PromptAttachment.readLimit); the 10 MiB file
/// limit applies to what comes out.
enum AgentImages {
    static func prepare(_ f: PickedFile) -> PickedFile {
        let type = PromptAttachment.sniffImage(f.data)
        guard type != nil || f.mime.hasPrefix("image/"), let img = UIImage(data: f.data) else { return f }
        let w = Int((img.size.width * img.scale).rounded())
        let h = Int((img.size.height * img.scale).rounded())
        switch PromptAttachment.imagePlan(width: w, height: h, bytes: f.data.count, type: type) {
        case .keep:
            return f
        case .reencode(let width, let height, let png):
            let size = CGSize(width: width, height: height)
            let format = UIGraphicsImageRendererFormat.default()
            format.scale = 1
            let drawn = UIGraphicsImageRenderer(size: size, format: format).image { _ in
                img.draw(in: CGRect(origin: .zero, size: size))
            }
            if png, let d = drawn.pngData(), d.count <= PromptAttachment.maxInlineImageBytes {
                return PickedFile(name: f.name, mime: "image/png", data: d)
            }
            var quality: CGFloat = 0.85
            var out = drawn.jpegData(compressionQuality: quality)
            while let d = out, d.count > PromptAttachment.maxInlineImageBytes, quality > 0.45 {
                quality -= 0.15
                out = drawn.jpegData(compressionQuality: quality)
            }
            guard let data = out else { return f }
            return PickedFile(name: jpegName(f.name), mime: "image/jpeg", data: data)
        }
    }

    static func jpegName(_ name: String) -> String {
        let base = (name as NSString).deletingPathExtension
        return (base.isEmpty ? "photo" : base) + ".jpg"
    }
}
