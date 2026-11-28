# Gilir manual

Gilir is a task queue for coding agents. This manual carries the workflows and
invariants; for flags and arguments, run `gilir --help` or
`gilir <command> --help`.

## IDs

`<task_id>` is the full task ID, including the `GILIR-` prefix. Project,
repository and comment IDs are bare positive integers.

## Seeing a task

Run `gilir show <task_id>` to read a task, its blockers, sessions, and activity.

## Working a task

### Dispatched tasks

Use the task brief in your opening message when it’s included. Run
`gilir show <task_id>` if the brief is missing or you need current details or
comments. When resuming a prior attempt, use `gilir show` to read `sessions`
and `activity` before editing.

Work in the OpenCode worktree listed on the task's latest session, not in the
repository's main checkout. Gilir neither creates nor tracks branches. The
worktree is reused for every attempt, and Gilir never deletes it, even when the
task is done or deleted.

### Updating a task

Tag every write with your own actor so the audit trail is attributable. Comment
edit and removal require the actor that wrote the comment. If you’re the
dispatched parent and need your session ID, read it from `sessions` in
`gilir show <task_id>`. A subagent uses its own session ID.

```sh
task_id='<task_id>'
session_id=ses_your_opencode_session
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
gilir depend '<task_id>' '<blocker_task_id>' # the first task waits for the second
gilir delete '<task_id>'
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
gilir approve '<task_id>' --actor "agent:$session_id"
gilir request-changes '<task_id>' --message "Add a rounding test." --actor "agent:$session_id"
```
