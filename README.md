# Gilir

Gilir is a local task queue for coding agents. Tasks live in a local SQLite
database. A single daemon dispatches ready tasks to an OpenCode V2 server and
serves a web dashboard on loopback. Each dispatch gets its own new session.
A task that goes through several review cycles keeps every earlier session in
its history.

## Build

Requirements: Go 1.24+, Node.js `^22.18.0 || ^24.11.0 || >=26.0.0`, and pnpm
10.34.9 (the version pinned by `web/package.json`; Corepack or a global Vite+
`vp env` can provide it). The web build installs the Vite+ 1.8.0 CLI as a
development dependency.

```sh
(cd web && pnpm install --frozen-lockfile --prod=false && pnpm build)   # writes web/dist
go build -o gilir ./cmd/gilir
```

The explicit `--prod=false` keeps the Vite+ development dependencies installed
when `NODE_ENV=production`. Run the CLI from that install as `pnpm exec vp` in
`web` when you need it directly.

Put `gilir` on your `PATH`. The daemon prompts each agent to run `gilir show
<id> --json`, so the binary and the same `GILIR_DB` must also be available in
the environment where the OpenCode server runs its agents.

## Run

```sh
gilir daemon run      # stays in the foreground; stop it with Ctrl-C or SIGTERM
```

Open <http://127.0.0.1:4718>. Startup details go to stderr. One daemon runs per
database: a second `daemon run` on the same database exits with an error.

| Variable | Default | Meaning |
| --- | --- | --- |
| `GILIR_DB` | `${XDG_DATA_HOME:-$HOME/.local/share}/gilir/gilir.sqlite` (Windows: `%LOCALAPPDATA%\gilir\gilir.sqlite`) | Database file. If missing, it is created with mode 0600 in a 0700 directory. An existing file keeps its mode. |
| `GILIR_LISTEN` | `127.0.0.1:4718` | HTTP address. The host must be `127.0.0.1` or `[::1]`. |
| `GILIR_WEB_DIR` | `web/dist` | Static web bundle. Gilir serves only the API if `index.html` is absent. |
| `GILIR_OPENCODE_URL` | `http://127.0.0.1:49374` | OpenCode V2 server. It must be on loopback. |
| `OPENCODE_PASSWORD` | unset | Sent as HTTP Basic auth (user `opencode`). |
| `GILIR_HOOKS` | unset | Path to a hook config file (see below). |
| `GILIR_ACTOR` | `user` | Default actor recorded for CLI changes. `--actor` overrides it. |

### Connecting OpenCode

Point Gilir at your running OpenCode V2 server. Set the password without
printing it or saving it in your shell history. For the managed OpenCode
service:

```sh
export GILIR_OPENCODE_URL=http://127.0.0.1:49374
export OPENCODE_PASSWORD="$(opencode service get password)"
gilir daemon run
```

To paste the password yourself instead, use a silent Bash prompt:

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
gilir create "Fix rounding" --repo 1 --status todo --priority high
gilir create "Ship release notes" --repo 1 --project 1 --status todo   # -> id 2
gilir list --status todo                         # tasks across all projects
gilir list --project 1 --status todo
gilir show 1 --json
gilir depend 2 1                                 # task 2 waits for task 1
gilir comment 1 "Repro in **ledger_test.go**" --actor agent:me
gilir approve 1                                  # or: gilir request-changes 1
gilir daemon status
gilir daemon pause
gilir daemon resume
gilir daemon concurrency 2
gilir --help
gilir skill                                      # agent workflow instructions
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
- `approve` moves an `in-review` task to `done`. `request-changes` moves it to
  `todo` and marks it `changes-requested`.

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
