# Gilir

Gilir is a task queue for coding agents. Tasks live in a SQLite database owned
by a single background service. The service dispatches ready tasks to an
OpenCode V2 server, serves a web dashboard (loopback by default), and runs
hooks; the `gilir` CLI is an HTTP client for that service and never opens
SQLite itself. Each dispatch gets its own new session. A task that goes through
several review cycles keeps every earlier session in its history.

## Install

```sh
npm install -g @armedi/gilir
```

The installed command is `gilir` and requires Node.js 22 or newer. `npm install`
downloads and verifies the matching binary from GitHub Releases. If install
scripts are disabled or the download fails, the launcher tries on first run.
The binary includes the dashboard and is cached for offline use. Supported
platforms are macOS, Linux, and Windows on x64 and arm64.

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
GILIR-<n> --json`, so the binary must also be available where the OpenCode
server runs its agents, with `GILIR_URL` pointing at the service when it is not
using the default address.

### Publish to npm

Set the version in the root `package.json`, then tag that commit as
`v<version>` and push the tag. From macOS or Linux, build the assets, upload
them to GitHub Releases, and publish the npm launcher from the same checkout:

```sh
npm run build:release
tag="v$(node -p 'require("./package.json").version')" && gh release create "$tag" --verify-tag --generate-notes npm/dist/"$tag"/gilir-*
npm publish
```

`npm run build:release` embeds the dashboard in the six binaries and records
their SHA-256 checksums in `npm/release.json`. Publish from that same build so
the npm checksums match the uploaded binaries. Use `npm pack` to inspect
the launcher package locally without publishing.

## Run

```sh
gilir service start                  # detached; default http://127.0.0.1:44547
gilir service start --host 0.0.0.0   # accept connections from other machines
gilir service status
gilir service stop
gilir service restart

# A custom listener port needs its matching client URL:
gilir service start --port 9000
GILIR_URL=http://127.0.0.1:9000 gilir list
```

`service start` launches a detached worker and returns once it is serving.
`service restart` keeps the running host and port unless `--host` or `--port`
overrides them. The worker appends startup, warning, and
shutdown details to `<GILIR_DB>.service.log`, and publishes
`<GILIR_DB>.service.json` while it runs; stop and status use that record, so
they do not need the original flags. One service runs per database: a second
start on the same database is refused, or is a no-op when the requested
address matches.

Open <http://127.0.0.1:44547>. To reach the dashboard from another machine,
bind a wildcard address and visit `http://<machine-ip>:44547`:

```sh
gilir service start --host 0.0.0.0
```

There is no authentication and the API can create, change, and delete tasks,
and dispatched tasks run OpenCode agents on this machine. Treat an exposed port
as remote code execution on the host and only expose it on a trusted network.
When bound to a wildcard address the
server accepts request hosts that are IP literals (plus loopback); access by a
DNS name other than `localhost` is rejected to limit DNS rebinding. Process
shutdown is served only on a private loopback listener, so it is never exposed
with the UI.

The `service start` and `service restart` flags are the only way to choose the
bind address; the service never takes it from `GILIR_URL`. Every ordinary
command and queue control uses `GILIR_URL` only. The default `GILIR_URL` matches
the default listener, and a wildcard bind on the default port still works with
it. Any other port requires setting `GILIR_URL` to an address the service
accepts. If the service binds `[::1]` specifically, set `GILIR_URL` to that
address too, because the client default is IPv4 loopback.

The schema version changed when task IDs gained their `GILIR-` prefix. A
database created by an older Gilir is rejected with an error; delete the file
and restart the service to recreate it (task history is not migrated).

| Variable | Default | Meaning |
| --- | --- | --- |
| `GILIR_URL` | `http://127.0.0.1:44547` | HTTP origin of the service for every stateful CLI command and queue control. `http://` and `https://` are accepted; credentials, paths, queries, and fragments are rejected. It never chooses the service bind address. |
| `GILIR_DB` | `${XDG_DATA_HOME:-$HOME/.local/share}/gilir/gilir.sqlite` (Windows: `%LOCALAPPDATA%\gilir\gilir.sqlite`) | Database file for the background service. If missing, it is created with mode 0600 in a 0700 directory. An existing file keeps its mode. CLI commands never open it. |
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
request. `service status`, `--help`, and `gilir skill` use local state or no
service. Add `--json` to get machine-readable output on stdout.

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
gilir show GILIR-1 --json
gilir depend GILIR-2 GILIR-1                     # GILIR-2 waits for GILIR-1
gilir comment GILIR-1 "Repro in **ledger_test.go**" --actor agent:me
gilir edit-comment GILIR-1 12 "Narrowed to **rounding**" --actor agent:me
gilir remove-comment GILIR-1 12 --actor agent:me
gilir approve GILIR-1                            # or: gilir request-changes GILIR-1 --message "Add tests"
gilir service status
gilir daemon pause
gilir daemon resume
gilir daemon concurrency 2
gilir delete GILIR-1                             # keep the audit history
gilir --help
gilir skill                                      # agent workflow instructions
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
- A service bound to `0.0.0.0` or `::` accepts `GILIR_URL` addresses that are
  loopback or a concrete IP literal of the host. DNS names other than
  `localhost` are rejected. The Host/Origin checks still protect the API.
- Remote API authentication is not part of this client/flag integration. An
  exposed service is unauthenticated, so treat adding authentication as a
  separate follow-up and only expose the port on a trusted network.

SSH local forwarding works today and keeps the API loopback-only on the service
host. Use the same local and remote port, because the API validates the request
`Host` against the service's port:

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
  paused, the task has no open attempt, and every task it depends on is
  `in-review` or `done`.
- Ready tasks are ordered by priority (urgent, high, medium, low). Ties go
  first to `changes-requested` tasks, then to tasks with fewer blockers still in
  review, then to the lowest ID.
- `max_concurrency` (default 1) is compared with the parent (root) session
  runs Gilir sees on the OpenCode server. That count includes sessions you
  started by hand. Gilir only waits for free capacity. It never interrupts,
  steers or prompts sessions it did not start. If it cannot read OpenCode
  state, it dispatches nothing.
- Every dispatch creates a new OpenCode session. Sessions from earlier attempts
  and review cycles stay in the task's history.
- A dispatched task moves to `in-progress`. A successful session moves it to
  `in-review`. A failed session leaves it `in-progress` and marks it `error`.
  To retry, move it back to `todo`.
- `approve` moves an `in-review` task to `done`. `request-changes` requires a
  message, moves the task to `todo`, marks it `changes-requested`, and records
  the message as a comment. The same message requirement applies to
  `gilir update GILIR-<n> --status todo --message "..."` from `in-review`, because it
  applies the same marking. The store enforces both paths, so CLI and HTTP
  callers cannot request changes without a message.

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
- Failures are retried with backoff (up to 30s apart) and logged to stderr. A
  failing hook blocks only its own later events. To skip an event, fix the
  handler so it succeeds.
- Command hooks run with `GILIR_ACTOR=hook:<name>` and the service's own
  `GILIR_URL`, and OpenCode passwords are removed from their environment. A
  `gilir` change made from a hook is not delivered back to that same hook.
