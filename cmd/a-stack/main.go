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
	fs.StringVar(&state, "state-dir", "", "State directory (default ~/.local/state/a-stack)")
	fs.StringVar(&credential, "credential-file", "", "Credential file (default: operator.json)")
	fs.StringVar(&kind, "as", "", "Credential kind: operator, agent or browser")
	fs.StringVar(&listen, "listen", "127.0.0.1:0", "Loopback address; port 0 picks a free port")
	fs.StringVar(&destination, "destination", "", "New absolute file name for the backup")
	fs.BoolVar(&machine, "json", false, "Machine-readable output")
	fs.BoolVar(&help, "help", false, "Show help")
	fs.BoolVar(&help, "h", false, "Short for --help")
	fs.BoolVar(&showVersion, "version", false, "Print the version")
	fs.BoolVar(&noBrowser, "no-browser", false, "Print the URL, but do not start a browser")
	fs.BoolVar(&resume, "resume", false, "Pick a Session to open")
	fs.BoolVar(&resume, "r", false, "Short for --resume")
	fs.StringVar(&herdrExecutable, "herdr-executable", "", "Turn on Herdr mention notices with this herdr binary")
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
		return writeHelp(fs, command, machine, stdout)
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
