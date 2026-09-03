import SwiftUI

struct SidebarSelectionKey: FocusedValueKey {
    typealias Value = Binding<RootView.Selection?>
}

extension FocusedValues {
    /// The main window's sidebar selection, published by RootView so menu
    /// commands can move it.
    var sidebarSelection: Binding<RootView.Selection?>? {
        get { self[SidebarSelectionKey.self] }
        set { self[SidebarSelectionKey.self] = newValue }
    }
}

/// A Go menu: ⌘1…⌘7 jump to the sidebar's fixed destinations, the way Mail
/// and Finder number theirs. Servers and sessions are not numbered — they
/// come and go, and a shortcut that means a different host next week is
/// worse than none.
public struct NavigationCommands: Commands {
    // Not observed: the App re-evaluates its commands when the language
    // changes, which is the only time these titles move.
    private let loc: Localization
    @FocusedValue(\.sidebarSelection) private var selection

    public init(localization: Localization) {
        self.loc = localization
    }

    public var body: some Commands {
        CommandMenu(loc.t("nav.goTo")) {
            item("nav.dashboard", .dashboard, "1")
            item("nav.machines", .machines, "2")
            item("nav.identities", .identities, "3")
            item("nav.sshKeys", .sshKeys, "4")
            item("nav.snippets", .snippets, "5")
            item("nav.docker", .docker, "6")
            item("nav.history", .history, "7")
        }
    }

    private func item(_ key: String, _ destination: RootView.Selection, _ digit: KeyEquivalent) -> some View {
        Button(loc.t(key)) { selection?.wrappedValue = destination }
            .keyboardShortcut(digit, modifiers: .command)
            // No main window focused (the menu bar panel, Settings): nothing to move.
            .disabled(selection == nil)
    }
}
