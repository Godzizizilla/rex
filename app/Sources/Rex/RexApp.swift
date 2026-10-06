import AppKit
import Carbon.HIToolbox
import SwiftUI
import UserNotifications

@main
struct RexApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        MenuBarExtra {
            MenuBarContent(state: delegate.state)
        } label: {
            MenuBarLabel(state: delegate.state)
        }
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    static private(set) weak var shared: AppDelegate?

    let state = AppState()
    private(set) var island: IslandController!
    private(set) var mainWindow: MainWindowController!
    private let hotkeys = HotKeys()

    override init() {
        super.init()
        AppDelegate.shared = self
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        signal(SIGPIPE, SIG_IGN) // writing to a dead rexd must not kill the app
        Settings.registerDefaults()
        NSApp.setActivationPolicy(.accessory)

        island = IslandController(state: state)
        mainWindow = MainWindowController(state: state)
        state.onChange = { [weak self] in self?.island.update() }
        state.onNewPending = { r in Notifier.shared.notifyPending(r) }
        state.onDecided = { r in Notifier.shared.remove(r.id) }
        state.onNotice = { n in Notifier.shared.notifyNotice(n) }
        Notifier.shared.setup(state: state, island: island)
        applyHotkeys()
        state.start()
        if CommandLine.arguments.contains("--window") {
            mainWindow.show()
        }
    }

    /// Opening Rex again (Finder, Spotlight, `open -a Rex`) shows the main window.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        mainWindow.show()
        return true
    }

    func applicationWillTerminate(_ notification: Notification) {
        state.shutdown()
    }

    func applyHotkeys() {
        hotkeys.unregisterAll()
        guard UserDefaults.standard.bool(forKey: Settings.hotkeys) else { return }
        let mods = UInt32(controlKey | optionKey)
        hotkeys.register(key: UInt32(kVK_ANSI_Y), modifiers: mods) { [weak self] in self?.approveCurrent() }
        hotkeys.register(key: UInt32(kVK_ANSI_N), modifiers: mods) { [weak self] in self?.island.beginReject() }
        hotkeys.register(key: UInt32(kVK_ANSI_R), modifiers: mods) { [weak self] in self?.island.toggle() }
    }

    private func approveCurrent() {
        guard let r = island.selectedRequest ?? state.pending.first, r.isPending else { return }
        if r.isRisky {
            island.toggle() // high-risk: show it, approval needs press-and-hold
            return
        }
        state.approve(r.id)
    }
}

/// A single SF Symbol: changing the label's view structure (e.g. adding a
/// count Text) at runtime can loop AppKit's status item layout.
struct MenuBarLabel: View {
    let state: AppState

    var body: some View {
        Image(systemName: symbol)
    }

    private var symbol: String {
        if !state.connected { return "exclamationmark.shield" }
        if !state.pending.isEmpty { return "exclamationmark.shield.fill" }
        if state.daemon.auto.active { return "bolt.shield.fill" }
        return "checkmark.shield"
    }
}

struct MenuBarContent: View {
    let state: AppState

    var body: some View {
        if !state.connected {
            Text("rexd 未运行")
            if let e = state.daemonError { Text(e) }
            Button("重启 rexd") { state.restartDaemon() }
            Divider()
        }

        let n = state.pending.count
        Button(n == 0 ? "没有待审批的请求" : "\(n) 个待审批请求…") {
            AppDelegate.shared?.island.toggle()
        }
        .disabled(n == 0)

        Divider()

        let auto = state.daemon.auto
        if auto.active {
            Text("自动审批至 \(Fmt.time.string(from: (auto.until ?? 0).date))（通过 \(auto.approved)，挂起 \(auto.held)）")
            Button("停止自动审批") { state.stopAuto() }
        } else {
            Menu("临时自动审批") {
                ForEach([15, 30, 60, 120], id: \.self) { m in
                    Button(m < 60 ? "\(m) 分钟" : "\(m / 60) 小时") { state.startAuto(minutes: m) }
                }
                Divider()
                Button("更多选项…") { AppDelegate.shared?.mainWindow.show(section: .auto) }
            }
        }

        if !state.forwards.isEmpty {
            Menu("端口转发（\(state.forwards.count)）") {
                ForEach(state.forwards) { f in
                    Button("关闭 127.0.0.1:\(f.localPort) → \(f.remoteHost):\(f.remotePort) @ \(f.server)") {
                        state.closeForward(f.id)
                    }
                }
            }
        }

        Divider()
        Button("打开 Rex…") { AppDelegate.shared?.mainWindow.show() }
            .keyboardShortcut(",")
        Button("退出 Rex") { NSApp.terminate(nil) }
            .keyboardShortcut("q")
    }
}

// MARK: - Global hotkeys (Carbon, no accessibility permission needed)

@MainActor
final class HotKeys {
    private static var actions: [UInt32: @MainActor () -> Void] = [:]
    private static var handlerInstalled = false
    private var refs: [EventHotKeyRef] = []
    private var nextID: UInt32 = 1

    func register(key: UInt32, modifiers: UInt32, action: @escaping @MainActor () -> Void) {
        Self.installHandler()
        let id = nextID
        nextID += 1
        var ref: EventHotKeyRef?
        let hotKeyID = EventHotKeyID(signature: OSType(0x5245_5844), id: id) // 'REXD'
        if RegisterEventHotKey(key, modifiers, hotKeyID, GetApplicationEventTarget(), 0, &ref) == noErr, let ref {
            refs.append(ref)
            Self.actions[id] = action
        }
    }

    func unregisterAll() {
        refs.forEach { UnregisterEventHotKey($0) }
        refs.removeAll()
        Self.actions.removeAll()
    }

    private static func installHandler() {
        guard !handlerInstalled else { return }
        handlerInstalled = true
        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        InstallEventHandler(GetApplicationEventTarget(), { _, event, _ in
            var hk = EventHotKeyID()
            GetEventParameter(event, EventParamName(kEventParamDirectObject), EventParamType(typeEventHotKeyID),
                              nil, MemoryLayout<EventHotKeyID>.size, nil, &hk)
            let id = hk.id
            DispatchQueue.main.async {
                MainActor.assumeIsolated { HotKeys.actions[id]?() }
            }
            return noErr
        }, 1, &spec, nil, nil)
    }
}

// MARK: - System notifications (optional; the island is the primary UI)

@MainActor
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    static let shared = Notifier()
    private weak var state: AppState?
    private weak var island: IslandController?

    private var available: Bool { Bundle.main.bundleIdentifier != nil }
    private var enabled: Bool { available && UserDefaults.standard.bool(forKey: Settings.notifications) }

    func setup(state: AppState, island: IslandController) {
        self.state = state
        self.island = island
        guard available else { return }
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        let approve = UNNotificationAction(identifier: "APPROVE", title: "批准")
        let reject = UNTextInputNotificationAction(identifier: "REJECT", title: "拒绝…",
                                                   textInputButtonTitle: "拒绝", textInputPlaceholder: "拒绝原因")
        center.setNotificationCategories([
            UNNotificationCategory(identifier: "REQUEST", actions: [approve, reject], intentIdentifiers: []),
            UNNotificationCategory(identifier: "RISKY", actions: [reject], intentIdentifiers: []),
        ])
    }

    func requestAuthorization() {
        guard available else { return }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    func notifyPending(_ r: RexRequest) {
        guard enabled else { return }
        let c = UNMutableNotificationContent()
        c.title = "Rex · \(r.server)"
        c.subtitle = r.desc
        c.body = r.summary
        c.categoryIdentifier = r.isRisky ? "RISKY" : "REQUEST"
        c.userInfo = ["id": r.id]
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "req-\(r.id)", content: c, trigger: nil))
    }

    func notifyNotice(_ n: Notice) {
        guard enabled else { return }
        let c = UNMutableNotificationContent()
        c.title = "Rex"
        c.body = n.text
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: n.id.uuidString, content: c, trigger: nil))
    }

    func remove(_ id: Int) {
        guard available else { return }
        UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: ["req-\(id)"])
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                            withCompletionHandler completionHandler: @escaping () -> Void) {
        let id = response.notification.request.content.userInfo["id"] as? Int
        let action = response.actionIdentifier
        let text = (response as? UNTextInputNotificationResponse)?.userText
        Task { @MainActor in
            guard let id, let state = self.state else { return }
            switch action {
            case "APPROVE": state.approve(id)
            case "REJECT": state.reject(id, feedback: text ?? "")
            default: self.island?.toggle()
            }
        }
        completionHandler()
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                            withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner])
    }
}
