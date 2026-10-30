package stack

import (
	"context"
	"time"
)

const (
	ReceiptAge            = 30 * 24 * time.Hour
	receiptExpiryBatch    = 200
	receiptExpiryInterval = 24 * time.Hour
	receiptExpiryPause    = 10 * time.Millisecond
)

func expiredOperation() error {
	return failure("operation_expired", "operation ID is older than 30 days; its result has expired and the change was not applied again; inspect the Task or retained history before making any new change")
}

// expireReceiptBatch replaces at most 200 full receipts with permanent compact
// tombstones in one transaction. A crash can leave neither an unguarded ID nor
// a tombstone for an operation whose receipt was not removed.
func (s *Store) expireReceiptBatch(ctx context.Context, at time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, storageError(err)
	}
	defer tx.Rollback()
	cutoff := at.Add(-ReceiptAge)
	// RFC3339Nano omits the fractional part at whole seconds. Use an indexed
	// upper bound, then parse candidates so those strings expire accurately too.
	upper := cutoff.UTC().Truncate(time.Second).Add(time.Second).Format("2006-01-02T15:04:05")

	rows, err := tx.QueryContext(ctx, "SELECT actor,operation_id,outcome,received_at FROM operation_receipts WHERE received_at<? ORDER BY received_at,actor,operation_id LIMIT ?", upper, receiptExpiryBatch)
	if err != nil {
		return 0, storageError(err)
	}

	type expired struct{ actor, id, outcome string }
	items := []expired{}
	for rows.Next() {
		var item expired
		var received string
		if err = rows.Scan(&item.actor, &item.id, &item.outcome, &received); err != nil {
			rows.Close()
			return 0, storageError(err)
		}

		timestamp, parseErr := time.Parse(time.RFC3339Nano, received)
		if parseErr != nil {
			rows.Close()
			return 0, storageError(parseErr)
		}

		if !timestamp.After(cutoff) {
			items = append(items, item)
		}
	}

	err = rows.Err()
	rows.Close()

	if err != nil {
		return 0, storageError(err)
	}

	for _, item := range items {
		if _, err = tx.ExecContext(ctx, "INSERT INTO expired_operations(actor,operation_id,outcome) VALUES(?,?,?)", item.actor, item.id, item.outcome); err != nil {
			return 0, storageError(err)
		}

		if _, err = tx.ExecContext(ctx, "DELETE FROM operation_receipts WHERE actor=? AND operation_id=?", item.actor, item.id); err != nil {
			return 0, storageError(err)
		}
	}

	if err = tx.Commit(); err != nil {
		return 0, storageError(err)
	}

	return len(items), nil
}

func (s *Service) startReceiptExpiry() {
	ctx, cancel := context.WithCancel(context.Background())
	s.receiptCancel = cancel
	s.receiptDone = make(chan struct{})
	s.Store.nextReceiptExpiry.Store(clockNow().Unix())
	go func() {
		defer close(s.receiptDone)
		s.Store.runReceiptExpiry(ctx, receiptExpiryInterval, receiptExpiryPause)
	}()
}

// The first pass runs at start, then once a day. Yield the single database
// connection between small batches so regular reads and changes can proceed.
func (s *Store) runReceiptExpiry(ctx context.Context, interval, pause time.Duration) {
	for {
		for {
			if ctx.Err() != nil {
				return
			}

			// Finish this bounded transaction before honoring shutdown, rather
			// than leaving database/sql's automatic rollback running behind us.
			count, err := s.expireReceiptBatch(context.WithoutCancel(ctx), clockNow())
			if err != nil || count < receiptExpiryBatch {
				break
			}

			timer := time.NewTimer(pause)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		s.nextReceiptExpiry.Store(clockNow().Add(interval).Unix())

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
