import Foundation

/// Where an address is, as far as a public geolocation service knows.
public struct GeoInfo: Sendable, Equatable {
    public var ip: String = ""
    public var countryCode: String = ""
    public var country: String = ""
    public var region: String = ""
    public var city: String = ""
    public var organisation: String = ""

    public init() {}

    public var isEmpty: Bool { countryCode.isEmpty && city.isEmpty && organisation.isEmpty }

    /// "San Jose, California" — the parts that exist, in the order people read.
    public var place: String {
        [city, region].filter { !$0.isEmpty }.joined(separator: ", ")
    }
}

/// Looks an address up with ipwho.is.
///
/// Deliberately **not** wired into the poll loop. Resolving a location means
/// telling a third party which servers this user runs, which is a disclosure
/// they should make on purpose, so it happens only when someone presses the
/// button on the IP card. The result is cached for the session and the country
/// is written back to the server so the flag survives without asking again.
public struct GeoLookup: Sendable {
    /// Injected so tests never touch the network.
    public typealias Fetch = @Sendable (URL) async throws -> Data

    private let fetch: Fetch

    public init(fetch: @escaping Fetch = GeoLookup.urlSessionFetch) {
        self.fetch = fetch
    }

    public static let urlSessionFetch: Fetch = { url in
        var request = URLRequest(url: url)
        request.timeoutInterval = 12
        let (data, response) = try await URLSession.shared.data(for: request)
        if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
            throw Failure.badStatus(http.statusCode)
        }
        return data
    }

    public enum Failure: LocalizedError {
        case badStatus(Int)
        case notFound(String)
        case malformed

        public var errorDescription: String? {
            switch self {
            case .badStatus(let code): return "Lookup service returned \(code)"
            case .notFound(let reason): return reason
            case .malformed: return "Lookup service sent something unreadable"
            }
        }
    }

    public func lookup(_ address: String) async throws -> GeoInfo {
        let trimmed = address.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty,
              let encoded = trimmed.addingPercentEncoding(withAllowedCharacters: .urlHostAllowed),
              let url = URL(string: "https://ipwho.is/\(encoded)")
        else { throw Failure.notFound("No address to look up") }
        return try Self.parse(try await fetch(url))
    }

    static func parse(_ data: Data) throws -> GeoInfo {
        guard let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw Failure.malformed
        }
        // The service answers 200 with success:false for a private or bogus
        // address, so the status code alone does not say whether this worked.
        if let success = root["success"] as? Bool, success == false {
            throw Failure.notFound((root["message"] as? String) ?? "Address not found")
        }
        var info = GeoInfo()
        info.ip = root["ip"] as? String ?? ""
        info.countryCode = (root["country_code"] as? String ?? "").uppercased()
        info.country = root["country"] as? String ?? ""
        info.region = root["region"] as? String ?? ""
        info.city = root["city"] as? String ?? ""
        if let connection = root["connection"] as? [String: Any] {
            info.organisation = connection["org"] as? String
                ?? connection["isp"] as? String ?? ""
        }
        return info
    }

    /// Addresses a public service can say nothing useful about, so the card can
    /// explain that rather than firing a request that will fail.
    public static func isPrivate(_ address: String) -> Bool {
        let text = address.trimmingCharacters(in: .whitespaces)
        if text == "localhost" || text.hasPrefix("127.") || text == "::1" { return true }
        if text.hasPrefix("10.") || text.hasPrefix("192.168.") || text.hasPrefix("169.254.") {
            return true
        }
        // 172.16.0.0 – 172.31.255.255
        let parts = text.split(separator: ".")
        if parts.count == 4, parts[0] == "172", let second = Int(parts[1]),
           (16...31).contains(second) {
            return true
        }
        return false
    }
}
