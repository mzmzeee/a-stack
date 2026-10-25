#!/usr/bin/env bash
# Record the README demo and the browser view images in docs/images:
#   demo.gif           the main tour: a new Session, the Now board, Task mail,
#                      Needs you and the answer, and a-stack -r
#   demo-graph.gif     the graph and its focus highlight
#   demo-archive.gif   finished Tasks shrink and fold into the archive
#   demo-agents.gif    the Agents tab
#   graph-light.png, graph-dark.png, now-light.png, now-dark.png,
#   mail-light.png, mail-dark.png, agents.png and picker.png
#
# The script builds a-stack from this checkout and runs a real Session on a
# disposable service. A temporary HOME holds the state, the credentials and the
# project folders; everything is deleted on exit.
#
# One story is recorded, and each GIF is cut from its scenes. A GIF has a
# caption that names the scene. Below it, the main GIF has a real terminal:
# scripts/demo-terminal.py types into a Bash in a pseudo-terminal and records
# it, and agg renders the recording. It is your terminal or the Agent
# 02-backend's terminal. At the bottom, the browser view, recorded by
# agent-browser at the same time. The commands and the service are real. Only
# the annotations are added: scripts/demo-overlay.js draws an outline and a
# callout on what just changed and zooms the page, in the recording browser
# only.
#
# The view URL fragment is a credential. The script never prints it or writes
# it to a file, and the terminal recording shows it as dots.
#
# Needs Go, Python 3, ffmpeg, ImageMagick (magick), agent-browser and curl.
# Downloads a pinned agg release into .tmp/ and checks its SHA-256.
# Usage: scripts/record-demo.sh
set -euo pipefail
cd "$(dirname "$0")/.."
repo="$PWD"
# DEMO_OUT writes the images to another folder, for a trial run.
out="${DEMO_OUT:-$repo/docs/images}"

# The scenes, in story order: the GIF it goes to, the view tab, whose terminal
# shows (main GIF only) and the caption. A tab the view does not have falls
# back to Graph.
scenes=(
  "demo|now|Your terminal|Create a Session for your project"
  "demo|now|Your terminal|The Orchestrator plans Tasks"
  "demo|now|Agent 02-backend's terminal|Agents pick up Tasks"
  "demo-graph|graph||The graph shows which Task waits for which"
  "demo|now|Agent 02-backend's terminal|Agents talk in Task mail"
  "demo|now|Agent 02-backend's terminal|An Agent asks you a question"
  "demo|now|Agent 02-backend's terminal|You answer, work continues"
  "demo-archive|graph||Finished work folds away"
  "demo-agents|agents||The Agents tab shows who does what"
  "demo|now|Your terminal|Pick another Session"
)
# The step badge of the caption in the feature GIFs.
declare -A badge=([demo-graph]=Graph [demo-archive]=Archive [demo-agents]=Agents)

# Pacing: a callout stays up HOLD seconds after its change settles, and BEAT
# seconds pass before the next command or change.
HOLD=4.5 BEAT=1.5
AGG_VERSION=v1.9.0
FPS=10
# The GIF is 960 pixels wide, about the width of a README on GitHub.
GIF_WIDTH=960 CAPTION_HEIGHT=44 VIEW_HEIGHT=480
# The terminal: 100 columns fit most commands on one line, 8 rows fit the
# picker.
TERM_COLS=100 TERM_ROWS=8
# The browser shows 1200x600 CSS pixels at 0.8 in the GIF. It renders at 1.6x,
# so the text stays sharp when the overlay zooms in.
VIEW_CSS_WIDTH=1200 VIEW_CSS_HEIGHT=600 VIEW_DEVICE_SCALE=1.6
# How far the overlay zooms in on a card.
ZOOM=1.5

die() { printf 'record-demo: %s\n' "$*" >&2; exit 1; }
step() { printf '== %s\n' "$*"; }

for tool in go python3 ffmpeg magick agent-browser curl sha256sum; do
  command -v "$tool" >/dev/null || die "$tool is not installed"
done

case "$(uname -m)" in
  x86_64) agg_asset='agg-x86_64-unknown-linux-musl'
    agg_sha256=ddcbf6ca044c8ac3a434dcb9ee89fb9e3be87209982b7c2adb55f782e8f0f390 ;;
  aarch64) agg_asset='agg-aarch64-unknown-linux-gnu'
    agg_sha256=2b4be407b97e00e1c313a41d154ced8fa3d02c560c8f47a0db4950a2576444c9 ;;
  *) die "no agg build for $(uname -m)" ;;
esac
agg="$repo/.tmp/agg-$AGG_VERSION"
if ! printf '%s  %s\n' "$agg_sha256" "$agg" | sha256sum --check --status 2>/dev/null; then
  step "download agg $AGG_VERSION"
  mkdir -p "$repo/.tmp"
  curl -fsSL -o "$agg.download" "https://github.com/asciinema/agg/releases/download/$AGG_VERSION/$agg_asset"
  printf '%s  %s\n' "$agg_sha256" "$agg.download" | sha256sum --check --status || die "agg checksum mismatch"
  chmod 755 "$agg.download"
  mv "$agg.download" "$agg"
fi

umask 077
mkdir -p "$out"
work_dir="$(mktemp -d -t a-stack-demo.XXXXXX)"
# The demo HOME. The terminal shows it as ~, so no personal path is recorded.
demo_home="$(mktemp -d -t demo.XXXXXX)"
cred_dir="$demo_home/.agents"
rec_session="a-stack-demo-$$"
shot_session="a-stack-demo-shots-$$"
# The background processes: the screenshot job, the two terminal recorders
# and the a-stack service. The PIDs are the processes themselves.
shot_pid="" service_pid=""
declare -A terminal_pid=([you]="" [agent]="")

cleanup() {
  trap '' INT TERM
  stop_process "$shot_pid"
  agent-browser --session "$rec_session" close >/dev/null 2>&1 || :
  agent-browser --session "$shot_session" close >/dev/null 2>&1 || :
  stop_process "${terminal_pid[you]}"
  stop_process "${terminal_pid[agent]}"
  stop_process "$service_pid"
  rm -rf "$work_dir" "$demo_home"
}
# stop_process PID: stop a background job and the commands it is running.
# shot_pid is a subshell, so its children are stopped too.
stop_process() {
  if [ -n "$1" ]; then
    local children
    children="$(pgrep -P "$1" | tr '\n' ' ')" || :
    # shellcheck disable=SC2086 # children is a list of PIDs.
    kill "$1" $children 2>/dev/null || :
    wait "$1" 2>/dev/null || :
  fi
}
trap cleanup EXIT
# Ctrl-C or kill at any point still runs cleanup through the EXIT trap.
trap 'exit 130' INT
trap 'exit 143' TERM

step "build a-stack"
mkdir -p "$work_dir/bin" "$cred_dir" "$demo_home/.local/state/a-stack" "$demo_home/code/oauth-app"
bin="$work_dir/bin/a-stack"
# The footer of the view shows this version.
go build -ldflags "-X main.version=$(git describe --tags --always)" -o "$bin" ./cmd/a-stack

# a-stack new and a-stack -r call xdg-open with the view URL. In the demo this
# stand-in shows the URL in the recording browser instead.
cat >"$work_dir/bin/xdg-open" <<'EOF'
#!/bin/sh
case "$1" in http://127.0.0.1:*) ;; *) exit 1 ;; esac
printf "location.replace('%s')" "$1" |
  HOME="$DEMO_REAL_HOME" agent-browser --session "$DEMO_BROWSER" eval --stdin >/dev/null 2>&1
EOF
chmod 755 "$work_dir/bin/xdg-open"

# --- a-stack helpers --------------------------------------------------------

stack() { HOME="$demo_home" XDG_STATE_HOME='' "$bin" "$@"; }
field() {
  python3 -c 'import json,sys
value=json.load(sys.stdin)
for key in sys.argv[1].split("."): value=value[int(key) if key.isdigit() else key]
print(value)' "$1"
}
operator() { stack --as operator --json "$@"; }
# as_agent NAME ARGS...: run a command as the Agent NAME in the current Session.
as_agent() {
  local name="$1"
  shift
  stack --as agent --credential-file "$cred_dir/$name.json" \
    --workspace "$workspace" --session "$session" --json "$@"
}

declare -A agent_id task_id title
lead=""

# new_session FOLDER NAME LEAD: a Workspace, a Session and its Orchestrator.
new_session() {
  mkdir -p "$demo_home/code/$1"
  workspace="$(operator workspace create --path "$demo_home/code/$1" \
    --operation-id "demo-workspace-$1" | field data.resource_id)"
  session="$(operator session create --workspace "$workspace" --name "$2" \
    --operation-id "demo-session-$1" | field data.resource_id)"
  appoint "$3"
}
appoint() {
  lead="$1"
  agent_id[$lead]="$(operator agent register --workspace "$workspace" --session "$session" \
    --name "$lead" --new-credential-file "$cred_dir/$lead.json" \
    --operation-id "demo-register-$lead" | field data.resource_id)"
  operator session appoint --workspace "$workspace" --session "$session" --agent "${agent_id[$lead]}" \
    --expected-revision "$(operator session inspect --workspace "$workspace" --session "$session" | field data.revision)" \
    --operation-id "demo-appoint-$lead" >/dev/null
}
register() {
  agent_id[$1]="$(as_agent "$lead" agent register --workspace "$workspace" --session "$session" \
    --name "$1" --new-credential-file "$cred_dir/$1.json" \
    --operation-id "demo-register-$1" | field data.resource_id)"
}
revision() { as_agent "$1" task show --task "${task_id[$2]}" | field data.revision; }
graph_revision() { as_agent "$lead" session inspect | field data.graph_revision; }
# change NAME TASK OPERATION ARGS...: one Task change with the current revision.
change() {
  local name="$1" key="$2" operation="$3"
  shift 3
  as_agent "$name" "$@" --task "${task_id[$key]}" --expected-revision "$(revision "$name" "$key")" \
    --operation-id "demo-$operation-$key" >/dev/null
}
new_task() {
  title[$1]="$2"
  task_id[$1]="$(as_agent "$lead" task create --title "$2" --ticket "$3" --description "$4" \
    --operation-id "demo-create-$1" | field data.resource_id)"
}
depends() {
  as_agent "$lead" dependency add --from "${task_id[$1]}" --to "${task_id[$2]}" \
    --expected-revision "$(graph_revision)" --operation-id "demo-edge-$1-$2" >/dev/null
}
assign() { change "$lead" "$1" "assign-$2" task assign --agent "${agent_id[$2]}"; }
start() { change "$1" "$2" start task status --progress working; }
finish() {
  change "$1" "$2" report task report --report "$3"
  change "$1" "$2" finish task status --progress finished --summary "$3"
}
ask() { change "$1" "$2" ask task attention set --question "$3"; }
mail() {
  as_agent "$1" mail send --task "${task_id[$2]}" --body "$3" --operation-id "demo-mail-$4" >/dev/null
}

# --- browser helpers --------------------------------------------------------

rec() { agent-browser --session "$rec_session" "$@"; }
shot() { agent-browser --session "$shot_session" "$@"; }
json() { python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$1"; }
# js CODE: run JavaScript in the recording browser.
js() { printf '%s' "$1" | rec eval --stdin >/dev/null; }
# A target for the overlay: a Task card by its key, or an element by selector.
card() { printf '{"task":%s}' "$(json "${title[$1]}")"; }
element() { printf '{"css":%s}' "$(json "$1")"; }
# callout TARGET TEXT [SIDE]: outline TARGET and point a callout with TEXT at
# it, from SIDE (below, above, left or right) or from the side with most room.
callout() { js "demo.mark($1, $(json "$2"), $(json "${3:-}"))"; }
zoom() { js "demo.zoom($1, ${2:-$ZOOM})"; }
click() { js "demo.click($1)"; }
# poll: let the view check for changes now instead of at its next poll.
poll() { js "demo.poll()"; }
# hold: keep the current callout up, then wait a beat.
hold() { sleep "$HOLD"; }
beat() { sleep "$BEAT"; }
# wait_for JS: wait until the expression is true in the recording browser.
wait_for() { rec wait --fn "$1" >/dev/null; }
# wait_card KEY TEXT [WITHOUT]: wait until the card shows TEXT and not WITHOUT.
wait_card() {
  wait_for "demo.ready($(card "$1"), $(json "$2"))${3:+ && !demo.ready($(card "$1"), $(json "$3"))}"
}
# wait_view BROWSER NAME: wait until the browser shows the Session NAME, live.
wait_view() {
  "$1" wait --fn "document.querySelector('h1')?.textContent === $(json "$2") &&
    !document.querySelector('.preview-label')?.textContent.includes('connecting')" >/dev/null
}
# overlay TAB: add the annotations to the recording browser and open TAB.
overlay() {
  rec eval --stdin <"$repo/scripts/demo-overlay.js" >/dev/null
  show_tab "$1"
}
show_tab() {
  if [ "$(rec eval "demo.tab($(json "$1"))")" = '"graph"' ]; then
    wait_for "!document.getElementById('graph-panel').hidden"
    js "document.getElementById('zoom-fit').click()"
  fi
}
# open_view BROWSER: open the current Session in that browser session. The URL
# goes to the page through stdin only.
open_view() {
  operator open --workspace "$workspace" --session "$session" --no-browser | field data.url |
    python3 -c 'import json,sys;print("location.replace(%s)" % json.dumps(sys.stdin.read().strip()))' |
    "$1" eval --stdin >/dev/null
  "$1" wait --fn "!document.querySelector('.preview-label').textContent.includes('connecting')" >/dev/null
}
no_fragment() {
  case "$("$1" get url)" in *'#'*) die "the view kept its URL fragment" ;; esac
}
# wait_graph BROWSER CARDS LINES: wait until the graph shows this many cards and lines.
wait_graph() {
  "$1" wait --fn "document.querySelectorAll('#graph-panel .task-card').length === $2 &&
    document.querySelectorAll('#graph-panel .connection').length === $3" >/dev/null
}
screenshot() {
  shot screenshot "$work_dir/$1" >/dev/null
  # The view uses flat colors, so 192 colors keep the PNGs small and sharp.
  # Without dithering, the blurred backdrop behind the Task details stays smooth.
  magick "$work_dir/$1" -strip -dither None -colors 192 "PNG8:$out/$1"
}
# The stills: the Graph and Now tabs and the Task details with Task mail open,
# in both themes, and the Agents tab.
take_screenshots() {
  local theme
  for theme in light dark; do
    shot click ".theme-button[data-theme=$theme]" >/dev/null
    shot click '#graph-tab' >/dev/null
    wait_graph shot 4 3
    shot click '#zoom-fit' >/dev/null
    sleep 0.5
    screenshot "graph-$theme.png"
    shot click '#now-tab' >/dev/null
    sleep 0.5
    screenshot "now-$theme.png"
    printf '[...document.querySelectorAll("#now-panel .task-card")].find(c => c.querySelector(".task-title").textContent === %s).click()' \
      "$(json "${title[ui]}")" | shot eval --stdin >/dev/null
    shot wait --fn "document.getElementById('task-dialog').open" >/dev/null
    shot click '#task-dialog .mail-toggle' >/dev/null
    shot wait --fn "document.querySelectorAll('#task-dialog .mail-message').length === 2" >/dev/null
    sleep 0.5
    screenshot "mail-$theme.png"
    shot eval "document.getElementById('task-dialog').close()" >/dev/null
  done
  shot click '.theme-button[data-theme=light]' >/dev/null
  shot click '#agents-tab' >/dev/null
  sleep 0.5
  screenshot agents.png
}

# --- scenes -----------------------------------------------------------------

now() { date +%s.%N; }
scene_switch=() scene_start=()
# scene_field N FIELD: a field of scene N; 1 is the GIF, 2 the tab, 3 the
# terminal and 4 the caption.
scene_field() {
  local -a fields
  IFS='|' read -r -a fields <<<"${scenes[$1 - 1]:-}"
  printf '%s' "${fields[$2 - 1]:-}"
}
# scene N: start scene N. A scene on another tab starts zoomed out, so the
# viewer sees the whole tab first. The GIFs cut between scene_switch and
# scene_start, so a cut never shows the tab change.
scene() {
  scene_switch[$1]="$(now)"
  if [ "$1" -gt 1 ]; then
    js "demo.clear()"
    if [ "$(scene_field "$1" 2)" != "$(scene_field $(($1 - 1)) 2)" ]; then
      js "demo.zoom(null)"
      show_tab "$(scene_field "$1" 2)"
      sleep 1
    fi
  fi
  scene_start[$1]="$(now)"
  beat
}

# --- the terminals ----------------------------------------------------------

cat >"$demo_home/.bashrc" <<'EOF'
PS1='\[\e]0;demo\a\e[1;32m\]\w\[\e[0m\] \$ '
HISTFILE=/dev/null
bind 'set enable-bracketed-paste off'
EOF
# Each terminal has a control and an ack FIFO. Read-write opens do not block,
# so a recorder that fails cannot hang the script.
mkfifo "$work_dir"/{you,agent}-{control,ack}
exec 3<>"$work_dir/you-control" 4<>"$work_dir/you-ack" 5<>"$work_dir/agent-control" 6<>"$work_dir/agent-ack"
declare -A control_fd=([you]=3 [agent]=5) ack_fd=([you]=4 [agent]=6) terminal_start
# say TERMINAL INSTRUCTION [TEXT]: see scripts/demo-terminal.py. TERMINAL is
# you or agent.
say() {
  printf '%s %s\n' "$2" "${3:-}" >&"${control_fd[$1]}"
  read -r -t 60 term_answer <&"${ack_fd[$1]}" || die "the $1 terminal stopped at: $2 ${3:-}"
}
# start_terminal TERMINAL CAST COLS ROWS RCFILE FOLDER: record a Bash. The
# recording starts at the first prompt; terminal_start holds that Unix time.
start_terminal() {
  (
    cd "$6"
    exec env -u XDG_STATE_HOME HOME="$demo_home" PATH="$work_dir/bin:$PATH" TERM=xterm-256color \
      DEMO_REAL_HOME="$HOME" DEMO_BROWSER="$rec_session" \
      python3 "$repo/scripts/demo-terminal.py" --control "$work_dir/$1-control" --ack "$work_dir/$1-ack" \
      --cast "$work_dir/$2" --cols "$3" --rows "$4" \
      -- bash --noprofile --rcfile "$5" -i
  ) &
  terminal_pid[$1]=$!
  read -r -t 60 "terminal_start[$1]" <&"${ack_fd[$1]}" || die "the $1 terminal did not start"
}
# next breaks a long command onto a second line with a backslash.
next=' \\\r    '
stop_terminal() {
  say "$1" stop
  wait "${terminal_pid[$1]}" 2>/dev/null || :
  terminal_pid[$1]=""
}
# agent_terminal NAME: a terminal for the Agent NAME in the current Session.
# me runs a-stack as the Agent, like the me script of the a-stack skill, and
# shows only the outcome and the new revision of each change.
agent_terminal() {
  cat >"$cred_dir/$1.rc" <<EOF
PS1='\[\e]0;demo\a\e[1;35m\]$1\[\e[0m\] \\$ '
HISTFILE=/dev/null
bind 'set enable-bracketed-paste off'
APP=${task_id[app]} UI=${task_id[ui]} TOKEN=${task_id[token]}
me() {
  a-stack --as agent --credential-file ~/.agents/$1.json --workspace $workspace --session $session "\$@" |
    python3 -c 'import json,sys;d=json.load(sys.stdin)["data"];print(json.dumps({"outcome":d["outcome"],"revision":d["revision"]}))'
}
EOF
  start_terminal agent agent.cast "$TERM_COLS" "$TERM_ROWS" "$cred_dir/$1.rc" "$demo_home"
}

# --- other Sessions, for the picker -----------------------------------------

step "start a disposable service"
# The binary itself runs in the background, so $! is the service process.
HOME="$demo_home" XDG_STATE_HOME='' "$bin" serve --json \
  >"$work_dir/serve.json" 2>"$work_dir/serve-error.json" &
service_pid=$!
for _ in {1..50}; do
  if stack health --json >/dev/null 2>&1; then break; fi
  sleep 0.1
done
stack health --json >/dev/null

step "create two older Sessions"
new_session docs-site 'Docs refresh' 01-writer
new_task d1 'Rewrite the install page' DOC-1 'Use the new install script.'
new_task d2 'Screenshots for the guide' DOC-2 'Light and dark.'
new_task d3 'Check every link' DOC-3 'Run the link checker.'
depends d1 d2
depends d1 d3
assign d1 01-writer
start 01-writer d1
ask 01-writer d1 'Keep the manual download steps, or link to the release page?'
new_session billing 'Upgrade the payment SDK' 01-lead
new_task b1 'Bump the SDK to v12' PAY-1 'Follow the migration guide.'
new_task b2 'Replay test webhooks' PAY-2 'Use the sandbox account.'
operator session close --workspace "$workspace" --session "$session" \
  --expected-revision "$(operator session inspect --workspace "$workspace" --session "$session" | field data.revision)" \
  --operation-id demo-close-billing >/dev/null

# --- record -----------------------------------------------------------------

step "start the browsers"
rec set viewport "$VIEW_CSS_WIDTH" "$VIEW_CSS_HEIGHT" "$VIEW_DEVICE_SCALE" >/dev/null
# The view follows the system theme. Dark matches the terminal and reads on
# GitHub in both themes.
rec set media dark >/dev/null
# Until a-stack new opens the view, show the color of its page.
rec open 'data:text/html,<body style="background:%23151a1c">' >/dev/null
shot set viewport 1440 900 >/dev/null
shot open about:blank >/dev/null

step "record"
# H.264 at 20 fps keeps up with the 1.6x frames; the GIFs have 10 fps.
rec record start "$work_dir/browser.mp4" --fps 20 >/dev/null
browser_start="$(now)"
start_terminal you you.cast "$TERM_COLS" "$TERM_ROWS" "$demo_home/.bashrc" "$demo_home/code/oauth-app"
story_start="${terminal_start[you]}"

scene 1 # Create a Session for your project.
say you line "a-stack new 'Add OAuth login'"
workspace="$(operator workspace list | python3 -c 'import json,sys
print(next(w["id"] for w in json.load(sys.stdin)["data"]["items"] if w["path"].endswith("/oauth-app")))')"
session="$(operator session list --workspace "$workspace" | field data.items.0.id)"
wait_view rec 'Add OAuth login'
no_fragment rec
open_view shot &
shot_pid=$!
overlay "$(scene_field 1 2)"
appoint 01-orchestrator
for name in 02-backend 03-frontend 04-reviewer; do register "$name"; done
poll
callout "$(element h1)" 'Your Session, live in the browser'
hold

scene 2 # The Orchestrator plans Tasks.
new_task app 'Register the OAuth app' AUTH-1 'Create the provider app; load its client ID and secret from config.'
new_task ui 'Login button and callback page' AUTH-2 'Add "Sign in with" to the header and handle /auth/callback.'
new_task token 'Token exchange endpoint' AUTH-3 'Exchange the code for tokens; check state and PKCE.'
new_task review 'Security review' AUTH-4 'Review state, PKCE, cookie flags and secret handling.'
depends app token
depends ui token
depends token review
assign app 02-backend
assign token 02-backend
assign ui 03-frontend
assign ui 02-backend
assign review 04-reviewer
poll
wait_card review Waiting
zoom "$(element '.now-column[data-column=waiting]')" 1.25
callout "$(element '.now-column[data-column=waiting]')" 'New Tasks wait here until an Agent starts' right
hold
agent_terminal 02-backend

scene 3 # Agents pick up Tasks.
say agent line "me task status --task \$APP --progress working$next--expected-revision $(revision 02-backend app) --operation-id be-start-1"
poll
wait_card app Working
zoom "$(card app)"
callout "$(card app)" '02-backend starts work'
hold
start 03-frontend ui
poll
wait_card ui Working
zoom "$(card ui)"
callout "$(card ui)" '03-frontend starts too'
hold

scene 4 # The graph shows which Task waits for which.
wait_graph rec 4 3
zoom "$(card token)" 1.25
callout "$(card token)" 'Curved lines show which Task waits for which' below
hold
click "$(card ui)"
wait_for "document.getElementById('task-dialog').open"
beat
js "demo.escape()"
callout "$(card ui)" 'A click lights up its lines and fades the rest'
hold
js "demo.escape()"
beat

scene 5 # Agents talk in Task mail.
say agent clear
say agent line "me mail send --task \$UI --body '@03-frontend the callback is /auth/callback'$next--operation-id be-mail-1"
beat
mail 03-frontend ui 'Thanks @02-backend, wiring the button to it now.' fe-1
poll
click "$(card ui)"
wait_for "document.getElementById('task-dialog').open"
beat
click "$(element '#task-dialog .mail-toggle')"
wait_for "document.querySelectorAll('#task-dialog .mail-message').length === 2"
js "demo.scroll($(element '#task-dialog .mail-section'))"
zoom "$(element '#task-dialog .mail-section')" 1.4
callout "$(element '#task-dialog .mail-toggle')" 'Task mail between the Agents' left
hold
callout "$(element '#task-dialog .mention')" '@03-frontend: this one is for 03-frontend' left
hold
js "demo.clear()"
zoom null
sleep 0.6
js "demo.escape()"

scene 6 # An Agent asks you a question.
say agent clear
say agent line "me task attention set --task \$APP --question 'GitHub or Google first?'$next--expected-revision $(revision 02-backend app) --operation-id be-ask-1"
poll
wait_card app 'Needs you'
zoom "$(card app)"
callout "$(card app)" 'Needs you: GitHub or Google first?'
wait "$shot_pid"
shot_pid=""
no_fragment shot
take_screenshots &
shot_pid=$!
hold
wait_for "document.getElementById('attention').textContent.includes('Needs you (1)')"
zoom "$(element '#attention')"
callout "$(element '#attention')" 'Questions for you wait here'
hold

scene 7 # You answer, work continues.
say agent clear
say agent line "me task attention resolve --task \$APP --note 'GitHub first'$next--expected-revision $(revision 02-backend app) --operation-id be-answer-1"
poll
wait_card app Working 'Needs you'
zoom "$(card app)"
callout "$(card app)" 'You answered "GitHub first". Back to work.'
hold
wait "$shot_pid"
shot_pid=""

scene 8 # Finished work folds away.
finish 02-backend app 'GitHub OAuth app registered; client ID and secret load from AUTH_GITHUB_*.'
poll
wait_for "document.querySelectorAll('#graph-panel .task-unit.compact').length === 1"
zoom "$(card app)" 1.3
callout "$(card app)" 'A finished Task shrinks to one line'
hold
finish 03-frontend ui 'Header shows "Sign in with GitHub"; the callback page handles errors.'
start 02-backend token
finish 02-backend token 'Code exchange with PKCE and state check; 14 new tests pass.'
poll
wait_for "document.getElementById('archived-label').textContent === 'Archived (2)'"
zoom "$(element '#archived')" 1.3
callout "$(element '#archived')" 'Its whole chain is done, so it folds into the archive' above
hold
click "$(element '#archived')"
wait_for "document.getElementById('archived').getAttribute('aria-pressed') === 'true'"
zoom null
callout "$(element '#archived')" 'The toggle shows archived Tasks again' above
hold
click "$(element '#archived')"
js "demo.clear()"
beat

scene 9 # The Agents tab shows who does what.
say you clear
zoom "$(element '#agents-panel .agent-row:nth-child(2) .agent-heading')" 1.25
callout "$(element '#agents-panel .agent-row:nth-child(2)')" 'Each Agent, with its Tasks'
hold

scene 10 # Pick another Session.
say you keys 'a-stack -r\r'
say you expect 'Tab to switch'
beat
say you keys '\t'
say you expect '(All)'
beat
say you keys 'docs'
beat
say you line ''
wait_view rec 'Docs refresh'
no_fragment rec
overlay "$(scene_field 10 2)"
wait_for "demo.ready($(card d1))"
zoom "$(card d1)"
callout "$(card d1)" 'Another Session, another question for you'
hold
beat
story_end="$(now)"
stop_terminal agent
stop_terminal you
rec record stop >/dev/null

step "record the picker"
# A wider terminal of its own, so the picker shows the full folder names.
start_terminal you picker.cast 80 8 "$demo_home/.bashrc" "$demo_home/code/oauth-app"
say you keys 'a-stack -r\r'
say you expect 'Tab to switch'
say you keys '\t'
say you expect '(All)'
sleep 0.5
say you mark
picker_at="$term_answer"
say you keys '\x1b'
sleep 0.3
stop_terminal you

# --- compose ----------------------------------------------------------------

step "render the terminals"
for name in you agent; do
  "$agg" --quiet --theme github-dark --font-size 16 --line-height 1.3 --idle-time-limit 600 --fps-cap 12 \
    --last-frame-duration 0 "$work_dir/$name.cast" "$work_dir/$name.gif"
done
"$agg" --quiet --theme github-dark --font-size 18 --idle-time-limit 600 \
  --select "$picker_at" "$work_dir/picker.cast" "$work_dir/picker.gif"
magick "$work_dir/picker.gif[0]" -strip -colors 64 "PNG8:$out/picker.png"

step "stack the terminal over the browser"
# Times on the story clock, which starts at the first prompt of your terminal.
since() { python3 -c 'import sys;print(max(0.0,float(sys.argv[1])-float(sys.argv[2])))' "$1" "$story_start"; }
offset="$(python3 -c 'import sys;print(max(0.0,float(sys.argv[1])-float(sys.argv[2])))' "$story_start" "$browser_start")"
agent_from="$(since "${terminal_start[agent]}")"
agent_to="$(since "${scene_switch[10]}")"
length="$(since "$story_end")"
term_height="$(magick "$work_dir/you.gif[0]" -format "%[fx:2*round(h*$GIF_WIDTH/w/2)]" info:)"
ffmpeg -v error -y -ss "$offset" -i "$work_dir/browser.mp4" -i "$work_dir/you.gif" -i "$work_dir/agent.gif" \
  -filter_complex "
  [0:v]fps=$FPS,scale=$GIF_WIDTH:$VIEW_HEIGHT:flags=lanczos,setsar=1[view];
  [1:v]fps=$FPS,scale=$GIF_WIDTH:$term_height:flags=lanczos,setsar=1,
    tpad=stop_duration=$length:stop_mode=clone[you];
  [2:v]fps=$FPS,scale=$GIF_WIDTH:$term_height:flags=lanczos,setsar=1,
    tpad=start_duration=$agent_from:start_mode=clone:stop_duration=$length:stop_mode=clone[agent];
  [you][agent]overlay=enable='between(t,$agent_from,$agent_to)'[term];
  [term][view]vstack=inputs=2,trim=duration=$length" \
  -c:v ffv1 "$work_dir/story.mkv"

step "make the GIFs"
# The captions are rendered by the screenshot browser.
cat >"$work_dir/caption.html" <<'EOF'
<!doctype html><meta charset="utf-8">
<body style="margin:0;height:44px;display:flex;align-items:center;gap:14px;padding:0 18px;
  background:#16191d;color:#fff;font:600 21px/1 Inter,ui-sans-serif,system-ui,sans-serif">
<span id="step" style="background:#f25f1c;border-radius:6px;padding:5px 9px;font-size:17px"></span>
<span id="text"></span>
<span id="who" style="margin-left:auto;color:#a9b3ba;font-size:15px;font-weight:500"></span>
</body>
EOF
shot set viewport "$GIF_WIDTH" "$CAPTION_HEIGHT" >/dev/null
shot open "file://$work_dir/caption.html" >/dev/null
scene_start[1]="$story_start"
scene_switch[${#scenes[@]} + 1]="$story_end"
scene_start[${#scenes[@]} + 1]="$story_end"
# make_gif NAME: cut the scenes of GIF NAME from the story, each under its
# caption, and join them. The feature GIFs leave out the terminal.
make_gif() {
  local name="$1" n count=0 total=0 step_text who text from to end inputs=() filters="" labels=""
  for n in $(seq "${#scenes[@]}"); do
    if [ "$(scene_field "$n" 1)" = "$name" ]; then total=$((total + 1)); fi
  done
  for n in $(seq "${#scenes[@]}"); do
    [ "$(scene_field "$n" 1)" = "$name" ] || continue
    count=$((count + 1))
    step_text="${badge[$name]:-$count/$total}"
    who="$(scene_field "$n" 3)" text="$(scene_field "$n" 4)"
    printf 'document.getElementById("step").textContent=%s;document.getElementById("text").textContent=%s;document.getElementById("who").textContent=%s' \
      "$(json "$step_text")" "$(json "$text")" "$(json "$who")" | shot eval --stdin >/dev/null
    shot screenshot "$work_dir/caption-$n.png" >/dev/null
    # A scene runs to the next scene of the same GIF, or to the next tab change.
    end="${scene_switch[n + 1]}"
    if [ "$(scene_field $((n + 1)) 1)" = "$name" ]; then end="${scene_start[n + 1]}"; fi
    from="$(since "${scene_start[n]}")" to="$(since "$end")"
    inputs+=(-loop 1 -framerate "$FPS" -t "$(python3 -c 'import sys;print(float(sys.argv[2])-float(sys.argv[1]))' "$from" "$to")" \
      -i "$work_dir/caption-$n.png")
    filters+="[0:v]trim=start=$from:end=$to,setpts=PTS-STARTPTS"
    if [ "$name" != demo ]; then filters+=",crop=$GIF_WIDTH:$VIEW_HEIGHT:0:$term_height"; fi
    filters+="[v$count];[$count:v]setsar=1[c$count];[c$count][v$count]vstack=inputs=2:shortest=1[s$count];"
    labels+="[s$count]"
  done
  ffmpeg -v error -y -i "$work_dir/story.mkv" "${inputs[@]}" -filter_complex "
    $filters
    ${labels}concat=n=$count:v=1,split[a][b];
    [a]palettegen=max_colors=128:stats_mode=diff[p];
    [b][p]paletteuse=dither=none:diff_mode=rectangle" \
    "$out/$name.gif"
}
for name in demo demo-graph demo-archive demo-agents; do make_gif "$name"; done

chmod 644 "$out"/*.gif "$out"/*.png
step "done"
for name in demo demo-graph demo-archive demo-agents; do
  printf '%s.gif: %s s, %s bytes\n' "$name" \
    "$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$out/$name.gif")" "$(stat -c %s "$out/$name.gif")"
done
