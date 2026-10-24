#!/usr/bin/env bash
# Record the README demo: docs/images/demo.gif, graph-light.png, graph-dark.png,
# agents.png and picker.png.
#
# The script builds a-stack from this checkout and runs a real Session on a
# disposable service. A temporary HOME holds the state, the credentials and the
# project folders; everything is deleted on exit.
#
# The GIF has three bands. At the top, a caption names the scene. In the
# middle, a real terminal: scripts/demo-terminal.py types into a Bash in a
# pseudo-terminal and records it, and agg renders the recording. It is your
# terminal in the first and last scene and the Agent 02-backend's terminal in
# between. At the bottom, the browser view, recorded by agent-browser at the
# same time. The commands and the service are real. Only the annotations are
# added: scripts/demo-overlay.js draws an outline and a callout on what just
# changed and zooms the page, in the recording browser only.
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

# The scenes, in order: the view tab, whose terminal shows, and the caption.
# A tab the view does not have falls back to Graph.
scenes=(
  "now|Your terminal|Create a Session for your project"
  "graph|Agent 02-backend's terminal|Agents pick up Tasks"
  "now|Agent 02-backend's terminal|An Agent asks you a question"
  "now|Agent 02-backend's terminal|You answer, work continues"
  "now|Your terminal|Pick another Session"
)

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
# wait_card KEY TEXT [WITHOUT]: wait until the card shows TEXT and not WITHOUT.
wait_card() {
  rec wait --fn "demo.ready($(card "$1"), $(json "$2"))${3:+ && !demo.ready($(card "$1"), $(json "$3"))}" >/dev/null
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
    rec wait --fn "!document.getElementById('graph-panel').hidden" >/dev/null
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
  magick "$work_dir/$1" -strip -colors 192 "PNG8:$out/$1"
}
take_screenshots() {
  shot click '#graph-tab' >/dev/null
  wait_graph shot 4 3
  shot click '#zoom-fit' >/dev/null
  sleep 0.5
  screenshot graph-light.png
  shot click '.theme-button[data-theme=dark]' >/dev/null
  sleep 0.5
  screenshot graph-dark.png
  shot click '.theme-button[data-theme=light]' >/dev/null
  shot click '#agents-tab' >/dev/null
  sleep 0.5
  screenshot agents.png
}

# --- scenes -----------------------------------------------------------------

now() { date +%s.%N; }
scene_start=()
# scene N: start scene N. A scene on another tab starts zoomed out, so the
# viewer sees the whole tab first.
scene() {
  scene_start[$1]="$(now)"
  if [ "$1" -gt 1 ]; then
    js "demo.clear()"
    if [ "$(scene_tab "$1")" != "$(scene_tab $(($1 - 1)))" ]; then
      js "demo.zoom(null)"
      show_tab "$(scene_tab "$1")"
    fi
  fi
}
# scene_tab N: the view tab of scene N.
scene_tab() { printf '%s' "${scenes[$1 - 1]%%|*}"; }

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
APP=${task_id[app]} TOKEN=${task_id[token]}
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
# H.264 at 20 fps keeps up with the 1.6x frames; the GIF has 10 fps.
rec record start "$work_dir/browser.mp4" --fps 20 >/dev/null
browser_start="$(now)"
start_terminal you you.cast "$TERM_COLS" "$TERM_ROWS" "$demo_home/.bashrc" "$demo_home/code/oauth-app"
story_start="${terminal_start[you]}"

# 1. Create a Session for your project.
scene 1
sleep 0.8
say you line "a-stack new 'Add OAuth login'"
workspace="$(operator workspace list | python3 -c 'import json,sys
print(next(w["id"] for w in json.load(sys.stdin)["data"]["items"] if w["path"].endswith("/oauth-app")))')"
session="$(operator session list --workspace "$workspace" | field data.items.0.id)"
wait_view rec 'Add OAuth login'
no_fragment rec
open_view shot &
shot_pid=$!
overlay "$(scene_tab 1)"
callout "$(element h1)" 'Your Session, live in the browser'
appoint 01-orchestrator
for name in 02-backend 03-frontend 04-reviewer; do register "$name"; done
sleep 1.8

# 2. Agents pick up Tasks.
new_task app 'Register the OAuth app' AUTH-1 'Create the provider app; load its client ID and secret from config.'
new_task ui 'Login button and callback page' AUTH-2 'Add "Sign in with" to the header and handle /auth/callback.'
new_task token 'Token exchange endpoint' AUTH-3 'Exchange the code for tokens; check state and PKCE.'
new_task review 'Security review' AUTH-4 'Review state, PKCE, cookie flags and secret handling.'
agent_terminal 02-backend
scene 2
depends app token
depends token review
depends ui token
assign app 02-backend
assign token 02-backend
assign ui 03-frontend
assign review 04-reviewer
if [ "$(scene_tab 2)" = graph ]; then wait_graph rec 4 3; fi
show_tab "$(scene_tab 2)"
zoom "$(card token)" 1.2
callout "$(card token)" 'Arrows show which Task waits for which' below
sleep 2.4
say agent line "me task status --task \$APP --progress working$next--expected-revision $(revision 02-backend app) --operation-id be-start-1"
wait_card app Working
zoom "$(card app)"
callout "$(card app)" '02-backend starts: Working'
sleep 2.2
start 03-frontend ui
wait_card ui Working
callout "$(card ui)" '03-frontend starts too'
sleep 1.8

# 3. An Agent asks you a question.
scene 3
say agent clear
say agent line "me task attention set --task \$APP --question 'GitHub or Google first?'$next--expected-revision $(revision 02-backend app) --operation-id be-ask-1"
wait_card app 'Needs you'
zoom "$(card app)"
callout "$(card app)" 'Needs you: GitHub or Google first?'
wait "$shot_pid"
shot_pid=""
no_fragment shot
take_screenshots &
shot_pid=$!
sleep 2.4
rec wait --fn "document.getElementById('attention').textContent.includes('1 needs you')" >/dev/null
zoom "$(element '#attention')"
callout "$(element '#attention')" 'Questions for you wait here'
sleep 2.2

# 4. You answer, work continues.
scene 4
say you clear
say agent clear
say agent line "me task attention resolve --task \$APP --note 'GitHub first'$next--expected-revision $(revision 02-backend app) --operation-id be-answer-1"
wait_card app Working 'Needs you'
zoom "$(card app)"
callout "$(card app)" 'You answered "GitHub first". Back to work.'
sleep 2.2
finish 02-backend app 'GitHub OAuth app registered; client ID and secret load from AUTH_GITHUB_*.'
start 02-backend token
wait_card token Working
zoom "$(card token)"
callout "$(card token)" 'Done, so the next Task starts'
sleep 2.4
wait "$shot_pid"
shot_pid=""

# 5. Pick another Session.
scene 5
say you keys 'a-stack -r\r'
say you expect 'Tab to switch'
sleep 1.2
say you keys '\t'
say you expect '(All)'
sleep 1
say you keys 'docs'
sleep 1.2
say you line ''
wait_view rec 'Docs refresh'
no_fragment rec
overlay "$(scene_tab 5)"
rec wait --fn "demo.ready($(card d1))" >/dev/null
zoom "$(card d1)"
callout "$(card d1)" 'Another Session, another question for you'
sleep 3
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

step "render the terminals and the captions"
for name in you agent; do
  "$agg" --quiet --theme github-dark --font-size 16 --line-height 1.3 --idle-time-limit 600 --fps-cap 12 \
    --last-frame-duration 0 "$work_dir/$name.cast" "$work_dir/$name.gif"
done
"$agg" --quiet --theme github-dark --font-size 18 --idle-time-limit 600 \
  --select "$picker_at" "$work_dir/picker.cast" "$work_dir/picker.gif"
magick "$work_dir/picker.gif[0]" -strip -colors 64 "PNG8:$out/picker.png"

# The captions are rendered by the screenshot browser, one image per scene.
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
caption_inputs=() caption_filters="" caption_labels=""
scene_start[1]="$story_start"
scene_start[${#scenes[@]} + 1]="$story_end"
for n in $(seq "${#scenes[@]}"); do
  IFS='|' read -r _ who text <<<"${scenes[n - 1]}"
  printf 'document.getElementById("step").textContent=%s;document.getElementById("text").textContent=%s;document.getElementById("who").textContent=%s' \
    "$(json "$n/${#scenes[@]}")" "$(json "$text")" "$(json "$who")" | shot eval --stdin >/dev/null
  shot screenshot "$work_dir/caption-$n.png" >/dev/null
  duration="$(python3 -c 'import sys;print(float(sys.argv[2])-float(sys.argv[1]))' "${scene_start[n]}" "${scene_start[n + 1]}")"
  caption_inputs+=(-loop 1 -framerate "$FPS" -t "$duration" -i "$work_dir/caption-$n.png")
  caption_filters+="[$((n + 2)):v]setsar=1[c$n];"
  caption_labels+="[c$n]"
done

step "make the GIF"
# Times on the story clock, which starts at the first prompt of your terminal.
since() { python3 -c 'import sys;print(max(0.0,float(sys.argv[1])-float(sys.argv[2])))' "$1" "$story_start"; }
offset="$(python3 -c 'import sys;print(max(0.0,float(sys.argv[1])-float(sys.argv[2])))' "$story_start" "$browser_start")"
agent_from="$(since "${terminal_start[agent]}")"
agent_to="$(since "${scene_start[5]}")"
length="$(since "$story_end")"
term_height="$(magick "$work_dir/you.gif[0]" -format "%[fx:2*round(h*$GIF_WIDTH/w/2)]" info:)"
ffmpeg -v error -y -ss "$offset" -i "$work_dir/browser.mp4" -i "$work_dir/you.gif" -i "$work_dir/agent.gif" \
  "${caption_inputs[@]}" -filter_complex "
  $caption_filters
  ${caption_labels}concat=n=${#scenes[@]}:v=1,fps=${FPS}[caption];
  [0:v]fps=$FPS,scale=$GIF_WIDTH:$VIEW_HEIGHT:flags=lanczos,setsar=1[view];
  [1:v]fps=$FPS,scale=$GIF_WIDTH:$term_height:flags=lanczos,setsar=1,
    tpad=stop_duration=$length:stop_mode=clone[you];
  [2:v]fps=$FPS,scale=$GIF_WIDTH:$term_height:flags=lanczos,setsar=1,
    tpad=start_duration=$agent_from:start_mode=clone:stop_duration=$length:stop_mode=clone[agent];
  [you][agent]overlay=enable='between(t,$agent_from,$agent_to)'[term];
  [caption][term][view]vstack=inputs=3,trim=duration=$length,split[a][b];
  [a]palettegen=max_colors=128:stats_mode=diff[p];
  [b][p]paletteuse=dither=none:diff_mode=rectangle" \
  "$out/demo.gif"

chmod 644 "$out"/demo.gif "$out"/graph-light.png "$out"/graph-dark.png "$out"/agents.png "$out"/picker.png
step "done"
ls -l "$out"
