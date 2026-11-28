# Gilir manual

Gilir is a task queue for coding agents. This manual carries the workflows and
invariants; for flags and arguments, run `gilir --help` or
`gilir <command> --help`.

Set these first:

```sh
task_id=GILIR-42                      # the prefixed task ID, never a bare number
session_id=ses_your_opencode_session  # your own OpenCode session
```

A dispatched parent reads its `session_id` from the `sessions` section of
`gilir show`; a subagent uses its own.

## IDs

Task IDs are accepted only with the `GILIR-` prefix: `gilir show GILIR-42`
works, `gilir show 42` is an error. Project, repository and comment IDs are bare
positive integers.

## Seeing a task

Read a task with `gilir show GILIR-42`. On a resumed or review cycle, read its
`sessions` and `activity` sections before starting: earlier attempts left their
sessions, worktree, comments and any review message there.

## Working a task

Work in the OpenCode worktree listed on the task's latest session, not in the
repository's main checkout. Gilir neither creates nor tracks branches. The
worktree is reused for every attempt, and Gilir never deletes it, even when the
task is done or deleted.

Tag every write with your own actor so the audit trail is attributable. Comment
edit and removal require the actor that wrote the comment.

```sh
gilir comment "$task_id" "Progress: parser and tests are in place." --actor "agent:$session_id"
```

If you cannot continue, block the task instead of finishing it:

```sh
gilir block "$task_id" --message "Waiting on the **ledger** migration." --actor "agent:$session_id"
```

A blocked task is never dispatched, and a run that finishes while the task is
blocked stays `in-progress` instead of moving to review, so its dependents stay
held. Clearing the block takes an optional message:

```sh
gilir unblock "$task_id" --message "Migration landed." --actor "agent:$session_id"
```

Unblocking an idle `in-progress` task returns it to `todo`, queued ahead of
other tasks at the same priority.

When the work is ready, move the task to review:

```sh
gilir update "$task_id" --status in-review --actor "agent:$session_id"
```

A successful dispatch also moves the task to `in-review`; a failed one leaves it
`in-progress` with the `error` marking. Moving it back to `todo` retries it.

## Human-requested queue and planning work

Only do these when the human asks.

```sh
gilir repo add /srv/payments                 # auto-creates project "payments"
gilir repo add /srv/payments --project 3     # links an existing project
gilir repo list                              # every repo with its project_ids
gilir create "Fix rounding" --repo 1 --status todo --priority high
gilir depend GILIR-7 GILIR-5                 # GILIR-7 waits for GILIR-5
gilir delete GILIR-7
```

- Repository paths resolve on the service host, not yours. A repository shared
  by several projects needs `--project` on `create`; a single-project
  repository infers it.
- `create` defaults to `backlog`; `--status todo` enqueues it.
- `depend` requires both tasks in one project. A dependency clears when the
  blocker reaches `in-review` or `done`, so a blocked blocker keeps dependents
  waiting.
- `delete` refuses a task with a reserved dispatch attempt or with dependents.
  The audit history survives.
- Queue controls run only on the machine hosting the service, never through a
  remote `GILIR_URL`: `gilir service status`, `gilir service pause`,
  `gilir service resume`, `gilir service concurrency 2`.

## Human-requested reviews

Review only when the human asks, and never approve a task you worked on.
Approval moves an `in-review` task to `done`. Requesting changes requires a
message, returns the task to `todo`, marks it `changes-requested`, and records
the message as a comment:

```sh
gilir approve GILIR-7 --actor "agent:$session_id"
gilir request-changes GILIR-7 --message "Add a rounding test." --actor "agent:$session_id"
```
