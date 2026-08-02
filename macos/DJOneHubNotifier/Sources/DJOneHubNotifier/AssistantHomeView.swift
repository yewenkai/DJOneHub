import SwiftUI

@MainActor
final class AssistantState: ObservableObject {
    @Published var backendConnected = false
    @Published var callPolling = false
    @Published var smsCount = 0
    @Published var missedCallCount = 0
    @Published var lastUpdate: Date?
    @Published var lastError = "等待连接本地后端"

    func receivedCalls(_ status: CallStatus) {
        backendConnected = true
        callPolling = status.polling
        missedCallCount = status.history?.filter(\.missed).count ?? 0
        lastUpdate = Date()
        lastError = status.lastPollError
    }

    func receivedMessages(_ messages: [SMSMessage]) {
        backendConnected = true
        smsCount = messages.count
        lastUpdate = Date()
        if lastError == "等待连接本地后端" {
            lastError = ""
        }
    }

    func backendUnavailable(_ error: Error) {
        backendConnected = false
        lastError = error.localizedDescription
        lastUpdate = Date()
    }
}

struct AssistantHomeView: View {
    @ObservedObject var state: AssistantState
    let openDJOneHub: () -> Void
    let hideWindow: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack(spacing: 14) {
                Image(systemName: "antenna.radiowaves.left.and.right.circle.fill")
                    .font(.system(size: 38))
                    .foregroundStyle(.blue)
                VStack(alignment: .leading, spacing: 3) {
                    Text("DJOneHub 通知助手")
                        .font(.system(size: 22, weight: .semibold))
                    Text("来电、未接来电与短信通知")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                connectionBadge
            }

            Divider()

            HStack(spacing: 12) {
                metric(title: "通话监听", value: state.callPolling ? "运行中" : "等待后端")
                metric(title: "未接来电", value: "\(state.missedCallCount)")
                metric(title: "短信缓存", value: "\(state.smsCount)")
            }

            VStack(alignment: .leading, spacing: 6) {
                Text("连接信息")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                Text(statusDetail)
                    .font(.system(size: 12))
                    .foregroundStyle(state.backendConnected ? Color.secondary : Color.orange)
                    .lineLimit(2)
                Text("助手只访问 127.0.0.1:7575，不直接操作 USB、串口或 AT 指令。")
                    .font(.system(size: 11))
                    .foregroundStyle(.tertiary)
            }

            Spacer()

            HStack {
                Button("隐藏窗口", action: hideWindow)
                Spacer()
                Button("打开 DJOneHub", action: openDJOneHub)
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(24)
        .frame(minWidth: 480, minHeight: 300)
        .background(Color(nsColor: .windowBackgroundColor))
    }

    private var connectionBadge: some View {
        HStack(spacing: 6) {
            Circle()
                .fill(state.backendConnected ? Color.green : Color.orange)
                .frame(width: 8, height: 8)
            Text(state.backendConnected ? "后端已连接" : "后端未连接")
                .font(.caption.weight(.semibold))
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(.quaternary, in: Capsule())
    }

    private func metric(title: String, value: String) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            Text(title)
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(value)
                .font(.system(size: 18, weight: .semibold, design: .rounded))
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(.quaternary.opacity(0.7), in: RoundedRectangle(cornerRadius: 12))
    }

    private var statusDetail: String {
        if !state.lastError.isEmpty {
            return state.lastError
        }
        if let lastUpdate = state.lastUpdate {
            return "最近同步：\(lastUpdate.formatted(date: .omitted, time: .standard))"
        }
        return "等待首次同步"
    }
}
