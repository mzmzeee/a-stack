package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func isHerdrCommand(command string) bool {
	return command == "herdr bind"
}

// herdrRequest: herdr bind binds the calling Agent to its own Herdr pane. It
// reads the pane from Herdr, then sends the same request as agent attach.
func herdrRequest(ctx context.Context, client *stack.Client, command string, f lifecycleFlags, executable string) ([]byte, error) {
	ref := stack.Context{WorkspaceID: f.workspace, SessionID: f.session}
	if f.workspace == "" || f.session == "" || f.agent == "" {
		return nil, invalid("explicit --workspace, --session and --agent required")
	}
	name, err := agentName(ctx, client, ref, f.agent)
	if err != nil {
		return nil, err
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

// agentName finds the Agent's own name, which herdr bind uses for its pane.
func agentName(ctx context.Context, client *stack.Client, ref stack.Context, id string) (string, error) {
	after := ""
	for {
		data, err := client.Do(ctx, "POST", "/v1/agent/list", stack.ListRequest{Context: ref, Limit: 100, After: after})
		if err != nil {
			return "", err
		}
		var page struct {
			Data stack.Page[stack.Agent] `json:"data"`
		}
		if json.Unmarshal(data, &page) != nil {
			return "", invalid("unreadable Agent list")
		}
		for _, agent := range page.Data.Items {
			if agent.ID == id {
				return agent.Name, nil
			}
		}
		if page.Data.Next == "" || page.Data.Next == after {
			return "", &stack.Error{Code: "not_found", Message: "Agent not found in this Session"}
		}
		after = page.Data.Next
	}
}
