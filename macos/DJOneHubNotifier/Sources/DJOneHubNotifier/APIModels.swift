import Foundation

struct CallRecord: Codable, Equatable, Sendable {
    let id: String
    let index: Int
    let direction: String
    let state: String
    let number: String?
    let startedAt: String
    let updatedAt: String
    let endedAt: String?
    let missed: Bool

    enum CodingKeys: String, CodingKey {
        case id, index, direction, state, number, missed
        case startedAt = "started_at"
        case updatedAt = "updated_at"
        case endedAt = "ended_at"
    }
}

struct CallStatus: Codable, Sendable {
    let active: CallRecord?
    let history: [CallRecord]?
    let polling: Bool
    let pollIntervalSeconds: Int
    let lastPollError: String

    enum CodingKeys: String, CodingKey {
        case active, history, polling
        case pollIntervalSeconds = "poll_interval_s"
        case lastPollError = "last_poll_error"
    }
}

struct SMSMessage: Codable, Equatable, Sendable {
    let sender: String
    let content: String
    let code: String?
    let timestamp: String

    var identity: String {
        "\(sender)\u{0}\(timestamp)\u{0}\(content)"
    }
}

struct OKResponse: Codable, Sendable {
    let ok: Bool
}

struct ModemStatus: Codable, Sendable {
    let operatorName: String?
    let signalDBM: Int?
    let networkMode: String?
    let radioBand: String?
    let radioChannel: Int?
    let simInserted: Bool?

    enum CodingKeys: String, CodingKey {
        case operatorName = "operator"
        case signalDBM = "signal_dbm"
        case networkMode = "network_mode"
        case radioBand = "radio_band"
        case radioChannel = "radio_channel"
        case simInserted = "sim_inserted"
    }
}

struct NetworkTraffic: Codable, Sendable {
    let available: Bool
    let interface: String?
    let rxBytes: UInt64
    let txBytes: UInt64
    let sessionRXBytes: UInt64
    let sessionTXBytes: UInt64
    let sessionTotalBytes: UInt64
    let sampledAtMS: Int64
    let error: String?

    enum CodingKeys: String, CodingKey {
        case available, interface, error
        case rxBytes = "rx_bytes"
        case txBytes = "tx_bytes"
        case sessionRXBytes = "session_rx_bytes"
        case sessionTXBytes = "session_tx_bytes"
        case sessionTotalBytes = "session_total_bytes"
        case sampledAtMS = "sampled_at_ms"
    }
}

struct NetworkCheckResult: Codable, Sendable {
    let ok: Bool
    let summary: String
    let detail: String
    let tunnelActive: Bool?
    let logicalInterface: String?
    let physicalInterface: String?
    let physicalKind: String?
    let physicalName: String?

    enum CodingKeys: String, CodingKey {
        case ok, summary, detail
        case tunnelActive = "tunnel_active"
        case logicalInterface = "logical_interface"
        case physicalInterface = "physical_interface"
        case physicalKind = "physical_kind"
        case physicalName = "physical_name"
    }
}

struct TrafficQuota: Codable, Sendable {
    let month: String
    let baseQuotaBytes: UInt64
    let rolloverBytes: UInt64
    let planTotalBytes: UInt64
    let usedBytes: UInt64
    let remainingBytes: UInt64
    let localTrackedBytes: UInt64
    let trackingStartedAt: String
    let source: String
    let partialEstimate: Bool
    let usedKnown: Bool
    let remainingKnown: Bool
    let lastCalibrationAt: String?
    let queryConfigured: Bool
    let autoQuery: Bool

    enum CodingKeys: String, CodingKey {
        case month, source
        case baseQuotaBytes = "base_quota_bytes"
        case rolloverBytes = "rollover_bytes"
        case planTotalBytes = "plan_total_bytes"
        case usedBytes = "used_bytes"
        case remainingBytes = "remaining_bytes"
        case localTrackedBytes = "local_tracked_bytes"
        case trackingStartedAt = "tracking_started_at"
        case partialEstimate = "partial_estimate"
        case usedKnown = "used_known"
        case remainingKnown = "remaining_known"
        case lastCalibrationAt = "last_calibration_at"
        case queryConfigured = "query_configured"
        case autoQuery = "auto_query"
    }
}

enum APIError: LocalizedError {
    case invalidResponse
    case http(Int)

    var errorDescription: String? {
        switch self {
        case .invalidResponse:
            return "DJOneHub 返回了无效响应"
        case let .http(status):
            return "DJOneHub 请求失败（HTTP \(status)）"
        }
    }
}

struct DJOneHubAPI: Sendable {
    let baseURL: URL

    func callStatus() async throws -> CallStatus {
        try await get(path: "api/voice/calls")
    }

    func messages() async throws -> [SMSMessage] {
        try await get(path: "api/sms")
    }

    func modemStatus() async throws -> ModemStatus {
        try await get(path: "api/status")
    }

    func networkTraffic() async throws -> NetworkTraffic {
        try await get(path: "api/network/traffic")
    }

    func trafficQuota() async throws -> TrafficQuota {
        try await get(path: "api/network/quota")
    }

    func cellularRoute() async throws -> NetworkCheckResult {
        try await post(path: "api/network/check-4g")
    }

    func hangup() async throws -> OKResponse {
        var request = URLRequest(url: baseURL.appendingPathComponent("api/voice/hangup"))
        request.httpMethod = "POST"
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.timeoutInterval = 5
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw APIError.invalidResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.http(http.statusCode)
        }
        return try JSONDecoder().decode(OKResponse.self, from: data)
    }

    private func get<T: Decodable & Sendable>(path: String) async throws -> T {
        var request = URLRequest(url: baseURL.appendingPathComponent(path))
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.timeoutInterval = 5
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw APIError.invalidResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.http(http.statusCode)
        }
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func post<T: Decodable & Sendable>(path: String) async throws -> T {
        var request = URLRequest(url: baseURL.appendingPathComponent(path))
        request.httpMethod = "POST"
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.timeoutInterval = 5
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw APIError.invalidResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.http(http.statusCode)
        }
        return try JSONDecoder().decode(T.self, from: data)
    }
}

enum ServerDate {
    static func parse(_ value: String) -> Date {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: value) {
            return date
        }
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: value) ?? Date()
    }
}
