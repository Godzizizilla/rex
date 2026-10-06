# 架构

```
┌──────────────┐  NDJSON / Unix socket   ┌──────────────────────────────┐   ssh    ┌────────┐
│ Agent        │  ~/.rex/rex.sock (0600) │ rexd                         │ ───────▶ │ 服务器 │
│  └ rex CLI   │ ──────────────────────▶ │  policy  评估白名单/风险      │          └────────┘
└──────────────┘  只能提交请求、查询信息   │  queue   待审批 + 超时 + 撤回  │
                                         │  ssh     执行 / 传输 / 转发   │
┌──────────────┐  NDJSON / stdin+stdout  │  history ~/.rex/history.jsonl │
│ Rex.app      │ ◀─────────────────────▶ │  auto    临时自动审批         │
│ 菜单栏+灵动岛 │  事件 / 审批命令          └──────────────────────────────┘
└──────────────┘  （rexd 是 App 的子进程）
```

## 组件

| 目录 | 说明 |
|---|---|
| `cmd/rex` | Agent 使用的 CLI。ssh 风格语法，成功时完全静默，失败时 `rex: ...` + 255 |
| `cmd/rexd` | 守护进程入口。监听 Agent socket，在 stdin/stdout 上提供控制协议 |
| `internal/policy` | 基于 mvdan.cc/sh 的命令分析、白名单匹配、高风险规则 |
| `internal/daemon` | 请求生命周期、ssh 执行、文件传输、端口转发、自动审批、历史 |
| `internal/protocol` | 两套协议的消息类型（见 [protocol.md](protocol.md)） |
| `internal/config` | `~/.rex/config.yaml` 读写 |
| `skill` | 内置的 Claude Code Skill（`rex skill install`） |
| `app` | Rex.app（SwiftUI + AppKit） |

## 为什么控制通道走 stdin/stdout

Agent 和 rexd 以同一个用户运行，文件权限挡不住 Agent。如果「批准」命令也走 `~/.rex/rex.sock`，Agent 就能自己批准自己的请求。因此：

- Agent socket 只接受提交和查询（`exec`/`upload`/`download`/`forward`/`servers`/`policy`/`forward_list`/`forward_close`）。
- 审批、白名单修改、自动审批开关只能通过 rexd 的 stdin 下发，而这对管道只有启动它的 Rex.app 持有。
- App 退出时 rexd 读到 EOF，取消所有请求、关闭端口转发后退出，不会留下无人审批的孤儿守护进程。

## 请求生命周期

```
            ┌─ 白名单覆盖 / 自动审批通过 ─────────────┐
submit ─▶ pending ─┬─ approve ──▶ approved ─▶ running ─┬─▶ completed（远端退出码）
                   ├─ reject  ──▶ rejected             ├─▶ failed（ssh 启动失败等）
                   ├─ 超时    ──▶ timeout              └─▶ cancelled（Agent 断开）
                   └─ Agent 断开 ─▶ cancelled
```

- `decide` 只在 `pending` 状态生效，先到先得，所以超时、断开和人工点击之间不会出现竞争。
- 每个 Agent 连接有一个 goroutine 持续读取：收到 `cancel` 或连接关闭就撤回请求；执行中则通过 context 取消 ssh 进程。
- 完成（含拒绝/超时/撤回）的请求追加写入 `history.jsonl`（输出截断为 16KB），重启后加载最近 500 条，ID 延续。

## 执行

- 命令执行：`ssh -o BatchMode=yes ... -- <host> <command>`，stdout/stderr 以原始字节块转发给 rex，不做任何行处理。
- 连接复用：`ControlMaster=auto`、`ControlPersist=10m`，控制 socket 在 `~/.rex/cm-%C`（路径过长时自动关闭复用）。
- 上传：`cat > "$t"`，数据走 stdin；目标是目录时追加本地文件名。审批前用 `head -c` 读取远端原文件生成 diff。
- 下载：`cat -- <path>`；rex 先写同目录临时文件，成功后 rename。
- 端口转发：独立的 `ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:<port>:<host>:<port>` 进程，不走连接复用；等本地端口可连接后才返回给 Agent。

## Rex.app

| 文件 | 说明 |
|---|---|
| `DaemonClient.swift` | 启动 `Contents/MacOS/rexd`，按行解析事件，stderr 写入 `~/.rex/rexd.log` |
| `AppState.swift` | `@Observable` 状态，事件 → 请求表/状态/配置；封装控制命令；rexd 退出后 3 秒自动重启 |
| `Island.swift` | 灵动岛：固定大小的非激活 `NSPanel`，位于菜单栏层级之上；内容为 SwiftUI |
| `RequestCard.swift` | 请求展示与审批按钮（弹窗和主窗口共用） |
| `MainWindow.swift` | 主窗口：请求历史、白名单、自动审批、端口转发、设置 |
| `RexApp.swift` | 入口、菜单栏、全局快捷键（Carbon `RegisterEventHotKey`，无需辅助功能权限）、系统通知 |

灵动岛实现要点：

- 刘海屏用 `NSScreen.safeAreaInsets`/`auxiliaryTopLeftArea` 计算刘海尺寸，弹窗顶部条与刘海等高，中间留空；无刘海屏用菜单栏高度画一个同形状的黑色胶囊。
- 面板固定大小（不随内容缩放：让窗口跟随 SwiftUI 内容尺寸会使 AppKit 布局循环崩溃）；`ignoresMouseEvents` 只在鼠标位于岛内时关闭，其余透明区域点击穿透。
- 面板是 `.nonactivatingPanel`，不抢焦点；只有填写拒绝原因时才 `makeKey`。
- 新请求出现后 0.7 秒内按钮不响应；高风险请求的「批准」是按住 1 秒的按钮。
- 新请求出现在鼠标所在屏幕。
