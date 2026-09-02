import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("vnStat parsing")
struct VnstatTests {

    /// The shape vnStat 2.x prints: JSON version 2, bytes, one key per series,
    /// `time` only on the sub-day series. Includes a docker bridge so interface
    /// selection has something to skip.
    static let v2 = """
    {"vnstatversion":"2.10","jsonversion":"2","interfaces":[
     {"name":"eth0","alias":"","created":{"date":{"year":2026,"month":1,"day":3}},
      "updated":{"date":{"year":2026,"month":8,"day":26},"time":{"hour":14,"minute":25}},
      "traffic":{"total":{"rx":918000000000,"tx":421000000000},
       "fiveminute":[{"id":1,"date":{"year":2026,"month":8,"day":26},"time":{"hour":14,"minute":15},"rx":1200000,"tx":340000},
                     {"id":2,"date":{"year":2026,"month":8,"day":26},"time":{"hour":14,"minute":20},"rx":900000,"tx":210000}],
       "hour":[{"id":1,"date":{"year":2026,"month":8,"day":26},"time":{"hour":13,"minute":0},"rx":52000000,"tx":18000000},
               {"id":2,"date":{"year":2026,"month":8,"day":26},"time":{"hour":14,"minute":0},"rx":21000000,"tx":7000000}],
       "day":[{"id":1,"date":{"year":2026,"month":8,"day":25},"rx":1300000000,"tx":400000000},
              {"id":2,"date":{"year":2026,"month":8,"day":26},"rx":700000000,"tx":250000000}],
       "month":[{"id":1,"date":{"year":2026,"month":7},"rx":41000000000,"tx":12000000000},
                {"id":2,"date":{"year":2026,"month":8},"rx":33000000000,"tx":9000000000}],
       "year":[{"id":1,"date":{"year":2026},"rx":300000000000,"tx":100000000000}],
       "top":[]}},
     {"name":"docker0","alias":"","traffic":{"total":{"rx":999999999999,"tx":999999999999},
       "fiveminute":[],"hour":[],"day":[{"id":1,"date":{"year":2026,"month":8,"day":26},"rx":1,"tx":1}],
       "month":[],"year":[],"top":[]}}
    ]}
    """

    private func report(_ json: String) throws -> TrafficReport {
        guard case .report(let report)? = VnstatParser.parse(json) else {
            Issue.record("did not parse as a report")
            return TrafficReport()
        }
        return report
    }

    @Test func parsesEverySeriesInBytes() throws {
        let report = try report(Self.v2)
        #expect(report.vnstatVersion == "2.10")
        let eth0 = try #require(report.interfaces.first { $0.name == "eth0" })
        #expect(eth0.totalRx == 918_000_000_000)
        #expect(eth0.buckets(.fiveMinute).count == 2)
        #expect(eth0.buckets(.hour).map(\.rx) == [52_000_000, 21_000_000])
        #expect(eth0.buckets(.day).map(\.tx) == [400_000_000, 250_000_000])
        #expect(eth0.buckets(.month).count == 2)
        #expect(eth0.buckets(.year).first?.total == 400_000_000_000)
    }

    @Test func subDayBucketsCarryTheHostsClock() throws {
        // The axis must show the hour vnStat recorded, so the date is built in
        // UTC from the components as written and read back the same way.
        let eth0 = try #require(try report(Self.v2).interfaces.first)
        let calendar = VnstatParser.calendar
        let slot = try #require(eth0.buckets(.fiveMinute).first)
        #expect(calendar.component(.hour, from: slot.date) == 14)
        #expect(calendar.component(.minute, from: slot.date) == 15)
        let hour = try #require(eth0.buckets(.hour).last)
        #expect(calendar.component(.hour, from: hour.date) == 14)
        #expect(calendar.component(.minute, from: hour.date) == 0)
        let month = try #require(eth0.buckets(.month).first)
        #expect(calendar.component(.month, from: month.date) == 7)
        #expect(calendar.component(.day, from: month.date) == 1, "a month bucket sits on the 1st")
    }

    @Test func bucketsComeOutOldestFirst() throws {
        let shuffled = Self.v2
            .replacingOccurrences(of: #""id":1,"date":{"year":2026,"month":8,"day":25}"#,
                                  with: #""id":1,"date":{"year":2026,"month":8,"day":27}"#)
        let eth0 = try #require(try report(shuffled).interfaces.first)
        let days = eth0.buckets(.day).map { VnstatParser.calendar.component(.day, from: $0.date) }
        #expect(days == [26, 27], "sorted by date, not by the order vnStat printed")
    }

    @Test func thePrimaryInterfaceIsTheBusiestRealOne() throws {
        // docker0 has the larger totals in the fixture and must still lose.
        let report = try report(Self.v2)
        #expect(report.primaryInterface?.name == "eth0")
    }

    @Test func aHostWithOnlyVirtualInterfacesStillPicksOne() throws {
        let onlyBridge = Self.v2.replacingOccurrences(of: #""name":"eth0""#, with: #""name":"br-abc""#)
        let report = try report(onlyBridge)
        #expect(report.primaryInterface != nil, "better a bridge than an empty card")
    }

    @Test func version1KiBAreScaledToBytesAndPluralKeysRead() throws {
        // vnStat 1.x: jsonversion 1, KiB, "hours"/"days"/"months", the hour in `id`.
        let v1 = """
        {"vnstatversion":"1.18","jsonversion":"1","interfaces":[{"id":"eth0","nick":"eth0",
         "traffic":{"total":{"rx":1024,"tx":2048},
          "days":[{"id":0,"date":{"year":2026,"month":8,"day":26},"rx":10,"tx":20}],
          "months":[{"id":0,"date":{"year":2026,"month":8},"rx":100,"tx":200}],
          "hours":[{"id":13,"date":{"year":2026,"month":8,"day":26},"rx":1,"tx":2}],
          "tops":[]}}]}
        """
        let eth0 = try #require(try report(v1).interfaces.first)
        #expect(eth0.totalRx == Int64(1024 * 1024))
        #expect(eth0.buckets(.day).first?.rx == Int64(10 * 1024))
        #expect(eth0.buckets(.month).first?.tx == Int64(200 * 1024))
        let hour = try #require(eth0.buckets(.hour).first)
        #expect(VnstatParser.calendar.component(.hour, from: hour.date) == 13)
        #expect(eth0.buckets(.fiveMinute).isEmpty, "1.x had no five-minute series")
    }

    @Test func theMarkerMeansNotInstalled() {
        #expect(VnstatParser.parse("SM_NO_VNSTAT\n") == .notInstalled)
        // The marker wins even if a shell printed something before it.
        #expect(VnstatParser.parse("bash: warning: locale\nSM_NO_VNSTAT") == .notInstalled)
    }

    @Test func aFreshInstallIsCollectingNotBroken() throws {
        let fresh = """
        {"vnstatversion":"2.10","jsonversion":"2","interfaces":[{"name":"eth0","alias":"",
         "traffic":{"total":{"rx":0,"tx":0},"fiveminute":[],"hour":[],"day":[],"month":[],"year":[],"top":[]}}]}
        """
        let report = try report(fresh)
        #expect(report.isCollecting)
        #expect(report.primaryInterface?.name == "eth0")
    }

    @Test func garbageIsNilRatherThanAnEmptyReport() {
        // An empty report would show "collecting" for a host that actually
        // answered with an error.
        for output in ["", "vnstat: database not found", "{not json", "[]"] {
            #expect(VnstatParser.parse(output) == nil, "accepted \(output.debugDescription)")
        }
    }

    @Test func numbersArriveAsWhateverJSONSerializationChose() {
        // Large byte counts come back as Int64 or Double depending on magnitude;
        // strings appear in some forks. All must read as the same number.
        #expect(VnstatParser.number(Int(5)) == 5)
        #expect(VnstatParser.number(Int64(918_000_000_000)) == 918_000_000_000)
        #expect(VnstatParser.number(Double(1.5e12)) == 1_500_000_000_000)
        #expect(VnstatParser.number(NSNumber(value: 7)) == 7)
        #expect(VnstatParser.number("42") == 42)
        #expect(VnstatParser.number(nil) == 0)
    }
}
