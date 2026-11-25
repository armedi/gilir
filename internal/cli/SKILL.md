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

Use `gilir --help` or `gilir <command> --help` for exact syntax.
