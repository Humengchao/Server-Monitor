import Foundation

/// A generation's right to write samples. The database checks it on its own
/// queue, not only before the caller suspends to enqueue a transaction.
final class PollWriteValidity: @unchecked Sendable {
    private let lock = NSLock()
    private var valid = true

    func invalidate() {
        lock.lock()
        valid = false
        lock.unlock()
    }

    func withValidWrite(_ body: () throws -> Void) throws {
        lock.lock()
        defer { lock.unlock() }
        guard valid else { throw CancellationError() }
        try body()
    }
}
