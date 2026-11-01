package stack

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const undoMigration16 = `DROP TABLE notice_relay_leases;
ALTER TABLE notice_batches DROP COLUMN delivery_owner;
ALTER TABLE mail_mentions DROP COLUMN extra_consumed;
ALTER TABLE agents DROP COLUMN pane_not_before;
DROP INDEX agents_runtime;
CREATE UNIQUE INDEX agents_runtime ON agents(runtime_id) WHERE runtime_id IS NOT NULL;
PRAGMA user_version=15;`

func TestSchema16PreservesHistoricalBudgetAndHold(t *testing.T) {
	for _, reason := range []string{"notice_waiting_mail", "mail_checked"} {
		t.Run(reason, func(t *testing.T) {
			f := noticeTestFixture(t)
			f.send(t, f.a.Token, "Old submitted @03-worker")
			f.drain(t, 4)

			claims := countTest(t, f.service.Store, "SELECT count(*) FROM runtime_claims")
			if claims == 0 {
				t.Fatal("fixture needs runtime claims")
			}

			path := f.service.Store.path
			deadline := time.Now().Add(10 * time.Minute).UnixMilli()

			if err := f.service.Close(ctx); err != nil {
				t.Fatal(err)
			}

			if schemaVersion > 15 {
				execFile(t, path, undoMigration16)
			}

			execFile(t, path, "DELETE FROM runtime_claims;")
			// A schema-15 transferred notice followed by its extra has exactly one
			// retained batch, indistinguishable from first delivery. Consume conservatively.
			execFile(t, path, fmt.Sprintf("UPDATE notice_targets SET reason='%s',next_attempt=%d;", reason, deadline))

			s, err := openStore(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.db.Close()

			if countTest(t, s, "SELECT count(*) FROM runtime_claims") != claims {
				t.Fatal("runtime claims not rebuilt")
			}

			if countTest(t, s, "PRAGMA user_version") != 16 {
				t.Fatal("schema 16 required")
			}

			if countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted' AND extra_consumed=1") != 1 {
				t.Fatal("historical extra allowance regenerated")
			}

			if countTest(t, s, fmt.Sprintf("SELECT count(*) FROM agents WHERE pane_not_before=%d", deadline)) != 1 {
				t.Fatal("historical hold lost")
			}

			if !strings.Contains(s.Migrated, "from schema 15 to 16") {
				t.Fatal(s.Migrated)
			}

			if !copyOf(ctx, path+".pre-v15", 15, s.id) {
				t.Fatal("missing pre-migration copy")
			}

			if countTest(t, s, "SELECT count(*) FROM pragma_foreign_key_check") != 0 {
				t.Fatal("broken references")
			}
		})
	}
}
