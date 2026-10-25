package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// home runs the Operator shortcuts: bare `a-stack`, `a-stack -r` and
// `a-stack new [NAME]`. They use the state's operator.json. When no service
// answers for the state directory, they run it in the foreground like serve and
// act once it is ready; the service then keeps running until SIGINT, SIGTERM
// or SIGHUP.
type home struct {
	state, mode, name          string
	orchestrator               string
	resume, machine, noBrowser bool
	stdout, stderr             io.Writer
	cwd, homeDir               string
	stdin, terminal            *os.File
	interactive                bool
	pick                       func(context.Context, *picker) (sessionRow, error)
}

func (h *home) run(ctx context.Context) error {
	h.setup()
	// answer is an error that a running service sent back, if any.
	var answer error
	client, err := stack.NewClient(h.state, "", stack.OperatorCapability)
	if err == nil {
		if _, err = client.Do(ctx, "GET", "/v1/health", nil); err == nil {
			return h.act(ctx, client)
		}
		if e, ok := err.(*stack.Error); ok && e.Code != "storage_unavailable" {
			answer = err
		}
	}
	// A coding agent runs the hand-off as often as a person does. A service in
	// the foreground would hang the agent's shell and stop with it.
	if h.orchestrator != "" {
		if answer != nil {
			return answer
		}
		return errNoServiceForHandOff
	}
	err = serveForeground(ctx, h.state, "", "", func(ctx context.Context, service *stack.Service) error {
		var err error
		if h.machine {
			err = json.NewEncoder(h.stdout).Encode(readyLine(service))
		} else {
			_, err = fmt.Fprintf(h.stdout, "The a-stack service is running at %s. Press Ctrl-C to stop it.\n%s\n", service.Discovery.URL, closeHint)
		}
		if err != nil {
			return err
		}
		client, err := stack.NewClient(h.state, "", stack.OperatorCapability)
		if err == nil {
			err = h.act(ctx, client)
		}
		// The service keeps running after a hint or a failed step.
		if err != nil && ctx.Err() == nil {
			writeError(h.stderr, err)
		}
		return nil
	})
	if err == stack.ErrStateOwned {
		// Another serve holds the lock. If it answered with an error, that
		// error says more; otherwise it is starting or hung.
		if answer != nil {
			return answer
		}
		return errStateBusy
	}
	return err
}

// closeHint follows the ready line when a shortcut starts the service in
// the foreground.
const closeHint = "Closing this terminal stops a-stack. To keep it running, run: a-stack service install"

var errNoServiceForHandOff = &stack.Error{Code: "storage_unavailable", Retryable: true, Message: "the a-stack service is not running, and an Orchestrator needs one that keeps running; run a-stack service install (or a-stack serve in another terminal), then run the same command again"}

// errStateBusy exits with 3, like stack.ErrStateOwned.
var errStateBusy = &stack.Error{Code: "conflict", Message: "another a-stack serve owns this state directory but does not answer yet; wait a moment or stop it (Ctrl-C in its terminal, or " + stack.ServiceCommand("stop") + ")"}

func (h *home) setup() {
	if wd, err := os.Getwd(); err == nil {
		if canonical, err := filepath.EvalSymlinks(wd); err == nil {
			wd = canonical
		}
		h.cwd = wd
	}
	h.homeDir, _ = os.UserHomeDir()
	if h.pick == nil {
		h.stdin, h.terminal = os.Stdin, os.Stdout
		h.interactive = isTerminal(os.Stdin) && isTerminal(os.Stdout)
		h.pick = func(ctx context.Context, p *picker) (sessionRow, error) {
			return runPicker(ctx, h.stdin, h.terminal, p, h.homeDir)
		}
	}
}

func (h *home) act(ctx context.Context, client *stack.Client) error {
	switch {
	case h.mode == "new":
		return h.create(ctx, client)
	case h.resume:
		return h.resumeSession(ctx, client)
	}
	return h.last(ctx, client)
}

// last opens the open Session that changed most recently, preferring the
// Workspace of the current folder.
func (h *home) last(ctx context.Context, client *stack.Client) error {
	rows, err := h.sessions(ctx, client)
	if err != nil {
		return err
	}
	open := []sessionRow{}
	for _, r := range rows {
		if !r.Closed {
			open = append(open, r)
		}
	}
	if len(rows) == 0 {
		return h.hint(noSessionHint)
	}
	if len(open) == 0 {
		return h.hint("No open Session. To open a closed one, run: a-stack -r. To create a new one, run: a-stack new")
	}
	chosen := open[0]
	for _, r := range open {
		if r.CurrentFolder {
			chosen = r
			break
		}
	}
	return h.open(ctx, client, chosen, len(open)-1)
}

func (h *home) resumeSession(ctx context.Context, client *stack.Client) error {
	rows, err := h.sessions(ctx, client)
	if err != nil {
		return err
	}
	if h.machine || !h.interactive {
		return json.NewEncoder(h.stdout).Encode(map[string]any{"data": map[string]any{"folder": h.cwd, "sessions": rows}})
	}
	if len(rows) == 0 {
		return h.hint(noSessionHint)
	}
	chosen, err := h.pick(ctx, newPicker(rows))
	if err == errPickerCancelled {
		return nil
	}
	if err != nil {
		return err
	}
	return h.open(ctx, client, chosen, -1)
}

// create reuses the Workspace of exactly this folder or makes it, then makes a
// Session, unless an open Session of that name exists there. The CLI is the caller that generates the operation IDs, and it
// prints them so that a change with an unknown outcome can be sent again.
func (h *home) create(ctx context.Context, client *stack.Client) error {
	if h.cwd == "" {
		return invalid("cannot read the current folder")
	}
	// Check --orchestrator before anything is made.
	if h.orchestrator != "" {
		if !orchestratorName.MatchString(h.orchestrator) {
			return invalid("--orchestrator takes an Agent name: up to 64 letters, digits, _ or -, starting with a letter or digit")
		}
	}
	workspaces, err := listPages[stack.Workspace](ctx, client, "/v1/workspace/list", stack.Context{})
	if err != nil {
		return err
	}
	row := sessionRow{WorkspacePath: h.cwd, CurrentFolder: true}
	for _, w := range workspaces {
		if w.Path == h.cwd {
			row.WorkspaceID = w.ID
		}
	}
	row.Name = h.name
	if row.Name == "" {
		row.Name = filepath.Base(h.cwd)
	}
	fields := map[string]any{}
	before := []string{}
	if row.WorkspaceID != "" {
		// A second `a-stack new` opens the open Session of the same name
		// instead of making a twin. Another name makes a new Session.
		sessions, err := listPages[stack.Session](ctx, client, "/v1/session/list", stack.Context{WorkspaceID: row.WorkspaceID})
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if !s.Closed && s.Name == row.Name {
				row.SessionID = s.ID
				fields["existing_session"] = true
				before = append(before, fmt.Sprintf("The Session %q already exists in this folder. To make another one, run: a-stack new NAME", printable(row.Name)))
				return h.handOff(ctx, client, row, fields, before)
			}
		}
	}
	// Refuse before anything is made.
	if h.orchestrator != "" {
		if _, err := os.Lstat(h.orchestratorFile()); err == nil {
			return errOrchestratorFile(h.orchestratorFile())
		}
	}
	if row.WorkspaceID == "" {
		id := operationID()
		receipt, err := mutate(ctx, client, "/v1/workspace/create", stack.WorkspaceCreateRequest{OperationID: id, Path: h.cwd}, "workspace create from this folder", id)
		if err != nil {
			return err
		}
		row.WorkspaceID = receipt.ResourceID
		fields["workspace_operation_id"] = id
		before = append(before, fmt.Sprintf("Created the Workspace %s (operation ID %s).", printable(shortenHome(h.cwd, h.homeDir)), id))
	}
	id := operationID()
	receipt, err := mutate(ctx, client, "/v1/session/create", stack.SessionCreateRequest{OperationID: id, WorkspaceID: row.WorkspaceID, Name: row.Name}, "session create --workspace "+row.WorkspaceID+" with the same name", id)
	if err != nil {
		return err
	}
	row.SessionID = receipt.ResourceID
	fields["session_operation_id"] = id
	before = append(before, fmt.Sprintf("Created the Session %q (operation ID %s).", printable(row.Name), id))
	return h.handOff(ctx, client, row, fields, before)
}

// handOff opens the Session. With --orchestrator it first registers that Agent
// with a credential file in a-stack-agents next to the state directory, appoints
// it and prints a brief to paste into the coding agent. The brief names only
// paths and IDs, never the credential.
func (h *home) handOff(ctx context.Context, client *stack.Client, row sessionRow, fields map[string]any, before []string) error {
	if h.orchestrator == "" {
		return h.openWith(ctx, client, row, fields, before, nil)
	}
	ref := stack.Context{WorkspaceID: row.WorkspaceID, SessionID: row.SessionID}
	session, err := inspectSession(ctx, client, ref)
	if err != nil {
		return err
	}
	credential := h.orchestratorFile()
	if session.OrchestratorID != nil {
		// The same hand-off again, for example from an agent that lost it:
		// the Orchestrator's own credential file proves it, and nothing is made.
		if h.ownsOrchestrator(ctx, credential, ref, *session.OrchestratorID) {
			fields["existing_orchestrator"] = true
			before = append(before, fmt.Sprintf("%s is already the Orchestrator of this Session.", printable(h.orchestrator)))
			return h.briefOrchestrator(ctx, client, row, *session.OrchestratorID, credential, fields, before)
		}
		return invalid("this Session already has an Orchestrator; to replace it, see a-stack help session appoint")
	}
	if _, err := os.Lstat(credential); err == nil {
		return errOrchestratorFile(credential)
	}
	if err := os.MkdirAll(filepath.Dir(credential), 0o700); err != nil {
		return invalid("cannot make the folder " + filepath.Dir(credential))
	}
	registerID := operationID()
	data, err := lifecycleRequest(ctx, client, "agent register", lifecycleFlags{workspace: row.WorkspaceID, session: row.SessionID, operationID: registerID, name: h.orchestrator, newCredential: credential})
	if err != nil {
		return err
	}
	var receipt struct {
		Data stack.Receipt `json:"data"`
	}
	if json.Unmarshal(data, &receipt) != nil {
		return invalid("unexpected agent register response")
	}
	appointID := operationID()
	if _, err := lifecycleRequest(ctx, client, "session appoint", lifecycleFlags{workspace: row.WorkspaceID, session: row.SessionID, operationID: appointID, revision: session.Revision, agent: receipt.Data.ResourceID}); err != nil {
		return err
	}
	fields["register_operation_id"], fields["appoint_operation_id"] = registerID, appointID
	before = append(before, fmt.Sprintf("Registered %s and appointed it as the Orchestrator (operation IDs %s, %s).", printable(h.orchestrator), registerID, appointID))
	return h.briefOrchestrator(ctx, client, row, receipt.Data.ResourceID, credential, fields, before)
}

func (h *home) briefOrchestrator(ctx context.Context, client *stack.Client, row sessionRow, id, credential string, fields map[string]any, before []string) error {
	brief := strings.Join([]string{
		"You are the Orchestrator of an a-stack Session. Load the a-stack skill and read its orchestrator reference.",
		"Credential file: " + credential,
		"Workspace: " + row.WorkspaceID + "   Session: " + row.SessionID,
		"Your name: " + h.orchestrator + "   Your private folder: make one with mktemp -d",
		"Work in: " + h.cwd,
		"Goal: <describe the goal>. Plan the Tasks, link each one to the Tasks it depends on, register one Agent per Task and brief each one.",
	}, "\n")
	fields["orchestrator_id"], fields["orchestrator_credential_file"], fields["brief"] = id, credential, brief
	after := []string{"Start your coding agent in this folder and give it this brief, with your goal:", "", brief, ""}
	if !skillInstalled(h.homeDir) {
		after = append(after, "Install the agent skill first, if your agent does not have it: a-stack skill install")
	}
	return h.openWith(ctx, client, row, fields, before, after)
}

// ownsOrchestrator reports whether the credential file at path belongs to the
// Orchestrator id of the Session ref.
func (h *home) ownsOrchestrator(ctx context.Context, path string, ref stack.Context, id string) bool {
	c, err := stack.ReadCredential(path)
	if err != nil || c.Kind != stack.AgentCapability || c.WorkspaceID != ref.WorkspaceID || c.SessionID != ref.SessionID || c.AgentName != h.orchestrator {
		return false
	}
	own, err := stack.NewClient(h.state, path, stack.AgentCapability)
	if err != nil {
		return false
	}
	session, err := inspectSession(ctx, own, ref)
	return err == nil && session.OrchestratorID != nil && *session.OrchestratorID == id
}

func errOrchestratorFile(path string) error {
	return invalid(path + " already exists; choose another --orchestrator name")
}

// orchestratorName is the service's Agent name rule.
var orchestratorName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// orchestratorFile is the new Orchestrator's credential file, in a private
// a-stack-agents folder next to the state directory.
func (h *home) orchestratorFile() string {
	return filepath.Join(filepath.Dir(h.state), "a-stack-agents", h.orchestrator+".json")
}

func inspectSession(ctx context.Context, client *stack.Client, ref stack.Context) (stack.Session, error) {
	data, err := client.Do(ctx, "POST", "/v1/session/inspect", ref)
	if err != nil {
		return stack.Session{}, err
	}
	var answer struct {
		Data stack.Session `json:"data"`
	}
	if json.Unmarshal(data, &answer) != nil {
		return stack.Session{}, invalid("unexpected session inspect response")
	}
	return answer.Data, nil
}

func skillInstalled(homeDir string) bool {
	for _, dir := range []string{".claude", ".agents"} {
		if _, err := os.Stat(filepath.Join(homeDir, dir, "skills", "a-stack", "SKILL.md")); err == nil {
			return true
		}
	}
	return false
}

const noSessionHint = "No Session yet. To create a Workspace for this folder and a Session, run: a-stack new"

func (h *home) hint(text string) error {
	if h.machine {
		return json.NewEncoder(h.stdout).Encode(map[string]any{"data": map[string]string{"hint": text}})
	}
	_, err := io.WriteString(h.stdout, text+"\n")
	return err
}

// open shows which Session it opens, by name and folder. others is the number
// of other open Sessions, or -1 when the Operator picked the Session.
func (h *home) open(ctx context.Context, client *stack.Client, r sessionRow, others int) error {
	fields := map[string]any{}
	var after []string
	if others >= 0 {
		fields["other_open_sessions"] = others
		if others > 0 {
			after = append(after, fmt.Sprintf("Other open Sessions: %d. To pick one, run: a-stack -r", others))
		}
	}
	return h.openWith(ctx, client, r, fields, nil, after)
}

func (h *home) openWith(ctx context.Context, client *stack.Client, r sessionRow, fields map[string]any, before, after []string) error {
	fields["workspace_id"], fields["workspace_path"] = r.WorkspaceID, r.WorkspacePath
	fields["session_id"], fields["session_name"] = r.SessionID, r.Name
	fields["current_folder"] = r.CurrentFolder
	where := printable(shortenHome(r.WorkspacePath, h.homeDir))
	if r.CurrentFolder {
		where += " (current folder)"
	}
	state := ""
	if r.Closed {
		state = " closed"
	}
	before = append(before, fmt.Sprintf("Opening the%s Session %q in %s.", state, printable(r.Name), where))
	return openView(ctx, client, stack.Context{WorkspaceID: r.WorkspaceID, SessionID: r.SessionID}, viewNotes{fields: fields, before: before, after: after}, h.machine, h.noBrowser, h.stdout, h.stderr)
}

// sessions lists the Sessions of every Workspace, newest change first. The
// change time is the latest update of the Session record or of one of its
// Tasks.
func (h *home) sessions(ctx context.Context, client *stack.Client) ([]sessionRow, error) {
	workspaces, err := listPages[stack.Workspace](ctx, client, "/v1/workspace/list", stack.Context{})
	if err != nil {
		return nil, err
	}
	current := ""
	for _, w := range workspaces {
		if within(h.cwd, w.Path) && len(w.Path) > len(current) {
			current = w.Path
		}
	}
	rows := []sessionRow{}
	for _, w := range workspaces {
		sessions, err := listPages[stack.Session](ctx, client, "/v1/session/list", stack.Context{WorkspaceID: w.ID})
		if err != nil {
			return nil, err
		}
		for _, s := range sessions {
			r := sessionRow{WorkspaceID: w.ID, WorkspacePath: w.Path, SessionID: s.ID, Name: s.Name, Closed: s.Closed, CurrentFolder: current != "" && w.Path == current}
			r.ChangedAt = later(r.ChangedAt, s.UpdatedAt)
			tasks, err := listPages[stack.Task](ctx, client, "/v1/task/list", stack.Context{WorkspaceID: w.ID, SessionID: s.ID})
			if err != nil {
				return nil, err
			}
			for _, t := range tasks {
				r.Tasks++
				if t.NeedsYou {
					r.NeedsYou++
				}
				r.ChangedAt = later(r.ChangedAt, t.UpdatedAt)
			}
			rows = append(rows, r)
		}
	}
	sortRows(rows)
	return rows, nil
}

func within(dir, root string) bool {
	return dir != "" && root != "" && (dir == root || strings.HasPrefix(dir, strings.TrimSuffix(root, "/")+"/"))
}

func later(t time.Time, stamp string) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, stamp); err == nil && parsed.After(t) {
		return parsed
	}
	return t
}

// listPages reads all pages of a lifecycle list or of the Task list.
func listPages[T any](ctx context.Context, client *stack.Client, path string, ref stack.Context) ([]T, error) {
	all := []T{}
	after := ""
	for {
		var body any = stack.ListRequest{Context: ref, Limit: 100, After: after}
		if path == "/v1/task/list" {
			body = stack.SnapshotPageRequest{Context: ref, Limit: 100, After: after}
		}
		data, err := client.Do(ctx, "POST", path, body)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data struct {
				Items []T    `json:"items"`
				Next  string `json:"next"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &page) != nil {
			return nil, &stack.Error{Code: "storage_unavailable", Message: "invalid service response", Retryable: true}
		}
		all = append(all, page.Data.Items...)
		if page.Data.Next == "" {
			return all, nil
		}
		after = page.Data.Next
	}
}

// mutate sends one change. When its outcome can be unknown, the error names
// the command and operation ID that send the same change again. It repeats no
// path or name.
func mutate(ctx context.Context, client *stack.Client, path string, body any, retry, id string) (stack.Receipt, error) {
	data, err := client.Do(ctx, "POST", path, body)
	if err != nil {
		if e, ok := err.(*stack.Error); ok && e.Code == "storage_unavailable" {
			copied := *e
			copied.Message += "; to send the same change again, use " + retry + " and --operation-id " + id
			return stack.Receipt{}, &copied
		}
		return stack.Receipt{}, err
	}
	var envelope struct {
		Data stack.Receipt `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return stack.Receipt{}, &stack.Error{Code: "storage_unavailable", Message: "invalid service response", Retryable: true}
	}
	return envelope.Data, nil
}

// operationID makes a random ID in the operation ID alphabet (cli- plus 22
// base64url characters).
func operationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("operating system random source unavailable")
	}
	return "cli-" + base64.RawURLEncoding.EncodeToString(b[:])
}
