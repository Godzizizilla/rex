import Combine
import SwiftUI

enum Palette {
    static let ok = Color(red: 0.19, green: 0.82, blue: 0.35)
    static let warn = Color(red: 1.0, green: 0.62, blue: 0.04)
    static let danger = Color(red: 1.0, green: 0.27, blue: 0.23)
    static let auto = Color(red: 1.0, green: 0.62, blue: 0.04)
}

/// Shows what a request will do. Used by the island and the main window.
struct RequestBody: View {
    let request: RexRequest
    var compact = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(request.desc)
                .font(.system(size: compact ? 13 : 14, weight: .semibold))
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)

            switch request.kind {
            case "upload": uploadView
            case "download": transferRow(symbol: "arrow.down.doc", text: "\(request.server):\(request.remotePath ?? "") → \(request.localPath ?? "")")
            case "forward": forwardView
            default: execView
            }

            warnings
        }
    }

    // MARK: exec

    @ViewBuilder private var execView: some View {
        CodeBox(text: request.command ?? "", maxHeight: compact ? 110 : 220)
        let cmds = request.eval?.commands ?? []
        if cmds.count > 1 || cmds.contains(where: { $0.nested == true }) {
            VStack(alignment: .leading, spacing: 3) {
                ForEach(Array(cmds.enumerated()), id: \.offset) { _, c in
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Circle().fill(color(for: c)).frame(width: 7, height: 7)
                        if let op = c.op, !op.isEmpty {
                            Text(op).font(.system(size: 11, design: .monospaced)).foregroundStyle(.secondary)
                        }
                        Text(c.raw)
                            .font(.system(size: 11.5, design: .monospaced))
                            .lineLimit(2)
                            .truncationMode(.middle)
                        if c.nested == true {
                            Text("嵌套").font(.system(size: 10)).foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
        if let size = request.stdinSize, size > 0 {
            VStack(alignment: .leading, spacing: 4) {
                Label("stdin \(Fmt.bytes(Int64(size)))", systemImage: "arrow.right.to.line")
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(.secondary)
                CodeBox(text: request.stdinPreview ?? "", maxHeight: compact ? 80 : 160)
            }
        }
    }

    private func color(for c: PolicyCommand) -> Color {
        if c.risk != nil { return Palette.danger }
        if c.whitelisted == true { return Palette.ok }
        return Palette.warn
    }

    // MARK: transfer & forward

    private func transferRow(symbol: String, text: String) -> some View {
        Label(text, systemImage: symbol)
            .font(.system(size: 12, design: .monospaced))
            .lineLimit(2)
            .truncationMode(.middle)
            .textSelection(.enabled)
    }

    @ViewBuilder private var uploadView: some View {
        transferRow(symbol: "arrow.up.doc",
                    text: "\(request.localPath ?? "") → \(request.server):\(request.remotePath ?? "") (\(Fmt.bytes(request.size ?? 0)))")
        if let diff = request.diff, !diff.isEmpty {
            DiffBox(diff: diff, maxHeight: compact ? 140 : 320)
        } else {
            Text("无法生成 diff（二进制、过大或读取远端失败）").font(.system(size: 11)).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder private var forwardView: some View {
        let local = (request.localPort ?? 0) == 0 ? "127.0.0.1:自动" : "127.0.0.1:\(request.localPort!)"
        transferRow(symbol: "point.3.connected.trianglepath.dotted",
                    text: "\(local) → \(request.remoteHost ?? "localhost"):\(request.remotePort ?? 0) @ \(request.server)，有效期 \(request.ttl ?? 0) 分钟")
        Text("转发期间经过隧道的流量不受审计").font(.system(size: 11)).foregroundStyle(.secondary)
    }

    // MARK: warnings

    @ViewBuilder private var warnings: some View {
        let risks = request.risks
        let constructs = request.eval?.constructs ?? []
        let held = (request.heldBy ?? []).filter { !risks.contains($0) }
        if !risks.isEmpty || !constructs.isEmpty || !held.isEmpty {
            VStack(alignment: .leading, spacing: 3) {
                ForEach(risks, id: \.self) { r in
                    Label(r, systemImage: "exclamationmark.octagon.fill").foregroundStyle(Palette.danger)
                }
                if !constructs.isEmpty {
                    Label("需人工审核：" + constructs.joined(separator: "、"), systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(Palette.warn)
                }
                ForEach(held, id: \.self) { h in
                    Label(h, systemImage: "bolt.slash.fill").foregroundStyle(Palette.auto)
                }
            }
            .font(.system(size: 11.5))
            .fixedSize(horizontal: false, vertical: true)
        }
    }
}

struct CodeBox: View {
    let text: String
    var maxHeight: CGFloat = 200

    var body: some View {
        ScrollView {
            Text(text)
                .font(.system(size: 12, design: .monospaced))
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
                .padding(8)
        }
        .frame(maxHeight: maxHeight)
        .fixedSize(horizontal: false, vertical: true)
        .background(RoundedRectangle(cornerRadius: 6).fill(Color.primary.opacity(0.08)))
    }
}

struct DiffBox: View {
    let diff: String
    var maxHeight: CGFloat = 240

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(diff.split(separator: "\n", omittingEmptySubsequences: false).enumerated()), id: \.offset) { _, line in
                    Text(String(line).isEmpty ? " " : String(line))
                        .font(.system(size: 11.5, design: .monospaced))
                        .foregroundStyle(color(line))
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(background(line))
                }
            }
            .padding(6)
            .textSelection(.enabled)
        }
        .frame(maxHeight: maxHeight)
        .fixedSize(horizontal: false, vertical: true)
        .background(RoundedRectangle(cornerRadius: 6).fill(Color.primary.opacity(0.08)))
    }

    private func color(_ l: Substring) -> Color {
        if l.hasPrefix("@@") { return .secondary }
        return .primary
    }

    private func background(_ l: Substring) -> Color {
        if l.hasPrefix("+++") || l.hasPrefix("---") { return .clear }
        if l.hasPrefix("+") { return Palette.ok.opacity(0.22) }
        if l.hasPrefix("-") { return Palette.danger.opacity(0.22) }
        return .clear
    }
}

/// Approve / reject / whitelist buttons for a pending request.
struct DecisionBar: View {
    let request: RexRequest
    let state: AppState
    /// Buttons ignore clicks until this date, so a panel that pops up under
    /// the cursor can't be approved by accident.
    var armedAt: Date = .distantPast
    var onRejectFocus: () -> Void = {}

    @State private var mode: Mode = .buttons
    @State private var feedback = ""
    @State private var selection: Set<Int> = []
    @FocusState private var feedbackFocused: Bool

    enum Mode { case buttons, reject, whitelist }

    var body: some View {
        TimelineView(.periodic(from: .now, by: 0.25)) { ctx in
            let armed = ctx.date >= armedAt
            Group {
                switch mode {
                case .buttons: buttons(armed: armed)
                case .reject: rejectForm
                case .whitelist: whitelistForm
                }
            }
            .disabled(!armed)
        }
        .onChange(of: request.id) {
            mode = .buttons
            feedback = ""
        }
        .onReceive(NotificationCenter.default.publisher(for: .rexBeginReject)) { n in
            if (n.object as? Int) == request.id { mode = .reject }
        }
    }

    private func buttons(armed: Bool) -> some View {
        HStack(spacing: 8) {
            PillButton(title: "拒绝…", systemImage: "xmark", tint: .secondary) {
                mode = .reject
                onRejectFocus()
            }
            .keyboardShortcut(.cancelAction)
            PillButton(title: whitelistTitle, systemImage: "checklist", tint: .secondary) {
                if request.kind == "exec" && (request.eval?.commands?.count ?? 0) > 0 {
                    let cmds = request.eval?.commands ?? []
                    selection = Set(cmds.indices.filter { cmds[$0].whitelisted != true && cmds[$0].nested != true })
                    mode = .whitelist
                } else {
                    state.approveWhitelist(request.id, indices: [])
                }
            }
            .disabled(request.isRisky)
            Spacer(minLength: 8)
            if request.isRisky {
                HoldButton(title: "按住批准", tint: Palette.danger) { state.approve(request.id) }
            } else {
                PillButton(title: "批准", systemImage: "checkmark", tint: Palette.ok, prominent: true) {
                    state.approve(request.id)
                }
            }
        }
        .opacity(armed ? 1 : 0.5)
    }

    private var whitelistTitle: String {
        request.kind == "exec" ? "加白名单并批准…" : "加白名单并批准"
    }

    private var rejectForm: some View {
        HStack(spacing: 8) {
            TextField("拒绝原因（会反馈给 Agent）", text: $feedback)
                .textFieldStyle(.roundedBorder)
                .focused($feedbackFocused)
                .onSubmit(sendReject)
                .onAppear { feedbackFocused = true }
            PillButton(title: "取消", tint: .secondary) { mode = .buttons }
                .keyboardShortcut(.cancelAction)
            PillButton(title: "拒绝", systemImage: "xmark", tint: Palette.danger, prominent: true, action: sendReject)
        }
    }

    private func sendReject() {
        state.reject(request.id, feedback: feedback.trimmingCharacters(in: .whitespacesAndNewlines))
        feedback = ""
        mode = .buttons
    }

    private var whitelistForm: some View {
        let cmds = request.eval?.commands ?? []
        return VStack(alignment: .leading, spacing: 6) {
            Text("选择要加入 \(request.server) 白名单的命令：").font(.system(size: 12, weight: .medium))
            ForEach(Array(cmds.enumerated()), id: \.offset) { i, c in
                if c.whitelisted == true || c.nested == true || c.name == nil {
                    EmptyView()
                } else {
                    Toggle(isOn: Binding(
                        get: { selection.contains(i) },
                        set: { on in if on { selection.insert(i) } else { selection.remove(i) } }
                    )) {
                        Text("\(c.name ?? c.raw) *").font(.system(size: 12, design: .monospaced))
                    }
                    .toggleStyle(.checkbox)
                }
            }
            HStack {
                Spacer()
                PillButton(title: "取消", tint: .secondary) { mode = .buttons }
                PillButton(title: "加入并批准", systemImage: "checkmark", tint: Palette.ok, prominent: true) {
                    state.approveWhitelist(request.id, indices: selection.sorted())
                    mode = .buttons
                }
            }
        }
    }
}

struct PillButton: View {
    let title: String
    var systemImage: String?
    var tint: Color = .accentColor
    var prominent = false
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 4) {
                if let s = systemImage { Image(systemName: s).font(.system(size: 11, weight: .bold)) }
                Text(title).font(.system(size: 12.5, weight: .semibold))
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .foregroundStyle(prominent ? Color.white : Color.primary)
            .background(Capsule().fill(prominent ? tint : Color.primary.opacity(0.12)))
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
    }
}

/// Approves only after being held down, for high-risk requests.
struct HoldButton: View {
    let title: String
    var tint: Color = Palette.danger
    var duration: Double = 1.0
    let action: () -> Void
    @State private var progress: CGFloat = 0

    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: "hand.raised.fill").font(.system(size: 11, weight: .bold))
            Text(title).font(.system(size: 12.5, weight: .semibold))
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .foregroundStyle(.white)
        .background(
            ZStack(alignment: .leading) {
                Capsule().fill(tint.opacity(0.35))
                GeometryReader { g in
                    Rectangle().fill(tint).frame(width: g.size.width * progress)
                }
            }
            .clipShape(Capsule())
        )
        .contentShape(Capsule())
        .onLongPressGesture(minimumDuration: duration, perform: {
            progress = 0
            action()
        }, onPressingChanged: { pressing in
            withAnimation(pressing ? .linear(duration: duration) : .easeOut(duration: 0.15)) {
                progress = pressing ? 1 : 0
            }
        })
        .help("高风险操作：按住 1 秒批准")
    }
}

/// Ring that empties as the approval deadline approaches.
struct DeadlineRing: View {
    let request: RexRequest

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { ctx in
            let total = Double(max(request.timeout, 1))
            let remain = max(0, (request.deadline ?? 0).date.timeIntervalSince(ctx.date))
            ZStack {
                Circle().stroke(Color.primary.opacity(0.15), lineWidth: 2.5)
                Circle()
                    .trim(from: 0, to: remain / total)
                    .stroke(remain < 10 ? Palette.danger : Palette.ok, style: StrokeStyle(lineWidth: 2.5, lineCap: .round))
                    .rotationEffect(.degrees(-90))
                Text("\(Int(remain.rounded()))")
                    .font(.system(size: 9.5, weight: .semibold, design: .rounded))
                    .monospacedDigit()
            }
            .frame(width: 24, height: 24)
        }
    }
}
