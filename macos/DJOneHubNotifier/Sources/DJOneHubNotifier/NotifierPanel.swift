import AppKit
import SwiftUI

@MainActor
final class NotifierPanel {
    private let panel: NSPanel
    private var hostingView: NSHostingView<NotifierView>?
    private var autoHideWorkItem: DispatchWorkItem?

    init() {
        panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 286, height: 138),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )
        panel.level = .floating
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = false
        panel.hidesOnDeactivate = false
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .stationary]
        panel.isMovableByWindowBackground = true
    }

    func show(
        _ content: PanelContent,
        onReject: @escaping () -> Void,
        onOpen: @escaping () -> Void
    ) {
        autoHideWorkItem?.cancel()
        let size: NSSize
        switch content {
        case .incoming:
            size = NSSize(width: 286, height: 138)
        case .sms, .idle:
            size = NSSize(width: 286, height: 60)
        case .missed, .error:
            size = NSSize(width: 286, height: 76)
        }
        panel.setContentSize(size)
        hostingView = NSHostingView(
            rootView: NotifierView(content: content, onReject: onReject, onOpen: onOpen)
        )
        panel.contentView = hostingView
        position(size: size)
        panel.orderFrontRegardless()

        if !content.isIncomingCall {
            let work = DispatchWorkItem { [weak self] in self?.hide() }
            autoHideWorkItem = work
            DispatchQueue.main.asyncAfter(deadline: .now() + 8, execute: work)
        }
    }

    func hide() {
        autoHideWorkItem?.cancel()
        autoHideWorkItem = nil
        panel.orderOut(nil)
    }

    func saveSnapshot(to url: URL) throws {
        guard let view = panel.contentView else { return }
        view.layoutSubtreeIfNeeded()
        guard let bitmap = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { return }
        view.cacheDisplay(in: view.bounds, to: bitmap)
        guard let data = bitmap.representation(using: .png, properties: [:]) else { return }
        try data.write(to: url, options: .atomic)
    }

    private func position(size: NSSize) {
        guard let visible = (NSScreen.main ?? NSScreen.screens.first)?.visibleFrame else { return }
        panel.setFrameOrigin(NSPoint(
            x: visible.maxX - size.width - 24,
            y: visible.maxY - size.height - 24
        ))
    }
}
