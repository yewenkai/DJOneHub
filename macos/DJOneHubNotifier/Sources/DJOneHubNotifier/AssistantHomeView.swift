import SwiftUI

@MainActor
final class AssistantState: ObservableObject {
    @Published var backendConnected = false
    @Published var operatorName = "蜂窝网络"
    @Published var signalDBM: Int?
    @Published var networkMode = "--"
    @Published var radioBand = "--"
    @Published var interfaceName = "--"
    @Published var usingCellularRoute = false
    @Published var tunnelActive = false
    @Published var physicalRouteKind = "unknown"
    @Published var routeSummary = "正在检查默认出口"
    @Published var downloadBytesPerSecond = 0.0
    @Published var uploadBytesPerSecond = 0.0
    @Published var showMenuBarSpeed = UserDefaults.standard.object(forKey: "showMenuBarSpeed") as? Bool ?? true {
        didSet {
            UserDefaults.standard.set(showMenuBarSpeed, forKey: "showMenuBarSpeed")
        }
    }
    @Published var sessionRXBytes: UInt64 = 0
    @Published var sessionTXBytes: UInt64 = 0
    @Published var sessionTotalBytes: UInt64 = 0
    @Published var trafficQuota: TrafficQuota?
    @Published var messages: [SMSMessage] = []
    @Published var lastUpdate: Date?
    @Published var lastError = "等待连接本地后端"

    private var previousTraffic: NetworkTraffic?

    var signalLevel: Int {
        guard let signalDBM else { return 0 }
        if signalDBM >= -65 { return 4 }
        if signalDBM >= -75 { return 3 }
        if signalDBM >= -85 { return 2 }
        return 1
    }

    func receivedTraffic(_ traffic: NetworkTraffic) {
        backendConnected = true
        interfaceName = traffic.interface ?? "--"
        sessionRXBytes = traffic.sessionRXBytes
        sessionTXBytes = traffic.sessionTXBytes
        sessionTotalBytes = traffic.sessionTotalBytes
        lastUpdate = Date()
        lastError = traffic.error ?? ""

        guard traffic.available,
              let previousTraffic,
              previousTraffic.available,
              previousTraffic.interface == traffic.interface,
              traffic.sampledAtMS > previousTraffic.sampledAtMS,
              traffic.rxBytes >= previousTraffic.rxBytes,
              traffic.txBytes >= previousTraffic.txBytes
        else {
            downloadBytesPerSecond = 0
            uploadBytesPerSecond = 0
            self.previousTraffic = traffic
            return
        }

        let elapsed = Double(traffic.sampledAtMS - previousTraffic.sampledAtMS) / 1_000
        downloadBytesPerSecond = Double(traffic.rxBytes - previousTraffic.rxBytes) / elapsed
        uploadBytesPerSecond = Double(traffic.txBytes - previousTraffic.txBytes) / elapsed
        self.previousTraffic = traffic
    }

    func receivedModem(_ status: ModemStatus) {
        backendConnected = true
        operatorName = status.operatorName?.nonEmpty ?? "蜂窝网络"
        signalDBM = status.signalDBM
        networkMode = status.networkMode?.nonEmpty ?? "--"
        radioBand = status.radioBand?.nonEmpty ?? "--"
        lastUpdate = Date()
    }

    func receivedQuota(_ quota: TrafficQuota) {
        backendConnected = true
        trafficQuota = quota
        lastUpdate = Date()
    }

    func receivedRoute(_ result: NetworkCheckResult) {
        backendConnected = true
        usingCellularRoute = result.ok
        tunnelActive = result.tunnelActive ?? false
        physicalRouteKind = result.physicalKind ?? (result.ok ? "cellular" : "unknown")
        routeSummary = result.summary
        lastUpdate = Date()
    }

    func cellularRouteUnavailable(_ error: Error) {
        usingCellularRoute = false
        routeSummary = "默认出口状态未知"
        lastError = error.localizedDescription
        lastUpdate = Date()
    }

    func receivedMessages(_ items: [SMSMessage]) {
        backendConnected = true
        messages = items
        lastUpdate = Date()
        if lastError == "等待连接本地后端" {
            lastError = ""
        }
    }

    func backendUnavailable(_ error: Error) {
        backendConnected = false
        downloadBytesPerSecond = 0
        uploadBytesPerSecond = 0
        lastError = error.localizedDescription
        lastUpdate = Date()
    }
}

struct AssistantHomeView: View {
    @ObservedObject var state: AssistantState
    let openDJOneHub: () -> Void
    let hideWindow: () -> Void

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                NetworkDashboardHeader(state: state)
                LiveSpeedView(state: state)
                SessionTrafficView(state: state)
                MonthlyQuotaView(state: state)
                NetworkDetailView(state: state)
                RecentMessagesView(messages: state.messages, limit: 5)
                HStack {
                    Button("隐藏窗口", action: hideWindow)
                    Spacer()
                    Button("打开完整控制面板", action: openDJOneHub)
                        .keyboardShortcut(.defaultAction)
                }
            }
            .padding(22)
        }
        .frame(minWidth: 520, minHeight: 540)
        .background(Color(nsColor: .windowBackgroundColor))
    }
}

struct MenuBarDashboardView: View {
    @ObservedObject var state: AssistantState
    let openDJOneHub: () -> Void
    let showMainWindow: () -> Void

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                NetworkDashboardHeader(state: state)
                LiveSpeedView(state: state)
                SessionTrafficView(state: state)
                MonthlyQuotaView(state: state)
                NetworkDetailView(state: state)
                RecentMessagesView(messages: state.messages, limit: 3)
                Divider()
                Toggle(
                    "在菜单栏显示模块实时网速",
                    isOn: $state.showMenuBarSpeed
                )
                .toggleStyle(.switch)
                HStack {
                    Button("打开应用", action: showMainWindow)
                    Spacer()
                    Button("完整面板", action: openDJOneHub)
                }
            }
            .padding(16)
        }
        .frame(width: 380, height: 520)
        .background(Color(nsColor: .windowBackgroundColor))
    }
}

private struct NetworkDashboardHeader: View {
    @ObservedObject var state: AssistantState

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "antenna.radiowaves.left.and.right.circle.fill")
                .font(.system(size: 34))
                .foregroundStyle(state.backendConnected ? Color.blue : Color.gray)
            VStack(alignment: .leading, spacing: 2) {
                Text(state.operatorName)
                    .font(.headline)
                Text("\(state.networkMode) · \(state.radioBand)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            HStack(spacing: 5) {
                Circle()
                    .fill(routeColor)
                    .frame(width: 7, height: 7)
                Text(state.routeSummary)
                    .font(.caption.weight(.semibold))
            }
            .padding(.horizontal, 9)
            .padding(.vertical, 5)
            .background(.quaternary, in: Capsule())
        }
    }

    private var routeColor: Color {
        switch state.physicalRouteKind {
        case "cellular": return .green
        case "wifi": return .blue
        case "ethernet": return .cyan
        default: return .orange
        }
    }
}

private struct LiveSpeedView: View {
    @ObservedObject var state: AssistantState

    var body: some View {
        HStack(spacing: 12) {
            speedMetric(
                title: "实时下载",
                symbol: "arrow.down",
                value: TrafficText.speed(state.downloadBytesPerSecond),
                color: .blue
            )
            speedMetric(
                title: "实时上传",
                symbol: "arrow.up",
                value: TrafficText.speed(state.uploadBytesPerSecond),
                color: .green
            )
        }
    }

    private func speedMetric(title: String, symbol: String, value: String, color: Color) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            Label(title, systemImage: symbol)
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(value)
                .font(.system(size: 21, weight: .semibold, design: .rounded))
                .foregroundStyle(color)
                .lineLimit(1)
                .minimumScaleFactor(0.72)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(13)
        .background(.quaternary.opacity(0.7), in: RoundedRectangle(cornerRadius: 12))
    }
}

private struct SessionTrafficView: View {
    @ObservedObject var state: AssistantState

    var body: some View {
        HStack(spacing: 12) {
            trafficMetric("本次总流量", state.sessionTotalBytes)
            trafficMetric("下载", state.sessionRXBytes)
            trafficMetric("上传", state.sessionTXBytes)
        }
    }

    private func trafficMetric(_ title: String, _ bytes: UInt64) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            Text(TrafficText.bytes(bytes))
                .font(.system(size: 14, weight: .semibold, design: .rounded))
                .lineLimit(1)
                .minimumScaleFactor(0.7)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct MonthlyQuotaView: View {
    @ObservedObject var state: AssistantState

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack {
                Text("本月流量").font(.headline)
                Spacer()
                Text(sourceText).font(.caption).foregroundStyle(.secondary)
            }
            if let quota = state.trafficQuota {
                HStack(alignment: .firstTextBaseline) {
                    VStack(alignment: .leading, spacing: 3) {
                        Text("剩余").font(.caption).foregroundStyle(.secondary)
                        Text(TrafficText.quotaBytes(quota.remainingBytes))
                            .font(.system(size: 20, weight: .semibold, design: .rounded))
                            .foregroundStyle(.blue)
                    }
                    Spacer()
                    VStack(alignment: .trailing, spacing: 3) {
                        Text("已用").font(.caption).foregroundStyle(.secondary)
                        Text((quota.usedKnown ? "" : "本机 ") + TrafficText.quotaBytes(quota.usedBytes))
                            .font(.system(size: 15, weight: .semibold, design: .rounded))
                    }
                }
                ProgressView(value: progress(quota))
                    .tint(.blue)
                HStack {
                    Text("套餐 \(TrafficText.quotaBytes(quota.planTotalBytes))")
                    if quota.rolloverBytes > 0 {
                        Text("含结转 \(TrafficText.quotaBytes(quota.rolloverBytes))")
                    }
                    Spacer()
                    if quota.partialEstimate {
                        Text("仅统计本机启动后的 4G 流量")
                    }
                }
                .font(.caption2)
                .foregroundStyle(.secondary)
            } else {
                Text("正在读取月度流量…")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .background(.quaternary.opacity(0.45), in: RoundedRectangle(cornerRadius: 10))
    }

    private var sourceText: String {
        guard let quota = state.trafficQuota else { return "--" }
        if quota.source == "operator_sms" { return "10099 已校准" }
        if quota.source == "manual" { return quota.partialEstimate ? "部分手动校准" : "手动校准" }
        return "本机估算"
    }

    private func progress(_ quota: TrafficQuota) -> Double {
        guard quota.planTotalBytes > 0 else { return 0 }
        return min(1, Double(quota.usedBytes) / Double(quota.planTotalBytes))
    }
}

private struct NetworkDetailView: View {
    @ObservedObject var state: AssistantState

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack {
                Label(state.signalDBM.map { "\($0) dBm" } ?? "信号 --", systemImage: "cellularbars")
                Spacer()
                Text("网卡 \(state.interfaceName)")
            }
            .font(.caption)
            Text(state.routeSummary)
                .font(.caption)
                .foregroundStyle(.secondary)
            if !state.lastError.isEmpty {
                Text(state.lastError)
                    .font(.caption)
                    .foregroundStyle(.orange)
            } else if let lastUpdate = state.lastUpdate {
                Text("最近更新 \(lastUpdate.formatted(date: .omitted, time: .standard))")
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
            }
        }
        .padding(12)
        .background(.quaternary.opacity(0.45), in: RoundedRectangle(cornerRadius: 10))
    }
}

private struct RecentMessagesView: View {
    let messages: [SMSMessage]
    let limit: Int

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack {
                Text("最近短信").font(.headline)
                Spacer()
                Text("\(messages.count) 条缓存").font(.caption).foregroundStyle(.secondary)
            }
            if messages.isEmpty {
                Text("暂无短信")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, minHeight: 44, alignment: .center)
            } else {
                ForEach(Array(messages.prefix(limit).enumerated()), id: \.element.identity) { index, message in
                    if index > 0 { Divider() }
                    VStack(alignment: .leading, spacing: 3) {
                        HStack {
                            Text(message.sender.isEmpty ? "未知发送方" : message.sender)
                                .font(.caption.weight(.semibold))
                            Spacer()
                            Text(ServerDate.parse(message.timestamp), style: .time)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                        }
                        Text(message.content)
                            .font(.caption)
                            .lineLimit(2)
                            .textSelection(.enabled)
                        if let code = message.code, !code.isEmpty {
                            Text("验证码 \(code)")
                                .font(.caption2.weight(.semibold))
                                .foregroundStyle(.blue)
                        }
                    }
                }
            }
        }
    }
}

enum TrafficText {
    static func menuBarLines(upload: Double, download: Double, active: Bool) -> String {
        guard active else { return "↑ --\n↓ --" }
        return "↑\(menuBarSpeed(upload))\n↓\(menuBarSpeed(download))"
    }

    static func menuBarSpeed(_ bytesPerSecond: Double) -> String {
        guard bytesPerSecond > 0 else { return "0K" }
        if bytesPerSecond >= 1_048_576 {
            let mebibytes = bytesPerSecond / 1_048_576
            if mebibytes >= 999.5 {
                let gibibytes = mebibytes / 1_024
                return String(format: gibibytes >= 9.95 ? "%.0fG" : "%.1fG", min(gibibytes, 999))
            }
            return String(format: mebibytes >= 9.95 ? "%.0fM" : "%.1fM", mebibytes)
        }
        return String(format: "%.0fK", bytesPerSecond / 1_024)
    }

    static func speed(_ bytesPerSecond: Double) -> String {
        guard bytesPerSecond > 0 else { return "0 KB/s" }
        if bytesPerSecond >= 1_048_576 {
            return String(format: "%.1f MB/s", bytesPerSecond / 1_048_576)
        }
        return String(format: "%.0f KB/s", bytesPerSecond / 1_024)
    }

    static func bytes(_ value: UInt64) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(clamping: value), countStyle: .file)
    }

    static func quotaBytes(_ value: UInt64) -> String {
        let gib = Double(value) / 1_073_741_824
        if gib >= 1 {
            return String(format: gib.rounded() == gib ? "%.0f GB" : "%.2f GB", gib)
        }
        let mib = Double(value) / 1_048_576
        return String(format: mib >= 10 ? "%.0f MB" : "%.1f MB", mib)
    }
}

private extension String {
    var nonEmpty: String? {
        isEmpty ? nil : self
    }
}
