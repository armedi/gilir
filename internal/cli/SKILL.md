---
name: gilir
description: Read and update Gilir projects, repositories, tasks, dependencies, reviews, and audit comments from the command line. Use when coordinating work with other agents or checking task state.
---

# Gilir

Gilir is the shared, local-first issue tracker. Use the `gilir` CLI for all state changes so the audit trail stays complete. Print this skill with `gilir skill` or `gilir skill --json` (`{"skill": ...}`).

Always inspect before acting:

```sh
gilir show <id> --json
gilir list --project <project-id> --json
```

IDs are positive integers. Add `--json` to any command for machine-readable output. Run `gilir <command> --help` for exact syntax.

## Preserve audit provenance

Name the actor on every write with `--actor`. An OpenCode agent must use its own session identity:

```sh
gilir comment <id> <markdown> --actor agent:<openCode-session-id>
gilir update <id> --status in-review --actor agent:<openCode-session-id>
```

Never omit `--actor`, reuse another actor's identity, or rewrite history to hide who changed what. The actor recorded with each mutation is part of the append-only audit provenance for the task.

## Delegate freely

Split work into separate tasks and connect them with `depend`. Delegate by commenting with the exact scope, files, and expected proof, then move the task to `in-review` when handing it off. Every agent records its own `agent:<openCode-session-id>`.

## Workflow examples

Read a project's tasks:

```sh
gilir list --project 1 --status todo --json
```

Create a task with context:

```sh
gilir create "Add repository onboarding" --project 1 --repo 2 \
  --description "Accept a path and persist it." --priority high \
  --actor agent:<openCode-session-id>
```

Claim and discuss work:

```sh
gilir mark 42 in-progress --actor agent:<openCode-session-id>
gilir comment 42 "Plan: parse arguments, then persist." --actor agent:<openCode-session-id>
```

Record dependencies and review:

```sh
gilir depend 42 41 --actor agent:<openCode-session-id>
gilir update 42 --status in-review --actor agent:<openCode-session-id>
gilir approve 42 --actor agent:<openCode-session-id>
```

Manage projects and repositories:

```sh
gilir project list
gilir project create "Gilir"
gilir repo add /workspace/repo --project 1
```

Control the daemon:

```sh
gilir daemon status
gilir daemon concurrency 4
```
