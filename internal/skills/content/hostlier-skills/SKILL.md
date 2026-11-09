---
name: hostlier-skills
description: List, inspect or install Hostlier command skills for a local coding agent.
---

# Hostlier skills

These commands use the skills bundled in the Hostlier executable and work offline:

```sh
hostlier skills list
hostlier skills show deploy
hostlier skills install deploy
hostlier skills install
```

Omitting the command on install installs every bundled skill. Naming a command installs exactly one self-contained skill such as `hostlier-deploy`. There is no `--all` flag.

The default destination is the current project's `.agents/skills`. Use `--target claude` for `.claude/skills`, or `--global` for the same target under the OS user's home. `--output DIRECTORY` selects an exact custom skills root and cannot be combined with `--target` or `--global`.

Use `--dry-run` to preview paths without writing. Identical files remain unchanged. Conflicting files require `--force`, which replaces only the selected SKILL.md files and preserves other files. `--json` gives structured list, show or installation output. Installation creates instruction files; it does not deploy applications, install provider executables or modify the agent's permissions.
