import AppKit
import ServiceManagement
import SwiftUI

enum Settings {
    static let notifications = "notificationsEnabled"
    static let sound = "soundEnabled"
    static let hotkeys = "hotkeysEnabled"

    static func registerDefaults() {
        UserDefaults.standard.register(defaults: [notifications: false, sound: true, hotkeys: true])
    }
}

@MainActor
final class MainWindowController {
    private var window: NSWindow?
    private let state: AppState

    init(state: AppState) { self.state = state }

    func show(section: MainSection = .requests) {
        if window == nil {
            let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1000, height: 660),
                             styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
                             backing: .buffered, defer: false)
            w.title = "Rex"
            w.isReleasedWhenClosed = false
            w.center()
            w.setFrameAutosaveName("RexMain")
            window = w
        }
        window?.contentViewController = NSHostingController(rootView: MainView(state: state, initial: section))
        NSApp.activate(ignoringOtherApps: true)
        window?.makeKeyAndOrderFront(nil)

        // Development aid, see IslandController.snapshotForDevelopment.
        if let dir = ProcessInfo.processInfo.environment["REX_SNAPSHOT_DIR"] {
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { [weak self] in
                guard let view = self?.window?.contentView,
                      let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { return }
                view.cacheDisplay(in: view.bounds, to: rep)
                try? rep.representation(using: .png, properties: [:])?
                    .write(to: URL(fileURLWithPath: dir).appendingPathComponent("main.png"))
            }
        }
    }
}

enum MainSection: String, CaseIterable, Identifiable {
    case requests, whitelist, auto, forwards, settings
    var id: String { rawValue }

    var title: String {
        switch self {
        case .requests: return "请求"
        case .whitelist: return "白名单"
        case .auto: return "自动审批"
        case .forwards: return "端口转发"
        case .settings: return "设置"
        }
    }

    var symbol: String {
        switch self {
        case .requests: return "list.bullet.rectangle"
        case .whitelist: return "checkmark.shield"
        case .auto: return "bolt"
        case .forwards: return "point.3.connected.trianglepath.dotted"
        case .settings: return "gearshape"
        }
    }
}

struct MainView: View {
    let state: AppState
    @State var initial: MainSection
    @State private var section: MainSection? = .requests

    var body: some View {
        NavigationSplitView {
            List(MainSection.allCases, selection: $section) { s in
                HStack {
                    Label(s.title, systemImage: s.symbol)
                    Spacer()
                    if s == .requests && !state.pending.isEmpty {
                        Text("\(state.pending.count)").font(.caption.bold())
                            .padding(.horizontal, 6).background(Capsule().fill(Palette.warn)).foregroundStyle(.black)
                    }
                    if s == .auto && state.daemon.auto.active {
                        Image(systemName: "bolt.fill").foregroundStyle(Palette.auto)
                    }
                    if s == .forwards && !state.forwards.isEmpty {
                        Text("\(state.forwards.count)").font(.caption).foregroundStyle(.secondary)
                    }
                }
                .tag(s)
            }
            .navigationSplitViewColumnWidth(min: 170, ideal: 190)
            .safeAreaInset(edge: .bottom) { DaemonStatus(state: state).padding(10) }
        } detail: {
            switch section ?? .requests {
            case .requests: RequestsView(state: state)
            case .whitelist: WhitelistView(state: state)
            case .auto: AutoApproveView(state: state)
            case .forwards: ForwardsView(state: state)
            case .settings: SettingsView(state: state)
            }
        }
        .onAppear { section = initial }
        .frame(minWidth: 820, minHeight: 520)
    }
}

struct DaemonStatus: View {
    let state: AppState

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Circle().fill(state.connected ? Palette.ok : Palette.danger).frame(width: 8, height: 8)
                Text(state.connected ? "rexd 运行中" : "rexd 未连接").font(.caption)
                Spacer()
            }
            if let e = state.daemonError, !state.connected {
                Text(e).font(.caption2).foregroundStyle(.secondary).lineLimit(4)
                Button("重启 rexd") { state.restartDaemon() }.controlSize(.small)
            }
        }
    }
}

// MARK: - Requests

struct RequestsView: View {
    let state: AppState
    @State private var selection: Int?
    @State private var filter = "all"

    private var items: [RexRequest] {
        state.recent.filter { r in
            switch filter {
            case "pending": return r.isPending
            case "auto": return r.approvedBy == "auto"
            case "failed": return ["rejected", "timeout", "cancelled", "failed"].contains(r.status) || (r.exitCode ?? 0) != 0
            default: return true
            }
        }
    }

    var body: some View {
        HSplitView {
            VStack(spacing: 0) {
                Picker("", selection: $filter) {
                    Text("全部").tag("all")
                    Text("待审批").tag("pending")
                    Text("自动审批").tag("auto")
                    Text("异常").tag("failed")
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .padding(8)
                List(items, selection: $selection) { r in
                    RequestRow(request: r).tag(r.id)
                }
            }
            .frame(minWidth: 300, idealWidth: 360, maxWidth: 480)

            Group {
                if let id = selection, let r = state.requests[id] {
                    RequestDetail(request: r, state: state)
                } else {
                    Text("选择一个请求").foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
            .frame(minWidth: 400, maxWidth: .infinity, maxHeight: .infinity)
        }
        .navigationTitle("请求")
        .toolbar {
            Button { state.loadHistory() } label: { Label("加载更早的历史", systemImage: "clock.arrow.circlepath") }
        }
        .onAppear(perform: selectDefault)
        .onChange(of: state.requests.count) { selectDefault() }
    }

    private func selectDefault() {
        if selection == nil {
            selection = state.pending.first?.id ?? state.recent.first?.id
        }
    }
}

struct RequestRow: View {
    let request: RexRequest

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: request.kindSymbol).foregroundStyle(.secondary).frame(width: 16)
            VStack(alignment: .leading, spacing: 2) {
                HStack {
                    Text(request.server).font(.caption.bold())
                    Text("#\(request.id)").font(.caption).foregroundStyle(.secondary)
                    Spacer()
                    StatusBadge(request: request)
                }
                Text(request.desc).font(.callout).lineLimit(1)
                Text(request.summary).font(.system(size: 11, design: .monospaced)).foregroundStyle(.secondary).lineLimit(1)
            }
        }
        .padding(.vertical, 2)
    }
}

struct StatusBadge: View {
    let request: RexRequest

    var color: Color {
        switch request.status {
        case "pending": return Palette.warn
        case "completed": return (request.exitCode ?? 0) == 0 ? Palette.ok : Palette.warn
        case "running", "approved": return .blue
        default: return .secondary
        }
    }

    var body: some View {
        HStack(spacing: 3) {
            if request.approvedBy == "auto" { Image(systemName: "bolt.fill").foregroundStyle(Palette.auto) }
            Text(request.statusLabel)
        }
        .font(.caption2.bold())
        .foregroundStyle(color)
    }
}

struct RequestDetail: View {
    let request: RexRequest
    let state: AppState

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Label("\(request.server) · \(request.host)", systemImage: request.kindSymbol).font(.headline)
                    Spacer()
                    if request.isPending { DeadlineRing(request: request) }
                    StatusBadge(request: request)
                }
                RequestBody(request: request)
                if request.isPending {
                    DecisionBar(request: request, state: state).id(request.id)
                }
                meta
                if let fb = request.feedback, !fb.isEmpty {
                    LabeledContent("拒绝原因", value: fb)
                }
                if let err = request.error, !err.isEmpty {
                    LabeledContent("错误", value: err)
                }
                if let out = request.output, !out.isEmpty {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(outputTitle).font(.subheadline.bold())
                        CodeBox(text: out, maxHeight: 360)
                    }
                }
            }
            .padding(18)
        }
    }

    private var outputTitle: String {
        let size = request.outputSize ?? 0
        if size > Int64(request.output?.utf8.count ?? 0) {
            return "输出（最后 \(Fmt.bytes(Int64(request.output?.utf8.count ?? 0)))，共 \(Fmt.bytes(size))）"
        }
        return "输出"
    }

    private var meta: some View {
        Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 4) {
            GridRow {
                Text("提交").foregroundStyle(.secondary)
                Text(Fmt.dateTime.string(from: request.createdAt.date))
            }
            if let d = request.decidedAt {
                GridRow {
                    Text("决定").foregroundStyle(.secondary)
                    Text(Fmt.dateTime.string(from: d.date) + (request.approvedByLabel.map { " · \($0)" } ?? ""))
                }
            }
            if let f = request.finishedAt, request.status == "completed" {
                GridRow {
                    Text("结束").foregroundStyle(.secondary)
                    Text(Fmt.dateTime.string(from: f.date) + " · exit \(request.exitCode ?? 0)")
                }
            }
        }
        .font(.callout)
    }
}

// MARK: - Whitelist

struct WhitelistView: View {
    let state: AppState
    @State private var kind = "exec"
    @State private var pattern = ""
    @State private var servers = "*"
    @State private var note = ""

    var body: some View {
        let rules = state.config?.whitelist ?? []
        VStack(alignment: .leading, spacing: 0) {
            Table(Array(rules.enumerated()).map { IndexedRule(index: $0.offset, rule: $0.element) }) {
                TableColumn("类型") { Text($0.rule.kind) }.width(70)
                TableColumn("规则") { Text($0.rule.pattern).font(.system(.body, design: .monospaced)) }
                TableColumn("服务器") { Text(($0.rule.servers ?? []).joined(separator: ", ")) }.width(120)
                TableColumn("备注") { Text($0.rule.note ?? "").foregroundStyle(.secondary) }
                TableColumn("") { item in
                    Button(role: .destructive) { state.removeRule(at: item.index) } label: { Image(systemName: "trash") }
                        .buttonStyle(.borderless)
                }.width(30)
            }
            Divider()
            HStack {
                Picker("", selection: $kind) {
                    Text("命令").tag("exec")
                    Text("下载").tag("download")
                    Text("上传").tag("upload")
                    Text("转发").tag("forward")
                }
                .labelsHidden()
                .frame(width: 80)
                TextField(placeholder, text: $pattern).font(.system(.body, design: .monospaced))
                TextField("服务器（逗号分隔，* 表示全部）", text: $servers).frame(width: 200)
                TextField("备注", text: $note).frame(width: 160)
                Button("添加") {
                    let list = servers.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
                    state.addRule(WhitelistRule(type: kind == "exec" ? nil : kind, pattern: pattern,
                                                servers: list.isEmpty ? ["*"] : list, note: note.isEmpty ? nil : note))
                    pattern = ""
                    note = ""
                }
                .disabled(pattern.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            .padding(10)
            Text("命令规则逐个匹配管道/&&/;/换行拆分后的每条命令，* 匹配任意字符。含 $(...)、反引号、写文件重定向、循环、& 或 eval 的命令永远需要人工审批。")
                .font(.caption).foregroundStyle(.secondary).padding([.horizontal, .bottom], 10)
        }
        .navigationTitle("白名单")
        .toolbar {
            Button { state.reloadConfig() } label: { Label("重新加载配置", systemImage: "arrow.clockwise") }
            Button { if let p = state.config?.path { NSWorkspace.shared.open(URL(fileURLWithPath: p)) } } label: {
                Label("编辑配置文件", systemImage: "doc.text")
            }
        }
    }

    private var placeholder: String {
        switch kind {
        case "download", "upload": return "远端路径，如 /var/log/*"
        case "forward": return "host:port，如 localhost:5432"
        default: return "命令，如 tail *"
        }
    }
}

struct IndexedRule: Identifiable {
    let index: Int
    let rule: WhitelistRule
    var id: Int { index }
}

// MARK: - Auto-approve

struct AutoApproveView: View {
    let state: AppState
    @State private var minutes = 30
    @State private var allServers = true
    @State private var selected: Set<String> = []

    var body: some View {
        let a = state.daemon.auto
        Form {
            if a.active {
                Section {
                    LabeledContent("状态") {
                        Label("开启中", systemImage: "bolt.fill").foregroundStyle(Palette.auto)
                    }
                    LabeledContent("结束时间", value: Fmt.time.string(from: (a.until ?? 0).date))
                    LabeledContent("范围", value: (a.servers ?? []).isEmpty ? "全部服务器" : (a.servers ?? []).joined(separator: ", "))
                    LabeledContent("已自动通过", value: "\(a.approved)")
                    LabeledContent("挂起待人工", value: "\(a.held)")
                    Button("立即停止", role: .destructive) { state.stopAuto() }
                }
            } else {
                Section {
                    Picker("时长", selection: $minutes) {
                        ForEach([15, 30, 60, 120, 240].filter { $0 <= (state.config?.autoMaxMinutes ?? 240) }, id: \.self) { m in
                            Text(m < 60 ? "\(m) 分钟" : "\(m / 60) 小时").tag(m)
                        }
                    }
                    Toggle("全部服务器", isOn: $allServers)
                    if !allServers {
                        ForEach(state.serverNames, id: \.self) { name in
                            Toggle(name, isOn: Binding(
                                get: { selected.contains(name) },
                                set: { on in if on { selected.insert(name) } else { selected.remove(name) } }
                            ))
                        }
                    }
                    Button("开启自动审批") {
                        state.startAuto(minutes: minutes, servers: allServers ? [] : selected.sorted())
                    }
                    .disabled(!allServers && selected.isEmpty)
                    if let reason = a.stopReason {
                        Text("上次结束：\(reason)，通过 \(a.approved)，挂起 \(a.held)").font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            Section("规则") {
                Text("开启期间，策略判定无风险的请求直接通过。命中下列高风险规则、无法完整分析（动态命令、eval 变量、未知脚本经 stdin 传入 shell）、写入系统路径的请求仍然挂起，无人处理则超时返回给 Agent。每分钟请求过多或连续失败会自动熔断。")
                    .font(.callout).foregroundStyle(.secondary)
                DisclosureGroup("高风险规则（\(state.config?.blocklist?.count ?? 0) 条）") {
                    Text((state.config?.blocklist ?? []).joined(separator: "\n"))
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                }
            }
        }
        .formStyle(.grouped)
        .navigationTitle("自动审批")
    }
}

// MARK: - Forwards

struct ForwardsView: View {
    let state: AppState

    var body: some View {
        Group {
            if state.forwards.isEmpty {
                Text("没有活动的端口转发").foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Table(state.forwards) {
                    TableColumn("#") { Text("\($0.id)") }.width(30)
                    TableColumn("本地") { Text("127.0.0.1:\($0.localPort)").font(.system(.body, design: .monospaced)) }
                    TableColumn("远端") { Text("\($0.remoteHost):\($0.remotePort) @ \($0.server)") }
                    TableColumn("到期") { f in
                        TimelineView(.periodic(from: .now, by: 30)) { _ in
                            Text("\(Fmt.time.string(from: f.expiresAt.date))（剩 \(Fmt.remaining(until: f.expiresAt))）")
                        }
                    }
                    TableColumn("说明") { Text($0.desc).foregroundStyle(.secondary) }
                    TableColumn("") { f in
                        Button("关闭") { state.closeForward(f.id) }
                    }.width(50)
                }
            }
        }
        .navigationTitle("端口转发")
    }
}

// MARK: - Settings

struct SettingsView: View {
    let state: AppState
    @AppStorage(Settings.notifications) private var notifications = false
    @AppStorage(Settings.sound) private var sound = true
    @AppStorage(Settings.hotkeys) private var hotkeys = true
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled
    @State private var skillMessage: String?

    var body: some View {
        Form {
            Section("提醒") {
                Toggle("新请求时播放提示音", isOn: $sound)
                Toggle("同时发送系统通知（可在通知上直接批准/拒绝）", isOn: $notifications)
                    .onChange(of: notifications) { if notifications { Notifier.shared.requestAuthorization() } }
            }
            Section("快捷键") {
                Toggle("启用全局快捷键", isOn: $hotkeys)
                    .onChange(of: hotkeys) { AppDelegate.shared?.applyHotkeys() }
                Text("⌃⌥Y 批准当前请求（高风险请求需在弹窗中按住批准）\n⌃⌥N 拒绝当前请求并填写原因\n⌃⌥R 展开/收起弹窗")
                    .font(.callout).foregroundStyle(.secondary)
            }
            Section("系统") {
                Toggle("登录时启动", isOn: $launchAtLogin)
                    .onChange(of: launchAtLogin) {
                        do {
                            if launchAtLogin { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
                        } catch {
                            launchAtLogin = SMAppService.mainApp.status == .enabled
                        }
                    }
                LabeledContent("配置文件", value: state.config?.path ?? "-")
                HStack {
                    Button("编辑配置") { if let p = state.config?.path { NSWorkspace.shared.open(URL(fileURLWithPath: p)) } }
                    Button("重新加载配置") { state.reloadConfig() }
                    Button("重启 rexd") { state.restartDaemon() }
                }
            }
            Section("Agent") {
                Text("安装 Claude Code Skill，让 Agent 知道如何使用 rex（写入 ~/.claude/skills/rex/SKILL.md）。")
                    .font(.callout).foregroundStyle(.secondary)
                HStack {
                    Button("安装 Skill") { installSkill() }
                    if let m = skillMessage { Text(m).font(.caption).foregroundStyle(.secondary) }
                }
            }
        }
        .formStyle(.grouped)
        .navigationTitle("设置")
    }

    private func installSkill() {
        let rex = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/rex")
        guard FileManager.default.isExecutableFile(atPath: rex.path) else {
            skillMessage = "App 内未找到 rex，请在终端运行：rex skill install"
            return
        }
        let p = Process()
        p.executableURL = rex
        p.arguments = ["skill", "install"]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = out
        do {
            try p.run()
            p.waitUntilExit()
            skillMessage = String(decoding: out.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
                .trimmingCharacters(in: .whitespacesAndNewlines)
        } catch {
            skillMessage = error.localizedDescription
        }
    }
}
