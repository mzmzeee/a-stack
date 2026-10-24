#!/usr/bin/env bash
# Record the README demo: docs/images/demo.gif, graph-light.png, graph-dark.png,
# agents.png and picker.png.
#
# The script builds a-stack from this checkout and runs a real Session on a
# disposable service. A temporary HOME holds the state, the credentials and the
# project folders; everything is deleted on exit. The left half of the GIF is a
# real terminal: scripts/demo-terminal.py types into a Bash in a
# pseudo-terminal and records it, and agg renders the recording. The right half
# is the browser view, recorded by agent-browser at the same time. Other Agents
# work in the background, so several cards change at once.
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
out="$repo/docs/images"

AGG_VERSION=v1.9.0
GIF_WIDTH=1200 GIF_HEIGHT=720 TERM_WIDTH=440 TERM_COLS=56 TERM_ROWS=34
VIEW_WIDTH=$((GIF_WIDTH - TERM_WIDTH)) VIEW_HEIGHT=$GIF_HEIGHT

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
service_pid="" terminal_pid=""

cleanup() {
  agent-browser --session "$rec_session" close >/dev/null 2>&1 || :
  agent-browser --session "$shot_session" close >/dev/null 2>&1 || :
  for pid in "$terminal_pid" "$service_pid"; do
    if [ -n "$pid" ]; then
      kill "$pid" 2>/dev/null || :
      wait "$pid" 2>/dev/null || :
    fi
  done
  rm -rf "$work_dir" "$demo_home"
}
trap cleanup EXIT

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

declare -A agent_id task_id
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
  wait_graph shot 7 8
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

# --- the terminal -----------------------------------------------------------

cat >"$demo_home/.bashrc" <<'EOF'
PS1='\[\e]0;demo\a\e[1;32m\]\w\[\e[0m\] \$ '
HISTFILE=/dev/null
bind 'set enable-bracketed-paste off'
EOF
mkfifo "$work_dir/control" "$work_dir/ack"
# Read-write opens do not block, so a recorder that fails cannot hang the script.
exec 3<>"$work_dir/control" 4<>"$work_dir/ack"
# term INSTRUCTION [TEXT]: see scripts/demo-terminal.py.
term() {
  printf '%s %s\n' "$1" "${2:-}" >&3
  read -r -t 60 term_answer <&4 || die "the terminal recorder stopped at: $1 ${2:-}"
}
# start_terminal CAST COLS ROWS: record a Bash in the project folder. The
# recording starts at the first prompt; terminal_start holds that Unix time.
start_terminal() {
  (
    cd "$demo_home/code/oauth-app"
    exec env -u XDG_STATE_HOME HOME="$demo_home" PATH="$work_dir/bin:$PATH" TERM=xterm-256color \
      DEMO_REAL_HOME="$HOME" DEMO_BROWSER="$rec_session" \
      python3 "$repo/scripts/demo-terminal.py" --control "$work_dir/control" --ack "$work_dir/ack" \
      --cast "$work_dir/$1" --cols "$2" --rows "$3" \
      -- bash --noprofile --rcfile "$demo_home/.bashrc" -i
  ) &
  terminal_pid=$!
  read -r -t 60 terminal_start <&4 || die "the terminal recorder did not start"
}
stop_terminal() {
  term stop
  wait "$terminal_pid" 2>/dev/null || :
  terminal_pid=""
}

# --- other Sessions, for the picker -----------------------------------------

step "start a disposable service"
stack serve --json >"$work_dir/serve.json" 2>"$work_dir/serve-error.json" &
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
rec set viewport "$VIEW_WIDTH" "$VIEW_HEIGHT" >/dev/null
# Until a-stack new opens the view, show the color of its page.
rec open 'data:text/html,<body style="background:%23f4f3ef">' >/dev/null
shot set viewport 1440 900 >/dev/null
shot open about:blank >/dev/null

step "record"
rec record start "$work_dir/browser.webm" >/dev/null
browser_start="$(date +%s.%N)"
start_terminal terminal.cast "$TERM_COLS" "$TERM_ROWS"
story_start="$terminal_start"

sleep 0.3
term line "a-stack new 'Add OAuth login'"
workspace="$(operator workspace list | python3 -c 'import json,sys
print(next(w["id"] for w in json.load(sys.stdin)["data"]["items"] if w["path"].endswith("/oauth-app")))')"
session="$(operator session list --workspace "$workspace" | field data.items.0.id)"
no_fragment rec
open_view shot &
shot_pid=$!

appoint 01-orchestrator
for name in 02-backend 03-frontend 04-database 05-reviewer; do register "$name"; done
new_task plan 'Plan the OAuth flow' AUTH-1 'Pick the grant type; write down the redirect and token steps.'
new_task app 'Register the OAuth app' AUTH-2 'Create the provider app; load its client ID and secret from config.'
new_task ui 'Login button and callback page' AUTH-3 'Add "Sign in with" to the header and handle /auth/callback.'
new_task schema 'Linked accounts table' AUTH-4 'Store the provider user ID for each account.'
depends plan app
depends plan ui
sleep 1.2

new_task token 'Token exchange endpoint' AUTH-5 'Exchange the code for tokens; check state and PKCE.'
new_task e2e 'End-to-end login test' AUTH-6 'Sign in, sign out and a denied consent, in a real browser.'
new_task review 'Security review' AUTH-7 'Review state, PKCE, cookie flags and secret handling.'
depends plan token
depends schema token
depends ui e2e
depends token e2e
depends app review
depends token review
assign plan 01-orchestrator
assign app 02-backend
assign token 02-backend
assign ui 03-frontend
assign ui 02-backend
assign schema 04-database
assign e2e 03-frontend
assign review 05-reviewer
start 01-orchestrator plan
start 04-database schema
wait_graph rec 7 8
rec click '#zoom-fit' >/dev/null
sleep 1.5

finish 01-orchestrator plan 'Authorization code flow with PKCE. Tokens stay on the server.'
start 03-frontend ui
cat >"$cred_dir/02-backend.env" <<EOF
AS="--as agent --credential-file $cred_dir/02-backend.json --workspace $workspace --session $session"
APP=${task_id[app]} UI=${task_id[ui]} TOKEN=${task_id[token]}
rev() { a-stack \$AS task show --task "\$1" --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["revision"])'; }
EOF
term line '# 02-backend picks up its Task'
term line 'source ~/.agents/02-backend.env'
term line "a-stack \$AS task status --task \$APP --progress working --expected-revision \$(rev \$APP) --operation-id app-start-1"
finish 04-database schema 'Migration 0042 adds linked_accounts with a unique (provider, subject) index.'
term line "a-stack \$AS task attention set --task \$APP --question 'GitHub or Google first?' --expected-revision \$(rev \$APP) --operation-id app-ask-1"
wait "$shot_pid"
no_fragment shot
sleep 1
take_screenshots &
shot_pid=$!
term line "a-stack \$AS mail send --task \$UI --body '@03-frontend the callback is /auth/callback' --operation-id ui-mail-1"
mail 03-frontend ui 'Thanks @02-backend, wiring the button to it now.' ui-2
term line "a-stack \$AS task attention resolve --task \$APP --note 'GitHub first' --expected-revision \$(rev \$APP) --operation-id app-answer-1"
sleep 0.5
finish 02-backend app 'GitHub OAuth app registered; client ID and secret load from AUTH_GITHUB_*.'
start 02-backend token
finish 03-frontend ui 'Header shows "Sign in with GitHub"; the callback page handles errors.'
sleep 2.2
finish 02-backend token 'Code exchange with PKCE and state check; 14 new tests pass.'
start 03-frontend e2e
start 05-reviewer review
sleep 2.2
finish 03-frontend e2e 'Sign-in, sign-out and denied consent pass in Chromium and Firefox.'
sleep 2.2
finish 05-reviewer review 'No blocking issues. Cookie is HttpOnly, Secure, SameSite=Lax.'
sleep 1.5
wait "$shot_pid"

term line 'clear'
term keys 'a-stack -r\r'
term expect 'Tab to switch'
sleep 1.4
term keys '\t'
term expect '(All)'
sleep 1.4
term keys 'docs'
sleep 1.4
term line ''
sleep 3
stop_terminal
rec record stop >/dev/null

step "record the picker"
# A wider terminal of its own, so the picker shows the full folder names.
start_terminal picker.cast 80 8
term keys 'a-stack -r\r'
term expect 'Tab to switch'
term keys '\t'
term expect '(All)'
sleep 0.5
term mark
picker_at="$term_answer"
term keys '\x1b'
sleep 0.3
stop_terminal

# --- compose ----------------------------------------------------------------

step "render the terminal"
"$agg" --quiet --theme github-dark --font-size 16 --idle-time-limit 600 --fps-cap 12 \
  --last-frame-duration 0 "$work_dir/terminal.cast" "$work_dir/terminal.gif"
"$agg" --quiet --theme github-dark --font-size 18 --idle-time-limit 600 \
  --select "$picker_at" "$work_dir/picker.cast" "$work_dir/picker.gif"
magick "$work_dir/picker.gif[0]" -strip -colors 64 "PNG8:$out/picker.png"
term_background="$(magick "$work_dir/terminal.gif[0]" -format '%[hex:p{1,1}]' info:)"

step "make the GIF"
offset="$(python3 -c 'import sys;print(max(0.0,float(sys.argv[1])-float(sys.argv[2])))' "$story_start" "$browser_start")"
ffmpeg -v error -y -ss "$offset" -i "$work_dir/browser.webm" -i "$work_dir/terminal.gif" -filter_complex "
  [0:v]fps=10,scale=$VIEW_WIDTH:$VIEW_HEIGHT:flags=lanczos,setsar=1[view];
  [1:v]fps=10,scale=$TERM_WIDTH:$GIF_HEIGHT:force_original_aspect_ratio=decrease:flags=lanczos,
    pad=$TERM_WIDTH:$GIF_HEIGHT:(ow-iw)/2:(oh-ih)/2:color=0x$term_background,setsar=1[term];
  [term][view]hstack=inputs=2:shortest=1,split[a][b];
  [a]palettegen=max_colors=192:stats_mode=diff[p];
  [b][p]paletteuse=dither=none:diff_mode=rectangle" \
  "$out/demo.gif"

chmod 644 "$out"/demo.gif "$out"/graph-light.png "$out"/graph-dark.png "$out"/agents.png "$out"/picker.png
step "done"
ls -l "$out"
