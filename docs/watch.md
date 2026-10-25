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

A Task that needs you counts only as needs you, not also as working or waiting. Finished counts the finished Tasks that are not archived. Cancelled Tasks are not counted. With `--json`, the command prints `{"data":{"sessions":[...]}}` with `workspace_id`, `workspace_path`, `session_id`, `name`, `closed`, `needs_you`, `working`, `waiting` and `finished` for each Session.

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

It reports new Tasks, status changes, new Needs you questions, answered questions and new reports. A new Needs you also rings the terminal bell. Text from Agents is shown with control characters replaced, so it cannot change your terminal.

Watch checks every two seconds. It reads the Session list each time, and reads the Tasks of a Session only when the Session's read revision changed. When the service stops answering, watch says so once on stderr, keeps trying, and says when it is connected again.

With `--json`, watch prints the first status as one JSON document and then one document per change: `{"data":{"at":"14:05:40","session_id":"...","session":"OAuth login","task_id":"...","title":"Login button","change":"needs_you","text":"GitHub or Google first?"}}`. `change` is `created`, `progress` (with `from` and `to`), `needs_you`, `answered` or `report` (with `text`). JSON output rings no bell.

### Desktop notices

`--notify` also shows a desktop notice through `notify-send`, when it is installed, for each Session with a new Needs you: `a-stack: 1 needs you in OAuth login`. The number counts all open questions in that Session. The notice has no question text, because notices can show on a locked screen or in a shared screen. To include the questions, use `--notify-text` (it turns on `--notify`). Without `notify-send`, watch says so once and keeps printing.

## Claude Code status line

`a-stack statusline` prints one line for the [status line](https://code.claude.com/docs/en/statusline) of your own Claude Code session, such as `a-stack OAuth login: 1 needs you · 2 working`. It reads the session's folder from the JSON that Claude Code sends on stdin, and counts the open Sessions of that folder's Workspace. With several open Sessions it shows only `a-stack:` and the sums. It never prints a view URL.

Add it to your Claude Code settings, for example `~/.claude/settings.json`:

```json
{
  "statusLine": {"type": "command", "command": "a-stack statusline"}
}
```

Add `--state-dir DIR` to the command if your service does not use the default state directory. Outside a Workspace, when the service is down, or on any other error, it prints nothing and exits with 0. It gives up after one second. Claude Code runs it after changes in the conversation, so the counts can be a little old while the session is idle; add `"refreshInterval": 10` to the `statusLine` object to update them every 10 seconds.
