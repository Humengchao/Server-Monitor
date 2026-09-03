import SwiftUI

extension View {
    /// Tooltip and VoiceOver name in one. An icon-only control needs both, and
    /// they are the same words — spelling them twice per button is how every
    /// icon button in this app came to have a tooltip and no name.
    func hint(_ text: String) -> some View {
        help(text).accessibilityLabel(text)
    }
}
