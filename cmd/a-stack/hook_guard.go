package main

import (
	"context"
	"fmt"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// hookStopGuard returns why the agent should not stop yet, or "". It names
// the first Task assigned to the Agent that is working, waits for no
// Operator answer and has no report written since its progress last changed.
// Usually the Task list decides: its last action is the report, or it never
// had one. Only a Task with an older report costs a read of its history.
func hookStopGuard(ctx context.Context, client *stack.Client, id hookIdentity) (string, error) {
	mine, err := assignedTasks(ctx, client, id.ref, id.agent)
	if err != nil || len(mine) == 0 {
		return "", err
	}
	tasks, err := listPages[stack.Task](ctx, client, "/v1/task/list", id.ref)
	if err != nil {
		return "", err
	}
	for _, t := range tasks {
		if !mine[t.ID] || t.Progress != "working" || t.NeedsYou || t.Archived || t.LastAction == "task.report" {
			continue
		}
		if t.ReportAt != "" {
			reported, err := reportedSinceProgress(ctx, client, id.ref, t.ID)
			if err != nil {
				return "", err
			}
			if reported {
				continue
			}
		}
		return fmt.Sprintf("a-stack: your Task %q (Task %s) is still working and has no report since its status last changed. Before you stop, either write a task report and set the Task finished, or set it waiting with a reason.", printable(truncate(t.Title, 120)), t.ID), nil
	}
	return "", nil
}

// reportedSinceProgress reports whether the Task's latest report comes after
// its latest progress change in its history.
func reportedSinceProgress(ctx context.Context, client *stack.Client, ref stack.Context, task string) (bool, error) {
	var report, progress int64
	err := sessionPages(ctx, client, stack.SnapshotPageRequest{Context: ref, Kind: "history", TaskID: task}, func(h stack.TaskHistoryEntry) {
		if h.Action == "task.report" {
			report = h.Sequence
		}
		if h.Changes.Progress != "" {
			progress = h.Sequence
		}
	})
	return report > progress, err
}
