package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func isOrdersCommand(command string) bool { return strings.HasPrefix(command, "workspace orders ") }

// ordersRequest runs workspace orders show, set and clear. Set takes the text
// from --body or --body-file.
func ordersRequest(ctx context.Context, client *stack.Client, command string, l lifecycleFlags, t taskFlags, seen map[string]bool) ([]byte, error) {
	path := "/v1/" + strings.ReplaceAll(command, " ", "/")
	switch command {
	case "workspace orders show":
		return client.Do(ctx, "POST", path, stack.Context{WorkspaceID: l.workspace, SessionID: l.session})
	case "workspace orders set":
		body := t.body

		if seen["body"] == seen["body-file"] {
			return nil, invalid("choose one of --body and --body-file")
		}

		if seen["body-file"] {
			var err error
			if body, err = bodyText(t.bodyFile); err != nil {
				return nil, err
			}
		}

		if len(body) > stack.MaxOrdersBytes {
			return nil, invalid(fmt.Sprintf("standing orders exceed %d bytes", stack.MaxOrdersBytes))
		}

		return client.Do(ctx, "POST", path, stack.OrdersRequest{OperationID: l.operationID, WorkspaceID: l.workspace, ExpectedRevision: l.revision, Body: body})
	}

	return client.Do(ctx, "POST", path, stack.OrdersRequest{OperationID: l.operationID, WorkspaceID: l.workspace, ExpectedRevision: l.revision})
}

// ordersLines is how many lines of the standing orders a reminder or a brief
// shows; the rest is one command away.
const ordersLines = 8

// ordersReminder is the standing orders for an Agent's context: the first
// lines, and the command that shows them all. It is empty without orders.
func ordersReminder(o *stack.StandingOrders, command string) string {
	if o == nil || strings.TrimSpace(o.Body) == "" {
		return ""
	}

	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(o.Body, "\r\n", "\n"), "\n"), "\n")
	var b strings.Builder
	b.WriteString("Standing orders of this Workspace, from the Operator. They override your own defaults:\n")

	for i, line := range lines {
		if i == ordersLines {
			fmt.Fprintf(&b, "  (%d more lines; read them all with: %s workspace orders show)\n", len(lines)-i, command)
			break
		}

		b.WriteString("  " + printable(truncate(strings.ReplaceAll(line, "\t", "  "), 200)) + "\n")
	}

	return b.String()
}
