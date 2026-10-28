package stack

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

// The model owns revisions, membership, edges and delivered positions; it never
// derives expected state from a database query after an action.
func TestRegressionModel(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		f := tasksFixture(t)
		defer f.service.Close(ctx)
		s := f.service.Store
		ids := make([]string, 3)
		revisions := []int64{2, 2, 2}
		members := []bool{true, true, true}
		bodies := make([][]string, 3)
		cursors := make([]string, 3)
		positions := make([]int, 3)
		edges := [3][3]bool{}
		for i := range ids {
			task := f.create(t, fmt.Sprintf("Model %d", i))
			ids[i] = task.ResourceID
			f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: ids[i], ExpectedRevision: 1, AgentID: f.aID})
		}
		session, err := s.InspectSession(ctx, f.orch.Token, f.ref(0))
		if err != nil {
			rt.Fatal(err)
		}
		graphRevision := session.GraphRevision
		checkError := func(err error, code string) {
			rt.Helper()
			if code == "" {
				if err != nil {
					rt.Fatal(err)
				}
				return
			}
			e, ok := err.(*Error)
			if !ok || e.Code != code {
				rt.Fatalf("want %s, got %v", code, err)
			}
		}
		for step := 0; step < 60; step++ {
			i := rapid.IntRange(0, 2).Draw(rt, "task")
			op := fmt.Sprintf("model-operation-%03d", step)
			switch rapid.IntRange(0, 5).Draw(rt, "action") {
			case 0: // Membership changes must affect reads and receipt replays immediately.
				action := "assign"
				if members[i] {
					action = "unassign"
				}
				_, err := s.ChangeTask(ctx, f.orch.Token, action, TaskChangeRequest{Context: f.ref(0), TaskID: ids[i], AgentID: f.aID, ExpectedRevision: revisions[i], OperationID: op})
				checkError(err, "")
				members[i] = !members[i]
				revisions[i]++
			case 1:
				stale := rapid.Bool().Draw(rt, "stale")
				revision := revisions[i]
				if stale {
					revision--
				}
				report := fmt.Sprintf("step %d", step)
				req := TaskChangeRequest{Context: f.ref(0), TaskID: ids[i], ExpectedRevision: revision, OperationID: op, Report: &report}
				result, err := s.ChangeTask(ctx, f.a.Token, "report", req)
				code := ""
				if !members[i] {
					code = "forbidden"
				} else if stale {
					code = "conflict"
				}
				checkError(err, code)
				if code == "" {
					revisions[i]++
					again, err := s.ChangeTask(ctx, f.a.Token, "report", req)
					checkError(err, "")
					if again != result {
						rt.Fatal("report replay changed receipt")
					}
				}
			case 2:
				body := fmt.Sprintf("message %d", step)
				req := MailSendRequest{Context: f.ref(0), TaskID: ids[i], OperationID: op, Body: body}
				result, err := s.SendMail(ctx, f.a.Token, req)
				if !members[i] {
					checkError(err, "forbidden")
					break
				}
				checkError(err, "")
				bodies[i] = append(bodies[i], body)
				again, err := s.SendMail(ctx, f.a.Token, req)
				checkError(err, "")
				if result != again || result.Revision != int64(len(bodies[i])) {
					rt.Fatal("send replay/sequence differs from model")
				}
			case 3:
				limit := rapid.IntRange(1, 4).Draw(rt, "page size")
				page, err := s.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: ids[i], Cursor: cursors[i], Limit: limit})
				if !members[i] {
					checkError(err, "forbidden")
					break
				}
				checkError(err, "")
				want := min(limit, len(bodies[i])-positions[i])
				if len(page.Messages) != want {
					rt.Fatal("cursor skipped or repeated content")
				}
				for j, message := range page.Messages {
					pos := positions[i] + j
					if message.Body != bodies[i][pos] || message.Sequence != int64(pos+1) {
						rt.Fatal("wrong body or sequence")
					}
				}
				positions[i] += want
				cursors[i] = page.Cursor
				if page.More != (positions[i] < len(bodies[i])) {
					rt.Fatal("wrong continuation")
				}
			case 4:
				j := rapid.IntRange(0, 2).Draw(rt, "dependent")
				if edges[i][j] {
					break
				}
				reachable := edges
				for k := range 3 {
					for a := range 3 {
						for b := range 3 {
							reachable[a][b] = reachable[a][b] || reachable[a][k] && reachable[k][b]
						}
					}
				}
				req := DependencyRequest{Context: f.ref(0), OperationID: op, ExpectedGraphRevision: graphRevision, PrerequisiteID: ids[i], DependentID: ids[j]}
				result, err := s.ChangeDependency(ctx, f.orch.Token, "add", req)
				if i == j {
					checkError(err, "invalid_input")
				} else if reachable[j][i] {
					checkError(err, "conflict")
				} else {
					checkError(err, "")
					edges[i][j] = true
					graphRevision++
					again, err := s.ChangeDependency(ctx, f.orch.Token, "add", req)
					checkError(err, "")
					if again != result {
						rt.Fatal("edge replay changed receipt")
					}
				}
			case 5:
				// Operator and unassigned Orchestrator have no mail authority.
				for _, token := range []string{f.orch.Token, f.operator.Token, f.b.Token} {
					_, err := s.ReadMail(ctx, token, MailReadRequest{Context: f.ref(0), TaskID: ids[i]})
					checkError(err, "forbidden")
				}
			}
			for j, id := range ids {
				shown, err := s.ShowTask(ctx, f.operator.Token, TaskReadRequest{Context: f.ref(0), TaskID: id})
				checkError(err, "")
				if shown.Revision != revisions[j] || shown.AssigneeCount != boolCount(members[j]) {
					rt.Fatal("Task diverged from model")
				}
			}
		}
	})
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
