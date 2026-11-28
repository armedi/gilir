---
name: gilir
description: Use only when the request explicitly mentions gilir or a GILIR-<n> task ID. Before any gilir command, run `gilir skill` and follow that manual.
---

# Gilir

This repository's tasks are tracked in Gilir.

Before running any `gilir` command, run:

```sh
gilir skill
```

That prints the current Gilir manual. Follow it; it carries the workflows and
invariants this file deliberately does not duplicate.

## Installing this skill

Agents only discover skills in their configured skill directories. To make this
available on an agent host, copy or symlink it into one, for example:

```sh
ln -s "$PWD/skills/gilir" ~/.agents/skills/gilir
```

The `gilir skill` command works without this file: it prints the same manual
from the binary, so an agent that only follows the task prompt still has the
full instructions.
