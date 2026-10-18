package operations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tax"
)

const (
	maxCashOrders = 1000
	maxActionKey  = 128
)

var japanLocation = time.FixedZone("JST", 9*60*60)

func cashInvoiceDate(now time.Time) time.Time { return now.In(japanLocation) }

// Service contains P-79 operational state. It receives actors from a verified
// MOD-08 adapter; it does not authenticate identities or manufacture roles.
type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) (*Service, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	return &Service{db: db}, nil
}

func validMode(m CashMode) bool { return m == Prepaid || m == PayLater }
func validActor(s string) bool  { return strings.TrimSpace(s) != "" && len(s) <= 200 }
func validKey(s string) bool    { return strings.TrimSpace(s) != "" && len(s) <= maxActionKey }
func randomID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Service) ReadCashSettings(ctx context.Context, scope StoreScope) (CashSettings, error) {
	if !validStoreScope(scope) {
		return CashSettings{}, ErrInvalid
	}
	var enabled bool
	var mode CashMode
	var version int64
	err := s.db.QueryRow(ctx, `SELECT c.enabled,c.mode,c.version FROM store_cash_settings c
		JOIN stores st USING(tenant_id,store_id) JOIN tenants t USING(tenant_id)
		WHERE c.tenant_id=$1 AND c.store_id=$2 AND st.enabled AND t.enabled`, scope.TenantID, scope.StoreID).Scan(&enabled, &mode, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stores st JOIN tenants t USING(tenant_id) WHERE st.tenant_id=$1 AND st.store_id=$2 AND st.enabled AND t.enabled)`, scope.TenantID, scope.StoreID).Scan(&exists); e != nil {
			return CashSettings{}, e
		}
		if !exists {
			return CashSettings{}, ErrNotFound
		}
		return CashSettings{Enabled: false, Mode: Prepaid, Version: 0}, nil
	}
	return CashSettings{Enabled: enabled, Mode: mode, Version: version}, err
}

func (s *Service) SetCashSettings(ctx context.Context, scope StoreScope, actor string, enabled bool, mode CashMode, expected int64) (CashSettings, error) {
	if !validStoreScope(scope) || !validActor(actor) || !validMode(mode) || expected < 0 || expected == math.MaxInt64 {
		return CashSettings{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return CashSettings{}, err
	}
	defer tx.Rollback(ctx)
	var ok bool
	err = tx.QueryRow(ctx, `SELECT st.enabled AND t.enabled FROM stores st JOIN tenants t USING(tenant_id) WHERE st.tenant_id=$1 AND st.store_id=$2 FOR UPDATE OF st`, scope.TenantID, scope.StoreID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return CashSettings{}, ErrNotFound
	}
	if err != nil {
		return CashSettings{}, err
	}
	if !ok {
		return CashSettings{}, ErrDenied
	}
	var beforeEnabled bool
	var beforeMode CashMode
	var version int64
	err = tx.QueryRow(ctx, `SELECT enabled,mode,version FROM store_cash_settings WHERE tenant_id=$1 AND store_id=$2 FOR UPDATE`, scope.TenantID, scope.StoreID).Scan(&beforeEnabled, &beforeMode, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		version = 0
	} else if err != nil {
		return CashSettings{}, err
	}
	if version != expected {
		return CashSettings{}, ErrConflict
	}
	newVersion := version + 1
	_, err = tx.Exec(ctx, `INSERT INTO store_cash_settings(tenant_id,store_id,enabled,mode,version,updated_by,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()) ON CONFLICT(tenant_id,store_id) DO UPDATE SET enabled=EXCLUDED.enabled,mode=EXCLUDED.mode,version=EXCLUDED.version,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`, scope.TenantID, scope.StoreID, enabled, string(mode), newVersion, actor)
	if err != nil {
		return CashSettings{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO store_cash_settings_audit(tenant_id,store_id,actor_id,before_enabled,before_mode,before_version,after_enabled,after_mode,after_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, scope.TenantID, scope.StoreID, actor, nullableBool(version > 0, beforeEnabled), nullableMode(version > 0, beforeMode), version, enabled, string(mode), newVersion)
	if err != nil {
		return CashSettings{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CashSettings{}, err
	}
	return CashSettings{Enabled: enabled, Mode: mode, Version: newVersion}, nil
}
func nullableBool(ok, v bool) any {
	if !ok {
		return nil
	}
	return v
}
func nullableMode(ok bool, v CashMode) any {
	if !ok {
		return nil
	}
	return string(v)
}
func validStoreScope(s StoreScope) bool {
	return strings.TrimSpace(s.TenantID) != "" && strings.TrimSpace(s.StoreID) != ""
}
func validTableScope(s TableScope) bool {
	return validStoreScope(StoreScope{s.TenantID, s.StoreID}) && strings.TrimSpace(s.TableID) != ""
}

// InitOrderTx is called by ordering inside the same transaction that inserts
// the immutable order. The order path already holds store SHARE before the
// table/session/cart locks; this hook never takes a store UPDATE lock.
func (s *Service) InitOrderTx(ctx context.Context, tx pgx.Tx, order ordering.CommittedOrder) error {
	if order.ID == "" || order.SessionID == "" || !validTableScope(TableScope{order.Scope.TenantID, order.Scope.StoreID, order.Scope.TableID}) {
		return ErrInvalid
	}
	var mode CashMode
	var enabled bool
	if order.PaymentMethod == string(ordering.Cash) {
		err := tx.QueryRow(ctx, `SELECT enabled,mode FROM store_cash_settings WHERE tenant_id=$1 AND store_id=$2`, order.Scope.TenantID, order.Scope.StoreID).Scan(&enabled, &mode)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!enabled || !validMode(mode))) {
			return ordering.ErrPaymentUnavailable
		}
		if err != nil {
			return err
		}
	} else if order.PaymentMethod != string(ordering.Stripe) {
		return ordering.ErrPaymentUnavailable
	}
	payment := Pending
	var cashMode any
	if order.PaymentMethod == string(ordering.Cash) {
		payment = Unpaid
		cashMode = string(mode)
	}
	fulfillment := Waiting
	_, err := tx.Exec(ctx, `INSERT INTO operation_order_state(order_id,session_id,tenant_id,store_id,table_id,generation,payment_method,payment_status,cash_mode,fulfillment_status,requires_staff_acceptance,staff_accepted,version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,false,1)`, order.ID, order.SessionID, order.Scope.TenantID, order.Scope.StoreID, order.Scope.TableID, order.Generation, order.PaymentMethod, string(payment), cashMode, string(fulfillment), order.RequiresStaffAcceptance)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,after_payment_status,after_fulfillment_status,state_version) VALUES($1,'system','ORDER_CREATED',$2,$3,1)`, order.ID, string(payment), string(fulfillment)); err != nil {
		return err
	}
	// A PAY_LATER CASH order is accepted for fulfillment immediately. PREPAID
	// and Stripe remain withheld until their independent payment gate opens.
	if order.PaymentMethod == string(ordering.Cash) && mode == PayLater && !order.RequiresStaffAcceptance {
		return releaseOrder(ctx, tx, order.ID, "system")
	}
	return nil
}

func releaseOrder(ctx context.Context, tx pgx.Tx, orderID, actor string) error {
	var method string
	var paid bool
	var staffRequired, staffAccepted, held bool
	var cashMode CashMode
	var fulfillment FulfillmentStatus
	var session, tenant, store, table string
	var version int64
	var review bool
	err := tx.QueryRow(ctx, `SELECT payment_method,payment_status='PAID',requires_staff_acceptance,staff_accepted,operational_hold,payment_review_required,coalesce(cash_mode,''),fulfillment_status,session_id,tenant_id,store_id,table_id,version FROM operation_order_state WHERE order_id=$1 FOR UPDATE`, orderID).
		Scan(&method, &paid, &staffRequired, &staffAccepted, &held, &review, &cashMode, &fulfillment, &session, &tenant, &store, &table, &version)
	if err != nil {
		return err
	}
	paymentOpen := (method == string(ordering.Stripe) && paid) || (method == string(ordering.Cash) && (cashMode == PayLater || paid))
	if held || review || !paymentOpen || (staffRequired && !staffAccepted) || fulfillment != Waiting {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_kds_cards(order_id,session_id,tenant_id,store_id,table_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(order_id) DO NOTHING`, orderID, session, tenant, store, table)
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE operation_order_state SET fulfillment_status='ACCEPTED',version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$2`, orderID, version)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_outbox(order_id,event_type) VALUES($1,'KDS_RELEASED') ON CONFLICT(order_id,event_type) DO NOTHING`, orderID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_fulfillment_status,after_fulfillment_status,state_version) VALUES($1,$2,'KDS_RELEASED',$3,'ACCEPTED',$4)`, orderID, actor, string(fulfillment), version+1)
	return err
}

func (s *Service) CheckClose(ctx context.Context, tx pgx.Tx, session tablesession.Session) error {
	var bad int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM table_orders o LEFT JOIN operation_order_state st ON st.order_id=o.order_id
		WHERE o.session_id=$1 AND (st.order_id IS NULL OR NOT ((st.payment_status='PAID' AND st.fulfillment_status='SERVED') OR (st.fulfillment_status='CANCELLED' AND (st.payment_status='REFUNDED' OR (st.payment_method='CASH' AND st.payment_status='UNPAID')))) OR st.operational_hold OR st.payment_review_required OR EXISTS(SELECT 1 FROM operation_refunds r WHERE r.order_id=o.order_id AND r.status<>'SUCCEEDED'))`, session.ID).Scan(&bad)
	if err != nil {
		return err
	}
	if bad != 0 {
		return tablesession.ErrCloseBlocked
	}
	return nil
}

func (s *Service) ListTables(ctx context.Context, scope StoreScope) ([]TableSummary, error) {
	if !validStoreScope(scope) {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT t.table_id,t.name,coalesce(ts.session_id,''),coalesce(ts.status,'IDLE'),coalesce(ts.qr_mode,st.qr_entry_mode),ts.expires_at,
		coalesce(sum(CASE WHEN os.payment_method='CASH' AND os.payment_status='UNPAID' AND os.fulfillment_status IS DISTINCT FROM 'CANCELLED' THEN coalesce(os.effective_total_gross_jpy,o.total_gross_jpy) ELSE 0 END),0),count(o.order_id)
		FROM dining_tables t JOIN stores st USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		LEFT JOIN table_sessions ts ON ts.tenant_id=t.tenant_id AND ts.store_id=t.store_id AND ts.table_id=t.table_id AND ts.status='OPEN'
		LEFT JOIN table_orders o ON o.session_id=ts.session_id LEFT JOIN operation_order_state os ON os.order_id=o.order_id
		WHERE t.tenant_id=$1 AND t.store_id=$2 AND t.enabled AND st.enabled AND st.operating AND n.enabled
		GROUP BY t.table_id,t.name,ts.session_id,ts.status,ts.qr_mode,st.qr_entry_mode,ts.expires_at ORDER BY t.table_id`, scope.TenantID, scope.StoreID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TableSummary{}
	for rows.Next() {
		var x TableSummary
		var mode string
		var exp *time.Time
		if err = rows.Scan(&x.TableID, &x.Name, &x.SessionID, &x.Status, &mode, &exp, &x.UnpaidCashJPY, &x.OrderCount); err != nil {
			return nil, err
		}
		x.Mode = tablesession.Mode(mode)
		x.ExpiresAt = exp
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Service) ReadSession(ctx context.Context, scope TableScope, sessionID string) (SessionDetail, error) {
	if !validTableScope(scope) || sessionID == "" {
		return SessionDetail{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return SessionDetail{}, err
	}
	defer tx.Rollback(ctx)
	var d SessionDetail
	var mode string
	err = tx.QueryRow(ctx, `SELECT ts.session_id,ts.status,ts.qr_mode,ts.expires_at FROM table_sessions ts JOIN stores st USING(tenant_id,store_id) JOIN tenants t USING(tenant_id) JOIN dining_tables dt USING(tenant_id,store_id,table_id)
		WHERE ts.session_id=$1 AND ts.tenant_id=$2 AND ts.store_id=$3 AND ts.table_id=$4 AND st.enabled AND st.operating AND t.enabled AND dt.enabled`, sessionID, scope.TenantID, scope.StoreID, scope.TableID).Scan(&d.SessionID, &d.Status, &mode, &d.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionDetail{}, ErrNotFound
	}
	if err != nil {
		return SessionDetail{}, err
	}
	d.Mode = tablesession.Mode(mode)
	d.Orders, err = readOrders(ctx, tx, scope, sessionID, false)
	if err != nil {
		return SessionDetail{}, err
	}
	rows, err := tx.Query(ctx, `SELECT receipt_id,session_id,order_ids,due_jpy,tendered_jpy,change_jpy,cashier_id,register_session_ref,created_at,tax_invoice FROM cash_settlements WHERE session_id=$1 ORDER BY created_at,receipt_id`, sessionID)
	if err != nil {
		return SessionDetail{}, err
	}
	defer rows.Close()
	d.Receipts = []Receipt{}
	for rows.Next() {
		var r Receipt
		var invoice []byte
		if err = rows.Scan(&r.ID, &r.SessionID, &r.OrderIDs, &r.DueJPY, &r.TenderedJPY, &r.ChangeJPY, &r.CashierID, &r.RegisterSessionRef, &r.CreatedAt, &invoice); err != nil {
			return SessionDetail{}, err
		}
		if err = json.Unmarshal(invoice, &r.Tax); err != nil {
			return SessionDetail{}, err
		}
		d.Receipts = append(d.Receipts, r)
	}
	if err = rows.Err(); err != nil {
		return SessionDetail{}, err
	}
	d.CloseBlockers = closeBlockers(d.Orders)
	if err = tx.Commit(ctx); err != nil {
		return SessionDetail{}, err
	}
	return d, nil
}

func (s *Service) ListKDS(ctx context.Context, scope StoreScope) ([]OrderState, error) {
	if !validStoreScope(scope) {
		return nil, ErrInvalid
	}
	return readOrders(ctx, s.db, TableScope{TenantID: scope.TenantID, StoreID: scope.StoreID}, "", true)
}

func readOrders(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, scope TableScope, sessionID string, releasedOnly bool) ([]OrderState, error) {
	return readOrdersMode(ctx, q, scope, sessionID, releasedOnly, false)
}

func readOrdersMode(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, scope TableScope, sessionID string, releasedOnly, reviewOnly bool) ([]OrderState, error) {
	args := []any{scope.TenantID, scope.StoreID}
	where := `o.tenant_id=$1 AND o.store_id=$2`
	if scope.TableID != "" {
		args = append(args, scope.TableID)
		where += fmt.Sprintf(" AND o.table_id=$%d", len(args))
	}
	if sessionID != "" {
		args = append(args, sessionID)
		where += fmt.Sprintf(" AND o.session_id=$%d", len(args))
	}
	if releasedOnly {
		where += ` AND EXISTS (SELECT 1 FROM operation_kds_cards kc WHERE kc.order_id=o.order_id) AND (st.fulfillment_status NOT IN ('SERVED','CANCELLED') OR (st.fulfillment_status='CANCELLED' AND st.order_updated)) AND NOT st.operational_hold AND NOT st.payment_review_required`
	}
	if reviewOnly {
		where += ` AND ((st.payment_status='PAID' AND st.payment_review_required AND st.fulfillment_status='WAITING' AND st.review_decision='UNRESOLVED' AND NOT EXISTS(SELECT 1 FROM operation_kds_cards kc WHERE kc.order_id=o.order_id)) OR EXISTS(SELECT 1 FROM operation_refunds r WHERE r.order_id=o.order_id AND r.status<>'SUCCEEDED')) AND dt.enabled AND EXISTS(SELECT 1 FROM stores rs JOIN tenants rt USING(tenant_id) WHERE rs.tenant_id=o.tenant_id AND rs.store_id=o.store_id AND rs.enabled AND rs.operating AND rt.enabled)`
	}
	rows, err := q.Query(ctx, `SELECT o.order_id,o.session_id,o.table_id,dt.name,o.generation,o.payment_method,st.payment_status,st.fulfillment_status,o.requires_staff_acceptance,coalesce(st.staff_accepted,false),coalesce(st.cash_mode,''),coalesce(st.operational_hold,false),coalesce(st.payment_review_required,false),coalesce(st.version,0),o.total_gross_jpy,o.created_at,o.quote_snapshot,coalesce(st.review_decision,'UNRESOLVED'),coalesce(st.effective_total_gross_jpy,o.total_gross_jpy),coalesce(st.order_updated,false),coalesce(st.cancelled_quantities,'[]'::jsonb),coalesce((SELECT jsonb_agg(jsonb_build_object('id',r.refund_id,'amount_jpy',r.amount_jpy,'status',r.status,'method',r.method,'safe_error_code',
		CASE WHEN r.status IN ('REQUESTED','PROCESSING') THEN (SELECT w.safe_error_code FROM stripe_payment_work w
		WHERE w.refund_id=r.refund_id AND w.refund_generation=r.generation AND w.status IN ('READY','LEASED')
		AND w.safe_error_code IN ('REFUND_DISPATCH_PENDING','HOLD_RECONCILIATION_NEEDED') ORDER BY w.updated_at DESC LIMIT 1) END) ORDER BY r.created_at,r.refund_id) FROM operation_refunds r WHERE r.order_id=o.order_id),'[]'::jsonb)
		FROM table_orders o JOIN dining_tables dt USING(tenant_id,store_id,table_id) LEFT JOIN operation_order_state st USING(order_id) WHERE `+where+` ORDER BY o.created_at,o.order_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrderState{}
	for rows.Next() {
		var v OrderState
		var cash string
		var hold bool
		var created time.Time
		var raw, cancelledJSON, refundsJSON []byte
		var payment, fulfill *string
		if err = rows.Scan(&v.ID, &v.SessionID, &v.TableID, &v.TableName, &v.Generation, &v.PaymentMethod, &payment, &fulfill, &v.RequiresStaffAcceptance, &v.StaffAccepted, &cash, &hold, &v.PaymentReviewRequired, &v.Version, &v.TotalGrossJPY, &created, &raw, &v.ReviewDecision, &v.EffectiveTotalGrossJPY, &v.OrderUpdated, &cancelledJSON, &refundsJSON); err != nil {
			return nil, err
		}
		v.CreatedAt = created
		v.CashMode = CashMode(cash)
		v.OperationalHold = hold
		if payment != nil {
			v.PaymentStatus = PaymentStatus(*payment)
		} else {
			v.PaymentStatus = Unknown
		}
		if fulfill != nil {
			v.FulfillmentStatus = FulfillmentStatus(*fulfill)
		} else {
			v.FulfillmentStatus = Waiting
			v.BlockedReason = "OPERATION_STATE_MISSING"
		}
		if v.OperationalHold || v.PaymentReviewRequired {
			v.BlockedReason = "OPERATION_HOLD"
		}
		var snap struct {
			Quote struct {
				Lines []struct {
					Name         string `json:"name"`
					Quantity     int64  `json:"quantity"`
					BaseGrossJPY int64  `json:"base_gross_jpy"`
					Options      []struct {
						GroupKey      string `json:"group_key"`
						Name          string `json:"name"`
						PriceGrossJPY int64  `json:"price_gross_jpy"`
					} `json:"options"`
				} `json:"lines"`
			} `json:"quote"`
		}
		if err = json.Unmarshal(raw, &snap); err != nil {
			return nil, err
		}
		var cancelled []int64
		if err = json.Unmarshal(cancelledJSON, &cancelled); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(refundsJSON, &v.Refunds); err != nil {
			return nil, err
		}
		for i, l := range snap.Quote.Lines {
			line := OrderLine{LineIndex: i, OriginalQuantity: l.Quantity, ProductName: l.Name, Quantity: l.Quantity, UnitGrossJPY: l.BaseGrossJPY, Options: []OrderOption{}}
			for _, o := range l.Options {
				if o.PriceGrossJPY < 0 || line.UnitGrossJPY > math.MaxInt64-o.PriceGrossJPY {
					return nil, ErrInvalid
				}
				line.UnitGrossJPY += o.PriceGrossJPY
				// Published order snapshots retain option keys and option display
				// names, but not the historical group display name. Keep the group
				// label empty rather than exposing an internal key as customer copy.
				line.Options = append(line.Options, OrderOption{OptionName: o.Name, PriceDeltaMinor: o.PriceGrossJPY})
			}
			if l.Quantity < 0 || (line.UnitGrossJPY != 0 && l.Quantity > math.MaxInt64/line.UnitGrossJPY) {
				return nil, ErrInvalid
			}
			if len(cancelled) > 0 {
				if len(cancelled) != len(snap.Quote.Lines) || cancelled[i] < 0 || cancelled[i] > l.Quantity {
					return nil, ErrInvalid
				}
				line.Quantity -= cancelled[i]
			}
			line.LineGrossJPY = line.UnitGrossJPY * line.Quantity
			v.Lines = append(v.Lines, line)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func closeBlockers(orders []OrderState) []string {
	out := []string{}
	for _, o := range orders {
		for _, r := range o.Refunds {
			if r.Status != "SUCCEEDED" {
				out = append(out, "REFUND_UNRESOLVED")
				break
			}
		}
	}
	for _, o := range orders {
		if o.PaymentStatus != Paid && !(o.FulfillmentStatus == Cancelled && (o.PaymentStatus == Refunded || (o.PaymentMethod == "CASH" && o.PaymentStatus == Unpaid))) {
			out = append(out, "UNPAID_OR_UNKNOWN_PAYMENT")
			break
		}
	}
	for _, o := range orders {
		if o.FulfillmentStatus != Served && o.FulfillmentStatus != Cancelled {
			out = append(out, "UNSERVED_OR_UNKNOWN_FULFILLMENT")
			break
		}
	}
	for _, o := range orders {
		if o.OperationalHold || o.PaymentReviewRequired {
			out = append(out, "OPERATION_HOLD")
			break
		}
	}
	return out
}

func (s *Service) ReadGuestOperations(ctx context.Context, sessionID, grant string) (GuestOperations, error) {
	var scope tablesession.Scope
	err := s.db.QueryRow(ctx, `SELECT ts.tenant_id,ts.store_id,ts.table_id FROM table_sessions ts WHERE ts.session_id=$1`, sessionID).Scan(&scope.TenantID, &scope.StoreID, &scope.TableID)
	if errors.Is(err, pgx.ErrNoRows) {
		return GuestOperations{}, ErrDenied
	}
	if err != nil {
		return GuestOperations{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return GuestOperations{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tablesession.LockAndValidateGrantTx(ctx, tx, scope, sessionID, grant); err != nil {
		return GuestOperations{}, ErrDenied
	}
	var mode CashMode
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT enabled,mode FROM store_cash_settings WHERE tenant_id=$1 AND store_id=$2`, scope.TenantID, scope.StoreID).Scan(&enabled, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		enabled = false
	} else if err != nil {
		return GuestOperations{}, err
	}
	rows, err := readOrders(ctx, tx, TableScope{TenantID: scope.TenantID, StoreID: scope.StoreID, TableID: scope.TableID}, sessionID, false)
	if err != nil {
		return GuestOperations{}, err
	}
	out := GuestOperations{Orders: make([]GuestOrderState, 0, len(rows)), CashEnabled: enabled}
	for _, o := range rows {
		payment := o.PaymentStatus
		if payment == RefundPending || payment == Refunded {
			payment = Unknown
		}
		out.Orders = append(out.Orders, GuestOrderState{ID: o.ID, PaymentStatus: payment, FulfillmentStatus: o.FulfillmentStatus, RequiresStaffAcceptance: o.RequiresStaffAcceptance, StaffAccepted: o.StaffAccepted, CashMode: o.CashMode})
	}
	var trustedAt time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&trustedAt); err != nil {
		return GuestOperations{}, err
	}
	if _, err = tablesession.RecheckGrantTx(ctx, tx, scope, sessionID, grant, trustedAt); err != nil {
		return GuestOperations{}, ErrDenied
	}
	if err = tx.Commit(ctx); err != nil {
		return GuestOperations{}, err
	}
	return out, nil
}

type cashOrder struct {
	id       string
	amount   int64
	held     bool
	snapshot tax.SnapshotRecord
}

func (s *Service) PreviewCash(ctx context.Context, scope TableScope, sessionID string) (CashPreview, error) {
	if !validTableScope(scope) || sessionID == "" {
		return CashPreview{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return CashPreview{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockSession(ctx, tx, scope, sessionID, "FOR SHARE"); err != nil {
		return CashPreview{}, err
	}
	orders, invoice, amount, err := currentCashOrders(ctx, tx, scope, sessionID)
	if err != nil {
		return CashPreview{}, err
	}
	if len(orders) == 0 {
		return CashPreview{}, ErrNotReady
	}
	fingerprint, err := cashFingerprint(sessionID, orders, amount)
	if err != nil {
		return CashPreview{}, err
	}
	ids := make([]string, len(orders))
	for i, o := range orders {
		ids[i] = o.id
	}
	if err = tx.Commit(ctx); err != nil {
		return CashPreview{}, err
	}
	return CashPreview{SessionID: sessionID, Fingerprint: fingerprint, OrderIDs: ids, DueJPY: amount, Tax: invoice}, nil
}

func (s *Service) SettleCash(ctx context.Context, input CashSettlementRequest) (Receipt, error) {
	if !validTableScope(input.Scope) || input.SessionID == "" || !validActor(input.Actor) || !validKey(input.IdempotencyKey) || input.TenderedJPY < 0 || strings.TrimSpace(input.RegisterSessionRef) == "" || len(input.RegisterSessionRef) > 200 || input.Fingerprint == "" {
		return Receipt{}, ErrInvalid
	}
	digest, err := jsonDigest(struct {
		Session, Fingerprint, Register string
		Tender                         int64
	}{input.SessionID, input.Fingerprint, input.RegisterSessionRef, input.TenderedJPY})
	if err != nil {
		return Receipt{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback(ctx)
	status, err := lockSessionStatus(ctx, tx, input.Scope, input.SessionID, "FOR UPDATE")
	if err != nil {
		return Receipt{}, err
	}
	var prior Receipt
	var priorDigest []byte
	var invoiceJSON []byte
	err = tx.QueryRow(ctx, `SELECT receipt_id,session_id,order_ids,due_jpy,tendered_jpy,change_jpy,cashier_id,register_session_ref,created_at,request_digest,tax_invoice FROM cash_settlements WHERE session_id=$1 AND idempotency_key=$2`, input.SessionID, input.IdempotencyKey).
		Scan(&prior.ID, &prior.SessionID, &prior.OrderIDs, &prior.DueJPY, &prior.TenderedJPY, &prior.ChangeJPY, &prior.CashierID, &prior.RegisterSessionRef, &prior.CreatedAt, &priorDigest, &invoiceJSON)
	if err == nil {
		if !equalHash(priorDigest, digest) {
			return Receipt{}, ErrIdempotencyConflict
		}
		if e := json.Unmarshal(invoiceJSON, &prior.Tax); e != nil {
			return Receipt{}, e
		}
		if err = tx.Commit(ctx); err != nil {
			return Receipt{}, err
		}
		return prior, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, err
	}
	// A committed receipt is replayable after the table was safely closed. A
	// closed session may not create a new settlement, even if new work appears.
	if status != "OPEN" {
		return Receipt{}, ErrConflict
	}
	orders, invoice, due, err := currentCashOrders(ctx, tx, input.Scope, input.SessionID)
	if err != nil {
		return Receipt{}, err
	}
	if len(orders) == 0 {
		return Receipt{}, ErrReconfirm
	}
	want, err := cashFingerprint(input.SessionID, orders, due)
	if err != nil {
		return Receipt{}, err
	}
	if want != input.Fingerprint {
		return Receipt{}, ErrReconfirm
	}
	if input.TenderedJPY < due {
		return Receipt{}, ErrInvalid
	}
	receiptID, err := randomID()
	if err != nil {
		return Receipt{}, err
	}
	ids := make([]string, len(orders))
	for i, o := range orders {
		ids[i] = o.id
	}
	invoiceJSON, err = json.Marshal(invoice)
	if err != nil {
		return Receipt{}, err
	}
	change := input.TenderedJPY - due
	var createdAt time.Time
	err = tx.QueryRow(ctx, `INSERT INTO cash_settlements(receipt_id,session_id,tenant_id,store_id,table_id,idempotency_key,request_digest,order_ids,due_jpy,tendered_jpy,change_jpy,cashier_id,register_session_ref,tax_invoice)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb) RETURNING created_at`, receiptID, input.SessionID, input.Scope.TenantID, input.Scope.StoreID, input.Scope.TableID, input.IdempotencyKey, digest, ids, due, input.TenderedJPY, change, input.Actor, input.RegisterSessionRef, invoiceJSON).Scan(&createdAt)
	if err != nil {
		return Receipt{}, err
	}
	for _, o := range orders {
		_, err = tx.Exec(ctx, `INSERT INTO cash_settlement_allocations(receipt_id,order_id,session_id,tenant_id,store_id,table_id,amount_jpy) VALUES($1,$2,$3,$4,$5,$6,$7)`, receiptID, o.id, input.SessionID, input.Scope.TenantID, input.Scope.StoreID, input.Scope.TableID, o.amount)
		if err != nil {
			return Receipt{}, err
		}
		var version int64
		if err = tx.QueryRow(ctx, `UPDATE operation_order_state SET payment_status='PAID',version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND payment_status='UNPAID' RETURNING version`, o.id).Scan(&version); err != nil {
			return Receipt{}, ErrReconfirm
		}
		if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_payment_status,after_payment_status,state_version) VALUES($1,$2,'PAYMENT_SETTLED','UNPAID','PAID',$3)`, o.id, input.Actor, version); err != nil {
			return Receipt{}, err
		}
		if err = releaseOrder(ctx, tx, o.id, input.Actor); err != nil {
			return Receipt{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Receipt{}, err
	}
	return Receipt{ID: receiptID, SessionID: input.SessionID, OrderIDs: ids, DueJPY: due, TenderedJPY: input.TenderedJPY, ChangeJPY: change, CashierID: input.Actor, RegisterSessionRef: input.RegisterSessionRef, CreatedAt: createdAt, Tax: invoice}, nil
}

func currentCashOrders(ctx context.Context, tx pgx.Tx, scope TableScope, sessionID string) ([]cashOrder, tax.InvoiceRecord, int64, error) {
	rows, err := tx.Query(ctx, `SELECT o.order_id,coalesce(st.effective_total_gross_jpy,o.total_gross_jpy),st.operational_hold,coalesce(st.effective_tax_snapshot,o.tax_snapshot) FROM table_orders o JOIN operation_order_state st USING(order_id)
		WHERE o.session_id=$1 AND o.tenant_id=$2 AND o.store_id=$3 AND o.table_id=$4 AND st.payment_method='CASH' AND st.payment_status='UNPAID' AND st.fulfillment_status IS DISTINCT FROM 'CANCELLED' ORDER BY o.order_id LIMIT $5`, sessionID, scope.TenantID, scope.StoreID, scope.TableID, maxCashOrders+1)
	if err != nil {
		return nil, tax.InvoiceRecord{}, 0, err
	}
	defer rows.Close()
	orders := []cashOrder{}
	var amount int64
	invoiceOrders := []tax.InvoiceOrder{}
	for rows.Next() {
		var o cashOrder
		var raw []byte
		if err = rows.Scan(&o.id, &o.amount, &o.held, &raw); err != nil {
			return nil, tax.InvoiceRecord{}, 0, err
		}
		if o.held {
			return nil, tax.InvoiceRecord{}, 0, ErrBlocked
		}
		if len(orders) >= maxCashOrders {
			return nil, tax.InvoiceRecord{}, 0, ErrInvalid
		}
		if o.amount < 0 || amount > math.MaxInt64-o.amount {
			return nil, tax.InvoiceRecord{}, 0, ErrInvalid
		}
		amount += o.amount
		if err = json.Unmarshal(raw, &o.snapshot); err != nil {
			return nil, tax.InvoiceRecord{}, 0, err
		}
		orders = append(orders, o)
		invoiceOrders = append(invoiceOrders, tax.InvoiceOrder{OrderID: o.id, Snapshot: o.snapshot})
	}
	if err = rows.Err(); err != nil {
		return nil, tax.InvoiceRecord{}, 0, err
	}
	if len(orders) == 0 {
		return orders, tax.InvoiceRecord{}, 0, nil
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, tax.InvoiceRecord{}, 0, err
	}
	invoice, err := tax.AggregateInvoice(tax.Scope{TenantID: scope.TenantID, StoreID: scope.StoreID}, cashInvoiceDate(now), invoiceOrders)
	if err != nil {
		return nil, tax.InvoiceRecord{}, 0, err
	}
	return orders, invoice, amount, nil
}

func cashFingerprint(sessionID string, orders []cashOrder, amount int64) (string, error) {
	lines := make([]struct {
		ID     string
		Amount int64
	}, len(orders))
	for i, o := range orders {
		lines[i] = struct {
			ID     string
			Amount int64
		}{o.id, o.amount}
	}
	digest, err := jsonDigest(struct {
		Session string
		Orders  any
		Due     int64
	}{sessionID, lines, amount})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest), nil
}

func lockSession(ctx context.Context, tx pgx.Tx, scope TableScope, sessionID, lock string) error {
	status, err := lockSessionStatus(ctx, tx, scope, sessionID, lock)
	if err != nil {
		return err
	}
	if status != "OPEN" {
		return ErrConflict
	}
	return nil
}

func lockSessionStatus(ctx context.Context, tx pgx.Tx, scope TableScope, sessionID, lock string) (string, error) {
	if lock != "FOR SHARE" && lock != "FOR UPDATE" {
		return "", ErrInvalid
	}
	var status string
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT ts.status,(t.enabled AND st.enabled AND st.operating AND n.enabled) FROM table_sessions ts JOIN dining_tables t USING(tenant_id,store_id,table_id) JOIN stores st USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE ts.session_id=$1 AND ts.tenant_id=$2 AND ts.store_id=$3 AND ts.table_id=$4 `+lock+` OF ts`, sessionID, scope.TenantID, scope.StoreID, scope.TableID).Scan(&status, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !enabled {
		return "", ErrDenied
	}
	return status, nil
}

func jsonDigest(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(b)
	return h[:], nil
}
func equalHash(a, b []byte) bool {
	return len(a) == len(b) && strings.EqualFold(hex.EncodeToString(a), hex.EncodeToString(b))
}

func (s *Service) AcceptOrder(ctx context.Context, input OrderActionRequest) (OrderState, error) {
	return s.mutateOrder(ctx, input, "ACCEPT")
}
func (s *Service) AdvanceFulfillment(ctx context.Context, input OrderActionRequest) (OrderState, error) {
	return s.mutateOrder(ctx, input, "FULFILLMENT")
}

func (s *Service) mutateOrder(ctx context.Context, input OrderActionRequest, action string) (OrderState, error) {
	if !validTableScope(input.Scope) || input.SessionID == "" || input.OrderID == "" || !validActor(input.Actor) || !validKey(input.IdempotencyKey) || input.ExpectedVersion < 1 {
		return OrderState{}, ErrInvalid
	}
	digest, err := jsonDigest(struct {
		Action, Target, Session, Order string
		Version                        int64
	}{action, string(input.Target), input.SessionID, input.OrderID, input.ExpectedVersion})
	if err != nil {
		return OrderState{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return OrderState{}, err
	}
	defer tx.Rollback(ctx)
	sessionStatus, err := lockSessionStatus(ctx, tx, input.Scope, input.SessionID, "FOR UPDATE")
	if err != nil {
		return OrderState{}, err
	}
	if sessionStatus != "OPEN" {
		// A manager may fulfill a reviewed late payment without reopening its
		// historical session or restoring any guest grant. Other actions stay shut.
		var approved bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_order_state WHERE order_id=$1 AND session_id=$2 AND tenant_id=$3 AND store_id=$4 AND table_id=$5 AND review_decision='APPROVED')`, input.OrderID, input.SessionID, input.Scope.TenantID, input.Scope.StoreID, input.Scope.TableID).Scan(&approved); err != nil {
			return OrderState{}, err
		}
		if action != "FULFILLMENT" || !approved {
			return OrderState{}, ErrConflict
		}
	}
	var priorDigest []byte
	err = tx.QueryRow(ctx, `SELECT request_digest FROM operation_action_idempotency WHERE session_id=$1 AND order_id=$2 AND action=$3 AND idempotency_key=$4`, input.SessionID, input.OrderID, action, input.IdempotencyKey).Scan(&priorDigest)
	if err == nil {
		if !equalHash(priorDigest, digest) {
			return OrderState{}, ErrIdempotencyConflict
		}
		out, e := readOneOrder(ctx, tx, input.Scope, input.SessionID, input.OrderID)
		if e != nil {
			return OrderState{}, e
		}
		if e = tx.Commit(ctx); e != nil {
			return OrderState{}, e
		}
		return out, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, err
	}
	var status FulfillmentStatus
	var required, accepted, held bool
	var version int64
	err = tx.QueryRow(ctx, `SELECT fulfillment_status,requires_staff_acceptance,staff_accepted,operational_hold,version FROM operation_order_state WHERE order_id=$1 AND session_id=$2 AND tenant_id=$3 AND store_id=$4 AND table_id=$5 FOR UPDATE`, input.OrderID, input.SessionID, input.Scope.TenantID, input.Scope.StoreID, input.Scope.TableID).Scan(&status, &required, &accepted, &held, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderState{}, ErrNotFound
	}
	if err != nil {
		return OrderState{}, err
	}
	if held {
		return OrderState{}, ErrBlocked
	}
	if version != input.ExpectedVersion {
		return OrderState{}, ErrConflict
	}
	newVersion := version
	if action == "ACCEPT" {
		if !required || accepted || status != Waiting {
			return OrderState{}, ErrNotReady
		}
		command, e := tx.Exec(ctx, `UPDATE operation_order_state SET staff_accepted=true,version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$2`, input.OrderID, version)
		if e != nil {
			return OrderState{}, e
		}
		if command.RowsAffected() != 1 {
			return OrderState{}, ErrConflict
		}
		newVersion++
		if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_fulfillment_status,after_fulfillment_status,state_version) VALUES($1,$2,'STAFF_ACCEPTED',$3,$3,$4)`, input.OrderID, input.Actor, string(status), newVersion); err != nil {
			return OrderState{}, err
		}
		if err = releaseOrder(ctx, tx, input.OrderID, input.Actor); err != nil {
			return OrderState{}, err
		}
	} else {
		var updated bool
		if err = tx.QueryRow(ctx, `SELECT order_updated FROM operation_order_state WHERE order_id=$1`, input.OrderID).Scan(&updated); err != nil {
			return OrderState{}, err
		}
		if updated {
			return OrderState{}, ErrUpdateAcknowledgementRequired
		}
		var next FulfillmentStatus
		switch {
		case status == Accepted && input.Target == Preparing:
			next = Preparing
		case status == Preparing && input.Target == Ready:
			next = Ready
		case status == Ready && input.Target == Served:
			next = Served
		default:
			return OrderState{}, ErrNotReady
		}
		command, e := tx.Exec(ctx, `UPDATE operation_order_state SET fulfillment_status=$2,version=version+1,updated_at=clock_timestamp() WHERE order_id=$1 AND version=$3`, input.OrderID, string(next), version)
		if e != nil {
			return OrderState{}, e
		}
		if command.RowsAffected() != 1 {
			return OrderState{}, ErrConflict
		}
		newVersion++
		if _, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,before_fulfillment_status,after_fulfillment_status,state_version) VALUES($1,$2,'FULFILLMENT_ADVANCED',$3,$4,$5)`, input.OrderID, input.Actor, string(status), string(next), newVersion); err != nil {
			return OrderState{}, err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT version FROM operation_order_state WHERE order_id=$1`, input.OrderID).Scan(&newVersion); err != nil {
		return OrderState{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operation_action_idempotency(session_id,order_id,action,idempotency_key,request_digest,result_version) VALUES($1,$2,$3,$4,$5,$6)`, input.SessionID, input.OrderID, action, input.IdempotencyKey, digest, newVersion); err != nil {
		return OrderState{}, err
	}
	out, err := readOneOrder(ctx, tx, input.Scope, input.SessionID, input.OrderID)
	if err != nil {
		return OrderState{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OrderState{}, err
	}
	return out, nil
}

func readOneOrder(ctx context.Context, tx pgx.Tx, scope TableScope, sessionID, orderID string) (OrderState, error) {
	rows, err := readOrders(ctx, tx, scope, sessionID, false)
	if err != nil {
		return OrderState{}, err
	}
	for _, o := range rows {
		if o.ID == orderID {
			return o, nil
		}
	}
	return OrderState{}, ErrNotFound
}

// PendingOutbox returns retryable delivery work. KDS cards remain durable and
// independently queryable after an event is acknowledged.
func (s *Service) PendingOutbox(ctx context.Context, scope StoreScope, limit int) ([]OutboxEvent, error) {
	if !validStoreScope(scope) || limit < 1 || limit > 500 {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT b.outbox_id,b.order_id,b.event_type,b.created_at,b.attempts FROM operation_outbox b JOIN operation_kds_cards k USING(order_id) JOIN operation_order_state st USING(order_id) WHERE k.tenant_id=$1 AND k.store_id=$2 AND b.delivered_at IS NULL AND NOT st.operational_hold AND NOT st.payment_review_required AND st.fulfillment_status NOT IN ('SERVED','CANCELLED') ORDER BY b.outbox_id LIMIT $3`, scope.TenantID, scope.StoreID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutboxEvent{}
	for rows.Next() {
		var e OutboxEvent
		if err = rows.Scan(&e.ID, &e.OrderID, &e.EventType, &e.CreatedAt, &e.Attempts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Service) RecordOutboxAttempt(ctx context.Context, id int64, delivered bool) error {
	if id < 1 {
		return ErrInvalid
	}
	var c pgconn.CommandTag
	var err error
	if delivered {
		c, err = s.db.Exec(ctx, `UPDATE operation_outbox SET attempts=attempts+1,delivered_at=clock_timestamp() WHERE outbox_id=$1 AND delivered_at IS NULL`, id)
	} else {
		c, err = s.db.Exec(ctx, `UPDATE operation_outbox SET attempts=attempts+1 WHERE outbox_id=$1 AND delivered_at IS NULL`, id)
	}
	if err != nil {
		return err
	}
	if c.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

var _ ordering.OrderCommitPolicy = (*Service)(nil)
var _ tablesession.ClosePolicy = (*Service)(nil)
