package main

import (
	"context"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// Completion is a read-only client. The entire lookup, including legacy
// credential context and every page, shares one deadline. Nothing is emitted
// until all reads succeed, so a stopped or slow service stays invisible.
func completeServiceValue(name string, values map[string]string) []completionItem {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	state := values["state-dir"]
	if state == "" {
		var err error
		state, err = stack.DefaultStateDir()
		if err != nil {
			return nil
		}
	}

	if remote, seen := values["remote"]; seen && remote == "" {
		return nil
	}

	client, err := commandClient(ctx, state, values["credential-file"], values["as"], values["remote"])
	if err != nil {
		return nil
	}

	command := "agent list"
	if name == "task" {
		command = "task list"
	}

	l := lifecycleFlags{workspace: values["workspace"], session: values["session"]}
	if loadContext(ctx, client, l, command) != nil || fillContext(client.Credential, &l, command) != nil || l.workspace == "" || l.session == "" {
		return nil
	}

	ref := stack.Context{WorkspaceID: l.workspace, SessionID: l.session}
	items := []completionItem{}
	if name == "task" {
		tasks, err := listPages[stack.Task](ctx, client, "/v1/task/list", ref)
		if err != nil {
			return nil
		}

		for _, task := range tasks {
			if !task.Archived && (task.Progress == "waiting" || task.Progress == "working") {
				items = append(items, completionItem{task.ID, task.Title})
			}
		}
	} else {
		agents, err := listPages[stack.Agent](ctx, client, "/v1/agent/list", ref)
		if err != nil {
			return nil
		}

		for _, agent := range agents {
			items = append(items, completionItem{agent.Name, ""})
		}
	}

	return items
}
