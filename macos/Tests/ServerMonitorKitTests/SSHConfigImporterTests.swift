import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("SSH config importer")
struct SSHConfigImporterTests {

    @Test func parsesHostBlocks() {
        let config = """
        # a comment
        Host km
            HostName 36.138.248.251
            User root
            Port 22
            IdentityFile ~/.ssh/id_rsa
            IdentitiesOnly yes

        Host cn2
            HostName 23.251.32.157
            User root
        """
        let hosts = SSHConfigImporter.parse(config)
        #expect(hosts.count == 2)
        #expect(hosts[0].alias == "km")
        #expect(hosts[0].hostName == "36.138.248.251")
        #expect(hosts[0].port == 22)
        #expect(hosts[0].identityFile?.hasSuffix("/.ssh/id_rsa") == true)
        #expect(hosts[0].identityFile?.hasPrefix("~") == false, "tilde must be expanded")
        // Missing User/Port fall back to sensible defaults.
        #expect(hosts[1].alias == "cn2")
        #expect(hosts[1].user == "root")
        #expect(hosts[1].port == 22)
        #expect(hosts[1].identityFile == nil)
    }

    @Test func skipsWildcardOnlyEntries() {
        // `Host *` describes defaults, not a machine worth importing.
        let config = """
        Host *
            ServerAliveInterval 60

        Host web
            HostName example.com
        """
        let hosts = SSHConfigImporter.parse(config)
        #expect(hosts.count == 1)
        #expect(hosts[0].alias == "web")
    }

    @Test func acceptsEqualsSeparatorAndOddSpacing() {
        let config = """
        Host  spaced
              HostName=10.0.0.5
              Port  = 2222
              User    deploy
        """
        let hosts = SSHConfigImporter.parse(config)
        #expect(hosts.count == 1)
        #expect(hosts[0].hostName == "10.0.0.5")
        #expect(hosts[0].port == 2222)
        #expect(hosts[0].user == "deploy")
    }

    @Test func aliasIsTheAddressWhenHostNameIsAbsent() {
        let hosts = SSHConfigImporter.parse("Host example.com\n    User admin")
        #expect(hosts.count == 1)
        #expect(hosts[0].hostName == "example.com")
        #expect(hosts[0].user == "admin")
    }

    @Test func readsTheRealUserConfigWithoutCrashing() {
        // Not an assertion about contents: it must simply never throw or hang
        // on whatever the machine actually has.
        _ = SSHConfigImporter.discover()
    }
}
