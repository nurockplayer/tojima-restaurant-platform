// Package staffauth stores staff sessions, authentication transactions, and memberships.
package staffauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
)

var (
	ErrNoSession      = errors.New("staff session not found or not live")
	ErrSubjectHeld    = errors.New("staff subject is on hold")
	ErrStaleAuth      = errors.New("authentication started before the latest hold/restore")
	ErrTxInvalid      = errors.New("staff auth transaction invalid, expired or used")
	ErrIDPLoggedOut   = errors.New("identity provider session already logged out")
	errSubjectNotHeld = errors.New("staff subject is not on hold")
)

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, errors.New("staff auth database pool is nil")
	}
	return &Store{db: db}, nil
}

type Session struct {
	ID, SubjectID   string
	StrongLogin     bool
	LastStrongAuth  *time.Time
	AbsoluteExpires time.Time
}

type AuthTxInput struct {
	StateHash, BindingHash                   [32]byte
	Nonce, PKCEVerifier, Purpose, ReturnPath string
	SessionID, SubjectID                     string
}

type AuthTx struct {
	Nonce, PKCEVerifier, Purpose, ReturnPath string
	SessionID, SubjectID                     string
	CreatedAt                                time.Time
}

type dbtx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func writeEvent(ctx context.Context, tx dbtx, subjectID, sessionID, kind string, detail any) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var subject, session any
	if subjectID != "" {
		subject = subjectID
	}
	if sessionID != "" {
		session = sessionID
	}
	_, err = tx.Exec(ctx, `INSERT INTO staff_auth_events(subject_id,session_id,kind,detail) VALUES($1,$2,$3,$4::jsonb)`, subject, session, kind, string(body))
	return err
}

func commitResult(ctx context.Context, tx pgx.Tx, result error) error {
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return result
}

// epochLock serialises auth transaction creation (shared) with hold/restore
// (exclusive). A transaction stamped after the shared lock is granted started
// after every committed epoch change, and one stamped earlier is rejected by
// CreateSession/PromoteStrong: transaction-start now() alone cannot order them.
const epochLock = 771904401

func (s *Store) CreateAuthTx(ctx context.Context, in AuthTxInput) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, epochLock); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO staff_auth_tx(state_sha256,binding_sha256,nonce,pkce_verifier,purpose,session_id,subject_id,return_path,created_at,expires_at)
		SELECT $1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,''),$8,c.t,c.t+interval '10 minutes' FROM (SELECT clock_timestamp() AS t) c`,
		in.StateHash[:], in.BindingHash[:], in.Nonce, in.PKCEVerifier, in.Purpose, in.SessionID, in.SubjectID, in.ReturnPath); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockIDPSession serialises session creation with back-channel logout for one
// IdP session, so a logout can never commit between verification and insert.
func lockIDPSession(ctx context.Context, tx dbtx, sid string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tojima-staff-idp-sid:'||$1,0))`, sid)
	return err
}

func (s *Store) ConsumeAuthTx(ctx context.Context, stateHash, bindingHash [32]byte) (AuthTx, error) {
	var out AuthTx
	var storedBinding []byte
	var sessionID, subjectID *string
	err := s.db.QueryRow(ctx, `UPDATE staff_auth_tx SET used_at=now()
		WHERE state_sha256=$1 AND used_at IS NULL AND expires_at>now()
		RETURNING binding_sha256,nonce,pkce_verifier,purpose,return_path,session_id::text,subject_id,created_at`, stateHash[:]).Scan(
		&storedBinding, &out.Nonce, &out.PKCEVerifier, &out.Purpose, &out.ReturnPath, &sessionID, &subjectID, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthTx{}, ErrTxInvalid
	}
	if err != nil {
		return AuthTx{}, err
	}
	if len(storedBinding) != len(bindingHash) || subtle.ConstantTimeCompare(storedBinding, bindingHash[:]) != 1 {
		return AuthTx{}, ErrTxInvalid
	}
	if sessionID != nil {
		out.SessionID = *sessionID
	}
	if subjectID != nil {
		out.SubjectID = *subjectID
	}
	return out, nil
}

func (s *Store) CreateSession(ctx context.Context, subjectID, idpSID string, strong bool, authStartedAt time.Time) (string, Session, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", Session{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO staff_subjects(subject_id) VALUES($1) ON CONFLICT DO NOTHING`, subjectID); err != nil {
		return "", Session{}, err
	}
	var held bool
	var epochChanged time.Time
	var authEpoch int64
	if err = tx.QueryRow(ctx, `SELECT hold_at IS NOT NULL,epoch_changed_at,auth_epoch FROM staff_subjects WHERE subject_id=$1 FOR UPDATE`, subjectID).Scan(&held, &epochChanged, &authEpoch); err != nil {
		return "", Session{}, err
	}
	if held {
		if err = writeEvent(ctx, tx, subjectID, "", "login_rejected", map[string]string{"reason": "hold"}); err != nil {
			return "", Session{}, err
		}
		return "", Session{}, commitResult(ctx, tx, ErrSubjectHeld)
	}
	if authEpoch > 0 && epochChanged.After(authStartedAt) {
		if err = writeEvent(ctx, tx, subjectID, "", "login_rejected", map[string]string{"reason": "stale_epoch"}); err != nil {
			return "", Session{}, err
		}
		return "", Session{}, commitResult(ctx, tx, ErrStaleAuth)
	}
	if err = lockIDPSession(ctx, tx, idpSID); err != nil {
		return "", Session{}, err
	}
	var loggedOut bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staff_revoked_idp_sessions WHERE idp_sid=$1)`, idpSID).Scan(&loggedOut); err != nil {
		return "", Session{}, err
	}
	if loggedOut {
		if err = writeEvent(ctx, tx, subjectID, "", "login_rejected", map[string]string{"reason": "idp_logged_out"}); err != nil {
			return "", Session{}, err
		}
		return "", Session{}, commitResult(ctx, tx, ErrIDPLoggedOut)
	}
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", Session{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	tokenHash := sha256.Sum256([]byte(token))
	var session Session
	session.SubjectID, session.StrongLogin = subjectID, strong
	err = tx.QueryRow(ctx, `INSERT INTO staff_sessions(token_sha256,subject_id,idp_sid,strong_login,absolute_expires_at,last_strong_auth_at)
		VALUES($1,$2,$3,$4,now()+interval '30 days',CASE WHEN $4 THEN now() ELSE NULL END)
		RETURNING id::text,absolute_expires_at,last_strong_auth_at`, tokenHash[:], subjectID, idpSID, strong).Scan(&session.ID, &session.AbsoluteExpires, &session.LastStrongAuth)
	if err != nil {
		return "", Session{}, err
	}
	if err = writeEvent(ctx, tx, subjectID, session.ID, "login", map[string]bool{"strong": strong}); err != nil {
		return "", Session{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", Session{}, err
	}
	return token, session, nil
}

func (s *Store) ResolveSession(ctx context.Context, rawToken string) (Session, error) {
	hash := sha256.Sum256([]byte(rawToken))
	var session Session
	err := s.db.QueryRow(ctx, `SELECT ss.id::text,ss.subject_id,ss.strong_login,ss.last_strong_auth_at,ss.absolute_expires_at
		FROM staff_sessions ss JOIN staff_subjects su USING(subject_id)
		WHERE ss.token_sha256=$1 AND ss.revoked_at IS NULL AND now()<ss.absolute_expires_at AND su.hold_at IS NULL`, hash[:]).Scan(
		&session.ID, &session.SubjectID, &session.StrongLogin, &session.LastStrongAuth, &session.AbsoluteExpires)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	return session, err
}

func (s *Store) PromoteStrong(ctx context.Context, sessionID, subjectID string, authStartedAt time.Time) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var held bool
	var epochChanged time.Time
	err = tx.QueryRow(ctx, `SELECT hold_at IS NOT NULL,epoch_changed_at FROM staff_subjects WHERE subject_id=$1 FOR UPDATE`, subjectID).Scan(&held, &epochChanged)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSession
	}
	if err != nil {
		return err
	}
	if held {
		if err = writeEvent(ctx, tx, subjectID, sessionID, "login_rejected", map[string]string{"reason": "hold"}); err != nil {
			return err
		}
		return commitResult(ctx, tx, ErrSubjectHeld)
	}
	if epochChanged.After(authStartedAt) {
		if err = writeEvent(ctx, tx, subjectID, sessionID, "login_rejected", map[string]string{"reason": "stale_epoch"}); err != nil {
			return err
		}
		return commitResult(ctx, tx, ErrStaleAuth)
	}
	tag, err := tx.Exec(ctx, `UPDATE staff_sessions SET strong_login=true,last_strong_auth_at=now() WHERE id=$1 AND subject_id=$2 AND revoked_at IS NULL AND now()<absolute_expires_at`, sessionID, subjectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoSession
	}
	if err = writeEvent(ctx, tx, subjectID, sessionID, "step_up", map[string]bool{"strong": true}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RevokeSession(ctx context.Context, sessionID, reason string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var subjectID string
	err = tx.QueryRow(ctx, `UPDATE staff_sessions SET revoked_at=now(),revoked_reason=$2 WHERE id=$1 AND revoked_at IS NULL RETURNING subject_id`, sessionID, reason).Scan(&subjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSession
	}
	if err != nil {
		return err
	}
	kind := "session_revoked"
	if reason == "logout" {
		kind = "logout"
	}
	if err = writeEvent(ctx, tx, subjectID, sessionID, kind, map[string]string{"reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RevokeSubjectSessions(ctx context.Context, subjectID, reason string) (int64, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE staff_sessions SET revoked_at=now(),revoked_reason=$2 WHERE subject_id=$1 AND revoked_at IS NULL`, subjectID, reason)
	if err != nil {
		return 0, err
	}
	n := tag.RowsAffected()
	if err = writeEvent(ctx, tx, subjectID, "", "session_revoked", map[string]any{"count": n, "reason": reason}); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) RevokeByIDP(ctx context.Context, sid, sub string) (int64, error) {
	if sid == "" && sub == "" {
		return 0, errors.New("idp sid or subject is required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var tag pgconn.CommandTag
	if sid != "" {
		// Fence first: a login for this IdP session still in flight either
		// committed before this lock (and is revoked below) or is refused later.
		if err = lockIDPSession(ctx, tx, sid); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM staff_revoked_idp_sessions WHERE revoked_at<now()-interval '31 days'`); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO staff_revoked_idp_sessions(idp_sid) VALUES($1) ON CONFLICT DO NOTHING`, sid); err != nil {
			return 0, err
		}
		tag, err = tx.Exec(ctx, `UPDATE staff_sessions SET revoked_at=now(),revoked_reason='backchannel_logout' WHERE idp_sid=$1 AND ($2='' OR subject_id=$2) AND revoked_at IS NULL`, sid, sub)
	} else {
		tag, err = tx.Exec(ctx, `UPDATE staff_sessions SET revoked_at=now(),revoked_reason='backchannel_logout' WHERE subject_id=$1 AND revoked_at IS NULL`, sub)
	}
	if err != nil {
		return 0, err
	}
	n := tag.RowsAffected()
	if err = writeEvent(ctx, tx, sub, "", "backchannel_revoke", map[string]any{"count": n, "sid_present": sid != ""}); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) Hold(ctx context.Context, subjectID, reason, actor string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, epochLock); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO staff_subjects(subject_id,hold_at,hold_reason,auth_epoch,epoch_changed_at) VALUES($1,now(),$2,1,clock_timestamp())
		ON CONFLICT(subject_id) DO UPDATE SET hold_at=now(),hold_reason=EXCLUDED.hold_reason,auth_epoch=staff_subjects.auth_epoch+1,epoch_changed_at=clock_timestamp()`, subjectID, reason)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE staff_sessions SET revoked_at=now(),revoked_reason='hold' WHERE subject_id=$1 AND revoked_at IS NULL`, subjectID)
	if err != nil {
		return err
	}
	if err = writeEvent(ctx, tx, subjectID, "", "hold", map[string]any{"actor": actor, "reason": reason, "revoked": tag.RowsAffected()}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Restore(ctx context.Context, subjectID, actor string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, epochLock); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE staff_subjects SET hold_at=NULL,hold_reason=NULL,restored_at=now(),auth_epoch=auth_epoch+1,epoch_changed_at=clock_timestamp() WHERE subject_id=$1 AND hold_at IS NOT NULL`, subjectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errSubjectNotHeld
	}
	if err = writeEvent(ctx, tx, subjectID, "", "restore", map[string]string{"actor": actor}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) SeeLogoutJTI(ctx context.Context, jti string) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM staff_logout_jti WHERE seen_at<now()-interval '24 hours'`); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO staff_logout_jti(jti) VALUES($1) ON CONFLICT DO NOTHING`, jti)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) Current(ctx context.Context, subjectID, tenantID string) ([]authz.Membership, error) {
	rows, err := s.db.Query(ctx, `SELECT m.tenant_id,coalesce(m.store_id,''),m.role FROM staff_memberships m JOIN staff_subjects s USING(subject_id)
		WHERE m.subject_id=$1 AND m.tenant_id=$2 AND m.revoked_at IS NULL AND s.hold_at IS NULL ORDER BY m.id`, subjectID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]authz.Membership, 0)
	for rows.Next() {
		var m authz.Membership
		if err = rows.Scan(&m.TenantID, &m.StoreID, &m.Role); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *Store) Grant(ctx context.Context, subjectID, tenantID, storeID string, role authz.Role, actor string) error {
	var store any
	if storeID != "" {
		store = storeID
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO staff_subjects(subject_id) VALUES($1) ON CONFLICT DO NOTHING`, subjectID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO staff_memberships(subject_id,tenant_id,store_id,role,granted_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, subjectID, tenantID, store, role, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if err = writeEvent(ctx, tx, subjectID, "", "membership_grant", map[string]string{"actor": actor, "tenant_id": tenantID, "store_id": storeID, "role": string(role)}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RevokeMembership(ctx context.Context, subjectID, tenantID, storeID string, role authz.Role, actor string) (bool, error) {
	var store any
	if storeID != "" {
		store = storeID
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE staff_memberships SET revoked_at=now(),revoked_by=$5 WHERE subject_id=$1 AND tenant_id=$2 AND store_id IS NOT DISTINCT FROM $3 AND role=$4 AND revoked_at IS NULL`, subjectID, tenantID, store, role, actor)
	if err != nil {
		return false, err
	}
	changed := tag.RowsAffected() > 0
	if changed {
		if err = writeEvent(ctx, tx, subjectID, "", "membership_revoke", map[string]string{"actor": actor, "tenant_id": tenantID, "store_id": storeID, "role": string(role)}); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return changed, nil
}

var _ authz.Memberships = (*Store)(nil)
