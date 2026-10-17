package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

type httpTestClose struct{}

func (httpTestClose) CheckClose(context.Context, pgx.Tx, tablesession.Session) error { return nil }

type httpTestIdentity struct{ actor authz.Actor }

func (i httpTestIdentity) Resolve(context.Context, *http.Request) (authz.Actor, error) {
	return i.actor, nil
}

type httpMemberships struct {
	mu     sync.RWMutex
	values []authz.Membership
}

func (m *httpMemberships) Current(context.Context, string, string) ([]authz.Membership, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]authz.Membership(nil), m.values...), nil
}

func TestSessionHTTPIntegration(t *testing.T) {
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
	schema := "p76_http_" + hex.EncodeToString(random)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, e := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); e != nil {
			t.Errorf("drop owned HTTP test schema: %v", e)
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
	_, err = pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES('t','Tenant');
		INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode) VALUES('t','s','Store','STAFF_OPEN'),('t','direct-store','Direct','DIRECT_SCAN');
		INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES('t','s','one','One','locator-one'),('t','direct-store','table','Direct','locator-direct');`)
	if err != nil {
		t.Fatal(err)
	}
	service, err := tablesession.New(pool, tablesession.Options{SessionTTL: time.Hour, GrantTTL: time.Minute, GrantWindow: time.Minute, GrantWindowMax: 1000, PINTTL: time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: []byte("01234567890123456789012345678901")}, httpTestClose{})
	if err != nil {
		t.Fatal(err)
	}
	memberships := &httpMemberships{values: []authz.Membership{{TenantID: "t", StoreID: "s", Role: authz.Staff}}}
	handler := NewSessionHandler(service, httpTestIdentity{authz.Actor{SubjectID: "staff-subject"}}, authz.PolicyAuthorizer{Directory: memberships})
	status := requestJSON(t, handler, http.MethodGet, "/v1/guest/tables/locator-one", "", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"mode":"STAFF_OPEN"`) {
		t.Fatalf("public status response %d: %s", status.Code, status.Body.String())
	}
	if !strings.Contains(status.Body.String(), `"open":false`) {
		t.Fatalf("initial public status unexpectedly open: %s", status.Body.String())
	}
	var initialCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM table_sessions`).Scan(&initialCount); err != nil || initialCount != 0 {
		t.Fatalf("public GET created a session: count=%d err=%v", initialCount, err)
	}
	if response := requestJSON(t, NewSessionHandler(service, nil, nil), http.MethodPost, "/v1/staff/tenants/t/stores/s/tables/one/sessions", "{}", ""); response.Code != http.StatusForbidden {
		t.Fatalf("default staff identity did not deny: %d %s", response.Code, response.Body.String())
	}
	openedResponse := requestJSON(t, handler, http.MethodPost, "/v1/staff/tenants/t/stores/s/tables/one/sessions", "{}", "X-Subject-ID:tenant-admin")
	if openedResponse.Code != http.StatusOK {
		t.Fatalf("staff open failed: %d %s", openedResponse.Code, openedResponse.Body.String())
	}
	var opened tablesession.OpenResult
	if err = json.Unmarshal(openedResponse.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	grantResponse := requestJSON(t, handler, http.MethodPost, "/v1/guest/sessions/"+opened.Session.ID+"/join", `{"expected_session_id":"wrong"}`, "")
	if grantResponse.Code != http.StatusForbidden {
		t.Fatalf("stale expected session was not rejected: %d", grantResponse.Code)
	}
	grantResponse = requestJSON(t, handler, http.MethodPost, "/v1/guest/sessions/"+opened.Session.ID+"/join", `{}`, "")
	if grantResponse.Code != http.StatusOK {
		t.Fatalf("guest join failed: %d %s", grantResponse.Code, grantResponse.Body.String())
	}
	var grant tablesession.Grant
	if err = json.Unmarshal(grantResponse.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	read := requestJSON(t, handler, http.MethodGet, "/v1/guest/sessions/"+opened.Session.ID, "", "Authorization: Bearer "+grant.Token)
	if read.Code != http.StatusOK {
		t.Fatalf("guest read failed: %d %s", read.Code, read.Body.String())
	}
	memberships.mu.Lock()
	memberships.values = nil
	memberships.mu.Unlock()
	closePath := "/v1/staff/tenants/t/stores/s/tables/one/sessions/" + opened.Session.ID + "/close"
	response := requestJSON(t, handler, http.MethodPost, closePath, "{}", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("revoked current membership was accepted: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(read.Body.String(), grant.Token) {
		t.Fatal("guest session read exposed bearer token")
	}
	if _, err = service.Read(ctx, opened.Session.ID, "invalid"); !errors.Is(err, tablesession.ErrDenied) {
		t.Fatalf("invalid bearer accepted: %v", err)
	}
	directResponse := requestJSON(t, handler, http.MethodPost, "/v1/guest/tables/locator-direct/join", `{}`, "")
	if directResponse.Code != http.StatusOK {
		t.Fatalf("direct API join failed: %d %s", directResponse.Code, directResponse.Body.String())
	}
	var firstDirect tablesession.Grant
	if err = json.Unmarshal(directResponse.Body.Bytes(), &firstDirect); err != nil {
		t.Fatal(err)
	}
	if !firstDirect.Session.RequiresStaffAcceptance {
		t.Fatal("direct API omitted staff acceptance gate")
	}
	status = requestJSON(t, handler, http.MethodGet, "/v1/guest/tables/locator-direct", "", "")
	var current tablesession.PublicTable
	if err = json.Unmarshal(status.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.CurrentSessionID != firstDirect.Session.ID {
		t.Fatalf("public locator reported wrong active session: %+v", current)
	}
	rejoin := requestJSON(t, handler, http.MethodPost, "/v1/guest/tables/locator-direct/join", `{"expected_session_id":"`+current.CurrentSessionID+`"}`, "")
	if rejoin.Code != http.StatusOK {
		t.Fatalf("direct client did not join expected active session: %d %s", rejoin.Code, rejoin.Body.String())
	}
	if err = service.Close(ctx, tablesession.Scope{TenantID: "t", StoreID: "direct-store", TableID: "table"}, firstDirect.Session.ID, "staff"); err != nil {
		t.Fatal(err)
	}
	nextDirect := requestJSON(t, handler, http.MethodPost, "/v1/guest/tables/locator-direct/join", `{}`, "")
	if nextDirect.Code != http.StatusOK {
		t.Fatalf("new direct seating did not open: %d %s", nextDirect.Code, nextDirect.Body.String())
	}
	staleDirect := requestJSON(t, handler, http.MethodPost, "/v1/guest/tables/locator-direct/join", `{"expected_session_id":"`+firstDirect.Session.ID+`"}`, "")
	if staleDirect.Code != http.StatusForbidden {
		t.Fatalf("stale direct scan was rebound: %d %s", staleDirect.Code, staleDirect.Body.String())
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, path, body, header string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if header != "" {
		parts := strings.SplitN(header, ":", 2)
		request.Header.Set(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
