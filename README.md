# Rex

Rex 是给 Coding Agent 用的「人工审批 SSH 网关」。Agent 不直接拿到 SSH 权限，而是通过 `rex` 提交远程命令、文件传输或端口转发请求；白名单内的请求立即执行，其余请求在 macOS 的灵动岛弹窗里由人批准或拒绝。

```
Agent ──rex CLI──▶ ~/.rex/rex.sock ──▶ rexd ──ssh──▶ 服务器
                                        ▲
                         stdin/stdout   │  审批 / 白名单 / 自动审批
                                        ▼
                                    Rex.app（菜单栏 + 灵动岛）
```

## 功能

- **像 ssh 一样用**：`rex -d "原因" prod -- tail -n 100 app.log`。成功时输出与远端逐字节一致、退出码即远端退出码；只有 rex 自身失败（被拒绝、审批超时、rexd 未运行）时才输出 `rex: ...` 并以 255 退出。
- **stdin、文件、端口转发**：`cat x.sql | rex ... -- psql`、`rex cp`（上传时审批界面显示 diff）、`rex forward`（只监听 127.0.0.1，到期自动关闭）。
- **可靠的白名单**：用真正的 shell 解析器（mvdan.cc/sh）拆分命令，换行、`&`、`$(...)`、反引号、重定向等都无法夹带命令绕过 `tail *` 这类规则。
- **灵动岛审批**：带刘海的 MacBook 从刘海展开，外接显示器在菜单栏中央显示同形弹窗；出现在鼠标所在屏幕，带倒计时和提示音；高风险命令需按住 1 秒批准，防误触。
- **临时自动审批**：休息时开 15 分钟到 4 小时，无风险请求自动通过；命中高风险规则的请求仍然挂起，超时返回给 Agent；请求过多或连续失败自动熔断；回来时给出摘要。
- **Agent 自我发现**：`rex servers`、`rex policy <server>`，以及内置的 Claude Code Skill（`rex skill install`）。

## 安装

依赖：macOS 14+、Go 1.23+、Xcode 命令行工具（Swift 5.10+）。

```bash
make install        # rex、rexd → /usr/local/bin（Agent 调用的 CLI）
make install-app    # Rex.app → /Applications（内含 rexd）
rex skill install   # 安装 Claude Code Skill 到 ~/.claude/skills/rex
```

然后打开 Rex.app（菜单栏出现盾牌图标）。在「设置」里可以开启登录时启动。

> 从旧版升级：旧版 rexd 是终端 TUI，请先退出它（`q`），否则 Rex.app 会提示 “another rexd is already running”。配置文件 `~/.rex/config.yaml` 无需修改。

## 配置

`~/.rex/config.yaml`（可用 `REX_HOME` 环境变量换目录），完整示例见 [docs/config.example.yaml](docs/config.example.yaml)：

```yaml
servers:
  prod:
    host: deploy@10.0.1.100      # 也可以是 ~/.ssh/config 里的别名
    description: "生产环境 API"
  staging:
    host: admin@staging.example.com
    port: 2222

timeout: 60                       # 默认审批超时（秒）

whitelist:
  - pattern: "tail *"
    servers: ["*"]
    note: "允许 tail"
  - type: download                # exec（默认）/ download / upload / forward
    pattern: "/var/log/*"
    servers: ["*"]
  - type: forward
    pattern: "localhost:5432"
    servers: ["staging"]
```

白名单、高风险规则和自动审批的规则详见 [docs/policy.md](docs/policy.md)。

## Agent 使用方式

```bash
rex servers                                              # 可用服务器
rex policy prod                                          # 哪些操作免审批
rex -d "查看最近错误" prod -- tail -n 200 /var/log/app/error.log
rex -d "统计 5xx" prod -- 'grep " 50[0-9] " access.log | wc -l'
cat migrate.sql | rex -d "执行迁移 042" prod -- psql app
rex cp -d "更新 nginx 配置" ./nginx.conf prod:/etc/nginx/nginx.conf
rex cp -d "拉取日志" prod:/var/log/app/error.log ./
rex forward -d "排查慢查询" prod 5432                    # → 127.0.0.1:<端口>
rex forward ls / rex forward close <id>
```

| 退出码 | 含义 |
|---|---|
| 远端退出码 | 命令已执行（包括 ssh 自身连接失败时的 255） |
| 255 + `rex: rejected by reviewer: <原因>` | 被拒绝，未执行 |
| 255 + `rex: approval timed out` | 无人审批，未执行 |
| 255 + `rex: cannot connect to rexd` | Rex.app 未运行 |

- `-d` 必填，会展示给审批人；`-t` 设置审批超时；`-n` 不转发 stdin；`-v` 输出排队/批准等过程信息。
- stdin 只在是管道或文件时才会读取（Agent 工具里的 `/dev/null`、终端不会被读）。
- rex 被中断（Ctrl-C、工具超时被杀）时，待审批请求自动撤回，执行中的命令会被终止。
- 旧写法 `rex -s prod -c "cmd" -d "..."` 仍然可用。

给 Agent 的完整说明见 [skill/SKILL.md](skill/SKILL.md)。

## App 使用

- **灵动岛**：新请求自动展开。「批准」/「拒绝…」（填写的原因会反馈给 Agent）/「加白名单并批准…」（勾选要加入白名单的子命令）。点击顶部条或 ⌃⌥R 展开/收起，多个请求可翻页。
- **快捷键**：⌃⌥Y 批准当前请求（高风险请求不响应，需在弹窗中按住批准）、⌃⌥N 拒绝并填写原因、⌃⌥R 展开/收起。
- **菜单栏**：待审批数、临时自动审批（15 分钟～2 小时，更多选项在主窗口）、端口转发列表。
- **主窗口**（菜单栏「打开 Rex…」或再次打开 App）：请求历史与输出、白名单管理、自动审批、端口转发、设置。

## 开发

```bash
make test           # go vet + go test -race
make build          # bin/rex、bin/rexd
make app            # build/Rex.app
```

- 架构说明：[docs/architecture.md](docs/architecture.md)
- 协议说明：[docs/protocol.md](docs/protocol.md)
- 调试 App：`REXD_PATH=bin/rexd swift run --package-path app` 使用指定的 rexd；设置 `REX_SNAPSHOT_DIR=<目录>` 时，灵动岛与主窗口会渲染成 PNG 写入该目录（不需要录屏权限）。
- 测试时可用 `REX_HOME=/tmp/xxx` 隔离配置、socket 和历史，不影响正在运行的 rexd。
