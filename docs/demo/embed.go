// Package demo provides a strict, test-only decoder for the source-labelled
// Shironagasukujira evidence dataset. It does not import a live restaurant menu.
package demo

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

//go:embed shironagasukujira-menu-evidence.json
var shironagasukujiraJSON []byte

var ErrInvalidEvidence = errors.New("demo menu evidence is invalid")

type Evidence struct {
	Metadata         Metadata          `json:"metadata"`
	Sources          map[string]Source `json:"sources"`
	Categories       []Category        `json:"categories"`
	Items            []Item            `json:"items"`
	DemoOptionGroups []OptionGroup     `json:"demo_option_groups"`
	Guardrails       Guardrails        `json:"implementation_guardrails"`
}

type Metadata struct {
	Schema                  string   `json:"schema"`
	CreatedOn               string   `json:"created_on"`
	RestaurantID            string   `json:"restaurant_id"`
	NameJA                  string   `json:"name_ja"`
	AddressJA               string   `json:"address_ja"`
	OwnerReportedByUser     bool     `json:"restaurant_owner_reported_by_user"`
	Currency                string   `json:"currency"`
	Service                 string   `json:"service"`
	BusinessContact         string   `json:"business_contact"`
	NotOfficialMenu         string   `json:"not_official_menu"`
	ProductionImportAllowed bool     `json:"production_import_allowed"`
	LivePaymentEnabled      bool     `json:"live_payment_enabled"`
	SeedTenant              string   `json:"seed_tenant"`
	SeedStore               string   `json:"seed_store"`
	Notes                   []string `json:"notes"`
}

type Source struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
	Date string `json:"date"`
}

type Category struct {
	Key     string `json:"key"`
	LabelJA string `json:"label_ja"`
}

type Item struct {
	DemoKey                string `json:"demo_key"`
	NameJA                 string `json:"name_ja"`
	CategoryKey            string `json:"category_key"`
	ReferenceChannel       string `json:"reference_channel"`
	ReferencePriceJPY      *int64 `json:"reference_price_jpy"`
	SourceID               string `json:"source_id"`
	OwnerVerified          bool   `json:"owner_verified"`
	OrderableInDemo        bool   `json:"orderable_in_demo"`
	EligibleForRealPayment bool   `json:"eligible_for_real_payment"`
	Notes                  string `json:"notes"`
}

type OptionGroup struct {
	DemoKey       string   `json:"demo_key"`
	NameJA        string   `json:"name_ja"`
	Required      bool     `json:"required"`
	MaxSelect     int      `json:"max_select"`
	ProductKeys   []string `json:"product_keys"`
	Options       []Option `json:"options"`
	OwnerVerified bool     `json:"owner_verified"`
	SourceID      string   `json:"source_id"`
	Notes         string   `json:"notes"`
}

type Option struct {
	Key           string `json:"key"`
	NameJA        string `json:"name_ja"`
	PriceDeltaJPY int64  `json:"price_delta_jpy"`
}

type Guardrails struct {
	UseTestTenantOnly                   bool `json:"use_test_tenant_only"`
	NoRealStripeOrCash                  bool `json:"no_real_stripe_or_cash"`
	NeverClaimLiveMenu                  bool `json:"never_claim_live_menu"`
	DoNotImportDeliveryPricesIntoDineIn bool `json:"do_not_import_delivery_prices_into_dine_in"`
	AllItemsPendingOwnerConfirmation    bool `json:"all_items_pending_owner_confirmation"`
	StableDemoKeysNotProductionIDs      bool `json:"stable_demo_keys_not_production_product_ids"`
	ImagesRequireOwnerPermission        bool `json:"images_require_owner_permission"`
}

// LoadShironagasukujira decodes the checked-in evidence document and rejects
// changes that could enable unreviewed or delivery-only data for the fixture.
func LoadShironagasukujira() (Evidence, error) {
	var evidence Evidence
	decoder := json.NewDecoder(strings.NewReader(string(shironagasukujiraJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return Evidence{}, fmt.Errorf("%w: decode: %v", ErrInvalidEvidence, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Evidence{}, fmt.Errorf("%w: %v", ErrInvalidEvidence, err)
	}
	if err := ValidateShironagasukujira(evidence); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

// ValidateShironagasukujira checks the fixed DEMO-only contract before any
// data can be mapped into a catalog. Its exact count and source checks make
// changes to the source document fail closed pending explicit code review.
func ValidateShironagasukujira(e Evidence) error {
	invalid := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidEvidence, reason) }
	m := e.Metadata
	if m.Schema != "tojima-demo-menu-evidence-v1" || m.RestaurantID != "shironagasukujira-iidabashi-demo" || m.CreatedOn == "" || m.NameJA == "" || m.AddressJA == "" ||
		!m.OwnerReportedByUser || m.Currency != "JPY" || m.Service != "DEMO_ONLY" || m.BusinessContact == "" || m.NotOfficialMenu == "" ||
		m.ProductionImportAllowed || m.LivePaymentEnabled || m.SeedTenant != "DEMO_SYNTHETIC" || m.SeedStore != "DEMO_SHIRONAGASUKUJIRA" || len(m.Notes) == 0 {
		return invalid("metadata does not describe the approved synthetic demo fixture")
	}
	g := e.Guardrails
	if !g.UseTestTenantOnly || !g.NoRealStripeOrCash || !g.NeverClaimLiveMenu || !g.DoNotImportDeliveryPricesIntoDineIn ||
		!g.AllItemsPendingOwnerConfirmation || !g.StableDemoKeysNotProductionIDs || !g.ImagesRequireOwnerPermission {
		return invalid("one or more test-only guardrails are disabled")
	}
	if len(e.Sources) != 6 || len(e.Categories) != 9 || len(e.Items) != 32 || len(e.DemoOptionGroups) != 1 {
		return invalid("dataset shape changed")
	}
	expectedSources := map[string]bool{
		"official_line": true, "tabelog_menu_photo": true, "tabelog_2026_review": true,
		"tabelog_2025_review": true, "dinner_2025_blog": true, "ubereats": true,
	}
	sourceIDs := make(map[string]bool, len(e.Sources))
	for id, source := range e.Sources {
		if !expectedSources[id] || strings.TrimSpace(id) != id || !strings.HasPrefix(source.URL, "https://") || source.Kind == "" || source.Date == "" {
			return invalid("source reference is incomplete")
		}
		sourceIDs[id] = true
	}
	expectedCategories := map[string]string{
		"lunch": "ランチ定食", "gyoza": "博多餃子", "small_plate": "酒肴・小皿", "hotpot": "鍋", "grill": "鉄板料理",
		"drinks": "ドリンク", "delivery_bento": "Uber Eats 弁当参考", "delivery_rice": "Uber Eats ご飯参考", "delivery_sides": "Uber Eats サイド参考",
	}
	categories := make(map[string]bool, len(e.Categories))
	for _, category := range e.Categories {
		if expectedCategories[category.Key] != category.LabelJA || categories[category.Key] {
			return invalid("category is missing or duplicated")
		}
		categories[category.Key] = true
	}
	keys := make(map[string]bool, len(e.Items))
	counts := map[string]int{}
	historicalPrices := map[string]struct {
		category string
		source   string
		price    int64
	}{
		"LUNCH-SHIRO":          {"lunch", "tabelog_2026_review", 980},
		"LUNCH-KARAAGE":        {"lunch", "tabelog_menu_photo", 960},
		"LUNCH-GYOZA":          {"lunch", "tabelog_menu_photo", 1200},
		"LUNCH-CHICKENNANBAN":  {"lunch", "tabelog_menu_photo", 980},
		"LUNCH-BEEF-CURRY":     {"lunch", "tabelog_menu_photo", 1000},
		"LUNCH-SABA":           {"lunch", "tabelog_menu_photo", 1080},
		"DINNER-IRON-GYOZA":    {"gyoza", "dinner_2025_blog", 1150},
		"DINNER-TAKANA-POTATO": {"small_plate", "dinner_2025_blog", 560},
		"DINNER-TSUKEMONO":     {"small_plate", "dinner_2025_blog", 720},
	}
	for _, item := range e.Items {
		if item.DemoKey == "" || item.NameJA == "" || item.Notes == "" || keys[item.DemoKey] || !categories[item.CategoryKey] || !sourceIDs[item.SourceID] {
			return invalid("item identity or provenance is missing or duplicated")
		}
		keys[item.DemoKey] = true
		if item.OwnerVerified || item.EligibleForRealPayment {
			return invalid("items must remain unverified and ineligible for real payment")
		}
		switch item.ReferenceChannel {
		case "DINE_IN_HISTORICAL":
			counts[item.ReferenceChannel]++
			if !item.OrderableInDemo || item.ReferencePriceJPY == nil || *item.ReferencePriceJPY <= 0 {
				return invalid("historical dine-in items must carry a positive reference price and explicit demo-only orderability")
			}
			known, found := historicalPrices[item.DemoKey]
			if !found || item.CategoryKey != known.category || item.SourceID != known.source || *item.ReferencePriceJPY != known.price {
				return invalid("historical dine-in price or provenance changed")
			}
		case "DINE_IN_UNPRICED":
			counts[item.ReferenceChannel]++
			if item.OrderableInDemo || item.ReferencePriceJPY != nil || (item.SourceID != "official_line" && item.SourceID != "tabelog_menu_photo") {
				return invalid("unpriced dine-in items must remain non-orderable and unpriced")
			}
		case "DELIVERY_ONLY":
			counts[item.ReferenceChannel]++
			if item.OrderableInDemo || item.ReferencePriceJPY == nil || *item.ReferencePriceJPY <= 0 || item.SourceID != "ubereats" {
				return invalid("delivery-only reference data must remain excluded from dine-in ordering")
			}
		default:
			return invalid("unknown reference channel")
		}
	}
	if counts["DINE_IN_HISTORICAL"] != 9 || counts["DINE_IN_UNPRICED"] != 8 || counts["DELIVERY_ONLY"] != 15 {
		return invalid("reference channel counts changed")
	}
	group := e.DemoOptionGroups[0]
	if group.DemoKey != "RICE-SIZE" || group.NameJA == "" || !group.Required || group.MaxSelect != 1 || group.OwnerVerified || group.SourceID != "tabelog_2026_review" || len(group.ProductKeys) != 1 || group.ProductKeys[0] != "LUNCH-SHIRO" || len(group.Options) != 2 {
		return invalid("rice-size option provenance or selection contract changed")
	}
	if !keys[group.ProductKeys[0]] {
		return invalid("rice option references an unknown product")
	}
	if group.Options[0].Key != "regular" || group.Options[0].PriceDeltaJPY != 0 || group.Options[1].Key != "small" || group.Options[1].PriceDeltaJPY != -20 || group.Notes == "" {
		return invalid("rice-size historical adjustment changed")
	}
	return nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
