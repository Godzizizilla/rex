# 协议

两套协议都是 NDJSON（每行一个 JSON 对象）。时间戳为 Unix 毫秒，`[]byte` 字段为 base64。类型定义见 `internal/protocol/protocol.go`，当前版本 `Version = 2`。

## Agent 协议（rex ⇄ rexd，`~/.rex/rex.sock`）

一个连接对应一个请求。客户端先发一条请求消息，然后读取响应直到终态。

### 客户端消息 `AgentMsg`

| type | 字段 | 说明 |
|---|---|---|
| `exec`（旧名 `submit`） | `server` `desc` `command` `timeout?` `stdin?` | 执行命令 |
| `upload` | `server` `desc` `local_path` `remote_path` `data` | 上传文件（≤50MB） |
| `download` | `server` `desc` `remote_path` `local_path` | 下载文件，内容以 `output` 的 stdout 流返回 |
| `forward` | `server` `desc` `remote_port` `remote_host?` `local_port?` `ttl?`（分钟） | 端口转发 |
| `servers` | | 列出服务器（不需审批） |
| `policy` | `server` | 该服务器的白名单与高风险规则 |
| `forward_list` | | 活动的端口转发 |
| `forward_close` | `forward_id` | 关闭端口转发 |
| `cancel` | | 撤回当前请求；直接关闭连接效果相同 |

### 服务端消息 `AgentResp`

| type | 字段 | 说明 |
|---|---|---|
| `queued` | `id` | 等待人工审批 |
| `approved` | `id` `message` | `message` 为 `manual` 或 `whitelist`（自动审批也报告为 `manual`） |
| `output` | `stream` `data` | 原始输出字节块，`stream` 为 `stdout`/`stderr` |
| `completed` | `exit_code` | 终态：远端退出码 |
| `rejected` | `message` | 终态：拒绝原因 |
| `timeout` | | 终态：审批超时 |
| `cancelled` | `message` | 终态：被撤回 |
| `error` | `message` | 终态：参数错误、ssh 启动失败等 |
| `result` | `result` | 终态：查询结果，或端口转发信息（`Forward`） |

示例：

```
→ {"type":"exec","server":"prod","desc":"看日志","command":"tail -n 5 app.log"}
← {"type":"queued","id":7}
← {"type":"approved","id":7,"message":"manual"}
← {"type":"output","stream":"stdout","data":"bGluZTEK..."}
← {"type":"completed","id":7}
```

## 控制协议（Rex.app ⇄ rexd，stdin/stdout）

rexd 启动后先输出一条 `hello`，之后持续推送事件；App 随时写入命令。stdin 关闭即 rexd 退出。

### 事件 `ControlEvent`（rexd → App）

| type | 字段 | 说明 |
|---|---|---|
| `hello` | `version` `config` `state` `requests` | 初始快照（最近 500 条请求） |
| `request` | `request` | 请求新建或变化（执行中的输出每 0.5s 更新一次尾部） |
| `state` | `state` | 自动审批状态、端口转发列表变化 |
| `config` | `config` | 白名单等配置变化 |
| `history` | `requests` | 对 `history` 命令的回复 |
| `notice` | `message` | 提示信息（自动审批结束摘要、转发到期/异常退出等） |
| `result` | `seq` `ok` `error` | 命令执行结果 |

`Request` 的主要字段：`id` `kind` `server` `host` `desc` `timeout` `command` `stdin_size` `stdin_preview` `local_path` `remote_path` `size` `diff` `local_port` `remote_host` `remote_port` `ttl` `eval` `status` `approved_by` `held_by` `feedback` `error` `output` `output_size` `exit_code` `created_at` `deadline` `decided_at` `finished_at`。

`eval`（`policy.Evaluation`）：`kind` `target` `commands[]`（`raw` `name` `op` `nested` `whitelisted` `rule` `risk`）`constructs` `opaque` `risky_writes` `whitelisted` `rule` `risks`。

### 命令 `ControlCmd`（App → rexd）

| type | 字段 |
|---|---|
| `approve` | `id` |
| `reject` | `id` `feedback` |
| `approve_whitelist` | `id` `indices`（exec 请求要加入白名单的命令下标） |
| `auto_start` | `minutes` `servers`（空 = 全部） |
| `auto_stop` | |
| `forward_close` | `id` |
| `whitelist_add` | `rule` |
| `whitelist_remove` | `index` |
| `reload_config` | |
| `history` | `limit` |

每条命令可带 `seq`，rexd 以同样 `seq` 的 `result` 事件回复。

手动调试：

```bash
REX_HOME=/tmp/rexdev bin/rexd          # 在终端里直接输入命令
{"type":"approve","id":1}
```
