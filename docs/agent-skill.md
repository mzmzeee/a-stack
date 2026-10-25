# Agent skill

The [`skills/a-stack`](../skills/a-stack/SKILL.md) folder is an agent skill. A coding agent loads it to learn how to work in an a-stack Session with the CLI: what it gets from the Operator or the Orchestrator, its work loop on a Task, Task mail, Needs you, and the rules for operation IDs, revisions, mentions and credentials. The Orchestrator gets a separate reference in the same folder.

With the skill installed, an Agent's brief can be short. It needs only the credential file path, the Workspace and Session IDs, the Agent's name, its Task IDs, and the state directory if it is not the default.

## Install

Install the skill with one command:

```sh
a-stack skill install
```

This is the recommended way. The `a-stack` binary carries the skill of its own version, so the skill always matches your binary, and it needs no Node.js and no network. The command writes the skill to `~/.claude/skills/a-stack` for Claude Code and to `~/.agents/skills/a-stack` for the harnesses that read `.agents/skills`: Codex, Pi, OpenCode, Gemini CLI, Cursor CLI, GitHub Copilot CLI, Amp and others. If `CLAUDE_CONFIG_DIR` is set, the Claude Code copy goes to `$CLAUDE_CONFIG_DIR/skills/a-stack`. To install the skill for one project only, run the command in the project folder with `--project`. It then writes `.claude/skills/a-stack` and `.agents/skills/a-stack` there.

Releases up to v0.1.3 do not have `skill install`; update a-stack first or use one of the ways below.

The command replaces an older copy of the a-stack skill whole, so no removed files stay behind. It does not touch other skills. `a-stack update` updates the copies that exist. `skill install --refresh` does the same and writes no new copies. `a-stack skill uninstall` removes the copies, and takes `--project` too.

You can also use the [`skills`](https://github.com/vercel-labs/skills) CLI, which knows where many agents look for skills. It needs Node.js:

```sh
npx skills add mzmzeee/a-stack       # in this project
npx skills add mzmzeee/a-stack -g    # for all your projects
```

It asks which agents to install the skill for. It installs the skill from the default branch, which can be newer than your binary.

Without either command, download the skill from a release and unpack it where your harness looks for skills, such as `~/.claude/skills` or `~/.agents/skills`. Releases up to v0.1.1 do not include the skill. Remove an old `a-stack` folder first, so that no removed files stay behind:

```sh
mkdir -p ~/.claude/skills
rm -rf ~/.claude/skills/a-stack
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/a-stack-skill.tar.gz \
  | tar -xz -C ~/.claude/skills
```

## Use

To keep an agent on the protocol after its context is compacted, to show its runtime state in the view, to stop it once when it would end a turn while a Task it set Working has no report, and to tell it about new Task mail, also install the [agent hooks](hooks.md).

Start the agent and tell it that it is an a-stack Agent. Give it its credential file path and IDs as [Getting started](getting-started.md) describes. The skill's description makes the agent load it when it hears that it is an a-stack Agent, gets an a-stack credential file, or must coordinate Tasks or Task mail with other agents. You can also name the skill in the prompt.
