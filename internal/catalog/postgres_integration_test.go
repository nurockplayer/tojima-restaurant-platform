package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func TestPostgresCatalogAcceptance(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "p77_catalog_" + hex.EncodeToString(random)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, dropErr := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); dropErr != nil {
			t.Errorf("drop owned catalog test schema: %v", dropErr)
		}
	}()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = postgres.Up(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ('t','Tenant'),('other','Other'),('fk-fixture','FK fixture'),('writer-fixture','Writer fixture');
		INSERT INTO stores(tenant_id,store_id,name) VALUES ('t','s1','One'),('t','s2','Two'),('other','s1','Other one'),('fk-fixture','store','Foreign-key fixture');
		INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('t','s1','table','Table one','locator-one'),('t','s2','table','Table two','locator-two'),('other','s1','table','Other table','other-locator')`); err != nil {
		t.Fatal(err)
	}
	service, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("draft publish and linked detached price semantics", func(t *testing.T) {
		initial := sampleMenu("Soup", 900, false)
		draft, err := service.SaveCompanyDraft(ctx, "t", "owner", 0, initial)
		if err != nil || draft.Version != 1 {
			t.Fatalf("save company draft: version=%d err=%v", draft.Version, err)
		}
		if _, err = service.ReadPublishedByTableLocator(ctx, "locator-one"); !errors.Is(err, ErrNotPublished) {
			t.Fatalf("draft leaked before publish: %v", err)
		}
		published, err := service.PublishCompanyDraft(ctx, "t", "owner", draft.Version)
		if err != nil || published.Revision != 1 {
			t.Fatalf("publish company draft: %+v %v", published, err)
		}
		var sealed bool
		if err = pool.QueryRow(ctx, `SELECT sealed FROM menu_catalog_revisions WHERE catalog_id=(SELECT catalog_id FROM menu_catalogs WHERE tenant_id='t' AND owner_kind='COMPANY') AND revision=1`).Scan(&sealed); err != nil || !sealed {
			t.Fatalf("company publication was not sealed: sealed=%v err=%v", sealed, err)
		}
		if _, err = pool.Exec(ctx, `UPDATE menu_revision_products SET name='mutated' WHERE catalog_id=(SELECT catalog_id FROM menu_catalogs WHERE tenant_id='t' AND owner_kind='COMPANY') AND revision=1`); err == nil {
			t.Fatal("database allowed mutation of a published revision")
		}
		companyCatalogID := catalogID(t, ctx, pool, "t", "COMPANY", "")
		for _, query := range []string{
			`UPDATE menu_catalog_revisions SET sealed=false WHERE catalog_id=$1 AND revision=1`,
			`UPDATE menu_catalog_revisions SET published_by='attacker' WHERE catalog_id=$1 AND revision=1`,
			`DELETE FROM menu_catalog_revisions WHERE catalog_id=$1 AND revision=1`,
		} {
			if _, err = pool.Exec(ctx, query, companyCatalogID); err == nil {
				t.Fatalf("database allowed forbidden revision mutation: %s", query)
			}
		}
		if _, err = pool.Exec(ctx, `INSERT INTO menu_catalog_revisions(catalog_id,revision,draft_version,published_by,sealed) VALUES($1,99,99,'attacker',true)`, companyCatalogID); err == nil {
			t.Fatal("database allowed revision insertion already sealed")
		}
		sealedChildInserts := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO menu_revision_categories(catalog_id,revision,category_key,name,position) VALUES($1,1,'late','Late',9)`, []any{companyCatalogID}},
			{`INSERT INTO menu_revision_products(catalog_id,revision,product_key,category_key,name,description,base_price_minor,position,active) VALUES($1,1,'late','food','Late','',1,9,true)`, []any{companyCatalogID}},
			{`INSERT INTO menu_revision_option_groups(catalog_id,revision,product_key,group_key,name,min_select,max_select,position) VALUES($1,1,'soup','late','Late',0,1,9)`, []any{companyCatalogID}},
			{`INSERT INTO menu_revision_options(catalog_id,revision,product_key,group_key,option_key,name,price_delta_minor,position,active) VALUES($1,1,'soup','size','late','Late',0,9,true)`, []any{companyCatalogID}},
		}
		for _, insert := range sealedChildInserts {
			if _, err = pool.Exec(ctx, insert.query, insert.args...); err == nil || !strings.Contains(err.Error(), "sealed") {
				t.Fatalf("published child insert was not rejected by seal guard: %v", err)
			}
		}
		assertUnsealedRevisionCannotBecomeCurrent(t, ctx, pool, companyCatalogID)
		if _, err = service.PublishCompanyDraft(ctx, "t", "owner", draft.Version); !errors.Is(err, ErrStale) {
			t.Fatalf("repeat stale publish accepted: %v", err)
		}
		if _, err = service.SetStoreAvailability(ctx, StoreScope{"t", "s1"}, "manager", AvailabilityChange{Target: AvailabilityTarget{Kind: ProductAvailability, ProductKey: "soup"}, Status: Available, ExpectedVersion: 1}); err != nil {
			t.Fatalf("make existing product available: %v", err)
		}
		if _, err = service.SetStoreAvailability(ctx, StoreScope{"t", "s1"}, "manager", AvailabilityChange{Target: AvailabilityTarget{Kind: OptionAvailability, ProductKey: "soup", GroupKey: "size", OptionKey: "large"}, Status: Available, ExpectedVersion: 1}); err != nil {
			t.Fatalf("make existing option available: %v", err)
		}
		if _, err = service.SetStoreAvailability(ctx, StoreScope{"t", "s2"}, "manager", AvailabilityChange{Target: AvailabilityTarget{Kind: ProductAvailability, ProductKey: "soup"}, Status: Available, ExpectedVersion: 1}); err != nil {
			t.Fatalf("make linked product available: %v", err)
		}
		override, err := service.SetStorePriceOverride(ctx, StoreScope{"t", "s1"}, "manager", "soup", 1100, 0)
		if err != nil || override.Version != 1 {
			t.Fatalf("set linked override: %+v %v", override, err)
		}
		var beforeAmount int64
		var afterAmount int64
		var beforeSource string
		var auditActor string
		if err = pool.QueryRow(ctx, `SELECT before_value,before_amount,after_amount,actor FROM menu_admin_audit WHERE tenant_id='t' AND store_id='s1' AND operation='STORE_PRICE_OVERRIDE' AND entity_key='soup'`).Scan(&beforeSource, &beforeAmount, &afterAmount, &auditActor); err != nil {
			t.Fatal(err)
		}
		if beforeSource != "COMPANY_PRICE" || beforeAmount != 900 || afterAmount != 1100 || auditActor != "manager" {
			t.Fatalf("price audit omitted effective before/after amounts or provenance: %s %d %d %q", beforeSource, beforeAmount, afterAmount, auditActor)
		}
		reset, err := service.ResetStorePriceOverride(ctx, StoreScope{"t", "s1"}, "manager", "soup", override.Version)
		if err != nil || reset.PriceMinor != 900 || reset.Active || reset.Version != 2 {
			t.Fatalf("reset override did not return current company price: %+v %v", reset, err)
		}
		var resetBefore, resetAfter int64
		if err = pool.QueryRow(ctx, `SELECT before_amount,after_amount FROM menu_admin_audit WHERE tenant_id='t' AND store_id='s1' AND operation='STORE_PRICE_OVERRIDE_RESET' AND entity_key='soup'`).Scan(&resetBefore, &resetAfter); err != nil {
			t.Fatal(err)
		}
		if resetBefore != 1100 || resetAfter != 900 {
			t.Fatalf("override reset audit omitted effective company price: %d → %d", resetBefore, resetAfter)
		}
		override, err = service.SetStorePriceOverride(ctx, StoreScope{"t", "s1"}, "manager", "soup", 1200, reset.Version)
		if err != nil || override.Version != 3 {
			t.Fatalf("reapply store override after reset: %+v %v", override, err)
		}
		var reenabledSource string
		var reenabledBefore, reenabledAfter int64
		if err = pool.QueryRow(ctx, `SELECT before_value,before_amount,after_amount FROM menu_admin_audit WHERE tenant_id='t' AND store_id='s1' AND operation='STORE_PRICE_OVERRIDE' AND entity_key='soup' ORDER BY audit_id DESC LIMIT 1`).Scan(&reenabledSource, &reenabledBefore, &reenabledAfter); err != nil {
			t.Fatal(err)
		}
		if reenabledSource != "COMPANY_PRICE" || reenabledBefore != 900 || reenabledAfter != 1200 {
			t.Fatalf("re-enabled override audited inactive stored price instead of effective base: %s %d → %d", reenabledSource, reenabledBefore, reenabledAfter)
		}
		menu, err := service.ReadStoreMenu(ctx, StoreScope{"t", "s1"})
		if err != nil || menu.Categories[0].Products[0].EffectivePriceMinor != 1200 {
			t.Fatalf("linked effective price: %+v %v", menu, err)
		}
		if _, err = service.DetachStore(ctx, StoreScope{"t", "s1"}, "owner", 1); err != nil {
			t.Fatalf("detach store: %v", err)
		}
		var companyCategoryID, detachedCategoryID int64
		if err = pool.QueryRow(ctx, `SELECT category_id FROM menu_revision_categories WHERE catalog_id=(SELECT catalog_id FROM menu_catalogs WHERE tenant_id='t' AND owner_kind='COMPANY') AND revision=1`).Scan(&companyCategoryID); err != nil {
			t.Fatal(err)
		}
		if err = pool.QueryRow(ctx, `SELECT category_id FROM menu_revision_categories WHERE catalog_id=(SELECT catalog_id FROM menu_catalogs WHERE tenant_id='t' AND owner_kind='STORE' AND store_id='s1') AND revision=1`).Scan(&detachedCategoryID); err != nil {
			t.Fatal(err)
		}
		if err = pool.QueryRow(ctx, `SELECT sealed FROM menu_catalog_revisions WHERE catalog_id=(SELECT catalog_id FROM menu_catalogs WHERE tenant_id='t' AND owner_kind='STORE' AND store_id='s1') AND revision=1`).Scan(&sealed); err != nil || !sealed {
			t.Fatalf("detached publication was not sealed: sealed=%v err=%v", sealed, err)
		}
		if companyCategoryID == detachedCategoryID {
			t.Fatal("detach reused company category identity")
		}
		menu, err = service.ReadStoreMenu(ctx, StoreScope{"t", "s1"})
		if err != nil || menu.Mode != "DETACHED" || menu.Categories[0].Products[0].EffectivePriceMinor != 1200 {
			t.Fatalf("detached snapshot did not preserve effective price: %+v %v", menu, err)
		}

		next := sampleMenu("Soup v2", 1000, true)
		draft, err = service.SaveCompanyDraft(ctx, "t", "owner", 1, next)
		if err != nil || draft.Version != 2 {
			t.Fatalf("save second company draft: version=%d err=%v", draft.Version, err)
		}
		if _, err = service.SaveCompanyDraft(ctx, "t", "owner", 1, next); !errors.Is(err, ErrStale) {
			t.Fatalf("stale draft CAS accepted: %v", err)
		}
		if _, err = service.PublishCompanyDraft(ctx, "t", "owner", draft.Version); err != nil {
			t.Fatalf("publish second company draft: %v", err)
		}
		linked, err := service.ReadStoreMenu(ctx, StoreScope{"t", "s2"})
		if err != nil {
			t.Fatal(err)
		}
		if linked.Categories[0].Products[0].Name != "Soup v2" || linked.Categories[0].Products[0].Availability != Available {
			t.Fatalf("existing stable key lost local availability: %+v", linked.Categories[0].Products[0])
		}
		if linked.Categories[0].Products[1].Availability != PendingConfirmation {
			t.Fatalf("new product not defaulted to pending: %+v", linked.Categories[0].Products[1])
		}
		if linked.Categories[0].Products[0].Groups[0].Options[1].Availability != PendingConfirmation {
			t.Fatalf("new option not defaulted to pending: %+v", linked.Categories[0].Products[0].Groups[0].Options)
		}
		if _, err = service.SetStoreAvailability(ctx, StoreScope{"t", "s2"}, "manager", AvailabilityChange{Target: AvailabilityTarget{Kind: ProductAvailability, ProductKey: "new-item"}, Status: Available, ExpectedVersion: 1}); err != nil {
			t.Fatalf("make new key available before removal: %v", err)
		}
		removed, err := service.SaveCompanyDraft(ctx, "t", "owner", 2, sampleMenu("Soup v3", 1050, false))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.PublishCompanyDraft(ctx, "t", "owner", removed.Version); err != nil {
			t.Fatalf("publish removal: %v", err)
		}
		restored, err := service.SaveCompanyDraft(ctx, "t", "owner", 3, sampleMenu("Soup v4", 1150, true))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.PublishCompanyDraft(ctx, "t", "owner", restored.Version); err != nil {
			t.Fatalf("publish restored stable key: %v", err)
		}
		var resetStatus Availability
		var resetVersion int64
		if err = pool.QueryRow(ctx, `SELECT status,version FROM store_product_availability WHERE tenant_id='t' AND store_id='s2' AND product_key='new-item'`).Scan(&resetStatus, &resetVersion); err != nil {
			t.Fatal(err)
		}
		if resetStatus != PendingConfirmation || resetVersion != 3 {
			t.Fatalf("restored stable key was not reset with bumped version: status=%s version=%d", resetStatus, resetVersion)
		}
		var beforeStatus, afterStatus string
		var beforeVersion, afterVersion int64
		if err = pool.QueryRow(ctx, `SELECT before_value,after_value,before_version,after_version FROM menu_admin_audit WHERE tenant_id='t' AND store_id='s2' AND operation='NEW_PRODUCT_PENDING' AND entity_key='new-item' ORDER BY audit_id DESC LIMIT 1`).Scan(&beforeStatus, &afterStatus, &beforeVersion, &afterVersion); err != nil {
			t.Fatal(err)
		}
		if beforeStatus != string(Available) || afterStatus != string(PendingConfirmation) || beforeVersion != 2 || afterVersion != 3 {
			t.Fatalf("stable-key reset audit fabricated prior state: %s→%s %d→%d", beforeStatus, afterStatus, beforeVersion, afterVersion)
		}
		detached, err := service.ReadStoreMenu(ctx, StoreScope{"t", "s1"})
		if err != nil || detached.Categories[0].Products[0].Name != "Soup" || detached.Categories[0].Products[0].EffectivePriceMinor != 1200 {
			t.Fatalf("company publish changed detached store: %+v %v", detached, err)
		}
		var foreignCatalogID int64
		if err = pool.QueryRow(ctx, `INSERT INTO menu_catalogs(tenant_id,owner_kind,store_id) VALUES('fk-fixture','STORE','store') RETURNING catalog_id`).Scan(&foreignCatalogID); err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO store_menu_bindings(tenant_id,store_id,mode,catalog_id,detached_by) VALUES('t','s2','DETACHED',$1,'bad')`, foreignCatalogID)
		var foreignKeyErr *pgconn.PgError
		if !errors.As(err, &foreignKeyErr) || foreignKeyErr.Code != "23503" || foreignKeyErr.ConstraintName != "store_menu_bindings_catalog_id_tenant_id_owner_kind_store__fkey" {
			t.Fatalf("database did not reject cross-scope catalog binding via composite FK: %v", err)
		}
		testConcurrentRevisionSeal(t, ctx, pool, foreignCatalogID)
		if _, err = service.SetStorePriceOverride(ctx, StoreScope{"other", "s1"}, "manager", "soup", 1, 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-tenant store mutation was not rejected: %v", err)
		}
		if _, err = service.ReadPublishedByTableLocator(ctx, "other-locator"); !errors.Is(err, ErrNotPublished) {
			t.Fatalf("other tenant locator with no company menu should be unpublished, got %v", err)
		}
	})

	t.Run("concurrent draft saves return their own committed snapshot", func(t *testing.T) {
		start := make(chan struct{})
		type saveResult struct {
			draft Draft
			err   error
		}
		firstDone := make(chan saveResult, 1)
		secondDone := make(chan saveResult, 1)
		firstData := sampleMenu("Writer A", 100, false)
		secondData := sampleMenu("Writer B", 200, false)
		go func() {
			<-start
			draft, saveErr := service.SaveCompanyDraft(ctx, "writer-fixture", "writer-a", 0, firstData)
			firstDone <- saveResult{draft: draft, err: saveErr}
		}()
		go func() {
			<-start
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				var version int64
				queryErr := pool.QueryRow(ctx, `SELECT draft_version FROM menu_catalogs WHERE tenant_id='writer-fixture' AND owner_kind='COMPANY'`).Scan(&version)
				if queryErr == nil && version == 1 {
					draft, saveErr := service.SaveCompanyDraft(ctx, "writer-fixture", "writer-b", 1, secondData)
					secondDone <- saveResult{draft: draft, err: saveErr}
					return
				}
				time.Sleep(time.Millisecond)
			}
			secondDone <- saveResult{err: errors.New("first draft did not commit before timeout")}
		}()
		close(start)
		first := <-firstDone
		second := <-secondDone
		if first.err != nil || first.draft.Version != 1 || first.draft.Categories[0].Products[0].Name != "Writer A" {
			t.Fatalf("first save response did not preserve its own revision: %+v %v", first.draft, first.err)
		}
		if second.err != nil || second.draft.Version != 2 || second.draft.Categories[0].Products[0].Name != "Writer B" {
			t.Fatalf("second save response did not preserve its own revision: %+v %v", second.draft, second.err)
		}
	})

	t.Run("publication rollback and concurrent detach", func(t *testing.T) {
		// This tenant is isolated from the first scenario while sharing the same migrated schema.
		if _, err := pool.Exec(ctx, `UPDATE tenants SET enabled=true WHERE tenant_id='other'`); err != nil {
			t.Fatal(err)
		}
		draft, err := service.SaveCompanyDraft(ctx, "other", "owner", 0, sampleMenu("Other", 500, false))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_menu_publish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected publish failure'; END $$;
			CREATE TRIGGER fail_menu_publish BEFORE INSERT ON menu_revision_products FOR EACH ROW WHEN (NEW.product_key='soup') EXECUTE FUNCTION fail_menu_publish()`); err != nil {
			t.Fatal(err)
		}
		if _, err = service.PublishCompanyDraft(ctx, "other", "owner", draft.Version); err == nil {
			t.Fatal("injected revision insert failure unexpectedly published")
		}
		var current *int64
		if err = pool.QueryRow(ctx, `SELECT current_revision FROM menu_catalogs WHERE tenant_id='other' AND owner_kind='COMPANY'`).Scan(&current); err != nil || current != nil {
			t.Fatalf("failed publish left current pointer: %v %v", current, err)
		}
		if _, err = pool.Exec(ctx, `DROP TRIGGER fail_menu_publish ON menu_revision_products; DROP FUNCTION fail_menu_publish()`); err != nil {
			t.Fatal(err)
		}
		if _, err = service.PublishCompanyDraft(ctx, "other", "owner", draft.Version); err != nil {
			t.Fatalf("healthy publication retry failed: %v", err)
		}
		// Establish the publish-first winner explicitly: after publication moves
		// the company pointer, a detach request based on the prior revision must be stale.
		next := sampleMenu("Other v2", 600, false)
		draft, err = service.SaveCompanyDraft(ctx, "other", "owner", 1, next)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.PublishCompanyDraft(ctx, "other", "owner", draft.Version); err != nil {
			t.Fatalf("publish-first preparation failed: %v", err)
		}
		if _, err = service.DetachStore(ctx, StoreScope{"other", "s1"}, "owner", 1); !errors.Is(err, ErrStale) {
			t.Fatalf("detach based on pre-publish revision returned %v", err)
		}
		// Race the next publication against detach. A detach-first outcome
		// snapshots revision 2; a publish-first outcome rejects the stale detach.
		next = sampleMenu("Other v3", 700, false)
		draft, err = service.SaveCompanyDraft(ctx, "other", "owner", 2, next)
		if err != nil {
			t.Fatal(err)
		}
		deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, e := service.PublishCompanyDraft(deadline, "other", "owner", draft.Version)
			results <- e
		}()
		go func() {
			<-start
			_, e := service.DetachStore(deadline, StoreScope{"other", "s1"}, "owner", 2)
			results <- e
		}()
		close(start)
		for i := 0; i < 2; i++ {
			select {
			case e := <-results:
				if e != nil && !errors.Is(e, ErrStale) && !errors.Is(e, ErrAlreadyDetached) {
					t.Fatalf("concurrent publication/detach failed: %v", e)
				}
			case <-deadline.Done():
				t.Fatal("concurrent publication/detach timed out (possible lock-order deadlock)")
			}
		}
	})
}

func sampleMenu(name string, price int64, addItems bool) CatalogData {
	maxSelect := 1
	if addItems {
		maxSelect = 2
	}
	product := Product{Key: "soup", Name: name, BasePriceMinor: price, Active: true, Groups: []OptionGroup{{Key: "size", Name: "Size", MinSelect: 0, MaxSelect: maxSelect, Options: []Option{{Key: "large", Name: "Large", Active: true}}}}}
	if addItems {
		product.Groups[0].Options = append(product.Groups[0].Options, Option{Key: "small", Name: "Small", Active: true})
	}
	categories := []Category{{Key: "food", Name: "Food", Products: []Product{product}}}
	if addItems {
		categories[0].Products = append(categories[0].Products, Product{Key: "new-item", Name: "New item", BasePriceMinor: 200, Position: 1, Active: true})
	}
	return CatalogData{Categories: categories}
}

func catalogID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, ownerKind, storeID string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `SELECT catalog_id FROM menu_catalogs WHERE tenant_id=$1 AND owner_kind=$2 AND (($2='COMPANY' AND store_id IS NULL) OR store_id=$3)`, tenantID, ownerKind, storeID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertUnsealedRevisionCannotBecomeCurrent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id int64) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO menu_catalog_revisions(catalog_id,revision,draft_version,published_by) VALUES($1,99,99,'test')`, id); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `UPDATE menu_catalogs SET current_revision=99 WHERE catalog_id=$1`, id)
	if err == nil || !strings.Contains(err.Error(), "must be sealed") {
		t.Fatalf("database accepted an unsealed current revision: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("rollback unsealed pointer test: %v", err)
	}
}

func testConcurrentRevisionSeal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO menu_catalog_revisions(catalog_id,revision,draft_version,published_by) VALUES($1,1,1,'test')`, id); err != nil {
		t.Fatal(err)
	}

	// A child insert that locks the parent first may finish before sealing; the
	// seal update must wait for that insert transaction and include its row.
	insertTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = insertTx.Exec(ctx, `INSERT INTO menu_revision_categories(catalog_id,revision,category_key,name,position) VALUES($1,1,'first','First',0)`, id); err != nil {
		_ = insertTx.Rollback(ctx)
		t.Fatalf("insert before seal: %v", err)
	}
	sealTx, err := pool.Begin(ctx)
	if err != nil {
		_ = insertTx.Rollback(ctx)
		t.Fatal(err)
	}
	sealDone := make(chan error, 1)
	sealStarted := make(chan struct{})
	go func() {
		close(sealStarted)
		_, sealErr := sealTx.Exec(ctx, `UPDATE menu_catalog_revisions SET sealed=true WHERE catalog_id=$1 AND revision=1`, id)
		if sealErr == nil {
			sealErr = sealTx.Commit(ctx)
		} else {
			_ = sealTx.Rollback(ctx)
		}
		sealDone <- sealErr
	}()
	<-sealStarted
	select {
	case err = <-sealDone:
		_ = insertTx.Rollback(ctx)
		t.Fatalf("seal did not wait for in-flight child insert: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = insertTx.Commit(ctx); err != nil {
		t.Fatalf("commit pre-seal child: %v", err)
	}
	select {
	case err = <-sealDone:
		if err != nil {
			t.Fatalf("seal after child insert: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seal timed out after pre-seal child committed")
	}

	// Conversely, when the seal owns the parent first, a child insert waits and
	// then observes the sealed state and is rejected after the seal commits.
	if _, err = pool.Exec(ctx, `INSERT INTO menu_catalog_revisions(catalog_id,revision,draft_version,published_by) VALUES($1,2,2,'test')`, id); err != nil {
		t.Fatal(err)
	}
	sealTx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sealTx.Exec(ctx, `UPDATE menu_catalog_revisions SET sealed=true WHERE catalog_id=$1 AND revision=2`, id); err != nil {
		_ = sealTx.Rollback(ctx)
		t.Fatal(err)
	}
	insertDone := make(chan error, 1)
	insertStarted := make(chan struct{})
	go func() {
		close(insertStarted)
		_, insertErr := pool.Exec(ctx, `INSERT INTO menu_revision_categories(catalog_id,revision,category_key,name,position) VALUES($1,2,'late','Late',0)`, id)
		insertDone <- insertErr
	}()
	<-insertStarted
	select {
	case err = <-insertDone:
		_ = sealTx.Rollback(ctx)
		t.Fatalf("child insert bypassed in-flight seal: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = sealTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-insertDone:
		if err == nil || !strings.Contains(err.Error(), "sealed") {
			t.Fatalf("child insert after seal returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child insert timed out after seal committed")
	}
}
