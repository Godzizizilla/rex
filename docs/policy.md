# 审批策略：白名单、高风险规则与临时自动审批

每个请求在提交时由 `internal/policy` 评估，得到三类信息：

- **Whitelisted**：白名单是否完整覆盖了这个请求。覆盖则立即执行，不弹窗。
- **Constructs**：命令里出现了哪些不允许免审批的结构（命令替换、写文件重定向、循环……）。只要有一项，就不能走白名单。
- **Risks**：高风险命中、无法分析的部分、写入系统路径等。有风险的请求在弹窗中标红、需要按住批准，自动审批模式也不会放行。

评估结果完整地展示在弹窗里：每条子命令用绿（白名单）、橙（需审批）、红（高风险）标出。

## 命令解析

命令用 [mvdan.cc/sh](https://github.com/mvdan/sh) 按 bash 语法解析成 AST，而不是按字符串切分：

| 输入 | 解析结果 |
|---|---|
| `a \| b`、`a && b`、`a \|\| b`、`a; b`、换行 | 拆成多条简单命令，逐条匹配白名单 |
| `(a; b)`、`{ a; b; }` | 展开内部命令，逐条匹配 |
| `cmd 2>&1`、`cmd 2>/dev/null`、`cmd < in.txt` | 允许，不影响白名单 |
| `cmd > file`、`cmd >> file` | 结构：写文件。写到 `/tmp`、`/var/tmp` 以外的绝对路径还算风险 |
| `$(...)`、反引号、`<(...)` | 结构：命令替换，内部命令也会被分析（用于风险判断） |
| `cmd &` | 结构：后台任务 |
| `if`/`for`/`while`/`case`/函数/`[[ ]]`/`export` 等 | 结构，内部命令也会被分析 |
| `bash -c '...'`、`sh -lc "..."`、`eval '...'` | 结构，并递归分析字符串里的脚本；脚本是变量时视为无法分析 |
| `bash -s` / `bash` + stdin、`bash <<EOF` | 结构，分析 stdin 或 heredoc 中的脚本；来源未知（如 `curl ... \| bash`）视为无法分析 |
| `$CMD args` | 无法分析：命令名是动态的 |
| `sudo`/`env`/`xargs`/`timeout`/`nohup`/`nice`/… | 判断风险时剥掉这些包装，看实际执行的命令（`sudo rm -rf x`、`xargs rm` 都会命中 `rm *`） |

## 白名单

```yaml
whitelist:
  - pattern: "tail *"          # type 省略即 exec
    servers: ["*"]             # 服务器别名，* 表示全部
    note: "允许 tail"
  - type: download             # rex cp 下载：匹配远端路径
    pattern: "/var/log/*"
    servers: ["*"]
  - type: upload               # rex cp 上传：匹配远端路径
    pattern: "/tmp/*"
    servers: ["staging"]
  - type: forward              # rex forward：匹配 host:port
    pattern: "localhost:5432"
    servers: ["staging"]
```

- `*` 匹配任意字符（包括空格和 `/`）。`name *` 同时匹配不带参数的 `name`。
- exec 规则匹配的是**每条简单命令**的原文（不含重定向）。一条命令行被白名单放行，必须：每条简单命令都匹配某条规则，且没有任何「结构」或「无法分析」项。
- 在弹窗点「加白名单并批准…」会为勾选的子命令生成 `命令名 *` 规则（仅限当前服务器）；下载/上传生成 `目录/*`，转发生成 `host:port`。

### 不要加进白名单的命令

下列命令本身会执行别的命令或任意代码，加入 `xxx *` 等于放行一切：

`bash`、`sh`、`zsh`、`python`、`perl`、`node`、`ruby`、`eval`、`exec`、`source`、`xargs`、`sudo`、`env`、`nohup`、`timeout`、`watch`、`time`、`/usr/bin/time`、`find`（`-exec`/`-delete`）、`awk`（`system()`）、`ssh`、`docker exec`、`kubectl exec`。

`cat *` 这类只读规则可以放心添加；`sed *` 要注意 `-i` 会修改文件。

## 高风险规则（blocklist）

内置规则见 `internal/policy/policy.go` 中的 `DefaultBlocklist`，覆盖删除文件、磁盘操作、关机重启、杀进程、停止/重启服务、防火墙、用户管理、容器删除、强制推送，以及 SQL 的 `drop table`、`truncate table`、`delete from`、Redis `flushall` 等。规则不区分大小写；以 `*` 开头的内容规则（如 `*drop table*`）还会匹配通过 stdin 传入的数据。

```yaml
auto_approve:
  blocklist:                     # 追加自己的规则
    - "deploy.sh *"
    - "*migrate*"
  disable_default_blocklist: false
```

高风险规则不影响白名单：如果你把 `rm *` 加进白名单，它仍会直接执行（只是会在历史里看到风险标记）。

## 临时自动审批

在菜单栏或主窗口开启，选择时长（上限 `auto_approve.max_minutes`，默认 240 分钟）和服务器范围。开启期间：

| 请求 | 处理 |
|---|---|
| 白名单覆盖 | 照常直接执行 |
| 无风险（无高风险命中、可完整分析、不写系统路径） | **自动通过**，历史标记为「自动审批」 |
| 有风险 / 不在所选服务器范围 | 照常挂起，弹窗中注明原因；无人处理则按超时返回给 Agent |

安全措施：

- 只存在于 rexd 内存中，到期、手动停止、rexd 重启都会关闭。
- 熔断：每分钟自动通过的请求数超过 `max_per_minute`（默认 30），或自动通过的命令连续失败 `max_consecutive_failures` 次（默认 5），立即关闭并通知。
- 结束时弹出摘要：期间自动通过多少、挂起多少。
- Agent 看不出当前是否处于自动审批模式（返回和人工批准一致），避免 Agent 因此「放开手脚」。

```yaml
auto_approve:
  max_minutes: 240
  max_per_minute: 30
  max_consecutive_failures: 5
```

## 端口转发

```yaml
forward:
  default_ttl_minutes: 30
  max_ttl_minutes: 240
```

转发由 rexd 以 `ssh -N -L 127.0.0.1:<本地端口>:<host>:<port>` 维持，只监听本机回环地址，到期、手动关闭（菜单栏 / `rex forward close`）或 rexd 退出时关闭。只支持 `-L`。注意：转发建立后，经过隧道的流量（例如对数据库执行的 SQL）不再经过审批。

## 已知限制

- 目标是**防止误操作**，不是对抗恶意 Agent：Agent 与 rexd 以同一用户运行，理论上能读写 `~/.rex/config.yaml`，如果它能直接调用 `ssh` 也能绕过 rex。请在 Agent 的权限配置中禁止直接使用 `ssh`/`scp`。
- 上传请求在审批**之前**会以只读方式读取一次远端文件（`head -c`）用于生成 diff。
- 取消执行中的命令会终止本地 ssh 进程；远端进程在下次写输出时收到 SIGPIPE 退出，不产生输出的进程（如 `sleep`）可能继续运行到结束。
