package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// `a-stack hook` runs as a Claude Code or Codex hook for one Agent. The hook
// configuration that `hook install` writes names the Agent explicitly:
// credential file, Workspace, Session and Agent ID, never the environment.

func isHookCommand(command string) bool {
	return command == "hook" || command == "hook install" || command == "hook uninstall"
}

// isHookRun reports whether args run the hook handler, which always exits 0.
func isHookRun(args []string) bool {
	return len(args) > 0 && args[0] == "hook" && (len(args) == 1 || strings.HasPrefix(args[1], "-"))
}

type hookFlags struct{ claude, codex, noStopGuard, noMailNudge bool }

func (f *hookFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.claude, "claude", false, "Only Claude Code (default: Claude Code and Codex)")
	fs.BoolVar(&f.codex, "codex", false, "Only Codex (default: Claude Code and Codex)")
	fs.BoolVar(&f.noStopGuard, "no-stop-guard", false, "Let the agent stop with a working Task that has no report")
	fs.BoolVar(&f.noMailNudge, "no-mail-nudge", false, "Do not tell the agent about new Task mail after tool calls")
}

// hookTimeout bounds one hook run; the harness gets hookTimeoutSeconds.
const hookTimeout, hookTimeoutSeconds = 2 * time.Second, 5

// hookEvents maps each event that install registers to the runtime state it
// records; SessionStart prints the reminder instead.
var hookEvents = []struct{ event, matcher, state string }{
	{"SessionStart", "", ""},
	{"UserPromptSubmit", "", "working"},
	{"PostToolUse", "", "working"},
	{"PermissionRequest", "", "blocked"},
	{"Notification", "elicitation_dialog", "blocked"}, // Claude Code only
	{"Stop", "", "idle"},
}

type hookInput struct {
	Event            string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	NotificationType string `json:"notification_type"`
	StopHookActive   bool   `json:"stop_hook_active"`
}

type hookIdentity struct {
	state, credential, agent, runtimeID, worktree string
	ref                                           stack.Context
	noStopGuard, noMailNudge                      bool
}

// runHook handles one hook event. Any failure ends it silently: a hook must
// never stop the Agent, also when a-stack is down.
func runHook(id hookIdentity, stdin io.Reader, stdout io.Writer) {
	var in hookInput
	if json.NewDecoder(io.LimitReader(stdin, 16<<20)).Decode(&in) != nil {
		return
	}
	cwd := in.Cwd
	if canonical, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = canonical
	}
	if id.worktree != "" && !within(cwd, id.worktree) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	client, err := stack.NewClient(id.state, id.credential, stack.AgentCapability)
	if err != nil {
		return
	}
	if in.Event == "SessionStart" {
		if text, err := hookReminder(ctx, client, id); err == nil {
			_, _ = io.WriteString(stdout, text)
		}
		if !id.noMailNudge {
			hookMailNudge(ctx, client, id, in) // reads up to the newest mail
		}
		return
	}
	// The Stop guard blocks one stop at most: when the agent stops again
	// right after, stop_hook_active is true.
	if in.Event == "Stop" && !id.noStopGuard && !in.StopHookActive {
		if reason, err := hookStopGuard(ctx, client, id); err == nil && reason != "" {
			_ = json.NewEncoder(stdout).Encode(map[string]string{"decision": "block", "reason": reason})
			return // the agent goes on, so its state stays working
		}
	}
	for _, e := range hookEvents {
		if e.event == in.Event && e.state != "" && (e.matcher == "" || e.matcher == in.NotificationType) && id.runtimeID != "" {
			_ = hookObserve(ctx, client, id, e.state)
		}
	}
	if (in.Event == "PostToolUse" || in.Event == "UserPromptSubmit") && !id.noMailNudge {
		if text := hookMailNudge(ctx, client, id, in); text != "" {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": in.Event, "additionalContext": text}})
		}
	}
}

// hookObserve records the runtime state. It reads the Agent first and writes
// only when the state changes, so a tool call costs one read.
func hookObserve(ctx context.Context, client *stack.Client, id hookIdentity, state string) error {
	agents, err := listPages[stack.Agent](ctx, client, "/v1/agent/list", id.ref)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.ID == id.agent && a.RuntimeState != state {
			_, err = client.Do(ctx, "POST", "/v1/agent/observe", stack.AgentObserveRequest{Context: id.ref, AgentID: a.ID, ExpectedRevision: a.Revision, RuntimeID: id.runtimeID, State: state})
			return err
		}
	}
	return nil
}

// hookReminder is the SessionStart context, also after compaction: who the
// Agent is, how to run a-stack as itself, its open Tasks, and to check mail.
func hookReminder(ctx context.Context, client *stack.Client, id hookIdentity) (string, error) {
	data, err := client.Do(ctx, "POST", "/v1/session/inspect", id.ref)
	if err != nil {
		return "", err
	}
	var session struct {
		Data stack.Session `json:"data"`
	}
	if json.Unmarshal(data, &session) != nil {
		return "", invalid("invalid service response")
	}
	agents, err := listPages[stack.Agent](ctx, client, "/v1/agent/list", id.ref)
	if err != nil {
		return "", err
	}
	name := ""
	for _, a := range agents {
		if a.ID == id.agent {
			name = a.Name
		}
	}
	if name == "" {
		return "", invalid("Agent not in this Session")
	}
	mine, err := assignedTasks(ctx, client, id.ref, id.agent)
	if err != nil {
		return "", err
	}
	tasks, err := listPages[stack.Task](ctx, client, "/v1/task/list", id.ref)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "a-stack: you are the Agent %s in the a-stack Session %q (Workspace %s, Session %s).", name, printable(session.Data.Name), id.ref.WorkspaceID, id.ref.SessionID)
	if session.Data.Closed {
		b.WriteString(" The Session is closed.")
	}
	command := "a-stack"
	if id.state != "" {
		command += " --state-dir " + quotePath(id.state)
	}
	fmt.Fprintf(&b, "\nRun a-stack as yourself: %s --as agent --credential-file %s --workspace %s --session %s COMMAND\n", command, quotePath(id.credential), id.ref.WorkspaceID, id.ref.SessionID)
	open := 0
	for _, t := range tasks {
		if !mine[t.ID] || t.Progress == "finished" || t.Progress == "cancelled" {
			continue
		}
		if open == 0 {
			b.WriteString("Your open Tasks:\n")
		}
		open++
		state := t.Progress
		if t.NeedsYou {
			state += ", waits for the Operator's answer"
		}
		fmt.Fprintf(&b, "- %s (Task %s): %s\n", printable(truncate(t.Title, 120)), t.ID, state)
	}
	if open == 0 {
		b.WriteString("You have no open Tasks.\n")
	}
	b.WriteString("Run mail check now, and follow the a-stack skill: Task mail at milestones, a report, then finish.\n")
	return b.String(), nil
}

// assignedTasks reads the Session's assignments and returns the Agent's Tasks.
func assignedTasks(ctx context.Context, client *stack.Client, ref stack.Context, agent string) (map[string]bool, error) {
	mine := map[string]bool{}
	err := sessionPages(ctx, client, stack.SnapshotPageRequest{Context: ref, Kind: "assignments"}, func(a stack.Assignment) {
		if a.AgentID == agent {
			mine[a.TaskID] = true
		}
	})
	return mine, err
}

// sessionPages reads every page of one session page kind, at one read
// revision, and calls each for every item.
func sessionPages[T any](ctx context.Context, client *stack.Client, request stack.SnapshotPageRequest, each func(T)) error {
	request.Limit = 100
	for {
		data, err := client.Do(ctx, "POST", "/v1/session/page", request)
		if err != nil {
			return err
		}
		var page struct {
			Data struct {
				ReadRevision int64  `json:"read_revision"`
				Items        []T    `json:"items"`
				Next         string `json:"next"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &page) != nil {
			return invalid("invalid service response")
		}
		for _, item := range page.Data.Items {
			each(item)
		}
		if page.Data.Next == "" {
			return nil
		}
		request.After, request.ReadRevision = page.Data.Next, page.Data.ReadRevision
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// quotePath is shellQuote for a path with forward slashes. On Windows,
// Claude Code runs hook commands in Git Bash, which mangles backslashes;
// a-stack reads C:/folder as C:\folder.
func quotePath(path string) string { return shellQuote(filepath.ToSlash(path)) }

// hookSettings returns the settings files to change: Claude Code's
// settings.json (settings.local.json in a project, so it is not committed)
// and Codex's hooks.json.
func hookSettings(f hookFlags, project bool) (map[string]string, error) {
	files := map[string]string{}
	both := !f.claude && !f.codex
	if project {
		dir, err := os.Getwd()
		if err != nil {
			return nil, invalid("cannot read the current folder")
		}
		if both || f.claude {
			files["claude"] = filepath.Join(dir, ".claude", "settings.local.json")
		}
		if both || f.codex {
			files["codex"] = filepath.Join(dir, ".codex", "hooks.json")
		}
		return files, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, invalid("cannot find home directory")
	}
	for harness, variable := range map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME"} {
		if !both && (harness == "claude") != f.claude {
			continue
		}
		dir := filepath.Join(home, "."+harness)
		if v := os.Getenv(variable); v != "" {
			if !filepath.IsAbs(v) {
				return nil, invalid(variable + " must be absolute")
			}
			dir = v
		}
		name := "settings.json"
		if harness == "codex" {
			name = "hooks.json"
		}
		files[harness] = filepath.Join(dir, name)
	}
	return files, nil
}

type hookInstall struct {
	identity                 hookIdentity
	name                     string
	flags                    hookFlags
	project, remove, machine bool
	stdout                   io.Writer
}

// run installs or removes the hook entries of one worktree in each settings
// file. Install replaces this worktree's earlier a-stack entries; other hooks
// and other worktrees' entries stay.
func (h *hookInstall) run(ctx context.Context) error {
	id := &h.identity
	if id.worktree == "" {
		dir, err := os.Getwd()
		if err != nil {
			return invalid("cannot read the current folder")
		}
		id.worktree = dir
	}
	if !filepath.IsAbs(id.worktree) {
		return invalid("--worktree must be absolute")
	}
	if canonical, err := filepath.EvalSymlinks(id.worktree); err == nil {
		id.worktree = canonical
	}
	command := ""
	if !h.remove {
		var err error
		if command, err = h.command(ctx); err != nil {
			return err
		}
	}
	files, err := hookSettings(h.flags, h.project)
	if err != nil {
		return err
	}
	done := []string{}
	for _, harness := range []string{"claude", "codex"} {
		file, ok := files[harness]
		if !ok {
			continue
		}
		changed, err := mergeHooks(file, harness, id.worktree, command)
		if err != nil {
			return err
		}
		if changed {
			done = append(done, file)
		}
	}
	key, text := "installed", "Installed the a-stack hooks for "+printable(id.worktree)+" in:\n"
	if h.remove {
		key, text = "removed", "Removed the a-stack hooks for "+printable(id.worktree)+" from:\n"
	}
	if h.machine {
		return json.NewEncoder(h.stdout).Encode(map[string]any{"data": map[string]any{"worktree": id.worktree, "agent_id": id.agent, key: done}})
	}
	if len(done) == 0 {
		text = "No a-stack hooks for " + printable(id.worktree) + " were installed.\n"
	}
	for _, file := range done {
		text += "  " + file + "\n"
	}
	if !h.remove {
		if id.runtimeID == "" {
			text += "Without --runtime-id the hooks only print the reminder at session start; they record no runtime state.\n"
		}
		if _, ok := files["codex"]; ok {
			text += "Codex runs new hooks only after you trust them: run /hooks in Codex.\n"
		}
	}
	_, err = io.WriteString(h.stdout, text)
	return err
}

// command checks the identity and builds the hook command. With --name it
// asks the service for the Agent ID.
func (h *hookInstall) command(ctx context.Context) (string, error) {
	id := &h.identity
	if id.credential == "" || !filepath.IsAbs(id.credential) {
		return "", invalid("--credential-file with an absolute path required")
	}
	credential, err := stack.ReadCredential(id.credential)
	if err != nil {
		return "", err
	}
	if credential.Kind != stack.AgentCapability {
		return "", invalid("the credential file is not an Agent credential")
	}
	// A credential file from v0.1.5 on names its Workspace, Session and Agent.
	ref := lifecycleFlags{workspace: id.ref.WorkspaceID, session: id.ref.SessionID}
	if err = fillContext(credential, &ref, "hook install"); err != nil {
		return "", err
	}
	id.ref = stack.Context{WorkspaceID: ref.workspace, SessionID: ref.session}
	if id.agent == "" && h.name == "" {
		h.name = credential.AgentName
	}
	if !stack.ValidID(id.ref.WorkspaceID) || !stack.ValidID(id.ref.SessionID) {
		return "", invalid("--workspace and --session required; this credential file does not name them")
	}
	if (id.agent == "") == (h.name == "") {
		return "", invalid("choose --agent ID or --name NAME")
	}
	if h.name != "" {
		client, err := stack.NewClient(id.state, id.credential, stack.AgentCapability)
		if err != nil {
			return "", err
		}
		agents, err := listPages[stack.Agent](ctx, client, "/v1/agent/list", id.ref)
		if err != nil {
			return "", err
		}
		for _, a := range agents {
			if a.Name == h.name {
				id.agent = a.ID
			}
		}
		if id.agent == "" {
			return "", &stack.Error{Code: "not_found", Message: "no Agent with this name in the Session"}
		}
	}
	if !stack.ValidID(id.agent) {
		return "", invalid("invalid Agent ID")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", invalid("cannot find the a-stack binary")
	}
	parts := []string{quotePath(exe), "hook"}
	if id.state != "" {
		parts = append(parts, "--state-dir", quotePath(id.state))
	}
	parts = append(parts, "--credential-file", quotePath(id.credential), "--workspace", id.ref.WorkspaceID, "--session", id.ref.SessionID, "--agent", id.agent)
	if id.runtimeID != "" {
		parts = append(parts, "--runtime-id", shellQuote(id.runtimeID))
	}
	if h.flags.noStopGuard {
		parts = append(parts, "--no-stop-guard")
	}
	if h.flags.noMailNudge {
		parts = append(parts, "--no-mail-nudge")
	}
	parts = append(parts, "--worktree", quotePath(id.worktree))
	return strings.Join(parts, " "), nil
}

// isOurs reports whether a hook command is an a-stack hook for worktree.
func isOurs(command, worktree string) bool {
	return strings.Contains(command, "' hook ") && strings.HasSuffix(command, " --worktree "+quotePath(worktree))
}

// mergeHooks removes the worktree's a-stack hooks from file and, when
// command is set, adds them again. It keeps every other key and hook, and
// writes nothing when nothing changes.
func mergeHooks(file, harness, worktree, command string) (bool, error) {
	if target, err := filepath.EvalSymlinks(file); err == nil {
		file = target // a settings file kept in a dotfiles folder stays a link
	}
	fail := func(message string) error {
		return &stack.Error{Code: "storage_unavailable", Message: "cannot change " + file + ": " + message}
	}
	doc := map[string]json.RawMessage{}
	mode := os.FileMode(0o600)
	old, err := os.ReadFile(file)
	if err == nil {
		if json.Unmarshal(old, &doc) != nil || doc == nil {
			return false, fail("it is not a JSON object; fix or move it first")
		}
		if info, err := os.Stat(file); err == nil {
			mode = info.Mode().Perm()
		}
	} else if !os.IsNotExist(err) {
		return false, fail(err.Error())
	} else if command == "" {
		return false, nil
	}
	hooks := map[string][]json.RawMessage{}
	if raw, ok := doc["hooks"]; ok && json.Unmarshal(raw, &hooks) != nil {
		return false, fail(`"hooks" is not an object of event lists`)
	}
	for event, groups := range hooks {
		kept := []json.RawMessage{}
		for _, group := range groups {
			group, err := withoutOurs(group, worktree)
			if err != nil {
				return false, fail(err.Error())
			}
			if group != nil {
				kept = append(kept, group)
			}
		}
		hooks[event] = kept
		if len(kept) == 0 {
			delete(hooks, event)
		}
	}
	if command != "" {
		for _, e := range hookEvents {
			if e.event == "Notification" && harness != "claude" {
				continue // Codex has no Notification event
			}
			group := map[string]any{"hooks": []map[string]any{{"type": "command", "command": command, "timeout": hookTimeoutSeconds}}}
			if e.matcher != "" {
				group["matcher"] = e.matcher
			}
			raw, _ := json.Marshal(group)
			hooks[e.event] = append(hooks[e.event], raw)
		}
	}
	delete(doc, "hooks")
	if len(hooks) > 0 {
		doc["hooks"], _ = json.Marshal(hooks)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, fail(err.Error())
	}
	data = append(data, '\n')
	if old != nil {
		var a, b bytes.Buffer
		if json.Compact(&a, old) == nil && json.Compact(&b, data) == nil && a.String() == b.String() {
			return false, nil
		}
	}
	if err = writeAtomic(file, data, mode); err != nil {
		return false, fail(err.Error())
	}
	return true, nil
}

// withoutOurs drops the worktree's a-stack hooks from one matcher group, and
// returns nil when no hook is left in it.
func withoutOurs(raw json.RawMessage, worktree string) (json.RawMessage, error) {
	group := map[string]json.RawMessage{}
	var list []json.RawMessage
	if json.Unmarshal(raw, &group) != nil || json.Unmarshal(group["hooks"], &list) != nil {
		return raw, nil // not ours to judge
	}
	kept := []json.RawMessage{}
	for _, h := range list {
		var hook struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(h, &hook) == nil && isOurs(hook.Command, worktree) {
			continue
		}
		kept = append(kept, h)
	}
	if len(kept) == len(list) {
		return raw, nil
	}
	if len(kept) == 0 {
		return nil, nil
	}
	group["hooks"], _ = json.Marshal(kept)
	return json.Marshal(group)
}

func writeAtomic(file string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+"-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return stack.Rename(tmp.Name(), file)
}
