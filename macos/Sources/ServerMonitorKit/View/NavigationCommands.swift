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
            // Numbered in sidebar order, from the one list the sidebar draws.
            ForEach(Array(RootView.Selection.fixedDestinations.enumerated()), id: \.offset) { index, destination in
                Button(loc.t(destination.titleKey)) { selection?.wrappedValue = destination }
                    .keyboardShortcut(KeyEquivalent(Character(String(index + 1))), modifiers: .command)
                    // No main window focused (the menu bar panel, Settings): nothing to move.
                    .disabled(selection == nil)
            }
        }
    }
}
