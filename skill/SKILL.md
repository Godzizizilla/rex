---
name: rex
description: Access remote servers (production, staging, any host managed by rex) through rex, a human-approved SSH gateway. Use it whenever you need to run a command, read logs, copy a file, or reach a database / admin console on a remote server. Never call ssh, scp or sftp directly.
---

# rex: human-approved remote access

`rex` behaves like `ssh`, except that a human approves each request in Rex.app
before it runs. Read-only commands on the whitelist run immediately.

## Discover first

```bash
rex servers          # servers you can use, with descriptions
rex policy prod      # what runs on "prod" without waiting for approval
```

## Run a command

```bash
rex -d "<what and why>" <server> -- <command...>
```

- `-d` is required. Write one sentence the reviewer can judge quickly, e.g.
  `-d "check why the API returns 502: last 200 lines of the error log"`.
- Put everything after `--`. Quote pipelines and operators so they run remotely:
  `rex -d "count 5xx" prod -- 'grep " 50[0-9] " /var/log/nginx/access.log | wc -l'`
- stdin is forwarded when you pipe into rex:
  `cat migrate.sql | rex -d "apply migration 042" prod -- psql app`
  Pass `-n` if stdin should not be forwarded.

### Output and exit codes

- On success rex is silent: stdout and stderr are exactly the remote command's,
  and the exit code is the remote exit code. Redirecting to a file
  (`rex ... -- cat x > x`) yields the exact remote bytes.
- Exit code **255** with a `rex: ...` line on stderr means the command did NOT
  run (or rex failed):
  - `rex: rejected by reviewer: <feedback>` - read the feedback, adjust, do not
    resubmit the same request.
  - `rex: approval timed out` - nobody approved in time. Tell the user what you
    were trying to do instead of retrying in a loop.
  - `rex: cannot connect to rexd` - Rex.app is not running; ask the user.

### Waiting for approval

A request that is not whitelisted blocks until a human decides (default up to
60s, `-t <seconds>` to change). Run rex with a tool timeout longer than the
approval timeout. If rex is killed while waiting, the request is withdrawn; if
it is killed while running, the remote command is terminated.

### Getting approved quickly

- Prefer read-only commands and the forms listed by `rex policy <server>`.
- A command line is auto-approved only if every command in it is whitelisted
  and it has no `$(...)`, backticks, redirects into files, loops, `&` or `eval`.
  Split complex work into simple commands.
- One focused request per step. Don't batch unrelated changes into one command.

## Copy files

```bash
rex cp -d "<why>" ./nginx.conf prod:/etc/nginx/nginx.conf   # upload (reviewer sees a diff)
rex cp -d "<why>" prod:/var/log/app/error.log ./            # download
```

## Reach a database or admin console (port forward)

```bash
rex forward -d "<why>" prod 5432                  # localhost:5432 on prod
rex forward -d "<why>" --ttl 2h prod 18080:10.0.0.5:8080
# -> 127.0.0.1:54321 -> localhost:5432 on prod (forward #3, expires 15:30; close with: rex forward close 3)
psql -h 127.0.0.1 -p 54321 ...
rex forward ls
rex forward close 3                               # close it when you are done
```

Forwards listen on 127.0.0.1 only and expire automatically. Traffic through a
forward is not reviewed, so treat it with the same care as the server itself.

## Rules

- Never use `ssh`, `scp`, `sftp` or `rsync` to these servers directly.
- Never try to work around a rejection (different wording, encoding, scripts
  piped into a shell). Ask the user instead.
