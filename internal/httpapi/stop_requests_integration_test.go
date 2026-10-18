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
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

func TestPostgresP82StopRequestHTTPRolesAndKitchenConfirmation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	orderID := createP82HTTPOrder(t, pf, grant.Session.ID, grant.Token, "tea", 1, ordering.Cash, "p82-stop-http")
	sessionPath := pf.staffPath("table-01", grant.Session.ID, "")
	orderPath := sessionPath + "/orders/" + orderID
	order := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	accept := pf.requestWithKey(t, http.MethodPost, orderPath+"/accept", fmt.Sprintf(`{"expected_version":%d}`, int64JSON(order["version"])), pf.creds.Cashier.Token, nil, "", "", "stop-accept")
	if accept.Code != http.StatusOK {
		t.Fatalf("accept=%d %s", accept.Code, accept.Body.String())
	}
	order = readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	prepared := pf.requestWithKey(t, http.MethodPost, orderPath+"/fulfillment", fmt.Sprintf(`{"expected_version":%d,"target":"PREPARING"}`, int64JSON(order["version"])), pf.creds.Kitchen.Token, nil, "", "", "stop-prepare")
	if prepared.Code != http.StatusOK {
		t.Fatalf("prepare=%d %s", prepared.Code, prepared.Body.String())
	}
	stopPath := orderPath + "/stop-requests"
	body := fmt.Sprintf(`{"expected_version":%d,"reason":"guest changed their mind","lines":[{"line_index":0,"cancelled_quantity":1}]}`, int64JSON(order["version"])+1)
	request := func(method, path, payload, token, key string) *httptest.ResponseRecorder {
		return pf.requestWithKey(t, method, path, payload, token, nil, "", "", key)
	}
	for _, denied := range []struct{ name, token string }{{"cashier", pf.creds.Cashier.Token}, {"kitchen", pf.creds.Kitchen.Token}, {"guest", grant.Token}, {"anonymous", ""}} {
		w := request(http.MethodPost, stopPath, body, denied.token, "stop-denied")
		if w.Code != http.StatusForbidden {
			t.Fatalf("stop request allowed for %s: %d %s", denied.name, w.Code, w.Body.String())
		}
	}
	setMembership := func(subject string, memberships ...authz.Membership) {
		t.Helper()
		if err := pf.directory.Set(subject, memberships); err != nil {
			t.Fatal(err)
		}
	}
	setMembership(pf.creds.Cashier.SubjectID, authz.Membership{TenantID: "test-tenant", StoreID: "test-store", Role: authz.StoreManager})
	if w := request(http.MethodPost, stopPath, body, pf.creds.Cashier.Token, "stop-no-mfa"); w.Code != http.StatusForbidden {
		t.Fatalf("manager without MFA allowed: %d %s", w.Code, w.Body.String())
	}
	setMembership(pf.creds.Cashier.SubjectID, authz.Membership{TenantID: "test-tenant", StoreID: "test-store", Role: authz.Staff})
	for _, membership := range []authz.Membership{{TenantID: "test-tenant", Role: authz.TenantAdmin}, {TenantID: "test-tenant", StoreID: "other-store", Role: authz.StoreManager}, {TenantID: "other-tenant", StoreID: "test-store", Role: authz.StoreManager}} {
		setMembership(pf.creds.Manager.SubjectID, membership)
		w := request(http.MethodPost, stopPath, body, pf.creds.Manager.Token, "stop-wrong-scope")
		if w.Code != http.StatusForbidden {
			t.Fatalf("out of scope manager %+v allowed: %d %s", membership, w.Code, w.Body.String())
		}
	}
	setMembership(pf.creds.Manager.SubjectID, authz.Membership{TenantID: "test-tenant", StoreID: "test-store", Role: authz.StoreManager})
	created := request(http.MethodPost, stopPath, body, pf.creds.Manager.Token, "stop-http")
	if created.Code != http.StatusOK {
		t.Fatalf("request=%d %s", created.Code, created.Body.String())
	}
	var state map[string]any
	if err = json.Unmarshal(created.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["effective_total_gross_jpy"] != "330" || state["total_gross_jpy"] != order["total_gross_jpy"] {
		t.Fatalf("request changed the order amount: %s", created.Body.String())
	}
	requests, ok := state["cancellation_requests"].([]any)
	if !ok || len(requests) != 1 {
		t.Fatalf("missing request projection: %s", created.Body.String())
	}
	stop := requests[0].(map[string]any)
	id := stop["id"].(string)
	version := int64JSON(stop["version"])
	actionPath := stopPath + "/" + id + "/confirm"
	kitchenBody := fmt.Sprintf(`{"expected_version":%d,"expected_request_version":%d}`, int64JSON(state["version"]), version)
	for _, denied := range []struct{ name, token string }{{"manager", pf.creds.Manager.Token}, {"cashier", pf.creds.Cashier.Token}, {"guest", grant.Token}, {"anonymous", ""}} {
		w := request(http.MethodPost, actionPath, kitchenBody, denied.token, "stop-confirm-denied")
		if w.Code != http.StatusForbidden {
			t.Fatalf("confirm allowed for %s: %d %s", denied.name, w.Code, w.Body.String())
		}
	}
	if err = pf.directory.Set(pf.creds.Kitchen.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "other-store", Role: authz.Kitchen}}); err != nil {
		t.Fatal(err)
	}
	if denied := request(http.MethodPost, actionPath, kitchenBody, pf.creds.Kitchen.Token, "stop-confirm-other-store"); denied.Code != http.StatusForbidden {
		t.Fatalf("cross-store kitchen confirmed stop: %d %s", denied.Code, denied.Body.String())
	}
	if err = pf.directory.Set(pf.creds.Kitchen.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "test-store", Role: authz.Kitchen}}); err != nil {
		t.Fatal(err)
	}
	confirmed := request(http.MethodPost, actionPath, kitchenBody, pf.creds.Kitchen.Token, "stop-confirm")
	if confirmed.Code != http.StatusOK || !strings.Contains(confirmed.Body.String(), `"status":"KITCHEN_CONFIRMED"`) {
		t.Fatalf("confirm=%d %s", confirmed.Code, confirmed.Body.String())
	}
	if strings.Contains(confirmed.Body.String(), `"effective_total_gross_jpy":"0"`) {
		t.Fatalf("kitchen confirm changed amounts: %s", confirmed.Body.String())
	}
	var confirmedState map[string]any
	if err = json.Unmarshal(confirmed.Body.Bytes(), &confirmedState); err != nil {
		t.Fatal(err)
	}
	ready := request(http.MethodPost, orderPath+"/fulfillment", fmt.Sprintf(`{"expected_version":%d,"target":"READY"}`, int64JSON(confirmedState["version"])), pf.creds.Kitchen.Token, "stop-ready-pending")
	if ready.Code != http.StatusConflict || !strings.Contains(ready.Body.String(), `"error":"stop_request_pending"`) {
		t.Fatalf("READY over confirmed stop request status=%d body=%s", ready.Code, ready.Body.String())
	}
	pf.directory.Remove(pf.creds.Kitchen.SubjectID)
	if replay := request(http.MethodPost, actionPath, kitchenBody, pf.creds.Kitchen.Token, "stop-confirm"); replay.Code != http.StatusForbidden {
		t.Fatalf("revoked kitchen replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}

func TestPostgresP82StopRequestStripePartialRefundAfterApprovedPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	orderID := createP82HTTPOrder(t, pf, grant.Session.ID, grant.Token, "tea", 2, ordering.Stripe, "p82-stop-stripe")
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("create checkout worked=%v err=%v", worked, err)
	}
	var checkoutID string
	if err = f.Pool.QueryRow(ctx, `SELECT checkout_session_id FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&checkoutID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Pool.Exec(ctx, `UPDATE operation_order_state SET operational_hold=true,version=version+1 WHERE order_id=$1`, orderID); err != nil {
		t.Fatal(err)
	}
	paid, err := pf.provider.SetOutcome(checkoutID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pf.service.EnqueueVerifiedWebhook(ctx, stripeprovider.VerifiedEvent{ID: "evt_p82_stop_stripe", Type: "checkout.session.completed", AccountID: paid.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now().UTC(), ObjectID: paid.SessionID, Checkout: &paid}); err != nil {
		t.Fatal(err)
	}
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("paid webhook worked=%v err=%v", worked, err)
	}
	base := pf.staffPath("table-01", grant.Session.ID, "")
	orderPath := base + "/orders/" + orderID
	request := func(method, path, body, token, key string) *httptest.ResponseRecorder {
		return pf.requestWithKey(t, method, path, body, token, nil, "", "", key)
	}
	order := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	approved := request(http.MethodPost, orderPath+"/approve-fulfillment", fmt.Sprintf(`{"expected_version":%d,"reason":"manager confirms this paid order"}`, int64JSON(order["version"])), pf.creds.Manager.Token, "stop-stripe-approve")
	if approved.Code != http.StatusOK {
		t.Fatalf("approve=%d %s", approved.Code, approved.Body.String())
	}
	order = readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	prep := request(http.MethodPost, orderPath+"/fulfillment", fmt.Sprintf(`{"expected_version":%d,"target":"PREPARING"}`, int64JSON(order["version"])), pf.creds.Kitchen.Token, "stop-stripe-prepare")
	if prep.Code != http.StatusOK {
		t.Fatalf("prepare=%d %s", prep.Code, prep.Body.String())
	}
	order = readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	stopPath := orderPath + "/stop-requests"
	stopBody := fmt.Sprintf(`{"expected_version":%d,"reason":"one tea was not needed","lines":[{"line_index":0,"cancelled_quantity":1}]}`, int64JSON(order["version"]))
	created := request(http.MethodPost, stopPath, stopBody, pf.creds.Manager.Token, "stop-stripe-request")
	if created.Code != http.StatusOK {
		t.Fatalf("request=%d %s", created.Code, created.Body.String())
	}
	var state map[string]any
	if err = json.Unmarshal(created.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	entry := state["cancellation_requests"].([]any)[0].(map[string]any)
	confirm := request(http.MethodPost, stopPath+"/"+entry["id"].(string)+"/confirm", fmt.Sprintf(`{"expected_version":%d,"expected_request_version":%d}`, int64JSON(state["version"]), int64JSON(entry["version"])), pf.creds.Kitchen.Token, "stop-stripe-confirm")
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm=%d %s", confirm.Code, confirm.Body.String())
	}
	var confirmed map[string]any
	if err = json.Unmarshal(confirm.Body.Bytes(), &confirmed); err != nil {
		t.Fatal(err)
	}
	confirmedEntry := confirmed["cancellation_requests"].([]any)[0].(map[string]any)
	finalBody := fmt.Sprintf(`{"expected_version":%d,"expected_request_version":%d,"non_delivery_confirmed":true}`, int64JSON(confirmed["version"]), int64JSON(confirmedEntry["version"]))
	for _, denied := range []struct{ name, token string }{{"cashier", pf.creds.Cashier.Token}, {"kitchen", pf.creds.Kitchen.Token}} {
		w := request(http.MethodPost, stopPath+"/"+entry["id"].(string)+"/finalize", finalBody, denied.token, "stop-stripe-finalize-denied")
		if w.Code != http.StatusForbidden {
			t.Fatalf("finalize allowed for %s: %d %s", denied.name, w.Code, w.Body.String())
		}
	}
	final := request(http.MethodPost, stopPath+"/"+entry["id"].(string)+"/finalize", finalBody, pf.creds.Manager.Token, "stop-stripe-finalize")
	if final.Code != http.StatusOK || !strings.Contains(final.Body.String(), `"status":"REQUESTED"`) || !strings.Contains(final.Body.String(), `"effective_total_gross_jpy":"330"`) {
		t.Fatalf("finalize=%d %s", final.Code, final.Body.String())
	}
	var refunds int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_refunds WHERE order_id=$1 AND amount_jpy=330`, orderID).Scan(&refunds); err != nil || refunds != 1 {
		t.Fatalf("Stripe refund request count=%d err=%v", refunds, err)
	}
	var refundID, paymentIntent string
	if err = f.Pool.QueryRow(ctx, `SELECT r.refund_id,a.payment_intent_id FROM operation_refunds r JOIN stripe_payment_attempts a USING(order_id) WHERE r.order_id=$1`, orderID).Scan(&refundID, &paymentIntent); err != nil {
		t.Fatal(err)
	}
	pf.provider.SetNextRefundOutcome("succeeded", false)
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("refund worker worked=%v err=%v", worked, err)
	}
	providerRefund, ok := pf.provider.RefundByBusinessID(refundID)
	if !ok || providerRefund.PaymentIntentID != paymentIntent || providerRefund.AmountJPY != 330 || providerRefund.Status != "succeeded" {
		t.Fatalf("provider refund=%+v payment intent=%q found=%v", providerRefund, paymentIntent, ok)
	}
	var status string
	if err = f.Pool.QueryRow(ctx, `SELECT status FROM operation_refunds WHERE refund_id=$1`, refundID).Scan(&status); err != nil || status != "SUCCEEDED" {
		t.Fatalf("local refund status=%q err=%v", status, err)
	}
	finished := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	if finished["payment_status"] != "PAID" || finished["fulfillment_status"] != "PREPARING" || finished["review_decision"] != "APPROVED" || finished["effective_total_gross_jpy"] != "330" {
		t.Fatalf("post-refund order state=%v", finished)
	}
	finishedStops := finished["cancellation_requests"].([]any)
	if finishedStops[0].(map[string]any)["status"] != "FINALIZED" {
		t.Fatalf("cancellation state after refund=%v", finishedStops)
	}
	secondBody := fmt.Sprintf(`{"expected_version":%d,"reason":"cancel the last remaining tea","lines":[{"line_index":0,"cancelled_quantity":1}]}`, int64JSON(finished["version"]))
	secondRequest := request(http.MethodPost, stopPath, secondBody, pf.creds.Manager.Token, "stop-stripe-request-last")
	if secondRequest.Code != http.StatusOK {
		t.Fatalf("last item request=%d %s", secondRequest.Code, secondRequest.Body.String())
	}
	var last map[string]any
	if err = json.Unmarshal(secondRequest.Body.Bytes(), &last); err != nil {
		t.Fatal(err)
	}
	lastEntry := last["cancellation_requests"].([]any)[1].(map[string]any)
	lastConfirm := request(http.MethodPost, stopPath+"/"+lastEntry["id"].(string)+"/confirm", fmt.Sprintf(`{"expected_version":%d,"expected_request_version":%d}`, int64JSON(last["version"]), int64JSON(lastEntry["version"])), pf.creds.Kitchen.Token, "stop-stripe-confirm-last")
	if lastConfirm.Code != http.StatusOK {
		t.Fatalf("last item confirm=%d %s", lastConfirm.Code, lastConfirm.Body.String())
	}
	var lastConfirmed map[string]any
	if err = json.Unmarshal(lastConfirm.Body.Bytes(), &lastConfirmed); err != nil {
		t.Fatal(err)
	}
	lastEntry = lastConfirmed["cancellation_requests"].([]any)[1].(map[string]any)
	lastFinal := request(http.MethodPost, stopPath+"/"+lastEntry["id"].(string)+"/finalize", fmt.Sprintf(`{"expected_version":%d,"expected_request_version":%d,"non_delivery_confirmed":true}`, int64JSON(lastConfirmed["version"]), int64JSON(lastEntry["version"])), pf.creds.Manager.Token, "stop-stripe-finalize-last")
	if lastFinal.Code != http.StatusOK || !strings.Contains(lastFinal.Body.String(), `"fulfillment_status":"CANCELLED"`) {
		t.Fatalf("last item finalize=%d %s", lastFinal.Code, lastFinal.Body.String())
	}
	pf.provider.SetNextRefundOutcome("succeeded", false)
	if worked, err := pf.service.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("last refund worker worked=%v err=%v", worked, err)
	}
	closedState := readP82HTTPOrder(t, pf, grant.Session.ID, orderID)
	if closedState["payment_status"] != "REFUNDED" || closedState["fulfillment_status"] != "CANCELLED" || closedState["effective_total_gross_jpy"] != "0" {
		t.Fatalf("approved order did not resolve after last item stop: %v", closedState)
	}
}
