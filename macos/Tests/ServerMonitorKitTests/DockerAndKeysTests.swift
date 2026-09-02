import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("Docker summary")
struct DockerSummaryTests {

    @Test func parsesInfoLine() {
        let summary = DockerClient.parseSummary("29.1.3|14|5|3\n")
        #expect(summary.engineVersion == "29.1.3")
        #expect(summary.images == 14)
        #expect(summary.running == 5)
        #expect(summary.stopped == 3)
        #expect(summary.total == 8)
    }

    @Test func ignoresBannerLinesBeforeTheData() {
        // Hosts print login banners on every command; the data is the last
        // line that actually looks like data.
        let output = """
        SSH warning: Authorized users only.
        29.1.3|14|5|3
        """
        #expect(DockerClient.parseSummary(output).engineVersion == "29.1.3")
    }

    @Test func missingDockerYieldsZeroes() {
        let summary = DockerClient.parseSummary("bash: docker: command not found")
        #expect(summary.engineVersion == "")
        #expect(summary.images == 0)
        #expect(summary.total == 0)
    }

    @Test func partialFieldsDoNotCrash() {
        let summary = DockerClient.parseSummary("29.1.3|14")
        #expect(summary.engineVersion == "")
    }
}

@Suite("SSH key manager")
struct SSHKeyManagerTests {

    @Test func acceptsOrdinaryNames() {
        #expect(SSHKeyManager.isValidName("id_ed25519"))
        #expect(SSHKeyManager.isValidName("work-key.2026"))
    }

    @Test func rejectsNamesThatWouldEscapeTheDirectory() {
        // A name reaches the filesystem and ssh-keygen's argv, so traversal and
        // shell metacharacters must never get through.
        #expect(SSHKeyManager.isValidName("../authorized_keys") == false)
        #expect(SSHKeyManager.isValidName("a/b") == false)
        #expect(SSHKeyManager.isValidName("key; rm -rf ~") == false)
        #expect(SSHKeyManager.isValidName("key name") == false)
        #expect(SSHKeyManager.isValidName("") == false)
    }

    @Test func rejectsHiddenAndPublicSuffixes() {
        // A leading dot would hide the key; ".pub" would collide with the
        // public half the generator writes.
        #expect(SSHKeyManager.isValidName(".ssh") == false)
        #expect(SSHKeyManager.isValidName("id_rsa.pub") == false)
    }

    @Test func recognisesPrivateKeyText() {
        let openssh = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----"
        let pem = "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----"
        #expect(SSHKeyManager.looksLikePrivateKey(openssh))
        #expect(SSHKeyManager.looksLikePrivateKey(pem))
        // A public key is not a private key, however similar it looks.
        #expect(SSHKeyManager.looksLikePrivateKey("ssh-ed25519 AAAAC3Nz user@host") == false)
        #expect(SSHKeyManager.looksLikePrivateKey("") == false)
    }

    @Test func importRejectsNonKeyText() {
        #expect(throws: SSHKeyManager.Failure.self) {
            _ = try SSHKeyManager.importKey(text: "hello world", name: "junk")
        }
    }

    @Test func importRejectsBadNamesBeforeTouchingDisk() {
        let key = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----"
        #expect(throws: SSHKeyManager.Failure.self) {
            _ = try SSHKeyManager.importKey(text: key, name: "../escape")
        }
    }
}

@Suite("Machine groups")
struct MachineGroupTests {

    @Test func groupSurvivesItsMembersAndViceVersa() throws {
        let database = try Database(inMemory: true)
        let group = MachineGroup(name: "生产")
        try database.save(group)
        var server = Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .agent)
        server.groupID = group.id
        try database.save(server)

        // Deleting the group must keep the machine, just ungrouped.
        try database.deleteGroup(id: group.id)
        let servers = try database.allServers()
        #expect(servers.count == 1)
        #expect(servers[0].groupID == nil)
        #expect(try database.allGroups().isEmpty)
    }

    @Test func groupsOrderBySortIndexThenName() throws {
        let database = try Database(inMemory: true)
        try database.save(MachineGroup(name: "b", sortIndex: 1))
        try database.save(MachineGroup(name: "a", sortIndex: 2))
        try database.save(MachineGroup(name: "c", sortIndex: 0))
        #expect(try database.allGroups().map(\.name) == ["c", "b", "a"])
    }

    @Test func colourNamesMapToStableChoices() {
        // Stored by name so the colour keeps meaning in both appearances.
        #expect(MachineGroup(name: "x", colorName: "green").color == .green)
        #expect(MachineGroup(name: "x", colorName: "nonsense").color == .blue)
    }
}

@Suite("Docker summary from the metrics batch")
struct DockerBatchSummaryTests {

    @Test func dockerVersionSurvivesTheWidenedFormat() {
        // The batch's docker section now carries four fields. `hasDocker` — and
        // therefore whether the card and the Docker page list this host at all
        // — is derived from this, so it must keep returning just the version.
        #expect(ProcParsers.dockerVersion("29.1.3|14|5|3") == "29.1.3")
        #expect(ProcParsers.dockerVersion("29.1.3|14|5|3\n") == "29.1.3")
        // A host whose docker info only gave a version still works.
        #expect(ProcParsers.dockerVersion("29.1.3") == "29.1.3")
        // And a host without docker still reports none.
        #expect(ProcParsers.dockerVersion("") == "")
        #expect(ProcParsers.dockerVersion("|0|0|0") == "")
    }

    @Test func anErrorMessageIsNotAVersion() {
        // `docker info` on a host where the daemon is down prints a paragraph;
        // treating that as a version would light up the card for a dead engine.
        #expect(ProcParsers.dockerVersion("Cannot connect to the Docker daemon") == "")
        #expect(ProcParsers.dockerVersion("permission denied while trying to connect") == "")
    }

    @Test func pausedContainersAreCounted() {
        // Paused is neither running nor stopped. Without it the donut's parts
        // stop summing to the number printed in its middle.
        let summary = DockerClient.parseSummary("29.1.3|14|5|3|1")
        #expect(summary.running == 5)
        #expect(summary.stopped == 3)
        #expect(summary.paused == 1)
        #expect(summary.total == 9)
    }

    @Test func theOlderFourFieldFormatStillParses() {
        // A host whose facts were stored by an earlier build answers without
        // the paused field; it must parse rather than yield zeroes.
        let summary = DockerClient.parseSummary("29.1.3|14|5|3")
        #expect(summary.engineVersion == "29.1.3")
        #expect(summary.images == 14)
        #expect(summary.running == 5)
        #expect(summary.stopped == 3)
        #expect(summary.paused == 0)
        #expect(summary.total == 8)
    }

    @Test func aMalformedCountIsZeroNotAWholeFailedParse() {
        // The engine prints an empty field rather than 0 in some versions.
        let summary = DockerClient.parseSummary("29.1.3|14||3|")
        #expect(summary.engineVersion == "29.1.3")
        #expect(summary.running == 0)
        #expect(summary.stopped == 3)
    }

    @Test func theBatchSectionParsesIntoASummary() {
        let summary = DockerClient.parseSummary("29.1.3|14|5|3")
        #expect(summary.engineVersion == "29.1.3")
        #expect(summary.images == 14)
        #expect(summary.running == 5)
        #expect(summary.stopped == 3)
        #expect(summary.total == 8)
    }
}

@Suite("Docker resource listings")
struct DockerResourceTests {
    static let separator = "\u{1F}"

    @Test func statsParseAndKeyOnTheShortID() {
        let output = [
            ["a1b2c3d4e5f6", "12.34%", "5.60%", "128MiB / 2GiB"],
            ["0f0e0d0c0b0a", "0.00%", "0.10%", "4MiB / 2GiB"],
        ].map { $0.joined(separator: Self.separator) }.joined(separator: "\n")

        let stats = DockerClient.parseStats(output)
        #expect(stats.count == 2)
        #expect(stats["a1b2c3d4e5f6"]?.cpuPercent == 12.34)
        #expect(stats["a1b2c3d4e5f6"]?.memoryPercent == 5.6)
        #expect(stats["a1b2c3d4e5f6"]?.memoryUsage == "128MiB / 2GiB")
    }

    @Test func netAndBlockIOSplitIntoTheirTwoHalves() {
        let line = [
            "a1b2c3d4e5f6", "54.31%", "80.93%", "1.6GiB / 2GiB",
            "18.5GB / 53.3GB", "17.4TB / 75.9GB",
        ].joined(separator: Self.separator)
        let sample = DockerClient.parseStats(line)["a1b2c3d4e5f6"]
        #expect(sample?.netRx == "18.5GB")
        #expect(sample?.netTx == "53.3GB")
        #expect(sample?.blockRead == "17.4TB")
        #expect(sample?.blockWrite == "75.9GB")
    }

    @Test func theOlderFourFieldStatsFormatStillParses() {
        let line = ["a1b2c3d4e5f6", "1.0%", "2.0%", "52MiB / 2GiB"].joined(separator: Self.separator)
        let sample = DockerClient.parseStats(line)["a1b2c3d4e5f6"]
        #expect(sample?.cpuPercent == 1.0)
        #expect(sample?.netRx == "")
    }

    @Test func containerAgeDropsTheWordUpButKeepsHealth() {
        #expect(DockerContainerTile.age("Up 3 months") == "3 months")
        #expect(DockerContainerTile.age("Up 2 weeks (healthy)") == "2 weeks (healthy)")
        // Not every status starts with "Up".
        #expect(DockerContainerTile.age("Restarting (1) 5 seconds ago") == "Restarting (1) 5 seconds ago")
    }

    @Test func anUnsampledContainerIsZeroNotAParseFailure() {
        // The engine prints "--" for a container it could not sample; dropping
        // the row would make the container look like it had vanished.
        let line = ["abc123456789", "--", "--", "-- / --"].joined(separator: Self.separator)
        let stats = DockerClient.parseStats(line)
        #expect(stats["abc123456789"]?.cpuPercent == 0)
        #expect(stats["abc123456789"]?.memoryPercent == 0)
    }

    @Test func containerShortIDMatchesWhatStatsPrints() {
        // ps --no-trunc gives 64 chars, stats gives 12; the join is on this.
        let container = DockerContainer(
            id: String(repeating: "a", count: 64),
            name: "web", image: "nginx", state: "running", status: "Up 2 days"
        )
        #expect(container.shortID.count == 12)
        #expect(container.shortID == String(repeating: "a", count: 12))
    }

    @Test func danglingImagesAreLabelledByID() {
        let dangling = DockerImage(
            id: "sha256:abcdef012345", repository: "<none>", tag: "<none>",
            size: "1.24GB", created: "2 days ago"
        )
        #expect(dangling.isDangling)
        #expect(dangling.displayName == "sha256:abcde")
        let named = DockerImage(
            id: "x", repository: "nginx", tag: "1.27", size: "50MB", created: "1 week ago"
        )
        #expect(named.isDangling == false)
        #expect(named.displayName == "nginx:1.27")
    }

    @Test func builtInNetworksAreRecognised() {
        for name in ["bridge", "host", "none"] {
            let network = DockerNetwork(id: "i", name: name, driver: "d", scope: "local")
            #expect(network.isBuiltIn, "\(name) is created by the engine")
        }
        let custom = DockerNetwork(id: "i", name: "app_default", driver: "bridge", scope: "local")
        #expect(custom.isBuiltIn == false)
    }
}

@Suite("Compose projects")
struct DockerComposeTests {

    static let realOutput = """
    [{"Name":"stock-a","Status":"running(2)","ConfigFiles":"/root/stock-a/docker-compose.yml"},\
    {"Name":"wow","Status":"running(2)","ConfigFiles":"/root/wow/docker-compose.yml"},\
    {"Name":"wower","Status":"exited(2), running(1)","ConfigFiles":"/opt/wower/docker-compose.yml"}]
    """

    @Test func parsesProjectsInNameOrder() {
        let projects = DockerClient.parseComposeProjects(Self.realOutput)
        #expect(projects.map(\.name) == ["stock-a", "wow", "wower"])
        #expect(projects[0].directory == "/root/stock-a")
    }

    @Test func mixedStatusSplitsIntoItsStates() {
        // "exited(2), running(1)" is one string covering two facts.
        let projects = DockerClient.parseComposeProjects(Self.realOutput)
        let wower = projects[2]
        #expect(wower.counts.map(\.state) == ["exited", "running"])
        #expect(wower.counts.map(\.count) == [2, 1])
        #expect(wower.runningCount == 1)
        #expect(wower.isRunning, "one running service still makes the project up")
    }

    @Test func aFullyStoppedProjectIsNotRunning() {
        let stopped = DockerComposeProject(
            name: "old", status: "exited(3)", configFiles: "/srv/old/compose.yaml"
        )
        #expect(stopped.isRunning == false)
        #expect(stopped.runningCount == 0)
        #expect(stopped.directory == "/srv/old")
    }

    @Test func aWarningAboveTheJsonDoesNotBreakParsing() {
        // The engine prints deprecation notices on stdout on some versions.
        let noisy = """
        WARN[0000] /root/a/docker-compose.yml: `version` is obsolete
        [{"Name":"a","Status":"running(1)","ConfigFiles":"/root/a/docker-compose.yml"}]
        """
        #expect(DockerClient.parseComposeProjects(noisy).map(\.name) == ["a"])
    }

    @Test func composeV1HostsYieldNothingRatherThanCrashing() {
        // Compose v1 was a separate binary with no `ls`; the command fails and
        // the tab should simply be empty.
        for output in ["", "docker: 'compose' is not a docker command.", "[]", "not json"] {
            #expect(DockerClient.parseComposeProjects(output).isEmpty)
        }
    }
}
