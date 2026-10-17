package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	catalog "github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

type (
	CatalogData                = catalog.CatalogData
	Category                   = catalog.Category
	Product                    = catalog.Product
	OptionGroup                = catalog.OptionGroup
	Option                     = catalog.Option
	StoreScope                 = catalog.StoreScope
	Service                    = catalog.Service
	PublishedMenu              = catalog.PublishedMenu
	AvailabilityChange         = catalog.AvailabilityChange
	AvailabilityTarget         = catalog.AvailabilityTarget
	PublicationIssue           = catalog.PublicationIssue
	PublicationValidationError = catalog.PublicationValidationError
)

const (
	OptionAvailability                = catalog.OptionAvailability
	ProductAvailability               = catalog.ProductAvailability
	Paused                            = catalog.Paused
	Available                         = catalog.Available
	PendingConfirmation               = catalog.PendingConfirmation
	PublicationNewRequiredGroup       = catalog.PublicationNewRequiredGroup
	PublicationOptionalBecameRequired = catalog.PublicationOptionalBecameRequired
	PublicationRequiredMinIncreased   = catalog.PublicationRequiredMinIncreased
	PublicationRequiredChoiceShortage = catalog.PublicationRequiredChoiceShortage
)

var ErrInvalid = catalog.ErrInvalid

func TestPostgresP81CompanyRequiredOptionPublicationGuards(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())

	if _, err = fixture.Pool.Exec(ctx, `
		INSERT INTO tenants(tenant_id,name) VALUES ('other-tenant','Other tenant');
		INSERT INTO stores(tenant_id,store_id,name) VALUES
			('test-tenant','linked-2','Second linked store'),
			('test-tenant','detached','Detached store'),
			('other-tenant','other-store','Other tenant store');`); err != nil {
		t.Fatal(err)
	}
	scope := StoreScope{TenantID: "test-tenant", StoreID: "test-store"}
	secondScope := StoreScope{TenantID: "test-tenant", StoreID: "linked-2"}
	detachedScope := StoreScope{TenantID: "test-tenant", StoreID: "detached"}
	otherScope := StoreScope{TenantID: "other-tenant", StoreID: "other-store"}
	if _, err = fixture.Menus.DetachStore(ctx, detachedScope, "p81-test", 1); err != nil {
		t.Fatalf("detach independent store fixture: %v", err)
	}
	if err = seedSecondLinkedStoreAvailability(ctx, fixture.Menus, secondScope); err != nil {
		t.Fatal(err)
	}
	otherData := CatalogData{Categories: []Category{{Key: "meals", Name: "Meals", Products: []Product{{
		Key: "teishoku", Name: "Other tenant meal", BasePriceMinor: 800, Active: true,
		Groups: []OptionGroup{{Key: "rice", Name: "Rice", MinSelect: 1, MaxSelect: 1, Options: []Option{{Key: "regular", Name: "Regular", Active: true}}}},
	}}}}}
	otherDraft, err := fixture.Menus.SaveCompanyDraft(ctx, "other-tenant", "p81-test", 0, otherData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Menus.PublishCompanyDraft(ctx, "other-tenant", "p81-test", otherDraft.Version); err != nil {
		t.Fatalf("publish other tenant's independent catalog: %v", err)
	}
	if err = setProductAndOptionsAvailable(ctx, fixture.Menus, otherScope, "teishoku"); err != nil {
		t.Fatal(err)
	}

	baselineDraft, err := fixture.Menus.GetCompanyDraft(ctx, "test-tenant")
	if err != nil {
		t.Fatal(err)
	}
	baseline := baselineDraft.CatalogData
	version := baselineDraft.Version
	attempt := func(data CatalogData) error {
		t.Helper()
		draft, saveErr := fixture.Menus.SaveCompanyDraft(ctx, "test-tenant", "p81-test", version, data)
		if saveErr != nil {
			return saveErr
		}
		version = draft.Version
		_, publishErr := fixture.Menus.PublishCompanyDraft(ctx, "test-tenant", "p81-test", draft.Version)
		return publishErr
	}

	newRequired := cloneCatalogData(t, baseline)
	menuProduct(newRequired, "teishoku").Groups = append(menuProduct(newRequired, "teishoku").Groups, OptionGroup{
		Key: "sauce", Name: "Sauce", MinSelect: 1, MaxSelect: 1, Options: []Option{{Key: "miso", Name: "Miso", Active: true}},
	})
	assertPublicationIssue(t, attempt(newRequired), PublicationNewRequiredGroup, "teishoku", "sauce", "", 1, 0)
	assertCompanyRevision(t, ctx, fixture, "test-tenant", 1)

	optionalRequired := cloneCatalogData(t, baseline)
	menuProduct(optionalRequired, "tea").Groups[0].MinSelect = 1
	assertPublicationIssue(t, attempt(optionalRequired), PublicationOptionalBecameRequired, "tea", "aroma", "", 1, 0)
	assertCompanyRevision(t, ctx, fixture, "test-tenant", 1)

	keyRename := cloneCatalogData(t, baseline)
	menuProduct(keyRename, "tea").Groups[0].Key = "aroma-renamed"
	menuProduct(keyRename, "tea").Groups[0].MinSelect = 1
	assertPublicationIssue(t, attempt(keyRename), PublicationNewRequiredGroup, "tea", "aroma-renamed", "", 1, 0)
	assertCompanyRevision(t, ctx, fixture, "test-tenant", 1)

	minIncrease := cloneCatalogData(t, baseline)
	menuProduct(minIncrease, "teishoku").Groups[0].MinSelect = 2
	menuProduct(minIncrease, "teishoku").Groups[0].MaxSelect = 2
	assertPublicationIssue(t, attempt(minIncrease), PublicationRequiredMinIncreased, "teishoku", "rice", "", 2, 0)
	assertCompanyRevision(t, ctx, fixture, "test-tenant", 1)

	// A stale AVAILABLE row for a new stable option must not be counted to
	// justify disabling the only old available option at the currently selling
	// first store. The second linked store remains unaffected and has an old
	// AVAILABLE choice; detached and foreign-tenant stores are not in scope.
	storeMenu, err := fixture.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	large := menuOption(t, storeMenu.CatalogData, "teishoku", "rice", "large")
	if _, err = fixture.Menus.SetStoreAvailability(ctx, scope, "p81-test", AvailabilityChange{
		Target: AvailabilityTarget{Kind: OptionAvailability, ProductKey: "teishoku", GroupKey: "rice", OptionKey: "large"},
		Status: Paused, Reason: "fixture pause", ExpectedVersion: large.AvailabilityVersion,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Pool.Exec(ctx, `INSERT INTO store_option_availability(tenant_id,store_id,product_key,group_key,option_key,status,updated_by)
		VALUES('test-tenant','test-store','teishoku','rice','extra','AVAILABLE','stale-fixture')`); err != nil {
		t.Fatal(err)
	}
	losingChoice := cloneCatalogData(t, baseline)
	rice := menuGroup(losingChoice, "teishoku", "rice")
	rice.Options = append(rice.Options, Option{Key: "extra", Name: "Extra", Active: true})
	rice.MaxSelect = 2
	for i := range rice.Options {
		if rice.Options[i].Key == "regular" {
			rice.Options[i].Active = false
		}
	}
	issues := assertPublicationIssue(t, attempt(losingChoice), PublicationRequiredChoiceShortage, "teishoku", "rice", "test-store", 1, 0)
	if issues[0].StoreName != "P78 test store" {
		t.Fatalf("store-specific issue omitted current store name: %+v", issues[0])
	}
	assertCompanyRevision(t, ctx, fixture, "test-tenant", 1)
	unchanged, err := fixture.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != 1 || !menuProduct(unchanged.CatalogData, "teishoku").Selectable {
		t.Fatalf("failed validation changed published menu or sellability: %+v", unchanged)
	}

	// A safe publish can add required-group choices, optional groups, and a new
	// required product. The new keys start PENDING while old statuses survive.
	safeAddition := cloneCatalogData(t, baseline)
	safeRice := menuGroup(safeAddition, "teishoku", "rice")
	safeRice.Options = append(safeRice.Options, Option{Key: "extra", Name: "Extra", Active: true})
	safeRice.MaxSelect = 2
	menuProduct(safeAddition, "teishoku").Groups = append(menuProduct(safeAddition, "teishoku").Groups, OptionGroup{
		Key: "sauce", Name: "Sauce", MinSelect: 0, MaxSelect: 1, Options: []Option{{Key: "miso", Name: "Miso", Active: true}},
	})
	safeAddition.Categories[0].Products = append(safeAddition.Categories[0].Products, Product{
		Key: "new-meal", Name: "New meal", BasePriceMinor: 500, Active: true,
		Groups: []OptionGroup{{Key: "size", Name: "Size", MinSelect: 1, MaxSelect: 1, Options: []Option{{Key: "small", Name: "Small", Active: true}}}},
	})
	if err = attempt(safeAddition); err != nil {
		t.Fatalf("safe option/product/optional-group addition failed: %v", err)
	}
	assertCompanyRevision(t, ctx, fixture, "test-tenant", 2)
	readAfterAddition, err := fixture.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	addedRiceOption := menuOption(t, readAfterAddition.CatalogData, "teishoku", "rice", "extra")
	if addedRiceOption.Availability != PendingConfirmation {
		t.Fatalf("new required-group option was not pending: %+v", addedRiceOption)
	}
	for key, want := range map[string]catalog.Availability{"regular": Available, "large": Paused} {
		if got := menuOption(t, readAfterAddition.CatalogData, "teishoku", "rice", key).Availability; got != want {
			t.Fatalf("old option %s state changed after publication: got %s want %s", key, got, want)
		}
	}
	if !menuProduct(readAfterAddition.CatalogData, "teishoku").Selectable {
		t.Fatal("adding pending optional group/required choice made existing product unselectable")
	}
	newProduct := menuProduct(readAfterAddition.CatalogData, "new-meal")
	if newProduct.Availability != PendingConfirmation || menuOption(t, readAfterAddition.CatalogData, "new-meal", "size", "small").Availability != PendingConfirmation {
		t.Fatalf("new required product must await store confirmation: %+v", newProduct)
	}
	if _, err = fixture.Menus.SetStoreAvailability(ctx, scope, "p81-test", AvailabilityChange{
		Target: AvailabilityTarget{Kind: OptionAvailability, ProductKey: "teishoku", GroupKey: "rice", OptionKey: "extra"},
		Status: Available, ExpectedVersion: addedRiceOption.AvailabilityVersion,
	}); err != nil {
		t.Fatalf("confirm new required-group option: %v", err)
	}

	// Retirement is allowed once each currently selling linked store retains
	// enough previously confirmed choices. A detached store and another tenant
	// continue to read their original independent revisions.
	safeRetirement := cloneCatalogData(t, safeAddition)
	for i := range menuGroup(safeRetirement, "teishoku", "rice").Options {
		if menuGroup(safeRetirement, "teishoku", "rice").Options[i].Key == "regular" {
			menuGroup(safeRetirement, "teishoku", "rice").Options[i].Active = false
		}
	}
	if err = attempt(safeRetirement); err != nil {
		t.Fatalf("safe required option retirement failed: %v", err)
	}
	assertCompanyRevision(t, ctx, fixture, "test-tenant", 3)
	for label, storeScope := range map[string]StoreScope{"detached": detachedScope, "other tenant": otherScope} {
		menu, readErr := fixture.Menus.ReadStoreMenu(ctx, storeScope)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if menu.Revision != 1 {
			t.Fatalf("company publish affected %s menu revision: %d", label, menu.Revision)
		}
	}
	detachedMenu := menuAfterRead(t, ctx, fixture.Menus, detachedScope)
	if hasMenuOption(detachedMenu.CatalogData, "teishoku", "rice", "extra") {
		t.Fatal("detached store unexpectedly received company option")
	}
}

func TestPostgresP81PublishSerializesWithStoreAvailability(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())

	scope := StoreScope{TenantID: "test-tenant", StoreID: "test-store"}
	menu, err := fixture.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	large := menuOption(t, menu.CatalogData, "teishoku", "rice", "large")
	if _, err = fixture.Menus.SetStoreAvailability(ctx, scope, "p81-race", AvailabilityChange{
		Target: AvailabilityTarget{Kind: OptionAvailability, ProductKey: "teishoku", GroupKey: "rice", OptionKey: "large"},
		Status: Paused, Reason: "race fixture", ExpectedVersion: large.AvailabilityVersion,
	}); err != nil {
		t.Fatal(err)
	}
	menu, err = fixture.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	regular := menuOption(t, menu.CatalogData, "teishoku", "rice", "regular")

	draft, err := fixture.Menus.GetCompanyDraft(ctx, "test-tenant")
	if err != nil {
		t.Fatal(err)
	}
	for i := range menuGroup(draft.CatalogData, "teishoku", "rice").Options {
		option := &menuGroup(draft.CatalogData, "teishoku", "rice").Options[i]
		if option.Key == "regular" {
			option.Active = false
		}
	}
	draft, err = fixture.Menus.SaveCompanyDraft(ctx, "test-tenant", "p81-race", draft.Version, draft.CatalogData)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var publishErr, availabilityErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, publishErr = fixture.Menus.PublishCompanyDraft(ctx, "test-tenant", "p81-race", draft.Version)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, availabilityErr = fixture.Menus.SetStoreAvailability(ctx, scope, "p81-race", AvailabilityChange{
			Target: AvailabilityTarget{Kind: OptionAvailability, ProductKey: "teishoku", GroupKey: "rice", OptionKey: "regular"},
			Status: Paused, Reason: "concurrent availability change", ExpectedVersion: regular.AvailabilityVersion,
		})
	}()
	close(start)
	wg.Wait()
	if availabilityErr != nil {
		t.Fatalf("concurrent availability update: %v", availabilityErr)
	}
	if publishErr != nil {
		var validation *PublicationValidationError
		if !errors.As(publishErr, &validation) {
			t.Fatalf("publish returned unexpected race error: %v", publishErr)
		}
	}
	current, err := fixture.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	wantRevision := int64(1)
	if publishErr == nil {
		wantRevision = 2
	}
	if current.Revision != wantRevision {
		t.Fatalf("publish/availability race left revision %d, want %d (publish err %v)", current.Revision, wantRevision, publishErr)
	}
	if got := menuOption(t, current.CatalogData, "teishoku", "rice", "regular").Availability; got != Paused {
		t.Fatalf("concurrent store transition was not preserved: %s", got)
	}
	if menuProduct(current.CatalogData, "teishoku").Selectable {
		t.Fatal("resulting menu reports product selectable after all required options became unavailable")
	}
}

func TestPostgresP81PublishRechecksAvailabilityAfterStoreLockWait(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(context.Background())

	scope := StoreScope{TenantID: "test-tenant", StoreID: "test-store"}
	menu, err := fixture.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if got := menuOption(t, menu.CatalogData, "teishoku", "rice", "regular").Availability; got != Available {
		t.Fatalf("fixture regular option status=%s want %s", got, Available)
	}
	if got := menuOption(t, menu.CatalogData, "teishoku", "rice", "large").Availability; got != Available {
		t.Fatalf("fixture large option status=%s want %s", got, Available)
	}

	draft, err := fixture.Menus.GetCompanyDraft(ctx, scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range menuGroup(draft.CatalogData, "teishoku", "rice").Options {
		option := &menuGroup(draft.CatalogData, "teishoku", "rice").Options[i]
		if option.Key == "regular" {
			option.Active = false
		}
	}
	draft, err = fixture.Menus.SaveCompanyDraft(ctx, scope.TenantID, "p81-lock-wait", draft.Version, draft.CatalogData)
	if err != nil {
		t.Fatal(err)
	}

	held, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(context.Background())
	if _, err = held.Exec(ctx, `SELECT store_id FROM stores WHERE tenant_id=$1 AND store_id=$2 FOR UPDATE`, scope.TenantID, scope.StoreID); err != nil {
		t.Fatal(err)
	}
	publishResult := make(chan error, 1)
	go func() {
		_, publishErr := fixture.Menus.PublishCompanyDraft(ctx, scope.TenantID, "p81-lock-wait", draft.Version)
		publishResult <- publishErr
	}()
	waitForLinkedStoreLockWait(t, ctx, fixture.Pool, int32(held.Conn().PgConn().PID()))

	var oldVersion int64
	if err = held.QueryRow(ctx, `SELECT version FROM store_option_availability
		WHERE tenant_id=$1 AND store_id=$2 AND product_key='teishoku' AND group_key='rice' AND option_key='large' FOR UPDATE`,
		scope.TenantID, scope.StoreID).Scan(&oldVersion); err != nil {
		t.Fatal(err)
	}
	tag, err := held.Exec(ctx, `UPDATE store_option_availability SET status='PAUSED',reason='serialization regression',updated_by='p81-lock-wait',updated_at=clock_timestamp(),version=version+1
		WHERE tenant_id=$1 AND store_id=$2 AND product_key='teishoku' AND group_key='rice' AND option_key='large' AND status='AVAILABLE'`, scope.TenantID, scope.StoreID)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("pause update affected %d rows, want 1", tag.RowsAffected())
	}
	if _, err = held.Exec(ctx, `INSERT INTO menu_admin_audit(tenant_id,store_id,actor,operation,entity_key,before_value,after_value,before_version,after_version)
		VALUES($1,$2,'p81-lock-wait','AVAILABILITY_OPTION','teishoku/rice/large','AVAILABLE','PAUSED',$3,$4)`,
		scope.TenantID, scope.StoreID, oldVersion, oldVersion+1); err != nil {
		t.Fatal(err)
	}
	if err = held.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case publishErr := <-publishResult:
		var validation *PublicationValidationError
		if !errors.As(publishErr, &validation) {
			t.Fatalf("publish after availability lock wait error=%v, want publication validation error", publishErr)
		}
		found := false
		for _, issue := range validation.Issues {
			if issue.Code == PublicationRequiredChoiceShortage && issue.ProductKey == "teishoku" && issue.GroupKey == "rice" && issue.StoreID == scope.StoreID && issue.RequiredCount == 1 && issue.AvailableCount == 0 {
				found = true
			}
		}
		if !found {
			t.Fatalf("post-wait publication issues do not show the latest option state: %+v", validation.Issues)
		}
	case <-ctx.Done():
		t.Fatal("publication did not finish after the store lock was released")
	}

	unchanged, err := fixture.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != 1 || !menuProduct(unchanged.CatalogData, "teishoku").Selectable {
		t.Fatalf("stale availability publish changed current menu: revision=%d product=%+v", unchanged.Revision, menuProduct(unchanged.CatalogData, "teishoku"))
	}
	if got := menuOption(t, unchanged.CatalogData, "teishoku", "rice", "large").Availability; got != Paused {
		t.Fatalf("serialized store availability update was not committed: %s", got)
	}
}

func waitForLinkedStoreLockWait(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blockerPID int32) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND state='active' AND wait_event_type='Lock'
			AND $1::int=ANY(pg_blocking_pids(pid))
			AND lower(query) LIKE '%select s.store_id from stores s where s.tenant_id=$1%')`, blockerPID).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for publication query to block on linked-store row")
		case <-deadline.C:
			t.Fatal("publication did not visibly wait on linked-store row lock")
		case <-ticker.C:
		}
	}
}

func seedSecondLinkedStoreAvailability(ctx context.Context, service *Service, scope StoreScope) error {
	menu, err := service.ReadStoreMenu(ctx, scope)
	if err != nil {
		return err
	}
	product := menuProduct(menu.CatalogData, "teishoku")
	if _, err = service.SetStoreAvailability(ctx, scope, "p81-test", AvailabilityChange{
		Target: AvailabilityTarget{Kind: ProductAvailability, ProductKey: product.Key}, Status: Available, ExpectedVersion: product.AvailabilityVersion,
	}); err != nil {
		return err
	}
	for _, group := range product.Groups {
		for _, option := range group.Options {
			if _, err = service.SetStoreAvailability(ctx, scope, "p81-test", AvailabilityChange{
				Target: AvailabilityTarget{Kind: OptionAvailability, ProductKey: product.Key, GroupKey: group.Key, OptionKey: option.Key}, Status: Available, ExpectedVersion: option.AvailabilityVersion,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func setProductAndOptionsAvailable(ctx context.Context, service *Service, scope StoreScope, key string) error {
	menu, err := service.ReadStoreMenu(ctx, scope)
	if err != nil {
		return err
	}
	product := menuProduct(menu.CatalogData, key)
	if _, err = service.SetStoreAvailability(ctx, scope, "p81-test", AvailabilityChange{
		Target: AvailabilityTarget{Kind: ProductAvailability, ProductKey: product.Key}, Status: Available, ExpectedVersion: product.AvailabilityVersion,
	}); err != nil {
		return err
	}
	for _, group := range product.Groups {
		for _, option := range group.Options {
			if _, err = service.SetStoreAvailability(ctx, scope, "p81-test", AvailabilityChange{
				Target: AvailabilityTarget{Kind: OptionAvailability, ProductKey: product.Key, GroupKey: group.Key, OptionKey: option.Key}, Status: Available, ExpectedVersion: option.AvailabilityVersion,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func assertPublicationIssue(t *testing.T, err error, code, productKey, groupKey, storeID string, required, available int) []PublicationIssue {
	t.Helper()
	var validation *PublicationValidationError
	if !errors.Is(err, ErrInvalid) || !errors.As(err, &validation) {
		t.Fatalf("publish error=%v, want structured PublicationValidationError wrapping ErrInvalid", err)
	}
	for _, issue := range validation.Issues {
		if issue.Code == code && issue.ProductKey == productKey && issue.GroupKey == groupKey && issue.StoreID == storeID && issue.RequiredCount == required && issue.AvailableCount == available {
			return validation.Issues
		}
	}
	t.Fatalf("publication issues=%+v, missing code=%s product=%s group=%s store=%s required=%d available=%d", validation.Issues, code, productKey, groupKey, storeID, required, available)
	return nil
}

func assertCompanyRevision(t *testing.T, ctx context.Context, fixture *testguest.Fixture, tenantID string, want int64) {
	t.Helper()
	menu, err := fixture.Menus.ReadStoreMenu(ctx, StoreScope{TenantID: tenantID, StoreID: map[string]string{"test-tenant": "test-store", "other-tenant": "other-store"}[tenantID]})
	if err != nil {
		t.Fatal(err)
	}
	if menu.Revision != want {
		t.Fatalf("published pointer changed on invalid draft: tenant=%s revision=%d want=%d", tenantID, menu.Revision, want)
	}
}

func cloneCatalogData(t *testing.T, data CatalogData) CatalogData {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var clone CatalogData
	if err = json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func menuProduct(data CatalogData, key string) *Product {
	for ci := range data.Categories {
		for pi := range data.Categories[ci].Products {
			if data.Categories[ci].Products[pi].Key == key {
				return &data.Categories[ci].Products[pi]
			}
		}
	}
	panic("test product not found: " + key)
}

func menuGroup(data CatalogData, productKey, groupKey string) *OptionGroup {
	product := menuProduct(data, productKey)
	for i := range product.Groups {
		if product.Groups[i].Key == groupKey {
			return &product.Groups[i]
		}
	}
	panic("test group not found: " + productKey + "/" + groupKey)
}

func menuOption(t *testing.T, data CatalogData, productKey, groupKey, optionKey string) Option {
	t.Helper()
	for _, option := range menuGroup(data, productKey, groupKey).Options {
		if option.Key == optionKey {
			return option
		}
	}
	t.Fatalf("test option not found: %s/%s/%s", productKey, groupKey, optionKey)
	return Option{}
}

func hasMenuOption(data CatalogData, productKey, groupKey, optionKey string) bool {
	for _, option := range menuGroup(data, productKey, groupKey).Options {
		if option.Key == optionKey {
			return true
		}
	}
	return false
}

func menuAfterRead(t *testing.T, ctx context.Context, service *Service, scope StoreScope) PublishedMenu {
	t.Helper()
	menu, err := service.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	return menu
}
