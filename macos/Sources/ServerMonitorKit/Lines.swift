import Foundation

extension StringProtocol {
    /// Splits text into lines on any kind of line break.
    ///
    /// The obvious `split(separator: "\n")` silently fails on Windows output:
    /// Swift treats `\r\n` as a *single* extended grapheme cluster, so it never
    /// compares equal to `"\n"` and a CRLF document comes back as one enormous
    /// "line". Every parser here reads output from remote hosts, so none of
    /// them may assume LF.
    ///
    /// Empty lines are dropped, matching `split(separator:)`'s default.
    func lines() -> [SubSequence] {
        split(whereSeparator: \.isNewline)
    }
}
