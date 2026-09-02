import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("History reduction")
struct HistoryReducerTests {

    private func series(count: Int, step: TimeInterval = 5, cpu: (Int) -> Double = { Double($0 % 100) }) -> [MetricSample] {
        let id = UUID()
        let start = Date(timeIntervalSince1970: 1_700_000_000)
        return (0..<count).map { i in
            var snapshot = MetricSnapshot()
            snapshot.cpuPercent = cpu(i)
            snapshot.memoryUsed = Int64(i) * 1_000
            snapshot.memoryTotal = 32_000_000_000
            snapshot.netRxTotal = Int64(i) * 10_000
            snapshot.netRxRate = Double(i)
            return MetricSample(serverID: id, timestamp: start.addingTimeInterval(Double(i) * step), snapshot: snapshot)
        }
    }

    @Test func shortSeriesAreReturnedUntouched() {
        let samples = series(count: 180)
        let reduced = HistoryReducer.reduce(samples, maxPoints: 240)
        #expect(reduced == samples, "every real reading is kept when it fits")
    }

    @Test func aDayOfPollsComesDownToTheLimit() {
        let reduced = HistoryReducer.reduce(series(count: 17_280), maxPoints: 240)
        #expect(reduced.count <= 241, "got \(reduced.count)")
        #expect(reduced.count >= 200, "got \(reduced.count) — far fewer than asked for")
    }

    @Test func timestampsStayMonotonicAndInsideTheRange() {
        let samples = series(count: 5_000)
        let reduced = HistoryReducer.reduce(samples, maxPoints: 240)
        for (a, b) in zip(reduced, reduced.dropFirst()) {
            #expect(a.timestamp < b.timestamp)
        }
        #expect(reduced.first!.timestamp >= samples.first!.timestamp)
        #expect(reduced.last!.timestamp <= samples.last!.timestamp)
    }

    @Test func aBucketIsTheMeanOfItsLevelsAndTheLastOfItsTotals() {
        // 100 samples → 10 buckets of 10. cpu = index, so bucket 0 averages 0…9.
        let reduced = HistoryReducer.reduce(series(count: 100, cpu: { Double($0) }), maxPoints: 10)
        let first = reduced[0]
        #expect(abs(first.cpuPercent - 4.5) < 0.01, "mean of 0…9 is 4.5, got \(first.cpuPercent)")
        #expect(abs(first.netRxRate - 4.5) < 0.01)
        // A mean of running totals would be a number that never happened; the
        // bucket carries the last one.
        #expect(first.netRxTotal == 9 * 10_000)
        #expect(first.memoryTotal == 32_000_000_000)
    }

    @Test func aSpikeSurvivesAsAVisibleBump() {
        // Averaging must not flatten a real event into nothing: a 100% spike
        // over 20 of 5000 polls (100 s) still has to show against a 5% baseline
        // once reduced to 240 points (~21 samples per bucket).
        let samples = series(count: 5_000, cpu: { (2_500...2_519).contains($0) ? 100 : 5 })
        let reduced = HistoryReducer.reduce(samples, maxPoints: 240)
        let peak = reduced.map(\.cpuPercent).max() ?? 0
        #expect(peak > 40, "spike averaged down to \(peak)%")
    }

    @Test func degenerateInputDoesNotDivideByZero() {
        let id = UUID()
        let same = Date()
        let samples = (0..<50).map { _ in MetricSample(serverID: id, timestamp: same, snapshot: MetricSnapshot()) }
        let reduced = HistoryReducer.reduce(samples, maxPoints: 10)
        #expect(reduced.count == 1, "fifty samples at one instant are one point")
        #expect(HistoryReducer.reduce([], maxPoints: 10).isEmpty)
        #expect(HistoryReducer.reduce(series(count: 5), maxPoints: 0).count == 5, "a zero limit disables reduction")
    }
}

@Suite("History reduction in SQLite")
struct SQLHistoryReductionTests {

    private func seeded(count: Int, step: TimeInterval = 5, cpu: (Int) -> Double = { Double($0) }) throws -> (Database, Server, [MetricSample]) {
        let database = try Database(inMemory: true)
        let server = Server(name: "s", host: "h", username: "u", authKind: .agent)
        try database.save(server)
        let start = Date().addingTimeInterval(-Double(count) * step)
        let samples = (0..<count).map { i -> MetricSample in
            var snapshot = MetricSnapshot()
            snapshot.cpuPercent = cpu(i)
            snapshot.memoryUsed = Int64(i) * 1_000
            snapshot.memoryTotal = 32_000_000_000
            snapshot.netRxTotal = Int64(i) * 10_000
            snapshot.netRxRate = Double(i)
            return MetricSample(serverID: server.id, timestamp: start.addingTimeInterval(Double(i) * step), snapshot: snapshot)
        }
        for sample in samples { try database.insert(sample) }
        return (database, server, samples)
    }

    @Test func returnsAtMostTheRequestedNumberOfBuckets() throws {
        let (database, server, samples) = try seeded(count: 5_000)
        let since = samples.first!.timestamp.addingTimeInterval(-1)
        let reduced = try database.reducedSamples(serverID: server.id, since: since, maxPoints: 240)
        #expect(reduced.count <= 241, "got \(reduced.count)")
        #expect(reduced.count >= 200, "got \(reduced.count)")
    }

    @Test func agreesWithTheSwiftReducer() throws {
        // Same data, same bucket count: means must match to rounding, totals
        // must both be the bucket's last/max, timestamps must both sit inside
        // their bucket. They are two implementations of one contract.
        let (database, server, samples) = try seeded(count: 2_400)
        let since = samples.first!.timestamp
        let until = samples.last!.timestamp
        let sql = try database.reducedSamples(serverID: server.id, since: since, until: until, maxPoints: 240)
        let swift = HistoryReducer.reduce(samples, maxPoints: 240)
        #expect(abs(sql.count - swift.count) <= 1, "SQL \(sql.count) vs Swift \(swift.count) buckets")

        let sqlMean = sql.map(\.cpuPercent).reduce(0, +) / Double(sql.count)
        let swiftMean = swift.map(\.cpuPercent).reduce(0, +) / Double(swift.count)
        #expect(abs(sqlMean - swiftMean) < 1.0, "means \(sqlMean) vs \(swiftMean)")
        #expect(sql.last!.netRxTotal == swift.last!.netRxTotal, "totals are the bucket's last reading in both")
        #expect(sql.first!.memoryTotal == 32_000_000_000)
    }

    @Test func timestampsAreMonotonicAndInsideTheRange() throws {
        let (database, server, samples) = try seeded(count: 3_000)
        let since = samples.first!.timestamp
        let reduced = try database.reducedSamples(serverID: server.id, since: since, maxPoints: 240)
        for (a, b) in zip(reduced, reduced.dropFirst()) { #expect(a.timestamp < b.timestamp) }
        // The stored text has millisecond precision and the epoch mean is in
        // whole seconds, so allow a second of slack at the edges.
        #expect(reduced.first!.timestamp >= since.addingTimeInterval(-1))
        #expect(reduced.last!.timestamp <= samples.last!.timestamp.addingTimeInterval(1))
    }

    @Test func aSpikeSurvivesInSQLToo() throws {
        let (database, server, samples) = try seeded(count: 5_000, cpu: { (2_500...2_519).contains($0) ? 100 : 5 })
        let reduced = try database.reducedSamples(serverID: server.id, since: samples.first!.timestamp, maxPoints: 240)
        #expect((reduced.map(\.cpuPercent).max() ?? 0) > 40)
    }

    @Test func aShortRangeKeepsEveryReading() throws {
        let (database, server, samples) = try seeded(count: 100)
        let reduced = try database.reducedSamples(serverID: server.id, since: samples.first!.timestamp, maxPoints: 240)
        #expect(reduced.count == 100, "fewer readings than buckets: each is its own bucket")
        #expect(reduced.map(\.cpuPercent) == samples.map(\.cpuPercent))
    }

    @Test func onlyTheRequestedServerIsRead() throws {
        let (database, server, samples) = try seeded(count: 500, cpu: { _ in 5 })
        let other = Server(name: "o", host: "h", username: "u", authKind: .agent)
        try database.save(other)
        var snapshot = MetricSnapshot()
        snapshot.cpuPercent = 100
        for i in 0..<500 {
            try database.insert(MetricSample(serverID: other.id, timestamp: samples[i].timestamp, snapshot: snapshot))
        }
        let reduced = try database.reducedSamples(serverID: server.id, since: samples.first!.timestamp, maxPoints: 50)
        #expect((reduced.map(\.cpuPercent).max() ?? 0) < 100, "another server's 100% leaked into this one's buckets")
    }
}
