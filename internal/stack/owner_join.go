package stack

import (
	"context"
	"database/sql"
	"time"
)

// CreateOwnerCode stores only a hash. The Operator generates and displays the code locally.
func (s *Store) CreateOwnerCode(ctx context.Context, token, hash string) error {
	if !validHash(hash) {
		return failure("invalid_input", "code hash required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()

	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return err
	}

	if err = operator(p); err != nil {
		return err
	}

	// Preserve the requested hash so retrying issuance cannot revive a retired code.
	if _, err = tx.ExecContext(ctx, "DELETE FROM owner_join_codes WHERE code_hash<>? AND (expires_at<=? OR consumed_hash<>'')", hash, now()); err != nil {
		return storageError(err)
	}

	if _, err = tx.ExecContext(ctx, "INSERT INTO owner_join_codes(code_hash,expires_at) VALUES(?,?) ON CONFLICT DO NOTHING", hash, clockNow().UTC().Add(24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		return storageError(err)
	}

	return storageOrNil(tx.Commit())
}

func (s *Store) JoinOwner(ctx context.Context, r SessionJoinRequest) error {
	actor, ok := ActorFromContext(ctx)
	if !ok || actor.Login == "" || r.Code == "" {
		return failure("forbidden", "trusted Tailscale identity and owner code required")
	}

	if r.SessionID != "" || !validHash(r.CredentialHash) {
		return failure("invalid_input", "owner credential hash required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	var consumed, login string

	err = tx.QueryRowContext(ctx, "SELECT consumed_hash,login FROM owner_join_codes WHERE code_hash=? AND expires_at>?", tokenHash(r.Code), now()).Scan(&consumed, &login)
	if err != nil && err != sql.ErrNoRows {
		return storageError(err)
	}

	if err == sql.ErrNoRows || (consumed != "" && (consumed != r.CredentialHash || login != actor.Login)) {
		return failure("forbidden", "owner code invalid, expired or used")
	}

	if consumed != "" {
		var existing bool

		err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM capabilities WHERE token_hash=? AND kind='operator' AND session_id='' AND token_hash NOT IN (SELECT token_hash FROM session_operators))", r.CredentialHash).Scan(&existing)
		if err != nil {
			return storageError(err)
		}

		if !existing {
			return failure("forbidden", "owner credential no longer active")
		}

		return nil
	}

	if _, err = tx.ExecContext(ctx, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id) VALUES(?,'operator',?,'')", r.CredentialHash, opaqueID()); err != nil {
		return failure("conflict", "credential hash already exists")
	}

	if _, err = tx.ExecContext(ctx, "UPDATE owner_join_codes SET consumed_hash=?,login=? WHERE code_hash=?", r.CredentialHash, actor.Login, tokenHash(r.Code)); err != nil {
		return storageError(err)
	}

	return storageOrNil(tx.Commit())
}
