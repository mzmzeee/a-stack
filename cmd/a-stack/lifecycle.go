package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

var lifecycleCommands = []string{"workspace create", "workspace list", "workspace orders show", "workspace orders set", "workspace orders clear", "session create", "session list", "session inspect", "session close", "session reopen", "session appoint", "agent register", "agent list", "agent attach", "agent rebind", "agent observe"}

func isLifecycleCommand(command string) bool {
	for _, c := range lifecycleCommands {
		if c == command {
			return true
		}
	}
	return false
}

type lifecycleFlags struct {
	workspace, session, operationID, path, name, agent, worktree, newCredential, runtimeID, runtimeFile, state, after string
	revision                                                                                                          int64
	limit                                                                                                             int
	clearRuntime                                                                                                      bool
	// afters has each --after: task create takes several prerequisites; the
	// other commands take one cursor, after.
	afters []string
}

func (f *lifecycleFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.workspace, "workspace", "", "Workspace ID")
	fs.StringVar(&f.session, "session", "", "Session ID")
	fs.StringVar(&f.operationID, "operation-id", "", "Your ID for this change: 8–128 letters, digits, _ or -; reuse it to retry")
	fs.StringVar(&f.path, "path", "", "Absolute project folder")
	fs.StringVar(&f.name, "name", "", "Session or Agent name")
	fs.StringVar(&f.agent, "agent", "", "Agent ID, or its name in the Session")
	fs.StringVar(&f.worktree, "worktree", "", "Absolute working folder of the Agent")
	fs.StringVar(&f.newCredential, "new-credential-file", "", "New private credential file for the Agent")
	fs.StringVar(&f.runtimeID, "runtime-id", "", "ID of the running instance")
	fs.StringVar(&f.runtimeFile, "runtime-file", "", "Private JSON file with the runtime binding")
	fs.BoolVar(&f.clearRuntime, "clear-runtime", false, "Clear the runtime binding (Operator)")
	fs.StringVar(&f.state, "runtime-state", "", "unknown, working, idle, done, stopped or blocked")
	fs.Int64Var(&f.revision, "expected-revision", 0, "Current revision of the resource")
	fs.IntVar(&f.limit, "limit", 0, "Page size: 1 to 100, mail 1 to 500")
	fs.Func("after", "Continue after this ID or cursor", func(value string) error {
		f.after, f.afters = value, append(f.afters, value)
		return nil
	})
}

// acceptedFlags lists the flags a command accepts, space-separated with a
// space at both ends. Help uses the same list.
func acceptedFlags(command string) string {
	// No service and no credential.
	if command == "version" {
		return " json help "
	} else if command == "skill install" {
		return " project refresh json help "
	} else if command == "skill uninstall" {
		return " project json help "
	} else if command == "update" {
		return " check version no-restart json help "
	} else if command == "statusline" {
		return " state-dir help "
	} else if command == "hook" {
		return " state-dir credential-file workspace session agent runtime-id no-stop-guard no-mail-nudge worktree help "
	} else if command == "hook install" {
		return " claude codex pi opencode project state-dir credential-file workspace session agent name runtime-id no-stop-guard no-mail-nudge worktree json help "
	} else if command == "hook uninstall" {
		return " claude codex pi opencode project worktree json help "
	} else if command == "service install" || command == "service status" {
		return " state-dir json help "
	} else if command == "service uninstall" {
		return " json help "
	}
	allowed := " state-dir json help "
	// The Operator shortcuts use operator.json and may start the service, so
	// they take neither credential nor serve flags.
	if command == "" {
		allowed += "no-browser resume r "
	} else if command == "new" {
		allowed += "no-browser orchestrator "
	} else if command == "serve" {
		allowed += "listen herdr-executable "
	} else {
		allowed += "credential-file as "
	}
	switch command {
	case "notifications status":
		allowed += "workspace session agent limit after "
	case "herdr focus":
		allowed += "workspace session agent "
	case "herdr bind":
		allowed += "workspace session agent expected-revision operation-id herdr-executable "
	case "notifications drain":
		allowed += "workspace session limit "
	case "view revoke":
		allowed += "workspace session all "
	case "open":
		allowed += "workspace session no-browser "
	case "storage backup":
		allowed += "destination "
	case "workspace create":
		allowed += "operation-id path "
	case "workspace list":
		allowed += "workspace session limit after "
	case "workspace orders show":
		allowed += "workspace session "
	case "workspace orders set":
		allowed += "workspace operation-id expected-revision body body-file "
	case "workspace orders clear":
		allowed += "workspace operation-id expected-revision "
	case "session create":
		allowed += "workspace operation-id name "
	case "session list", "agent list":
		allowed += "workspace session limit after "
	case "session inspect":
		allowed += "workspace session "
	case "session close", "session reopen":
		allowed += "workspace session operation-id expected-revision "
	case "session appoint":
		allowed += "workspace session operation-id expected-revision agent "
	case "agent register":
		allowed += "workspace session operation-id name worktree new-credential-file "
	case "agent attach":
		allowed += "workspace session operation-id expected-revision agent runtime-id runtime-file "
	case "agent rebind":
		allowed += "workspace session operation-id expected-revision agent runtime-id runtime-file clear-runtime new-credential-file "
	case "agent observe":
		allowed += "workspace session agent expected-revision runtime-id runtime-state "
	case "watch":
		allowed += "session all notify notify-text "
	case "status":
		allowed += "session all "
	}
	if isTaskCommand(command) {
		allowed += "workspace session " + taskAllowedFlags(command) + " "
	}
	if isMailCommand(command) {
		allowed += "workspace session " + mailAllowedFlags(command) + " "
	}
	return allowed
}
func lifecycleRequest(ctx context.Context, client *stack.Client, command string, f lifecycleFlags) ([]byte, error) {
	// A new credential file that this request writes. If the request fails for
	// good, no Agent holds its token, so the file goes. A retryable failure
	// keeps it: the same command resumes with it.
	fresh := ""
	if command == "agent register" || command == "agent rebind" {
		if _, err := os.Lstat(f.newCredential); errors.Is(err, fs.ErrNotExist) {
			fresh = f.newCredential
		}
	}
	data, err := lifecycleCall(ctx, client, command, f)
	var e *stack.Error
	if fresh != "" && errors.As(err, &e) && !e.Retryable {
		_ = os.Remove(fresh)
	}
	return data, err
}
func lifecycleCall(ctx context.Context, client *stack.Client, command string, f lifecycleFlags) ([]byte, error) {
	ref := stack.Context{WorkspaceID: f.workspace, SessionID: f.session}
	var body any
	switch command {
	case "workspace create":
		body = stack.WorkspaceCreateRequest{OperationID: f.operationID, Path: f.path}
	case "workspace list", "session list", "agent list":
		body = stack.ListRequest{Context: ref, Limit: f.limit, After: f.after}
	case "session create":
		body = stack.SessionCreateRequest{OperationID: f.operationID, WorkspaceID: f.workspace, Name: f.name}
	case "session inspect":
		body = ref
	case "session close", "session reopen", "session appoint":
		body = stack.SessionChangeRequest{Context: ref, OperationID: f.operationID, ExpectedRevision: f.revision, AgentID: f.agent}
	case "agent register":
		r := stack.AgentRegisterRequest{Context: ref, OperationID: f.operationID, Name: f.name, WorktreePath: f.worktree}
		hash, err := stack.PrepareAgentCredential(f.newCredential, client.Discovery.DatabaseID, command, f.operationID, r)
		if err != nil {
			return nil, err
		}
		r.CredentialHash = hash
		body = r
	case "agent attach", "agent rebind":
		if f.runtimeFile != "" && f.runtimeID != "" {
			return nil, invalid("choose --runtime-id or --runtime-file")
		}
		if f.clearRuntime && (f.runtimeID != "" || f.runtimeFile != "") {
			return nil, invalid("--clear-runtime excludes a replacement runtime")
		}
		var binding *stack.RuntimeBinding
		if f.runtimeID != "" {
			binding = &stack.RuntimeBinding{InstanceID: f.runtimeID}
		}
		if f.runtimeFile != "" {
			var err error
			binding, err = stack.ReadRuntimeBinding(f.runtimeFile)
			if err != nil {
				return nil, err
			}
		}
		if binding == nil && !f.clearRuntime {
			return nil, invalid("explicit runtime binding or Operator --clear-runtime required")
		}
		r := stack.AgentBindingRequest{Context: ref, OperationID: f.operationID, AgentID: f.agent, ExpectedRevision: f.revision, Runtime: binding}
		if command == "agent rebind" {
			name, err := agentName(ctx, client, ref, f.agent)
			if err != nil {
				return nil, err
			}
			hash, err := stack.PrepareRebindCredential(f.newCredential, client.Discovery.DatabaseID, r, name)
			if err != nil {
				return nil, err
			}
			r.CredentialHash = hash
		}
		body = r
	case "agent observe":
		body = stack.AgentObserveRequest{Context: ref, AgentID: f.agent, ExpectedRevision: f.revision, RuntimeID: f.runtimeID, State: f.state}
	default:
		return nil, invalid("unknown lifecycle command")
	}
	return client.Do(ctx, "POST", "/v1/"+strings.ReplaceAll(command, " ", "/"), body)
}

// agentName looks up an Agent's name for its new credential file. It returns ""
// for an unknown ID, which the rebind itself then rejects.
func agentName(ctx context.Context, client *stack.Client, ref stack.Context, id string) (string, error) {
	after := ""
	for {
		data, err := client.Do(ctx, "POST", "/v1/agent/list", stack.ListRequest{Context: ref, Limit: 100, After: after})
		if err != nil {
			return "", err
		}
		var page struct {
			Data struct {
				Items []stack.Agent `json:"items"`
				Next  string        `json:"next"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &page) != nil {
			return "", invalid("unexpected agent list response")
		}
		for _, agent := range page.Data.Items {
			if agent.ID == id {
				return agent.Name, nil
			}
		}
		if page.Data.Next == "" {
			return "", nil
		}
		after = page.Data.Next
	}
}

// fillContext takes --workspace and --session from an Agent credential file
// that names them. A flag that is given must name the same ID.
func fillContext(c stack.Credential, f *lifecycleFlags, command string) error {
	allowed := acceptedFlags(command)
	for _, field := range []struct {
		flag        *string
		saved, name string
	}{{&f.workspace, c.WorkspaceID, "workspace"}, {&f.session, c.SessionID, "session"}} {
		if field.saved == "" || !strings.Contains(allowed, " "+field.name+" ") {
			continue
		}
		if *field.flag == "" {
			*field.flag = field.saved
		} else if *field.flag != field.saved {
			return invalid("--" + field.name + " does not match the " + map[string]string{"workspace": "Workspace", "session": "Session"}[field.name] + " of the credential file")
		}
	}
	return nil
}

// missingContext is the error of a command that has no Workspace or Session.
var missingContext = invalid("explicit --workspace and --session required; the Operator finds them with a-stack workspace list and a-stack session list --workspace ID")

// legacyContext is the error when neither the credential file nor the service
// can name the Agent's Workspace and Session.
var legacyContext = invalid("this credential file is from before a-stack v0.1.5 and does not name its Workspace and Session, and the service is too old to look them up: add --workspace WORKSPACE_ID --session SESSION_ID; the one who briefed you has them, and the Operator finds them with a-stack workspace list and a-stack session list --workspace WORKSPACE_ID")

// loadContext asks the service for the Workspace and Session of an Agent
// credential file that does not name them, which is a file from before
// v0.1.5, when the command takes them and the flags do not give both. The
// service stores the Session of each credential, so the token is enough. The
// file stays as it is.
func loadContext(ctx context.Context, client *stack.Client, f lifecycleFlags, command string) error {
	c := &client.Credential
	if c.Kind != stack.AgentCapability || c.WorkspaceID != "" && c.SessionID != "" || f.workspace != "" && f.session != "" || !strings.Contains(acceptedFlags(command), " session ") {
		return nil
	}
	data, err := client.Do(ctx, "GET", "/v1/credential", nil)
	var e *stack.Error
	if errors.As(err, &e) && e.Code == "not_found" {
		return legacyContext // a service before v0.1.7 has no such read
	}
	if err != nil {
		return err
	}
	var envelope struct {
		Data stack.CredentialContext `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || !stack.ValidID(envelope.Data.WorkspaceID) || !stack.ValidID(envelope.Data.SessionID) {
		return legacyContext
	}
	c.WorkspaceID, c.SessionID = envelope.Data.WorkspaceID, envelope.Data.SessionID
	return nil
}

// agentIDs replaces each Agent name in values with the ID of the Agent of
// that name in the Session, with one agent list. An Agent ID stays as it is.
// Names are unique in a Workspace, and the service checks the IDs, so the
// lookup authorizes nothing (ADR 0002).
func agentIDs(ctx context.Context, client *stack.Client, ref stack.Context, flagName string, values ...*string) error {
	var agents []stack.Agent
	for _, value := range values {
		if stack.ValidID(*value) {
			continue
		}
		if ref.WorkspaceID == "" || ref.SessionID == "" {
			return missingContext
		}
		if agents == nil {
			var err error
			if agents, err = listPages[stack.Agent](ctx, client, "/v1/agent/list", ref); err != nil {
				return err
			}
		}
		id := ""
		for _, agent := range agents {
			if agent.Name == *value {
				if id != "" {
					return invalid("--" + flagName + " " + *value + " names more than one Agent; give the Agent ID from a-stack agent list")
				}
				id = agent.ID
			}
		}
		if id == "" {
			return invalid("--" + flagName + " " + *value + " is no Agent of this Session; see a-stack agent list")
		}
		*value = id
	}
	return nil
}
