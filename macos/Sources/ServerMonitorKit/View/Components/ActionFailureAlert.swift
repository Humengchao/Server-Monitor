import SwiftUI

/// Surfaces a failed action instead of letting it vanish.
///
/// Deleting a group, an identity or a snippet is one SQLite write, and those
/// normally succeed — so these call sites all used `try?`. But when one does
/// fail (a full disk, a locked database, a constraint), swallowing it means
/// the user confirms a deletion, watches nothing happen, and has no idea
/// whether the app is broken or they misclicked.
struct ActionFailureAlert: ViewModifier {
    @Binding var failure: String?

    @EnvironmentObject private var loc: Localization

    func body(content: Content) -> some View {
        content.alert(
            loc.t("common.error"),
            isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } }),
            presenting: failure
        ) { _ in
            Button(loc.t("common.close"), role: .cancel) { failure = nil }
        } message: { message in
            Text(message)
        }
    }
}

extension View {
    /// Pairs with `failureMessage(of:)` at the call site.
    func actionFailureAlert(_ failure: Binding<String?>) -> some View {
        modifier(ActionFailureAlert(failure: failure))
    }
}

/// Runs a throwing action and returns what to show, or `nil` when it worked —
/// so a call site reads `failure = failureMessage { try … }`.
@MainActor
func failureMessage(of action: () throws -> Void) -> String? {
    do {
        try action()
        return nil
    } catch {
        return error.localizedDescription
    }
}
