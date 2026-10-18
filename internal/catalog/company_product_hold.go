package catalog

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const maxHoldRequestKeyRunes = 128
const maxHoldReasonRunes = 1000

// GetCompanyProductEmergencyHold reads the current hold and its bounded admin
// summary. A retired product remains readable while it has a hold row so an
// administrator can explicitly clear that historical hold.
func (s *Service) GetCompanyProductEmergencyHold(ctx context.Context, tenantID, productKey string) (CompanyProductEmergencyHold, error) {
	if s == nil || !cleanKey(tenantID) || !catalogKey(productKey) {
		return CompanyProductEmergencyHold{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	defer tx.Rollback(ctx)
	catalogID, revision, err := lockCompanyCatalogForHold(ctx, tx, tenantID, "SHARE")
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	if revision == nil {
		return CompanyProductEmergencyHold{}, ErrNotPublished
	}
	result, found, err := readCompanyHoldSummary(ctx, tx, tenantID, catalogID, *revision, productKey)
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	if !found {
		return CompanyProductEmergencyHold{}, ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	return result, nil
}

// SetCompanyProductEmergencyHold performs one company-level transition. The
// company catalog parent row is the serialization lock even when no product
// hold row exists yet. It is acquired before request replay lookup, matching
// order admission and detach lock ordering.
func (s *Service) SetCompanyProductEmergencyHold(ctx context.Context, tenantID, productKey, actor string, active bool, reason string, expectedVersion int64, idempotencyKey string) (CompanyProductEmergencyHold, error) {
	if s == nil || !cleanKey(tenantID) || !catalogKey(productKey) || !cleanKey(actor) || !validHoldReason(reason) || expectedVersion < 0 || !validHoldRequestKey(idempotencyKey) {
		return CompanyProductEmergencyHold{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	defer tx.Rollback(ctx)
	catalogID, revision, err := lockCompanyCatalogForHold(ctx, tx, tenantID, "UPDATE")
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	fingerprint, err := holdRequestFingerprint(tenantID, catalogID, productKey, actor, active, reason, expectedVersion)
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	if prior, found, replayErr := replayCompanyHoldRequest(ctx, tx, tenantID, idempotencyKey, fingerprint); replayErr != nil {
		return CompanyProductEmergencyHold{}, replayErr
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return CompanyProductEmergencyHold{}, err
		}
		return prior, nil
	}
	if revision == nil {
		return CompanyProductEmergencyHold{}, ErrNotPublished
	}
	var beforeActive bool
	var beforeVersion int64
	var beforeReason string
	err = tx.QueryRow(ctx, `SELECT active,version,reason FROM company_product_emergency_holds
		WHERE tenant_id=$1 AND company_catalog_id=$2 AND product_key=$3`, tenantID, catalogID, productKey).
		Scan(&beforeActive, &beforeVersion, &beforeReason)
	if errors.Is(err, pgx.ErrNoRows) {
		beforeActive, beforeVersion, beforeReason = false, 0, ""
	} else if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	if expectedVersion != beforeVersion {
		return CompanyProductEmergencyHold{}, ErrStale
	}
	if active == beforeActive {
		return CompanyProductEmergencyHold{}, ErrConflict
	}
	if active {
		var publishedActive bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM menu_revision_products
			WHERE catalog_id=$1 AND revision=$2 AND product_key=$3 AND active)`, catalogID, *revision, productKey).Scan(&publishedActive); err != nil {
			return CompanyProductEmergencyHold{}, err
		}
		if !publishedActive {
			return CompanyProductEmergencyHold{}, ErrNotFound
		}
	}
	if beforeVersion == math.MaxInt64 {
		return CompanyProductEmergencyHold{}, ErrConflict
	}
	nextVersion := beforeVersion + 1
	if active {
		if beforeVersion == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO company_product_emergency_holds(
				tenant_id,company_catalog_id,product_key,active,version,reason,activated_by,activated_at)
				VALUES($1,$2,$3,true,$4,$5,$6,clock_timestamp())`, tenantID, catalogID, productKey, nextVersion, reason, actor)
		} else {
			_, err = tx.Exec(ctx, `UPDATE company_product_emergency_holds SET active=true,version=$4,reason=$5,
				activated_by=$6,activated_at=clock_timestamp(),cleared_by=NULL,cleared_at=NULL,updated_at=clock_timestamp()
				WHERE tenant_id=$1 AND company_catalog_id=$2 AND product_key=$3`, tenantID, catalogID, productKey, nextVersion, reason, actor)
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE company_product_emergency_holds SET active=false,version=$4,reason=$5,
				cleared_by=$6,cleared_at=clock_timestamp(),updated_at=clock_timestamp()
			WHERE tenant_id=$1 AND company_catalog_id=$2 AND product_key=$3`, tenantID, catalogID, productKey, nextVersion, reason, actor)
	}
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	if active {
		if err = enqueueHeldCheckoutsTx(ctx, tx, tenantID, productKey, actor); err != nil {
			return CompanyProductEmergencyHold{}, err
		}
	}
	result, found, err := readCompanyHoldSummary(ctx, tx, tenantID, catalogID, *revision, productKey)
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	if !found {
		return CompanyProductEmergencyHold{}, ErrConflict
	}
	beforeJSON, err := json.Marshal(struct {
		Active  bool   `json:"active"`
		Version int64  `json:"version"`
		Reason  string `json:"reason,omitempty"`
	}{beforeActive, beforeVersion, beforeReason})
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	afterJSON, err := json.Marshal(result)
	if err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	operation := "HQ_HOLD_ON"
	if !active {
		operation = "HQ_HOLD_OFF"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO menu_admin_audit(
		tenant_id,store_id,actor,operation,entity_key,before_value,after_value,before_version,after_version,
		request_key,request_fingerprint,after_json)
		VALUES($1,NULL,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)`, tenantID, actor, operation,
		"HQ_HOLD/"+productKey, string(beforeJSON), string(afterJSON), beforeVersion, nextVersion,
		idempotencyKey, fingerprint[:], string(afterJSON)); err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CompanyProductEmergencyHold{}, err
	}
	return result, nil
}

func lockCompanyCatalogForHold(ctx context.Context, tx pgx.Tx, tenantID, strength string) (int64, *int64, error) {
	if strength != "SHARE" && strength != "UPDATE" {
		return 0, nil, ErrInvalid
	}
	query := `SELECT catalog_id,current_revision FROM menu_catalogs WHERE tenant_id=$1 AND owner_kind='COMPANY' FOR SHARE`
	if strength == "UPDATE" {
		query = `SELECT catalog_id,current_revision FROM menu_catalogs WHERE tenant_id=$1 AND owner_kind='COMPANY' FOR UPDATE`
	}
	var catalogID int64
	var revision *int64
	err := tx.QueryRow(ctx, query, tenantID).Scan(&catalogID, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, err
	}
	var tenantEnabled bool
	if err = tx.QueryRow(ctx, `SELECT enabled FROM tenants WHERE tenant_id=$1 FOR SHARE`, tenantID).Scan(&tenantEnabled); errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrNotFound
	} else if err != nil {
		return 0, nil, err
	}
	if !tenantEnabled {
		return 0, nil, ErrDenied
	}
	return catalogID, revision, nil
}

func readCompanyHoldSummary(ctx context.Context, tx pgx.Tx, tenantID string, catalogID, revision int64, productKey string) (CompanyProductEmergencyHold, bool, error) {
	result := CompanyProductEmergencyHold{ProductKey: productKey, PublishedRevision: revision}
	var productExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM menu_revision_products WHERE catalog_id=$1 AND revision=$2 AND product_key=$3)`, catalogID, revision, productKey).Scan(&productExists); err != nil {
		return CompanyProductEmergencyHold{}, false, err
	}
	var activatedAt, clearedAt *time.Time
	var clearedBy *string
	err := tx.QueryRow(ctx, `SELECT active,version,reason,activated_by,activated_at,cleared_by,cleared_at
		FROM company_product_emergency_holds WHERE tenant_id=$1 AND company_catalog_id=$2 AND product_key=$3`, tenantID, catalogID, productKey).
		Scan(&result.Active, &result.Version, &result.Reason, &result.ActivatedBy, &activatedAt, &clearedBy, &clearedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if !productExists {
			return CompanyProductEmergencyHold{}, false, nil
		}
	} else if err != nil {
		return CompanyProductEmergencyHold{}, false, err
	} else {
		result.ActivatedAt, result.ClearedAt = activatedAt, clearedAt
		if clearedBy != nil {
			result.ClearedBy = *clearedBy
		}
	}
	var linkedCount int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM stores s WHERE s.tenant_id=$1
		AND NOT EXISTS(SELECT 1 FROM store_menu_bindings b WHERE b.tenant_id=s.tenant_id AND b.store_id=s.store_id)`, tenantID).Scan(&linkedCount); err != nil {
		return CompanyProductEmergencyHold{}, false, err
	}
	result.LinkedStoreCount = int(linkedCount)
	return result, true, nil
}

func replayCompanyHoldRequest(ctx context.Context, tx pgx.Tx, tenantID, requestKey string, fingerprint [32]byte) (CompanyProductEmergencyHold, bool, error) {
	var savedFingerprint []byte
	var savedJSON []byte
	err := tx.QueryRow(ctx, `SELECT request_fingerprint,after_json FROM menu_admin_audit
		WHERE tenant_id=$1 AND request_key=$2`, tenantID, requestKey).Scan(&savedFingerprint, &savedJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return CompanyProductEmergencyHold{}, false, nil
	}
	if err != nil {
		return CompanyProductEmergencyHold{}, false, err
	}
	if !bytes.Equal(savedFingerprint, fingerprint[:]) {
		return CompanyProductEmergencyHold{}, false, ErrConflict
	}
	var prior CompanyProductEmergencyHold
	if err = json.Unmarshal(savedJSON, &prior); err != nil {
		return CompanyProductEmergencyHold{}, false, err
	}
	return prior, true, nil
}

func holdRequestFingerprint(tenantID string, catalogID int64, productKey, actor string, active bool, reason string, expectedVersion int64) ([32]byte, error) {
	payload, err := json.Marshal(struct {
		TenantID        string `json:"tenant_id"`
		CatalogID       int64  `json:"catalog_id"`
		ProductKey      string `json:"product_key"`
		Actor           string `json:"actor"`
		Active          bool   `json:"active"`
		Reason          string `json:"reason"`
		ExpectedVersion int64  `json:"expected_version"`
	}{tenantID, catalogID, productKey, actor, active, reason, expectedVersion})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func validHoldRequestKey(key string) bool {
	if !cleanKey(key) || len([]rune(key)) > maxHoldRequestKeyRunes {
		return false
	}
	return true
}

func validHoldReason(reason string) bool {
	if !utf8.ValidString(reason) || strings.TrimSpace(reason) == "" || strings.TrimSpace(reason) != reason || len([]rune(reason)) > maxHoldReasonRunes {
		return false
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// enqueueHeldCheckoutsTx runs under the company parent UPDATE lock. Admission,
// detachment and every payment writer acquire that parent first, so this scan
// cannot miss an admitted attempt or race a payment transition. Session locks
// precede attempt locks, and are acquired in a stable order across all stores.
func enqueueHeldCheckoutsTx(ctx context.Context, tx pgx.Tx, tenantID, productKey, actor string) error {
	rows, err := tx.Query(ctx, `SELECT a.order_id,a.session_id FROM stripe_payment_attempts a
		JOIN table_orders o USING(order_id)
		WHERE a.tenant_id=$1 AND o.payment_method='STRIPE'
		AND (a.state IN ('CREATE_PENDING','OPEN','UNKNOWN','PROCESSING')
		     OR (a.state='FAILED' AND a.checkout_session_id IS NOT NULL))
		AND a.provider_payment_status IS DISTINCT FROM 'paid'
		AND (a.checkout_status IS NULL OR a.checkout_status IN ('open','complete'))
		AND NOT EXISTS(SELECT 1 FROM store_menu_bindings b WHERE b.tenant_id=a.tenant_id AND b.store_id=a.store_id)
		AND EXISTS(SELECT 1 FROM jsonb_array_elements(o.quote_snapshot->'quote'->'lines') l WHERE l->>'product_key'=$2)
		ORDER BY a.session_id,a.order_id`, tenantID, productKey)
	if err != nil {
		return err
	}
	type target struct{ order, session string }
	var targets []target
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.order, &t.session); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, t := range targets {
		var id string
		if err = tx.QueryRow(ctx, `SELECT session_id FROM table_sessions WHERE session_id=$1 FOR UPDATE`, t.session).Scan(&id); err != nil {
			return err
		}
		var state string
		if err = tx.QueryRow(ctx, `UPDATE stripe_payment_attempts SET
			company_hold_expire_requested_at=coalesce(company_hold_expire_requested_at,clock_timestamp()),
			continuation_revoked_at=coalesce(continuation_revoked_at,clock_timestamp()),updated_at=clock_timestamp()
			WHERE order_id=$1 RETURNING state`, t.order).Scan(&state); err != nil {
			return err
		}
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return err
		}
		key := "company-hold-expire:" + t.order
		if _, err = tx.Exec(ctx, `INSERT INTO stripe_payment_work(work_id,dedupe_key,work_type,order_id)
			VALUES($1,$2,'EXPIRE',$3) ON CONFLICT(dedupe_key) DO NOTHING`, hex.EncodeToString(nonce[:]), key, t.order); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO stripe_payment_attempt_events(order_id,event_key,actor_id,before_state,after_state)
			VALUES($1,$2,$3,$4,$4) ON CONFLICT(order_id,event_key) DO NOTHING`, t.order, key, actor, state); err != nil {
			return err
		}
	}
	return nil
}
