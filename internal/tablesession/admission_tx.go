package tablesession

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PrecheckGrant is a nonlocking authorization precheck used to derive the
// canonical tenant/store/table scope before acquiring broad admission locks.
// It never returns or logs the raw table-join token.
func (s *Service) PrecheckGrant(ctx context.Context, sessionID, rawToken string) (Session, error) {
	if s == nil || rawToken == "" {
		return Session{}, ErrDenied
	}
	return s.Read(ctx, sessionID, rawToken)
}

// LockAndValidateGrantTx establishes the table-session side of the admission
// lock order. It must be called only after catalog/store admission locks have
// been acquired. The dining table is explicitly locked before the session;
// the session and matching grant are then locked and checked again so any
// revocation, close, scope mismatch, or generation change during lock waits is
// authoritative.
func LockAndValidateGrantTx(ctx context.Context, tx pgx.Tx, expectedScope Scope, sessionID, rawToken string) (Session, error) {
	if tx == nil || rawToken == "" || expectedScope.TenantID == "" || expectedScope.StoreID == "" || expectedScope.TableID == "" || sessionID == "" {
		return Session{}, ErrDenied
	}
	var tableEnabled bool
	err := tx.QueryRow(ctx, `SELECT t.enabled AND st.enabled AND st.operating AND n.enabled
		FROM dining_tables t JOIN stores st USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE t.tenant_id=$1 AND t.store_id=$2 AND t.table_id=$3 FOR SHARE OF t`,
		expectedScope.TenantID, expectedScope.StoreID, expectedScope.TableID).Scan(&tableEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrDenied
	}
	if err != nil {
		return Session{}, err
	}
	if !tableEnabled {
		return Session{}, ErrDenied
	}

	var session Session
	err = tx.QueryRow(ctx, `SELECT session_id,tenant_id,store_id,table_id,status,qr_mode,
		requires_staff_acceptance,opened_at,expires_at,closed_at,version
		FROM table_sessions WHERE session_id=$1 AND tenant_id=$2 AND store_id=$3 AND table_id=$4
		FOR UPDATE`, sessionID, expectedScope.TenantID, expectedScope.StoreID, expectedScope.TableID).
		Scan(&session.ID, &session.Scope.TenantID, &session.Scope.StoreID, &session.Scope.TableID, &session.Status,
			&session.Mode, &session.RequiresStaffAcceptance, &session.OpenedAt, &session.ExpiresAt, &session.ClosedAt, &session.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrDenied
	}
	if err != nil {
		return Session{}, err
	}
	hash := sha256.Sum256([]byte(rawToken))
	var generation int64
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT generation,expires_at FROM table_join_grants
		WHERE token_hash=$1 AND session_id=$2 FOR SHARE`, hash[:], sessionID).Scan(&generation, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrDenied
	}
	if err != nil {
		return Session{}, err
	}
	trustedAt, err := transactionClock(ctx, tx)
	if err != nil {
		return Session{}, err
	}
	return RecheckGrantTx(ctx, tx, expectedScope, sessionID, rawToken, trustedAt)
}

// RecheckGrantTx repeats the locked admission predicates using a trusted time
// obtained after every potentially blocking lock wait (including the cart
// lock). Callers already hold the dining-table, session, and grant row locks
// acquired in the documented order. It takes no additional locks, preserving
// company -> store -> table -> session -> cart ordering.
func RecheckGrantTx(ctx context.Context, tx pgx.Tx, expectedScope Scope, sessionID, rawToken string, trustedAt time.Time) (Session, error) {
	if tx == nil || rawToken == "" || trustedAt.IsZero() || expectedScope.TenantID == "" || expectedScope.StoreID == "" || expectedScope.TableID == "" || sessionID == "" {
		return Session{}, ErrDenied
	}
	hash := sha256.Sum256([]byte(rawToken))
	var session Session
	var grantGeneration int64
	var grantExpiresAt time.Time
	err := tx.QueryRow(ctx, `SELECT s.session_id,s.tenant_id,s.store_id,s.table_id,s.status,s.qr_mode,
		s.requires_staff_acceptance,s.opened_at,s.expires_at,s.closed_at,s.version,
		g.generation,g.expires_at
		FROM table_sessions s JOIN table_join_grants g ON g.session_id=s.session_id
		WHERE s.session_id=$1 AND g.token_hash=$2`, sessionID, hash[:]).
		Scan(&session.ID, &session.Scope.TenantID, &session.Scope.StoreID, &session.Scope.TableID, &session.Status,
			&session.Mode, &session.RequiresStaffAcceptance, &session.OpenedAt, &session.ExpiresAt, &session.ClosedAt,
			&session.Version, &grantGeneration, &grantExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrDenied
	}
	if err != nil {
		return Session{}, err
	}
	if session.Scope != expectedScope || session.Status != Open {
		return Session{}, ErrDenied
	}
	var sessionGeneration int64
	err = tx.QueryRow(ctx, `SELECT grant_generation FROM table_sessions WHERE session_id=$1`, sessionID).Scan(&sessionGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrDenied
	}
	if err != nil {
		return Session{}, err
	}
	if grantGeneration != sessionGeneration {
		return Session{}, ErrDenied
	}
	if !session.ExpiresAt.After(trustedAt) || !grantExpiresAt.After(trustedAt) {
		return Session{}, ErrExpired
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT t.enabled AND st.enabled AND st.operating AND n.enabled
		FROM dining_tables t JOIN stores st USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE t.tenant_id=$1 AND t.store_id=$2 AND t.table_id=$3`,
		expectedScope.TenantID, expectedScope.StoreID, expectedScope.TableID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrDenied
	}
	if err != nil {
		return Session{}, err
	}
	if !active {
		return Session{}, ErrDenied
	}
	return session, nil
}

func transactionClock(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}
