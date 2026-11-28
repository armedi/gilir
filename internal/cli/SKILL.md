# Gilir manual

You are working with Gilir, a task queue for coding agents. This manual covers
the workflows and invariants that `--help` does not. For exact flags and
arguments, run `gilir --help` or `gilir <command> --help`.

Set these first:

```sh
task_id=GILIR-42                      # the prefixed task ID, never a bare number
session_id=ses_your_opencode_session  # your own OpenCode session
```

## IDs

Task IDs carry a `GILIR-` prefix and are the only accepted form: `gilir show
GILIR-42` works, `gilir show 42` is an error. Project, repository and comment
IDs are bare positive integers.

## Seeing a task

Read a task with `gilir show GILIR-42` (no `--json`); the sections print in
reading order: the task, its blockers, its sessions (each with the OpenCode
worktree it ran in) and its activity. On a resumed or review cycle, read the
`sessions` and `activity` sections before starting: earlier attempts left their
sessions, comments and any review message there. Add `--json` only when you
need to parse the output.

## Working a task

Work in the OpenCode worktree recorded for the task and listed on its latest
session, not in the repository's main checkout. Pick your own branch name inside
that worktree; Gilir does not create or track branches. The worktree is reused
for every attempt on the task, and Gilir never deletes it, not even when the
task is done or deleted.

Record progress as Markdown comments, tagging every write with your own actor so
the audit trail is attributable:

```sh
gilir comment "$task_id" "Progress: parser and tests are in place." --actor "agent:$session_id"
```

When you are working as a dispatched parent, read `session_id` from the
`sessions` section of `gilir show`; a subagent uses its own session ID. Comment
edit and removal require the same actor that wrote the comment, so keep using
yours.

If you cannot continue, block the task instead of finishing it:

```sh
gilir block "$task_id" --message "Waiting on the **ledger** migration." --actor "agent:$session_id"
```

A blocked task is never dispatched, and a run that finishes while the task is
blocked stays in progress instead of moving to review, so its dependents stay
held. A human or agent then clears the block, optionally with a message:

```sh
gilir unblock "$task_id" --message "Migration landed." --actor "agent:$session_id"
```

Unblocking an idle in-progress task returns it to todo and re-queues it ahead of
other tasks at the same priority.

When the work is ready, move the task to review:

```sh
gilir update "$task_id" --status in-review --actor "agent:$session_id"
```

A successful dispatch also moves the task to `in-review`; a failed one leaves it
`in-progress` with the `error` marking, and moving it back to `todo` retries it.

## Human-requested queue and planning work

Only do these when the human asks.

```sh
gilir repo add /srv/payments                 # auto-creates project "payments"
gilir repo add /srv/payments --project 3     # links an existing project
gilir repo list                              # every repo with its project_ids
gilir create "Fix rounding" --repo 1 --status todo --priority high
gilir depend GILIR-7 GILIR-5                 # GILIR-7 waits for GILIR-5
gilir unblock GILIR-7
gilir delete GILIR-7
```

- Repository paths resolve on the service host, not yours. A repository shared
  by several projects needs `--project` on `create`; a single-project
  repository infers it.
- `create` defaults to `backlog`. Pass `--status todo` to enqueue it.
- `depend` requires both tasks in one project. A dependency unblocks when the
  blocker reaches `in-review` or `done`, so a blocked blocker keeps dependents
  waiting.
- `delete` needs no reserved dispatch attempt and no task still depending on
  the task. The audit history survives.
- Retry a task that carries `error` by moving it back to `todo`.
- Queue controls: `gilir service status`, `gilir daemon pause`,
  `gilir daemon resume`, `gilir daemon concurrency 2`.

## Human-requested reviews

Review only when the human asks for it, and never approve a task you worked on.
Approval moves an `in-review` task to `done`; requesting changes requires a
message, returns the task to `todo`, marks it `changes-requested`, and records
the message as a comment:

```sh
gilir approve GILIR-7 --actor "agent:$session_id"
gilir request-changes GILIR-7 --message "Add a rounding test." --actor "agent:$session_id"
```
