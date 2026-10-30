package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	fields      string
}

// A trailing ! marks a required argument. Context is fixed when mcp starts,
// not supplied by the model. The remaining fields are the HTTP request fields.
var mcpTools = []mcpTool{
	{Name: "session_inspect", Description: "Read your Session, revisions, Agent ID and standing orders."},
	{Name: "task_list", Description: "Read one page of Tasks. Follow next with after; restart on a read revision conflict.", fields: "after limit read_revision"},
	{Name: "task_show", Description: "Read a Task and its current revision before changing it.", fields: "task_id! read_revision"},
	{Name: "task_create", Description: "Create a Task (Orchestrator only). after links prerequisites; assign takes Agent IDs or names. One atomic operation.", fields: "operation_id! title! description ticket_ref prerequisite_ids agent_ids after assign"},
	{Name: "task_status", Description: "Set Task progress as an assignee. Waiting/cancelled need reason; finished needs summary. Use the last Task revision.", fields: "task_id! operation_id! expected_revision! progress! reason summary"},
	{Name: "task_report", Description: "Replace the shared Task report as an assignee. Read it first when others share the Task.", fields: "task_id! operation_id! expected_revision! report!"},
	{Name: "task_assign", Description: "Assign an Agent ID or name to a Task (Orchestrator only).", fields: "task_id! operation_id! expected_revision! agent_id!"},
	{Name: "task_dependencies", Description: "Read a page of this Task's prerequisites and their progress.", fields: "task_id! after limit read_revision"},
	{Name: "dependency_add", Description: "Make dependent_id wait for prerequisite_id (Orchestrator only). expected_graph_revision is the Session graph_revision, not a Task revision.", fields: "operation_id! expected_graph_revision! prerequisite_id! dependent_id!"},
	{Name: "mail_check", Description: "Check your assigned Tasks. Keep tasks[].cursor in cursors and follow next_task with after_task while more is true. No server-side read position.", fields: "cursors after_task task_limit limit"},
	{Name: "mail_read", Description: "Read a page of Task mail as an assignee. Save cursor and follow it while more is true.", fields: "task_id! cursor limit"},
	{Name: "mail_send", Description: "Send Task mail to all current assignees. Mentions take assignee names, also parsed from @name in body. No Task revision is needed.", fields: "task_id! operation_id! body! mentions"},
	{Name: "task_attention_set", Description: "Ask the Operator one explicit Needs you question as an assignee. This does not change progress.", fields: "task_id! operation_id! expected_revision! question!"},
	{Name: "task_attention_resolve", Description: "Resolve the open Needs you question with a note as an assignee.", fields: "task_id! operation_id! expected_revision! note!"},
	{Name: "task_handoff", Description: "Read the Task, report, standing orders, unfinished prerequisites, assignees and latest mail. Does not change anything.", fields: "task_id! limit"},
}

func mcpFieldType(tool, field string) string {
	switch field {
	case "expected_revision", "expected_graph_revision", "read_revision", "limit", "task_limit":
		return "integer"
	case "assign", "prerequisite_ids", "agent_ids", "mentions":
		return "array"
	case "after":
		if tool == "task_create" {
			return "array"
		}
	case "cursors":
		return "object"
	}

	return "string"
}

func mcpToolList() []mcpTool {
	tools := append([]mcpTool(nil), mcpTools...)
	for i := range tools {
		tool := &tools[i]

		properties := map[string]any{}
		required := []string{}
		for _, field := range strings.Fields(tool.fields) {
			name := strings.TrimSuffix(field, "!")

			kind := mcpFieldType(tool.Name, name)
			property := map[string]any{"type": kind}
			if kind == "array" {
				property["items"] = map[string]string{"type": "string"}
			}

			if kind == "object" {
				property["additionalProperties"] = map[string]string{"type": "string"}
			}

			properties[name] = property
			if strings.HasSuffix(field, "!") {
				required = append(required, name)
			}
		}

		tool.InputSchema = map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}

	return tools
}

// mcpArguments validates the small schemas above with encoding/json. The
// service still enforces text limits, IDs, roles, membership and revisions.
func mcpArguments(tool mcpTool, raw json.RawMessage) (map[string]json.RawMessage, error) {
	args := map[string]json.RawMessage{}
	if len(raw) != 0 && (json.Unmarshal(raw, &args) != nil || args == nil) {
		return nil, invalid("arguments must be an object")
	}

	allowed := map[string]bool{}
	for _, field := range strings.Fields(tool.fields) {
		name := strings.TrimSuffix(field, "!")

		allowed[name] = true
		value, exists := args[name]
		if !exists {
			if strings.HasSuffix(field, "!") {
				return nil, invalid(name + " is required")
			}

			continue
		}

		var dest any
		switch mcpFieldType(tool.Name, name) {
		case "string":
			dest = new(string)
		case "integer":
			dest = new(int64)
		case "array":
			dest = new([]*string)
		case "object":
			dest = new(map[string]*string)
		}

		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, dest) != nil {
			return nil, invalid("invalid argument type for " + name)
		}

		// encoding/json accepts null into a string as its zero value. Reject
		// null members too, rather than silently changing an ID or cursor.
		switch values := dest.(type) {
		case *[]*string:
			for _, value := range *values {
				if value == nil {
					return nil, invalid("invalid argument type for " + name)
				}
			}
		case *map[string]*string:
			for _, value := range *values {
				if value == nil {
					return nil, invalid("invalid argument type for " + name)
				}
			}
		}
	}

	for name := range args {
		if !allowed[name] {
			return nil, invalid("unknown tool argument")
		}
	}

	return args, nil
}

func mcpCall(ctx context.Context, client *stack.Client, ref stack.Context, tool mcpTool, raw json.RawMessage) ([]byte, error) {
	args, err := mcpArguments(tool, raw)
	if err != nil {
		return nil, err
	}

	if tool.Name == "task_handoff" {
		var task string
		var limit int
		_ = json.Unmarshal(args["task_id"], &task)
		_ = json.Unmarshal(args["limit"], &limit)
		var out bytes.Buffer
		err := taskHandoff(ctx, client, lifecycleFlags{workspace: ref.WorkspaceID, session: ref.SessionID, limit: limit}, taskFlags{task: task}, args["limit"] != nil, true, &out)
		return out.Bytes(), err
	}

	if tool.Name == "task_create" {
		for _, alias := range []struct{ from, to string }{{"after", "prerequisite_ids"}, {"assign", "agent_ids"}} {
			if value, ok := args[alias.from]; ok {
				if _, exists := args[alias.to]; exists {
					return nil, invalid("choose " + alias.from + " or " + alias.to)
				}

				args[alias.to] = value
				delete(args, alias.from)
			}
		}
	}

	// Resolve names with the same helper as the CLI; this grants no authority.
	for _, field := range []string{"agent_id", "agent_ids"} {
		if raw, ok := args[field]; ok {
			var names []string
			if field == "agent_id" {
				var name string
				_ = json.Unmarshal(raw, &name)
				names = []string{name}
			} else {
				_ = json.Unmarshal(raw, &names)
			}

			values := make([]*string, len(names))
			for i := range names {
				values[i] = &names[i]
			}

			if err := agentIDs(ctx, client, ref, "agent", values...); err != nil {
				return nil, err
			}

			if field == "agent_id" {
				args[field], _ = json.Marshal(names[0])
			} else {
				args[field], _ = json.Marshal(names)
			}
		}
	}

	args["workspace_id"], _ = json.Marshal(ref.WorkspaceID)
	args["session_id"], _ = json.Marshal(ref.SessionID)

	return client.Do(ctx, "POST", "/v1/"+strings.ReplaceAll(tool.Name, "_", "/"), args)
}
