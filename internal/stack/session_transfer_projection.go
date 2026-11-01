package stack

import (
	"context"
	"database/sql"
	"encoding/json"
)

const transferFormat = 2

// The portable snapshot contains no runtime routes. Its digest also fences the
// source-only delivery state so a claim or hold cannot race the seal.
func transferProjection(ctx context.Context, tx *sql.Tx, session string) ([]TransferTable, string, error) {
	portable := []TransferTable{}
	full := []TransferTable{}
	for _, spec := range transferTables {
		columns, rows, err := transferRows(ctx, tx, spec, session)
		if err != nil {
			return nil, "", err
		}

		copyRows := make([][]any, len(rows))
		for i, row := range rows {
			copyRows[i] = append([]any(nil), row...)
		}

		full = append(full, TransferTable{Name: spec.name, Rows: rows})
		if spec.name == "agents" {
			for _, row := range copyRows {
				for j, col := range columns {
					switch col {
					case "binding":
						var binding RuntimeBinding

						encoded, _ := row[j].(string)
						if encoded == "" {
							encoded = "{}"
						}

						if err = json.Unmarshal([]byte(encoded), &binding); err != nil {
							return nil, "", err
						}

						value, _ := json.Marshal(RuntimeBinding{Machine: binding.Machine})
						row[j] = string(value)
					case "runtime_id":
						row[j] = nil
					case "runtime_state":
						row[j] = "unknown"
					case "observed_at":
						row[j] = ""
					}
				}
			}
		}

		portable = append(portable, TransferTable{Name: spec.name, Rows: copyRows})
	}

	for _, spec := range []transferTableSpec{
		{"notice_relay_leases", "session_id=?", "recipient_id"},
		{"notice_targets", "session_id=?", "recipient_id"},
		{"notice_batches", "session_id=?", "id"},
		{"notice_batch_refs", "batch_id IN (SELECT id FROM notice_batches WHERE session_id=?)", "batch_id,message_id,recipient_id"},
		{"notice_ack_refs", "batch_id IN (SELECT id FROM notice_batches WHERE session_id=?)", "batch_id,message_id,recipient_id"},
		{"runtime_claims", "agent_id IN (SELECT id FROM agents WHERE session_id=?)", "binding_key"},
	} {
		_, rows, err := transferRows(ctx, tx, spec, session)
		if err != nil {
			return nil, "", err
		}

		full = append(full, TransferTable{Name: spec.name, Rows: rows})
	}

	data, err := json.Marshal(full)
	if err != nil {
		return nil, "", err
	}

	return portable, tokenHash(string(data)), nil
}
