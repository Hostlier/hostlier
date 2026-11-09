# Install coding-agent skills

[All guides](../../GUIDE.md)

The CLI embeds a standalone `SKILL.md` guide for each top-level command. No source checkout, network, controller connection or provider package is required to inspect or install them.

```sh
hostlier skills list
hostlier skills show deploy
hostlier skills install deploy   # Only the deploy skill
hostlier skills install          # Every bundled skill
```

The selector is usually a top-level command such as `deploy`, `resource`, or `sidecar`. The `minion` skill covers Hostlier’s Minion product: local installation, preparation, development, packaging, and Fused integration. Run `hostlier minion` for its local commands and `hostlier skills show minion` for the complete guide. Commands with subcommands include their workflows in one skill. The `domain` and `client` aliases and full names such as `hostlier-deploy` are also accepted. Omitting the selector installs all skills; there is no `--all` flag.

Installation defaults to `.agents/skills/hostlier-COMMAND/SKILL.md` under the current directory. Supported targets follow the documented [Codex skill locations](https://learn.chatgpt.com/docs/build-skills) and [Claude Code skill locations](https://code.claude.com/docs/en/skills):

| Target | Project default | With `--global` |
| --- | --- | --- |
| `codex` (default) | `.agents/skills` | `~/.agents/skills` |
| `claude` | `.claude/skills` | `~/.claude/skills` |

```sh
hostlier skills install --target claude
hostlier skills install resource --target codex --global
hostlier skills install --output ./agent-skills --dry-run
hostlier skills install deploy --force
```

`--output DIRECTORY` selects an exact skills root for another agent or export and cannot be combined with `--target` or `--global`. Relative paths resolve from the current directory. Global installs use the OS user's home, independently of `HOSTLIER_HOME`.

`--dry-run` checks the selection and destination without creating files. Identical files remain unchanged. Different existing instructions cause an error unless `--force` is supplied; replacement touches only the selected `SKILL.md` files and preserves supporting files. All selected destinations are checked for conflicts before writing. Each changed file is published atomically; an I/O failure can leave earlier files installed, and rerunning safely resumes. Symlinked skill directories or instruction files are rejected. `--json` provides structured list, show and installation output, including paths, actions and dry-run status.

The bundled sources live in [`internal/skills/content`](../../internal/skills/content). Reinstall skills after upgrading Hostlier to pick up revised guidance; use `--force` only when replacing existing instructions is intended. Skill installation does not change agent permissions or authorize deployment. The guides preserve Hostlier's interactive plan approval requirements.

## Output for agents

In a terminal, Hostlier prints readable text. When output is piped or captured,
as it is for most coding agents, results stay JSON, exactly as before. Add
`--toon` to any command for [TOON](https://github.com/toon-format/spec), a
compact form of the same data that uses fewer tokens. Use `--json` to force
JSON. Progress lines on stderr follow the same choice. `--toon` cannot be
combined with `logs --follow` or the local `controller` commands.

For agent workloads, see [agents and sidecars](../agent-deployments.md).
