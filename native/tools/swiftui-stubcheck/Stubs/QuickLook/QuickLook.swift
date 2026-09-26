@_exported import Foundation
import SwiftUI

// The SwiftUI part of QuickLook the renderer uses (iOS 14+).
extension View {
    public func quickLookPreview(_ item: Binding<URL?>) -> some View { _V(self) }
}
