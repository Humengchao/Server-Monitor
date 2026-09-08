// Serial, deliberately. HandleTests measures this process's handle count
// across a hundred failed SSH connections, and a handle count is a property
// of the process, not of a test: run in parallel, whatever SftpTests,
// SshTests and MonitorServiceTests happen to have open lands inside the
// measurement window. That failed about one solution run in ten at 54 handles
// against a threshold of 40 — noise indistinguishable, from the test's point
// of view, from the per-attempt leak it exists to catch.
//
// Closing the stores the tests leak (Database is disposable now) cut the
// baseline but not the variance, because the contamination is concurrent
// activity rather than accumulation. Serialising is what makes the number
// mean what the assertion says it means.
[assembly: Xunit.CollectionBehavior(DisableTestParallelization = true)]
