package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxCatalogProducts         = 500
	maxCatalogOptionGroups     = 1000
	maxCatalogOptions          = 5000
	maxCatalogDescriptionBytes = 256 << 10
)

type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) (*Service, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &Service{db: db}, nil
}

func (s *Service) ResolveStoreScope(ctx context.Context, tenantID, storeID string) (StoreScope, error) {
	var scope StoreScope
	err := s.db.QueryRow(ctx, `SELECT s.tenant_id,s.store_id FROM stores s JOIN tenants n USING(tenant_id)
		WHERE s.tenant_id=$1 AND s.store_id=$2 AND s.enabled AND s.operating AND n.enabled`, tenantID, storeID).Scan(&scope.TenantID, &scope.StoreID)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoreScope{}, ErrNotFound
	}
	return scope, err
}

func (s *Service) TenantEnabled(ctx context.Context, tenantID string) error {
	var enabled bool
	err := s.db.QueryRow(ctx, `SELECT enabled FROM tenants WHERE tenant_id=$1`, tenantID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !enabled {
		return ErrDenied
	}
	return nil
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (s *Service) GetCompanyDraft(ctx context.Context, tenantID string) (Draft, error) {
	var id int64
	err := s.db.QueryRow(ctx, `SELECT catalog_id FROM menu_catalogs WHERE tenant_id=$1 AND owner_kind='COMPANY'`, tenantID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, nil
	}
	if err != nil {
		return Draft{}, err
	}
	return readDraftConsistent(ctx, s.db, id)
}

func (s *Service) SaveCompanyDraft(ctx context.Context, tenantID, actor string, expectedVersion int64, data CatalogData) (Draft, error) {
	if !validActorScope(tenantID, actor) || expectedVersion < 0 {
		return Draft{}, ErrInvalid
	}
	if err := validateData(data); err != nil {
		return Draft{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback(ctx)
	id, err := ensureCatalog(ctx, tx, tenantID, "COMPANY", "")
	if err != nil {
		return Draft{}, err
	}
	if err = saveDraft(ctx, tx, id, actor, expectedVersion, data); err != nil {
		return Draft{}, err
	}
	draft, err := readDraft(ctx, tx, id)
	if err != nil {
		return Draft{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *Service) PublishCompanyDraft(ctx context.Context, tenantID, actor string, expectedVersion int64) (PublishResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	defer tx.Rollback(ctx)
	id, err := lookupCatalog(ctx, tx, tenantID, "COMPANY", "")
	if err != nil {
		return PublishResult{}, err
	}
	result, err := publishCatalog(ctx, tx, id, actor, expectedVersion)
	if err != nil {
		return PublishResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

func (s *Service) ReadPublishedByTableLocator(ctx context.Context, locator string) (PublishedMenu, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PublishedMenu{}, err
	}
	defer tx.Rollback(ctx)
	var scope StoreScope
	err = tx.QueryRow(ctx, `SELECT t.tenant_id,t.store_id FROM dining_tables t
		JOIN stores st USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE t.public_locator=$1 AND t.enabled AND st.enabled AND st.operating AND n.enabled`, locator).Scan(&scope.TenantID, &scope.StoreID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublishedMenu{}, ErrNotFound
	}
	if err != nil {
		return PublishedMenu{}, err
	}
	menu, err := readStoreMenu(ctx, tx, scope)
	if err != nil {
		return PublishedMenu{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PublishedMenu{}, err
	}
	return menu, nil
}

func (s *Service) ReadStoreMenu(ctx context.Context, scope StoreScope) (PublishedMenu, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PublishedMenu{}, err
	}
	defer tx.Rollback(ctx)
	var actual StoreScope
	err = tx.QueryRow(ctx, `SELECT s.tenant_id,s.store_id FROM stores s JOIN tenants n USING(tenant_id)
		WHERE s.tenant_id=$1 AND s.store_id=$2 AND s.enabled AND s.operating AND n.enabled`, scope.TenantID, scope.StoreID).Scan(&actual.TenantID, &actual.StoreID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublishedMenu{}, ErrNotFound
	}
	if err != nil {
		return PublishedMenu{}, err
	}
	if actual != scope {
		return PublishedMenu{}, ErrDenied
	}
	menu, err := readStoreMenu(ctx, tx, actual)
	if err != nil {
		return PublishedMenu{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PublishedMenu{}, err
	}
	return menu, nil
}

func (s *Service) GetStoreDraft(ctx context.Context, scope StoreScope) (Draft, error) {
	id, err := detachedCatalogID(ctx, s.db, scope)
	if err != nil {
		return Draft{}, err
	}
	return readDraftConsistent(ctx, s.db, id)
}

func (s *Service) SaveStoreDraft(ctx context.Context, scope StoreScope, actor string, expectedVersion int64, data CatalogData) (Draft, error) {
	if !validStoreActor(scope, actor) || expectedVersion < 0 {
		return Draft{}, ErrInvalid
	}
	if err := validateData(data); err != nil {
		return Draft{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback(ctx)
	id, err := lockDetachedCatalog(ctx, tx, scope)
	if err != nil {
		return Draft{}, err
	}
	if err = saveDraft(ctx, tx, id, actor, expectedVersion, data); err != nil {
		return Draft{}, err
	}
	draft, err := readDraft(ctx, tx, id)
	if err != nil {
		return Draft{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *Service) PublishStoreDraft(ctx context.Context, scope StoreScope, actor string, expectedVersion int64) (PublishResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	defer tx.Rollback(ctx)
	id, err := lockDetachedCatalog(ctx, tx, scope)
	if err != nil {
		return PublishResult{}, err
	}
	result, err := publishCatalog(ctx, tx, id, actor, expectedVersion)
	if err != nil {
		return PublishResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

func (s *Service) DetachStore(ctx context.Context, scope StoreScope, actor string, expectedRevision int64) (PublishResult, error) {
	if !validStoreActor(scope, actor) || expectedRevision < 1 {
		return PublishResult{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	defer tx.Rollback(ctx)
	companyID, err := lookupCatalog(ctx, tx, scope.TenantID, "COMPANY", "")
	if err != nil {
		return PublishResult{}, err
	}
	var companyRevision *int64
	err = tx.QueryRow(ctx, `SELECT current_revision FROM menu_catalogs WHERE catalog_id=$1 FOR SHARE`, companyID).Scan(&companyRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublishResult{}, ErrNotPublished
	}
	if err != nil {
		return PublishResult{}, err
	}
	if companyRevision == nil {
		return PublishResult{}, ErrNotPublished
	}
	if *companyRevision != expectedRevision {
		return PublishResult{}, ErrStale
	}
	if err = lockStore(ctx, tx, scope); err != nil {
		return PublishResult{}, err
	}
	var bound bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM store_menu_bindings WHERE tenant_id=$1 AND store_id=$2)`, scope.TenantID, scope.StoreID).Scan(&bound); err != nil {
		return PublishResult{}, err
	}
	if bound {
		return PublishResult{}, ErrAlreadyDetached
	}
	data, err := readRevision(ctx, tx, companyID, *companyRevision)
	if err != nil {
		return PublishResult{}, err
	}
	rows, err := tx.Query(ctx, `SELECT product_key,price_minor FROM store_product_price_overrides WHERE tenant_id=$1 AND store_id=$2 AND active`, scope.TenantID, scope.StoreID)
	if err != nil {
		return PublishResult{}, err
	}
	prices := map[string]int64{}
	for rows.Next() {
		var key string
		var amount int64
		if err = rows.Scan(&key, &amount); err != nil {
			rows.Close()
			return PublishResult{}, err
		}
		prices[key] = amount
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return PublishResult{}, err
	}
	rows.Close()
	for ci := range data.Categories {
		for pi := range data.Categories[ci].Products {
			product := &data.Categories[ci].Products[pi]
			if price, ok := prices[product.Key]; ok {
				product.BasePriceMinor = price
			}
		}
	}
	var detachedID int64
	err = tx.QueryRow(ctx, `INSERT INTO menu_catalogs(tenant_id,owner_kind,store_id,draft_version) VALUES($1,'STORE',$2,0) RETURNING catalog_id`, scope.TenantID, scope.StoreID).Scan(&detachedID)
	if err != nil {
		return PublishResult{}, err
	}
	if err = replaceDraftRows(ctx, tx, detachedID, data); err != nil {
		return PublishResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE menu_catalogs SET draft_version=1 WHERE catalog_id=$1`, detachedID); err != nil {
		return PublishResult{}, err
	}
	result, err := writeRevision(ctx, tx, detachedID, actor, 1, data)
	if err != nil {
		return PublishResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE menu_catalogs SET current_revision=$2,draft_base_revision=$2 WHERE catalog_id=$1`, detachedID, result.Revision); err != nil {
		return PublishResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO store_menu_bindings(tenant_id,store_id,mode,catalog_id,detached_by) VALUES($1,$2,'DETACHED',$3,$4)`, scope.TenantID, scope.StoreID, detachedID, actor); err != nil {
		return PublishResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE store_product_price_overrides SET active=false,version=version+1,updated_by=$3,updated_at=clock_timestamp() WHERE tenant_id=$1 AND store_id=$2 AND active`, scope.TenantID, scope.StoreID, actor); err != nil {
		return PublishResult{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO menu_admin_audit(tenant_id,store_id,actor,operation,entity_key,before_value,after_value,before_version,after_version)
		VALUES($1,$2,$3,'STORE_MENU_DETACHED',$4,$5,$6,$7,$8)`, scope.TenantID, scope.StoreID, actor, "catalog", "LINKED", "DETACHED", *companyRevision, result.Revision)
	if err != nil {
		return PublishResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

func (s *Service) SetStorePriceOverride(ctx context.Context, scope StoreScope, actor, productKey string, amount int64, expectedVersion int64) (PriceOverride, error) {
	if !validStoreActor(scope, actor) || !catalogKey(productKey) || amount < 0 || expectedVersion < 0 {
		return PriceOverride{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PriceOverride{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockStore(ctx, tx, scope); err != nil {
		return PriceOverride{}, err
	}
	catalogID, mode, err := storeCatalog(ctx, tx, scope)
	if err != nil {
		return PriceOverride{}, err
	}
	if mode != "LINKED" {
		return PriceOverride{}, ErrNotDetached
	}
	var revision *int64
	if err = tx.QueryRow(ctx, `SELECT current_revision FROM menu_catalogs WHERE catalog_id=$1`, catalogID).Scan(&revision); err != nil {
		return PriceOverride{}, err
	}
	if revision == nil {
		return PriceOverride{}, ErrNotPublished
	}
	var base int64
	err = tx.QueryRow(ctx, `SELECT base_price_minor FROM menu_revision_products WHERE catalog_id=$1 AND revision=$2 AND product_key=$3`, catalogID, *revision, productKey).Scan(&base)
	if errors.Is(err, pgx.ErrNoRows) {
		return PriceOverride{}, ErrNotFound
	}
	if err != nil {
		return PriceOverride{}, err
	}
	previousPrice := base
	previousSource := "COMPANY_PRICE"
	var storedPrice int64
	var storedActive bool
	var oldVersion int64
	err = tx.QueryRow(ctx, `SELECT price_minor,active,version FROM store_product_price_overrides WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3 FOR UPDATE`, scope.TenantID, scope.StoreID, productKey).Scan(&storedPrice, &storedActive, &oldVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		oldVersion = 0
	} else if err != nil {
		return PriceOverride{}, err
	} else if storedActive {
		previousPrice = storedPrice
		previousSource = "STORE_OVERRIDE"
	}
	if oldVersion != expectedVersion {
		return PriceOverride{}, ErrStale
	}
	newVersion := oldVersion + 1
	if oldVersion == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO store_product_price_overrides(tenant_id,store_id,product_key,price_minor,active,version,updated_by)
			VALUES($1,$2,$3,$4,true,$5,$6)`, scope.TenantID, scope.StoreID, productKey, amount, newVersion, actor)
	} else {
		command, updateErr := tx.Exec(ctx, `UPDATE store_product_price_overrides SET price_minor=$4,active=true,version=$5,updated_by=$6,updated_at=clock_timestamp()
			WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3 AND version=$7`, scope.TenantID, scope.StoreID, productKey, amount, newVersion, actor, expectedVersion)
		err = updateErr
		if err == nil && command.RowsAffected() != 1 {
			err = ErrStale
		}
	}
	if err != nil {
		return PriceOverride{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO menu_admin_audit(tenant_id,store_id,actor,operation,entity_key,before_value,after_value,before_amount,after_amount,before_version,after_version)
		VALUES($1,$2,$3,'STORE_PRICE_OVERRIDE',$4,$5,'STORE_OVERRIDE',$6,$7,$8,$9)`, scope.TenantID, scope.StoreID, actor, productKey, previousSource, previousPrice, amount, oldVersion, newVersion); err != nil {
		return PriceOverride{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PriceOverride{}, err
	}
	return PriceOverride{ProductKey: productKey, PriceMinor: amount, Active: true, Version: newVersion}, nil
}

func (s *Service) ResetStorePriceOverride(ctx context.Context, scope StoreScope, actor, productKey string, expectedVersion int64) (PriceOverride, error) {
	if !validStoreActor(scope, actor) || !catalogKey(productKey) || expectedVersion < 1 {
		return PriceOverride{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PriceOverride{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockStore(ctx, tx, scope); err != nil {
		return PriceOverride{}, err
	}
	catalogID, mode, err := storeCatalog(ctx, tx, scope)
	if err != nil {
		return PriceOverride{}, err
	}
	if mode != "LINKED" {
		return PriceOverride{}, ErrConflict
	}
	var revision *int64
	if err = tx.QueryRow(ctx, `SELECT current_revision FROM menu_catalogs WHERE catalog_id=$1`, catalogID).Scan(&revision); err != nil {
		return PriceOverride{}, err
	}
	if revision == nil {
		return PriceOverride{}, ErrNotPublished
	}
	var companyPrice int64
	if err = tx.QueryRow(ctx, `SELECT base_price_minor FROM menu_revision_products WHERE catalog_id=$1 AND revision=$2 AND product_key=$3`, catalogID, *revision, productKey).Scan(&companyPrice); errors.Is(err, pgx.ErrNoRows) {
		return PriceOverride{}, ErrNotFound
	} else if err != nil {
		return PriceOverride{}, err
	}
	var amount int64
	var version int64
	err = tx.QueryRow(ctx, `SELECT price_minor,version FROM store_product_price_overrides WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3 AND active FOR UPDATE`, scope.TenantID, scope.StoreID, productKey).Scan(&amount, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return PriceOverride{}, ErrNotFound
	}
	if err != nil {
		return PriceOverride{}, err
	}
	if version != expectedVersion {
		return PriceOverride{}, ErrStale
	}
	newVersion := version + 1
	if _, err = tx.Exec(ctx, `UPDATE store_product_price_overrides SET active=false,version=$4,updated_by=$5,updated_at=clock_timestamp() WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3`, scope.TenantID, scope.StoreID, productKey, newVersion, actor); err != nil {
		return PriceOverride{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO menu_admin_audit(tenant_id,store_id,actor,operation,entity_key,before_value,after_value,before_amount,after_amount,before_version,after_version)
		VALUES($1,$2,$3,'STORE_PRICE_OVERRIDE_RESET',$4,$5,$6,$7,$8,$9,$10)`, scope.TenantID, scope.StoreID, actor, productKey, formatPrice(&amount), formatPrice(&companyPrice), amount, companyPrice, version, newVersion); err != nil {
		return PriceOverride{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PriceOverride{}, err
	}
	return PriceOverride{ProductKey: productKey, PriceMinor: companyPrice, Active: false, Version: newVersion}, nil
}

func (s *Service) SetStoreAvailability(ctx context.Context, scope StoreScope, actor string, change AvailabilityChange) (AvailabilityResult, error) {
	if !validStoreActor(scope, actor) || change.ExpectedVersion < 0 || !validAvailability(change.Status) {
		return AvailabilityResult{}, ErrInvalid
	}
	if change.Status == Paused && strings.TrimSpace(change.Reason) == "" {
		return AvailabilityResult{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AvailabilityResult{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockStore(ctx, tx, scope); err != nil {
		return AvailabilityResult{}, err
	}
	catalogID, mode, err := storeCatalog(ctx, tx, scope)
	if err != nil {
		return AvailabilityResult{}, err
	}
	var revision *int64
	if err = tx.QueryRow(ctx, `SELECT current_revision FROM menu_catalogs WHERE catalog_id=$1`, catalogID).Scan(&revision); err != nil {
		return AvailabilityResult{}, err
	}
	if revision == nil {
		return AvailabilityResult{}, ErrNotPublished
	}
	if err = validateAvailabilityTarget(ctx, tx, scope, catalogID, *revision, change.Target); err != nil {
		return AvailabilityResult{}, err
	}
	table, key, beforeStatus, oldVersion := availabilityTableAndKey(change.Target)
	var existingStatus string
	err = tx.QueryRow(ctx, "SELECT status,version FROM "+table+" WHERE tenant_id=$1 AND store_id=$2 AND "+key.where+" FOR UPDATE", append([]any{scope.TenantID, scope.StoreID}, key.args...)...).Scan(&existingStatus, &oldVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		oldVersion = 0
		beforeStatus = PendingConfirmation
	} else if err != nil {
		return AvailabilityResult{}, err
	} else {
		beforeStatus = Availability(existingStatus)
	}
	if oldVersion != change.ExpectedVersion || !allowedTransition(beforeStatus, change.Status) {
		return AvailabilityResult{}, ErrStale
	}
	newVersion := oldVersion + 1
	if change.Target.Kind == ProductAvailability {
		_, err = tx.Exec(ctx, `INSERT INTO store_product_availability(tenant_id,store_id,product_key,status,reason,updated_by,version)
			VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id,store_id,product_key) DO UPDATE SET status=EXCLUDED.status,reason=EXCLUDED.reason,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp(),version=EXCLUDED.version`, scope.TenantID, scope.StoreID, change.Target.ProductKey, change.Status, change.Reason, actor, newVersion)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO store_option_availability(tenant_id,store_id,product_key,group_key,option_key,status,reason,updated_by,version)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(tenant_id,store_id,product_key,group_key,option_key) DO UPDATE SET status=EXCLUDED.status,reason=EXCLUDED.reason,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp(),version=EXCLUDED.version`, scope.TenantID, scope.StoreID, change.Target.ProductKey, change.Target.GroupKey, change.Target.OptionKey, change.Status, change.Reason, actor, newVersion)
	}
	if err != nil {
		return AvailabilityResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO menu_admin_audit(tenant_id,store_id,actor,operation,entity_key,before_value,after_value,before_version,after_version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, scope.TenantID, scope.StoreID, actor, "AVAILABILITY_"+string(change.Target.Kind), availabilityKey(change.Target), string(beforeStatus), string(change.Status), oldVersion, newVersion); err != nil {
		return AvailabilityResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AvailabilityResult{}, err
	}
	_ = mode
	return AvailabilityResult{Status: change.Status, Version: newVersion}, nil
}

func Quote(menu PublishedMenu, productKey string, selected []OptionSelection) (PriceQuote, error) {
	if !catalogKey(productKey) {
		return PriceQuote{}, ErrInvalid
	}
	var product *Product
	for ci := range menu.Categories {
		for pi := range menu.Categories[ci].Products {
			if menu.Categories[ci].Products[pi].Key == productKey {
				product = &menu.Categories[ci].Products[pi]
				break
			}
		}
	}
	if product == nil {
		return PriceQuote{}, ErrNotFound
	}
	if !product.Active || product.Availability != Available || !product.Selectable {
		return PriceQuote{}, ErrDenied
	}
	if product.EffectivePriceMinor < 0 {
		return PriceQuote{}, ErrInvalid
	}
	amount := product.EffectivePriceMinor
	selectedByGroup := make(map[string]map[string]bool)
	for _, pick := range selected {
		if !catalogKey(pick.GroupKey) || !catalogKey(pick.OptionKey) {
			return PriceQuote{}, ErrInvalid
		}
		if selectedByGroup[pick.GroupKey] == nil {
			selectedByGroup[pick.GroupKey] = make(map[string]bool)
		}
		if selectedByGroup[pick.GroupKey][pick.OptionKey] {
			return PriceQuote{}, ErrInvalid
		}
		selectedByGroup[pick.GroupKey][pick.OptionKey] = true
	}
	knownGroups := map[string]bool{}
	for _, group := range product.Groups {
		knownGroups[group.Key] = true
		picks := selectedByGroup[group.Key]
		if len(picks) < group.MinSelect || len(picks) > group.MaxSelect {
			return PriceQuote{}, ErrInvalid
		}
		for optionKey := range picks {
			var option *Option
			for oi := range group.Options {
				if group.Options[oi].Key == optionKey {
					option = &group.Options[oi]
					break
				}
			}
			if option == nil || !option.Active || option.Availability != Available {
				return PriceQuote{}, ErrDenied
			}
			if amount > math.MaxInt64-option.PriceDeltaMinor {
				return PriceQuote{}, ErrPriceOverflow
			}
			amount += option.PriceDeltaMinor
		}
	}
	for groupKey := range selectedByGroup {
		if !knownGroups[groupKey] {
			return PriceQuote{}, ErrInvalid
		}
	}
	return PriceQuote{ProductKey: product.Key, UnitMinor: amount}, nil
}

func ensureCatalog(ctx context.Context, tx pgx.Tx, tenantID, kind, storeID string) (int64, error) {
	if kind == "COMPANY" {
		if _, err := tx.Exec(ctx, `INSERT INTO menu_catalogs(tenant_id,owner_kind) SELECT tenant_id,'COMPANY' FROM tenants WHERE tenant_id=$1 AND enabled ON CONFLICT DO NOTHING`, tenantID); err != nil {
			return 0, err
		}
	} else {
		if _, err := tx.Exec(ctx, `INSERT INTO menu_catalogs(tenant_id,owner_kind,store_id) SELECT s.tenant_id,'STORE',s.store_id FROM stores s JOIN tenants n USING(tenant_id) WHERE s.tenant_id=$1 AND s.store_id=$2 AND s.enabled AND s.operating AND n.enabled ON CONFLICT DO NOTHING`, tenantID, storeID); err != nil {
			return 0, err
		}
	}
	return lookupCatalog(ctx, tx, tenantID, kind, storeID)
}

func lookupCatalog(ctx context.Context, tx pgx.Tx, tenantID, kind, storeID string) (int64, error) {
	var id int64
	var err error
	if kind == "COMPANY" {
		err = tx.QueryRow(ctx, `SELECT c.catalog_id FROM menu_catalogs c JOIN tenants n USING(tenant_id) WHERE c.tenant_id=$1 AND c.owner_kind='COMPANY' AND n.enabled`, tenantID).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, `SELECT c.catalog_id FROM menu_catalogs c JOIN stores s USING(tenant_id,store_id) JOIN tenants n USING(tenant_id) WHERE c.tenant_id=$1 AND c.owner_kind='STORE' AND c.store_id=$2 AND s.enabled AND s.operating AND n.enabled`, tenantID, storeID).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func detachedCatalogID(ctx context.Context, q queryer, scope StoreScope) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `SELECT b.catalog_id FROM store_menu_bindings b JOIN stores s USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE b.tenant_id=$1 AND b.store_id=$2 AND s.enabled AND s.operating AND n.enabled`, scope.TenantID, scope.StoreID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotDetached
	}
	return id, err
}

func lockDetachedCatalog(ctx context.Context, tx pgx.Tx, scope StoreScope) (int64, error) {
	if err := lockStore(ctx, tx, scope); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow(ctx, `SELECT b.catalog_id FROM store_menu_bindings b WHERE b.tenant_id=$1 AND b.store_id=$2 FOR UPDATE`, scope.TenantID, scope.StoreID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotDetached
	}
	return id, err
}

func lockStore(ctx context.Context, tx pgx.Tx, scope StoreScope) error {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT s.enabled AND s.operating AND n.enabled FROM stores s JOIN tenants n USING(tenant_id) WHERE s.tenant_id=$1 AND s.store_id=$2 FOR UPDATE OF s`, scope.TenantID, scope.StoreID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !enabled {
		return ErrDenied
	}
	return nil
}

func storeCatalog(ctx context.Context, q queryer, scope StoreScope) (int64, string, error) {
	var id int64
	var mode string
	err := q.QueryRow(ctx, `SELECT b.catalog_id,'DETACHED' FROM store_menu_bindings b JOIN stores s USING(tenant_id,store_id) JOIN tenants n USING(tenant_id)
		WHERE b.tenant_id=$1 AND b.store_id=$2 AND s.enabled AND s.operating AND n.enabled`, scope.TenantID, scope.StoreID).Scan(&id, &mode)
	if err == nil {
		return id, mode, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, "", err
	}
	err = q.QueryRow(ctx, `SELECT c.catalog_id,'LINKED' FROM menu_catalogs c JOIN stores s USING(tenant_id) JOIN tenants n USING(tenant_id)
		WHERE c.tenant_id=$1 AND c.owner_kind='COMPANY' AND s.store_id=$2 AND s.enabled AND s.operating AND n.enabled`, scope.TenantID, scope.StoreID).Scan(&id, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	return id, mode, err
}

func saveDraft(ctx context.Context, tx pgx.Tx, catalogID int64, actor string, expected int64, data CatalogData) error {
	var version int64
	if err := tx.QueryRow(ctx, `SELECT draft_version FROM menu_catalogs WHERE catalog_id=$1 FOR UPDATE`, catalogID).Scan(&version); err != nil {
		return err
	}
	if version != expected {
		return ErrStale
	}
	if err := replaceDraftRows(ctx, tx, catalogID, data); err != nil {
		return err
	}
	newVersion := version + 1
	if _, err := tx.Exec(ctx, `UPDATE menu_catalogs SET draft_version=$2,draft_base_revision=current_revision WHERE catalog_id=$1`, catalogID, newVersion); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO menu_admin_audit(tenant_id,store_id,actor,operation,entity_key,before_version,after_version)
		SELECT tenant_id,store_id,$2,'DRAFT_REPLACED',$3,$4,$5 FROM menu_catalogs WHERE catalog_id=$1`, catalogID, actor, "catalog", version, newVersion)
	return err
}

func replaceDraftRows(ctx context.Context, tx pgx.Tx, catalogID int64, data CatalogData) error {
	if _, err := tx.Exec(ctx, `DELETE FROM menu_draft_categories WHERE catalog_id=$1`, catalogID); err != nil {
		return err
	}
	for _, category := range data.Categories {
		if _, err := tx.Exec(ctx, `INSERT INTO menu_draft_categories(catalog_id,category_key,name,position) VALUES($1,$2,$3,$4)`, catalogID, category.Key, category.Name, category.Position); err != nil {
			return err
		}
		for _, product := range category.Products {
			if _, err := tx.Exec(ctx, `INSERT INTO menu_draft_products(catalog_id,product_key,category_key,name,description,base_price_minor,position,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, catalogID, product.Key, category.Key, product.Name, product.Description, product.BasePriceMinor, product.Position, product.Active); err != nil {
				return err
			}
			for _, group := range product.Groups {
				if _, err := tx.Exec(ctx, `INSERT INTO menu_draft_option_groups(catalog_id,product_key,group_key,name,min_select,max_select,position) VALUES($1,$2,$3,$4,$5,$6,$7)`, catalogID, product.Key, group.Key, group.Name, group.MinSelect, group.MaxSelect, group.Position); err != nil {
					return err
				}
				for _, option := range group.Options {
					if _, err := tx.Exec(ctx, `INSERT INTO menu_draft_options(catalog_id,product_key,group_key,option_key,name,price_delta_minor,position,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, catalogID, product.Key, group.Key, option.Key, option.Name, option.PriceDeltaMinor, option.Position, option.Active); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func publishCatalog(ctx context.Context, tx pgx.Tx, catalogID int64, actor string, expectedVersion int64) (PublishResult, error) {
	if !cleanKey(actor) || expectedVersion < 1 {
		return PublishResult{}, ErrInvalid
	}
	var tenantID string
	var ownerKind string
	var draftVersion int64
	var draftBaseRevision *int64
	var current *int64
	err := tx.QueryRow(ctx, `SELECT tenant_id,owner_kind,draft_version,draft_base_revision,current_revision FROM menu_catalogs WHERE catalog_id=$1 FOR UPDATE`, catalogID).Scan(&tenantID, &ownerKind, &draftVersion, &draftBaseRevision, &current)
	if err != nil {
		return PublishResult{}, err
	}
	if expectedVersion != draftVersion {
		return PublishResult{}, ErrStale
	}
	if !sameRevision(draftBaseRevision, current) {
		return PublishResult{}, ErrStale
	}
	data, err := readDraft(ctx, tx, catalogID)
	if err != nil {
		return PublishResult{}, err
	}
	if err = validateData(data.CatalogData); err != nil {
		return PublishResult{}, err
	}
	var previous CatalogData
	var previousRevision int64
	if current != nil {
		previousRevision = *current
		var lastDraftVersion int64
		if err = tx.QueryRow(ctx, `SELECT draft_version FROM menu_catalog_revisions WHERE catalog_id=$1 AND revision=$2`, catalogID, previousRevision).Scan(&lastDraftVersion); err != nil {
			return PublishResult{}, err
		}
		if expectedVersion <= lastDraftVersion {
			return PublishResult{}, ErrStale
		}
		previous, err = readRevision(ctx, tx, catalogID, previousRevision)
		if err != nil {
			return PublishResult{}, err
		}
	}
	if ownerKind == "COMPANY" {
		// New stable keys start unavailable in LINKED stores. Existing stable-key
		// availability is retained; detached stores are outside this publication.
		if err = lockLinkedStores(ctx, tx, tenantID); err != nil {
			return PublishResult{}, err
		}
	}
	result, err := writeRevision(ctx, tx, catalogID, actor, draftVersion, data.CatalogData)
	if err != nil {
		return PublishResult{}, err
	}
	if ownerKind == "COMPANY" {
		if err = initializeNewStoreStates(ctx, tx, tenantID, actor, previous, data.CatalogData); err != nil {
			return PublishResult{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE menu_catalogs SET current_revision=$2,draft_base_revision=$2 WHERE catalog_id=$1`, catalogID, result.Revision); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

func writeRevision(ctx context.Context, tx pgx.Tx, catalogID int64, actor string, draftVersion int64, data CatalogData) (PublishResult, error) {
	var revision int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(max(revision),0)+1 FROM menu_catalog_revisions WHERE catalog_id=$1`, catalogID).Scan(&revision)
	if err != nil {
		return PublishResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO menu_catalog_revisions(catalog_id,revision,draft_version,published_by) VALUES($1,$2,$3,$4)`, catalogID, revision, draftVersion, actor); err != nil {
		return PublishResult{}, err
	}
	for _, category := range data.Categories {
		if _, err = tx.Exec(ctx, `INSERT INTO menu_revision_categories(catalog_id,revision,category_key,name,position) VALUES($1,$2,$3,$4,$5)`, catalogID, revision, category.Key, category.Name, category.Position); err != nil {
			return PublishResult{}, err
		}
		for _, product := range category.Products {
			if _, err = tx.Exec(ctx, `INSERT INTO menu_revision_products(catalog_id,revision,product_key,category_key,name,description,base_price_minor,position,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, catalogID, revision, product.Key, category.Key, product.Name, product.Description, product.BasePriceMinor, product.Position, product.Active); err != nil {
				return PublishResult{}, err
			}
			for _, group := range product.Groups {
				if _, err = tx.Exec(ctx, `INSERT INTO menu_revision_option_groups(catalog_id,revision,product_key,group_key,name,min_select,max_select,position) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, catalogID, revision, product.Key, group.Key, group.Name, group.MinSelect, group.MaxSelect, group.Position); err != nil {
					return PublishResult{}, err
				}
				for _, option := range group.Options {
					if _, err = tx.Exec(ctx, `INSERT INTO menu_revision_options(catalog_id,revision,product_key,group_key,option_key,name,price_delta_minor,position,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, catalogID, revision, product.Key, group.Key, option.Key, option.Name, option.PriceDeltaMinor, option.Position, option.Active); err != nil {
						return PublishResult{}, err
					}
				}
			}
		}
	}
	command, err := tx.Exec(ctx, `UPDATE menu_catalog_revisions SET sealed=true WHERE catalog_id=$1 AND revision=$2 AND NOT sealed`, catalogID, revision)
	if err != nil {
		return PublishResult{}, err
	}
	if command.RowsAffected() != 1 {
		return PublishResult{}, ErrConflict
	}
	return PublishResult{Revision: revision, DraftVersion: draftVersion}, nil
}

func validateData(data CatalogData) error {
	if len(data.Categories) > 200 {
		return ErrInvalid
	}
	productCount, groupCount, optionCount, descriptionBytes := 0, 0, 0, 0
	categoryKeys, productKeys := map[string]bool{}, map[string]bool{}
	for _, category := range data.Categories {
		if !catalogKey(category.Key) || !validName(category.Name, 160) || category.Position < 0 || categoryKeys[category.Key] || len(category.Products) > 1000 {
			return ErrInvalid
		}
		categoryKeys[category.Key] = true
		for _, product := range category.Products {
			productCount++
			descriptionBytes += len(product.Description)
			if productCount > maxCatalogProducts || descriptionBytes > maxCatalogDescriptionBytes || !catalogKey(product.Key) || !validName(product.Name, 200) || len(product.Description) > 4000 || product.BasePriceMinor < 0 || product.Position < 0 || productKeys[product.Key] || len(product.Groups) > 100 {
				return ErrInvalid
			}
			productKeys[product.Key] = true
			groupKeys := map[string]bool{}
			for _, group := range product.Groups {
				groupCount++
				optionCount += len(group.Options)
				if groupCount > maxCatalogOptionGroups || optionCount > maxCatalogOptions || !catalogKey(group.Key) || !validName(group.Name, 160) || group.Position < 0 || group.MinSelect < 0 || group.MaxSelect < 1 || group.MaxSelect < group.MinSelect || groupKeys[group.Key] || len(group.Options) == 0 || group.MaxSelect > len(group.Options) {
					return ErrInvalid
				}
				groupKeys[group.Key] = true
				activeCount, optionKeys := 0, map[string]bool{}
				for _, option := range group.Options {
					if !catalogKey(option.Key) || !validName(option.Name, 160) || option.Position < 0 || option.PriceDeltaMinor < 0 || optionKeys[option.Key] {
						return ErrInvalid
					}
					optionKeys[option.Key] = true
					if option.Active {
						activeCount++
					}
				}
				if activeCount < group.MinSelect {
					return ErrInvalid
				}
			}
		}
	}
	budgetSize, err := catalogBudgetSize(data)
	if err != nil || budgetSize > MaxCatalogCanonicalJSONBytes {
		return ErrInvalid
	}
	return nil
}

func catalogBudgetSize(data CatalogData) (int, error) {
	// DETACH can replace a short company price with any nonnegative bigint
	// override. Measure a shallow product copy at the maximum int64 width so
	// every accepted catalog remains within budget after that transformation.
	sized := data
	sized.Categories = append([]Category(nil), data.Categories...)
	for ci := range sized.Categories {
		sized.Categories[ci].Products = append([]Product(nil), data.Categories[ci].Products...)
		for pi := range sized.Categories[ci].Products {
			sized.Categories[ci].Products[pi].BasePriceMinor = math.MaxInt64
		}
	}
	canonical, err := json.Marshal(sized)
	if err != nil {
		return 0, err
	}
	return len(canonical), nil
}

func readDraft(ctx context.Context, q queryer, catalogID int64) (Draft, error) {
	var version int64
	if err := q.QueryRow(ctx, `SELECT draft_version FROM menu_catalogs WHERE catalog_id=$1`, catalogID).Scan(&version); err != nil {
		return Draft{}, err
	}
	data, err := readData(ctx, q, catalogID, 0, true)
	if err != nil {
		return Draft{}, err
	}
	return Draft{Version: version, CatalogData: data}, nil
}

func readDraftConsistent(ctx context.Context, db *pgxpool.Pool, catalogID int64) (Draft, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback(ctx)
	draft, err := readDraft(ctx, tx, catalogID)
	if err != nil {
		return Draft{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func sameRevision(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func readRevision(ctx context.Context, q queryer, catalogID, revision int64) (CatalogData, error) {
	return readData(ctx, q, catalogID, revision, false)
}

func readData(ctx context.Context, q queryer, catalogID, revision int64, draft bool) (CatalogData, error) {
	data := CatalogData{Categories: []Category{}}
	var rows pgx.Rows
	var err error
	if draft {
		rows, err = q.Query(ctx, `SELECT category_key,name,position FROM menu_draft_categories WHERE catalog_id=$1 ORDER BY position,category_key`, catalogID)
	} else {
		rows, err = q.Query(ctx, `SELECT category_key,name,position FROM menu_revision_categories WHERE catalog_id=$1 AND revision=$2 ORDER BY position,category_key`, catalogID, revision)
	}
	if err != nil {
		return data, err
	}
	categoryIndex := map[string]int{}
	for rows.Next() {
		var category Category
		if err = rows.Scan(&category.Key, &category.Name, &category.Position); err != nil {
			rows.Close()
			return data, err
		}
		category.Products = []Product{}
		categoryIndex[category.Key] = len(data.Categories)
		data.Categories = append(data.Categories, category)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	type productPosition struct{ category, product int }
	productIndex := map[string]productPosition{}
	if draft {
		rows, err = q.Query(ctx, `SELECT product_key,category_key,name,description,base_price_minor,position,active FROM menu_draft_products WHERE catalog_id=$1 ORDER BY position,product_key`, catalogID)
	} else {
		rows, err = q.Query(ctx, `SELECT product_key,category_key,name,description,base_price_minor,position,active FROM menu_revision_products WHERE catalog_id=$1 AND revision=$2 ORDER BY position,product_key`, catalogID, revision)
	}
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var product Product
		var categoryKey string
		if err = rows.Scan(&product.Key, &categoryKey, &product.Name, &product.Description, &product.BasePriceMinor, &product.Position, &product.Active); err != nil {
			rows.Close()
			return data, err
		}
		ci, ok := categoryIndex[categoryKey]
		if !ok {
			rows.Close()
			return data, ErrInvalid
		}
		product.Groups = []OptionGroup{}
		data.Categories[ci].Products = append(data.Categories[ci].Products, product)
		productIndex[product.Key] = productPosition{category: ci, product: len(data.Categories[ci].Products) - 1}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	type groupPosition struct{ category, product, group int }
	groupIndex := map[string]groupPosition{}
	if draft {
		rows, err = q.Query(ctx, `SELECT product_key,group_key,name,min_select,max_select,position FROM menu_draft_option_groups WHERE catalog_id=$1 ORDER BY position,group_key`, catalogID)
	} else {
		rows, err = q.Query(ctx, `SELECT product_key,group_key,name,min_select,max_select,position FROM menu_revision_option_groups WHERE catalog_id=$1 AND revision=$2 ORDER BY position,group_key`, catalogID, revision)
	}
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var productKey string
		var group OptionGroup
		if err = rows.Scan(&productKey, &group.Key, &group.Name, &group.MinSelect, &group.MaxSelect, &group.Position); err != nil {
			rows.Close()
			return data, err
		}
		position, ok := productIndex[productKey]
		if !ok {
			rows.Close()
			return data, ErrInvalid
		}
		group.Options = []Option{}
		product := &data.Categories[position.category].Products[position.product]
		product.Groups = append(product.Groups, group)
		groupIndex[productKey+"\x00"+group.Key] = groupPosition{category: position.category, product: position.product, group: len(product.Groups) - 1}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	if draft {
		rows, err = q.Query(ctx, `SELECT product_key,group_key,option_key,name,price_delta_minor,position,active FROM menu_draft_options WHERE catalog_id=$1 ORDER BY position,option_key`, catalogID)
	} else {
		rows, err = q.Query(ctx, `SELECT product_key,group_key,option_key,name,price_delta_minor,position,active FROM menu_revision_options WHERE catalog_id=$1 AND revision=$2 ORDER BY position,option_key`, catalogID, revision)
	}
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var productKey, groupKey string
		var option Option
		if err = rows.Scan(&productKey, &groupKey, &option.Key, &option.Name, &option.PriceDeltaMinor, &option.Position, &option.Active); err != nil {
			rows.Close()
			return data, err
		}
		position, ok := groupIndex[productKey+"\x00"+groupKey]
		if !ok {
			rows.Close()
			return data, ErrInvalid
		}
		group := &data.Categories[position.category].Products[position.product].Groups[position.group]
		group.Options = append(group.Options, option)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	return data, nil
}

func readStoreMenu(ctx context.Context, tx pgx.Tx, scope StoreScope) (PublishedMenu, error) {
	catalogID, mode, err := storeCatalog(ctx, tx, scope)
	if errors.Is(err, ErrNotFound) {
		return PublishedMenu{}, ErrNotPublished
	}
	if err != nil {
		return PublishedMenu{}, err
	}
	var revision *int64
	err = tx.QueryRow(ctx, `SELECT current_revision FROM menu_catalogs WHERE catalog_id=$1`, catalogID).Scan(&revision)
	if err != nil {
		return PublishedMenu{}, err
	}
	if revision == nil {
		return PublishedMenu{}, ErrNotPublished
	}
	data, err := readRevision(ctx, tx, catalogID, *revision)
	if err != nil {
		return PublishedMenu{}, err
	}
	for ci := range data.Categories {
		for pi := range data.Categories[ci].Products {
			product := &data.Categories[ci].Products[pi]
			product.Availability, product.AvailabilityVersion, err = readProductAvailability(ctx, tx, scope, product.Key)
			if err != nil {
				return PublishedMenu{}, err
			}
			product.EffectivePriceMinor = product.BasePriceMinor
			if mode == "LINKED" {
				var amount int64
				var version int64
				var active bool
				e := tx.QueryRow(ctx, `SELECT price_minor,active,version FROM store_product_price_overrides WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3`, scope.TenantID, scope.StoreID, product.Key).Scan(&amount, &active, &version)
				if e != nil && !errors.Is(e, pgx.ErrNoRows) {
					return PublishedMenu{}, e
				}
				if e == nil {
					product.PriceOverrideVersion = version
					if active {
						product.EffectivePriceMinor = amount
					}
				}
			}
			product.Selectable = product.Active && product.Availability == Available
			for gi := range product.Groups {
				group := &product.Groups[gi]
				availableCount := 0
				for oi := range group.Options {
					option := &group.Options[oi]
					option.Availability, option.AvailabilityVersion, err = readOptionAvailability(ctx, tx, scope, product.Key, group.Key, option.Key)
					if err != nil {
						return PublishedMenu{}, err
					}
					if option.Active && option.Availability == Available {
						availableCount++
					}
				}
				if availableCount < group.MinSelect {
					product.Selectable = false
				}
			}
		}
	}
	return PublishedMenu{TenantID: scope.TenantID, StoreID: scope.StoreID, Mode: mode, Revision: *revision, CatalogData: data}, nil
}

func readProductAvailability(ctx context.Context, tx pgx.Tx, scope StoreScope, productKey string) (Availability, int64, error) {
	var status Availability
	var version int64
	err := tx.QueryRow(ctx, `SELECT status,version FROM store_product_availability WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3`, scope.TenantID, scope.StoreID, productKey).Scan(&status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingConfirmation, 0, nil
	}
	return status, version, err
}

func readOptionAvailability(ctx context.Context, tx pgx.Tx, scope StoreScope, productKey, groupKey, optionKey string) (Availability, int64, error) {
	var status Availability
	var version int64
	err := tx.QueryRow(ctx, `SELECT status,version FROM store_option_availability WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3 AND group_key=$4 AND option_key=$5`, scope.TenantID, scope.StoreID, productKey, groupKey, optionKey).Scan(&status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingConfirmation, 0, nil
	}
	return status, version, err
}

func validateAvailabilityTarget(ctx context.Context, tx pgx.Tx, scope StoreScope, catalogID, revision int64, target AvailabilityTarget) error {
	switch target.Kind {
	case ProductAvailability:
		if !catalogKey(target.ProductKey) || target.GroupKey != "" || target.OptionKey != "" {
			return ErrInvalid
		}
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM menu_revision_products WHERE catalog_id=$1 AND revision=$2 AND product_key=$3)`, catalogID, revision, target.ProductKey).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		return nil
	case OptionAvailability:
		if !catalogKey(target.ProductKey) || !catalogKey(target.GroupKey) || !catalogKey(target.OptionKey) {
			return ErrInvalid
		}
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM menu_revision_options WHERE catalog_id=$1 AND revision=$2 AND product_key=$3 AND group_key=$4 AND option_key=$5)`, catalogID, revision, target.ProductKey, target.GroupKey, target.OptionKey).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		return nil
	default:
		return ErrInvalid
	}
}

func lockLinkedStores(ctx context.Context, tx pgx.Tx, tenantID string) error {
	rows, err := tx.Query(ctx, `SELECT s.store_id FROM stores s WHERE s.tenant_id=$1
		AND NOT EXISTS(SELECT 1 FROM store_menu_bindings b WHERE b.tenant_id=s.tenant_id AND b.store_id=s.store_id)
		ORDER BY s.store_id FOR UPDATE`, tenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var storeID string
		if err = rows.Scan(&storeID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func initializeNewStoreStates(ctx context.Context, tx pgx.Tx, tenantID, actor string, previous, next CatalogData) error {
	oldProducts := flattenProducts(previous)
	newProducts := flattenProducts(next)
	storesRows, err := tx.Query(ctx, `SELECT s.store_id FROM stores s LEFT JOIN store_menu_bindings b USING(tenant_id,store_id) WHERE s.tenant_id=$1 AND b.store_id IS NULL`, tenantID)
	if err != nil {
		return err
	}
	var stores []string
	for storesRows.Next() {
		var id string
		if err = storesRows.Scan(&id); err != nil {
			storesRows.Close()
			return err
		}
		stores = append(stores, id)
	}
	if err = storesRows.Err(); err != nil {
		storesRows.Close()
		return err
	}
	storesRows.Close()
	for _, storeID := range stores {
		for productKey := range newProducts {
			if _, existed := oldProducts[productKey]; existed {
				continue
			}
			if err = resetNewState(ctx, tx, tenantID, storeID, actor, "PRODUCT", productKey, "", ""); err != nil {
				return err
			}
		}
		for productKey, product := range newProducts {
			oldOptions := map[string]bool{}
			if oldProduct, ok := oldProducts[productKey]; ok {
				for _, group := range oldProduct.Groups {
					for _, option := range group.Options {
						oldOptions[group.Key+"\x00"+option.Key] = true
					}
				}
			}
			for _, group := range product.Groups {
				for _, option := range group.Options {
					if oldOptions[group.Key+"\x00"+option.Key] {
						continue
					}
					if err = resetNewState(ctx, tx, tenantID, storeID, actor, "OPTION", productKey, group.Key, option.Key); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func resetNewState(ctx context.Context, tx pgx.Tx, tenantID, storeID, actor, kind, productKey, groupKey, optionKey string) error {
	var before Availability
	var version int64
	var query string
	var args []any
	var operation, entity string
	if kind == "PRODUCT" {
		query = `SELECT status,version FROM store_product_availability WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3 FOR UPDATE`
		args = []any{tenantID, storeID, productKey}
		operation, entity = "NEW_PRODUCT_PENDING", productKey
	} else {
		query = `SELECT status,version FROM store_option_availability WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3 AND group_key=$4 AND option_key=$5 FOR UPDATE`
		args = []any{tenantID, storeID, productKey, groupKey, optionKey}
		operation, entity = "NEW_OPTION_PENDING", productKey+"/"+groupKey+"/"+optionKey
	}
	err := tx.QueryRow(ctx, query, args...).Scan(&before, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		before, version = "", 0
	} else if err != nil {
		return err
	}
	newVersion := version + 1
	if kind == "PRODUCT" {
		_, err = tx.Exec(ctx, `INSERT INTO store_product_availability(tenant_id,store_id,product_key,status,updated_by,version) VALUES($1,$2,$3,'PENDING_CONFIRMATION',$4,$5)
			ON CONFLICT(tenant_id,store_id,product_key) DO UPDATE SET status='PENDING_CONFIRMATION',reason='',updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp(),version=EXCLUDED.version`, tenantID, storeID, productKey, actor, newVersion)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO store_option_availability(tenant_id,store_id,product_key,group_key,option_key,status,updated_by,version) VALUES($1,$2,$3,$4,$5,'PENDING_CONFIRMATION',$6,$7)
			ON CONFLICT(tenant_id,store_id,product_key,group_key,option_key) DO UPDATE SET status='PENDING_CONFIRMATION',reason='',updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp(),version=EXCLUDED.version`, tenantID, storeID, productKey, groupKey, optionKey, actor, newVersion)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO menu_admin_audit(tenant_id,store_id,actor,operation,entity_key,before_value,after_value,before_version,after_version)
		VALUES($1,$2,$3,$4,$5,$6,'PENDING_CONFIRMATION',$7,$8)`, tenantID, storeID, actor, operation, entity, before, version, newVersion)
	return err
}

type availabilityQuery struct {
	where string
	args  []any
}

func availabilityTableAndKey(target AvailabilityTarget) (string, availabilityQuery, Availability, int64) {
	if target.Kind == ProductAvailability {
		return "store_product_availability", availabilityQuery{where: "product_key=$3", args: []any{target.ProductKey}}, PendingConfirmation, 0
	}
	return "store_option_availability", availabilityQuery{where: "product_key=$3 AND group_key=$4 AND option_key=$5", args: []any{target.ProductKey, target.GroupKey, target.OptionKey}}, PendingConfirmation, 0
}

func flattenProducts(data CatalogData) map[string]Product {
	result := map[string]Product{}
	for _, category := range data.Categories {
		for _, product := range category.Products {
			result[product.Key] = product
		}
	}
	return result
}

func validAvailability(status Availability) bool {
	return status == PendingConfirmation || status == Available || status == Paused
}
func allowedTransition(from, to Availability) bool {
	switch from {
	case PendingConfirmation:
		return to == Available || to == Paused
	case Available:
		return to == Paused
	case Paused:
		return to == Available
	}
	return false
}
func availabilityKey(target AvailabilityTarget) string {
	if target.Kind == ProductAvailability {
		return target.ProductKey
	}
	return target.ProductKey + "/" + target.GroupKey + "/" + target.OptionKey
}
func validActorScope(tenant, actor string) bool { return cleanKey(tenant) && cleanKey(actor) }
func validStoreActor(scope StoreScope, actor string) bool {
	return validActorScope(scope.TenantID, actor) && cleanKey(scope.StoreID)
}
func validName(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len([]rune(value)) <= max && strings.TrimSpace(value) == value
}
func formatPrice(value *int64) string {
	if value == nil {
		return ""
	}
	return big.NewInt(*value).String()
}
