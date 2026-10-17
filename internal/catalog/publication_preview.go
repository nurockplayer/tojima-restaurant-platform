package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
)

type linkedStoreFingerprint struct {
	StoreID   string `json:"store_id"`
	Enabled   bool   `json:"enabled"`
	Operating bool   `json:"operating"`
}

type productAvailabilityFingerprint struct {
	StoreID string       `json:"store_id"`
	Product string       `json:"product_key"`
	Status  Availability `json:"status"`
	Version int64        `json:"version"`
}

type optionAvailabilityFingerprint struct {
	StoreID string       `json:"store_id"`
	Product string       `json:"product_key"`
	Group   string       `json:"group_key"`
	Option  string       `json:"option_key"`
	Status  Availability `json:"status"`
	Version int64        `json:"version"`
}

type publicationFingerprintInput struct {
	TenantID          string                           `json:"tenant_id"`
	CatalogID         int64                            `json:"catalog_id"`
	PublishedRevision int64                            `json:"published_revision"`
	DraftVersion      int64                            `json:"draft_version"`
	DraftDigest       string                           `json:"draft_digest"`
	LinkedStores      []linkedStoreFingerprint         `json:"linked_stores"`
	ProductStates     []productAvailabilityFingerprint `json:"product_availability"`
	OptionStates      []optionAvailabilityFingerprint  `json:"option_availability"`
}

type publicationOptionKey struct {
	product string
	group   string
	option  string
}

// ReadCompanyPublishedSnapshot reads the current immutable company revision
// and its contents from one repeatable-read database snapshot. Draft data is
// intentionally exposed only by GetCompanyDraft.
func (s *Service) ReadCompanyPublishedSnapshot(ctx context.Context, tenantID string) (CompanyPublishedSnapshot, error) {
	if !cleanKey(tenantID) {
		return CompanyPublishedSnapshot{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return CompanyPublishedSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	var catalogID int64
	var revision *int64
	err = tx.QueryRow(ctx, `SELECT catalog_id,current_revision FROM menu_catalogs WHERE tenant_id=$1 AND owner_kind='COMPANY'`, tenantID).Scan(&catalogID, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return CompanyPublishedSnapshot{}, ErrNotFound
	}
	if err != nil {
		return CompanyPublishedSnapshot{}, err
	}
	if revision == nil {
		return CompanyPublishedSnapshot{}, ErrNotPublished
	}
	data, err := readRevision(ctx, tx, catalogID, *revision)
	if err != nil {
		return CompanyPublishedSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CompanyPublishedSnapshot{}, err
	}
	return CompanyPublishedSnapshot{Revision: *revision, CatalogData: data}, nil
}

// PreviewCompanyPublication validates the exact saved company draft against
// one locked view of the current publication and linked-store availability.
// It is advisory and never changes catalog, availability, or audit rows.
func (s *Service) PreviewCompanyPublication(ctx context.Context, tenantID string, expectedDraftVersion int64) (CompanyPublicationPreview, error) {
	if !cleanKey(tenantID) || expectedDraftVersion < 1 {
		return CompanyPublicationPreview{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return CompanyPublicationPreview{}, err
	}
	defer tx.Rollback(ctx)
	var catalogID int64
	var draftVersion int64
	var draftBaseRevision, currentRevision *int64
	err = tx.QueryRow(ctx, `SELECT catalog_id,draft_version,draft_base_revision,current_revision FROM menu_catalogs
		WHERE tenant_id=$1 AND owner_kind='COMPANY' FOR SHARE`, tenantID).Scan(&catalogID, &draftVersion, &draftBaseRevision, &currentRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return CompanyPublicationPreview{}, ErrNotFound
	}
	if err != nil {
		return CompanyPublicationPreview{}, err
	}
	if draftVersion != expectedDraftVersion || !sameRevision(draftBaseRevision, currentRevision) {
		return CompanyPublicationPreview{}, ErrStale
	}
	if currentRevision != nil {
		var publishedDraftVersion int64
		if err = tx.QueryRow(ctx, `SELECT draft_version FROM menu_catalog_revisions WHERE catalog_id=$1 AND revision=$2`, catalogID, *currentRevision).Scan(&publishedDraftVersion); err != nil {
			return CompanyPublicationPreview{}, err
		}
		if expectedDraftVersion <= publishedDraftVersion {
			return CompanyPublicationPreview{}, ErrStale
		}
	}
	draft, err := readDraft(ctx, tx, catalogID)
	if err != nil {
		return CompanyPublicationPreview{}, err
	}
	if err = validateData(draft.CatalogData); err != nil {
		return CompanyPublicationPreview{}, err
	}
	var previous CatalogData
	publishedRevision := int64(0)
	if currentRevision != nil {
		publishedRevision = *currentRevision
		previous, err = readRevision(ctx, tx, catalogID, publishedRevision)
		if err != nil {
			return CompanyPublicationPreview{}, err
		}
	}
	stores, err := lockLinkedStoreRows(ctx, tx, tenantID, "SHARE")
	if err != nil {
		return CompanyPublicationPreview{}, err
	}
	fingerprint, err := companyPublicationFingerprint(ctx, tx, tenantID, catalogID, publishedRevision, draft.Version, previous, draft.CatalogData, stores)
	if err != nil {
		return CompanyPublicationPreview{}, err
	}
	issues := []PublicationIssue{}
	if err = validateCompanyPublicationDiff(ctx, tx, tenantID, previous, draft.CatalogData); err != nil {
		var blocked *PublicationValidationError
		if !errors.As(err, &blocked) {
			return CompanyPublicationPreview{}, err
		}
		issues = append(issues, blocked.Issues...)
	}
	if err = tx.Commit(ctx); err != nil {
		return CompanyPublicationPreview{}, err
	}
	return CompanyPublicationPreview{
		DraftVersion:       draft.Version,
		PublishedRevision:  publishedRevision,
		LinkedStoreCount:   len(stores),
		CanPublish:         len(issues) == 0,
		Issues:             issues,
		PreviewFingerprint: fingerprint,
	}, nil
}

// PublishCompanyDraftChecked publishes only if the reviewed preview inputs
// still match. PublishCompanyDraft remains available to compatibility callers
// and independently validates the latest state without a preview precondition.
func (s *Service) PublishCompanyDraftChecked(ctx context.Context, tenantID, actor string, expectedDraftVersion int64, previewFingerprint string) (PublishResult, error) {
	if !cleanKey(tenantID) || !cleanKey(actor) || expectedDraftVersion < 1 || !validPreviewFingerprint(previewFingerprint) {
		return PublishResult{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	defer tx.Rollback(ctx)
	catalogID, err := lookupCatalog(ctx, tx, tenantID, "COMPANY", "")
	if err != nil {
		return PublishResult{}, err
	}
	result, err := publishCatalogWithFingerprint(ctx, tx, catalogID, actor, expectedDraftVersion, previewFingerprint)
	if err != nil {
		return PublishResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

// ReadStoreAdminProjection exposes the server-resolved base/override/effective
// price values alongside one coherent current menu. It contains no permission
// flags; the HTTP boundary derives those from the verified staff actor.
func (s *Service) ReadStoreAdminProjection(ctx context.Context, scope StoreScope) (StoreAdminProjection, error) {
	menu, err := s.ReadStoreMenu(ctx, scope)
	if err != nil {
		return StoreAdminProjection{}, err
	}
	prices := make([]StoreProductPrice, 0)
	for _, category := range menu.Categories {
		for _, product := range category.Products {
			prices = append(prices, StoreProductPrice{
				ProductKey: product.Key, SourceBasePriceMinor: product.BasePriceMinor,
				OverrideActive: product.PriceOverrideActive, OverridePriceMinor: product.PriceOverrideMinor,
				EffectivePriceMinor: product.EffectivePriceMinor, OverrideVersion: product.PriceOverrideVersion,
			})
		}
	}
	return StoreAdminProjection{Menu: menu, ProductPrices: prices}, nil
}

func companyPublicationFingerprint(ctx context.Context, tx pgx.Tx, tenantID string, catalogID, publishedRevision, draftVersion int64, previous, next CatalogData, stores []linkedStoreFingerprint) (string, error) {
	draftBytes, err := json.Marshal(next)
	if err != nil {
		return "", err
	}
	draftDigest := sha256.Sum256(draftBytes)
	input := publicationFingerprintInput{
		TenantID: tenantID, CatalogID: catalogID, PublishedRevision: publishedRevision, DraftVersion: draftVersion,
		DraftDigest: hex.EncodeToString(draftDigest[:]), LinkedStores: stores,
		ProductStates: []productAvailabilityFingerprint{}, OptionStates: []optionAvailabilityFingerprint{},
	}
	storeIDs := make([]string, 0, len(stores))
	for _, store := range stores {
		storeIDs = append(storeIDs, store.StoreID)
	}
	productKeys, optionKeys := requiredOptionRemovalKeys(previous, next)
	if len(storeIDs) > 0 && len(productKeys) > 0 {
		rows, queryErr := tx.Query(ctx, `SELECT store_id,product_key,status,version FROM store_product_availability
			WHERE tenant_id=$1 AND store_id=ANY($2::text[]) AND product_key=ANY($3::text[])
			ORDER BY store_id,product_key`, tenantID, storeIDs, productKeys)
		if queryErr != nil {
			return "", queryErr
		}
		for rows.Next() {
			var state productAvailabilityFingerprint
			if err = rows.Scan(&state.StoreID, &state.Product, &state.Status, &state.Version); err != nil {
				rows.Close()
				return "", err
			}
			input.ProductStates = append(input.ProductStates, state)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return "", err
		}
		rows.Close()
	}
	if len(storeIDs) > 0 && len(optionKeys) > 0 {
		products := make([]string, 0, len(optionKeys))
		groups := make([]string, 0, len(optionKeys))
		options := make([]string, 0, len(optionKeys))
		for _, key := range optionKeys {
			products = append(products, key.product)
			groups = append(groups, key.group)
			options = append(options, key.option)
		}
		rows, queryErr := tx.Query(ctx, `SELECT a.store_id,a.product_key,a.group_key,a.option_key,a.status,a.version
			FROM store_option_availability a
			JOIN unnest($2::text[],$3::text[],$4::text[]) AS k(product_key,group_key,option_key)
			ON a.product_key=k.product_key AND a.group_key=k.group_key AND a.option_key=k.option_key
			WHERE a.tenant_id=$1 AND a.store_id=ANY($5::text[])
			ORDER BY a.store_id,a.product_key,a.group_key,a.option_key`, tenantID, products, groups, options, storeIDs)
		if queryErr != nil {
			return "", queryErr
		}
		for rows.Next() {
			var state optionAvailabilityFingerprint
			if err = rows.Scan(&state.StoreID, &state.Product, &state.Group, &state.Option, &state.Status, &state.Version); err != nil {
				rows.Close()
				return "", err
			}
			input.OptionStates = append(input.OptionStates, state)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return "", err
		}
		rows.Close()
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func requiredOptionRemovalKeys(previous, next CatalogData) ([]string, []publicationOptionKey) {
	oldProducts := flattenProducts(previous)
	newProducts := flattenProducts(next)
	productSet := map[string]bool{}
	optionSet := map[publicationOptionKey]bool{}
	for productKey, oldProduct := range oldProducts {
		newProduct, exists := newProducts[productKey]
		if !exists || !oldProduct.Active || !newProduct.Active {
			continue
		}
		oldGroups := make(map[string]OptionGroup, len(oldProduct.Groups))
		for _, group := range oldProduct.Groups {
			oldGroups[group.Key] = group
		}
		newGroups := make(map[string]OptionGroup, len(newProduct.Groups))
		for _, group := range newProduct.Groups {
			newGroups[group.Key] = group
		}
		for groupKey, oldGroup := range oldGroups {
			newGroup, groupExists := newGroups[groupKey]
			if oldGroup.MinSelect < 1 || !groupExists || newGroup.MinSelect < 1 || newGroup.MinSelect > oldGroup.MinSelect || !hasRemovedActiveOption(oldGroup, newGroup) {
				continue
			}
			productSet[productKey] = true
			for _, option := range oldGroup.Options {
				if option.Active {
					optionSet[publicationOptionKey{product: productKey, group: groupKey, option: option.Key}] = true
				}
			}
		}
	}
	products := make([]string, 0, len(productSet))
	for key := range productSet {
		products = append(products, key)
	}
	sort.Strings(products)
	options := make([]publicationOptionKey, 0, len(optionSet))
	for key := range optionSet {
		options = append(options, key)
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].product != options[j].product {
			return options[i].product < options[j].product
		}
		if options[i].group != options[j].group {
			return options[i].group < options[j].group
		}
		return options[i].option < options[j].option
	})
	return products, options
}

func validPreviewFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
