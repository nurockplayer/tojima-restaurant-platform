package catalog

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

const (
	PublicationNewRequiredGroup       = "NEW_REQUIRED_GROUP"
	PublicationOptionalBecameRequired = "OPTIONAL_GROUP_BECAME_REQUIRED"
	PublicationRequiredMinIncreased   = "REQUIRED_GROUP_MIN_INCREASED"
	PublicationRequiredChoiceShortage = "REQUIRED_OPTION_MIN_UNAVAILABLE"
)

type linkedAvailabilityStore struct {
	id   string
	name string
}

// validateCompanyPublicationDiff checks required-option compatibility against
// the prior immutable revision and the current LINKED store availability. The
// caller holds each LINKED store row FOR UPDATE before invoking it, serializing
// this snapshot with store availability mutations. New options never satisfy
// an existing group's minimum: only previously published option keys can carry
// forward an AVAILABLE state.
func validateCompanyPublicationDiff(ctx context.Context, tx pgx.Tx, tenantID string, previous, next CatalogData) error {
	oldProducts := flattenProducts(previous)
	newProducts := flattenProducts(next)
	if len(oldProducts) == 0 {
		return nil
	}
	issues := make([]PublicationIssue, 0)
	var stores []linkedAvailabilityStore
	storesLoaded := false
	loadStores := func() error {
		if storesLoaded {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT s.store_id,s.name FROM stores s
			WHERE s.tenant_id=$1 AND s.enabled AND s.operating
			AND NOT EXISTS(SELECT 1 FROM store_menu_bindings b WHERE b.tenant_id=s.tenant_id AND b.store_id=s.store_id)
			ORDER BY s.store_id`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var store linkedAvailabilityStore
			if err = rows.Scan(&store.id, &store.name); err != nil {
				return err
			}
			stores = append(stores, store)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		storesLoaded = true
		return nil
	}

	for productKey, oldProduct := range oldProducts {
		newProduct, exists := newProducts[productKey]
		if !exists {
			continue
		}
		oldGroups := make(map[string]OptionGroup, len(oldProduct.Groups))
		for _, group := range oldProduct.Groups {
			oldGroups[group.Key] = group
		}
		for _, newGroup := range newProduct.Groups {
			if newGroup.MinSelect <= 0 {
				continue
			}
			oldGroup, existed := oldGroups[newGroup.Key]
			if !existed {
				// Every required group absent by stable key is new for an existing
				// product. This also rejects renaming a required/optional group key
				// as a way to evade the stable-key comparison.
				issues = append(issues, PublicationIssue{Code: PublicationNewRequiredGroup, ProductKey: productKey, GroupKey: newGroup.Key, RequiredCount: newGroup.MinSelect})
				continue
			}
			if oldGroup.MinSelect == 0 {
				issues = append(issues, PublicationIssue{Code: PublicationOptionalBecameRequired, ProductKey: productKey, GroupKey: newGroup.Key, RequiredCount: newGroup.MinSelect})
				continue
			}
			if newGroup.MinSelect > oldGroup.MinSelect {
				issues = append(issues, PublicationIssue{Code: PublicationRequiredMinIncreased, ProductKey: productKey, GroupKey: newGroup.Key, RequiredCount: newGroup.MinSelect})
				continue
			}
			if !oldProduct.Active || !newProduct.Active || !hasRemovedActiveOption(oldGroup, newGroup) {
				continue
			}
			if err := loadStores(); err != nil {
				return err
			}
			oldKeys := activeOptionKeys(oldGroup)
			if len(oldKeys) == 0 {
				continue
			}
			nextActive := activeOptionsByKey(newGroup)
			for _, store := range stores {
				var productAvailable bool
				err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM store_product_availability
					WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3 AND status='AVAILABLE')`, tenantID, store.id, productKey).Scan(&productAvailable)
				if err != nil {
					return err
				}
				if !productAvailable {
					continue
				}
				rows, err := tx.Query(ctx, `SELECT option_key FROM store_option_availability
					WHERE tenant_id=$1 AND store_id=$2 AND product_key=$3 AND group_key=$4 AND status='AVAILABLE' AND option_key=ANY($5::text[])`,
					tenantID, store.id, productKey, oldGroup.Key, oldKeys)
				if err != nil {
					return err
				}
				available := make(map[string]bool, len(oldKeys))
				for rows.Next() {
					var optionKey string
					if err = rows.Scan(&optionKey); err != nil {
						rows.Close()
						return err
					}
					available[optionKey] = true
				}
				if err = rows.Err(); err != nil {
					rows.Close()
					return err
				}
				rows.Close()
				oldAvailableCount, nextAvailableCount := 0, 0
				for _, key := range oldKeys {
					if available[key] {
						oldAvailableCount++
						if nextActive[key] {
							nextAvailableCount++
						}
					}
				}
				// Only block a removal that would newly break an item which was
				// sellable immediately before this publication.
				if oldAvailableCount >= oldGroup.MinSelect && nextAvailableCount < newGroup.MinSelect {
					issues = append(issues, PublicationIssue{
						Code: PublicationRequiredChoiceShortage, ProductKey: productKey, GroupKey: newGroup.Key,
						StoreID: store.id, StoreName: store.name, RequiredCount: newGroup.MinSelect, AvailableCount: nextAvailableCount,
					})
				}
			}
		}
	}
	if len(issues) == 0 {
		return nil
	}
	sort.Slice(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.ProductKey != b.ProductKey {
			return a.ProductKey < b.ProductKey
		}
		if a.GroupKey != b.GroupKey {
			return a.GroupKey < b.GroupKey
		}
		return a.StoreID < b.StoreID
	})
	return &PublicationValidationError{Issues: issues}
}

func activeOptionKeys(group OptionGroup) []string {
	keys := make([]string, 0, len(group.Options))
	for _, option := range group.Options {
		if option.Active {
			keys = append(keys, option.Key)
		}
	}
	return keys
}

func activeOptionsByKey(group OptionGroup) map[string]bool {
	options := make(map[string]bool, len(group.Options))
	for _, option := range group.Options {
		if option.Active {
			options[option.Key] = true
		}
	}
	return options
}

func hasRemovedActiveOption(oldGroup, newGroup OptionGroup) bool {
	newActive := activeOptionsByKey(newGroup)
	for _, option := range oldGroup.Options {
		if option.Active && !newActive[option.Key] {
			return true
		}
	}
	return false
}
