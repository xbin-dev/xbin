import Foundation
import SwiftUI
import UniformTypeIdentifiers
public struct PHPickerFilter: Sendable {
    public static let images = PHPickerFilter(), videos = PHPickerFilter()
    public static func any(of f: [PHPickerFilter]) -> PHPickerFilter { PHPickerFilter() }
}
public struct PhotosPickerSelectionBehavior: Sendable { public static let `default` = PhotosPickerSelectionBehavior() }
public struct PhotosPickerItem: Equatable, Hashable, Sendable {
    public struct EncodingDisambiguationPolicy: Sendable { public static let automatic = EncodingDisambiguationPolicy(), compatible = EncodingDisambiguationPolicy(), current = EncodingDisambiguationPolicy() }
    public var supportedContentTypes: [UTType] { [] }
    public var itemIdentifier: String? { nil }
    public func loadTransferable<T: Transferable>(type: T.Type) async throws -> T? { nil }
}
public protocol Transferable {}
extension Data: Transferable {}
extension View {
    public func photosPicker(isPresented: Binding<Bool>, selection: Binding<[PhotosPickerItem]>, maxSelectionCount: Int? = nil,
                             selectionBehavior: PhotosPickerSelectionBehavior = .default, matching filter: PHPickerFilter? = nil,
                             preferredItemEncoding: PhotosPickerItem.EncodingDisambiguationPolicy = .automatic) -> some View { _V(self) }
}
