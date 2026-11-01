package stack

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) expireRelayLeases(ctx context.Context, clock time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, storageError(err)
	}
	defer tx.Rollback()
	ids, err := readIDs(ctx, tx, "SELECT notice_id FROM notice_relay_leases WHERE expires_at<=? ORDER BY expires_at LIMIT 100", clock.UnixMilli())
	if err != nil {
		return 0, storageError(err)
	}
	orphans, err := readIDs(ctx, tx, `SELECT b.id FROM notice_batches b WHERE b.delivery_owner='relay' AND b.state='submitting' AND NOT EXISTS(SELECT 1 FROM notice_relay_leases l WHERE l.notice_id=b.id) LIMIT ?`, 100-len(ids))
	if err != nil {
		return 0, storageError(err)
	}
	ids = append(ids, orphans...)
	n := &notifier{store: s, clock: func() time.Time { return clock }}
	for _, id := range ids {
		if err = retireRelayLease(ctx, tx, n, id); err != nil {
			return 0, err
		}
	}
	return len(ids), storageOrNil(tx.Commit())
}

func retireAgentRelay(ctx context.Context, tx *sql.Tx, agent string) error {
	ids, err := readIDs(ctx, tx, `SELECT notice_id FROM notice_relay_leases WHERE recipient_id=? UNION SELECT id FROM notice_batches WHERE recipient_id=? AND delivery_owner='relay' AND state='submitting'`, agent, agent)
	if err != nil {
		return storageError(err)
	}
	n := &notifier{clock: clockNow}
	for _, id := range ids {
		if err = retireRelayLease(ctx, tx, n, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) startRelayExpiry() {
	ctx, cancel := context.WithCancel(context.Background())
	s.relayCancel = cancel
	s.relayDone = make(chan struct{})
	go func() {
		defer close(s.relayDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Finish the bounded transaction before shutdown releases database ownership.
				_, _ = s.Store.expireRelayLeases(context.WithoutCancel(ctx), clockNow())
			}
		}
	}()
}

// Run before admitting requests, without a Herdr adapter. Terminal outcomes stay
// terminal; even an orphaned relay batch is never handed to local recovery.
func (s *Store) recoverRelayStartup(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	ids, err := readIDs(ctx, tx, `SELECT notice_id FROM notice_relay_leases UNION SELECT id FROM notice_batches WHERE delivery_owner='relay' AND state='submitting'`)
	if err != nil {
		return storageError(err)
	}
	n := &notifier{store: s, clock: clockNow}
	for _, id := range ids {
		if err = retireRelayLease(ctx, tx, n, id); err != nil {
			return err
		}
	}
	return storageOrNil(tx.Commit())
}
