# Gilir

Gilir is a local task queue for coding agents. Tasks live in a local SQLite
database. A single daemon dispatches ready tasks to an OpenCode V2 server and
serves a web dashboard on loopback. Each dispatch gets its own new session.
A task that goes through several review cycles keeps every earlier session in
its history.

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

Put `gilir` on your `PATH`. The daemon prompts each agent to run `gilir show
GILIR-<n> --json`, so the binary and the same `GILIR_DB` must also be available
in the environment where the OpenCode server runs its agents.

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
gilir daemon run      # stays in the foreground; stop it with Ctrl-C or SIGTERM
gilir daemon run --port 9000   # override the default port for this run
```

Open <http://127.0.0.1:44547>. Startup details go to stderr. One daemon runs per
database: a second `daemon run` on the same database exits with an error.

The schema version changed when task IDs gained their `GILIR-` prefix. A
database created by an older Gilir is rejected with an error; delete the file
and restart the daemon to recreate it (task history is not migrated).

| Variable | Default | Meaning |
| --- | --- | --- |
| `GILIR_DB` | `${XDG_DATA_HOME:-$HOME/.local/share}/gilir/gilir.sqlite` (Windows: `%LOCALAPPDATA%\gilir\gilir.sqlite`) | Database file. If missing, it is created with mode 0600 in a 0700 directory. An existing file keeps its mode. |
| `GILIR_LISTEN` | `127.0.0.1:44547` | HTTP address. The host must be `127.0.0.1` or `[::1]`. `gilir daemon run --port` overrides only the port. |
| `GILIR_WEB_DIR` | Embedded dashboard, or `web/dist` for Go-only builds | Override the static web bundle. If the selected directory has no `index.html`, Gilir serves only the API. |
| `GILIR_OPENCODE_URL` | discovered from `opencode service status` | OpenCode V2 server. It must be on loopback. Set it to pin an explicit server. |
| `OPENCODE_PASSWORD` | discovered from `opencode service get password` | Sent as HTTP Basic auth (user `opencode`). Set it to pin an explicit password. |
| `GILIR_HOOKS` | unset | Path to a hook config file (see below). |
| `GILIR_ACTOR` | `user` | Default actor recorded for CLI changes. `--actor` overrides it. |

### Connecting OpenCode

By default Gilir discovers the local managed OpenCode service through the
`opencode` CLI: `opencode service status` for the URL and `opencode service get
password` for the Basic auth password. Nothing needs to be set. Gilir never
starts OpenCode; when the service is not running the daemon stays up, dispatches
nothing, and `gilir daemon status` reports `OpenCode is not running`. It
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
`gilir daemon status` shows `active_parents: null` and a `watcher_error`. Once
connected, each dispatched task shows a session link in the dashboard. The
link opens the session in OpenCode Web.

## CLI

Commands work directly on the database, so they also work when the daemon is
stopped. The exception is `daemon status`, which asks the running daemon. Add
`--json` to get machine-readable output on stdout.

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
gilir daemon status
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
- Command hooks run with `GILIR_ACTOR=hook:<name>`, and OpenCode passwords are
  removed from their environment. A `gilir` change made from a hook is not
  delivered back to that same hook.
