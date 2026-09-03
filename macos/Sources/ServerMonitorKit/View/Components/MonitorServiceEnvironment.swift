import SwiftUI

/// The service, reachable without observing it.
///
/// `@EnvironmentObject` subscribes a view to every published change, and
/// MonitorService publishes once a tick. Views that only *call* the service —
/// resolve a target, list a directory, run a snippet — were re-evaluating
/// their whole body on every poll for nothing: the terminal pane (with its
/// `NSViewRepresentable` update), the SFTP table, the Docker pane. This key
/// hands them the same instance with no subscription attached.
///
/// Optional because an environment key needs a default and there is no
/// meaningful service to default to; the App always injects one. `required`
/// turns the missing case into a thrown error at the call site, where the
/// view already has a `catch` that shows what went wrong.
struct MonitorServiceKey: EnvironmentKey {
    static let defaultValue: MonitorService? = nil
}

extension EnvironmentValues {
    public var monitorService: MonitorService? {
        get { self[MonitorServiceKey.self] }
        set { self[MonitorServiceKey.self] = newValue }
    }
}

struct MonitorServiceMissing: LocalizedError {
    var errorDescription: String? { "The monitor service is not available to this view." }
}

extension Optional where Wrapped == MonitorService {
    /// The injected service, or a thrown error where the App forgot to inject it.
    var required: MonitorService {
        get throws {
            guard let self else { throw MonitorServiceMissing() }
            return self
        }
    }
}
