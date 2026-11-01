package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const relayLeaseDuration = time.Minute

type RelayRequest struct {
	Context
	Generation int64  `json:"generation"`
	RuntimeID  string `json:"runtime_id"`
	Proof      string `json:"proof"`
	NoticeID   string `json:"notice_id,omitempty"`
	LeaseToken string `json:"lease_token,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
type RelayGrant struct {
	NoticeID   string `json:"notice_id,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	LeaseToken string `json:"lease_token,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	ServerTime int64  `json:"server_time,omitempty"`
}

func (s *Service) relayHTTP(w http.ResponseWriter, r *http.Request, token string, p Principal) bool {
	if r.URL.Path != "/v1/relay/claim" && r.URL.Path != "/v1/relay/begin" && r.URL.Path != "/v1/relay/result" {
		return false
	}
	if p.Kind != AgentCapability {
		writeError(w, failure("forbidden", "relay requires the bound Agent credential"))
		return true
	}
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return true
	}
	if s.relayInFlight.Add(1) > 4 {
		s.relayInFlight.Add(-1)
		writeError(w, &Error{Code: "rate_limited", Message: "relay busy", Retryable: true, RetryAfter: 5})
		return true
	}
	defer s.relayInFlight.Add(-1)
	// Limit before decoding independently of the general API request limit.
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var q RelayRequest
	if err := requestJSON(w, r, &q); err != nil {
		writeError(w, err)
		return true
	}
	data, err := s.Store.relay(context.WithoutCancel(r.Context()), token, strings.TrimPrefix(r.URL.Path, "/v1/relay/"), s.Discovery.InstanceID, q, clockNow())
	if err != nil {
		writeError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}

func (s *Store) relay(ctx context.Context, token, action, instance string, q RelayRequest, clock time.Time) (any, error) {
	empty := RelayGrant{}
	tx, p, session, err := s.readSessionTx(ctx, token, q.Context)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if p.Kind != AgentCapability {
		return empty, failure("forbidden", "relay requires Agent authority")
	}
	if err = sessionTransferGuard(ctx, tx, session.ID); err != nil {
		return empty, err
	}
	t := noticeTarget{agent: p.SubjectID, session: session.ID}
	var runtime string
	if err = tx.QueryRowContext(ctx, "SELECT revision,COALESCE(runtime_id,''),COALESCE(binding,'{}') FROM agents WHERE id=? AND session_id=?", p.SubjectID, session.ID).Scan(&t.revision, &runtime, &t.bindingJSON); err != nil {
		return empty, failure("forbidden", "Agent not in Session")
	}
	if json.Unmarshal([]byte(t.bindingJSON), &t.binding) != nil || !validToken(q.Proof) || t.binding.RelayMachine == "" || tokenHash(q.Proof) != t.binding.RelayKeyHash || q.Generation != t.revision || q.RuntimeID != runtime || runtime == "" {
		return empty, failure("forbidden", "relay binding proof or generation differs")
	}
	if action != "result" && session.Closed {
		return empty, failure("session_closed", "Session is closed")
	}
	n := &notifier{store: s, clock: func() time.Time { return clock }}
	var id, hash, bindingHash, leaseInstance string
	var expiry, revision int64
	err = tx.QueryRowContext(ctx, "SELECT notice_id,lease_hash,binding_revision,binding_hash,expires_at,instance_id FROM notice_relay_leases WHERE session_id=? AND recipient_id=?", t.session, t.agent).Scan(&id, &hash, &revision, &bindingHash, &expiry, &leaseInstance)
	if err != nil && err != sql.ErrNoRows {
		return empty, storageError(err)
	}
	if id != "" && (expiry <= clock.UnixMilli() || revision != t.revision || bindingHash != tokenHash(t.bindingJSON) || leaseInstance != instance) {
		if err = retireRelayLease(ctx, tx, n, id); err != nil {
			return empty, err
		}
		id = ""
	}
	commit := func(v any) (any, error) { return v, storageOrNil(tx.Commit()) }
	if action == "claim" {
		if q.NoticeID != "" || q.LeaseToken != "" || q.Outcome != "" || q.Reason != "" {
			return empty, failure("invalid_input", "claim accepts only binding identity")
		}
		if id != "" || t.binding.Notices != "pane" {
			return commit(empty)
		}
		var busy bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM notice_batches WHERE recipient_id=? AND state='submitting')", t.agent).Scan(&busy); err != nil {
			return empty, storageError(err)
		}
		if busy {
			return commit(empty)
		}
		eligible, e := n.capture(ctx, tx, t, "", "relay")
		if e != nil {
			return empty, e
		}
		if eligible.id == "" {
			return commit(empty)
		}
		id, secret := opaqueID(), opaqueID()
		expiry = clock.Add(relayLeaseDuration).UnixMilli()
		_, err = tx.ExecContext(ctx, "INSERT INTO notice_relay_leases VALUES(?,?,?,?,?,?,?,?)", t.session, t.agent, id, t.revision, tokenHash(t.bindingJSON), tokenHash(secret), expiry, instance)
		if err != nil {
			return empty, storageError(err)
		}
		return commit(RelayGrant{NoticeID: id, Generation: t.revision, LeaseToken: secret, ExpiresAt: expiry, ServerTime: clock.UnixMilli()})
	}
	if id == "" || !validToken(q.NoticeID) || !validToken(q.LeaseToken) || id != q.NoticeID || hash != tokenHash(q.LeaseToken) {
		if err = tx.Commit(); err != nil {
			return empty, storageError(err)
		}
		return empty, failure("conflict", "relay lease expired or differs")
	}
	var state, reason string
	err = tx.QueryRowContext(ctx, "SELECT state,reason FROM notice_batches WHERE id=? AND delivery_owner='relay'", id).Scan(&state, &reason)
	if err != nil && err != sql.ErrNoRows {
		return empty, storageError(err)
	}
	if action == "begin" {
		if q.Outcome != "" || q.Reason != "" {
			return empty, failure("invalid_input", "begin accepts only lease identity")
		}
		if state != "" {
			return empty, failure("conflict", "relay begin was already consumed")
		}
		if t.binding.Notices != "pane" {
			return empty, failure("forbidden", "pane notices not enabled")
		}
		batch, e := n.capture(ctx, tx, t, id, "relay")
		if e != nil {
			return empty, e
		}
		if batch.id == "" {
			return empty, failure("conflict", "notice no longer eligible")
		}
		if batch.extra {
			if _, err = tx.ExecContext(ctx, "UPDATE mail_mentions SET extra_consumed=1 WHERE (message_id,recipient_id) IN (SELECT message_id,recipient_id FROM notice_batch_refs WHERE batch_id=?)", id); err != nil {
				return empty, storageError(err)
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE agents SET pane_not_before=max(pane_not_before,?) WHERE id=?", expiry+noticeCooldown.Milliseconds(), t.agent); err != nil {
			return empty, storageError(err)
		}
		return commit(RelayGrant{NoticeID: id, Generation: t.revision, ExpiresAt: expiry, ServerTime: clock.UnixMilli()})
	}
	if action != "result" {
		return empty, failure("invalid_input", "unknown relay action")
	}
	if q.Outcome == "deferred" {
		if state != "" {
			return empty, failure("conflict", "begun relay work cannot return to pending")
		}
		switch q.Reason {
		case "blocked", "unknown", "stopped", "unsupported", "identity_changed", "unavailable":
		default:
			return empty, failure("invalid_input", "invalid relay diagnostic")
		}
		if err = recordNoticeTarget(ctx, tx, t, "relay_"+q.Reason, false, clock); err != nil {
			return empty, storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM notice_relay_leases WHERE notice_id=?", id); err != nil {
			return empty, storageError(err)
		}
		return commit(map[string]string{"outcome": "deferred"})
	}
	if q.Outcome != "submitted" && q.Outcome != "uncertain" || q.Reason != "" {
		return empty, failure("invalid_input", "relay result must be submitted or uncertain")
	}
	if state == "" {
		return empty, failure("conflict", "relay work has not begun")
	}
	if state != "submitting" {
		if reason != "relay_"+q.Outcome {
			return empty, failure("conflict", "relay outcome already recorded")
		}
		return map[string]string{"outcome": q.Outcome}, nil
	}
	if err = n.finishTx(ctx, tx, noticeBatch{id: id, target: t}, NoticeState(q.Outcome), "relay_"+q.Outcome); err != nil {
		return empty, err
	}
	return commit(map[string]string{"outcome": q.Outcome})
}

func retireRelayLease(ctx context.Context, tx *sql.Tx, n *notifier, id string) error {
	var t noticeTarget
	var state string
	err := tx.QueryRowContext(ctx, "SELECT recipient_id,session_id,binding_revision,state FROM notice_batches WHERE id=? AND delivery_owner='relay'", id).Scan(&t.agent, &t.session, &t.revision, &state)
	if err != nil && err != sql.ErrNoRows {
		return storageError(err)
	}
	if state == "submitting" {
		// No stale diagnostic is installed: bindingJSON is deliberately unset.
		if err = n.finishTx(ctx, tx, noticeBatch{id: id, target: t}, NoticeUncertain, "recovery_uncertain"); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM notice_relay_leases WHERE notice_id=?", id)
	return storageOrNil(err)
}
