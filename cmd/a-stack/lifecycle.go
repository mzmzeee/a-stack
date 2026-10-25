package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"os"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

var lifecycleCommands = []string{"workspace create", "workspace list", "session create", "session list", "session inspect", "session close", "session reopen", "session appoint", "agent register", "agent list", "agent attach", "agent rebind", "agent observe"}

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
}

func (f *lifecycleFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.workspace, "workspace", "", "Workspace ID")
	fs.StringVar(&f.session, "session", "", "Session ID")
	fs.StringVar(&f.operationID, "operation-id", "", "Your ID for this change: 8–128 letters, digits, _ or -; reuse it to retry")
	fs.StringVar(&f.path, "path", "", "Absolute project folder")
	fs.StringVar(&f.name, "name", "", "Session or Agent name")
	fs.StringVar(&f.agent, "agent", "", "Agent ID")
	fs.StringVar(&f.worktree, "worktree", "", "Absolute working folder of the Agent")
	fs.StringVar(&f.newCredential, "new-credential-file", "", "New private credential file for the Agent")
	fs.StringVar(&f.runtimeID, "runtime-id", "", "ID of the running instance")
	fs.StringVar(&f.runtimeFile, "runtime-file", "", "Private JSON file with the runtime binding")
	fs.BoolVar(&f.clearRuntime, "clear-runtime", false, "Clear the runtime binding (Operator)")
	fs.StringVar(&f.state, "runtime-state", "", "unknown, working, idle, done, stopped or blocked")
	fs.Int64Var(&f.revision, "expected-revision", 0, "Current revision of the resource")
	fs.IntVar(&f.limit, "limit", 0, "Page size: 1 to 100, mail 1 to 500")
	fs.StringVar(&f.after, "after", "", "Continue after this ID or cursor")
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
		return " check version json help "
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
		allowed += "no-browser "
	} else if command == "serve" {
		allowed += "listen herdr-executable "
	} else {
		allowed += "credential-file as "
	}
	switch command {
	case "notifications status":
		allowed += "workspace session agent limit after "
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
			hash, err := stack.PrepareAgentCredential(f.newCredential, client.Discovery.DatabaseID, command, f.operationID, r)
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
