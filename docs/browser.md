# Browser view

The browser view shows one Session: its Task graph, its Agents and the details of each Task. It is read-only. It cannot read Task mail or change anything.

## Open the view

Run `serve` in one terminal. Then open one Workspace and Session:

```sh
./a-stack open --workspace WORKSPACE_ID --session SESSION_ID
# Print the URL, but do not start xdg-open:
./a-stack open --workspace WORKSPACE_ID --session SESSION_ID --no-browser --json
```

`open` accepts the usual `--state-dir`, `--credential-file` and `--as` flags. The Operator or a currently registered Agent of the Session can open the view, also for a closed Session. The view gets only the caller's read access to Session metadata. A browser credential cannot open another view or use the write, mail, storage or runtime endpoints.

`open` prints the URL before it runs `xdg-open`. If `xdg-open` fails, the command still succeeds, the URL is on stdout, and stderr shows a fixed message. With `--json`, the output is `{"data":{"url":"..."}}`.

## Keep the URL private

The URL works like a credential. The client makes a new random read secret and operation ID. It sends only the secret's SHA-256 hash to `POST /v1/browser/open`, and it builds the URL `/view/WORKSPACE_ID/SESSION_ID#SECRET`. The service stores a hash of the scoped capability and a scalar retry receipt. It never stores the secret.

Each `open` makes a new capability. A lost response can leave a hash that nobody uses. There is no automatic retry, and the capability does not expire. A direct API caller keeps the same operation ID and hash for a matching retry.

The page reads the secret from the URL fragment at once, removes the fragment with `history.replaceState`, and keeps the secret in the tab's session storage. If you open a URL with a new fragment in the same tab, the page uses the new fragment. You can reload the clean URL in the same tab. A different tab without the fragment asks you for a new `open` URL.

The page sends the secret only in the `Authorization` header of data requests to the same origin. The secret is never in request paths, query strings, assets or logs. The Operator credential never reaches the page.

## What the view shows

The view is HTML, CSS, JavaScript and SVG, embedded in the binary. It keeps the approved Graph and Agents tabs and both themes.

- Graph arrows point from a prerequisite to its dependent Task. The details also name both directions.
- Full names wrap above the cards.
- Groups of Tasks that are not connected sit side by side with a wide space between them, largest first, and wrap into rows that follow the shape of the panel. A Task with no dependencies is its own group. Inside a group, each Task is placed near the Tasks it connects to.
- A column that is much taller than the panel wraps into sub-columns that sit closer together than real columns. Lines then leave a card through the gap below its row and enter a card through the gap above its row. In a gap, lines out run in the upper part and lines in run in the lower part, and every lane (see below) has its own track, 12px from the next, so lines of different Tasks never merge. A gap grows only as much as its tracks need. When the column has lines on both sides, lines out also leave low on a card and lines in enter high on a card, in a separate lane, so a line out of one card never meets a line into its neighbour.
- Between columns and in the gaps of wrapped rows, lines into a Task with several prerequisites share one lane and track, and the lines of any other prerequisite branch from its own lane and track. A line that skips a column takes the lane nearest its own column and runs above or below its group, on its own track for each prerequisite, so fewer lines pass next to cards they do not connect.
- The layout runs again when Tasks or dependencies change, or when a card changes size: a new title, other assigned Agents, Needs you, file references or cancelled prerequisites. It uses the panel size at that moment. Resizing the window alone does not move cards, and neither does a poll without changes.
- A shared Task shows the same data in every Agent row. The Unassigned row shows Tasks with no assignee.
- Progress and Needs you requests are shown separately. When several Tasks need input, the attention button opens a chooser. Viewing a Task never resolves its request.
- The chooser buttons keep their identity across unrelated refreshes. If the focused request is resolved somewhere else, the focus moves to a remaining choice, or to the view tab when the chooser is empty.
- The runtime state has an "unverified" label and the age of its last observation. The age increases also when polls succeed.

## Keyboard and controls

- The arrow keys, Home and End switch between the tabs.
- In search, Enter moves the focus to the first matching card.
- On a card, Enter opens the details. Escape closes them and returns the focus.
- Search looks at Agent names, titles, Ticket references and the short report preview.
- The status filter includes Cancelled and Needs you.
- Drag an empty part of the graph to move it. Zoom and Fit are separate controls.
- Fit scales the graph to the Task cards, never above 100%, and centers them in the panel.
- In the Graph tab, filters dim the cards that do not match, so the connections stay visible. In the Agents tab, filters hide them.

## Refresh

While the page is visible, it polls the snapshot every two seconds. It sends `If-None-Match` and its known read revision. The service checks authentication and Session access before it returns an empty 304 response for a matching ETag.

When the snapshot has changed, the page loads all pages of Tasks, Agents, assignments and dependencies for that one read revision. It shows the new model only when all collections match the counts in the snapshot. If there is a conflict, it starts the collection again, up to three times, and then backs off.

- If the service instance changes, the page fetches the public page again to read the new instance ID, and tries again.
- If an error occurs, the page keeps the last complete model and marks it as saved and stale.
- The backoff starts at four seconds and grows to thirty seconds.
- When the page is hidden, polling stops and the current collection is cancelled. When the page is visible again, polling continues.

The footer shows the time of the last successful check with the local service. The About panel shows when the snapshot was captured, and its revision. A poll with no changes does not change the report or runtime times.

The full description, report, reason, result, question and file references load only for the selected Task. Text from the service is shown as text, also when it contains markup. References can be seen and copied. The page never follows or fetches them.

## Read interface and limits

The Task data has an optional `report_preview`: the first 160 Unicode code points of the report, 640 UTF-8 bytes at most. SQLite `substr` computes it from the stored report. An empty report has no preview. The preview ends at an embedded NUL character, but the full report is kept. This adds no schema, no copy of the body, and no change to history, authorization or revisions. The full report is still available through `task/show`. Cards reserve two lines for the preview, so a change to the report alone does not change their size.

`POST /v1/session/snapshot` returns an ETag made of the Session ID and the read revision. The service checks `If-None-Match` after the normal authenticated read. CLI clients can still use the JSON `known_revision`. The collection limits and the page budget of 768 KiB still apply. During polling, the page does not load details for every Task. It refreshes only the selected Task's details, when that Task's revision changes.

The public part of the service has exactly three resources: `/view/WORKSPACE_ID/SESSION_ID`, `/assets/app.css` and `/assets/app.js`. The service reads no files to serve them. It checks `Host` and `Origin` before it serves an asset or data, and it refuses queries and `GET` requests with a body. The Content Security Policy allows scripts and connections only to the same origin, and it blocks framing, forms and changes of the base URL. Inline styles are allowed for the numeric positions in the graph, but data never becomes CSS or HTML. The service has no CORS grants, no general file endpoint and no arbitrary navigation. It never exposes mail bodies, transcripts or private runtime bindings.

## Measurements and practical limits

Measured on 2026-10-24 with an Intel Core i5-13420H (12 logical CPUs), Linux x86_64, Go 1.27.3, SQLite 3.53.4, agent-browser 0.39.2 and an isolated headless Chromium at 1440×900. For each size, five fresh page loads used retained live API fixtures with reports, assignments, a 64-character name, questions and dependencies. The positions were the same in all five loads for each size. The p95 value is the maximum of five samples, not a service objective.

| Tasks | Layout p50 / p95 | Navigation to complete model p50 / p95 | Initial read requests | Detail requests |
| --- | --- | --- | --- | --- |
| 10 | 2.2 / 2.6 ms | 30.7 / 36.7 ms | 5 | 0 |
| 100 | 11.3 / 12.9 ms | 64.3 / 70.8 ms | 6 | 0 |
| 1,000 | 108.8 / 138.7 ms ¹ | 503.8 / 549.7 ms | 33 | 0 |

The layout time includes the rank calculation, card measurement, positions and SVG connections. The navigation time includes the subprocess, browser, API and render work, and is only a local observation.

¹ The table was measured before groups, ordering and column wrapping were added to the layout. A later check on the same machine and browser, with a plain Root → 999 Tasks fixture at 1440×900 and five fresh loads each, measured layout at 93.5–105.4 ms (p50 98.8 ms) before that change and 98.3–103.2 ms (p50 100.8 ms) after it. A review of an earlier version of the change measured 158–169 ms before and 179–221 ms after, on its own fixture.

With 1,000 Tasks, one wide rank made a canvas of 192,929 px. That measurement was taken before columns wrapped. Such a rank now wraps into sub-columns: in the Root → 999 check, the canvas is 11,348 × 5,642 px. The view shows every Task. There is no virtualization, minimap or automatic Fit. At this size, Fit gives an overview with labels that you cannot read. Use scrolling, search, filters and the Agents tab to find single Tasks. The layout and ownership scans are simple on purpose, and they can become slow above these sizes. Add indexed client maps or virtualization only when larger measured workloads need them.

## Checks

The prototype file `prototypes/h-stack-visual/index.html` keeps the SHA-256 hash `7fc0310c177fcb41f8a5a2de6a66af0338620b166288cb1ca15796e8f0436a1c`, which shows that it has not changed.

Browser QA used disposable state and an isolated named browser session. It covered both themes, full names, literal injection strings, keyboard and chooser behavior, and preserved focus, positions and view after report-only changes. It also covered failure and recovery with saved data, a service restart, and the growing observation age during polls with no changes. Focused browser regression checks also covered repeated opens of populated and empty references, and the focus of the chooser and Copy buttons during polling.

Go tests cover the scope and permissions of issued capabilities, guarded assets, authenticated conditional reads, opener failure without credential leaks, replay, and UTF-8 boundaries of the preview.
