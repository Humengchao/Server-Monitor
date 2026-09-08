# Shared fixtures

Captured output from real hosts, plus the few synthetic samples whose exact
numbers a test asserts on. One copy, read by every collector's tests, so the
three parsers cannot quietly disagree about what a host said.

Consumed today by `windows/tests/ServerMonitor.Core.Tests` (copied to the test
output as `Fixtures/`). Wiring the Swift tests to read these instead of their
inline heredocs is a task for the macOS machine — see `../probes/README.md`.

Small synthetic inputs — a two-line `/proc/stat` pair whose arithmetic the test
spells out — stay inline in the test that asserts on them: naming a file for
four numbers costs more than it explains. What lives here is either real host
output or a sample long enough that inlining it buries the assertion.
