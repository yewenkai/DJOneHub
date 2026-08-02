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
