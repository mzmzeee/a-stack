package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// version is set by release builds with -ldflags "-X main.version=vX.Y.Z".
var version string

func main() {
	if runtime.GOOS == "windows" {
		removeOldBinaries()
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	err := execute(args, stdout, stderr)
	if isHookRun(args) || len(args) > 0 && args[0] == "statusline" {
		return 0 // a hook or a status line never stops the harness
	}
	if err == nil {
		return 0
	}
	return writeError(stderr, err)
}

// writeError prints one JSON error document and returns the exit code for it.
func writeError(stderr io.Writer, err error) int {
	e, ok := err.(*stack.Error)
	if !ok {
		e = &stack.Error{Code: "storage_unavailable", Message: "command failed", Retryable: true}
	}
	_ = json.NewEncoder(stderr).Encode(map[string]any{"error": e})
	if e == stack.ErrStateOwned || e == errStateBusy {
		return 3 // systemd: RestartPreventExitStatus
	}
	if e.DatabaseUnusable() {
		return 4 // a damaged database, or a newer or unknown schema
	}
	if e.Code == "invalid_input" {
		return 2
	}
	return 1
}
func invalid(message string) error { return &stack.Error{Code: "invalid_input", Message: message} }

// writeData prints a service response, which already ends with a newline.
func writeData(stdout io.Writer, data []byte) error {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	_, err := stdout.Write(data)
	return err
}
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
func execute(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("a-stack", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var state, credential, kind, listen, destination, herdrExecutable, orchestrator string
	var machine, help, noBrowser, resume, project, refresh, check, all bool
	var release versionFlag
	fs.StringVar(&state, "state-dir", "", "State directory (default "+stateDirDefault+")")
	fs.StringVar(&credential, "credential-file", "", "Credential file (default: operator.json)")
	fs.StringVar(&kind, "as", "", "Credential kind: operator, agent or browser")
	fs.StringVar(&listen, "listen", "", "Loopback address (default: the last port, else a free one)")
	fs.StringVar(&destination, "destination", "", "New absolute file name for the backup")
	fs.BoolVar(&machine, "json", false, "Machine-readable output")
	fs.BoolVar(&help, "help", false, "Show help")
	fs.BoolVar(&help, "h", false, "Short for --help")
	fs.Var(&release, "version", "Release to install, such as v0.1.3; also older ones")
	fs.BoolVar(&noBrowser, "no-browser", false, "Print the URL, but do not start a browser")
	fs.BoolVar(&resume, "resume", false, "Pick a Session to open")
	fs.BoolVar(&resume, "r", false, "Short for --resume")
	fs.BoolVar(&project, "project", false, "Use .claude and .agents in the current folder, not in your home folder")
	fs.BoolVar(&refresh, "refresh", false, "Replace only the skill copies that exist; write no new ones")
	fs.BoolVar(&check, "check", false, "Only print the current and the latest version")
	fs.BoolVar(&all, "all", false, "Every Session")
	fs.StringVar(&herdrExecutable, "herdr-executable", "", "The herdr binary; serve turns on mention notices with it")
	fs.StringVar(&orchestrator, "orchestrator", "", "Also register an Agent with this name and appoint it as the Orchestrator")
	var lifecycle lifecycleFlags
	lifecycle.register(fs)
	var tasks taskFlags
	tasks.register(fs)
	var mail mailFlags
	mail.register(fs)
	var exporting exportFlags
	exporting.register(fs)
	var watching watchFlags
	watching.register(fs)
	var hooks hookFlags
	hooks.register(fs)
	// stdlib flag stops at the first positional; set known flags here so the
	// same explicit flags work before or after the command without a CLI
	// dependency.
	var command []string
	given := map[string]bool{}
	repeated := false // --after more than once, which only task create takes
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			command = append(command, a)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			return unknownFlag(fs, name)
		}
		// --version takes a release only when one follows, as in
		// `update --version v0.1.3`; alone it prints the version.
		if name == "version" && !hasValue && i+1 < len(args) && releaseTag.MatchString(args[i+1]) {
			i++
			value, hasValue = args[i], true
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() && !hasValue {
			value = "true"
		} else if !hasValue {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") && fs.Lookup(strings.TrimLeft(strings.SplitN(args[i], "=", 2)[0], "-")) != nil {
				return invalid("--" + name + " requires a value")
			}
			value = args[i]
		}
		if given[name] && name != "mention" && name != "after" && name != "assign" {
			return invalid("--" + name + " is given more than once")
		}
		repeated = repeated || given[name] && name == "after"
		given[name] = true
		if fs.Set(name, value) != nil {
			message := "invalid value " + strconv.Quote(short(value)) + " for --" + name
			if flagValues[name] == "N" {
				message += "; give a whole number"
			}
			return invalid(message)
		}
	}
	command, help = aliasCommands(command, help, release.set)
	if help {
		return writeHelp(fs, command, machine, stdout)
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	cmd := strings.Join(command, " ")
	if cmd == "version" {
		delete(seen, "version") // a-stack --version
	}
	var sessionName string
	if len(command) > 0 && command[0] == "new" {
		if len(command) > 2 {
			return invalid("new takes at most one Session name; quote a name with spaces")
		}
		if len(command) == 2 {
			sessionName = command[1]
		}
		cmd = "new"
	}
	if cmd != "" && cmd != "new" && !knownCommand(cmd) {
		return unknownCommand(command, seen, stdout)
	}
	if cmd != "" && cmd != "new" && cmd != "serve" && cmd != "health" && cmd != "storage status" && cmd != "storage backup" && cmd != "open" && cmd != "view revoke" && !isLifecycleCommand(cmd) && !isTaskCommand(cmd) && !isMailCommand(cmd) && !isNotificationCommand(cmd) && !isHerdrCommand(cmd) && !isSkillCommand(cmd) && cmd != "update" && !isServiceCommand(cmd) && cmd != "version" && !isWatchCommand(cmd) && !isHookCommand(cmd) && cmd != "statusline" && cmd != "mcp" {
		return invalid("unknown command; use --help")
	}
	if kind != "" && kind != "operator" && kind != "agent" && kind != "browser" {
		return invalid("--as takes operator, agent or browser")
	}
	if err := validateCommandFlags(cmd, seen); err != nil {
		return err
	}
	if cmd == "session export" {
		if exporting.markdown && machine {
			return invalid("choose --markdown or --json")
		}
		if seen["output"] && exporting.output == "" {
			return invalid("--output must name a file")
		}
	}
	if repeated && cmd != "task create" {
		return invalid("--after is given more than once")
	}
	if !herdrSupported && (isHerdrCommand(cmd) || seen["herdr-executable"]) {
		return invalid("Herdr is not supported on Windows; a-stack works without it, and Task mail reaches Agents through mail check")
	}
	if err := checkIDs(cmd, seen, lifecycle, tasks); err != nil {
		return err
	}
	if isTaskCommand(cmd) {
		if err := tasks.readText(cmd, seen); err != nil {
			return err
		}
	}
	if cmd == "version" {
		return json.NewEncoder(stdout).Encode(map[string]any{"data": map[string]string{"version": buildVersion()}})
	}
	if isSkillCommand(cmd) {
		return skillCommand(cmd, project, refresh, machine, stdout)
	}
	if cmd == "update" {
		return updateCommand(release, check, machine, stdout, stderr)
	}
	stateFlag := state
	if state == "" {
		var err error
		state, err = stack.DefaultStateDir()
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(state) {
		return invalid("--state-dir must be absolute")
	}
	if isServiceCommand(cmd) {
		return serviceCommand(cmd, state, machine, stdout)
	}
	ctx := context.Background()
	if cmd == "statusline" {
		statusLine(state, os.Stdin, stdout)
		return nil
	}
	if isHookCommand(cmd) {
		id := hookIdentity{state: state, credential: credential, agent: lifecycle.agent, runtimeID: lifecycle.runtimeID, worktree: lifecycle.worktree, ref: stack.Context{WorkspaceID: lifecycle.workspace, SessionID: lifecycle.session}, noStopGuard: hooks.noStopGuard, noMailNudge: hooks.noMailNudge}
		if cmd == "hook" {
			runHook(id, os.Stdin, stdout)
			return nil
		}
		id.state = stateFlag // the command names the state only when you did
		h := hookInstall{identity: id, name: lifecycle.name, flags: hooks, project: project, remove: cmd == "hook uninstall", machine: machine, stdout: stdout}
		return h.run(ctx)
	}
	if cmd == "" || cmd == "new" {
		h := home{state: state, mode: cmd, name: sessionName, orchestrator: orchestrator, resume: resume, machine: machine, noBrowser: noBrowser, stdout: stdout, stderr: stderr}
		return h.run(ctx)
	}
	if cmd == "serve" {
		if credential != "" || kind != "" {
			return invalid("serve initializes its state Operator credential; credential selection applies to client commands")
		}
		log := detachConsole(state)
		if log != nil {
			defer log.Close()
			stdout = log
		}
		err := serveForeground(ctx, state, listen, herdrExecutable, func(_ context.Context, service *stack.Service) error {
			return json.NewEncoder(stdout).Encode(readyLine(service))
		})
		if err != nil && log != nil {
			writeError(log, err)
		}
		return err
	}
	if cmd == "mcp" {
		return mcpCommand(ctx, state, credential, kind, lifecycle, os.Stdin, stdout)
	}
	if isWatchCommand(cmd) {
		connect := func() (*stack.Client, error) { return stack.NewClient(state, credential, stack.CapabilityKind(kind)) }
		watching.all = all
		return watchCommand(ctx, connect, cmd, lifecycle.session, watching, machine, stdout, stderr)
	}
	client, err := stack.NewClient(state, credential, stack.CapabilityKind(kind))
	if err != nil {
		return err
	}
	if err = loadContext(ctx, client, lifecycle, cmd); err != nil {
		return err
	}
	if err = fillContext(client.Credential, &lifecycle, cmd); err != nil {
		return err
	}
	ref := stack.Context{WorkspaceID: lifecycle.workspace, SessionID: lifecycle.session}
	if seen["agent"] {
		if err = agentIDs(ctx, client, ref, "agent", &lifecycle.agent); err != nil {
			return err
		}
	}
	assignees := make([]*string, len(tasks.assign))
	for i := range tasks.assign {
		assignees[i] = &tasks.assign[i]
	}
	if err = agentIDs(ctx, client, ref, "assign", assignees...); err != nil {
		return err
	}
	if cmd == "open" {
		return openBrowser(ctx, client, stack.Context{WorkspaceID: lifecycle.workspace, SessionID: lifecycle.session}, machine, noBrowser, stdout, stderr)
	}
	if cmd == "view revoke" {
		data, err := client.RevokeViews(ctx, stack.ViewRevokeRequest{Context: stack.Context{WorkspaceID: lifecycle.workspace, SessionID: lifecycle.session}, All: all})
		if err != nil {
			return err
		}
		return writeData(stdout, data)
	}
	if isHerdrCommand(cmd) {
		data, err := herdrRequest(ctx, client, cmd, lifecycle, herdrExecutable)
		if err != nil {
			return err
		}
		return writeData(stdout, data)
	}
	if isNotificationCommand(cmd) {
		data, err := notificationRequest(ctx, client, cmd, lifecycle)
		if err != nil {
			return err
		}
		return writeData(stdout, data)
	}
	if isMailCommand(cmd) {
		data, err := mailRequest(ctx, client, cmd, lifecycle, tasks, mail, seen)
		if err != nil {
			return err
		}
		return writeData(stdout, data)
	}
	if cmd == "task handoff" {
		return taskHandoff(ctx, client, lifecycle, tasks, seen["limit"], machine, stdout)
	}
	if isTaskCommand(cmd) {
		data, err := taskRequest(ctx, client, cmd, lifecycle, tasks, seen)
		if err != nil {
			return err
		}
		if cmd == "task create" && len(lifecycle.afters) == 0 {
			unlinkedHint(ctx, client, ref, data, stderr)
		}
		return writeData(stdout, data)
	}
	if isOrdersCommand(cmd) {
		data, err := ordersRequest(ctx, client, cmd, lifecycle, tasks, seen)
		if err != nil {
			return err
		}
		return writeData(stdout, data)
	}
	if cmd == "session export" {
		return exportSession(ctx, client, lifecycle, exporting, machine, stdout)
	}
	if isLifecycleCommand(cmd) {
		data, err := lifecycleRequest(ctx, client, cmd, lifecycle)
		if err != nil {
			return err
		}
		return writeData(stdout, data)
	}
	method, path := "GET", "/v1/health"
	var body any
	if cmd == "storage status" {
		path = "/v1/storage"
	}
	if cmd == "storage backup" {
		if destination == "" {
			return invalid("--destination required")
		}
		method = "POST"
		path = "/v1/storage/backup"
		body = map[string]string{"destination": destination}
	}
	data, err := client.Do(ctx, method, path, body)
	if err != nil {
		return err
	}
	// Foundation commands use JSON in both modes; --json guarantees this remains
	// machine-readable when future domain commands gain human-oriented output.
	return writeData(stdout, data)
}

// readyLine prints no credentials. Discovery is private on disk; this ready
// outcome is one JSON document and all diagnostics go to stderr.
func readyLine(service *stack.Service) map[string]any {
	return map[string]any{"data": map[string]any{"status": "ready", "url": service.Discovery.URL, "database_id": service.Discovery.DatabaseID, "version": service.Version}}
}

// serveForeground runs the service until SIGINT, SIGTERM or SIGHUP. ready
// runs once the service accepts requests; an error from it stops the service.
func serveForeground(ctx context.Context, state, listen, herdrExecutable string, ready func(context.Context, *stack.Service) error) error {
	// SIGHUP too: a service in a closed terminal removes its discovery file.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	stack.TuneServiceGC()
	service, err := stack.Start(ctx, state, listen)
	if err != nil {
		return err
	}
	service.Version = buildVersion()
	for _, line := range []string{service.Store.Migrated, service.Notice} {
		if line != "" {
			_, _ = io.WriteString(os.Stderr, line+"\n")
		}
	}
	if herdrExecutable != "" {
		if err = service.EnableNotifications(herdrExecutable); err != nil {
			_ = service.Close(ctx)
			return err
		}
	}
	result := make(chan error, 1)
	go func() { result <- service.Serve() }()
	err = ready(ctx, service)
	if err == nil {
		select {
		case <-ctx.Done():
		case err = <-result:
		}
	}
	// Stop catching signals, so a second Ctrl-C during the wait below ends
	// the process at once.
	stop()
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	closeErr := service.Close(closeCtx)
	if err != nil {
		return err
	}
	return closeErr
}

// aliasCommands makes `help [COMMAND]` and -h work like --help, and
// --version without a command work like `version`.
func aliasCommands(command []string, help, showVersion bool) ([]string, bool) {
	if len(command) > 0 && command[0] == "help" {
		return command[1:], true
	}
	if showVersion && len(command) == 0 {
		return []string{"version"}, help
	}
	return command, help
}

// versionFlag is --version. Alone it prints the version, like the version
// command; with update it names the release to install.
type versionFlag struct {
	set   bool
	value string
}

func (v *versionFlag) String() string   { return v.value }
func (v *versionFlag) IsBoolFlag() bool { return true }
func (v *versionFlag) Set(s string) error {
	v.set = s != "false"
	if s != "true" && s != "false" {
		v.value = s
	}
	return nil
}
