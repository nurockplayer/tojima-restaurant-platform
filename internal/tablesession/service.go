package tablesession

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db          *pgxpool.Pool
	opts        Options
	closePolicy ClosePolicy
}

func New(db *pgxpool.Pool, opts Options, closePolicy ClosePolicy) (*Service, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	if closePolicy == nil {
		closePolicy = DenyClose{}
	}
	return &Service{db: db, opts: opts, closePolicy: closePolicy}, nil
}

func validMode(mode Mode) bool { return mode == StaffOpen || mode == SessionPIN || mode == DirectScan }

func (s *Service) TableByLocator(ctx context.Context, locator string) (TableInfo, error) {
	var t TableInfo
	err := s.db.QueryRow(ctx, `SELECT t.tenant_id,t.store_id,t.table_id,t.public_locator,t.enabled,s.enabled,s.operating,n.enabled,s.qr_entry_mode
		FROM dining_tables t JOIN stores s USING (tenant_id,store_id) JOIN tenants n USING(tenant_id) WHERE t.public_locator=$1`, locator).
		Scan(&t.Scope.TenantID, &t.Scope.StoreID, &t.Scope.TableID, &t.Locator, &t.Enabled, &t.StoreEnabled, &t.Operating, &t.TenantEnabled, &t.Mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return TableInfo{}, ErrNotFound
	}
	return t, err
}

func (s *Service) Table(ctx context.Context, scope Scope) (TableInfo, error) {
	var t TableInfo
	err := s.db.QueryRow(ctx, `SELECT t.tenant_id,t.store_id,t.table_id,t.public_locator,t.enabled,s.enabled,s.operating,n.enabled,s.qr_entry_mode
		FROM dining_tables t JOIN stores s USING (tenant_id,store_id) JOIN tenants n USING(tenant_id) WHERE t.tenant_id=$1 AND t.store_id=$2 AND t.table_id=$3`, scope.TenantID, scope.StoreID, scope.TableID).
		Scan(&t.Scope.TenantID, &t.Scope.StoreID, &t.Scope.TableID, &t.Locator, &t.Enabled, &t.StoreEnabled, &t.Operating, &t.TenantEnabled, &t.Mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return TableInfo{}, ErrNotFound
	}
	return t, err
}

func (s *Service) StoreScope(ctx context.Context, tenantID, storeID string) (Scope, error) {
	var scope Scope
	err := s.db.QueryRow(ctx, `SELECT s.tenant_id,s.store_id FROM stores s JOIN tenants n USING(tenant_id) WHERE s.tenant_id=$1 AND s.store_id=$2 AND s.enabled AND n.enabled`, tenantID, storeID).Scan(&scope.TenantID, &scope.StoreID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Scope{}, ErrNotFound
	}
	return scope, err
}

func available(t TableInfo) error {
	if !t.Enabled || !t.StoreEnabled || !t.TenantEnabled || !t.Operating {
		return ErrDenied
	}
	return nil
}

func (s *Service) PublicStatus(ctx context.Context, locator string) (PublicTable, error) {
	t, err := s.TableByLocator(ctx, locator)
	if err != nil {
		return PublicTable{}, err
	}
	result := PublicTable{Mode: t.Mode}
	if !t.Enabled || !t.StoreEnabled || !t.TenantEnabled || !t.Operating {
		return result, nil
	}
	var sessionActive bool
	err = s.db.QueryRow(ctx, `SELECT session_id,qr_mode,expires_at>clock_timestamp() FROM table_sessions WHERE tenant_id=$1 AND store_id=$2 AND table_id=$3 AND status='OPEN'`, t.Scope.TenantID, t.Scope.StoreID, t.Scope.TableID).Scan(&result.CurrentSessionID, &result.Mode, &sessionActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return PublicTable{}, err
	}
	if !sessionActive {
		result.CurrentSessionID = ""
		result.Open = false
		result.Expired = true
		return result, nil
	}
	result.Open = true
	return result, nil
}

func (s *Service) SetStoreMode(ctx context.Context, tenantID, storeID string, mode Mode) error {
	if !validMode(mode) {
		return ErrDenied
	}
	command, err := s.db.Exec(ctx, `UPDATE stores s SET qr_entry_mode=$3 FROM tenants n WHERE s.tenant_id=$1 AND s.store_id=$2 AND s.enabled=true AND n.tenant_id=s.tenant_id AND n.enabled=true`, tenantID, storeID, mode)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Open(ctx context.Context, scope Scope, actor string) (OpenResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	defer tx.Rollback(ctx)
	var mode Mode
	var tableOK bool
	err = tx.QueryRow(ctx, `SELECT s.qr_entry_mode,t.enabled AND s.enabled AND s.operating AND n.enabled
		FROM stores s JOIN dining_tables t USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE s.tenant_id=$1 AND s.store_id=$2 AND t.table_id=$3 FOR UPDATE OF s,t`, scope.TenantID, scope.StoreID, scope.TableID).Scan(&mode, &tableOK)
	if errors.Is(err, pgx.ErrNoRows) {
		return OpenResult{}, ErrNotFound
	}
	if err != nil {
		return OpenResult{}, err
	}
	if !tableOK {
		return OpenResult{}, ErrDenied
	}
	var existing string
	var existingExpired bool
	err = tx.QueryRow(ctx, `SELECT session_id,expires_at<=clock_timestamp() FROM table_sessions WHERE tenant_id=$1 AND store_id=$2 AND table_id=$3 AND status='OPEN' FOR UPDATE`, scope.TenantID, scope.StoreID, scope.TableID).Scan(&existing, &existingExpired)
	if err == nil {
		if existingExpired {
			return OpenResult{}, ErrExpired
		}
		return OpenResult{}, ErrConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return OpenResult{}, err
	}
	result, err := s.insertSession(ctx, tx, scope, mode, actor, false)
	if err != nil {
		return OpenResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OpenResult{}, err
	}
	return result, nil
}

func (s *Service) DirectJoin(ctx context.Context, locator, expectedSessionID string) (Grant, error) {
	t, err := s.TableByLocator(ctx, locator)
	if err != nil {
		return Grant{}, err
	}
	if err = available(t); err != nil {
		return Grant{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback(ctx)
	var mode Mode
	var active bool
	err = tx.QueryRow(ctx, `SELECT s.qr_entry_mode,t.enabled AND s.enabled AND s.operating AND n.enabled FROM stores s JOIN dining_tables t USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE s.tenant_id=$1 AND s.store_id=$2 AND t.table_id=$3 FOR UPDATE OF s,t`, t.Scope.TenantID, t.Scope.StoreID, t.Scope.TableID).Scan(&mode, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrNotFound
	}
	if err != nil {
		return Grant{}, err
	}
	if !active {
		return Grant{}, ErrDenied
	}
	var session Session
	err = readOpenSession(ctx, tx, t.Scope, &session)
	if errors.Is(err, pgx.ErrNoRows) {
		if expectedSessionID != "" {
			return Grant{}, ErrDenied
		}
		if mode != DirectScan {
			return Grant{}, ErrDenied
		}
		result, insertErr := s.insertSession(ctx, tx, t.Scope, DirectScan, "", true)
		if insertErr != nil {
			return Grant{}, insertErr
		}
		session = result.Session
	} else if err != nil {
		return Grant{}, err
	} else if session.Mode != DirectScan {
		return Grant{}, ErrDenied
	} else if expectedSessionID != "" && expectedSessionID != session.ID {
		return Grant{}, ErrDenied
	}
	if session.Expired {
		return Grant{}, ErrExpired
	}
	grant, err := s.insertGrant(ctx, tx, session)
	if err != nil {
		return Grant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Grant{}, err
	}
	return grant, nil
}

func (s *Service) Join(ctx context.Context, sessionID, expectedSessionID, pin string) (Grant, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback(ctx)
	var session Session
	var pinHash []byte
	var attempts int
	var pinAlive, pinLocked bool
	err = tx.QueryRow(ctx, `SELECT session_id,tenant_id,store_id,table_id,status,qr_mode,requires_staff_acceptance,opened_at,expires_at,expires_at<=clock_timestamp(),closed_at,version,pin_hash,COALESCE(pin_expires_at>clock_timestamp(),false),COALESCE(pin_locked_until>clock_timestamp(),false),pin_attempts
		FROM table_sessions WHERE session_id=$1 FOR UPDATE`, sessionID).Scan(&session.ID, &session.Scope.TenantID, &session.Scope.StoreID, &session.Scope.TableID, &session.Status, &session.Mode, &session.RequiresStaffAcceptance, &session.OpenedAt, &session.ExpiresAt, &session.Expired, &session.ClosedAt, &session.Version, &pinHash, &pinAlive, &pinLocked, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrNotFound
	}
	if err != nil {
		return Grant{}, err
	}
	if expectedSessionID != "" && expectedSessionID != session.ID {
		return Grant{}, ErrDenied
	}
	if session.Status != Open {
		return Grant{}, ErrDenied
	}
	if session.Expired {
		return Grant{}, ErrExpired
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT t.enabled AND s.enabled AND s.operating AND n.enabled AND t.public_locator IS NOT NULL FROM dining_tables t JOIN stores s USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE t.tenant_id=$1 AND t.store_id=$2 AND t.table_id=$3`, session.Scope.TenantID, session.Scope.StoreID, session.Scope.TableID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrDenied
	}
	if err != nil {
		return Grant{}, err
	}
	if !active {
		return Grant{}, ErrDenied
	}
	switch session.Mode {
	case StaffOpen:
		if pin != "" {
			return Grant{}, ErrDenied
		}
	case SessionPIN:
		if pin == "" {
			return Grant{}, ErrInvalidPIN
		}
		if pinLocked {
			return Grant{}, ErrPINLocked
		}
		if !pinAlive {
			return Grant{}, ErrInvalidPIN
		}
		provided := s.pinHash(pin)
		if !hmac.Equal(pinHash, provided) {
			attempts++
			var lock any
			if attempts >= s.opts.PINMaxAttempts {
				lock = s.opts.PINLockout.Milliseconds()
			}
			_, updateErr := tx.Exec(ctx, `UPDATE table_sessions SET pin_attempts=$2,pin_locked_until=CASE WHEN $3::bigint IS NULL THEN NULL ELSE clock_timestamp()+($3 * interval '1 millisecond') END,version=version+1 WHERE session_id=$1`, session.ID, attempts, lock)
			if updateErr != nil {
				return Grant{}, updateErr
			}
			if err = tx.Commit(ctx); err != nil {
				return Grant{}, err
			}
			if attempts >= s.opts.PINMaxAttempts {
				return Grant{}, ErrPINLocked
			}
			return Grant{}, ErrInvalidPIN
		}
		if _, err = tx.Exec(ctx, `UPDATE table_sessions SET pin_attempts=0,pin_locked_until=NULL WHERE session_id=$1`, session.ID); err != nil {
			return Grant{}, err
		}
	case DirectScan:
	default:
		return Grant{}, ErrDenied
	}
	grant, err := s.insertGrant(ctx, tx, session)
	if err != nil {
		return Grant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Grant{}, err
	}
	return grant, nil
}

func (s *Service) Read(ctx context.Context, sessionID, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrDenied
	}
	hash := sha256.Sum256([]byte(token))
	var session Session
	err := s.db.QueryRow(ctx, `SELECT s.session_id,s.tenant_id,s.store_id,s.table_id,s.status,s.qr_mode,s.requires_staff_acceptance,s.opened_at,s.expires_at,s.closed_at,s.version
		FROM table_join_grants g JOIN table_sessions s USING(session_id) JOIN dining_tables t USING(tenant_id,store_id,table_id) JOIN stores st USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE g.token_hash=$1 AND g.session_id=$2 AND s.status='OPEN' AND s.expires_at>now() AND g.expires_at>now() AND g.generation=s.grant_generation AND t.enabled AND st.enabled AND st.operating AND n.enabled`, hash[:], sessionID).
		Scan(&session.ID, &session.Scope.TenantID, &session.Scope.StoreID, &session.Scope.TableID, &session.Status, &session.Mode, &session.RequiresStaffAcceptance, &session.OpenedAt, &session.ExpiresAt, &session.ClosedAt, &session.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrDenied
	}
	if err != nil {
		return Session{}, err
	}
	return session, nil
}

// ReadStaff returns one session only through its complete physical-table scope.
// The caller must authorize the current staff membership before exposing it.
func (s *Service) ReadStaff(ctx context.Context, scope Scope, sessionID string) (Session, error) {
	var session Session
	err := s.db.QueryRow(ctx, `SELECT ts.session_id,ts.tenant_id,ts.store_id,ts.table_id,ts.status,ts.qr_mode,ts.requires_staff_acceptance,ts.opened_at,ts.expires_at,ts.closed_at,ts.version
		FROM table_sessions ts JOIN tenants n USING(tenant_id) JOIN stores st USING(tenant_id,store_id) JOIN dining_tables t USING(tenant_id,store_id,table_id)
		WHERE ts.session_id=$1 AND ts.tenant_id=$2 AND ts.store_id=$3 AND ts.table_id=$4 AND ts.expires_at>now() AND n.enabled AND st.enabled AND st.operating AND t.enabled`, sessionID, scope.TenantID, scope.StoreID, scope.TableID).
		Scan(&session.ID, &session.Scope.TenantID, &session.Scope.StoreID, &session.Scope.TableID, &session.Status, &session.Mode, &session.RequiresStaffAcceptance, &session.OpenedAt, &session.ExpiresAt, &session.ClosedAt, &session.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return session, err
}

func (s *Service) Close(ctx context.Context, scope Scope, sessionID, actor string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var session Session
	err = tx.QueryRow(ctx, `SELECT ts.session_id,ts.tenant_id,ts.store_id,ts.table_id,ts.status,ts.qr_mode,ts.requires_staff_acceptance,ts.opened_at,ts.expires_at,ts.closed_at,ts.version
		FROM table_sessions ts JOIN tenants n USING(tenant_id) JOIN stores st USING(tenant_id,store_id) JOIN dining_tables t USING(tenant_id,store_id,table_id)
		WHERE ts.session_id=$1 AND ts.tenant_id=$2 AND ts.store_id=$3 AND ts.table_id=$4 AND n.enabled AND st.enabled AND st.operating AND t.enabled FOR UPDATE OF ts`, sessionID, scope.TenantID, scope.StoreID, scope.TableID).
		Scan(&session.ID, &session.Scope.TenantID, &session.Scope.StoreID, &session.Scope.TableID, &session.Status, &session.Mode, &session.RequiresStaffAcceptance, &session.OpenedAt, &session.ExpiresAt, &session.ClosedAt, &session.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if session.Status != Open {
		return ErrConflict
	}
	if err = s.closePolicy.CheckClose(ctx, tx, session); err != nil {
		return err
	}
	now := time.Now().UTC()
	command, err := tx.Exec(ctx, `UPDATE table_sessions SET status='CLOSED',closed_at=$2,closed_by=$3,grant_generation=grant_generation+1,pin_generation=pin_generation+1,pin_hash=NULL,pin_expires_at=NULL,version=version+1 WHERE session_id=$1 AND status='OPEN'`, session.ID, now, actor)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Service) ResetPIN(ctx context.Context, scope Scope, sessionID, actor string) (OpenResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	defer tx.Rollback(ctx)
	var session Session
	var expired bool
	var previousPINHash []byte
	err = tx.QueryRow(ctx, `SELECT ts.session_id,ts.tenant_id,ts.store_id,ts.table_id,ts.status,ts.qr_mode,ts.requires_staff_acceptance,ts.opened_at,ts.expires_at,ts.closed_at,ts.version,ts.expires_at<=clock_timestamp(),ts.pin_hash FROM table_sessions ts
		JOIN tenants n USING(tenant_id) JOIN stores st USING(tenant_id,store_id) JOIN dining_tables t USING(tenant_id,store_id,table_id)
		WHERE ts.session_id=$1 AND ts.tenant_id=$2 AND ts.store_id=$3 AND ts.table_id=$4 AND ts.status='OPEN' AND n.enabled AND st.enabled AND st.operating AND t.enabled FOR UPDATE OF ts`, sessionID, scope.TenantID, scope.StoreID, scope.TableID).
		Scan(&session.ID, &session.Scope.TenantID, &session.Scope.StoreID, &session.Scope.TableID, &session.Status, &session.Mode, &session.RequiresStaffAcceptance, &session.OpenedAt, &session.ExpiresAt, &session.ClosedAt, &session.Version, &expired, &previousPINHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return OpenResult{}, ErrNotFound
	}
	if err != nil {
		return OpenResult{}, err
	}
	if session.Status != Open || session.Mode != SessionPIN {
		return OpenResult{}, ErrConflict
	}
	if expired {
		return OpenResult{}, ErrExpired
	}
	pin, err := s.newPINDifferentFrom(previousPINHash)
	if err != nil {
		return OpenResult{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE table_sessions SET pin_generation=pin_generation+1,grant_generation=grant_generation+1,pin_hash=$2,pin_expires_at=clock_timestamp()+($3 * interval '1 millisecond'),pin_attempts=0,pin_locked_until=NULL,version=version+1 WHERE session_id=$1`, session.ID, s.pinHash(pin), s.opts.PINTTL.Milliseconds())
	if err != nil {
		return OpenResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OpenResult{}, err
	}
	session.Version++
	return OpenResult{Session: session, PIN: pin}, nil
}

func (s *Service) insertSession(ctx context.Context, tx pgx.Tx, scope Scope, mode Mode, actor string, direct bool) (OpenResult, error) {
	id, err := randomID(s.opts.Random)
	if err != nil {
		return OpenResult{}, err
	}
	var pin string
	var hash any
	var pinTTL any
	if mode == SessionPIN {
		pin, err = s.newPIN()
		if err != nil {
			return OpenResult{}, err
		}
		hash = s.pinHash(pin)
		pinTTL = s.opts.PINTTL.Milliseconds()
	}
	var requiresStaff bool
	if direct || mode == DirectScan {
		requiresStaff = true
	}
	_, err = tx.Exec(ctx, `INSERT INTO table_sessions(session_id,tenant_id,store_id,table_id,status,qr_mode,requires_staff_acceptance,opened_at,expires_at,opened_by,pin_hash,pin_expires_at)
		VALUES($1,$2,$3,$4,'OPEN',$5,$6,clock_timestamp(),clock_timestamp()+($7 * interval '1 millisecond'),NULLIF($8,''),$9,CASE WHEN $5='SESSION_PIN' THEN clock_timestamp()+($10 * interval '1 millisecond') ELSE NULL END)`, id, scope.TenantID, scope.StoreID, scope.TableID, mode, requiresStaff, s.opts.SessionTTL.Milliseconds(), actor, hash, pinTTL)
	if err != nil {
		return OpenResult{}, err
	}
	var session Session
	err = tx.QueryRow(ctx, `SELECT session_id,tenant_id,store_id,table_id,status,qr_mode,requires_staff_acceptance,opened_at,expires_at,closed_at,version FROM table_sessions WHERE session_id=$1`, id).
		Scan(&session.ID, &session.Scope.TenantID, &session.Scope.StoreID, &session.Scope.TableID, &session.Status, &session.Mode, &session.RequiresStaffAcceptance, &session.OpenedAt, &session.ExpiresAt, &session.ClosedAt, &session.Version)
	return OpenResult{Session: session, PIN: pin}, err
}

func readOpenSession(ctx context.Context, tx pgx.Tx, scope Scope, session *Session) error {
	return tx.QueryRow(ctx, `SELECT session_id,tenant_id,store_id,table_id,status,qr_mode,requires_staff_acceptance,opened_at,expires_at,expires_at<=clock_timestamp(),closed_at,version FROM table_sessions
		WHERE tenant_id=$1 AND store_id=$2 AND table_id=$3 AND status='OPEN' FOR UPDATE`, scope.TenantID, scope.StoreID, scope.TableID).
		Scan(&session.ID, &session.Scope.TenantID, &session.Scope.StoreID, &session.Scope.TableID, &session.Status, &session.Mode, &session.RequiresStaffAcceptance, &session.OpenedAt, &session.ExpiresAt, &session.Expired, &session.ClosedAt, &session.Version)
}

func (s *Service) insertGrant(ctx context.Context, tx pgx.Tx, session Session) (Grant, error) {
	windowMillis := s.opts.GrantWindow.Milliseconds()
	var issued int
	err := tx.QueryRow(ctx, `INSERT INTO table_join_grant_limits(tenant_id,store_id,table_id,window_started_at,grant_count)
		VALUES($1,$2,$3,date_bin($4 * interval '1 millisecond',clock_timestamp(),'epoch'::timestamptz),1)
		ON CONFLICT(tenant_id,store_id,table_id) DO UPDATE SET
		window_started_at=EXCLUDED.window_started_at,
		grant_count=CASE WHEN table_join_grant_limits.window_started_at < EXCLUDED.window_started_at THEN 1 ELSE table_join_grant_limits.grant_count+1 END
		WHERE table_join_grant_limits.window_started_at < EXCLUDED.window_started_at OR table_join_grant_limits.grant_count < $5
		RETURNING grant_count`, session.Scope.TenantID, session.Scope.StoreID, session.Scope.TableID, windowMillis, s.opts.GrantWindowMax).Scan(&issued)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrRateLimited
	}
	if err != nil {
		return Grant{}, err
	}
	bytes := make([]byte, 32)
	if _, err := io.ReadFull(s.opts.Random, bytes); err != nil {
		return Grant{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	hash := sha256.Sum256([]byte(token))
	var expires time.Time
	err = tx.QueryRow(ctx, `INSERT INTO table_join_grants(token_hash,session_id,generation,expires_at)
		SELECT $1,session_id,grant_generation,LEAST(clock_timestamp()+($2 * interval '1 millisecond'),expires_at)
		FROM table_sessions WHERE session_id=$3 AND status='OPEN' AND expires_at>clock_timestamp() RETURNING expires_at`, hash[:], s.opts.GrantTTL.Milliseconds(), session.ID).Scan(&expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrExpired
	}
	if err != nil {
		return Grant{}, err
	}
	return Grant{Token: token, ExpiresAt: expires, Session: session}, nil
}

func (s *Service) pinHash(pin string) []byte {
	mac := hmac.New(sha256.New, s.opts.PINPepper)
	_, _ = mac.Write([]byte(pin))
	return mac.Sum(nil)
}
func (s *Service) newPIN() (string, error) {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(s.opts.PINLength)), nil)
	n, err := rand.Int(s.opts.Random, limit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", s.opts.PINLength, n.Int64()), nil
}
func (s *Service) newPINDifferentFrom(previousHash []byte) (string, error) {
	for range 8 {
		pin, err := s.newPIN()
		if err != nil {
			return "", err
		}
		if !hmac.Equal(previousHash, s.pinHash(pin)) {
			return pin, nil
		}
	}
	return "", errors.New("unable to generate a distinct PIN")
}
func randomID(random io.Reader) (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(random, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ValidateBearer extracts the opaque bearer value without accepting arbitrary
// client-supplied identity or tenant fields.
func ValidateBearer(header string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(header, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(header, prefix))
	}
	return ""
}
