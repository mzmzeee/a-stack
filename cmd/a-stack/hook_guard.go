package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// stopGuard runs the Stop guard and reports whether it blocked the stop. It
// never blocks two stops in a row: the harness sets stop_hook_active on the
// stop after a block, and the guard also leaves a marker file next to the
// mail nudge's cursor file, which the next stop removes, for a harness that
// does not send the flag. Without a marker file it does not block at all.
func stopGuard(ctx context.Context, client *stack.Client, id hookIdentity, in hookInput, stdout io.Writer) bool {
	marker, ok := hookCacheFile(id.agent, in.SessionID, ".stop")
	if !ok || os.Remove(marker) == nil || in.StopHookActive {
		return false
	}

	reason, err := hookStopGuard(ctx, client, id)
	if err != nil || reason == "" || os.WriteFile(marker, nil, 0o600) != nil {
		return false
	}

	_ = json.NewEncoder(stdout).Encode(map[string]string{"decision": "block", "reason": reason})

	return true
}

// hookStopGuard returns why the agent should not stop yet, or "". It names
// the first Task assigned to the Agent that is working, that this Agent set
// working, that waits for no Operator answer and that has no report written
// since. Usually the Task list decides: its last action is the report, or the
// status change, which names who made it. Otherwise it costs a read of the
// Task's history. A closed Session takes no changes, so it never blocks.
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

		setter := t.UpdatedBy // when the last action is the status change
		if t.LastAction != "task.status" {
			var reported bool
			if setter, reported, err = workingSince(ctx, client, id.ref, t.ID); err != nil {
				return "", err
			}

			if reported {
				continue
			}
		}

		if setter != id.agent {
			continue // a helper's or a watching Orchestrator's stop
		}

		if session, err := inspectSession(ctx, client, id.ref); err != nil || session.Closed {
			return "", err
		}

		return guardReason(id, t), nil
	}

	return "", nil
}

// guardReason names the Task and gives the ways out as whole commands, with
// the Task's revision and operation IDs, so the agent need not look them up.
func guardReason(id hookIdentity, t stack.Task) string {
	run := agentCommand(id)
	op := func(rev int64, step string) string { return fmt.Sprintf("guard-%s-%d-%s", t.ID[:10], rev, step) }
	var b strings.Builder
	fmt.Fprintf(&b, "a-stack: your Task %q (Task %s) is still working and has no report since you set it working. Before you stop, do one of these, with your own text:\n", printable(truncate(t.Title, 120)), t.ID)
	fmt.Fprintf(&b, "1. Report and finish:\n%s task report --task %s --report 'What you did, and where' --expected-revision %d --operation-id %s\n", run, t.ID, t.Revision, op(t.Revision, "report"))
	fmt.Fprintf(&b, "%s task status --task %s --progress finished --summary 'What you did' --expected-revision %d --operation-id %s\n", run, t.ID, t.Revision+1, op(t.Revision+1, "finish"))
	fmt.Fprintf(&b, "2. Wait:\n%s task status --task %s --progress waiting --reason 'What it waits for' --expected-revision %d --operation-id %s\n", run, t.ID, t.Revision, op(t.Revision, "wait"))

	if t.AssigneeCount > 1 {
		fmt.Fprintf(&b, "Other Agents share this Task: read its report with task show before you replace it. If only your part is done, say so with mail send --task %s instead, and leave the Task working.\n", t.ID)
	}

	return b.String()
}

// workingSince reads the Task's history: who made its latest progress change,
// and whether its latest report comes after that change.
func workingSince(ctx context.Context, client *stack.Client, ref stack.Context, task string) (setter string, reported bool, err error) {
	var report, progress int64
	err = sessionPages(ctx, client, stack.SnapshotPageRequest{Context: ref, Kind: "history", TaskID: task}, func(h stack.TaskHistoryEntry) {
		if h.Action == "task.report" {
			report = h.Sequence
		}

		if h.Changes.Progress != "" {
			progress, setter = h.Sequence, h.ActorID
		}
	})

	return setter, report > progress, err
}
