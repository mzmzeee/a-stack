package main

import (
	"context"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func isNotificationCommand(command string) bool {
	return command == "notifications status" || command == "notifications drain"
}
func notificationRequest(ctx context.Context, client *stack.Client, command string, f lifecycleFlags) ([]byte, error) {
	if f.workspace == "" || f.session == "" {
		return nil, missingContext
	}
	endpoint := "/v1/notifications/status"
	if command == "notifications drain" {
		endpoint = "/v1/notifications/drain"
	}
	return client.Do(ctx, "POST", endpoint, stack.NoticeRequest{Context: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}, AgentID: f.agent, Limit: f.limit, After: f.after})
}
