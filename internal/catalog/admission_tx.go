package catalog

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// LockStoreMenuForAdmissionTx takes the catalog-side locks required before a
// table/session/cart admission. Callers must keep tx open through quote and
// admission. The global order is company catalog -> tenant/store -> binding
// and detached catalog -> tax schedule -> dining table -> table session -> cart.
//
// FOR SHARE conflicts with the catalog/store writers' FOR UPDATE locks. The
// schedule table lock additionally blocks inserts as well as rate edits during
// this short transaction, preventing a new overlapping legal row from racing
// the quote. Missing company catalogs are not created here; unpublished menus
// remain unpublished.
func (s *Service) LockStoreMenuForAdmissionTx(ctx context.Context, tx pgx.Tx, scope StoreScope) (PublishedMenu, error) {
	if s == nil || tx == nil || scope.TenantID == "" || scope.StoreID == "" {
		return PublishedMenu{}, ErrInvalid
	}
	var companyCatalogID int64
	companyErr := tx.QueryRow(ctx, `SELECT catalog_id FROM menu_catalogs
		WHERE tenant_id=$1 AND owner_kind='COMPANY' FOR SHARE`, scope.TenantID).Scan(&companyCatalogID)
	if companyErr != nil && !errors.Is(companyErr, pgx.ErrNoRows) {
		return PublishedMenu{}, companyErr
	}

	var tenantEnabled bool
	err := tx.QueryRow(ctx, `SELECT enabled FROM tenants WHERE tenant_id=$1 FOR SHARE`, scope.TenantID).Scan(&tenantEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublishedMenu{}, ErrNotFound
	}
	if err != nil {
		return PublishedMenu{}, err
	}
	if !tenantEnabled {
		return PublishedMenu{}, ErrDenied
	}
	var storeEnabled bool
	err = tx.QueryRow(ctx, `SELECT enabled AND operating FROM stores
		WHERE tenant_id=$1 AND store_id=$2 FOR SHARE`, scope.TenantID, scope.StoreID).Scan(&storeEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublishedMenu{}, ErrNotFound
	}
	if err != nil {
		return PublishedMenu{}, err
	}
	if !storeEnabled {
		return PublishedMenu{}, ErrDenied
	}

	// Resolve the binding only after the parent locks. A detached binding is
	// locked before its immutable source catalog; linked mode uses the already
	// locked company catalog.
	var boundCatalogID int64
	err = tx.QueryRow(ctx, `SELECT catalog_id FROM store_menu_bindings
		WHERE tenant_id=$1 AND store_id=$2 FOR SHARE`, scope.TenantID, scope.StoreID).Scan(&boundCatalogID)
	if err == nil {
		var tenantID, ownerKind, storeID string
		err = tx.QueryRow(ctx, `SELECT tenant_id,owner_kind,store_id FROM menu_catalogs
			WHERE catalog_id=$1 FOR SHARE`, boundCatalogID).Scan(&tenantID, &ownerKind, &storeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return PublishedMenu{}, ErrNotPublished
		}
		if err != nil {
			return PublishedMenu{}, err
		}
		if tenantID != scope.TenantID || ownerKind != "STORE" || storeID != scope.StoreID {
			return PublishedMenu{}, ErrDenied
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return PublishedMenu{}, err
	} else if companyErr != nil { // no binding and no company catalog
		return PublishedMenu{}, ErrNotPublished
	}

	menu, err := readStoreMenu(ctx, tx, scope)
	if err != nil {
		return PublishedMenu{}, err
	}
	// The locked rows and read path must resolve to the same source. This guards
	// against accidental use of the helper with a transaction isolation/locking
	// protocol that does not preserve the intended menu identity.
	if boundCatalogID != 0 && menu.CatalogID != boundCatalogID || boundCatalogID == 0 && menu.CatalogID != companyCatalogID {
		return PublishedMenu{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE tax_rate_schedule IN SHARE MODE`); err != nil {
		return PublishedMenu{}, err
	}
	return menu, nil
}

// ReadStoreMenuTx reads one menu inside a caller-owned transaction without
// taking admission locks. It is intended for consistent reads that already
// have their own locking protocol, not for order admission.
func (s *Service) ReadStoreMenuTx(ctx context.Context, tx pgx.Tx, scope StoreScope) (PublishedMenu, error) {
	if s == nil || tx == nil || scope.TenantID == "" || scope.StoreID == "" {
		return PublishedMenu{}, ErrInvalid
	}
	return readStoreMenu(ctx, tx, scope)
}
