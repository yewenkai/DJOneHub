import Foundation

enum PanelContent: Equatable {
    case idle
    case incoming(number: String, startedAt: Date)
    case sms(sender: String, preview: String, code: String?)
    case missed(number: String, startedAt: Date)
    case error(message: String)

    var isIncomingCall: Bool {
        if case .incoming = self {
            return true
        }
        return false
    }
}

enum NotificationText {
    static func displayNumber(_ number: String?) -> String {
        let trimmed = number?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? "未知号码" : trimmed
    }

    static func smsPreview(_ message: SMSMessage, limit: Int = 48) -> String {
        if let code = message.code, !code.isEmpty {
            return "验证码 \(code)"
        }
        let singleLine = message.content
            .replacingOccurrences(of: "\r", with: " ")
            .replacingOccurrences(of: "\n", with: " ")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard singleLine.count > limit else {
            return singleLine
        }
        return String(singleLine.prefix(limit)) + "…"
    }
}
