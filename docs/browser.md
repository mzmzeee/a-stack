# Read-only Session browser

Run `serve` in one terminal, then open an explicit Workspace/Session:

```sh
a-stack open --workspace WORKSPACE_ID --session SESSION_ID
# Print the URL without starting xdg-open:
a-stack open --workspace WORKSPACE_ID --session SESSION_ID --no-browser --json
```

The command supports the ordinary `--state-dir`, `--credential-file` and `--as` selection. An Operator or currently registered Session Agent can delegate its metadata read scope, including for a closed Session. A browser credential cannot delegate or call write, mail, storage or runtime endpoints. The URL is printed before `xdg-open` runs; opener failure leaves success and the usable URL on stdout, with a fixed diagnostic on stderr. JSON output is `{"data":{"url":"..."}}`.

Treat the printed URL as a credential. The client generates a fresh random read secret and operation ID, submits only its SHA-256 hash to `POST /v1/browser/open`, and constructs `/view/WORKSPACE_ID/SESSION_ID#SECRET`. The service stores a scoped capability hash and scalar retry receipt, never the secret. Each invocation creates a fresh capability; a lost response can leave an unused hash. There is no automatic retry or expiry. Direct API callers retain the same operation ID and hash for a matching retry.

The page consumes the fragment immediately, removes it with `history.replaceState`, and retains the read secret in that tab's session storage. Reopening a fragment URL in the same document consumes the new fragment too. Reloading the clean URL works in the same tab; another tab without the fragment asks for a new `open` URL. The read secret is sent only in Authorization headers on same-origin data requests. It is absent from request paths, query strings, assets and logs. No Operator credential reaches the page.

## Views and refresh

The embedded HTML/CSS/JavaScript/SVG preserves the approved Graph/Agents controls and both themes. Graph arrows point from prerequisites to dependents; details also name both directions. Names wrap in full above cards. Shared Tasks use the same metadata in every Agent row, while the Unassigned row preserves work with no assignee. Progress and explicit Needs you requests are separate; the attention button opens a chooser when several Tasks need input. Viewing never resolves a request. Chooser buttons keep their identity across unrelated refreshes; if the focused request is resolved externally, focus moves to a remaining choice, or to the view tab when the chooser empties. Runtime state is separately labelled unverified with the age of its last observation; this age advances independently of successful polls.

Tab arrows/Home/End switch views. Search Enter focuses the first matching card; card Enter opens details, and Escape closes details and restores focus. Search covers names, titles, ticket references and the bounded report preview. Status filtering includes Cancelled and Needs you. Drag empty graph space to pan; zoom and Fit are explicit controls. Graph filters dim unmatched nodes to preserve connection context; Agents filters hide unmatched cards.

While visible, the page polls the snapshot manifest every two seconds, sending `If-None-Match` and its known read revision. Authentication and Session authorization precede a matching ETag's empty 304 response. Changed manifests trigger all pages of Tasks, Agents, assignments and dependencies pinned to that one read revision. The page publishes a model only after all collections match the manifest counts; a conflict restarts collection, up to three attempts before backing off. Service instance changes refresh the public shell header and retry. Errors keep the last complete model and mark it saved/stale. Backoff increases from four to thirty seconds; hiding the page stops polling and aborts an active collection, and returning resumes it.

The footer distinguishes the last successful local-service check from the snapshot's captured time/revision shown in About. A successful unchanged poll does not change report or runtime receipt times. Full description/report/reason/result/question and file references load only for the selected Task. Backend text is inserted as text, including literal markup. References are displayed/copied metadata, never navigated or fetched.

## Read interface and bounds

The existing Task projection adds optional `report_preview`: a prefix of up to **160 Unicode code points**, at most **640 UTF-8 bytes**, computed by SQLite `substr` from the retained `task_content` report. Empty reports omit the field. SQLite ends this preview at an embedded NUL; the full report is retained. No schema, copied body storage, history, authorization or revision rules change. The full report remains available through `task/show`. Cards reserve two lines for the preview so report-only changes do not change their geometry.

`POST /v1/session/snapshot` now returns an ETag of the Session ID/read revision. `If-None-Match` is evaluated after the ordinary authenticated read. Existing JSON/known-revision behavior remains available to CLI clients. Collection limits and the 768 KiB page budget remain in force. The UI performs no per-Task detail collection during polling; only selected details are refreshed when their Task revision changes.

The public surface consists of exact `/view/WORKSPACE_ID/SESSION_ID`, `/assets/app.css` and `/assets/app.js` resources, with no filesystem lookup. Host and Origin checks apply before assets or data; queries and body-bearing GETs are refused. CSP restricts scripts/connections to the same origin and forbids framing, forms and base URL changes. Inline styles support numeric graph positioning; data never becomes CSS/HTML. The service exposes no CORS grants, generic file endpoint, arbitrary navigation, mail bodies, transcripts or private runtime bindings.

## Measurements and practical limits

Measured on 2026-10-24 with an Intel Core i5-13420H (12 logical CPUs), Linux x86_64, Go 1.27.3, SQLite 3.53.4, agent-browser 0.39.2 and isolated headless Chromium at 1440×900. Five fresh navigations per size used retained live API fixtures with reports, assignments, a 64-character name, questions and dependencies. Positions were identical across all five loads for each size. The p95 below is the maximum of five samples, not a service objective.

| Tasks | Layout p50 / p95 | Navigation to complete model p50 / p95 | Initial read requests | Detail requests |
| --- | --- | --- | --- | --- |
| 10 | 2.2 / 2.6 ms | 30.7 / 36.7 ms | 5 | 0 |
| 100 | 11.3 / 12.9 ms | 64.3 / 70.8 ms | 6 | 0 |
| 1,000 | 108.8 / 138.7 ms | 503.8 / 549.7 ms | 33 | 0 |

Layout timing covers rank calculation, card measurement, positioning and SVG connections. Navigation timing includes subprocess/browser/API/render work and is useful only as a local observation. The 1,000-Task fixture's broad rank produced a 192,929 px canvas. Every Task is retained; there is no virtualization, minimap or automatic Fit. Fit at this scale is an overview with unreadable labels; scroll, search/filter and Agents provide access to individual work. Layout/ownership scans are intentionally simple and can become expensive above these measured sizes; introduce indexed client maps or virtualization only if larger measured workloads require them.

The immutable prototype hash remains `7fc0310c177fcb41f8a5a2de6a66af0338620b166288cb1ca15796e8f0436a1c`. Browser QA uses disposable state and an isolated named session. It covers both themes, full names, literal injection strings, keyboard/chooser behavior, report-only focus/position/viewport preservation, saved-data failure/recovery, service restart and advancing observation age during unchanged polls. Focused browser regressions also cover repeated populated/empty reference opens and retained chooser/Copy focus during polling. Go tests cover issued capability scope/permissions, guarded assets, authenticated conditional reads, credential-safe opener failure, replay and UTF-8 preview boundaries.
