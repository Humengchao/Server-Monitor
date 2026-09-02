import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("Geo lookup")
struct GeoLookupTests {

    static let googleResponse = """
    {"ip":"8.8.8.8","success":true,"country":"United States","country_code":"US",
     "region":"California","city":"San Jose",
     "connection":{"asn":15169,"org":"Google LLC","isp":"Google LLC"}}
    """

    private func lookup(_ body: String) -> GeoLookup {
        GeoLookup { _ in Data(body.utf8) }
    }

    @Test func parsesCountryPlaceAndOrganisation() async throws {
        let info = try await lookup(Self.googleResponse).lookup("8.8.8.8")
        #expect(info.countryCode == "US")
        #expect(info.country == "United States")
        #expect(info.place == "San Jose, California")
        #expect(info.organisation == "Google LLC")
        #expect(info.isEmpty == false)
    }

    @Test func successFalseIsAFailureDespiteHTTP200() async {
        // The service answers 200 with success:false for an address it cannot
        // place, so the status code alone would read as a good result.
        let body = #"{"success":false,"message":"Reserved range"}"#
        await #expect(throws: GeoLookup.Failure.self) {
            _ = try await lookup(body).lookup("10.0.0.1")
        }
    }

    @Test func aMissingConnectionBlockIsNotAFailure() async throws {
        let info = try await lookup(#"{"success":true,"country_code":"jp","city":"Tokyo"}"#)
            .lookup("1.1.1.1")
        #expect(info.countryCode == "JP", "country code is upper-cased for the flag")
        #expect(info.organisation.isEmpty)
        #expect(info.place == "Tokyo")
    }

    @Test func garbageIsReportedRatherThanSilentlyEmpty() async {
        await #expect(throws: GeoLookup.Failure.self) {
            _ = try await lookup("<html>502</html>").lookup("1.1.1.1")
        }
    }

    @Test func anEmptyAddressNeverReachesTheNetwork() async {
        let never = GeoLookup { _ in
            Issue.record("a blank address must not be sent anywhere")
            return Data()
        }
        await #expect(throws: GeoLookup.Failure.self) { _ = try await never.lookup("   ") }
    }

    @Test func privateAddressesAreRecognisedBeforeAnyRequest() {
        for address in [
            "127.0.0.1", "localhost", "::1", "10.1.2.3", "192.168.9.132",
            "169.254.1.1", "172.16.0.1", "172.31.255.254",
        ] {
            #expect(GeoLookup.isPrivate(address), "\(address) is private")
        }
        for address in ["8.8.8.8", "1.1.1.1", "172.32.0.1", "172.15.0.1", "example.com"] {
            #expect(GeoLookup.isPrivate(address) == false, "\(address) is routable")
        }
    }

    @Test func theFlagComesFromWhicheverCodeIsKnown() {
        // The card prefers a looked-up country but falls back to the one typed
        // into the editor, so both have to produce a flag.
        #expect(Format.flag("US") == "🇺🇸")
        #expect(Format.flag("cn") == "🇨🇳")
        #expect(Format.flag("") == "")
        #expect(Format.flag("XYZ") == "")
    }
}
