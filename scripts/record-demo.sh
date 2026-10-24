#!/usr/bin/env bash
# Record the README demo: docs/images/demo.gif and three PNG screenshots.
#
# The script builds a-stack from this checkout and starts a disposable service
# with its own temporary state. A small team plays a scripted "Add OAuth login"
# Session while agent-browser records the browser view. Everything temporary is
# deleted on exit. The view URL fragment is a credential: it is never printed
# or written to a file.
#
# Needs Go, ffmpeg, ImageMagick (magick), agent-browser and Python 3.
# Usage: scripts/record-demo.sh   (BEAT=SECONDS changes the pace, default 2.5)
set -euo pipefail
cd "$(dirname "$0")/.."
repo="$PWD"
out="$repo/docs/images"
beat_seconds="${BEAT:-2.5}"

for tool in go ffmpeg magick agent-browser python3; do
  command -v "$tool" >/dev/null || { printf 'record-demo: %s is not installed\n' "$tool" >&2; exit 1; }
done

umask 077
mkdir -p "$out"
work_dir="$(mktemp -d -t a-stack-demo.XXXXXX)"
state_dir="$(mktemp -d -t a-stack-demo-state.XXXXXX)"
credential_dir="$(mktemp -d -t a-stack-demo-credentials.XXXXXX)"
browser_session="a-stack-demo-$$"
service_pid=""

cleanup() {
  agent-browser --session "$browser_session" close >/dev/null 2>&1 || :
  if [ -n "$service_pid" ]; then
    kill "$service_pid" 2>/dev/null || :
    wait "$service_pid" 2>/dev/null || :
  fi
  rm -rf "$work_dir" "$state_dir" "$credential_dir"
}
trap cleanup EXIT

step() { printf '== %s\n' "$*"; }
beat() { sleep "$beat_seconds"; }

step "build a-stack"
bin="$work_dir/a-stack"
go build -o "$bin" ./cmd/a-stack

step "start a disposable service"
"$bin" serve --state-dir "$state_dir" --json >"$state_dir/serve.json" 2>"$state_dir/serve-error.json" &
service_pid=$!
for _ in {1..50}; do
  if "$bin" health --state-dir "$state_dir" --json >/dev/null 2>&1; then break; fi
  sleep 0.1
done
"$bin" health --state-dir "$state_dir" --json >/dev/null

# --- a-stack helpers --------------------------------------------------------

field() {
  python3 -c 'import json,sys
value=json.load(sys.stdin)
for key in sys.argv[1].split("."): value=value[key]
print(value)' "$1"
}
operator() {
  "$bin" --state-dir "$state_dir" --as operator --credential-file "$state_dir/operator.json" --json "$@"
}
# as_agent NAME ARGS...: run a command as the Agent NAME.
as_agent() {
  local name="$1"
  shift
  "$bin" --state-dir "$state_dir" --as agent --credential-file "$credential_dir/$name.json" \
    --workspace "$workspace" --session "$session" --json "$@"
}

declare -A agent_id task_id
lead=01-orchestrator

register() {
  agent_id[$1]="$(as_agent "$lead" agent register --workspace "$workspace" --session "$session" \
    --name "$1" --new-credential-file "$credential_dir/$1.json" \
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
mail() {
  as_agent "$1" mail send --task "${task_id[$2]}" --body "$3" --operation-id "demo-mail-$4" >/dev/null
}

# --- browser helpers --------------------------------------------------------

ab() { agent-browser --session "$browser_session" "$@"; }
js() { ab eval --stdin >/dev/null; }
click_card() {
  printf '[...document.querySelectorAll("#graph-panel .task-card")].find(c => c.textContent.includes(%s)).click()' \
    "$(printf '%s' "$1" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')" | js
}
fit() { ab click '#zoom-fit' >/dev/null; }
# wait_graph CARDS LINES: wait until the graph shows this many cards and lines.
wait_graph() {
  ab wait --fn "document.querySelectorAll('#graph-panel .task-card').length === $1 &&
    document.querySelectorAll('#graph-panel .connection').length === $2" >/dev/null
}
theme() { ab click ".theme-button[data-theme=$1]" >/dev/null; }
show_tab() { ab click "#$1-tab" >/dev/null; }
screenshot() {
  ab screenshot "$work_dir/$1" >/dev/null
  # Fewer colors keep the PNGs small; the view uses flat colors.
  magick "$work_dir/$1" -strip -colors 192 "PNG8:$out/$1"
}

# --- the team ---------------------------------------------------------------

step "create the Session and the team"
project_dir="$work_dir/acme-web"
mkdir "$project_dir"
workspace="$(operator workspace create --path "$project_dir" --operation-id demo-workspace-1 | field data.resource_id)"
session="$(operator session create --workspace "$workspace" --name 'Add OAuth login' \
  --operation-id demo-session-1 | field data.resource_id)"
agent_id[$lead]="$(operator agent register --workspace "$workspace" --session "$session" \
  --name "$lead" --new-credential-file "$credential_dir/$lead.json" \
  --operation-id demo-register-lead | field data.resource_id)"
operator session appoint --workspace "$workspace" --session "$session" --agent "${agent_id[$lead]}" \
  --expected-revision "$(operator session inspect --workspace "$workspace" --session "$session" | field data.revision)" \
  --operation-id demo-appoint-1 >/dev/null
register 02-backend
register 03-frontend
register 04-reviewer

step "open the browser view"
ab set viewport 1440 900 >/dev/null
ab open about:blank >/dev/null
# The URL stays in this variable and goes to the page through stdin only.
view_url="$(operator open --workspace "$workspace" --session "$session" --no-browser | field data.url)"
printf 'location.replace(%s)' "$(printf '%s' "$view_url" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')" | js
unset view_url
ab wait --fn "!document.querySelector('.preview-label').textContent.includes('connecting')" >/dev/null
case "$(ab get url)" in *'#'*) printf 'record-demo: the view kept its URL fragment\n' >&2; exit 1 ;; esac
theme light
show_tab graph
sleep 1

# --- the story --------------------------------------------------------------

step "record part 1: plan, assign, start, Needs you"
ab record start "$work_dir/part1.webm" >/dev/null
sleep 0.5

new_task plan 'Plan the OAuth flow' AUTH-1 'Pick the grant type and write down the redirect and token steps.'
new_task app 'Register the OAuth app' AUTH-2 'Create the provider app and load its client ID and secret from config.'
new_task ui 'Login button and callback page' AUTH-3 'Add "Sign in with" to the header and handle /auth/callback.'
depends plan app
depends plan ui
beat

new_task token 'Token exchange endpoint' AUTH-4 'Exchange the code for tokens and check the state parameter.'
new_task cookie 'Session cookie and logout' AUTH-5 'Store the session in a secure cookie and add /logout.'
new_task review 'Security review' AUTH-6 'Review the whole flow: state, PKCE, cookie flags and secrets.'
depends app token
depends token cookie
depends ui review
depends cookie review
wait_graph 6 6
fit
# The cards are on screen already; the next poll adds the assignments.
sleep 1.5

assign plan "$lead"
assign app 02-backend
assign token 02-backend
assign cookie 02-backend
assign ui 03-frontend
assign ui 02-backend
assign review 04-reviewer
start "$lead" plan
beat

finish "$lead" plan 'Authorization code flow with PKCE. Callback at /auth/callback; tokens never reach the browser.'
start 02-backend app
start 03-frontend ui
beat

change 02-backend app question task attention set \
  --question 'Which provider do we support first, GitHub or Google? I need it to register the app.'
beat

ab record stop >/dev/null

step "screenshots"
screenshot graph-light.png
theme dark
sleep 0.5
screenshot graph-dark.png
theme light
show_tab agents
sleep 0.5
screenshot agents.png
show_tab graph
sleep 0.5

step "record part 2: answer, mail, finish"
ab record start "$work_dir/part2.webm" >/dev/null
ab click '#attention' >/dev/null
beat
ab press Escape >/dev/null

change 02-backend app resolve task attention resolve --note 'Operator chose GitHub first; Google follows in the next release.'
finish 02-backend app 'GitHub OAuth app registered. Client ID and secret load from AUTH_GITHUB_* settings.'
start 02-backend token
mail 03-frontend ui 'What does /auth/callback return on success, @02-backend?' 01
mail 02-backend ui 'A 302 to the page the user came from, with the session cookie set.' 02
beat

finish 03-frontend ui 'Header shows "Sign in with GitHub"; callback page handles errors and redirects back.'
finish 02-backend token 'Code exchange with PKCE and state check. 14 new tests, all passing.'
start 02-backend cookie
beat

finish 02-backend cookie 'HttpOnly, Secure, SameSite=Lax cookie; /logout clears it and revokes the token.'
start 04-reviewer review
beat

finish 04-reviewer review 'No blocking issues. Suggested a shorter cookie lifetime; done in AUTH-5.'
beat

click_card 'Security review'
beat
ab record stop >/dev/null

step "make the GIF"
ffmpeg -v error -y -i "$work_dir/part1.webm" -i "$work_dir/part2.webm" -filter_complex \
  '[0:v][1:v]concat=n=2:v=1:a=0,fps=10,scale=960:-2:flags=lanczos,split[a][b];[a]palettegen=max_colors=128:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle' \
  "$out/demo.gif"

chmod 644 "$out"/demo.gif "$out"/graph-light.png "$out"/graph-dark.png "$out"/agents.png
step "done"
ls -l "$out"
