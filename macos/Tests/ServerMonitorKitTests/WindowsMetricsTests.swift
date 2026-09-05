import Compression
import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("Windows metrics")
struct WindowsMetricsTests {

    private let sample = """
    cpu=37
    memtotal=16777216
    memfree=8388608
    netrx=1000000
    nettx=200000
    diskread=4096
    diskwrite=8192
    uptime=123456
    cores=8
    disktotal=536870912000
    diskfree=136870912000
    queue=2
    docker=24.0.7
    """

    @Test func parsesKeyValueOutput() {
        let snapshot = WindowsMetrics.parse(sample)
        #expect(snapshot?.cpuPercent == 37)
        #expect(snapshot?.cores == 8)
        #expect(snapshot?.uptimeSeconds == 123_456)
        #expect(snapshot?.dockerVersion == "24.0.7")
    }

    @Test func memoryIsConvertedFromKilobytes() {
        // TotalVisibleMemorySize and FreePhysicalMemory are kB, unlike the
        // byte-valued disk fields next to them.
        let snapshot = WindowsMetrics.parse(sample)
        // 16_777_216 kB = 16 GiB; used is total - free = 8_388_608 kB.
        #expect(snapshot?.memoryTotal == Int64(17_179_869_184))
        #expect(snapshot?.memoryUsed == Int64(8_589_934_592))
    }

    @Test func diskIsAlreadyInBytes() {
        let snapshot = WindowsMetrics.parse(sample)
        #expect(snapshot?.diskTotal == Int64(536_870_912_000))
        // total - free, both already in bytes.
        #expect(snapshot?.diskUsed == Int64(400_000_000_000))
    }

    @Test func processorQueueStandsInForLoad() {
        // Windows has no load average; the queue length is the closest thing
        // and is what the web backend showed too.
        #expect(WindowsMetrics.parse(sample)?.load1 == 2)
    }

    @Test func negativeDerivedValuesAreClamped() {
        // A host reporting more free than total must not yield negative usage.
        let odd = "memtotal=100\nmemfree=500\ndisktotal=10\ndiskfree=99\ncores=1"
        let snapshot = WindowsMetrics.parse(odd)
        #expect(snapshot?.memoryUsed == 0)
        #expect(snapshot?.diskUsed == 0)
    }

    @Test func linuxOutputParsesToNothing() {
        // This is what makes auto-detection work: /proc output has no
        // key=value lines, so Windows parsing declines it.
        #expect(WindowsMetrics.parse("cpu  100 0 100 1000 0 0 0 0") == nil)
        #expect(WindowsMetrics.parse("") == nil)
    }

    @Test func countersAreExtractedForRates() {
        let counters = WindowsMetrics.counters(sample)
        #expect(counters.net.rx == 1_000_000)
        #expect(counters.net.tx == 200_000)
        #expect(counters.disk.read == 4096)
        #expect(counters.disk.written == 8192)
    }

    @Test func encodesAsUTF16LEBase64() {
        // -EncodedCommand demands UTF-16LE; getting this wrong yields a shell
        // error rather than a wrong number, but only on a real Windows host.
        let encoded = WindowsMetrics.encode("AB")
        #expect(encoded == Data([0x41, 0x00, 0x42, 0x00]).base64EncodedString())
    }

    @Test func commandIsNonInteractiveAndProfileFree() {
        let command = WindowsMetrics.command
        #expect(command.contains("-NoProfile"))
        #expect(command.contains("-NonInteractive"))
        #expect(command.contains("-EncodedCommand"))
    }
}

@Suite("Per-server thresholds")
@MainActor
struct PerServerThresholdTests {

    final class Recorder: @unchecked Sendable {
        private(set) var count = 0
        func record(_ title: String, _ body: String) { count += 1 }
    }

    private func snapshot(cpu: Double) -> MetricSnapshot {
        var value = MetricSnapshot()
        value.cpuPercent = cpu
        return value
    }

    @Test func serverLimitOverridesTheGlobalOne() {
        let settings = AppSettings()
        settings.notificationsEnabled = true
        settings.notifyOnOffline = false
        settings.cpuThreshold = 90
        let recorder = Recorder()
        let service = AlertService(settings: settings) { recorder.record($0, $1) }

        var server = Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .agent)
        server.cpuThreshold = 50

        // 60% is under the global 90 but over this server's own 50.
        for _ in 0..<3 {
            service.evaluate(server: server, status: .online(at: Date()), snapshot: snapshot(cpu: 60))
        }
        #expect(recorder.count == 1)
    }

    @Test func serverCanOptOutWhileGlobalIsOn() {
        let settings = AppSettings()
        settings.notificationsEnabled = true
        settings.notifyOnOffline = false
        settings.cpuThreshold = 50
        let recorder = Recorder()
        let service = AlertService(settings: settings) { recorder.record($0, $1) }

        var server = Server(name: "noisy", host: "10.0.0.2", username: "root", authKind: .agent)
        server.cpuThreshold = 0   // explicitly off, not "inherit"

        for _ in 0..<5 {
            service.evaluate(server: server, status: .online(at: Date()), snapshot: snapshot(cpu: 99))
        }
        #expect(recorder.count == 0)
    }

    @Test func nilMeansInheritNotOff() throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .agent)
        try database.save(server)
        let stored = try #require(try database.allServers().first)
        #expect(stored.cpuThreshold == nil)
        #expect(stored.osKind == .auto)
    }
}

@Suite("Windows real output")
struct WindowsRealOutputTests {

    /// Captured verbatim from a Windows Server 2016 host, CLIXML noise and all.
    /// PowerShell serialises its progress stream into stdout when redirected,
    /// and the parser has to walk past it rather than choke.
    private let captured = """
    #< CLIXML
    cpu=85
    memtotal=2013484
    memfree=327740
    netrx=10269283506
    nettx=8587544680
    diskread=857311823872
    diskwrite=512823487488
    uptime=4160659
    cores=2
    disktotal=42947571712
    diskfree=14780121088
    queue=13
    docker=
    <Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04"><Obj S="progress" RefId="0"><TN RefId="0"><T>System.Management.Automation.PSCustomObject</T></TN><MS><I64 N="SourceId">1</I64><PR N="Record"><AV>preparing</AV><AI>0</AI><PI>-1</PI><PC>-1</PC><T>Completed</T><SR>-1</SR></PR></MS></Obj></Objs>
    """

    @Test func parsesRealHostOutputDespiteCLIXML() {
        let snapshot = WindowsMetrics.parse(captured)
        #expect(snapshot != nil)
        #expect(snapshot?.cpuPercent == 85)
        #expect(snapshot?.cores == 2)
        #expect(snapshot?.uptimeSeconds == 4_160_659)
        // 2013484 kB total, 327740 kB free.
        #expect(snapshot?.memoryTotal == Int64(2_061_807_616))
        #expect(snapshot?.memoryUsed == Int64(1_726_201_856))
        #expect(snapshot?.diskTotal == Int64(42_947_571_712))
        #expect(snapshot?.diskUsed == Int64(28_167_450_624))
        // Processor queue length stands in for load average.
        #expect(snapshot?.load1 == 13)
    }

    @Test func absentDockerIsReportedAsAbsent() {
        // The host has no Docker, so the line is "docker=" with nothing after.
        #expect(WindowsMetrics.parse(captured)?.dockerVersion == "")
    }

    @Test func clixmlDoesNotPolluteTheCounters() {
        // The XML line is full of `=` signs; none may be mistaken for a metric.
        let counters = WindowsMetrics.counters(captured)
        #expect(counters.net.rx == 10_269_283_506)
        #expect(counters.net.tx == 8_587_544_680)
        #expect(counters.disk.read == 857_311_823_872)
        #expect(counters.disk.written == 512_823_487_488)
    }
}

@Suite("Password auth plumbing", .serialized)
struct PasswordAuthTests {

    private func target(_ id: UUID) -> SSHTarget {
        SSHTarget(serverID: id, host: "10.0.0.1", port: 22, username: "Administrator", credential: .password)
    }

    @Test func noHelperWithoutAStoredPassword() throws {
        // Nothing in the keychain means nothing to hand ssh.
        #expect(try SSHRunner.makeAskpass(for: target(UUID())) == nil)
    }

    @Test func helperIsCreatedLockedDownAndRemoved() throws {
        let id = UUID()
        try Keychain.savePassword("s3cret!", serverID: id)
        defer { Keychain.deletePassword(serverID: id) }

        let askpass = try #require(try SSHRunner.makeAskpass(for: target(id)))
        let helper = try #require(askpass.environment["SSH_ASKPASS"])
        let secret = try #require(askpass.environment["SM_SECRET"])

        // ssh must be told to prefer the helper over a terminal prompt.
        #expect(askpass.environment["SSH_ASKPASS_REQUIRE"] == "force")

        // The secret is readable only by this user.
        let attributes = try FileManager.default.attributesOfItem(atPath: secret)
        #expect((attributes[.posixPermissions] as? NSNumber)?.intValue == 0o600)
        #expect(try String(contentsOfFile: secret, encoding: .utf8) == "s3cret!")

        // And the helper hands exactly that back.
        let output = try SSHRunnerTestSupport.runHelper(helper, secretPath: secret)
        #expect(output == "s3cret!")

        askpass.cleanUp()
        #expect(FileManager.default.fileExists(atPath: secret) == false)
        #expect(FileManager.default.fileExists(atPath: helper) == false)
    }

    @Test func removingAnOptionTakesItsFlagToo() {
        // Leaving a bare "-o" makes ssh refuse to start with
        // `no argument after keyword "-o"` — found only against a real host.
        var arguments = ["-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "host"]
        SSHRunner.removeOption("BatchMode=yes", from: &arguments)
        #expect(arguments == ["-o", "ConnectTimeout=10", "host"])
    }

    @Test func removingAnAbsentOptionIsANoOp() {
        var arguments = ["-o", "ConnectTimeout=10", "host"]
        SSHRunner.removeOption("BatchMode=yes", from: &arguments)
        #expect(arguments == ["-o", "ConnectTimeout=10", "host"])
    }

    @Test func aValueNotPrecededByDashOIsLeftAlone() {
        // "host" happens to equal the value; it is not an option and must stay.
        var arguments = ["-o", "ConnectTimeout=10", "BatchMode=yes"]
        SSHRunner.removeOption("BatchMode=yes", from: &arguments)
        #expect(arguments == ["-o", "ConnectTimeout=10", "BatchMode=yes"])
    }

    @Test func everyGeneratedArgumentListIsWellFormed() {
        // Every -o must be followed by a value, for all credential kinds.
        for credential in [SSHCredential.sshConfigAlias, .agent, .password, .identityFile(path: "/k")] {
            let target = SSHTarget(
                serverID: UUID(), host: "h", port: 2222, username: "u", credential: credential
            )
            let arguments = SSHRunner.baseArguments(for: target, controlPath: "/tmp/cp")
            for (index, argument) in arguments.enumerated() where argument == "-o" {
                #expect(index + 1 < arguments.count, "dangling -o for \(credential)")
                #expect(arguments[index + 1].contains("="), "bad -o value for \(credential)")
            }
        }
    }

    @Test func crlfOutputParses() throws {
        // Real Windows hosts answer with CRLF. Swift treats "\r\n" as a single
        // grapheme cluster, so split(separator: "\n") matches nothing and the
        // whole document arrives as one line — every value then fails to parse
        // and the host reads as "no metrics".
        let crlf = """
        cpu=93
        memtotal=2013484
        memfree=327172
        netrx=10275729650
        nettx=8592295383
        diskread=857671295488
        diskwrite=513161345024
        uptime=4162760
        cores=2
        disktotal=42947571712
        diskfree=14778548224
        queue=8
        docker=
        """.replacingOccurrences(of: "\n", with: "\r\n")

        let snapshot = try #require(WindowsMetrics.parse(crlf))
        #expect(snapshot.cpuPercent == 93)
        #expect(snapshot.cores == 2)
        #expect(snapshot.memoryTotal == Int64(2_013_484) * 1024)
        #expect(snapshot.uptimeSeconds == 4_162_760)
        #expect(snapshot.diskTotal == 42_947_571_712)

        let counters = WindowsMetrics.counters(crlf)
        #expect(counters.net.rx == 10_275_729_650)
        #expect(counters.disk.written == 513_161_345_024)
    }

    @Test func lineSplittingHandlesEveryLineEnding() {
        for terminator in ["\n", "\r\n", "\r"] {
            let text = ["a=1", "b=2", "c=3"].joined(separator: terminator)
            #expect(text.lines().map(String.init) == ["a=1", "b=2", "c=3"], "failed for \(terminator.debugDescription)")
        }
    }

    @Test func theEncodedCommandFitsInCmdExe() {
        // Windows OpenSSH runs the command through the host's default shell.
        // cmd.exe caps its command line near 8191 characters and answers
        // "命令行太长" past that — with a non-zero exit and nothing on stdout,
        // which reads like a broken host rather than a too-long command.
        #expect(WindowsMetrics.command.count < 7000, "encoded command is \(WindowsMetrics.command.count) chars")
    }

    @Test func theDeflatedPayloadRoundTrips() throws {
        // If the compressor and .NET's DeflateStream ever disagreed, the host
        // would fail at `iex` with no useful message, so prove the bytes are
        // real DEFLATE here rather than discovering it over SSH.
        let deflated = WindowsMetrics.deflate(WindowsMetrics.script)
        #expect(deflated.count < WindowsMetrics.script.utf8.count, "payload did not compress")

        let inflated = try #require(WindowsMetricsTestSupport.inflate(deflated))
        #expect(inflated == WindowsMetrics.script)
    }

    @Test func theStubCarriesTheWholeScript() {
        // A truncated payload would still be valid base64 and still run.
        let stub = WindowsMetrics.stub
        #expect(stub.contains("DeflateStream"))
        #expect(stub.contains("iex"))
        #expect(stub.hasSuffix("ReadToEnd()"))
    }

    @Test func cancellingTheTaskKillsTheProcess() async throws {
        // `sleep 30` stands in for any long ssh. Cancel after a moment and the
        // call must come back promptly with a CancellationError — not after 30 s,
        // and not disguised as a timeout.
        let started = Date()
        let task = Task {
            try await SSHRunner.execute(executable: "/bin/sleep", arguments: ["30"], timeout: 60)
        }
        try await Task.sleep(for: .milliseconds(150))
        task.cancel()
        let outcome = await task.result
        let elapsed = Date().timeIntervalSince(started)

        #expect(elapsed < 2, "took \(elapsed)s — the process was not killed on cancellation")
        switch outcome {
        case .success: Issue.record("a cancelled execute must not succeed")
        case .failure(let error):
            #expect(error is CancellationError, "got \(error) instead of CancellationError")
        }
        // And nothing left behind.
        let stray = Process()
        stray.executableURL = URL(fileURLWithPath: "/usr/bin/pgrep")
        stray.arguments = ["-f", "^/bin/sleep 30$"]
        let pipe = Pipe()
        stray.standardOutput = pipe
        try stray.run()
        stray.waitUntilExit()
        #expect(stray.terminationStatus == 1, "a `sleep 30` from this test is still running")
    }

    @Test func aNormalRunIsNotAffectedByTheCancellationHandler() async throws {
        let output = try await SSHRunner.execute(executable: "/bin/echo", arguments: ["hello"], timeout: 5)
        #expect(output.trimmingCharacters(in: .whitespacesAndNewlines) == "hello")
    }

    @Test func theWatchdogStillReportsATimeout() async throws {
        // Cancellation and timeout both end in SIGTERM; only one is a timeout.
        await #expect(throws: SSHRunner.Failure.self) {
            _ = try await SSHRunner.execute(executable: "/bin/sleep", arguments: ["30"], timeout: 1)
        }
    }

    @Test func aTimedOutCommandNeverPublishesPartialOutput() async throws {
        // A metrics script can print a valid-looking prefix before hanging.
        // That prefix must not be accepted as a current snapshot after the
        // watchdog fires.
        await #expect(throws: SSHRunner.Failure.self) {
            _ = try await SSHRunner.execute(
                executable: "/bin/sh",
                arguments: ["-c", "printf 'cpu=42\\n'; sleep 30"],
                timeout: 1
            )
        }
    }

    @Test func controlMastersAreIsolatedPerConfiguredServer() throws {
        let endpoint = UUID()
        let first = SSHTarget(
            serverID: endpoint, host: "same.example", port: 22,
            username: "root", credential: .identityFile(path: "/keys/one")
        )
        let second = SSHTarget(
            serverID: UUID(), host: "same.example", port: 22,
            username: "root", credential: .identityFile(path: "/keys/one")
        )
        let differentCredential = SSHTarget(
            serverID: endpoint, host: "same.example", port: 22,
            username: "root", credential: .identityFile(path: "/keys/two")
        )

        let firstPath = try SSHRunner.controlPath(for: first)
        #expect(firstPath != (try SSHRunner.controlPath(for: second)))
        #expect(firstPath != (try SSHRunner.controlPath(for: differentCredential)))
        #expect(firstPath == (try SSHRunner.controlPath(for: first)))
    }

    @Test func onlySilent255CountsAsADeadMultiplexSocket() {
        // Retrying on anything else would double the time an unreachable host
        // takes to be reported offline.
        #expect(SSHRunner.isDeadMultiplexSocket(.commandFailed(status: 255, stderr: "")))
        #expect(SSHRunner.isDeadMultiplexSocket(.commandFailed(status: 255, stderr: "  \n ")))
        #expect(SSHRunner.isDeadMultiplexSocket(
            .commandFailed(status: 255, stderr: "ssh: connect to host x port 22: No route to host")
        ) == false)
        #expect(SSHRunner.isDeadMultiplexSocket(.commandFailed(status: 1, stderr: "")) == false)
        #expect(SSHRunner.isDeadMultiplexSocket(.timedOut(seconds: 30)) == false)
    }

    @Test func aLoginBannerDoesNotBecomeTheErrorMessage() {
        // km's sshd prints "SSH warring: Authorized users only…" to stderr on
        // every connection; the reason a command failed is the line after it.
        let stderr = """
        SSH warring: Authorized users only. All activity may be monitored and reported

        permission denied while trying to connect to the Docker daemon socket
        """
        #expect(SSHRunner.Failure.commandFailed(status: 1, stderr: stderr).errorDescription
            == "permission denied while trying to connect to the Docker daemon socket")
        // Single-line messages are untouched.
        #expect(SSHRunner.Failure.commandFailed(status: 255, stderr: "ssh: connect to host h port 22: Connection refused").errorDescription
            == "ssh: connect to host h port 22: Connection refused")
    }

    @Test func aChangedHostKeyIsExplainedNotTruncated() {
        let warning = """
        @@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
        @    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
        @@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
        Someone could be eavesdropping on you right now (man-in-the-middle attack)!
        Host key verification failed.
        """
        let text = SSHRunner.Failure.commandFailed(status: 255, stderr: warning).errorDescription ?? ""
        #expect(text.contains("Host key changed"), "got \(text)")
        #expect(text.contains("known_hosts"))
    }

    @Test func silentFailuresStillDescribeThemselves() {
        // The server list showed a bare "ssh failed" for every one of these.
        #expect(SSHRunner.Failure.commandFailed(status: 255, stderr: "").errorDescription
            == "ssh exited 255 with no message")
        #expect(SSHRunner.Failure.commandFailed(status: 3, stderr: "").errorDescription
            == "remote command exited 3")
        #expect(SSHRunner.Failure.commandFailed(status: 1, stderr: "boom").errorDescription == "boom")
    }

    @Test func firstTimeHostsAreAcceptedRatherThanPrompted() {
        // The default `ask` policy deadlocks here: with SSH_ASKPASS_REQUIRE=force
        // ssh sends the host key question to the askpass helper, which answers
        // with the password instead of "yes", and the connection hangs until the
        // watchdog fires. Cost 30s per poll against every host not already in
        // known_hosts.
        for credential in [SSHCredential.sshConfigAlias, .agent, .password, .identityFile(path: "/k")] {
            let target = SSHTarget(
                serverID: UUID(), host: "h", port: 22, username: "u", credential: credential
            )
            let arguments = SSHRunner.baseArguments(for: target, controlPath: "/tmp/cp")
            #expect(
                arguments.contains("StrictHostKeyChecking=accept-new"),
                "missing host key policy for \(credential)"
            )
            // accept-new, never `no`: a *changed* key still has to fail.
            #expect(arguments.contains("StrictHostKeyChecking=no") == false)
        }
    }

    @Test func passwordNeverAppearsInArguments() throws {
        let id = UUID()
        try Keychain.savePassword("s3cret!", serverID: id)
        defer { Keychain.deletePassword(serverID: id) }

        let arguments = SSHRunner.baseArguments(for: target(id), controlPath: "/tmp/cp")
        // Putting it on the command line would expose it in `ps`.
        #expect(arguments.contains("s3cret!") == false)
        // BatchMode would suppress the helper along with every other prompt.
        #expect(arguments.contains("BatchMode=yes") == false)
        #expect(arguments.contains("BatchMode=no"))
    }
}

/// Runs the generated helper the way ssh would.
enum SSHRunnerTestSupport {
    static func runHelper(_ path: String, secretPath: String) throws -> String {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: path)
        process.environment = ["SM_SECRET": secretPath]
        let pipe = Pipe()
        process.standardOutput = pipe
        try process.run()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        return String(decoding: data, as: UTF8.self)
    }
}

@Suite("OS detection")
struct OSDetectionTests {

    @Test func linuxIsRecognised() {
        #expect(MetricsCollector.osKind(fromUname: "Linux\n") == .linux)
        #expect(MetricsCollector.osKind(fromUname: "  linux  ") == .linux)
    }

    @Test func anythingElseIsTreatedAsWindows() {
        // Windows shells produce an error on stderr and nothing on stdout, or
        // an unrecognised-command message; both mean "not Linux".
        #expect(MetricsCollector.osKind(fromUname: "") == .windows)
        #expect(MetricsCollector.osKind(fromUname: "'uname' is not recognized") == .windows)
    }

    @Test func otherUnixesFallToWindowsPath() {
        // Deliberate: the collector only has Linux and Windows scripts, and the
        // /proc one would fail on Darwin or BSD anyway.
        #expect(MetricsCollector.osKind(fromUname: "Darwin") == .windows)
    }
}

/// Inflates raw DEFLATE, mirroring what .NET's DeflateStream does remotely.
enum WindowsMetricsTestSupport {
    static func inflate(_ data: Data) -> String? {
        let source = [UInt8](data)
        var destination = [UInt8](repeating: 0, count: 1 << 20)
        let written = compression_decode_buffer(
            &destination, destination.count,
            source, source.count,
            nil, COMPRESSION_ZLIB
        )
        guard written > 0 else { return nil }
        return String(decoding: destination.prefix(written), as: UTF8.self)
    }
}
