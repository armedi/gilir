<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg" />
    <source media="(prefers-color-scheme: light)" srcset="assets/logo-light.svg" />
    <img alt="Gilir" src="assets/logo-light.svg" height="44" />
  </picture>
</h1>

Gilir is a task queue for coding agents. Tasks live in a SQLite database owned
by a single background service. The service dispatches ready tasks to an
OpenCode V2 server, serves a web dashboard (loopback by default), and runs
hooks; the `gilir` CLI is an HTTP client for that service and never opens
SQLite itself. Each dispatch gets its own new session. A task that goes through
several review cycles keeps every earlier session in its history.

## Install

Prebuilt binaries are available for macOS and Linux on x64 and arm64. Node.js is
not required for this path:

```sh
curl -fsSL https://raw.githubusercontent.com/armedi/gilir/main/install.sh | sh
```

The script installs the matching release binary, verifies its SHA-256 against the
release's `SHA256SUMS`, and replaces `<dir>/gilir` atomically. The default `<dir>`
is `$XDG_BIN_HOME` or `~/.local/bin`; it needs no `sudo` and prints a `PATH` hint
when the directory is not on `PATH`. Choose a different version or directory with
flags or environment variables:

```sh
curl -fsSL https://raw.githubusercontent.com/armedi/gilir/main/install.sh |
  sh -s -- --version 0.3.2 --dir "$HOME/bin"

GILIR_VERSION=0.3.2 GILIR_INSTALL_DIR="$HOME/bin" sh install.sh
```

Re-run the command to upgrade, then `gilir service restart` if the service is
running. To uninstall, delete `gilir` from that directory (`~/.local/bin/gilir`
by default). Releases before `SHA256SUMS` existed are verified against the
per-asset digest the GitHub API reports, so v0.3.2 installs too.

Windows is not covered by this script. Use npm below, or download
`gilir-win32-<arch>.exe` and `SHA256SUMS` from the release and compare
`(Get-FileHash .\gilir-win32-x64.exe).Hash` with the file's line for that binary.

Or install the npm launcher (macOS, Linux, and Windows):

```sh
npm install -g @armedi/gilir
```

The installed command is `gilir` and requires Node.js 22 or newer. `npm install`
downloads and verifies the matching binary from GitHub Releases. If install
scripts are disabled or the download fails, the launcher tries on first run.
The binary includes the dashboard and is cached for offline use. Supported
platforms are macOS, Linux, and Windows on x64 and arm64.

Both paths install the same binary. Do not keep both on `PATH`: the first
`gilir` found wins, and the other copy goes stale. Uninstall the npm one with
`npm uninstall -g @armedi/gilir` when switching to the script.

### Agent skill

Install the Gilir skill so coding agents pick up the manual whenever a request
mentions gilir or a `GILIR-<n>` task:

```sh
npx skills add armedi/gilir
```

The skill points agents at `gilir skill`, which prints the manual embedded in
the installed binary, so it stays in step with the version you run.

## Build

Requirements: Go 1.24+, Node.js `^22.18.0 || ^24.11.0 || >=26.0.0`, and pnpm
10.34.9 (the version pinned by `web/package.json`; Corepack or a global Vite+
`vp env` can provide it). The web build installs the Vite+ 1.8.0 CLI as a
development dependency.

```sh
(cd web && pnpm install --frozen-lockfile --prod=false && pnpm build)   # writes web/dist
go build -tags=embedweb -o gilir ./cmd/gilir
```

For a Go-only install, `go install github.com/armedi/gilir/cmd/gilir@latest`
builds without an embedded dashboard.

The explicit `--prod=false` keeps the Vite+ development dependencies installed
when `NODE_ENV=production`. Run the CLI from that install as `pnpm exec vp` in
`web` when you need it directly.

Put `gilir` on your `PATH`. The service prompts each agent to run `gilir show
GILIR-<n>`, so the binary must also be available where the OpenCode
server runs its agents, with `GILIR_URL` pointing at the service when it is not
using the default address.

### Publish a release

Set the version in the root `package.json`, then tag that commit as
`v<version>` and push the tag. From macOS or Linux, build the assets, upload
them to GitHub Releases, and publish the npm launcher from the same checkout:

```sh
npm run build:release
tag="v$(node -p 'require("./package.json").version')" && gh release create "$tag" --verify-tag --generate-notes npm/dist/"$tag"/gilir-* npm/dist/"$tag"/SHA256SUMS
npm publish
```

`npm run build:release` embeds the dashboard in the six binaries, stamps each
with `package.json`'s version so `gilir --version` reports it, records their
SHA-256 checksums in `npm/release.json` for the npm launcher, and writes the
same checksums as `npm/dist/v<version>/SHA256SUMS` for `install.sh`. Upload all
of them from the same build so every checksum matches the uploaded binaries; the
`--check` step and `install.sh` both refuse a build or release without a matching
`SHA256SUMS`. Use `npm pack` to inspect the launcher package locally without
publishing. The package ships the npm launcher, its downloader, and
`release.json`; `install.sh` is served from `main`, so
installer fixes do not need a release.

## Run

```sh
gilir serve                          # foreground; default http://127.0.0.1:44547
gilir serve --host 0.0.0.0           # foreground, accept remote connections

gilir service start                  # detached; default http://127.0.0.1:44547
gilir service start --host 0.0.0.0   # accept connections from other machines
gilir service status
gilir service stop
gilir service restart

# A custom listener port needs its matching client URL:
gilir service start --port 9000
GILIR_URL=http://127.0.0.1:9000 gilir list
```

`gilir serve` runs the HTTP API, dashboard, watcher, and hooks in the current
process. It logs to `stderr`, writes nothing to `stdout`, and exits 0 on
`SIGINT`/`SIGTERM`. It refuses `--json`, takes `--host`, `--port`, and `--db`,
and holds `<GILIR_DB>.lock` while it runs. It never touches the managed-service
files, so several `gilir serve` instances may run alongside the managed service
as long as each uses its own database and port.

`service start` launches a detached worker and returns once it is serving.
`service restart` keeps the running host, port, and database unless `--host`,
`--port`, or `--db` overrides them. The managed service is a singleton per
user: its state, log, and operation lock live in the Gilir data directory
(`<dataDir>/gilir/service.json`, `service.log`, `service.lock`, where `dataDir`
is `$XDG_DATA_HOME`/`~/.local/share` on Unix and `%LOCALAPPDATA%` on Windows).
`service.json` records the canonical `db_path`; `service status` and
`service start` report it. One managed service runs per user: a second start is
a no-op only when the requested `--host`, `--port`, and resolved `--db` match
the running one, and otherwise asks you to run `gilir service stop` first.

Open <http://127.0.0.1:44547>. To reach the dashboard from another machine,
bind a wildcard address and visit `http://<machine-ip>:44547` or
`http://<hostname>:44547`.

There is no authentication and the API can create, change, and delete tasks,
and dispatched tasks run OpenCode agents on this machine. Treat an exposed port
as remote code execution on the host and only expose it on a trusted network.
Process shutdown is served only on a private loopback listener.

### Tailscale Serve

To use MagicDNS and HTTPS while keeping Gilir on loopback:

```sh
gilir service start
tailscale serve --bg --https=44547 http://127.0.0.1:44547
```

Open the URL printed by `tailscale serve` from your tailnet. Tailscale
terminates HTTPS and forwards requests directly to Gilir. Local HTTP clients
continue to use `http://127.0.0.1:44547`. If you previously started Gilir on a
non-loopback address, run `gilir service restart --host 127.0.0.1` first. Use
Serve rather than Funnel for tailnet-only access.

### Service configuration

The `serve`/`service start`/`service restart` flags are the only way to choose
the bind address; the service never takes it from `GILIR_URL`. Every ordinary
command uses `GILIR_URL` only. The host-local queue controls
(`service pause|resume|concurrency`) read `<dataDir>/gilir/service.json` and
reach the managed service directly, so they only work on the host running it.

The default `GILIR_URL` matches the default listener, and a wildcard bind on the
default port still works with it. Any other port requires setting `GILIR_URL` to
an address the service accepts. If the service binds `[::1]` specifically, set
`GILIR_URL` to that address too, because the client default is IPv4 loopback.

The schema is not migrated. A database file from any other schema version is
refused at startup with an error naming the version; delete the file and
restart the service to recreate it. Task history is not carried across schema
versions.

| Variable | Default | Meaning |
| --- | --- | --- |
| `GILIR_URL` | `http://127.0.0.1:44547` | HTTP origin of the service for every stateful CLI command. Host-local `service` lifecycle and queue-control commands ignore it. `http://` and `https://` are accepted; credentials, paths, queries, and fragments are rejected. It never chooses the service bind address. |
| `GILIR_DB` | `${XDG_DATA_HOME:-$HOME/.local/share}/gilir/gilir.sqlite` (Windows: `%LOCALAPPDATA%\gilir\gilir.sqlite`) | Database file for the background service. If missing, it is created with mode 0600 in a 0700 directory. An existing file keeps its mode. CLI commands never open it. The `--db` flag on `gilir serve`, `service start`, and `service restart` overrides it. |
| `GILIR_WEB_DIR` | Embedded dashboard, or `web/dist` for Go-only builds | Override the static web bundle. If the selected directory has no `index.html`, Gilir serves only the API. |
| `GILIR_OPENCODE_URL` | discovered from `opencode service status` | OpenCode V2 server. It must be on loopback. Set it to pin an explicit server. |
| `OPENCODE_PASSWORD` | discovered from `opencode service get password` | Sent as HTTP Basic auth (user `opencode`). Set it to pin an explicit password. |
| `GILIR_HOOKS` | unset | Path to a hook config file used by the service (see below). |
| `GILIR_ACTOR` | `user` | Default actor recorded for CLI changes. `--actor` overrides it. |

### Connecting OpenCode

By default Gilir discovers the local managed OpenCode service through the
`opencode` CLI: `opencode service status` for the URL and `opencode service get
password` for the Basic auth password. Nothing needs to be set. Gilir never
starts OpenCode; when it is not running the Gilir service stays up, dispatches
nothing, and `gilir service status` reports `OpenCode is not running`. It
connects on its own once OpenCode starts, and re-reads the password if the
service restarts and rotates it.

To pin an explicit server or password, set `GILIR_OPENCODE_URL` and/or
`OPENCODE_PASSWORD`. Each unset variable is still discovered. This is useful
for a remote or hand-started server, where you can read the password without
printing it into your shell history:

```sh
read -rsp 'OpenCode password: ' OPENCODE_PASSWORD && echo && export OPENCODE_PASSWORD
```

The CLI and API still work when OpenCode cannot be reached. In that case
`gilir service status` shows `active_parents: null` and a `watcher_error`. Once
connected, each dispatched task shows a session link in the dashboard. The
link opens the session in OpenCode Web.

## CLI

Stateful commands send one POST to the service's command API at `GILIR_URL`, so
the service must be running. If it cannot be reached, the command exits with an
error naming the target and never opens a local database or retries the
request. `gilir serve` runs the service itself. `service start|stop|restart|status`
and the host-local queue controls (`service pause|resume|concurrency`) read the
local service record and never use `GILIR_URL`; `--help`, `gilir skill`, and
`gilir version` use no service. Add `--json` to get machine-readable output on
stdout.

```sh
gilir project create Payments                    # -> id 1
gilir repo add ~/src/payments --project 1        # -> id 1
gilir repo add ~/src/ledger                      # -> auto-creates project "ledger"
gilir repo list                                  # every repo with its project_ids
gilir repo link 2 1                              # share repo 2 with project 1
gilir repo unlink 2 1                            # remove that link
gilir create "Fix rounding" --repo 1 --status todo --priority high   # -> GILIR-1
gilir create "Ship release notes" --repo 1 --project 1 --status todo   # -> GILIR-2
gilir list --status todo                         # tasks across all projects
gilir list --project 1 --status todo
gilir show GILIR-1                               # task, blockers, sessions, activity
gilir show GILIR-1 --json
gilir depend GILIR-2 GILIR-1                     # GILIR-2 waits for GILIR-1
gilir comment GILIR-1 "Repro in **ledger_test.go**" --actor agent:me
gilir block GILIR-1 --message "Waiting on **ledger**"   # stop dispatch, keep dependents held
gilir unblock GILIR-1 --message "Migration landed"      # optionally re-queue immediately
gilir edit-comment GILIR-1 12 "Narrowed to **rounding**" --actor agent:me
gilir remove-comment GILIR-1 12 --actor agent:me
gilir approve GILIR-1                            # or: gilir request-changes GILIR-1 --message "Add tests"
gilir service status
gilir service pause
gilir service resume
gilir service concurrency 2
gilir delete GILIR-1                             # keep the audit history
gilir --help
gilir skill                                      # embedded agent manual
gilir --version                                  # or: gilir -v, gilir version
```

Task IDs carry a `GILIR-` prefix, for example `GILIR-1`. The prefix is stored
with the task, appears in JSON and the dashboard, and is used verbatim in the
agent prompt, so a `GILIR-` reference always identifies Gilir task management.
Task commands accept only that form; `gilir show 1` is an error. Project,
repository, and comment IDs stay bare positive integers.

`gilir delete GILIR-<n>` removes the task, its terminal dispatch attempts and
local sessions, and its outgoing dependencies. The append-only activity history
is preserved. Deletion is refused while a dispatch attempt is reserved or
another task still depends on the task.

### Remote and central operation

SQLite, repository checkouts, OpenCode, and command hooks are all central to the
service host. The CLI is only a client, so it can run anywhere that can reach
that service.

- `gilir repo add` sends the path you type. The service resolves it on its own
  filesystem and runs Git there, so use an absolute path on the service host.
  Relative paths resolve from the service's working directory.
- OpenCode agents run under the OpenCode server, not under the Gilir service.
  Where `GILIR_URL` is not the default, set it in the OpenCode server's launch
  environment so dispatched agents reach the same service; `gilir` must also be
  on their `PATH`.
- Command hooks are started by the service and receive that service's own
  `GILIR_URL` automatically, replacing any inherited value. A hook that invokes
  `gilir` therefore reaches the service that delivered its event, including on
  a custom port or a wildcard bind.
- Any supplied browser `Origin` must match the request host and scheme.
- Remote API authentication is not part of this client/flag integration. An
  exposed service is unauthenticated, so treat adding authentication as a
  separate follow-up and only expose the port on a trusted network.

SSH local forwarding works today and keeps the API loopback-only on the service
host:

```sh
# Service on the default port, CLI on this machine using the default GILIR_URL:
ssh -N -L 127.0.0.1:44547:127.0.0.1:44547 gilir-host
gilir list

# Service started with --port 9000:
ssh -N -L 127.0.0.1:9000:127.0.0.1:9000 gilir-host
GILIR_URL=http://127.0.0.1:9000 gilir list
```

## Queue semantics

- Only `todo` tasks are dispatched. A task is ready when the queue is not
  paused, the task is not `blocked`, the task has no open attempt, and every
  task it depends on is `in-review` or `done`.
- Ready tasks are ordered by priority (urgent, high, medium, low). Ties go first
  to `changes-requested` and just-unblocked tasks, then to tasks with fewer
  blockers still in review, then to the lowest ID.
- `max_concurrency` (default 1) is compared with the parent (root) session
  runs Gilir sees on the OpenCode server. That count includes sessions you
  started by hand. Gilir only waits for free capacity. It never interrupts,
  steers or prompts sessions it did not start. If it cannot read OpenCode
  state, it dispatches nothing.
- Every dispatch gets its own OpenCode session inside the task's worktree.
  Sessions from earlier attempts and review cycles stay in the task's history.
  See [Worktrees](#worktrees).
- A dispatched task moves to `in-progress`. A successful session moves it to
  `in-review`, unless the task is `blocked`, in which case it stays
  `in-progress` so its dependents remain held. A failed session leaves it
  `in-progress` and marks it `error`. To retry, move it back to `todo`.
- `block` requires a message, records it as a comment, and marks the task
  `blocked`; it is refused for `in-review` and `done` tasks. `unblock` clears
  the marking and optionally records a message. Unblocking an idle
  `in-progress` task moves it back to `todo` and re-queues it ahead of other
  tasks at the same priority; an actively running task keeps running.
- `approve` moves an `in-review` task to `done`. `request-changes` requires a
  message, moves the task to `todo`, marks it `changes-requested`, and records
  the message as a comment. The same message requirement applies to
  `gilir update GILIR-<n> --status todo --message "..."` from `in-review`, because it
  applies the same marking. The store enforces both paths, so CLI and HTTP
  callers cannot request changes without a message.

## Worktrees

Each task owns one OpenCode worktree, created on its first dispatch through
`POST /api/worktree` and passed to the session as its location. The directory
name includes this database's identifier, so a recreated database cannot adopt
the previous one's retained worktrees. Gilir records the directory in SQLite
before it creates the session, so a watcher restart or retry reuses the same
worktree instead of leaking another one. Review cycles and retries reuse it
too, and the agent picks its own branch inside it — Gilir does not create or
track branches.

If a recorded worktree has disappeared or is no longer a directory by the time
the task resumes, the attempt fails with an error naming the path instead of
silently creating a replacement. Restore it, or recreate the task. Gilir never
deletes worktrees, not even when a task is `done` or deleted; clean them up
yourself.

Gilir never migrates a database across schema versions: a file from any other
version is refused at startup naming that version. The worktree rules below
describe a database that carries task history; after a schema change the file is
recreated empty.

An existing OpenCode session under the next attempt's deterministic ID but in
another directory fails the attempt closed, so a stale session stays visible
instead of blocking the queue.

## Hooks

`GILIR_HOOKS` points to a JSON array. Each hook has a `name` and exactly one
of `command` (an argv array, run without a shell, that receives the event on
stdin) or `url` (receives a POST with the event as the JSON body). `events`
is optional. It defaults to the task, dependency and session events.

```json
[
  {"name": "notify", "events": ["task.commented", "task.status_changed"], "command": ["/usr/local/bin/notify-gilir"]},
  {"name": "ci", "url": "http://127.0.0.1:9000/gilir"}
]
```

- A new hook starts at the current latest event. It does not replay history.
- Delivery is at least once. A per-hook cursor advances only after success, so
  handlers must be idempotent. Use the `id` in the payload, the
  `X-Gilir-Event-ID` header or `GILIR_EVENT_ID` to drop duplicates.
- `task.commented` carries a person's wording under `data.message`; `body` is
  reserved for `task.body`. `task.created` records only `repo_id`, `title`,
  `status` and `priority`, and `task.updated` carries `old.body`/`new.body`. A
  handler that needs the task text should read it by `task_id`; that returns the
  current body, not the value at creation, which no event preserves.
- Failures are retried with backoff (up to 30s apart) and logged to stderr. A
  failing hook blocks only its own later events. To skip an event, fix the
  handler so it succeeds.
- Command hooks run with `GILIR_ACTOR=hook:<name>` and the service's own
  `GILIR_URL`, and OpenCode passwords are removed from their environment. A
  `gilir` change made from a hook is not delivered back to that same hook.
