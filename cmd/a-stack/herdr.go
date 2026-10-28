package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"runtime"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// herdrSupported is false on Windows: Herdr's Windows build is a preview,
// and a-stack reaches Herdr through a Unix socket. Tests change it.
var herdrSupported = runtime.GOOS != "windows"

func isHerdrCommand(command string) bool {
	return command == "herdr bind" || command == "herdr focus"
}

// Herdr identifies Pi by a session file, but the hook uses the header ID.
// Read only its bounded metadata header; renamed files and custom IDs work.
func hookNativeSession(binding *stack.RuntimeBinding) (string, error) {
	if binding.Harness != "pi" {
		return binding.NativeSessionID, nil
	}
	info, err := os.Stat(binding.NativeSessionID)
	if err != nil || !info.Mode().IsRegular() {
		return "", invalid("cannot read Pi session header")
	}
	file, err := os.Open(binding.NativeSessionID)
	if err != nil {
		return "", invalid("cannot read Pi session header")
	}
	defer file.Close()
	var header struct{ Type, ID string }
	if json.NewDecoder(io.LimitReader(file, 16<<10)).Decode(&header) != nil || header.Type != "session" || header.ID == "" {
		return "", invalid("invalid Pi session header")
	}
	return header.ID, nil
}

// herdrRequest runs herdr focus in the service, which holds the bindings.
// herdr bind binds the calling Agent to its own Herdr pane: it reads the pane
// from Herdr, then lets the service verify any stale binding it replaces.
func herdrRequest(ctx context.Context, client *stack.Client, command string, f lifecycleFlags, executable, credential string) ([]byte, error) {
	ref := stack.Context{WorkspaceID: f.workspace, SessionID: f.session}
	if f.workspace == "" || f.session == "" || f.agent == "" {
		return nil, invalid("explicit --workspace, --session and --agent required")
	}
	if command == "herdr focus" {
		return client.Do(ctx, "POST", "/v1/herdr/focus", stack.HerdrFocusRequest{Context: ref, AgentID: f.agent})
	}
	// The pane gets the Agent's own name.
	name, err := agentName(ctx, client, ref, f.agent)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, &stack.Error{Code: "not_found", Message: "Agent not found in this Session"}
	}
	if executable == "" {
		executable = "herdr"
	}
	binding, err := stack.HerdrBind(ctx, executable, name, os.Getenv)
	if err != nil {
		return nil, err
	}
	worktree, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	session, err := hookNativeSession(binding)
	if err != nil {
		return nil, err
	}
	data, err := client.Do(ctx, "POST", "/v1/herdr/bind", stack.AgentBindingRequest{Context: ref, OperationID: f.operationID, AgentID: f.agent, ExpectedRevision: f.revision, Runtime: binding})
	if err != nil {
		return nil, err
	}
	var result struct{ Data stack.Receipt }
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	id := hookIdentity{credential: credential, agent: f.agent, ref: ref, worktree: worktree, runtimeID: binding.InstanceID}
	if err = enrollHook(id, binding.Harness, session, "", result.Data.Revision); err != nil {
		return nil, err
	}
	return data, nil
}
