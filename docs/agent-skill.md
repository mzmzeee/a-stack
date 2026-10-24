# Agent skill

The [`skills/a-stack`](../skills/a-stack/SKILL.md) folder is an agent skill. A coding agent loads it to learn how to work in an a-stack Session with the CLI: what it gets from the Operator or the Orchestrator, its work loop on a Task, Task mail, Needs you, and the rules for operation IDs, revisions, mentions and credentials. The Orchestrator gets a separate reference in the same folder.

With the skill installed, an Agent's brief can be short. It needs only the credential file path, the Workspace and Session IDs, the Agent's name, its Task IDs, and the state directory if it is not the default.

## Install

The skill is a plain folder with a `SKILL.md` file. Copy it to the place where your harness looks for skills.

| Harness | Folder |
| --- | --- |
| Claude Code, for all projects | `~/.claude/skills/a-stack/` |
| Claude Code, for one project | `.claude/skills/a-stack/` in that project |
| Codex, pi and other harnesses that read `SKILL.md` folders | `~/.agents/skills/a-stack/` |

From a checkout of this repository:

```sh
mkdir -p ~/.claude/skills
cp -R skills/a-stack ~/.claude/skills/
```

Without a checkout, download the folder from GitHub. Change the target folder for your harness:

```sh
mkdir -p ~/.claude/skills
curl -fsSL https://github.com/mzmzeee/a-stack/archive/refs/heads/main.tar.gz \
  | tar -xz -C ~/.claude/skills --strip-components=2 a-stack-main/skills/a-stack
```

The download works only after the skill is merged into `main`. Run the same command again to update the skill.

## Use

Start the agent and tell it that it is an a-stack Agent. Give it its credential file path and IDs as [Getting started](getting-started.md) describes. The skill's description makes the agent load it when it hears that it is an a-stack Agent, gets an a-stack credential file, or must coordinate Tasks or Task mail with other agents. You can also name the skill in the prompt.
