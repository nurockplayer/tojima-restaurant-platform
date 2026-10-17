package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testoperations"
)

func TestPaymentsRuntimeKeepsExistingStaffSessionAndOperationsRoutesReachable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)

	pay, err := payments.New(f.Pool, f.Operations, nil, payments.Config{CreateRetryWindow: time.Hour, ContinuationTTL: time.Minute, WorkLeaseTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	if _, err = rand.Read(pepper); err != nil {
		t.Fatal(err)
	}
	f.Sessions, err = tablesession.New(f.Pool, tablesession.Options{SessionTTL: 4 * time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 1000, PINTTL: 15 * time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: pepper}, pay)
	if err != nil {
		t.Fatal(err)
	}
	f.Orders, err = ordering.NewWithOrderPolicy(f.Pool, f.Sessions, f.Menus, pay)
	if err != nil {
		t.Fatal(err)
	}
	signingKey := make([]byte, 32)
	if _, err = rand.Read(signingKey); err != nil {
		t.Fatal(err)
	}
	proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: [][]byte{signingKey}, TTL: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	identity, directory, creds, err := testoperations.NewCredentialSet("test-tenant", "test-store")
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewPaymentsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, pay, identity, authz.PolicyAuthorizer{Directory: directory}, PaymentsHTTPConfig{
		Proofs: proofs, Origin: "http://127.0.0.1:8082", AllowLoopbackHTTP: true, WebhookSecrets: []string{"whsec_fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:8082"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	base := "/v1/staff/tenants/test-tenant/stores/test-store"
	opened := request(http.MethodPost, base+"/tables/table-01/sessions", "{}", creds.Cashier.Token)
	if opened.Code != http.StatusOK {
		t.Fatalf("staff open route status=%d body=%s", opened.Code, opened.Body.String())
	}
	var openResult map[string]any
	if err = json.Unmarshal(opened.Body.Bytes(), &openResult); err != nil {
		t.Fatal(err)
	}
	session := openResult["session"].(map[string]any)
	if session["status"] != "OPEN" {
		t.Fatalf("staff open returned unexpected session: %v", session)
	}
	sessionID := session["id"].(string)
	closed := request(http.MethodPost, base+"/tables/table-01/sessions/"+sessionID+"/close", "{}", creds.Cashier.Token)
	if closed.Code != http.StatusOK {
		t.Fatalf("staff close route status=%d body=%s", closed.Code, closed.Body.String())
	}
	tables := request(http.MethodGet, base+"/operations/tables", "", creds.Cashier.Token)
	if tables.Code != http.StatusOK {
		t.Fatalf("operations route status=%d body=%s", tables.Code, tables.Body.String())
	}
	if got := opened.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("staff session response missing no-store: %q", got)
	}
}
