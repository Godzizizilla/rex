import AppKit
import Observation

/// Everything the UI shows, fed by control events from rexd.
@MainActor
@Observable
final class AppState {
    var requests: [Int: RexRequest] = [:]
    var daemon = DaemonState.empty
    var config: ConfigView?
    var connected = false
    var daemonError: String?
    var notice: Notice?

    /// Called after every change, for AppKit parts (island panel, notifications).
    @ObservationIgnored var onChange: (() -> Void)?
    @ObservationIgnored var onNewPending: ((RexRequest) -> Void)?
    @ObservationIgnored var onDecided: ((RexRequest) -> Void)?
    @ObservationIgnored var onNotice: ((Notice) -> Void)?

    @ObservationIgnored private let client = DaemonClient()
    @ObservationIgnored private var quitting = false
    @ObservationIgnored private var restartScheduled = false

    var pending: [RexRequest] {
        requests.values.filter(\.isPending).sorted { $0.id < $1.id }
    }

    var recent: [RexRequest] {
        requests.values.sorted { $0.id > $1.id }
    }

    var forwards: [Forward] {
        (daemon.forwards ?? []).sorted { $0.id < $1.id }
    }

    var serverNames: [String] {
        (config?.servers ?? []).map(\.name)
    }

    // MARK: - daemon lifecycle

    func start() {
        client.onEvent = { [weak self] ev in self?.apply(ev) }
        client.onExit = { [weak self] msg in self?.daemonExited(msg) }
        launch()
    }

    func restartDaemon() {
        if client.isRunning {
            client.stop()
        } else {
            launch()
        }
    }

    func shutdown() {
        quitting = true
        client.stop()
    }

    private func launch() {
        restartScheduled = false
        do {
            try client.start()
            daemonError = nil
        } catch {
            daemonError = error.localizedDescription
            connected = false
            onChange?()
        }
    }

    private func daemonExited(_ message: String) {
        connected = false
        daemonError = message.isEmpty ? "rexd 已退出" : message
        // Requests can no longer be decided; drop them from the island.
        for (id, r) in requests where r.isPending {
            var c = r
            c.status = "cancelled"
            requests[id] = c
        }
        onChange?()
        guard !quitting, !restartScheduled else { return }
        restartScheduled = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 3) { [weak self] in
            guard let self, !self.quitting, !self.client.isRunning else { return }
            self.launch()
        }
    }

    // MARK: - events

    private func apply(_ ev: ControlEvent) {
        switch ev.type {
        case "hello":
            connected = true
            daemonError = nil
            requests = Dictionary((ev.requests ?? []).map { ($0.id, $0) }, uniquingKeysWith: { _, b in b })
            if let s = ev.state { daemon = s }
            config = ev.config
            for r in pending { onNewPending?(r) }
        case "request":
            guard let r = ev.request else { break }
            let old = requests[r.id]
            requests[r.id] = r
            if r.isPending && old == nil {
                onNewPending?(r)
            } else if old?.isPending == true && !r.isPending {
                onDecided?(r)
            }
            trim()
        case "state":
            if let s = ev.state { daemon = s }
        case "config":
            config = ev.config
        case "history":
            for r in ev.requests ?? [] where requests[r.id] == nil {
                requests[r.id] = r
            }
        case "notice":
            show(Notice(text: ev.message ?? ""))
        case "result":
            if ev.ok != true, let e = ev.error {
                show(Notice(text: e, isError: true))
            }
        default:
            break
        }
        onChange?()
    }

    private func show(_ n: Notice) {
        notice = n
        onNotice?(n)
        DispatchQueue.main.asyncAfter(deadline: .now() + 6) { [weak self] in
            if self?.notice == n {
                self?.notice = nil
                self?.onChange?()
            }
        }
    }

    private func trim() {
        guard requests.count > 600 else { return }
        let keep = Set(recent.prefix(500).map(\.id) + pending.map(\.id))
        requests = requests.filter { keep.contains($0.key) }
    }

    // MARK: - commands

    func approve(_ id: Int) {
        client.send(ControlCmd(type: "approve", id: id))
    }

    func reject(_ id: Int, feedback: String) {
        client.send(ControlCmd(type: "reject", id: id, feedback: feedback))
    }

    func approveWhitelist(_ id: Int, indices: [Int]) {
        client.send(ControlCmd(type: "approve_whitelist", id: id, indices: indices))
    }

    func startAuto(minutes: Int, servers: [String] = []) {
        client.send(ControlCmd(type: "auto_start", minutes: minutes, servers: servers))
    }

    func stopAuto() {
        client.send(ControlCmd(type: "auto_stop"))
    }

    func closeForward(_ id: Int) {
        client.send(ControlCmd(type: "forward_close", id: id))
    }

    func addRule(_ rule: WhitelistRule) {
        client.send(ControlCmd(type: "whitelist_add", rule: rule))
    }

    func removeRule(at index: Int) {
        client.send(ControlCmd(type: "whitelist_remove", index: index))
    }

    func reloadConfig() {
        client.send(ControlCmd(type: "reload_config"))
    }

    func loadHistory(limit: Int = 500) {
        client.send(ControlCmd(type: "history", limit: limit))
    }
}
