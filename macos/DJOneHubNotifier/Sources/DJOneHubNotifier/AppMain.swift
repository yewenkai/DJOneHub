import AppKit
import Foundation
import SwiftUI

@main
enum DJOneHubNotifierMain {
    @MainActor
    static func main() {
        if CommandLine.arguments.contains("--self-test") {
            SelfTest.run()
            return
        }
        let utilityLaunch = CommandLine.arguments.contains("--health-check") ||
            CommandLine.arguments.contains("--snapshot") ||
            CommandLine.arguments.contains("--preview")
        if !utilityLaunch,
           let bundleIdentifier = Bundle.main.bundleIdentifier,
           let existing = NSRunningApplication.runningApplications(withBundleIdentifier: bundleIdentifier)
               .first(where: { $0.processIdentifier != ProcessInfo.processInfo.processIdentifier })
        {
            existing.activate(options: [.activateAllWindows])
            return
        }
        let app = NSApplication.shared
        let delegate = AppDelegate(arguments: CommandLine.arguments)
        app.delegate = delegate
        app.setActivationPolicy(CommandLine.arguments.contains("--health-check") ? .accessory : .regular)
        app.run()
    }
}

enum SelfTest {
    @MainActor
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
        precondition(TrafficText.speed(1_048_576) == "1.0 MB/s")
        let state = AssistantState()
        state.receivedTraffic(NetworkTraffic(
            available: true, interface: "en19", rxBytes: 1_000, txBytes: 2_000,
            sessionRXBytes: 0, sessionTXBytes: 0, sessionTotalBytes: 0,
            sampledAtMS: 1_000, error: nil
        ))
        state.receivedTraffic(NetworkTraffic(
            available: true, interface: "en19", rxBytes: 2_024, txBytes: 2_512,
            sessionRXBytes: 1_024, sessionTXBytes: 512, sessionTotalBytes: 1_536,
            sampledAtMS: 2_000, error: nil
        ))
        precondition(state.downloadBytesPerSecond == 1_024)
        precondition(state.uploadBytesPerSecond == 512)
        for connected in [false, true] {
            for signalLevel in 0...4 {
                let image = AppDelegate.cellularStatusImage(
                    signalLevel: signalLevel,
                    connected: connected
                )
                precondition(image.size.width > 0 && image.size.height > 0)
                precondition(image.isTemplate)
                precondition(image.accessibilityDescription?.isEmpty == false)
            }
        }
        print("DJOneHubNotifier self-test passed")
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private let api: DJOneHubAPI
    private let webURL: URL
    private let panel = NotifierPanel()
    private let assistantState = AssistantState()
    private let previewMode: String?
    private let snapshotPath: String?
    private let healthCheck: Bool
    private let backgroundLaunch: Bool

    private var mainWindow: NSWindow?
    private var statusItem: NSStatusItem?
    private let statusPopover = NSPopover()
    private var callTimer: Timer?
    private var smsTimer: Timer?
    private var trafficTimer: Timer?
    private var quotaTimer: Timer?
    private var modemTimer: Timer?
    private var routeTimer: Timer?
    private var lastActiveCallID: String?
    private var seenCallHistoryIDs = Set<String>()
    private var seenMessageIDs = Set<String>()
    private var initializedCalls = false
    private var initializedMessages = false
    private var callPollInFlight = false
    private var smsPollInFlight = false
    private var trafficPollInFlight = false
    private var quotaPollInFlight = false
    private var modemPollInFlight = false
    private var routePollInFlight = false

    init(arguments: [String]) {
        let baseURL = Self.argumentValue("--base-url", in: arguments)
            .flatMap(URL.init(string:))
            ?? URL(string: "http://127.0.0.1:7575/")!
        api = DJOneHubAPI(baseURL: baseURL)
        webURL = baseURL
        previewMode = Self.argumentValue("--preview", in: arguments)
        snapshotPath = Self.argumentValue("--snapshot", in: arguments)
        healthCheck = arguments.contains("--health-check")
        backgroundLaunch = arguments.contains("--background")
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

        configureApplicationMenu()
        configureApplicationIcon()
        configureMainWindow()
        configureStatusItem()
        if !backgroundLaunch {
            showMainWindow()
        }
        if let snapshotPath {
            DispatchQueue.main.asyncAfter(deadline: .now() + 2.2) { [weak self] in
                guard let self else { return }
                try? self.saveMainWindowSnapshot(to: URL(fileURLWithPath: snapshotPath))
                NSApplication.shared.terminate(nil)
            }
        }

        callTimer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.pollCalls() }
        }
        smsTimer = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.pollMessages() }
        }
        trafficTimer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.pollTraffic() }
        }
        quotaTimer = Timer.scheduledTimer(withTimeInterval: 30, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.pollTrafficQuota() }
        }
        modemTimer = Timer.scheduledTimer(withTimeInterval: 15, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.pollModemStatus() }
        }
        routeTimer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.pollCellularRoute() }
        }
        Task { await pollTraffic() }
        Task { await pollTrafficQuota() }
        Task { await pollModemStatus() }
        Task { await pollCellularRoute() }
        Task { await pollCalls() }
        Task { await pollMessages() }
    }

    func applicationWillTerminate(_ notification: Notification) {
        callTimer?.invalidate()
        smsTimer?.invalidate()
        trafficTimer?.invalidate()
        quotaTimer?.invalidate()
        modemTimer?.invalidate()
        routeTimer?.invalidate()
        statusPopover.performClose(nil)
        if let statusItem {
            NSStatusBar.system.removeStatusItem(statusItem)
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if !flag {
            showMainWindow()
        }
        return true
    }

    private func runHealthCheck() async {
        do {
            let traffic = try await api.networkTraffic()
            let modem = try await api.modemStatus()
            let messages = try await api.messages()
            print(
                "health-check passed: traffic=\(traffic.available) " +
                    "network=\(modem.networkMode ?? "--") smsCount=\(messages.count)"
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

        let status: CallStatus
        do {
            status = try await api.callStatus()
        } catch {
            return
        }
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

        let messages: [SMSMessage]
        do {
            messages = try await api.messages()
            assistantState.receivedMessages(messages)
        } catch {
            return
        }
        if !initializedMessages {
            initializedMessages = true
            seenMessageIDs = Set(messages.map(\.identity))
            return
        }
        guard let newest = messages.first(where: { !seenMessageIDs.contains($0.identity) }) else { return }
        seenMessageIDs.formUnion(messages.map(\.identity))
        showMessage(newest)
    }

    private func pollTraffic() async {
        guard !trafficPollInFlight else { return }
        trafficPollInFlight = true
        defer { trafficPollInFlight = false }
        do {
            assistantState.receivedTraffic(try await api.networkTraffic())
            updateStatusItem()
        } catch {
            assistantState.backendUnavailable(error)
            updateStatusItem()
        }
    }

    private func pollTrafficQuota() async {
        guard !quotaPollInFlight else { return }
        quotaPollInFlight = true
        defer { quotaPollInFlight = false }
        guard let quota = try? await api.trafficQuota() else { return }
        assistantState.receivedQuota(quota)
    }

    private func pollModemStatus() async {
        guard !modemPollInFlight else { return }
        modemPollInFlight = true
        defer { modemPollInFlight = false }
        guard let status = try? await api.modemStatus() else { return }
        assistantState.receivedModem(status)
        updateStatusItem()
    }

    private func pollCellularRoute() async {
        guard !routePollInFlight else { return }
        routePollInFlight = true
        defer { routePollInFlight = false }
        guard let result = try? await api.cellularRoute() else { return }
        assistantState.receivedRoute(result)
        updateStatusItem()
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

    private func configureMainWindow() {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 560, height: 620),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "DJOneHub 蜂窝网络"
        window.isReleasedWhenClosed = false
        window.minSize = NSSize(width: 520, height: 540)
        window.setFrameAutosaveName("DJOneHubNotifierMainWindow")
        window.contentView = NSHostingView(rootView: AssistantHomeView(
            state: assistantState,
            openDJOneHub: openDJOneHub,
            hideWindow: { [weak window] in window?.orderOut(nil) }
        ))
        window.center()
        mainWindow = window
    }

    private func showMainWindow() {
        statusPopover.performClose(nil)
        mainWindow?.makeKeyAndOrderFront(nil)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    private func configureStatusItem() {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        item.autosaveName = "DJOneHubCellularStatus"
        item.button?.target = self
        item.button?.action = #selector(toggleStatusPopover)
        item.button?.imagePosition = .imageOnly
        statusItem = item

        statusPopover.behavior = .transient
        statusPopover.animates = true
        statusPopover.contentSize = NSSize(width: 380, height: 520)
        statusPopover.contentViewController = NSHostingController(rootView: MenuBarDashboardView(
            state: assistantState,
            openDJOneHub: openDJOneHub,
            showMainWindow: showMainWindow
        ))
        updateStatusItem()
    }

    @objc private func toggleStatusPopover() {
        guard let button = statusItem?.button else { return }
        if statusPopover.isShown {
            statusPopover.performClose(nil)
        } else {
            statusPopover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            NSApplication.shared.activate(ignoringOtherApps: false)
        }
    }

    private func updateStatusItem() {
        statusItem?.button?.image = Self.cellularStatusImage(
            signalLevel: assistantState.signalLevel,
            connected: assistantState.backendConnected
        )
        statusItem?.button?.toolTip = assistantState.backendConnected
            ? "\(assistantState.operatorName) · \(assistantState.routeSummary) · ↓ \(TrafficText.speed(assistantState.downloadBytesPerSecond))"
            : "DJOneHub 后端未连接"
    }

    fileprivate static func cellularStatusImage(signalLevel: Int, connected: Bool) -> NSImage {
        let description = connected
            ? "DJOneHub 蜂窝网络，信号 \(signalLevel) 格"
            : "DJOneHub 后端未连接"
        let symbolName = connected
            ? "antenna.radiowaves.left.and.right"
            : "antenna.radiowaves.left.and.right.slash"
        let baseImage = NSImage(
            systemSymbolName: symbolName,
            accessibilityDescription: description
        ) ?? NSImage(
            systemSymbolName: "network",
            accessibilityDescription: description
        ) ?? NSImage(size: NSSize(width: 18, height: 18))
        let configuration = NSImage.SymbolConfiguration(pointSize: 15, weight: .medium)
        let image = baseImage.withSymbolConfiguration(configuration) ?? baseImage
        image.isTemplate = true
        image.accessibilityDescription = description
        return image
    }

    private func saveMainWindowSnapshot(to url: URL) throws {
        guard let view = mainWindow?.contentView else { return }
        view.layoutSubtreeIfNeeded()
        guard let bitmap = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { return }
        view.cacheDisplay(in: view.bounds, to: bitmap)
        guard let data = bitmap.representation(using: .png, properties: [:]) else { return }
        try data.write(to: url, options: .atomic)
    }

    private func configureApplicationIcon() {
        guard let symbol = NSImage(
            systemSymbolName: "antenna.radiowaves.left.and.right.circle.fill",
            accessibilityDescription: "DJOneHub 通知助手"
        ) else { return }
        symbol.size = NSSize(width: 128, height: 128)
        NSApplication.shared.applicationIconImage = symbol
    }

    private func configureApplicationMenu() {
        let mainMenu = NSMenu()
        let appItem = NSMenuItem()
        mainMenu.addItem(appItem)

        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "关于 DJOneHub 通知助手", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "退出 DJOneHub 通知助手", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu
        NSApplication.shared.mainMenu = mainMenu
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
