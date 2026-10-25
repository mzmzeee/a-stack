package main

import (
	"context"
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

// herdrRequest runs herdr focus in the service, which holds the bindings.
// herdr bind binds the calling Agent to its own Herdr pane: it reads the pane
// from Herdr, then sends the same request as agent attach.
func herdrRequest(ctx context.Context, client *stack.Client, command string, f lifecycleFlags, executable string) ([]byte, error) {
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
	return client.Do(ctx, "POST", "/v1/agent/attach", stack.AgentBindingRequest{Context: ref, OperationID: f.operationID, AgentID: f.agent, ExpectedRevision: f.revision, Runtime: binding})
}
