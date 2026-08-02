import AppKit
import Foundation

@main
enum DJOneHubNotifierMain {
    @MainActor
    static func main() {
        if CommandLine.arguments.contains("--self-test") {
            SelfTest.run()
            return
        }
        let app = NSApplication.shared
        let delegate = AppDelegate(arguments: CommandLine.arguments)
        app.delegate = delegate
        app.setActivationPolicy(.accessory)
        app.run()
    }
}

enum SelfTest {
    static func run() {
        precondition(NotificationText.displayNumber("  ") == "未知号码")
        let message = SMSMessage(
            sender: "10086",
            content: "您的验证码是 482913",
            code: "482913",
            timestamp: "2026-08-02T12:00:00Z"
        )
        precondition(NotificationText.smsPreview(message) == "验证码 482913")
        let longMessage = SMSMessage(
            sender: "10086",
            content: "第一行\n第二行以及一段很长很长的短信正文",
            code: nil,
            timestamp: "2026-08-02T12:00:00Z"
        )
        precondition(NotificationText.smsPreview(longMessage, limit: 8) == "第一行 第二行以…")
        precondition(ServerDate.parse("2026-08-02T12:00:00.123456+08:00").timeIntervalSince1970 > 0)
        print("DJOneHubNotifier self-test passed")
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private let api: DJOneHubAPI
    private let webURL: URL
    private let panel = NotifierPanel()
    private let previewMode: String?
    private let snapshotPath: String?
    private let healthCheck: Bool

    private var callTimer: Timer?
    private var smsTimer: Timer?
    private var lastActiveCallID: String?
    private var seenCallHistoryIDs = Set<String>()
    private var seenMessageIDs = Set<String>()
    private var initializedCalls = false
    private var initializedMessages = false
    private var callPollInFlight = false
    private var smsPollInFlight = false

    init(arguments: [String]) {
        let baseURL = Self.argumentValue("--base-url", in: arguments)
            .flatMap(URL.init(string:))
            ?? URL(string: "http://127.0.0.1:7575/")!
        api = DJOneHubAPI(baseURL: baseURL)
        webURL = baseURL
        previewMode = Self.argumentValue("--preview", in: arguments)
        snapshotPath = Self.argumentValue("--snapshot", in: arguments)
        healthCheck = arguments.contains("--health-check")
        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        if healthCheck {
            Task { await runHealthCheck() }
            return
        }
        if let previewMode {
            showPreview(previewMode)
            if let snapshotPath {
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { [weak self] in
                    guard let self else { return }
                    try? self.panel.saveSnapshot(to: URL(fileURLWithPath: snapshotPath))
                    NSApplication.shared.terminate(nil)
                }
            }
            return
        }

        callTimer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.pollCalls() }
        }
        smsTimer = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.pollMessages() }
        }
        Task {
            await pollCalls()
            await pollMessages()
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        callTimer?.invalidate()
        smsTimer?.invalidate()
    }

    private func runHealthCheck() async {
        do {
            let calls = try await api.callStatus()
            let messages = try await api.messages()
            print(
                "health-check passed: callPolling=\(calls.polling) " +
                    "callHistory=\(calls.history?.count ?? 0) smsCount=\(messages.count)"
            )
            NSApplication.shared.terminate(nil)
        } catch {
            fputs("health-check failed: \(error.localizedDescription)\n", stderr)
            exit(1)
        }
    }

    private func pollCalls() async {
        guard !callPollInFlight else { return }
        callPollInFlight = true
        defer { callPollInFlight = false }

        guard let status = try? await api.callStatus() else { return }
        let history = status.history ?? []
        if !initializedCalls {
            initializedCalls = true
            seenCallHistoryIDs = Set(history.map(\.id))
        }

        if let active = status.active,
           active.direction == "incoming",
           active.state == "incoming" || active.state == "waiting"
        {
            if active.id != lastActiveCallID {
                showIncoming(active)
            }
            lastActiveCallID = active.id
        } else {
            if lastActiveCallID != nil {
                panel.hide()
            }
            lastActiveCallID = nil
        }

        if let missed = history.first(where: { $0.missed && !seenCallHistoryIDs.contains($0.id) }) {
            seenCallHistoryIDs.insert(missed.id)
            showMissed(missed)
        }
        seenCallHistoryIDs.formUnion(history.map(\.id))
    }

    private func pollMessages() async {
        guard !smsPollInFlight else { return }
        smsPollInFlight = true
        defer { smsPollInFlight = false }

        guard let messages = try? await api.messages() else { return }
        if !initializedMessages {
            initializedMessages = true
            seenMessageIDs = Set(messages.map(\.identity))
            return
        }
        guard let newest = messages.first(where: { !seenMessageIDs.contains($0.identity) }) else { return }
        seenMessageIDs.formUnion(messages.map(\.identity))
        showMessage(newest)
    }

    private func showIncoming(_ call: CallRecord) {
        NSSound(named: "Glass")?.play()
        panel.show(
            .incoming(
                number: NotificationText.displayNumber(call.number),
                startedAt: ServerDate.parse(call.startedAt)
            ),
            onReject: { [weak self] in
                Task { @MainActor in await self?.hangup() }
            },
            onOpen: openDJOneHub
        )
    }

    private func showMissed(_ call: CallRecord) {
        panel.show(
            .missed(
                number: NotificationText.displayNumber(call.number),
                startedAt: ServerDate.parse(call.startedAt)
            ),
            onReject: {},
            onOpen: openDJOneHub
        )
    }

    private func showMessage(_ message: SMSMessage) {
        NSSound(named: "Glass")?.play()
        panel.show(
            .sms(
                sender: message.sender.isEmpty ? "未知发送方" : message.sender,
                preview: NotificationText.smsPreview(message),
                code: message.code
            ),
            onReject: {},
            onOpen: openDJOneHub
        )
    }

    private func hangup() async {
        do {
            _ = try await api.hangup()
            panel.hide()
        } catch {
            panel.show(
                .error(message: "拒接失败：\(error.localizedDescription)"),
                onReject: {},
                onOpen: openDJOneHub
            )
        }
    }

    private func openDJOneHub() {
        NSWorkspace.shared.open(webURL)
    }

    private func showPreview(_ mode: String) {
        switch mode {
        case "sms":
            panel.show(
                .sms(sender: "10086", preview: "验证码 482913", code: "482913"),
                onReject: {},
                onOpen: openDJOneHub
            )
        case "missed":
            panel.show(
                .missed(number: "192 •••• 1510", startedAt: Date()),
                onReject: {},
                onOpen: openDJOneHub
            )
        default:
            panel.show(
                .incoming(number: "192 •••• 1510", startedAt: Date()),
                onReject: {},
                onOpen: openDJOneHub
            )
        }
    }

    private static func argumentValue(_ flag: String, in arguments: [String]) -> String? {
        guard let index = arguments.firstIndex(of: flag), arguments.indices.contains(index + 1) else {
            return nil
        }
        return arguments[index + 1]
    }
}
