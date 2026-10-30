package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const migration5 = `
CREATE TABLE notice_batches(
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), recipient_id TEXT NOT NULL REFERENCES agents(id),
 binding_revision INTEGER NOT NULL, binding_hash TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','submitting','submitted','uncertain','suppressed')),
 reason TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
) WITHOUT ROWID;
CREATE UNIQUE INDEX notice_batches_inflight ON notice_batches(session_id,recipient_id) WHERE state='submitting';
CREATE TABLE notice_batch_refs(
 batch_id TEXT NOT NULL REFERENCES notice_batches(id), message_id TEXT NOT NULL, recipient_id TEXT NOT NULL,
 PRIMARY KEY(batch_id,message_id), FOREIGN KEY(message_id,recipient_id) REFERENCES mail_mentions(message_id,recipient_id)
) WITHOUT ROWID;
CREATE TABLE notice_targets(
 session_id TEXT NOT NULL REFERENCES sessions(id), recipient_id TEXT NOT NULL REFERENCES agents(id),
 reason TEXT NOT NULL, failures INTEGER NOT NULL, next_attempt INTEGER NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(session_id,recipient_id)
) WITHOUT ROWID;
`

// Recovery never retries a possibly submitted batch. Suppression is terminal;
// both recovery and completion update only the exact still-submitting refs.
func (s *Store) recoverNotices(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO notice_targets
 SELECT b.session_id,b.recipient_id,'recovery_uncertain',0,0,? FROM notice_batches b JOIN agents a ON a.id=b.recipient_id
 WHERE b.state='submitting' AND b.binding_revision=a.revision
 ON CONFLICT(session_id,recipient_id) DO UPDATE SET reason=excluded.reason,failures=0,next_attempt=0,updated_at=excluded.updated_at`, now())
	if err != nil {
		return storageError()
	}
	_, err = tx.ExecContext(ctx, `UPDATE mail_mentions SET notice_state='uncertain',state_at=? WHERE notice_state='submitting' AND EXISTS(
 SELECT 1 FROM notice_batch_refs r JOIN notice_batches b ON b.id=r.batch_id WHERE b.state='submitting' AND r.message_id=mail_mentions.message_id AND r.recipient_id=mail_mentions.recipient_id)`, now())
	if err != nil {
		return storageError()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mail_acks SET reminder_state='uncertain' WHERE reminder_state='submitting' AND EXISTS(SELECT 1 FROM notice_ack_refs r JOIN notice_batches b ON b.id=r.batch_id WHERE b.state='submitting' AND r.message_id=mail_acks.message_id AND r.recipient_id=mail_acks.recipient_id)`); err != nil {
		return storageError()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET read_revision=read_revision+1 WHERE id IN (SELECT session_id FROM notice_batches WHERE state='submitting')"); err != nil {
		return storageError()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE notice_batches SET state='uncertain',reason='recovery_uncertain',updated_at=? WHERE state='submitting'", now()); err != nil {
		return storageError()
	}
	if tx.Commit() != nil {
		return storageError()
	}
	return nil
}

type noticeTarget struct {
	agent, session, bindingJSON string
	revision                    int64
	binding                     RuntimeBinding
}
type noticeBatch struct {
	id     string
	target noticeTarget
}
type NoticeDrainResult struct {
	Checked   int  `json:"checked"`
	Submitted int  `json:"submitted"`
	Uncertain int  `json:"uncertain"`
	Pending   int  `json:"pending"`
	Busy      bool `json:"busy"`
}

// ponytail: one drain serializes recipients; add bounded workers only if measured queue delay warrants it.
type notifier struct {
	store   *Store
	adapter *herdrAdapter
	mu      sync.Mutex
	wake    chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	clock   func() time.Time
}

func newNotifier(s *Store, h *herdrAdapter) *notifier {
	return &notifier{store: s, adapter: h, wake: make(chan struct{}, 1), done: make(chan struct{}), clock: time.Now}
}
func (n *notifier) signal() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}
func (n *notifier) run() {
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	go func() {
		defer close(n.done)
		timer := time.NewTicker(5 * time.Second)
		defer timer.Stop()
		n.signal()
		for {
			select {
			case <-ctx.Done():
				return
			case <-n.wake:
			case <-timer.C:
			}
			result, err := n.drain(ctx, "", 4)
			if err == nil && result.Checked == 4 {
				n.signal()
			}
		}
	}()
}
func (n *notifier) stop() { n.cancel(); <-n.done }

// EnableNotifications is configured once, before Serve accepts clients.
func (s *Service) EnableNotifications(executable string) error {
	return s.enableNotifications(newHerdr(executable))
}
func (s *Service) enableNotifications(h *herdrAdapter) error {
	if s.notifier != nil {
		return failure("conflict", "notifications already configured")
	}
	// A new binary can support a harness that the last one parked.
	if _, err := s.Store.db.Exec("UPDATE notice_targets SET next_attempt=0 WHERE next_attempt<0"); err != nil {
		return storageError()
	}
	s.notifier = newNotifier(s.Store, h)
	s.Store.noticeWake = s.notifier.signal
	s.notifier.run()
	return nil
}
func (n *notifier) nextTarget(ctx context.Context, session string) (noticeTarget, error) {
	var t noticeTarget
	err := n.store.db.QueryRowContext(ctx, `SELECT a.id,a.session_id,a.revision,COALESCE(a.binding,'') FROM agents a JOIN sessions s ON s.id=a.session_id
 LEFT JOIN notice_targets d ON d.session_id=a.session_id AND d.recipient_id=a.id
 WHERE s.closed=0 AND COALESCE(json_extract(a.binding,'$.notices'),CASE WHEN s.orchestrator_id=a.id THEN 'hook' ELSE 'pane' END)='pane'
 AND (?='' OR s.id=?) AND (COALESCE(d.next_attempt,0) BETWEEN 0 AND ?
 OR (d.reason='notice_waiting_idle' AND a.runtime_state IN ('idle','done')))
 AND NOT EXISTS(SELECT 1 FROM notice_batches b WHERE b.session_id=s.id AND b.recipient_id=a.id AND b.state='submitting')
 AND (EXISTS(SELECT 1 FROM mail_mentions m JOIN task_assignees x ON x.task_id=m.task_id AND x.agent_id=m.recipient_id
 WHERE m.recipient_id=a.id AND x.session_id=s.id AND m.notice_state='pending')
 OR EXISTS(SELECT 1 FROM mail_acks k JOIN task_assignees x ON x.task_id=k.task_id AND x.agent_id=k.recipient_id WHERE k.recipient_id=a.id AND x.session_id=s.id AND k.reminder_state='pending' AND k.acked_at='' AND k.withdrawn_at='' AND k.due_at<>'' AND julianday(k.due_at)<=julianday(?)))
 ORDER BY COALESCE(d.updated_at,''),a.id LIMIT 1`, session, session, n.clock().UnixMilli(), n.clock().UTC().Format(time.RFC3339Nano)).Scan(&t.agent, &t.session, &t.revision, &t.bindingJSON)
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal([]byte(t.bindingJSON), &t.binding)
	return t, nil
}
func currentNoticeTarget(ctx context.Context, tx *sql.Tx, t noticeTarget) (bool, bool, error) {
	var closed bool
	var revision int64
	var binding string
	err := tx.QueryRowContext(ctx, "SELECT s.closed,a.revision,COALESCE(a.binding,'') FROM agents a JOIN sessions s ON s.id=a.session_id WHERE a.id=? AND s.id=?", t.agent, t.session).Scan(&closed, &revision, &binding)
	return !closed, revision == t.revision && binding == t.bindingJSON, err
}

// noticeParked is next_attempt for a target that only a new binding or a
// service start can fix: no timer retries it. BindAgent and enableNotifications
// set next_attempt back to 0.
const noticeParked = -1

// Pane input is rate limited even when the Agent reads its mail immediately.
const noticeCooldown = 10 * time.Minute

func recordNoticeTarget(ctx context.Context, tx *sql.Tx, t noticeTarget, reason string, success bool, clock time.Time) error {
	_, same, err := currentNoticeTarget(ctx, tx, t)
	if err != nil || !same {
		return err
	}
	failures := 0
	var previous string
	err = tx.QueryRowContext(ctx, "SELECT failures,reason FROM notice_targets WHERE session_id=? AND recipient_id=?", t.session, t.agent).Scan(&failures, &previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if success {
		failures = 0
	} else {
		if failures < 6 {
			failures++
		}
	}
	next := int64(0)
	if reason == "binding_incomplete" || reason == "unsupported_harness" {
		// These depend only on the stored binding and this binary's table, so
		// nextTarget skips the row until it changes: no idle retry writes.
		next = noticeParked
	} else if reason == "notice_waiting_mail" || reason == "notice_waiting_idle" {
		next = clock.Add(noticeCooldown).UnixMilli()
	} else if !success {
		seconds := 5 << min(failures-1, 3)
		next = clock.Add(time.Duration(min(seconds, 30)) * time.Second).UnixMilli()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notice_targets VALUES(?,?,?,?,?,?) ON CONFLICT(session_id,recipient_id) DO UPDATE SET reason=excluded.reason,failures=excluded.failures,next_attempt=excluded.next_attempt,updated_at=excluded.updated_at`, t.session, t.agent, reason, failures, next, clock.UTC().Format(time.RFC3339Nano))
	if err == nil && previous != reason && (noticeWarning(previous) != nil || noticeWarning(reason) != nil) {
		err = bumpSessionRead(ctx, tx, t.session)
	}
	return err
}
func (n *notifier) unavailable(ctx context.Context, t noticeTarget, reason string) error {
	tx, err := n.store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	if err = recordNoticeTarget(ctx, tx, t, reason, false, n.clock()); err != nil {
		return storageError()
	}
	if tx.Commit() != nil {
		return storageError()
	}
	return nil
}
func (n *notifier) claim(ctx context.Context, t noticeTarget) (noticeBatch, error) {
	empty := noticeBatch{}
	tx, err := n.store.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, storageError()
	}
	defer tx.Rollback()
	open, same, err := currentNoticeTarget(ctx, tx, t)
	if err != nil {
		return empty, storageError()
	}
	if !open || !same {
		return empty, nil
	}
	var reason, state, observed string
	var next int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(d.reason,''),COALESCE(d.next_attempt,0),a.runtime_state,a.observed_at FROM agents a
 LEFT JOIN notice_targets d ON d.session_id=a.session_id AND d.recipient_id=a.id WHERE a.id=?`, t.agent).Scan(&reason, &next, &state, &observed); err != nil {
		return empty, storageError()
	}
	if (reason == "notice_waiting_mail" || reason == "mail_checked") && next > n.clock().UnixMilli() {
		return empty, nil
	}
	// Hook observations are sent only by an enrolled runtime. Unknown state
	// keeps the ordinary first-notice behavior; Herdr still verifies identity.
	if state == "working" && observed != "" && reason != "notice_waiting_mail" {
		if reason != "notice_waiting_idle" {
			if err = recordNoticeTarget(ctx, tx, t, "notice_waiting_idle", true, n.clock()); err != nil || tx.Commit() != nil {
				return empty, storageError()
			}
			return empty, nil
		}
		if next > n.clock().UnixMilli() {
			return empty, nil
		}
	}
	ids, err := readIDs(ctx, tx, `SELECT m.message_id FROM mail_mentions m JOIN task_assignees x ON x.task_id=m.task_id AND x.agent_id=m.recipient_id
 WHERE x.session_id=? AND m.recipient_id=? AND m.notice_state='pending' ORDER BY m.message_id LIMIT 500`, t.session, t.agent)
	if err != nil {
		return empty, err
	}
	acks, err := readIDs(ctx, tx, `SELECT k.message_id FROM mail_acks k JOIN task_assignees x ON x.task_id=k.task_id AND x.agent_id=k.recipient_id WHERE x.session_id=? AND k.recipient_id=? AND k.reminder_state='pending' AND k.acked_at='' AND k.withdrawn_at='' AND k.due_at<>'' AND julianday(k.due_at)<=julianday(?) ORDER BY k.message_id LIMIT ?`, t.session, t.agent, n.clock().UTC().Format(time.RFC3339Nano), 500-len(ids))
	if err != nil {
		return empty, err
	}
	if len(ids)+len(acks) == 0 {
		return empty, nil
	}
	batch := noticeBatch{id: opaqueID(), target: t}
	stamp := now()
	if _, err = tx.ExecContext(ctx, "INSERT INTO notice_batches VALUES(?,?,?,?,?,'submitting','captured',?,?)", batch.id, t.session, t.agent, t.revision, tokenHash(t.bindingJSON), stamp, stamp); err != nil {
		return empty, storageError()
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, "INSERT INTO notice_batch_refs VALUES(?,?,?)", batch.id, id, t.agent); err != nil {
			return empty, storageError()
		}
		if _, err = tx.ExecContext(ctx, "UPDATE mail_mentions SET notice_state='submitting',state_at=? WHERE message_id=? AND recipient_id=? AND notice_state='pending'", stamp, id, t.agent); err != nil {
			return empty, storageError()
		}
	}
	for _, id := range acks {
		if _, err = tx.ExecContext(ctx, "INSERT INTO notice_ack_refs VALUES(?,?,?)", batch.id, id, t.agent); err != nil {
			return empty, storageError()
		}
		if _, err = tx.ExecContext(ctx, "UPDATE mail_acks SET reminder_state='submitting' WHERE message_id=? AND recipient_id=? AND reminder_state='pending'", id, t.agent); err != nil {
			return empty, storageError()
		}
	}
	if tx.Commit() != nil {
		return empty, storageError()
	}
	return batch, nil
}
func (n *notifier) preflight(ctx context.Context, b noticeBatch) (NoticeState, string, error) {
	tx, err := n.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", storageError()
	}
	defer tx.Rollback()
	open, same, err := currentNoticeTarget(ctx, tx, b.target)
	if err != nil {
		return "", "", storageError()
	}
	if !same {
		return NoticePending, "binding_changed", nil
	}
	if !open {
		return NoticePending, "session_closed", nil
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM notice_batch_refs r JOIN mail_mentions m ON m.message_id=r.message_id AND m.recipient_id=r.recipient_id
 JOIN task_assignees x ON x.task_id=m.task_id AND x.agent_id=m.recipient_id WHERE r.batch_id=? AND m.notice_state='submitting' AND x.session_id=?`, b.id, b.target.session).Scan(&count)
	if err != nil {
		return "", "", storageError()
	}
	var ackCount int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM notice_ack_refs r JOIN mail_acks k ON k.message_id=r.message_id AND k.recipient_id=r.recipient_id JOIN task_assignees x ON x.task_id=k.task_id AND x.agent_id=k.recipient_id WHERE r.batch_id=? AND k.reminder_state='submitting' AND k.acked_at='' AND k.withdrawn_at='' AND x.session_id=?`, b.id, b.target.session).Scan(&ackCount)
	if err != nil {
		return "", "", storageError()
	}
	if count+ackCount == 0 {
		return NoticeSuppressed, "membership_removed", nil
	}
	return "", "", nil
}
func (n *notifier) finish(b noticeBatch, state NoticeState, reason string) error {
	// Complete a known outcome even if the HTTP caller disconnected or shutdown
	// cancelled the child. A crash before this commit is conservatively uncertain.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := n.store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	var old string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM notice_batches WHERE id=?", b.id).Scan(&old); err != nil {
		return storageError()
	}
	if old != "submitting" {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE mail_mentions SET notice_state=?,state_at=? WHERE notice_state='submitting' AND EXISTS(
 SELECT 1 FROM notice_batch_refs r WHERE r.batch_id=? AND r.message_id=mail_mentions.message_id AND r.recipient_id=mail_mentions.recipient_id)`, state, now(), b.id)
	if err != nil {
		return storageError()
	}
	count, err := result.RowsAffected()
	if err != nil {
		return storageError()
	}
	ackResult, err := tx.ExecContext(ctx, `UPDATE mail_acks SET reminder_state=? WHERE reminder_state='submitting' AND EXISTS(SELECT 1 FROM notice_ack_refs r WHERE r.batch_id=? AND r.message_id=mail_acks.message_id AND r.recipient_id=mail_acks.recipient_id)`, state, b.id)
	if err != nil {
		return storageError()
	}
	ackCount, err := ackResult.RowsAffected()
	if err != nil {
		return storageError()
	}
	count += ackCount
	if state == NoticeUncertain && count > 0 {
		if err = bumpSessionRead(ctx, tx, b.target.session); err != nil {
			return storageError()
		}
	}
	batchState := state
	if count == 0 {
		batchState = NoticeSuppressed
	}
	if _, err = tx.ExecContext(ctx, "UPDATE notice_batches SET state=?,reason=?,updated_at=? WHERE id=? AND state='submitting'", batchState, reason, now(), b.id); err != nil {
		return storageError()
	}
	if batchState == NoticeSubmitted {
		reason = "notice_waiting_mail"
	}
	if err = recordNoticeTarget(ctx, tx, b.target, reason, state != NoticePending || count == 0, n.clock()); err != nil {
		return storageError()
	}
	if tx.Commit() != nil {
		return storageError()
	}
	return nil
}
func (n *notifier) drain(ctx context.Context, session string, limit int) (NoticeDrainResult, error) {
	result := NoticeDrainResult{}
	if !n.mu.TryLock() {
		result.Busy = true
		return result, nil
	}
	defer n.mu.Unlock()
	// No external command owns a batch under this lock now. Recover any outcome
	// whose final storage commit failed on an earlier pass without resending it.
	if err := n.store.recoverNotices(ctx); err != nil {
		return result, err
	}
	for i := 0; i < limit; i++ {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		t, err := n.nextTarget(ctx, session)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return result, storageError()
		}
		result.Checked++
		if reason := n.adapter.verify(ctx, t.binding); reason != "" {
			if err = n.unavailable(ctx, t, reason); err != nil {
				return result, err
			}
			result.Pending++
			continue
		}
		batch, err := n.claim(ctx, t)
		if err != nil {
			return result, err
		}
		if batch.id == "" {
			continue
		}
		state, reason, err := n.preflight(ctx, batch)
		if err != nil {
			_ = n.finish(batch, NoticePending, "preinput_failure")
			return result, err
		}
		if state == "" {
			state, reason = n.adapter.submit(ctx, t.binding)
		}
		if err = n.finish(batch, state, reason); err != nil {
			return result, err
		}
		switch state {
		case NoticeSubmitted:
			result.Submitted++
		case NoticeUncertain:
			result.Uncertain++
		case NoticePending:
			result.Pending++
		}
	}
	return result, nil
}
