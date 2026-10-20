package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
)

// Go's JSON decoder permits every non-surrogate Unicode rune to arrive as a
// six-byte \\uXXXX escape. Six times the canonical catalog budget plus a small
// envelope allowance bounds that wire representation.
const menuDraftMaxBodyBytes = int64(6*catalog.MaxCatalogCanonicalJSONBytes + 16<<10)

type MenuAPI struct {
	Catalog    *catalog.Service
	Identity   IdentityResolver
	Authorizer authz.Authorizer
}

type detachMenuRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}
type priceOverrideRequest struct {
	PriceMinor      int64 `json:"price_minor"`
	ExpectedVersion int64 `json:"expected_version"`
}
type resetOverrideRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}
type companyPreviewRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}
type publishCompanyCheckedRequest struct {
	ExpectedVersion    int64  `json:"expected_version"`
	PreviewFingerprint string `json:"preview_fingerprint"`
}

type guestMenu struct {
	StoreDisplayName string          `json:"store_display_name,omitempty"`
	Currency         string          `json:"currency"`
	PriceMode        string          `json:"price_mode"`
	Mode             string          `json:"mode,omitempty"`
	Revision         int64           `json:"revision,omitempty"`
	Categories       []guestCategory `json:"categories"`
}
type guestCategory struct {
	Key      string         `json:"key"`
	Name     string         `json:"name"`
	Position int            `json:"position"`
	Products []guestProduct `json:"products"`
}
type guestProduct struct {
	Key                        string               `json:"key"`
	Name                       string               `json:"name"`
	Description                string               `json:"description,omitempty"`
	EffectivePriceMinor        int64                `json:"effective_price_minor"`
	Position                   int                  `json:"position"`
	Availability               catalog.Availability `json:"availability"`
	Selectable                 bool                 `json:"selectable"`
	CompanyEmergencyHoldActive bool                 `json:"company_emergency_hold_active"`
	AvailabilityVersion        int64                `json:"availability_version,omitempty"`
	PriceOverrideVersion       int64                `json:"price_override_version,omitempty"`
	Groups                     []guestGroup         `json:"option_groups"`
}
type guestGroup struct {
	Key       string        `json:"key"`
	Name      string        `json:"name"`
	MinSelect int           `json:"min_select"`
	MaxSelect int           `json:"max_select"`
	Position  int           `json:"position"`
	Options   []guestOption `json:"options"`
}
type guestOption struct {
	Key                 string               `json:"key"`
	Name                string               `json:"name"`
	PriceDeltaMinor     int64                `json:"price_delta_minor"`
	Position            int                  `json:"position"`
	Availability        catalog.Availability `json:"availability"`
	AvailabilityVersion int64                `json:"availability_version,omitempty"`
}

type storeAdminMenu struct {
	Currency               string          `json:"currency"`
	PriceMode              string          `json:"price_mode"`
	Mode                   string          `json:"mode"`
	Revision               int64           `json:"revision"`
	CanPauseAvailability   bool            `json:"can_pause_availability"`
	CanConfirmAvailability bool            `json:"can_confirm_availability"`
	CanEditPrices          bool            `json:"can_edit_prices"`
	CanDetach              bool            `json:"can_detach"`
	Categories             []adminCategory `json:"categories"`
}
type adminCategory struct {
	Key      string         `json:"key"`
	Name     string         `json:"name"`
	Position int            `json:"position"`
	Products []adminProduct `json:"products"`
}
type adminProduct struct {
	Key                        string               `json:"key"`
	Name                       string               `json:"name"`
	Description                string               `json:"description,omitempty"`
	BasePriceMinor             int64                `json:"base_price_minor"`
	OverrideActive             bool                 `json:"override_active"`
	OverridePriceMinor         *int64               `json:"override_price_minor"`
	EffectivePriceMinor        int64                `json:"effective_price_minor"`
	Position                   int                  `json:"position"`
	Active                     bool                 `json:"active"`
	Availability               catalog.Availability `json:"availability"`
	Selectable                 bool                 `json:"selectable"`
	CompanyEmergencyHoldActive bool                 `json:"company_emergency_hold_active"`
	AvailabilityVersion        int64                `json:"availability_version"`
	PriceOverrideVersion       int64                `json:"price_override_version"`
	Groups                     []adminGroup         `json:"option_groups"`
}
type adminGroup struct {
	Key       string        `json:"key"`
	Name      string        `json:"name"`
	MinSelect int           `json:"min_select"`
	MaxSelect int           `json:"max_select"`
	Position  int           `json:"position"`
	Options   []adminOption `json:"options"`
}
type adminOption struct {
	Key                 string               `json:"key"`
	Name                string               `json:"name"`
	PriceDeltaMinor     int64                `json:"price_delta_minor"`
	Position            int                  `json:"position"`
	Active              bool                 `json:"active"`
	Availability        catalog.Availability `json:"availability"`
	AvailabilityVersion int64                `json:"availability_version"`
}

func NewMenuHandler(service *catalog.Service, identity IdentityResolver, authorizer authz.Authorizer) http.Handler {
	if identity == nil {
		identity = DenyIdentity{}
	}
	if authorizer == nil {
		authorizer = authz.DenyAll{}
	}
	api := MenuAPI{Catalog: service, Identity: identity, Authorizer: authorizer}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/guest/tables/{locator}/menu", api.publicMenu)
	mux.HandleFunc("GET /v1/admin/tenants/{tenantID}/menu/published", api.getCompanyPublished)
	mux.Handle("GET /v1/admin/tenants/{tenantID}/menu/products/{productKey}/emergency-hold", noStore(http.HandlerFunc(api.getCompanyProductEmergencyHold)))
	mux.Handle("PUT /v1/admin/tenants/{tenantID}/menu/products/{productKey}/emergency-hold", noStore(http.HandlerFunc(api.setCompanyProductEmergencyHold)))
	mux.HandleFunc("GET /v1/admin/tenants/{tenantID}/menu/draft", api.getCompanyDraft)
	mux.HandleFunc("PUT /v1/admin/tenants/{tenantID}/menu/draft", api.saveCompanyDraft)
	mux.HandleFunc("POST /v1/admin/tenants/{tenantID}/menu/preview", api.previewCompanyDraft)
	mux.HandleFunc("POST /v1/admin/tenants/{tenantID}/menu/publish", api.publishCompanyDraft)
	mux.HandleFunc("POST /v1/admin/tenants/{tenantID}/stores/{storeID}/menu/detach", api.detachStore)
	mux.HandleFunc("GET /v1/admin/tenants/{tenantID}/stores/{storeID}/menu", api.storeMenu)
	mux.HandleFunc("GET /v1/admin/tenants/{tenantID}/stores/{storeID}/menu/draft", api.getStoreDraft)
	mux.HandleFunc("PUT /v1/admin/tenants/{tenantID}/stores/{storeID}/menu/draft", api.saveStoreDraft)
	mux.HandleFunc("POST /v1/admin/tenants/{tenantID}/stores/{storeID}/menu/publish", api.publishStoreDraft)
	mux.HandleFunc("PUT /v1/admin/tenants/{tenantID}/stores/{storeID}/menu/prices/{productKey}", api.setPriceOverride)
	mux.HandleFunc("POST /v1/admin/tenants/{tenantID}/stores/{storeID}/menu/prices/{productKey}/reset", api.resetPriceOverride)
	mux.HandleFunc("PUT /v1/admin/tenants/{tenantID}/stores/{storeID}/menu/availability", api.setAvailability)
	return mux
}

func (a MenuAPI) publicMenu(w http.ResponseWriter, r *http.Request) {
	menu, err := a.Catalog.ReadPublishedByTableLocator(r.Context(), r.PathValue("locator"))
	if err != nil {
		respondMenu(w, nil, err)
		return
	}
	if r.URL.Query().Get("amounts") == "string" {
		writeMoneyJSON(w, http.StatusOK, safeGuestMenu(menu))
		return
	}
	writeJSON(w, http.StatusOK, safeGuestMenu(menu))
}

func safeGuestMenu(menu catalog.PublishedMenu) guestMenu {
	return safeMenu(menu, false)
}

func safeMenu(menu catalog.PublishedMenu, includeInactive bool) guestMenu {
	result := guestMenu{Currency: "JPY", PriceMode: "TAX_INCLUSIVE", Categories: make([]guestCategory, 0, len(menu.Categories))}
	if includeInactive {
		result.Mode = menu.Mode
		result.Revision = menu.Revision
	} else if menu.GuestDisplayName != nil {
		result.StoreDisplayName = *menu.GuestDisplayName
	}
	for _, category := range menu.Categories {
		outCategory := guestCategory{Key: category.Key, Name: category.Name, Position: category.Position, Products: make([]guestProduct, 0, len(category.Products))}
		for _, product := range category.Products {
			if !includeInactive && !product.Active {
				continue
			}
			outProduct := guestProduct{Key: product.Key, Name: product.Name, Description: product.Description, EffectivePriceMinor: product.EffectivePriceMinor, Position: product.Position, Availability: product.Availability, Selectable: product.Selectable, CompanyEmergencyHoldActive: product.CompanyEmergencyHoldActive, Groups: make([]guestGroup, 0, len(product.Groups))}
			for _, group := range product.Groups {
				outGroup := guestGroup{Key: group.Key, Name: group.Name, MinSelect: group.MinSelect, MaxSelect: group.MaxSelect, Position: group.Position, Options: make([]guestOption, 0, len(group.Options))}
				for _, option := range group.Options {
					if !includeInactive && !option.Active {
						continue
					}
					outGroup.Options = append(outGroup.Options, guestOption{Key: option.Key, Name: option.Name, PriceDeltaMinor: option.PriceDeltaMinor, Position: option.Position, Availability: option.Availability})
				}
				outProduct.Groups = append(outProduct.Groups, outGroup)
			}
			outCategory.Products = append(outCategory.Products, outProduct)
		}
		if !includeInactive && len(outCategory.Products) == 0 {
			continue
		}
		result.Categories = append(result.Categories, outCategory)
	}
	return result
}

func safeStoreAdminProjection(a MenuAPI, ctx context.Context, actor authz.Actor, scope catalog.StoreScope, projection catalog.StoreAdminProjection) storeAdminMenu {
	result := storeAdminMenu{
		Currency: "JPY", PriceMode: "TAX_INCLUSIVE", Mode: projection.Menu.Mode, Revision: projection.Menu.Revision,
		CanPauseAvailability:   a.canStore(ctx, actor, scope, "menu.availability.write", []authz.Role{authz.Staff, authz.StoreManager}, []authz.Role{authz.StoreManager}),
		CanConfirmAvailability: a.canStore(ctx, actor, scope, "menu.availability.write", []authz.Role{authz.StoreManager}, []authz.Role{authz.StoreManager}),
		CanEditPrices:          a.canStore(ctx, actor, scope, "menu.price.override", []authz.Role{authz.StoreManager}, []authz.Role{authz.StoreManager}),
		CanDetach:              a.canTenant(ctx, actor, scope.TenantID, "menu.detach", []authz.Role{authz.TenantAdmin}, []authz.Role{authz.TenantAdmin}),
		Categories:             make([]adminCategory, 0, len(projection.Menu.Categories)),
	}
	prices := make(map[string]catalog.StoreProductPrice, len(projection.ProductPrices))
	for _, price := range projection.ProductPrices {
		prices[price.ProductKey] = price
	}
	for _, category := range projection.Menu.Categories {
		outCategory := adminCategory{Key: category.Key, Name: category.Name, Position: category.Position, Products: make([]adminProduct, 0, len(category.Products))}
		for _, product := range category.Products {
			price := prices[product.Key]
			outProduct := adminProduct{
				Key: product.Key, Name: product.Name, Description: product.Description,
				BasePriceMinor: price.SourceBasePriceMinor, OverrideActive: price.OverrideActive, OverridePriceMinor: price.OverridePriceMinor,
				EffectivePriceMinor: price.EffectivePriceMinor, Position: product.Position, Active: product.Active,
				Availability: product.Availability, Selectable: product.Selectable,
				CompanyEmergencyHoldActive: product.CompanyEmergencyHoldActive,
				AvailabilityVersion:        product.AvailabilityVersion, PriceOverrideVersion: price.OverrideVersion,
				Groups: make([]adminGroup, 0, len(product.Groups)),
			}
			for _, group := range product.Groups {
				outGroup := adminGroup{Key: group.Key, Name: group.Name, MinSelect: group.MinSelect, MaxSelect: group.MaxSelect, Position: group.Position, Options: make([]adminOption, 0, len(group.Options))}
				for _, option := range group.Options {
					outGroup.Options = append(outGroup.Options, adminOption{Key: option.Key, Name: option.Name, PriceDeltaMinor: option.PriceDeltaMinor, Position: option.Position, Active: option.Active, Availability: option.Availability, AvailabilityVersion: option.AvailabilityVersion})
				}
				outProduct.Groups = append(outProduct.Groups, outGroup)
			}
			outCategory.Products = append(outCategory.Products, outProduct)
		}
		result.Categories = append(result.Categories, outCategory)
	}
	return result
}

func (a MenuAPI) canStore(ctx context.Context, actor authz.Actor, scope catalog.StoreScope, permission string, roles, mfaRoles []authz.Role) bool {
	return a.Authorizer.Authorize(ctx, authz.Request{
		Actor: actor, Permission: authz.Permission(permission),
		Resource:     authz.Scope{TenantID: scope.TenantID, StoreID: scope.StoreID},
		AllowedRoles: roles, MFARequiredRoles: mfaRoles,
	}) == nil
}

func (a MenuAPI) canTenant(ctx context.Context, actor authz.Actor, tenantID, permission string, roles, mfaRoles []authz.Role) bool {
	return a.Authorizer.Authorize(ctx, authz.Request{
		Actor: actor, Permission: authz.Permission(permission),
		Resource: authz.Scope{TenantID: tenantID}, AllowedRoles: roles, MFARequiredRoles: mfaRoles,
	}) == nil
}

func safeAdminStoreMenu(menu catalog.PublishedMenu) storeAdminMenu {
	prices := make([]catalog.StoreProductPrice, 0)
	for _, category := range menu.Categories {
		for _, product := range category.Products {
			prices = append(prices, catalog.StoreProductPrice{
				ProductKey: product.Key, SourceBasePriceMinor: product.BasePriceMinor,
				OverrideActive: product.PriceOverrideActive, OverridePriceMinor: product.PriceOverrideMinor,
				EffectivePriceMinor: product.EffectivePriceMinor, OverrideVersion: product.PriceOverrideVersion,
			})
		}
	}
	return safeStoreAdminProjection(MenuAPI{Authorizer: authz.DenyAll{}}, context.Background(), authz.Actor{}, catalog.StoreScope{}, catalog.StoreAdminProjection{Menu: menu, ProductPrices: prices})
}

func (a MenuAPI) getCompanyDraft(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")
	if !a.authorizeTenant(w, r, tenantID, "menu.draft.read") {
		return
	}
	if err := a.Catalog.TenantEnabled(r.Context(), tenantID); err != nil {
		respondMenu(w, nil, err)
		return
	}
	draft, err := a.Catalog.GetCompanyDraft(r.Context(), tenantID)
	respondMenuRequest(w, r, draft, err)
}

func (a MenuAPI) getCompanyPublished(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantID")
	if !a.authorizeTenant(w, r, tenantID, "menu.published.read") {
		return
	}
	snapshot, err := a.Catalog.ReadCompanyPublishedSnapshot(r.Context(), tenantID)
	if err != nil {
		respondMenu(w, nil, err)
		return
	}
	writeMenuJSON(w, r, struct {
		Revision int64 `json:"revision"`
		catalog.CatalogData
	}{Revision: snapshot.Revision, CatalogData: snapshot.CatalogData})
}

func (a MenuAPI) previewCompanyDraft(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := a.authorizeCompany(w, r, "menu.publish")
	if !ok {
		return
	}
	var input companyPreviewRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	preview, err := a.Catalog.PreviewCompanyPublication(r.Context(), tenantID, input.ExpectedVersion)
	respondMenu(w, preview, err)
}

func (a MenuAPI) saveCompanyDraft(w http.ResponseWriter, r *http.Request) {
	tenantID, actor, ok := a.authorizeCompany(w, r, "menu.draft.write")
	if !ok {
		return
	}
	var input catalog.Draft
	if err := decodeMenuDraftJSON(w, r, &input); err != nil {
		writeMenuDraftDecodeError(w, err)
		return
	}
	draft, err := a.Catalog.SaveCompanyDraft(r.Context(), tenantID, actor.SubjectID, input.Version, input.CatalogData)
	respondMenuRequest(w, r, draft, err)
}

func (a MenuAPI) publishCompanyDraft(w http.ResponseWriter, r *http.Request) {
	tenantID, actor, ok := a.authorizeCompany(w, r, "menu.publish")
	if !ok {
		return
	}
	var input publishCompanyCheckedRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if input.PreviewFingerprint == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := a.Catalog.PublishCompanyDraftChecked(r.Context(), tenantID, actor.SubjectID, input.ExpectedVersion, input.PreviewFingerprint)
	var blocked *catalog.PublicationValidationError
	if errors.As(err, &blocked) {
		writeJSON(w, http.StatusBadRequest, struct {
			Error  string                     `json:"error"`
			Issues []catalog.PublicationIssue `json:"issues"`
		}{Error: "publication_blocked", Issues: blocked.Issues})
		return
	}
	respondMenu(w, result, err)
}

func decodeMenuDraftJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSONLimit(w, r, dst, menuDraftMaxBodyBytes)
}

func writeMenuDraftDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request")
}

func (a MenuAPI) detachStore(w http.ResponseWriter, r *http.Request) {
	tenantID, actor, ok := a.authorizeCompany(w, r, "menu.detach")
	if !ok {
		return
	}
	scope, err := a.Catalog.ResolveStoreScope(r.Context(), tenantID, r.PathValue("storeID"))
	if err != nil {
		respondMenu(w, nil, err)
		return
	}
	var input detachMenuRequest
	if err = decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := a.Catalog.DetachStore(r.Context(), scope, actor.SubjectID, input.ExpectedRevision)
	respondMenu(w, result, err)
}

func (a MenuAPI) storeMenu(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.authorizeStore(w, r, "menu.store.read", []authz.Role{authz.Staff, authz.StoreManager, authz.TenantAdmin}, []authz.Role{authz.StoreManager, authz.TenantAdmin})
	if !ok {
		return
	}
	projection, err := a.Catalog.ReadStoreAdminProjection(r.Context(), scope)
	if err != nil {
		respondMenu(w, nil, err)
		return
	}
	writeMenuJSON(w, r, safeStoreAdminProjection(a, r.Context(), actor, scope, projection))
}

func (a MenuAPI) getStoreDraft(w http.ResponseWriter, r *http.Request) {
	scope, _, ok := a.authorizeStore(w, r, "menu.detached.read", []authz.Role{authz.StoreManager}, []authz.Role{authz.StoreManager})
	if !ok {
		return
	}
	draft, err := a.Catalog.GetStoreDraft(r.Context(), scope)
	respondMenuRequest(w, r, draft, err)
}

func (a MenuAPI) saveStoreDraft(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.authorizeStore(w, r, "menu.detached.write", []authz.Role{authz.StoreManager}, []authz.Role{authz.StoreManager})
	if !ok {
		return
	}
	var input catalog.Draft
	if err := decodeMenuDraftJSON(w, r, &input); err != nil {
		writeMenuDraftDecodeError(w, err)
		return
	}
	draft, err := a.Catalog.SaveStoreDraft(r.Context(), scope, actor.SubjectID, input.Version, input.CatalogData)
	respondMenuRequest(w, r, draft, err)
}

func (a MenuAPI) publishStoreDraft(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.authorizeStore(w, r, "menu.detached.publish", []authz.Role{authz.StoreManager}, []authz.Role{authz.StoreManager})
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := a.Catalog.PublishStoreDraft(r.Context(), scope, actor.SubjectID, input.ExpectedVersion)
	respondMenu(w, result, err)
}

func (a MenuAPI) setPriceOverride(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.authorizeStore(w, r, "menu.price.override", []authz.Role{authz.StoreManager}, []authz.Role{authz.StoreManager})
	if !ok {
		return
	}
	var input priceOverrideRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := a.Catalog.SetStorePriceOverride(r.Context(), scope, actor.SubjectID, r.PathValue("productKey"), input.PriceMinor, input.ExpectedVersion)
	respondMenu(w, result, err)
}

func (a MenuAPI) resetPriceOverride(w http.ResponseWriter, r *http.Request) {
	scope, actor, ok := a.authorizeStore(w, r, "menu.price.override", []authz.Role{authz.StoreManager}, []authz.Role{authz.StoreManager})
	if !ok {
		return
	}
	var input resetOverrideRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := a.Catalog.ResetStorePriceOverride(r.Context(), scope, actor.SubjectID, r.PathValue("productKey"), input.ExpectedVersion)
	respondMenu(w, result, err)
}

func (a MenuAPI) setAvailability(w http.ResponseWriter, r *http.Request) {
	scope, err := a.Catalog.ResolveStoreScope(r.Context(), r.PathValue("tenantID"), r.PathValue("storeID"))
	if err != nil {
		respondMenu(w, nil, err)
		return
	}
	actor, err := a.Identity.Resolve(r.Context(), r)
	if err != nil {
		respondMenu(w, nil, authz.ErrDenied)
		return
	}
	var input catalog.AvailabilityChange
	if err := decodeJSON(w, r, &input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var roles, mfaRoles []authz.Role
	switch input.Status {
	case catalog.Paused:
		roles = []authz.Role{authz.Staff, authz.StoreManager}
		mfaRoles = []authz.Role{authz.StoreManager}
	case catalog.Available:
		roles = []authz.Role{authz.StoreManager}
		mfaRoles = []authz.Role{authz.StoreManager}
	default:
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err = a.Authorizer.Authorize(r.Context(), authz.Request{
		Actor: actor, Permission: "menu.availability.write",
		Resource:     authz.Scope{TenantID: scope.TenantID, StoreID: scope.StoreID},
		AllowedRoles: roles, MFARequiredRoles: mfaRoles,
	}); err != nil {
		respondMenu(w, nil, authz.ErrDenied)
		return
	}
	result, err := a.Catalog.SetStoreAvailability(r.Context(), scope, actor.SubjectID, input)
	respondMenu(w, result, err)
}

func (a MenuAPI) authorizeCompany(w http.ResponseWriter, r *http.Request, permission string) (string, authz.Actor, bool) {
	tenantID := r.PathValue("tenantID")
	if err := a.Catalog.TenantEnabled(r.Context(), tenantID); err != nil {
		respondMenu(w, nil, err)
		return "", authz.Actor{}, false
	}
	actor, err := a.Identity.Resolve(r.Context(), r)
	if err != nil {
		respondMenu(w, nil, authz.ErrDenied)
		return "", authz.Actor{}, false
	}
	request := authz.Request{Actor: actor, Permission: authz.Permission(permission), Resource: authz.Scope{TenantID: tenantID}, AllowedRoles: []authz.Role{authz.TenantAdmin}, MFARequiredRoles: []authz.Role{authz.TenantAdmin}}
	if err = a.Authorizer.Authorize(r.Context(), request); err != nil {
		respondMenu(w, nil, authz.ErrDenied)
		return "", authz.Actor{}, false
	}
	return tenantID, actor, true
}

func (a MenuAPI) authorizeTenant(w http.ResponseWriter, r *http.Request, tenantID, permission string) bool {
	_, _, ok := a.authorizeCompany(w, r, permission)
	return ok && r.PathValue("tenantID") == tenantID
}

func (a MenuAPI) authorizeStore(w http.ResponseWriter, r *http.Request, permission string, roles, mfaRoles []authz.Role) (catalog.StoreScope, authz.Actor, bool) {
	scope, err := a.Catalog.ResolveStoreScope(r.Context(), r.PathValue("tenantID"), r.PathValue("storeID"))
	if err != nil {
		respondMenu(w, nil, err)
		return catalog.StoreScope{}, authz.Actor{}, false
	}
	actor, err := a.Identity.Resolve(r.Context(), r)
	if err != nil {
		respondMenu(w, nil, authz.ErrDenied)
		return catalog.StoreScope{}, authz.Actor{}, false
	}
	request := authz.Request{Actor: actor, Permission: authz.Permission(permission), Resource: authz.Scope{TenantID: scope.TenantID, StoreID: scope.StoreID}, AllowedRoles: roles, MFARequiredRoles: mfaRoles}
	if err = a.Authorizer.Authorize(r.Context(), request); err != nil {
		respondMenu(w, nil, authz.ErrDenied)
		return catalog.StoreScope{}, authz.Actor{}, false
	}
	return scope, actor, true
}

func writeMenuJSON(w http.ResponseWriter, r *http.Request, value any) {
	if r.URL.Query().Get("amounts") == "string" {
		writeMoneyJSON(w, http.StatusOK, value)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func respondMenuRequest(w http.ResponseWriter, r *http.Request, result any, err error) {
	if err != nil {
		respondMenu(w, nil, err)
		return
	}
	writeMenuJSON(w, r, result)
}

func respondMenu(w http.ResponseWriter, result any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, catalog.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, catalog.ErrNotPublished):
		status, code = http.StatusNotFound, "menu_unavailable"
	case errors.Is(err, catalog.ErrDenied), errors.Is(err, authz.ErrDenied):
		status, code = http.StatusForbidden, "forbidden"
	case errors.Is(err, catalog.ErrConflict), errors.Is(err, catalog.ErrStale), errors.Is(err, catalog.ErrNotDetached), errors.Is(err, catalog.ErrAlreadyDetached):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, catalog.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_menu"
	case errors.Is(err, catalog.ErrPriceOverflow):
		status, code = http.StatusUnprocessableEntity, "price_overflow"
	}
	writeError(w, status, code)
}
