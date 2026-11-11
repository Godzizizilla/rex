import AppKit
import SwiftUI

// The "Dynamic Island": a black panel attached to the top edge of the screen.
// On a MacBook with a notch it grows out of the notch; on other displays it
// draws a notch-shaped capsule in the middle of the menu bar.
//
// - hidden:   nothing pending, auto-approve off
// - compact:  notch-sized, shows the pending count / auto-approve countdown
// - expanded: shows the current request with approve / reject buttons

enum IslandMode: Equatable {
    case hidden, compact, expanded
}

@MainActor
@Observable
final class IslandModel {
    var mode: IslandMode = .hidden
    var selectedID: Int?
    var notchSize = CGSize(width: 190, height: 32)
    var hasNotch = false
    /// Buttons for a request are armed only after it has been on screen briefly.
    var armedAt: [Int: Date] = [:]
    var collapsedByUser = false
    var hovering = false
}

final class IslandPanel: NSPanel {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }
    // Stay glued to the top edge, over the menu bar.
    override func constrainFrameRect(_ frameRect: NSRect, to screen: NSScreen?) -> NSRect { frameRect }
}

/// Hosting view that reacts to the first click even though the panel is not key.
final class ClickThroughHostingView<Content: View>: NSHostingView<Content> {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
}

@MainActor
final class IslandController {
    let model = IslandModel()
    private let state: AppState
    private let panel: IslandPanel
    private var screen: NSScreen?
    private var contentSize = CGSize.zero
    private var knownPending = Set<Int>()
    private var mouseMonitors: [Any] = []

    static let expandedWidth: CGFloat = 600
    /// Max scrollable body height so approve/reject buttons stay on screen.
    static let expandedBodyMaxHeight: CGFloat = 360
    // The panel has a fixed size large enough for the expanded island; it is
    // never resized (resizing a window from SwiftUI size changes loops
    // AppKit's constraint pass). Transparent parts let clicks through because
    // ignoresMouseEvents is only off while the pointer is over the island.
    static let panelSize = CGSize(width: expandedWidth + 60, height: 640)

    init(state: AppState) {
        self.state = state
        panel = IslandPanel(contentRect: NSRect(x: 0, y: 0, width: 10, height: 10),
                            styleMask: [.borderless, .nonactivatingPanel],
                            backing: .buffered, defer: false)
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = false
        panel.level = NSWindow.Level(rawValue: NSWindow.Level.mainMenu.rawValue + 3)
        panel.collectionBehavior = [.canJoinAllSpaces, .stationary, .fullScreenAuxiliary, .ignoresCycle]
        panel.isMovable = false
        panel.hidesOnDeactivate = false
        panel.isReleasedWhenClosed = false
        panel.ignoresMouseEvents = true

        let root = IslandRootView(state: state, model: model, controller: self)
        let hosting = ClickThroughHostingView(rootView: root)
        // When an NSHostingView is a window's contentView, SwiftUI resizes the
        // window to fit its content, which loops AppKit's layout pass while the
        // island animates. Wrapping it in a plain view keeps the panel size ours.
        hosting.sizingOptions = []
        let container = NSView(frame: NSRect(origin: .zero, size: Self.panelSize))
        hosting.frame = container.bounds
        hosting.autoresizingMask = [.width, .height]
        container.addSubview(hosting)
        panel.contentView = container

        let track: (NSEvent) -> Void = { [weak self] _ in
            MainActor.assumeIsolated { self?.trackMouse() }
        }
        if let m = NSEvent.addGlobalMonitorForEvents(matching: [.mouseMoved, .leftMouseDragged], handler: track) {
            mouseMonitors.append(m)
        }
        if let m = NSEvent.addLocalMonitorForEvents(matching: [.mouseMoved, .leftMouseDragged], handler: { e in track(e); return e }) {
            mouseMonitors.append(m)
        }

        NotificationCenter.default.addObserver(forName: NSApplication.didChangeScreenParametersNotification,
                                               object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.layout() }
        }
    }

    // MARK: state

    /// Recomputes the mode after any state change.
    func update() {
        let pending = state.pending
        let ids = Set(pending.map(\.id))
        let fresh = ids.subtracting(knownPending)
        knownPending = ids

        if !fresh.isEmpty {
            // A new request: come forward on the screen the user is looking at.
            model.collapsedByUser = false
            moveToMouseScreen()
            let armDate = Date().addingTimeInterval(0.7)
            for id in fresh { model.armedAt[id] = armDate }
            if model.selectedID == nil || !ids.contains(model.selectedID!) {
                model.selectedID = pending.first(where: { fresh.contains($0.id) })?.id
            }
            if UserDefaults.standard.bool(forKey: Settings.sound) {
                NSSound(named: "Glass")?.play()
            }
        }
        if model.selectedID == nil || !ids.contains(model.selectedID!) {
            let next = pending.first?.id
            if model.selectedID != next { model.selectedID = next }
        }
        if model.armedAt.keys.contains(where: { !ids.contains($0) }) {
            model.armedAt = model.armedAt.filter { ids.contains($0.key) }
        }

        let mode: IslandMode
        if !pending.isEmpty {
            mode = !model.collapsedByUser || model.hovering ? .expanded : .compact
        } else if state.notice != nil {
            mode = .expanded // show the notice for a few seconds
        } else if state.daemon.auto.active {
            mode = model.hovering ? .expanded : .compact
        } else if !state.connected {
            mode = .compact // warning sign: approvals are not possible
        } else {
            mode = .hidden
        }
        setMode(mode)
    }

    private func setMode(_ mode: IslandMode) {
        if model.mode != mode {
            withAnimation(.spring(response: 0.38, dampingFraction: 0.82)) {
                model.mode = mode
            }
        }
        if mode == .hidden {
            // Let the collapse animation finish before hiding the panel.
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.35) { [weak self] in
                guard let self, self.model.mode == .hidden else { return }
                self.panel.orderOut(nil)
            }
        } else {
            if screen == nil { moveToMouseScreen() }
            layout()
            panel.orderFrontRegardless()
        }
        if mode != .expanded && panel.isKeyWindow {
            panel.resignKey()
        }
    }

    func toggle() {
        if model.mode == .expanded {
            collapse()
        } else {
            model.collapsedByUser = false
            moveToMouseScreen()
            if state.pending.isEmpty {
                setMode(.compact)
            } else {
                setMode(.expanded)
            }
        }
    }

    func collapse() {
        model.collapsedByUser = true
        update()
    }

    func select(offset: Int) {
        let pending = state.pending
        guard let sel = model.selectedID, let i = pending.firstIndex(where: { $0.id == sel }) else { return }
        let j = (i + offset + pending.count) % pending.count
        model.selectedID = pending[j].id
    }

    func setHovering(_ h: Bool) {
        guard model.hovering != h else { return }
        model.hovering = h
        if !h && model.collapsedByUser {
            // Collapse back a little after the pointer leaves.
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.4) { [weak self] in
                guard let self, !self.model.hovering else { return }
                self.update()
            }
        } else {
            update()
        }
    }

    /// Makes the panel key so the reject text field can receive typing.
    func focusForTyping() {
        panel.makeKey()
    }

    /// Opens the island with the reject form for the current request.
    func beginReject() {
        model.collapsedByUser = false
        moveToMouseScreen()
        setMode(.expanded)
        focusForTyping()
        NotificationCenter.default.post(name: .rexBeginReject, object: model.selectedID)
    }

    var selectedRequest: RexRequest? {
        guard let id = model.selectedID else { return nil }
        return state.requests[id]
    }

    // MARK: geometry

    private func moveToMouseScreen() {
        let mouse = NSEvent.mouseLocation
        let target = NSScreen.screens.first(where: { NSMouseInRect(mouse, $0.frame, false) }) ?? NSScreen.main
        if target != screen {
            screen = target
            measureNotch()
        }
    }

    private func measureNotch() {
        guard let s = screen else { return }
        if s.safeAreaInsets.top > 0, let l = s.auxiliaryTopLeftArea, let r = s.auxiliaryTopRightArea {
            model.hasNotch = true
            model.notchSize = CGSize(width: s.frame.width - l.width - r.width, height: s.safeAreaInsets.top)
        } else {
            model.hasNotch = false
            let menuBar = s.frame.maxY - s.visibleFrame.maxY
            model.notchSize = CGSize(width: 190, height: max(menuBar > 0 ? menuBar : 24, 24))
        }
    }

    /// Called by the SwiftUI content with its size.
    func contentSizeChanged(_ size: CGSize) {
        guard size != contentSize else { return }
        contentSize = size
        // Don't touch observed state from inside SwiftUI's update pass.
        DispatchQueue.main.async { [weak self] in self?.trackMouse() }
    }

    /// The island's rectangle in screen coordinates.
    private var contentRect: NSRect {
        let f = panel.frame
        return NSRect(x: f.midX - contentSize.width / 2, y: f.maxY - contentSize.height,
                      width: contentSize.width, height: contentSize.height)
    }

    private func trackMouse() {
        guard panel.isVisible, model.mode != .hidden else {
            panel.ignoresMouseEvents = true
            return
        }
        let inside = NSMouseInRect(NSEvent.mouseLocation, contentRect.insetBy(dx: -2, dy: -2), false)
        if panel.ignoresMouseEvents == inside {
            panel.ignoresMouseEvents = !inside
        }
        setHovering(inside)
    }

    private func layout() {
        guard let s = screen ?? NSScreen.main else { return }
        if screen == nil {
            screen = s
            measureNotch()
        }
        let size = Self.panelSize
        let frame = NSRect(x: (s.frame.midX - size.width / 2).rounded(), y: s.frame.maxY - size.height,
                           width: size.width, height: size.height)
        if panel.frame != frame {
            panel.setFrame(frame, display: true)
        }
        snapshotForDevelopment()
    }

    private var snapshotCount = 0

    /// Development aid: with REX_SNAPSHOT_DIR set, the island is rendered to
    /// PNG files after each layout (no screen recording permission needed).
    private func snapshotForDevelopment() {
        guard let dir = ProcessInfo.processInfo.environment["REX_SNAPSHOT_DIR"], model.mode != .hidden else { return }
        snapshotCount += 1
        let n = snapshotCount
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.8) { [weak self] in
            guard let view = self?.panel.contentView,
                  let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { return }
            view.cacheDisplay(in: view.bounds, to: rep)
            let url = URL(fileURLWithPath: dir).appendingPathComponent(String(format: "island-%03d.png", n))
            try? rep.representation(using: .png, properties: [:])?.write(to: url)
        }
    }
}

extension Notification.Name {
    static let rexBeginReject = Notification.Name("rexBeginReject")
}

private struct SizeKey: PreferenceKey {
    static var defaultValue: CGSize = .zero
    // Siblings that don't set the key (the Spacer below the island) report
    // defaultValue; keep the non-zero size instead of letting them overwrite it.
    static func reduce(value: inout CGSize, nextValue: () -> CGSize) {
        let next = nextValue()
        if next != .zero { value = next }
    }
}

private struct ScrollContentHeightKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) {
        value = max(value, nextValue())
    }
}

/// The shape of the notch: square top, rounded bottom.
struct NotchShape: Shape {
    var radius: CGFloat
    var animatableData: CGFloat {
        get { radius }
        set { radius = newValue }
    }

    func path(in rect: CGRect) -> Path {
        UnevenRoundedRectangle(cornerRadii: .init(topLeading: 0, bottomLeading: radius,
                                                  bottomTrailing: radius, topTrailing: 0),
                               style: .continuous).path(in: rect)
    }
}

struct IslandRootView: View {
    let state: AppState
    @Bindable var model: IslandModel
    let controller: IslandController
    @State private var pendingBodyHeight: CGFloat = 0

    var body: some View {
        VStack(spacing: 0) {
            if model.mode != .hidden {
                island
                    .background(GeometryReader { g in Color.clear.preference(key: SizeKey.self, value: g.size) })
                    .transition(.opacity)
            }
            Spacer(minLength: 0)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .onPreferenceChange(SizeKey.self) { size in
            MainActor.assumeIsolated { controller.contentSizeChanged(size) }
        }
        .environment(\.colorScheme, .dark)
    }

    private var island: some View {
        let expanded = model.mode == .expanded
        return VStack(alignment: .leading, spacing: 0) {
            header
            if expanded {
                expandedContent
                    .padding(.horizontal, 18)
                    .padding(.top, 6)
                    .padding(.bottom, 16)
                    .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
        .frame(width: expanded ? IslandController.expandedWidth : model.notchSize.width + 2 * sideWidth)
        .background(NotchShape(radius: expanded ? 22 : 12).fill(Color.black))
        .shadow(color: .black.opacity(expanded ? 0.45 : 0), radius: 14, y: 6)
        .foregroundStyle(.white)
    }

    private var sideWidth: CGFloat {
        state.daemon.auto.active || state.pending.count > 0 ? 64 : 44
    }

    // The strip at notch height: status on the left, counter on the right.
    private var header: some View {
        HStack(spacing: 6) {
            leftStatus
            Spacer(minLength: model.notchSize.width)
            rightStatus
        }
        .padding(.horizontal, 12)
        .frame(height: model.notchSize.height)
        .contentShape(Rectangle())
        .onTapGesture { controller.toggle() }
    }

    @ViewBuilder private var leftStatus: some View {
        if !state.connected {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(Palette.warn)
        } else if state.daemon.auto.active {
            Image(systemName: "bolt.fill").foregroundStyle(Palette.auto)
        } else if let r = state.pending.first, r.isRisky {
            Image(systemName: "exclamationmark.shield.fill").foregroundStyle(Palette.danger)
        } else {
            Image(systemName: "checkmark.shield.fill").foregroundStyle(state.pending.isEmpty ? Color.white.opacity(0.7) : Palette.warn)
        }
    }

    @ViewBuilder private var rightStatus: some View {
        HStack(spacing: 6) {
            if state.daemon.auto.active, let until = state.daemon.auto.until {
                TimelineView(.periodic(from: .now, by: 30)) { _ in
                    Text(Fmt.remaining(until: until))
                        .font(.system(size: 11, weight: .semibold, design: .rounded))
                        .foregroundStyle(Palette.auto)
                }
            }
            let n = state.pending.count
            if n > 0 {
                Text("\(n)")
                    .font(.system(size: 11, weight: .bold, design: .rounded))
                    .padding(.horizontal, 6)
                    .padding(.vertical, 1)
                    .background(Capsule().fill(Palette.warn))
                    .foregroundStyle(.black)
            }
            if model.mode == .expanded && !state.pending.isEmpty {
                Button { controller.collapse() } label: {
                    Image(systemName: "chevron.up").font(.system(size: 11, weight: .bold))
                }
                .buttonStyle(.plain)
                .help("收起")
            }
        }
    }

    @ViewBuilder private var expandedContent: some View {
        if let r = controller.selectedRequest, r.isPending {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    Image(systemName: r.kindSymbol)
                    Text(r.server).font(.system(size: 12, weight: .bold))
                    Text(r.host).font(.system(size: 11)).foregroundStyle(.secondary)
                    Text("#\(r.id)").font(.system(size: 11)).foregroundStyle(.secondary)
                    Spacer()
                    if state.pending.count > 1 {
                        Button { controller.select(offset: -1) } label: { Image(systemName: "chevron.left") }
                            .buttonStyle(.plain)
                        Text("\((state.pending.firstIndex(where: { $0.id == r.id }) ?? 0) + 1)/\(state.pending.count)")
                            .font(.system(size: 11, design: .rounded)).monospacedDigit()
                        Button { controller.select(offset: 1) } label: { Image(systemName: "chevron.right") }
                            .buttonStyle(.plain)
                    }
                    DeadlineRing(request: r)
                }
                let bodyHeight = min(max(pendingBodyHeight, 1), IslandController.expandedBodyMaxHeight)
                ScrollView(.vertical, showsIndicators: bodyHeight >= IslandController.expandedBodyMaxHeight - 1) {
                    RequestBody(request: r, compact: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background {
                            GeometryReader { geo in
                                Color.clear.preference(key: ScrollContentHeightKey.self, value: geo.size.height)
                            }
                        }
                }
                .frame(height: bodyHeight)
                .onPreferenceChange(ScrollContentHeightKey.self) { pendingBodyHeight = $0 }
                .onChange(of: r.id) { pendingBodyHeight = 0 }
                DecisionBar(request: r, state: state, armedAt: model.armedAt[r.id] ?? .distantPast,
                            onRejectFocus: { controller.focusForTyping() })
                    .padding(.top, 4)
                    .id(r.id)
            }
        } else if let n = state.notice {
            Label(n.text, systemImage: n.isError ? "exclamationmark.triangle.fill" : "info.circle.fill")
                .font(.system(size: 12.5))
                .foregroundStyle(n.isError ? Palette.danger : .white)
                .fixedSize(horizontal: false, vertical: true)
        } else if state.daemon.auto.active {
            autoSummary
        }
    }

    private var autoSummary: some View {
        let a = state.daemon.auto
        return HStack {
            Label("自动审批中，\(Fmt.time.string(from: (a.until ?? 0).date)) 结束 · 已通过 \(a.approved) · 挂起 \(a.held)",
                  systemImage: "bolt.fill")
                .font(.system(size: 12.5))
            Spacer()
            PillButton(title: "停止", tint: Palette.auto, prominent: true) { state.stopAuto() }
        }
    }
}
