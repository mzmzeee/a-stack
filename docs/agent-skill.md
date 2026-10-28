# Agent skill

The [`skills/a-stack`](../skills/a-stack/SKILL.md) folder is an agent skill. A coding agent loads it to learn how to work in an a-stack Session with the CLI: what it gets from the Operator or the Orchestrator, how it sets itself up when nobody briefed it, its work loop on a Task, Task mail, Needs you, and the rules for operation IDs, revisions, mentions and credentials. The work loop starts with the Task's prerequisites, and every piece of work stays on a Task. The Orchestrator gets a separate reference in the same folder.

With the skill installed, an Agent's brief can be short. Give the credential file path, Agent name, Task IDs, a private folder, and the state directory if it is not the default. The credential file names the Workspace and the Session. Also name each Task's responsibility and report/finish owner, the candidate branch and commit, required verification evidence, and where findings go.

For nontrivial code work, the Orchestrator plans a builder and an independent verifier on linked Build → Verify Tasks. Both Agents and the Orchestrator are assigned to both Tasks so findings reach the builder directly. The builder owns Build's report and finish; the verifier owns Verify's. Helpers send evidence in Task mail without replacing the owner's report. Build Finished means a candidate is ready, not that it is safe to ship.

Use one worker for small, low-risk work and record why. Add a separate tester when browser/device behavior, migrations, security or performance need distinct checks. The [Orchestrator reference](../skills/a-stack/references/orchestrator.md) gives commands and the repair loop: findings go in Build mail, the builder reopens and fixes, and the verifier re-checks the new commit. Never add a reverse dependency. After two failed rounds, the Orchestrator narrows scope or decides. These are working agreements; assignee permissions and dependency behavior are unchanged.

## Install

Install the skill with one command:

```sh
a-stack skill install
```

This is the recommended way. The `a-stack` binary carries the skill of its own version, so the skill always matches your binary, and it needs no Node.js and no network. The command writes the skill to `~/.claude/skills/a-stack` for Claude Code and to `~/.agents/skills/a-stack` for the harnesses that read `.agents/skills`: Codex, Pi, OpenCode, Gemini CLI, Cursor CLI, GitHub Copilot CLI, Amp and others. If `CLAUDE_CONFIG_DIR` is set, the Claude Code copy goes to `$CLAUDE_CONFIG_DIR/skills/a-stack`. To install the skill for one project only, run the command in the project folder with `--project`. It then writes `.claude/skills/a-stack` and `.agents/skills/a-stack` there.

Releases up to v0.1.3 do not have `skill install`; update a-stack first or use one of the ways below.

An explicit `skill install` replaces an older copy of the a-stack skill whole, so no removed files stay behind. It does not touch other skills. `a-stack update` safely refreshes existing files in the home folder and the current project: only files that match a known released version are replaced. Edited, foreign, linked and missing files, and extra user files, stay untouched. `skill install --refresh` does the same for skill files, and takes `--project`; it prints one line per changed file. Use `a-stack update --no-refresh` to skip automatic refresh, or `a-stack update --refresh` in another project folder to refresh its copies without downloading a binary. `a-stack skill uninstall` removes the copies, and takes `--project` too.

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

To keep an agent on the protocol after its context is compacted, to show its runtime state in the view, to stop it once when it would end a turn while a Task it set Working has no report, and to tell it about new Task mail, also install the [agent hooks](hooks.md), for Claude Code, Codex, Pi or OpenCode.

Start the agent and tell it that it is an a-stack Agent. Give it its credential file path and IDs as [Getting started](getting-started.md) describes. To make your coding agent the Orchestrator, you can also ask it to use a-stack: it sets itself up with `a-stack new --orchestrator` (see [Hand your Session to an Orchestrator](getting-started.md#hand-your-session-to-an-orchestrator)). The skill's description tells the agent to load it before its first `a-stack` command or `me` helper, and again after a context reset. Its cues include Agent roles, credential files, Task and Workspace/Session IDs, notices, Task mail, Needs you, reports, `task handoff`, standing orders and Orchestrator setup. Start each brief with “Load the a-stack skill before running commands.” The hooks and mention notices also point back to the skill.
