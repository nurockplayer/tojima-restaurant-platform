package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
)

// This exercises the customer projections from the same C1/C2 mutations used
// by staff: the committed snapshot remains 660 while guests see the effective
// 330 and do not see a stop request before it is finalized.
func TestPostgresP83B2GuestAdjustmentAndStopVisibility(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	orderID := createP82HTTPOrder(t, pf, grant.Session.ID, grant.Token, "tea", 2, ordering.Cash, "p83-b2-cash")
	guestOps := NewOperationsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, pf.identity, authz.PolicyAuthorizer{Directory: pf.directory})
	readHistory := func() map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v1/guest/sessions/"+grant.Session.ID+"/operations", nil)
		r.Header.Set("Authorization", "Bearer "+grant.Token)
		w := httptest.NewRecorder()
		guestOps.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("guest history status=%d body=%s", w.Code, w.Body.String())
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	readGuestOrder := func() map[string]any {
		t.Helper()
		orders := readHistory()["orders"].([]any)
		for _, raw := range orders {
			o := raw.(map[string]any)
			if o["id"] == orderID {
				return o
			}
		}
		t.Fatalf("guest order missing: %v", orders)
		return nil
	}
	readStatus := func(token string) map[string]any {
		t.Helper()
		w := pf.request(t, http.MethodGet, "/v1/guest/orders/"+orderID+"/status", "", token, nil, "", "")
		var v map[string]any
		decodePaymentHTTP(t, w, http.StatusOK, &v)
		return v
	}
	readGrant, err := f.Orders.RequestOrderRead(ctx, grant.Session.ID, grant.Token, orderID)
	if err != nil {
		t.Fatal(err)
	}
	status := readStatus(readGrant.Token)
	assertGuestOrderDisplay(t, status, false, 660, 660, 2, "UNPAID", "WAITING")
	assertGuestOrderDisplay(t, readGuestOrder(), false, 660, 660, 2, "UNPAID", "WAITING")

	state := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	path := pf.staffPath("table-01", grant.Session.ID, "/orders/"+orderID)
	body := fmt.Sprintf(`{"expected_version":%d,"reason":"private staff reason","lines":[{"line_index":0,"cancelled_quantity":1}],"cash_hand_back_confirmed":false}`, int64JSON(state["version"]))
	adjusted := pf.requestWithKey(t, http.MethodPost, path+"/adjustments", body, pf.creds.Manager.Token, nil, "", "", "p83-b2-adjust")
	if adjusted.Code != http.StatusOK {
		t.Fatalf("adjust status=%d body=%s", adjusted.Code, adjusted.Body.String())
	}
	for _, response := range []map[string]any{readStatus(readGrant.Token), readGuestOrder()} {
		assertGuestOrderDisplay(t, response, true, 660, 330, 1, "UNPAID", "WAITING")
		if response["total_gross_jpy"] != "660" {
			t.Fatalf("original snapshot missing: %v", response)
		}
		if strings.Contains(fmt.Sprint(response), "private staff reason") {
			t.Fatal("guest projection leaked adjustment reason")
		}
	}
	assertExactGuestOrderKeys(t, readStatus(readGrant.Token), []string{"version", "payment_status", "fulfillment_status", "display_state", "currency", "is_terminal"})
	assertExactGuestOrderKeys(t, readGuestOrder(), []string{"id", "payment_status", "fulfillment_status", "display_state", "requires_staff_acceptance", "staff_accepted", "cash_mode"})

	// A pending C2 request is not an effective adjustment. Finalization writes
	// the adjustment, which then becomes visible through both guest surfaces.
	state = readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: grant.Session.Scope.TableID}
	opState, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "cashier", ExpectedVersion: int64JSON(state["version"]), IdempotencyKey: "p83-b2-accept"})
	if err != nil {
		t.Fatal(err)
	}
	opState, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "kitchen", ExpectedVersion: opState.Version, IdempotencyKey: "p83-b2-prepare", Target: operations.Preparing})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := f.Operations.RequestStop(ctx, operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "manager", ExpectedVersion: opState.Version, IdempotencyKey: "p83-b2-stop", Reason: "private stop reason", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []map[string]any{readStatus(readGrant.Token), readGuestOrder()} {
		assertGuestOrderDisplay(t, response, true, 660, 330, 1, "UNPAID", "PREPARING")
	}
	assertExactGuestOrderKeys(t, readStatus(readGrant.Token), []string{"version", "payment_status", "fulfillment_status", "display_state", "currency", "is_terminal"})
	assertExactGuestOrderKeys(t, readGuestOrder(), []string{"id", "payment_status", "fulfillment_status", "display_state", "requires_staff_acceptance", "staff_accepted", "cash_mode"})
	stop := requested.CancellationRequests[0]
	opState, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "kitchen", ExpectedVersion: requested.Version, IdempotencyKey: "p83-b2-confirm", Reason: "private confirmation reason"}, RequestID: stop.ID, ExpectedRequestVersion: stop.Version, Action: "confirm"})
	if err != nil {
		t.Fatal(err)
	}
	stop = opState.CancellationRequests[0]
	for _, response := range []map[string]any{readStatus(readGrant.Token), readGuestOrder()} {
		assertGuestOrderDisplay(t, response, true, 660, 330, 1, "UNPAID", "PREPARING")
	}
	_, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: orderID, Actor: "manager", ExpectedVersion: opState.Version, IdempotencyKey: "p83-b2-finalize"}, RequestID: stop.ID, ExpectedRequestVersion: stop.Version, Action: "finalize", NonDeliveryConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []map[string]any{readStatus(readGrant.Token), readGuestOrder()} {
		assertGuestOrderDisplay(t, response, true, 660, 0, 0, "UNPAID", "CANCELLED")
		if response["display_state"] != "CANCELLED" {
			t.Fatalf("cancel display state=%v", response["display_state"])
		}
	}
	assertExactGuestOrderKeys(t, readStatus(readGrant.Token), []string{"version", "payment_status", "fulfillment_status", "display_state", "currency", "is_terminal"})
	assertExactGuestOrderKeys(t, readGuestOrder(), []string{"id", "payment_status", "fulfillment_status", "display_state", "requires_staff_acceptance", "staff_accepted", "cash_mode"})
}

func TestPostgresP83B2GuestEffectiveLineOptionNames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	// Let the fixture tea accept two selections in its existing aroma group.
	draft, err := f.Menus.GetCompanyDraft(ctx, f.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for ci := range draft.Categories {
		for pi := range draft.Categories[ci].Products {
			product := &draft.Categories[ci].Products[pi]
			if product.Key == "tea" {
				product.Groups[0].MaxSelect = 2
			}
		}
	}
	saved, err := f.Menus.SaveCompanyDraft(ctx, f.TenantID, "fixture", draft.Version, draft.CatalogData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Menus.PublishCompanyDraft(ctx, f.TenantID, "fixture", saved.Version); err != nil {
		t.Fatal(err)
	}
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	cart, err := f.Orders.ReadCart(ctx, grant.Session.ID, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	cart, err = f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "p83-option-line", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 2, Options: []catalog.OptionSelection{{GroupKey: "aroma", OptionKey: "yuzu"}, {GroupKey: "aroma", OptionKey: "ginger"}}})
	if err != nil {
		t.Fatal(err)
	}
	cart, err = f.Orders.Mutate(ctx, ordering.Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "p83-option-line-single", Kind: ordering.AddLine, ProductKey: "tea", Quantity: 1, Options: []catalog.OptionSelection{{GroupKey: "aroma", OptionKey: "ginger"}}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Cash, IdempotencyKey: "p83-option-commit"})
	if err != nil {
		t.Fatal(err)
	}
	reducedIndex := -1
	wantNamesByLine := map[string][]string{}
	for index, quoteLine := range committed.Order.Quote.Lines {
		names := make([]string, len(quoteLine.Options))
		for i, option := range quoteLine.Options {
			names[i] = option.Name
		}
		wantNamesByLine[strings.Join(names, "\x00")] = names
		if len(quoteLine.Options) == 2 {
			reducedIndex = index
		}
	}
	if len(committed.Order.Quote.Lines) != 2 || reducedIndex < 0 || len(wantNamesByLine) != 2 {
		t.Fatalf("unexpected committed variants: %+v", committed.Order.Quote.Lines)
	}
	// Rename the live catalog options after commit; guest history must use names
	// from the immutable committed snapshot when the order is later adjusted.
	draft, err = f.Menus.GetCompanyDraft(ctx, f.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for ci := range draft.Categories {
		for pi := range draft.Categories[ci].Products {
			product := &draft.Categories[ci].Products[pi]
			if product.Key == "tea" {
				for oi := range product.Groups[0].Options {
					product.Groups[0].Options[oi].Name = "renamed-" + product.Groups[0].Options[oi].Key
				}
			}
		}
	}
	saved, err = f.Menus.SaveCompanyDraft(ctx, f.TenantID, "fixture", draft.Version, draft.CatalogData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Menus.PublishCompanyDraft(ctx, f.TenantID, "fixture", saved.Version); err != nil {
		t.Fatal(err)
	}
	state, err := f.Operations.AdjustOrder(ctx, operations.AdjustmentRequest{Scope: operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: grant.Session.Scope.TableID}, SessionID: grant.Session.ID, OrderID: committed.Order.ID, Actor: "manager", ExpectedVersion: 1, IdempotencyKey: "p83-option-reduce", Reason: "reduce quantity", Lines: []operations.AdjustmentLine{{LineIndex: reducedIndex, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if state.EffectiveTotalGrossJPY != 660 {
		t.Fatalf("effective total=%d", state.EffectiveTotalGrossJPY)
	}
	readGrant, err := f.Orders.RequestOrderRead(ctx, grant.Session.ID, grant.Token, committed.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	guestOps := NewOperationsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, pf.identity, authz.PolicyAuthorizer{Directory: pf.directory})
	for _, surface := range []string{"ORDER_READ", "history"} {
		var dto map[string]any
		if surface == "ORDER_READ" {
			w := pf.request(t, http.MethodGet, "/v1/guest/orders/"+committed.Order.ID+"/status", "", readGrant.Token, nil, "", "")
			decodePaymentHTTP(t, w, http.StatusOK, &dto)
		} else {
			r := httptest.NewRequest(http.MethodGet, "/v1/guest/sessions/"+grant.Session.ID+"/operations", nil)
			r.Header.Set("Authorization", "Bearer "+grant.Token)
			w := httptest.NewRecorder()
			guestOps.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("history status=%d body=%s", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			dto = body["orders"].([]any)[0].(map[string]any)
		}
		lines := dto["effective_lines"].([]any)
		if dto["total_gross_jpy"] != "990" || dto["effective_total_gross_jpy"] != "660" || dto["adjusted"] != true {
			t.Fatalf("%s original/effective amounts=%v", surface, dto)
		}
		if len(lines) != 2 {
			t.Fatalf("%s lines=%v", surface, lines)
		}
		seen := map[string]bool{}
		for _, rawLine := range lines {
			line := rawLine.(map[string]any)
			if len(line) != 4 || line["quantity"] != float64(1) || line["line_total_jpy"] != "330" {
				t.Fatalf("%s effective line=%v", surface, line)
			}
			optionNames := line["option_names"].([]any)
			nameSlice := make([]string, len(optionNames))
			for i, name := range optionNames {
				nameSlice[i], _ = name.(string)
			}
			signature := strings.Join(nameSlice, "\x00")
			want, ok := wantNamesByLine[signature]
			if !ok || len(nameSlice) != len(want) {
				t.Fatalf("%s committed option names=%v want map=%v", surface, nameSlice, wantNamesByLine)
			}
			seen[signature] = true
		}
		if len(seen) != 2 {
			t.Fatalf("%s did not preserve both committed option variants: %v", surface, seen)
		}
	}
}

func assertGuestOrderDisplay(t *testing.T, got map[string]any, adjusted bool, original, effective, quantity int64, payment, fulfillment string) {
	t.Helper()
	if got["adjusted"] != adjusted || got["total_gross_jpy"] != fmt.Sprint(original) || got["effective_total_gross_jpy"] != fmt.Sprint(effective) {
		t.Fatalf("guest amount projection=%v", got)
	}
	lines, ok := got["effective_lines"].([]any)
	if !ok || len(lines) != 1 {
		t.Fatalf("effective lines=%v", got["effective_lines"])
	}
	line := lines[0].(map[string]any)
	if line["product_name"] != "ほうじ茶" || line["quantity"] != float64(quantity) || line["line_total_jpy"] != fmt.Sprint(effective) {
		t.Fatalf("effective line=%v", line)
	}
	if got["payment_status"] != payment || got["fulfillment_status"] != fulfillment {
		t.Fatalf("guest state=%v", got)
	}
	wantDisplay := "STANDARD"
	if fulfillment == "CANCELLED" {
		wantDisplay = "CANCELLED"
	}
	if got["display_state"] != wantDisplay {
		t.Fatalf("guest display state=%v want=%s", got["display_state"], wantDisplay)
	}
	if refunds, ok := got["refunds"].([]any); !ok || len(refunds) != 0 {
		t.Fatalf("unexpected refunds=%v", got["refunds"])
	}
	if got["refunded_total_jpy"] != "0" {
		t.Fatalf("refunded total=%v", got["refunded_total_jpy"])
	}
}

func assertExactGuestOrderKeys(t *testing.T, got map[string]any, extra []string) {
	t.Helper()
	want := map[string]bool{"total_gross_jpy": true, "effective_total_gross_jpy": true, "effective_lines": true, "adjusted": true, "refunds": true, "refunded_total_jpy": true}
	for _, key := range extra {
		want[key] = true
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("guest projection missing %q: %v", key, got)
		}
	}
	for key := range got {
		if !want[key] {
			t.Fatalf("guest projection leaked/added key %q: %v", key, got)
		}
	}
	lines := got["effective_lines"].([]any)
	line := lines[0].(map[string]any)
	if len(line) != 4 || line["product_name"] == nil || line["quantity"] == nil || line["line_total_jpy"] == nil || line["option_names"] == nil {
		t.Fatalf("effective line keys=%v", line)
	}
	if names, ok := line["option_names"].([]any); !ok {
		t.Fatalf("option_names=%T", line["option_names"])
	} else {
		for _, name := range names {
			if _, ok := name.(string); !ok {
				t.Fatalf("option name is not a string: %T", name)
			}
		}
	}
	refunds := got["refunds"].([]any)
	if len(refunds) != 0 {
		refund := refunds[0].(map[string]any)
		if len(refund) != 3 || refund["status"] == nil || refund["amount_jpy"] == nil || refund["method"] == nil {
			t.Fatalf("refund keys=%v", refund)
		}
	}
}
