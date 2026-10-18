package ordering

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// DefaultOrderReadTTL is a provisional engineering default, not Owner-approved
// retention policy (EXT-4). Configure once at construction, never per request.
const DefaultOrderReadTTL = 24 * time.Hour
const MinOrderReadTTL = time.Minute
const MaxOrderReadTTL = 7 * 24 * time.Hour

func NewWithOrderReadTTL(db *pgxpool.Pool, sessions *tablesession.Service, menus *catalog.Service, policy OrderCommitPolicy, ttl time.Duration) (*Service, error) {
	if ttl < MinOrderReadTTL || ttl > MaxOrderReadTTL {
		return nil, ErrInvalid
	}
	s, err := NewWithOrderPolicy(db, sessions, menus, policy)
	if err != nil {
		return nil, err
	}
	s.orderReadTTL = ttl
	return s, nil
}

type OrderReadGrant struct {
	OrderID   string    `json:"order_id"`
	Token     string    `json:"order_read_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// OrderReadStatus deliberately has no provider, audit, hold or other-order data.
// Amount is the immutable committed snapshot, never a current-menu calculation.
type OrderReadStatus struct {
	Version           int64  `json:"version"`
	PaymentStatus     string `json:"payment_status"`
	FulfillmentStatus string `json:"fulfillment_status"`
	TotalGrossJPY     int64  `json:"total_gross_jpy"`
	Currency          string `json:"currency"`
	IsTerminal        bool   `json:"is_terminal"`
}

func (s *Service) issueOrderReadTx(ctx context.Context, tx pgx.Tx, session tablesession.Session, orderID string) (*OrderReadGrant, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	var expiry time.Time
	err := tx.QueryRow(ctx, `INSERT INTO order_read_grants(token_hash,order_id,session_id,tenant_id,store_id,table_id,created_at,expires_at)
  SELECT $1,o.order_id,o.session_id,o.tenant_id,o.store_id,o.table_id,clock_timestamp(),clock_timestamp()+$6*interval '1 millisecond'
  FROM table_orders o WHERE o.order_id=$2 AND o.session_id=$3 AND o.tenant_id=$4 AND o.store_id=$5 AND o.table_id=$7
  RETURNING expires_at`, hash[:], orderID, session.ID, session.Scope.TenantID, session.Scope.StoreID, s.orderReadTTL.Milliseconds(), session.Scope.TableID).Scan(&expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	return &OrderReadGrant{OrderID: orderID, Token: token, ExpiresAt: expiry}, nil
}

// RequestOrderRead requires a current TABLE_JOIN of the order's OPEN session.
// Serialize with close/revocation and recheck time after acquiring locks. No
// catalog admission or payment mutation is needed to issue a read capability.
func (s *Service) RequestOrderRead(ctx context.Context, sessionID, joinToken, orderID string) (*OrderReadGrant, error) {
	pre, err := s.sessions.PrecheckGrant(ctx, sessionID, joinToken)
	if err != nil {
		return nil, ErrDenied
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	session, err := tablesession.LockAndValidateGrantTx(ctx, tx, pre.Scope, sessionID, joinToken)
	if err != nil || session.Status != tablesession.Open || session.Expired {
		return nil, ErrDenied
	}
	grant, err := s.issueOrderReadTx(ctx, tx, session, orderID)
	if err != nil {
		return nil, err
	}
	at, err := databaseClock(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err = tablesession.RecheckGrantTx(ctx, tx, pre.Scope, sessionID, joinToken, at); err != nil {
		return nil, ErrDenied
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return grant, nil
}

// ReadOrderStatus authorizes in the same statement as the projection. The grant
// joins the full immutable order scope; caller-supplied IDs never confer access.
// A valid ORDER_READ intentionally survives table close, unlike TABLE_JOIN.
func (s *Service) ReadOrderStatus(ctx context.Context, orderID, token string) (OrderReadStatus, error) {
	if len(token) != 43 {
		return OrderReadStatus{}, ErrDenied
	}
	hash := sha256.Sum256([]byte(token))
	var out OrderReadStatus
	err := s.db.QueryRow(ctx, `SELECT st.version,st.payment_status,st.fulfillment_status,o.total_gross_jpy,'JPY',
  st.payment_status='PAID' AND st.fulfillment_status='SERVED' AND NOT st.operational_hold AND NOT st.payment_review_required
  FROM order_read_grants g
  JOIN table_orders o USING(order_id,session_id,tenant_id,store_id,table_id)
  JOIN operation_order_state st USING(order_id,session_id,tenant_id,store_id,table_id)
  JOIN stores so USING(tenant_id,store_id) JOIN tenants t USING(tenant_id)
  WHERE g.token_hash=$1 AND g.order_id=$2 AND g.revoked_at IS NULL AND g.expires_at>clock_timestamp()
  AND so.enabled AND t.enabled`, hash[:], orderID).Scan(&out.Version, &out.PaymentStatus, &out.FulfillmentStatus, &out.TotalGrossJPY, &out.Currency, &out.IsTerminal)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderReadStatus{}, ErrDenied
	}
	return out, err
}

// RevokeOrderRead is an internal capability lifecycle operation. It has no
// guest HTTP write route and cannot revoke a different token or mutate orders.
func (s *Service) RevokeOrderRead(ctx context.Context, orderID, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.db.Exec(ctx, `UPDATE order_read_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE token_hash=$1 AND order_id=$2`, hash[:], orderID)
	return err
}
