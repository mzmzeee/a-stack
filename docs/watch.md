# Watch from the terminal

`a-stack status` and `a-stack watch` show the Operator what the Agents do, without the browser view. Both only read. They use the Operator credential (`operator.json` in the state directory) and change nothing.

## Which Sessions

By default both commands show the open Sessions of the Workspace of the current folder, or of the closest folder above it that is a Workspace. Outside a Workspace they stop with `this folder has no Workspace; use --all or --session ID`.

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
