---
name: gilir
description: Work a Gilir task through its audited lifecycle: inspect it, record Markdown progress and blockers, and move it to in-review when ready.
---

# Gilir task bootstrap

Work in the shared Gilir task, not local notes. Set these first:

```sh
task_id=42
session_id=ses_your_session_id
```

1. Inspect the task:

```sh
gilir show "$task_id" --json
```

2. Add Markdown progress and blockers with your current actor:

```sh
gilir comment "$task_id" "Progress: parser and tests are in place." --actor "agent:$session_id"
gilir comment "$task_id" "Blocked by: waiting for the store migration." --actor "agent:$session_id"
```

Every write must carry your own actor; the actor recorded with each mutation is the audit provenance. If you are working as a dispatched parent, use the latest task session link for `session_id`; a subagent uses its own session ID.

3. When the work is ready, transition the task:

```sh
gilir update "$task_id" --status in-review --actor "agent:$session_id"
```

# Repositories and projects

Repositories are registered globally and can be linked to several projects. A repo always belongs to at least one project. When you add a repository without `--project`, Gilir creates a project from the repository basename and links the two:

```sh
gilir repo add ~/src/payments                                    # auto-creates project "payments"
gilir repo add ~/src/payments --project 3                        # links to existing project 3
gilir repo list                                                  # every repo with its project_ids
gilir repo list --project 3                                      # repos linked to project 3
gilir repo link 1 4                                              # link repo 1 to project 4
gilir repo unlink 1 4                                            # unlink; refused for a last link or tasks in use
```

A task always targets a repository. When the repo belongs to exactly one project, `--project` can be omitted and the project is inferred; a repo linked to several projects requires `--project`:

```sh
gilir create "Fix rounding" --repo 1 --status todo --priority high
gilir create "Fix rounding" --repo 1 --project 3
gilir list --repo 1 --status todo                                # tasks across all projects
gilir list --project 3                                           # tasks in one project
```

Use `gilir --help` or `gilir <command> --help` for exact syntax.
