# Watch from the terminal

`a-stack status`, `a-stack watch` and `a-stack statusline` show the Operator what the Agents do, without the browser view. All three only read. They use the Operator credential (`operator.json` in the state directory) and change nothing.

## Which Sessions

By default `status` and `watch` show the open Sessions of the Workspace of the current folder, or of the closest folder above it that is a Workspace. Outside a Workspace they stop with `this folder has no Workspace; use --all or --session ID`.

- `--all` shows the open Sessions of every Workspace.
- `--session ID` shows one Session, also when it is closed.

## Status

`a-stack status` prints one line per Session:

```text
$ a-stack status
"OAuth login" in ~/code/shop: 1 needs you · 2 working · 1 waiting · 3 finished
```

A Task that needs you counts only as needs you, not also as working or waiting. Finished counts the finished Tasks that are not archived. Cancelled Tasks are not counted. With `--json`, the command prints `{"data":{"sessions":[...]}}` with `workspace_id`, `workspace_path`, `session_id`, `name`, `closed`, `needs_you`, `working`, `quiet`, `waiting` and `finished` for each Session.

### Quiet Tasks

A Working Task is quiet when it had no change and no Task mail for 30 minutes. Its Agent may have stopped: a usage limit, a crash or a closed tab look the same as busy work otherwise. A Task that waits for your answer to its Needs you question is never quiet. Status shows the quiet Tasks among the working ones:

```text
"OAuth login" in ~/code/shop: 0 needs you · 2 working (1 quiet) · 1 waiting · 3 finished
```

The Task data has `quiet` and `last_mail_at` (see [What a page contains](tasks.md#what-a-page-contains)), so `task list` marks them too. Nothing is stored for this: a-stack derives it from the Task's `updated_at` and the time of its last Task mail when it reads the Task. A change or a Task mail ends it. To brief a fresh Agent for a quiet Task, see [Hand a Task over](tasks.md#hand-a-task-over).

## Watch

`a-stack watch` prints the same status once, then one line for each change until you press Ctrl-C:

```text
$ a-stack watch
Watching the Sessions of ~/code/shop. Press Ctrl-C to stop.
"OAuth login" in ~/code/shop: 0 needs you · 2 working · 1 waiting · 3 finished
14:02:11  OAuth login  Token exchange: waiting → working
14:05:40  OAuth login  Login button: needs you: GitHub or Google first?
14:07:02  OAuth login  Login button: Needs you answered
14:09:15  OAuth login  Token exchange: new report: Exchange works; tests pass
14:09:30  OAuth login  Token exchange: working → finished
14:10:01  OAuth login  Security review: new Task, waiting
```

It reports new Tasks, status changes, new Needs you questions, answered questions, new reports and Tasks that turn quiet, such as `14:40:00  OAuth login  Token exchange: quiet: Working, but no change or Task mail for 30 min`. A new Needs you and a Task that turns quiet also ring the terminal bell, once: a quiet Task rings again only after a change or a Task mail ended its quiet time and it turned quiet again. Text from Agents is shown with control characters replaced, so it cannot change your terminal.

Watch checks every two seconds. It reads the Session list each time, and reads the Tasks of a Session only when the Session's read revision changed, or when one of its Working Tasks turns quiet by then. When the service stops answering, watch says so once on stderr, keeps trying, and says when it is connected again.

With `--json`, watch prints the first status as one JSON document and then one document per change: `{"data":{"at":"14:05:40","session_id":"...","session":"OAuth login","task_id":"...","title":"Login button","change":"needs_you","text":"GitHub or Google first?"}}`. `change` is `created`, `progress` (with `from` and `to`), `needs_you`, `answered`, `report` (with `text`) or `quiet`. JSON output rings no bell.

### Desktop notices

`--notify` also shows a desktop notice through `notify-send` when it is installed, or through `osascript` on macOS (not yet on Windows, where `watch` says so and runs without notices), for each Session with a new Needs you: `a-stack: 1 needs you in OAuth login`. The number counts all open questions in that Session. The notice has no question text, because notices can show on a locked screen or in a shared screen. To include the questions, use `--notify-text` (it turns on `--notify`). `notify-send` reads the text as markup, so watch escapes `&`, `<` and `>` for it; macOS shows the text as it is. Without `notify-send`, watch says so once and keeps printing.

## Claude Code status line

`a-stack statusline` prints one line for the [status line](https://code.claude.com/docs/en/statusline) of your own Claude Code session, such as `a-stack OAuth login: 1 needs you · 2 working`. It reads the session's folder from the JSON that Claude Code sends on stdin, and counts the open Sessions of that folder's Workspace. With several open Sessions it shows only `a-stack:` and the sums. It never prints a view URL.

Add it to your Claude Code settings, for example `~/.claude/settings.json`:

```json
{
  "statusLine": {"type": "command", "command": "a-stack statusline"}
}
```

Add `--state-dir DIR` to the command if your service does not use the default state directory. Outside a Workspace, when the service is down, or on any other error, it prints nothing and exits with 0. It gives up after one second. Claude Code runs it after changes in the conversation, so the counts can be a little old while the session is idle; add `"refreshInterval": 10` to the `statusLine` object to update them every 10 seconds.
