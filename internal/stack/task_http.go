package stack

import "net/http"

func taskReadEndpoint(path string) bool {
	switch path {
	case "/v1/session/snapshot", "/v1/session/page", "/v1/task/list", "/v1/task/show", "/v1/task/history", "/v1/task/assignees", "/v1/task/references", "/v1/task/dependencies":
		return true
	}
	return false
}
func (s *Service) taskHTTP(w http.ResponseWriter, r *http.Request, token string) bool {
	var request any
	action := ""
	switch r.URL.Path {
	case "/v1/session/snapshot":
		request = &SnapshotRequest{}
	case "/v1/session/page", "/v1/task/list", "/v1/task/history", "/v1/task/assignees", "/v1/task/references", "/v1/task/dependencies":
		request = &SnapshotPageRequest{}
	case "/v1/task/show":
		request = &TaskReadRequest{}
	case "/v1/task/create":
		request = &TaskCreateRequest{}
	case "/v1/task/edit":
		action = "edit"
	case "/v1/task/assign":
		action = "assign"
	case "/v1/task/unassign":
		action = "unassign"
	case "/v1/task/status":
		action = "status"
	case "/v1/task/reopen":
		action = "reopen"
	case "/v1/task/archive":
		action = "archive"
	case "/v1/task/unarchive":
		action = "unarchive"
	case "/v1/task/report":
		action = "report"
	case "/v1/task/attention/set":
		action = "attention.set"
	case "/v1/task/attention/resolve":
		action = "attention.resolve"
	case "/v1/task/reference/add":
		action = "reference.add"
	case "/v1/task/reference/remove":
		action = "reference.remove"
	case "/v1/dependency/add", "/v1/dependency/remove":
		request = &DependencyRequest{}
	default:
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return true
	}
	if action != "" {
		request = &TaskChangeRequest{}
	}
	if err := requestJSON(w, r, request); err != nil {
		writeError(w, err)
		return true
	}
	var data any
	var err error
	switch q := request.(type) {
	case *SnapshotRequest:
		data, err = s.Store.Snapshot(r.Context(), token, *q)
	case *TaskReadRequest:
		data, err = s.Store.ShowTask(r.Context(), token, *q)
	case *SnapshotPageRequest:
		kind := ""
		switch r.URL.Path {
		case "/v1/task/list":
			kind = "tasks"
		case "/v1/task/history":
			kind = "history"
		case "/v1/task/assignees":
			kind = "assignments"
		case "/v1/task/references":
			kind = "references"
		case "/v1/task/dependencies":
			kind = "dependencies"
		}
		if kind != "" && r.URL.Path != "/v1/task/list" && q.TaskID == "" {
			writeError(w, failure("invalid_input", "Task ID required"))
			return true
		}
		if kind != "" {
			if q.Kind != "" && q.Kind != kind {
				writeError(w, failure("invalid_input", "page kind does not match endpoint"))
				return true
			}
			q.Kind = kind
		}
		data, err = s.Store.SnapshotPage(r.Context(), token, *q)
	case *TaskCreateRequest:
		data, err = s.Store.CreateTask(r.Context(), token, *q)
	case *TaskChangeRequest:
		data, err = s.Store.ChangeTask(r.Context(), token, action, *q)
	case *DependencyRequest:
		action := "add"
		if r.URL.Path == "/v1/dependency/remove" {
			action = "remove"
		}
		data, err = s.Store.ChangeDependency(r.Context(), token, action, *q)
	}
	if err != nil {
		writeError(w, err)
	} else if !snapshotConditional(w, r, data) {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}
