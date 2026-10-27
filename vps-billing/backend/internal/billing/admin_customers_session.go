package billing

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"
)

var ErrAccountSession = errors.New("original account-management session is no longer available")

type accountSessionKey struct{}
type accountSessionBinding struct{ actor, hash string }

var accountSessionDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Only the authenticated HTTP adapter creates this binding. It is neither a
// request field nor an optional compatibility path for privileged store writes.
func withAccountWriteSession(ctx context.Context, actor, hash string) context.Context {
	return context.WithValue(ctx, accountSessionKey{}, accountSessionBinding{actor, hash})
}

func accountWriteSessionHash(ctx context.Context, actor string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	binding, ok := ctx.Value(accountSessionKey{}).(accountSessionBinding)
	if !ok || binding.actor != actor || !auditOwner.MatchString(actor) || !accountSessionDigest.MatchString(binding.hash) {
		return "", ErrAccountSession
	}
	return binding.hash, nil
}

func (m *Memory) accountSessionLocked(actor, hash string) error {
	s, ok := m.sessions[hash]
	if !ok || s.Hash != hash || s.UserID != actor || !s.Expires.After(time.Now()) {
		return ErrAccountSession
	}
	// Session.Revision protects credential issuance, not ordinary name changes
	// after issuance. Do not turn all account edits into implicit global logout.
	return nil
}

func lockAccountSession(ctx context.Context, tx *sql.Tx, actor, hash string) error {
	var owner string
	// Own-password/reset operations can delete this row. Acquire X immediately,
	// avoiding an S->X upgrade behind a concurrent logout's queued X request.
	err := tx.QueryRowContext(ctx, "SELECT user_id FROM sessions WHERE token_hash=? FOR UPDATE", hash).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAccountSession
	}
	if err != nil {
		return err
	}
	if owner != actor {
		return ErrAccountSession
	}
	// MySQL fixes UTC_TIMESTAMP at statement start. A predicate in the locking
	// read would therefore accept a session that expired while waiting for X.
	// Re-read database time only after acquiring X; no client-clock authority.
	var valid bool
	if err := tx.QueryRowContext(ctx, "SELECT expires_at>UTC_TIMESTAMP(6) FROM sessions WHERE token_hash=?", hash).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrAccountSession
	}
	return nil
}
