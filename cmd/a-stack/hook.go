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
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mzmzeee/a-stack/contrib/opencode"
	"github.com/mzmzeee/a-stack/contrib/pi"
	"github.com/mzmzeee/a-stack/internal/stack"
)

// `a-stack hook` runs as a Claude Code, Codex, Pi or OpenCode hook for one Agent. The hook
// configuration that `hook install` writes names the Agent explicitly:
// credential file, Workspace, Session and Agent ID, never the environment.

func isHookCommand(command string) bool {
	return command == "hook" || command == "hook install" || command == "hook uninstall" || command == "hook enroll"
}

// isHookRun reports whether args run the hook handler, which always exits 0.
func isHookRun(args []string) bool {
	return len(args) > 0 && args[0] == "hook" && (len(args) == 1 || strings.HasPrefix(args[1], "-"))
}

type hookFlags struct {
	claude, codex, pi, opencode, noStopGuard, noMailNudge bool
	harness, nativeSession, nativeAgent                   string
}

func (f *hookFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.harness, "harness", "", "Harness that emitted the hook event")
	fs.StringVar(&f.nativeSession, "native-session", "", "Enroll this native harness session ID")
	fs.StringVar(&f.nativeAgent, "native-agent", "", "Claude Code subagent ID to enroll with its parent session")
	fs.BoolVar(&f.claude, "claude", false, "Only Claude Code (default: Claude Code and Codex)")
	fs.BoolVar(&f.codex, "codex", false, "Only Codex (default: Claude Code and Codex)")
	fs.BoolVar(&f.pi, "pi", false, "Pi, with the a-stack extension; not in the default")
	fs.BoolVar(&f.opencode, "opencode", false, "OpenCode, with the a-stack plugin; not in the default")
	fs.BoolVar(&f.noStopGuard, "no-stop-guard", false, "Let the agent stop with a working Task that has no report")
	fs.BoolVar(&f.noMailNudge, "no-mail-nudge", false, "Do not tell the agent about new Task mail after tool calls")
}

// hookTimeout bounds one hook run; the harness gets hookTimeoutSeconds.
const hookTimeout, hookTimeoutSeconds = 2 * time.Second, 5

// hookEvents maps each event that install registers to the runtime state it
// records; SessionStart and SubagentStart print the reminder instead. Only
// Claude Code gets the events marked claude.
var hookEvents = []struct {
	event, matcher, state string
	claude                bool
}{
	{"SessionStart", "", "", false},
	{"UserPromptSubmit", "", "working", false},
	{"PostToolUse", "", "working", false},
	{"PermissionRequest", "", "blocked", false},
	{"Notification", "elicitation_dialog", "blocked", true},
	{"Stop", "", "idle", false},
	{"SubagentStart", "", "", true},
	{"SubagentStop", "", "idle", true},
}

type hookInput struct {
	enrollmentRevision int64
	Event              string `json:"hook_event_name"`
	SessionID          string `json:"session_id"`
	AgentID            string `json:"agent_id"` // set by Claude Code in a subagent
	Cwd                string `json:"cwd"`
	NotificationType   string `json:"notification_type"`
	StopHookActive     bool   `json:"stop_hook_active"`
	Source             string `json:"source"` // of SessionStart, such as startup or compact
}

type hookIdentity struct {
	state, credential, agent, runtimeID, worktree, harness string
	ref                                                    stack.Context
	noStopGuard, noMailNudge                               bool
}

// runHook handles one hook event. Any failure ends it silently: a hook must
// never stop the Agent, also when a-stack is down.
func runHook(id hookIdentity, stdin io.Reader, stdout io.Writer) {
	var in hookInput
	if json.NewDecoder(io.LimitReader(stdin, 16<<20)).Decode(&in) != nil {
		return
	}
	cwd, err := stack.ResolvePath(in.Cwd)
	if err != nil || !filepath.IsAbs(in.Cwd) {
		return
	}
	id.worktree, err = stack.ResolvePath(id.worktree)
	if err != nil {
		return
	}
	if !ownEvent(id, &in, cwd) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	client, err := stack.NewClient(id.state, id.credential, stack.AgentCapability)
	if err != nil {
		return
	}
	current, err := inspectSession(ctx, client, id.ref)
	if err != nil || current.Closed || current.YourAgentID != id.agent {
		return
	}
	agents, err := listPages[stack.Agent](ctx, client, "/v1/agent/list", id.ref)
	if err != nil {
		return
	}
	valid := false
	for _, a := range agents {
		if a.ID == id.agent && a.Revision == in.enrollmentRevision {
			valid = true
		}
	}
	if !valid {
		return
	}
	if in.Event == "SessionStart" || in.Event == "SubagentStart" {
		text, err := hookReminder(ctx, client, id)
		if err != nil {
			return // a closed Session's hooks say nothing
		}
		hookContext(stdout, in.Event, text)
		if !id.noMailNudge {
			hookMailNudge(ctx, client, id, in) // reads up to the newest mail
		}
		return
	}
	if (in.Event == "Stop" || in.Event == "SubagentStop") && !id.noStopGuard && stopGuard(ctx, client, id, in, stdout) {
		return // the agent goes on, so its state stays working
	}
	for _, e := range hookEvents {
		if e.event == in.Event && e.state != "" && (e.matcher == "" || e.matcher == in.NotificationType) && id.runtimeID != "" {
			_ = hookObserve(ctx, client, id, e.state)
		}
	}
	if (in.Event == "PostToolUse" || in.Event == "UserPromptSubmit") && !id.noMailNudge {
		if text := hookMailNudge(ctx, client, id, in); text != "" {
			hookContext(stdout, in.Event, text)
		}
	}
}

// ownEvent requires explicit native-session enrollment; folder containment
// is only an additional boundary, never evidence of Agent identity.
func ownEvent(id hookIdentity, in *hookInput, cwd string) bool {
	home, err := os.UserHomeDir()
	if err != nil || id.worktree == "" || holdsHome(id.worktree, home) || !within(cwd, id.worktree) {
		return false
	}
	in.enrollmentRevision = enrolledHook(id, *in)
	if in.enrollmentRevision < 1 {
		return false
	}
	if in.AgentID != "" {
		if filepath.Clean(cwd) != filepath.Clean(id.worktree) {
			return false
		}
		in.SessionID += "/" + in.AgentID
	}
	in.SessionID = strconv.FormatInt(in.enrollmentRevision, 10) + "/" + id.harness + "/" + in.SessionID
	return true
}

// hookContext prints text for the agent's context as JSON, which Claude Code
// and Codex both read for SessionStart, UserPromptSubmit and PostToolUse.
func hookContext(stdout io.Writer, event, text string) {
	_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": text}})
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
	session, err := inspectSession(ctx, client, id.ref)
	if err != nil {
		return "", err
	}
	if session.Closed {
		return "", invalid("the Session is closed")
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
	fmt.Fprintf(&b, "a-stack: you are the Agent %s in the a-stack Session %q (Workspace %s, Session %s).", name, printable(session.Name), id.ref.WorkspaceID, id.ref.SessionID)
	b.WriteString(" Load the a-stack skill before running commands. Use a-stack only for this Session's work: a request about something else is no Task, so do it without a-stack.")
	fmt.Fprintf(&b, "\nRun Task and mail commands as yourself, such as: %s task list\n", agentCommand(id))
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
	b.WriteString("Run mail check now; send Task mail at milestones, write a report, then finish.\n")
	b.WriteString(ordersReminder(session.StandingOrders, agentCommand(id)))
	return b.String(), nil
}

// agentCommand is the a-stack command line that runs Task and mail commands
// as the hook's Agent.
func agentCommand(id hookIdentity) string {
	command := "a-stack"
	if id.state != "" {
		command += " --state-dir " + quotePath(id.state)
	}
	return command + " --as agent --credential-file " + quotePath(id.credential) + " --workspace " + id.ref.WorkspaceID + " --session " + id.ref.SessionID
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
// settings.json (settings.local.json in a project, so it is not committed),
// Codex's hooks.json, and with --pi or --opencode the hook list of the Pi
// extension or the OpenCode plugin.
func hookSettings(f hookFlags, project bool) (map[string]string, error) {
	files := map[string]string{}
	both := !f.claude && !f.codex && !f.pi && !f.opencode
	if project {
		if f.pi {
			return nil, invalid("--pi has no --project: Pi loads a project's extensions only in a trusted project, and the hook list would put a credential path in it; install the Pi hooks without --project")
		}
		if f.opencode {
			return nil, invalid("--opencode has no --project: the hook list would put a credential path in the project's .opencode folder; install the OpenCode hooks without --project")
		}
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
	config := filepath.Join(home, ".config") // OpenCode's, also on macOS and Windows
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" && f.opencode {
		if !filepath.IsAbs(v) {
			return nil, invalid("XDG_CONFIG_HOME must be absolute")
		}
		config = v
	}
	for _, h := range []struct {
		harness, variable, dir, name string
		on                           bool
	}{
		{"claude", "CLAUDE_CONFIG_DIR", ".claude", "settings.json", both || f.claude},
		{"codex", "CODEX_HOME", ".codex", "hooks.json", both || f.codex},
		{"pi", "PI_CODING_AGENT_DIR", filepath.Join(".pi", "agent"), "a-stack-hooks.json", f.pi},
		{"opencode", "OPENCODE_CONFIG_DIR", filepath.Join(config, "opencode"), "a-stack-hooks.json", f.opencode},
	} {
		if !h.on {
			continue
		}
		dir := h.dir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(home, dir)
		}
		if v := os.Getenv(h.variable); v != "" {
			if !filepath.IsAbs(v) {
				return nil, invalid(h.variable + " must be absolute")
			}
			dir = v
		}
		files[h.harness] = filepath.Join(dir, h.name)
	}
	return files, nil
}

// shims are the files, built into the binary, that run the hooks in a
// harness without command hooks, next to the hook list they read.
var shims = map[string]struct{ path, content, header, name string }{
	"pi":       {filepath.Join("extensions", "a-stack.ts"), pi.Extension, pi.Header, "Pi extension"},
	"opencode": {filepath.Join("plugins", "a-stack.js"), opencode.Plugin, opencode.Header, "OpenCode plugin"},
}

// shimsSupported is false on Windows, where the shims cannot run their
// hooks: both start each hook command with /bin/sh.
var shimsSupported = runtime.GOOS != "windows"

// writeShim writes a harness's shim next to its hook list. On uninstall,
// once the list has no hooks left, it removes the shim and the empty list.
// It returns the shim's path and whether it changed it. It never touches
// another file of that name.
func writeShim(harness, list string, remove bool) (string, bool, error) {
	shim := shims[harness]
	file := filepath.Join(filepath.Dir(list), shim.path)
	old, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) || err == nil && !strings.HasPrefix(string(old), shim.header) {
		if remove {
			return file, false, nil
		}
		return file, false, &stack.Error{Code: "storage_unavailable", Message: "cannot change " + file + ": it is not the a-stack " + shim.name + "; move it first"}
	}
	if !remove {
		if string(old) == shim.content {
			return file, false, nil
		}
		if err = writeAtomic(file, []byte(shim.content), 0o644); err != nil {
			return file, false, &stack.Error{Code: "storage_unavailable", Message: "cannot change " + file + ": " + err.Error()}
		}
		return file, true, nil
	}
	if data, err := os.ReadFile(list); err == nil {
		doc := map[string]json.RawMessage{}
		if json.Unmarshal(data, &doc) != nil || doc["hooks"] != nil {
			return file, false, nil // another folder's hooks still use it
		}
		if len(doc) == 0 {
			_ = os.Remove(list)
		}
	}
	return file, old != nil && os.Remove(file) == nil, nil
}

type hookInstall struct {
	identity                             hookIdentity
	name                                 string
	flags                                hookFlags
	project, remove, machine, enrollOnly bool
	stdout                               io.Writer
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
	if canonical, err := stack.ResolvePath(id.worktree); err == nil {
		id.worktree = canonical
	}
	if home, err := os.UserHomeDir(); err == nil && !h.remove && holdsHome(id.worktree, home) {
		return invalid("hooks need the Agent's own folder, not your home folder or a folder above it: every agent started below it would act as this Agent; run hook install in the project folder or pass --worktree")
	}
	if !h.remove && !h.enrollOnly && !shimsSupported && (h.flags.pi || h.flags.opencode) {
		flag := "--pi"
		if h.flags.opencode {
			flag = "--opencode"
		}
		return invalid(flag + " needs /bin/sh, so it works only on Linux and macOS")
	}
	if h.enrollOnly {
		if err := h.flags.detectNativeSession(); err != nil {
			return err
		}
	}
	command := ""
	if !h.remove {
		var err error
		if command, err = h.command(ctx); err != nil {
			return err
		}
	}
	if !h.remove {
		if err := h.enroll(ctx); err != nil {
			return err
		}
	}
	if h.enrollOnly {
		if h.machine {
			return json.NewEncoder(h.stdout).Encode(map[string]any{"data": map[string]any{"enrolled": true, "agent_id": id.agent}})
		}
		_, err := io.WriteString(h.stdout, "Enrolled this native session for a-stack hooks.\n")
		return err
	}
	files, err := hookSettings(h.flags, h.project)
	if err != nil {
		return err
	}
	done := []string{}
	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		file, ok := files[harness]
		if !ok {
			continue
		}
		harnessCommand := command
		if command != "" {
			harnessCommand += " --harness " + harness
		}
		changed, err := mergeHooks(file, harness, id.worktree, harnessCommand)
		if err != nil {
			return err
		}
		if changed {
			done = append(done, file)
		}
		if _, ok := shims[harness]; ok {
			if file, changed, err = writeShim(harness, file, h.remove); err != nil {
				return err
			}
			if changed {
				done = append(done, file)
			}
		}
	}
	key, text := "installed", "Installed the a-stack hooks for "+printable(id.worktree)+" in:\n"
	if h.remove {
		key, text = "removed", "Removed the a-stack hooks for "+printable(id.worktree)+" from:\n"
	}
	enrollmentCommand := agentCommand(*id) + " hook enroll"
	if len(files) == 1 {
		for harness := range files {
			enrollmentCommand += " --" + harness
		}
	}
	if h.machine {
		return json.NewEncoder(h.stdout).Encode(map[string]any{"data": map[string]any{"enrollment_command": enrollmentCommand, "worktree": id.worktree, "agent_id": id.agent, key: done, "enrollment_required": !h.remove && h.flags.nativeSession == ""}})
	}
	if len(done) == 0 {
		text = "No a-stack hooks for " + printable(id.worktree) + " were installed.\n"
	}
	for _, file := range done {
		text += "  " + file + "\n"
	}
	if !h.remove {
		if h.flags.nativeSession == "" {
			text += "Hooks stay silent until enrollment. From your own native session, run " + enrollmentCommand + "\nOr run your own herdr bind.\n"
		}
		if id.runtimeID == "" {
			text += "Without --runtime-id the hooks record no runtime state.\n"
		}
		if _, ok := files["codex"]; ok {
			text += "Codex runs new hooks only after you trust them: run /hooks in Codex.\n"
		}
		if _, ok := files["pi"]; ok {
			text += "Pi loads the extension when it starts; in a running Pi, run /reload.\n"
		}
		if _, ok := files["opencode"]; ok {
			text += "OpenCode loads the plugin when it starts; restart a running OpenCode.\n"
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
	// The service is asked only when the flags and the file leave a gap.
	var client *stack.Client
	connect := func() error {
		if client == nil {
			state := id.state
			if state == "" {
				state, err = stack.DefaultStateDir()
				if err != nil {
					return err
				}
			}
			client, err = stack.NewClient(state, id.credential, stack.AgentCapability)
		}
		return err
	}
	// A credential file from v0.1.5 on names its Workspace, Session and Agent;
	// for an older one the service names the Workspace and Session.
	ref := lifecycleFlags{workspace: id.ref.WorkspaceID, session: id.ref.SessionID}
	if (credential.WorkspaceID == "" || credential.SessionID == "") && (ref.workspace == "" || ref.session == "") {
		if err = connect(); err != nil {
			return "", err
		}
		if err = loadContext(ctx, client, ref, "hook install"); err != nil {
			return "", err
		}
		credential = client.Credential
	}
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
	if id.agent == "" && h.name == "" && client != nil {
		// A file from before v0.1.5 does not name its Agent either; the
		// service knows whose credential it is.
		session, err := inspectSession(ctx, client, id.ref)
		if err != nil {
			return "", err
		}
		id.agent = session.YourAgentID
	}
	if (id.agent == "") == (h.name == "") {
		return "", invalid("choose --agent ID or --name NAME")
	}
	if h.name != "" {
		if err = connect(); err != nil {
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
	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		command = strings.TrimSuffix(command, " --harness "+harness)
	}
	return strings.Contains(command, "' hook ") && strings.HasSuffix(command, " --worktree "+quotePath(worktree))
}

// mergeHooks removes the worktree's a-stack hooks from file and, when
// command is set, adds them again. It keeps every other key and hook, and
// writes nothing when nothing changes.
func mergeHooks(file, harness, worktree, command string) (bool, error) {
	if target, err := stack.ResolvePath(file); err == nil {
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
			if e.claude && harness != "claude" {
				continue // Codex, the Pi extension and the OpenCode plugin have no such event
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
