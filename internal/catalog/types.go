// Package catalog owns published menu snapshots, store-scoped price overrides,
// option rules, and the shared product availability decision.
package catalog

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxCatalogCanonicalJSONBytes bounds the complete accepted catalog document,
// including all nested items and reserving each nonnegative base price's space
// up to its 19-digit int64 maximum for later store-price overrides.
const MaxCatalogCanonicalJSONBytes = 1 << 20

type Availability string

const (
	PendingConfirmation Availability = "PENDING_CONFIRMATION"
	Available           Availability = "AVAILABLE"
	Paused              Availability = "PAUSED"
)

var (
	ErrNotFound           = errors.New("catalog resource not found")
	ErrDenied             = errors.New("catalog access denied")
	ErrConflict           = errors.New("catalog state conflict")
	ErrStale              = errors.New("catalog draft or state is stale")
	ErrInvalid            = errors.New("catalog data is invalid")
	ErrNotPublished       = errors.New("no published menu")
	ErrNotDetached        = errors.New("store menu is linked")
	ErrAlreadyDetached    = errors.New("store menu is already detached")
	ErrPriceOverflow      = errors.New("integer price overflow")
	ErrCompanyProductHeld = errors.New("company product emergency hold is active")
)

type StoreScope struct{ TenantID, StoreID string }

type CatalogData struct {
	Categories []Category `json:"categories"`
}

type Category struct {
	Key      string    `json:"key"`
	Name     string    `json:"name"`
	Position int       `json:"position"`
	Products []Product `json:"products"`
}

type Product struct {
	Key                         string        `json:"key"`
	Name                        string        `json:"name"`
	Description                 string        `json:"description,omitempty"`
	BasePriceMinor              int64         `json:"base_price_minor"`
	EffectivePriceMinor         int64         `json:"-"`
	PriceOverrideVersion        int64         `json:"-"`
	PriceOverrideActive         bool          `json:"-"`
	PriceOverrideMinor          *int64        `json:"-"`
	CompanyEmergencyHoldActive  bool          `json:"-"`
	CompanyEmergencyHoldVersion int64         `json:"-"`
	Position                    int           `json:"position"`
	Active                      bool          `json:"active"`
	Availability                Availability  `json:"-"`
	AvailabilityVersion         int64         `json:"-"`
	Selectable                  bool          `json:"-"`
	Groups                      []OptionGroup `json:"option_groups,omitempty"`
}

type OptionGroup struct {
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	MinSelect int      `json:"min_select"`
	MaxSelect int      `json:"max_select"`
	Position  int      `json:"position"`
	Options   []Option `json:"options"`
}

type Option struct {
	Key                 string       `json:"key"`
	Name                string       `json:"name"`
	PriceDeltaMinor     int64        `json:"price_delta_minor"`
	Position            int          `json:"position"`
	Active              bool         `json:"active"`
	Availability        Availability `json:"-"`
	AvailabilityVersion int64        `json:"-"`
}

type Draft struct {
	Version int64 `json:"version"`
	CatalogData
}

type PublishedMenu struct {
	TenantID         string  `json:"-"`
	StoreID          string  `json:"-"`
	GuestDisplayName *string `json:"-"`
	// CatalogID identifies the exact private source catalog for persisted order
	// provenance. It is deliberately excluded from public menu responses.
	CatalogID int64  `json:"-"`
	Mode      string `json:"mode"`
	Revision  int64  `json:"revision"`
	CatalogData
}

type PublishResult struct {
	Revision     int64 `json:"revision"`
	DraftVersion int64 `json:"draft_version"`
}

// CompanyPublishedSnapshot is one immutable company revision. It never
// contains draft data and is read from a single database snapshot.
type CompanyPublishedSnapshot struct {
	Revision int64 `json:"published_revision"`
	CatalogData
}

// CompanyPublicationPreview is an advisory result bound to the exact draft,
// published revision, linked-store identities and relevant availability state
// that were reviewed. Publishing with the fingerprint rechecks all inputs.
type CompanyPublicationPreview struct {
	DraftVersion       int64              `json:"draft_version"`
	PublishedRevision  int64              `json:"published_revision"`
	LinkedStoreCount   int                `json:"linked_store_count"`
	CanPublish         bool               `json:"can_publish"`
	Issues             []PublicationIssue `json:"issues"`
	PreviewFingerprint string             `json:"preview_fingerprint"`
}

// StoreAdminProjection contains current store-specific price resolution. The
// catalog menu remains the same coherent published menu used for reads; the
// projection separates company base, absolute override, and effective value.
type StoreAdminProjection struct {
	Menu          PublishedMenu       `json:"menu"`
	ProductPrices []StoreProductPrice `json:"product_prices"`
}

type StoreProductPrice struct {
	ProductKey           string `json:"product_key"`
	SourceBasePriceMinor int64  `json:"source_base_price_minor"`
	OverrideActive       bool   `json:"override_active"`
	OverridePriceMinor   *int64 `json:"override_price_minor"`
	EffectivePriceMinor  int64  `json:"effective_price_minor"`
	OverrideVersion      int64  `json:"override_version"`
}

// CompanyProductEmergencyHold is the current company-level emergency gate for
// one stable product key. It is separate from every store's manual availability
// state and survives product retirement and later publications.
type CompanyProductEmergencyHold struct {
	ProductKey        string     `json:"product_key"`
	Active            bool       `json:"active"`
	Version           int64      `json:"version"`
	Reason            string     `json:"reason"`
	ActivatedBy       string     `json:"activated_by,omitempty"`
	ActivatedAt       *time.Time `json:"activated_at,omitempty"`
	ClearedBy         string     `json:"cleared_by,omitempty"`
	ClearedAt         *time.Time `json:"cleared_at,omitempty"`
	PublishedRevision int64      `json:"published_revision"`
	LinkedStoreCount  int        `json:"linked_store_count"`
}

// PublicationIssue is a safe, machine-readable reason why a company draft
// would invalidate the required-option choices of an existing linked menu.
type PublicationIssue struct {
	Code           string `json:"code"`
	ProductKey     string `json:"product_key"`
	GroupKey       string `json:"group_key,omitempty"`
	StoreID        string `json:"store_id,omitempty"`
	StoreName      string `json:"store_name,omitempty"`
	RequiredCount  int    `json:"required_count,omitempty"`
	AvailableCount int    `json:"available_count,omitempty"`
}

// PublicationValidationError is returned when a company publication would
// introduce a new required choice or make a currently sellable linked item
// impossible to order. It unwraps to ErrInvalid for existing error mapping.
type PublicationValidationError struct {
	Issues []PublicationIssue `json:"issues"`
}

func (e *PublicationValidationError) Error() string {
	return "company publication failed required-option safety validation"
}

func (e *PublicationValidationError) Unwrap() error { return ErrInvalid }

type PriceOverride struct {
	ProductKey string `json:"product_key"`
	PriceMinor int64  `json:"price_minor"`
	Active     bool   `json:"active"`
	Version    int64  `json:"version"`
}

type AvailabilityKind string

const (
	ProductAvailability AvailabilityKind = "PRODUCT"
	OptionAvailability  AvailabilityKind = "OPTION"
)

type AvailabilityTarget struct {
	Kind       AvailabilityKind `json:"kind"`
	ProductKey string           `json:"product_key,omitempty"`
	GroupKey   string           `json:"group_key,omitempty"`
	OptionKey  string           `json:"option_key,omitempty"`
}

type AvailabilityChange struct {
	Target          AvailabilityTarget `json:"target"`
	Status          Availability       `json:"status"`
	Reason          string             `json:"reason,omitempty"`
	ExpectedVersion int64              `json:"expected_version"`
}

type AvailabilityResult struct {
	Status  Availability `json:"status"`
	Version int64        `json:"version"`
}

type OptionSelection struct {
	GroupKey  string `json:"group_key"`
	OptionKey string `json:"option_key"`
}

type PriceQuote struct {
	ProductKey string `json:"product_key"`
	UnitMinor  int64  `json:"unit_minor"`
}

func cleanKey(s string) bool {
	if s == "" || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func catalogKey(s string) bool {
	return cleanKey(s) && utf8.RuneCountInString(s) <= 100
}
