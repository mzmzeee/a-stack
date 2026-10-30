package stack

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Validate the whole snapshot before writing. Foreign keys alone can resolve
// to unrelated destination rows; imported references must stay in this Session.
func validateTransferSnapshot(ctx context.Context, tx *sql.Tx, b TransferBundle) error {
	bad := func() error {
		return failure("invalid_input", "transfer snapshot contains an invalid or out-of-Session reference")
	}
	columns := make([][]string, len(transferTables))
	ids := map[string]map[string]string{}
	for i, spec := range transferTables {
		if b.Tables[i].Name != spec.name {
			return bad()
		}
		rows, err := tx.QueryContext(ctx, "SELECT * FROM "+spec.name+" LIMIT 0")
		if err != nil {
			return storageError(err)
		}
		columns[i], err = rows.Columns()
		rows.Close()
		if err != nil {
			return storageError(err)
		}
		ids[spec.name] = map[string]string{}
		for _, row := range b.Tables[i].Rows {
			if len(row) != len(columns[i]) {
				return bad()
			}
			id, task := "", ""
			for j, col := range columns[i] {
				if col == "id" {
					var ok bool
					id, ok = row[j].(string)
					if !ok || !validToken(id) {
						return bad()
					}
				}
				if col == "task_id" {
					task, _ = row[j].(string)
				}
			}
			if id != "" {
				if _, exists := ids[spec.name][id]; exists {
					return bad()
				}
				ids[spec.name][id] = task
			}
		}
	}
	if len(b.Tables[0].Rows) != 1 {
		return bad()
	}
	contains := func(table string, value any) bool {
		if value == nil {
			return true
		}
		id, ok := value.(string)
		if !ok {
			return false
		}
		_, ok = ids[table][id]
		return ok
	}
	for i, spec := range transferTables {
		for _, row := range b.Tables[i].Rows {
			values := map[string]any{}
			for j, col := range columns[i] {
				values[col] = row[j]
			}
			for col, value := range values {
				switch col {
				case "workspace_id":
					if value != b.Session.WorkspaceID {
						return bad()
					}
				case "session_id":
					if value != b.Session.ID {
						return bad()
					}
				case "scope":
					if spec.name == "operation_receipts" && value != b.Session.ID {
						return bad()
					}
				case "task_id", "prerequisite_id", "dependent_id":
					if value == nil || !contains("tasks", value) {
						return bad()
					}
				case "agent_id", "sender_id", "recipient_id", "asker_id", "resolver_id", "created_by", "updated_by", "added_by", "removed_by", "claimed_by", "orchestrator_id":
					if !contains("agents", value) {
						return bad()
					}
				case "actor_id":
					if values["actor_kind"] != "operator" && !contains("agents", value) {
						return bad()
					}
				case "actor":
					actor, ok := value.(string)
					if !ok {
						return bad()
					}
					if strings.HasPrefix(actor, "agent:") {
						if !contains("agents", strings.TrimPrefix(actor, "agent:")) {
							return bad()
						}
					} else {
						kind, id, ok := strings.Cut(actor, ":")
						if spec.name == "expired_operations" || !ok || (kind != "operator" && kind != "browser") || !validToken(id) {
							return bad()
						}
					}
				case "description_ref", "report_ref", "reason_ref", "result_ref":
					if value != nil {
						id, ok := value.(string)
						if !ok || !contains("task_content", value) || ids["task_content"][id] != values["id"] {
							return bad()
						}
					}
				case "message_id":
					id, ok := value.(string)
					if !ok || !contains("mail_messages", value) || ids["mail_messages"][id] != values["task_id"] {
						return bad()
					}
				}
			}
			if spec.name == "sessions" {
				if values["id"] != b.Session.ID || fmt.Sprint(values["read_revision"]) != fmt.Sprint(b.Session.ReadRevision) || fmt.Sprint(values["revision"]) != fmt.Sprint(b.Session.Revision) {
					return bad()
				}
				closed := "0"
				if b.Session.Closed {
					closed = "1"
				}
				if fmt.Sprint(values["closed"]) != closed {
					return bad()
				}
			}
		}
	}
	return nil
}
