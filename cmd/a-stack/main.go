package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// version is set by release builds with -ldflags "-X main.version=vX.Y.Z".
var version string

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	err := execute(args, stdout, stderr)
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
	if e.Code == "invalid_input" {
		return 2
	}
	return 1
}
func invalid(message string) error { return &stack.Error{Code: "invalid_input", Message: message} }
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
	var state, credential, kind, listen, destination, herdrExecutable string
	var machine, help, noBrowser, resume, showVersion bool
	fs.StringVar(&state, "state-dir", "", "private absolute application-state directory")
	fs.StringVar(&credential, "credential-file", "", "private credential file")
	fs.StringVar(&kind, "as", "", "operator, agent or browser")
	fs.StringVar(&listen, "listen", "127.0.0.1:0", "serve loopback address")
	fs.StringVar(&destination, "destination", "", "new absolute backup filename")
	fs.BoolVar(&machine, "json", false, "machine-readable JSON")
	fs.BoolVar(&help, "help", false, "show usage")
	fs.BoolVar(&help, "h", false, "short for --help")
	fs.BoolVar(&showVersion, "version", false, "same as the version command")
	fs.BoolVar(&noBrowser, "no-browser", false, "print browser URL without launching")
	fs.BoolVar(&resume, "resume", false, "bare command: pick a Session to open")
	fs.BoolVar(&resume, "r", false, "short for --resume")
	fs.StringVar(&herdrExecutable, "herdr-executable", "", "serve: opt in to verified Herdr notices using this executable")
	var lifecycle lifecycleFlags
	lifecycle.register(fs)
	var tasks taskFlags
	tasks.register(fs)
	var mail mailFlags
	mail.register(fs)
	// stdlib flag stops at the first positional; extract known flags so the same
	// explicit flags work before or after the command without a CLI dependency.
	var flags, command []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
			if fs.Lookup(name) == nil {
				return invalid("unknown flag; use --help")
			}
			flags = append(flags, a)
			boolFlag, isBool := fs.Lookup(name).Value.(interface{ IsBoolFlag() bool })
			if !(isBool && boolFlag.IsBoolFlag()) && !strings.Contains(a, "=") {
				i++
				if i >= len(args) {
					return invalid("flag requires a value")
				}
				flags = append(flags, args[i])
			}
		} else {
			command = append(command, a)
		}
	}
	if fs.Parse(flags) != nil {
		return invalid("invalid flags; use --help")
	}
	command, help = aliasCommands(command, help, showVersion)
	if help {
		if machine {
			flags := []string{}
			fs.VisitAll(func(f *flag.Flag) {
				if len(f.Name) == 1 {
					flags = append(flags, "-"+f.Name)
				} else {
					flags = append(flags, "--"+f.Name)
				}
			})
			return json.NewEncoder(stdout).Encode(map[string]any{"data": map[string]any{"commands": append([]string{"serve", "open", "new", "version", "health", "storage status", "storage backup", "notifications status", "notifications drain"}, append(append(lifecycleCommands, taskCommands...), mailCommands...)...), "flags": flags}})
		}
		_, err := io.WriteString(stdout, "Usage: a-stack serve|open|health|version|storage status|storage backup [flags]\nOperator: a-stack (start the service if needed and open the last Session); a-stack -r|--resume (pick a Session); a-stack new [NAME] (Workspace for this folder, new Session, open it)\nNotifications: notifications status/drain --workspace ID --session ID; serve --herdr-executable FILE (opt in)\nLifecycle: workspace create/list; session create/list/inspect/close/reopen/appoint; agent register/list/attach/rebind/observe\nTasks: task create/list/show/edit/assign/unassign/status/reopen/report/history/assignees/references/dependencies; task attention set/resolve; task reference add/remove; dependency add/remove/history; session snapshot/page\nMail: mail send/read/check; --body TEXT | --body-file FILE|-; --mention NAME (repeat); --cursor TOKEN; --cursor-file FILE|-; --after-task TOKEN; --task-limit 1..100; --limit 1..500 (mail)\n  --state-dir ABSOLUTE_DIR --credential-file FILE --as operator|agent|browser --json\n  --workspace ID --session ID --operation-id ID --expected-revision N\n  open --workspace ID --session ID [--no-browser] [--json]\n  --path DIR --name NAME --agent ID --worktree DIR --new-credential-file FILE\n  --runtime-id ID | --runtime-file FILE | --clear-runtime --runtime-state STATE\n  --task ID --title TEXT --description TEXT --report TEXT --body-file FILE|- --ticket REF\n  --progress waiting|working|finished|cancelled --reason TEXT --summary TEXT --question TEXT --note TEXT\n  --from PREREQUISITE --to DEPENDENT --location PATH_OR_URL --reference ID --reference-revision REV\n  --read-revision N --known-revision N --page-kind KIND --limit 1..100 --after CURSOR --listen 127.0.0.1:PORT (serve) --destination FILE (backup)\n")
		return err
	}
	cmd := strings.Join(command, " ")
	if cmd == "version" {
		return json.NewEncoder(stdout).Encode(map[string]any{"data": map[string]string{"version": buildVersion()}})
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
	if cmd != "" && cmd != "new" && cmd != "serve" && cmd != "health" && cmd != "storage status" && cmd != "storage backup" && cmd != "open" && !isLifecycleCommand(cmd) && !isTaskCommand(cmd) && !isMailCommand(cmd) && !isNotificationCommand(cmd) {
		return invalid("unknown command; use --help")
	}
	if kind != "" && kind != "operator" && kind != "agent" && kind != "browser" {
		return invalid("invalid capability kind")
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	if err := validateCommandFlags(cmd, seen); err != nil {
		return err
	}
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
	ctx := context.Background()
	if cmd == "" || cmd == "new" {
		h := home{state: state, mode: cmd, name: sessionName, resume: resume, machine: machine, noBrowser: noBrowser, stdout: stdout, stderr: stderr}
		return h.run(ctx)
	}
	if cmd == "serve" {
		if credential != "" || kind != "" {
			return invalid("serve initializes its state Operator credential; credential selection applies to client commands")
		}
		return serveForeground(ctx, state, listen, herdrExecutable, func(_ context.Context, service *stack.Service) error {
			return json.NewEncoder(stdout).Encode(readyLine(service))
		})
	}
	client, err := stack.NewClient(state, credential, stack.CapabilityKind(kind))
	if err != nil {
		return err
	}
	if cmd == "open" {
		return openBrowser(ctx, client, stack.Context{WorkspaceID: lifecycle.workspace, SessionID: lifecycle.session}, machine, noBrowser, stdout, stderr)
	}
	if isNotificationCommand(cmd) {
		data, err := notificationRequest(ctx, client, cmd, lifecycle)
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(data, '\n'))
		return err
	}
	if isMailCommand(cmd) {
		data, err := mailRequest(ctx, client, cmd, lifecycle, tasks, mail, seen)
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(data, '\n'))
		return err
	}
	if isTaskCommand(cmd) {
		data, err := taskRequest(ctx, client, cmd, lifecycle, tasks, seen)
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(data, '\n'))
		return err
	}
	if isLifecycleCommand(cmd) {
		data, err := lifecycleRequest(ctx, client, cmd, lifecycle)
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(data, '\n'))
		return err
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
	_, err = stdout.Write(append(data, '\n'))
	return err
}

// readyLine prints no credentials. Discovery is private on disk; this ready
// outcome is one JSON document and all diagnostics go to stderr.
func readyLine(service *stack.Service) map[string]any {
	return map[string]any{"data": map[string]any{"status": "ready", "url": service.Discovery.URL, "database_id": service.Discovery.DatabaseID}}
}

// serveForeground runs the service until SIGINT or SIGTERM. ready runs once the
// service accepts requests; an error from it stops the service.
func serveForeground(ctx context.Context, state, listen, herdrExecutable string, ready func(context.Context, *stack.Service) error) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	service, err := stack.Start(ctx, state, listen)
	if err != nil {
		return err
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
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	closeErr := service.Close(closeCtx)
	if err != nil {
		return err
	}
	return closeErr
}

// aliasCommands makes `help` and -h work like --help, and --version without a
// command work like `version`.
func aliasCommands(command []string, help, showVersion bool) ([]string, bool) {
	if len(command) == 1 && command[0] == "help" {
		return nil, true
	}
	if showVersion && len(command) == 0 {
		return []string{"version"}, help
	}
	return command, help
}
