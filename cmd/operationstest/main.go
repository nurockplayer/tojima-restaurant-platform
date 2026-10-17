// Command operationstest starts an isolated, disposable P79 local acceptance
// runtime. It is intentionally not wired into the production command.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/httpapi"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testoperations"
)

type credentialDocument struct {
	BaseURL              string                       `json:"base_url"`
	StaffURL             string                       `json:"staff_url"`
	KitchenURL           string                       `json:"kitchen_url"`
	CatalogURL           string                       `json:"catalog_url,omitempty"`
	GuestURL             string                       `json:"guest_url"`
	TenantID             string                       `json:"tenant_id"`
	StoreID              string                       `json:"store_id"`
	TableID              string                       `json:"table_id"`
	TableLocator         string                       `json:"table_locator"`
	Schema               string                       `json:"schema"`
	Credentials          testoperations.CredentialSet `json:"credentials"`
	PaymentFixture       bool                         `json:"payment_fixture,omitempty"`
	WebhookFixtureSecret string                       `json:"webhook_fixture_secret,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	credentialsPath := flag.String("credentials", "", "new owner-only credentials output file (required)")
	webRoot := flag.String("web-dist", "web/dist", "built staff/guest SPA directory")
	withPayments := flag.Bool("payments-fixture", false, "enable isolated keyless provider fixture; no Stripe network or real payment")
	catalogFixture := flag.Bool("catalog-fixture", false, "enable isolated catalog admin routes and local test credentials")
	demoShironagasukujira := flag.Bool("demo-shironagasukujira", false, "seed the isolated Shironagasukujira reference-data demo")
	flag.Parse()
	if err := validateFixtureMode(*demoShironagasukujira, *withPayments); err != nil {
		return err
	}
	if strings.TrimSpace(*credentialsPath) == "" {
		return errors.New("-credentials is required")
	}
	if err := validateWebRoot(*webRoot); err != nil {
		return err
	}
	if *demoShironagasukujira {
		if err := validateDemoWebRoot(*webRoot); err != nil {
			return err
		}
	}
	if err := validateCredentialsOutsideWebRoot(*credentialsPath, *webRoot); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fixture, err := testguest.NewWithOptions(ctx, os.Getenv("TEST_DATABASE_URL"), testguest.Options{
		CashEnabled: true, CashMode: "PAY_LATER", DemoShironagasukujira: *demoShironagasukujira,
	})
	if err != nil {
		return fmt.Errorf("create isolated database fixture: %w", err)
	}
	defer fixture.Close(context.Background())
	var paymentService *payments.Service
	var proofs *checkoutproof.Service
	var webhookSecret string
	if *withPayments {
		paymentService, proofs, webhookSecret, err = configurePaymentFixture(ctx, fixture)
		if err != nil {
			return fmt.Errorf("configure isolated payment fixture: %w", err)
		}
	}

	useCatalogFixture := catalogFixtureEnabled(*catalogFixture, *demoShironagasukujira)
	newCredentials := testoperations.NewCredentialSet
	if useCatalogFixture {
		newCredentials = testoperations.NewCatalogCredentialSet
	}
	identity, directory, credentials, err := newCredentials(fixture.TenantID, fixture.StoreID)
	if err != nil {
		return err
	}
	doc := buildCredentialDocument(fixture, credentials, useCatalogFixture, *demoShironagasukujira, *withPayments, webhookSecret)
	ownedFile, err := writeCredentials(*credentialsPath, doc)
	if err != nil {
		return err
	}
	defer ownedFile.removeIfOwned()

	runtime := httpapi.NewOperationsRuntime(fixture.Sessions, fixture.Menus, fixture.Orders, fixture.Operations,
		identity, authz.PolicyAuthorizer{Directory: directory})
	if *withPayments {
		runtime, err = httpapi.NewPaymentsRuntime(fixture.Sessions, fixture.Menus, fixture.Orders, fixture.Operations,
			paymentService, identity, authz.PolicyAuthorizer{Directory: directory}, httpapi.PaymentsHTTPConfig{
				Proofs: proofs, Origin: doc.BaseURL, AllowLoopbackHTTP: true, WebhookSecrets: []string{webhookSecret},
			})
		if err != nil {
			return fmt.Errorf("configure payment HTTP fixture: %w", err)
		}
	}
	api := http.NewServeMux()
	if useCatalogFixture {
		api.Handle("/v1/admin/", httpapi.NewMenuHandler(fixture.Menus, identity, authz.PolicyAuthorizer{Directory: directory}))
	}
	if *demoShironagasukujira {
		api.Handle("/v1/", rejectDemoStripeCommit(runtime))
	} else {
		api.Handle("/v1/", runtime)
	}
	api.Handle("/healthz", httpapi.NewHandler())
	if *demoShironagasukujira {
		api.Handle("/", demoSPAHandler(*webRoot))
	} else {
		api.Handle("/", spaHandler(*webRoot))
	}
	server := &http.Server{
		Addr:              "127.0.0.1:8082",
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on loopback 127.0.0.1:8082: %w", err)
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	if *withPayments {
		defer runPaymentFixtureWorker(ctx, paymentService)()
		fmt.Fprintln(os.Stdout, "keyless payment fixture enabled; reserved .invalid Checkout URLs cannot accept payments")
	}
	fmt.Fprintln(os.Stdout, "isolated operations test runtime listening on http://127.0.0.1:8082; credentials are in the requested 0600 file")
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown local runtime: %w", err)
		}
		if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func validateFixtureMode(demoShironagasukujira, paymentsFixture bool) error {
	if demoShironagasukujira && paymentsFixture {
		return errors.New("-demo-shironagasukujira cannot be combined with -payments-fixture")
	}
	return nil
}

func catalogFixtureEnabled(catalogFixture, demoShironagasukujira bool) bool {
	return catalogFixture || demoShironagasukujira
}

func buildCredentialDocument(fixture *testguest.Fixture, credentials testoperations.CredentialSet, catalogFixture, demoShironagasukujira, paymentFixture bool, webhookSecret string) credentialDocument {
	baseURL := "http://127.0.0.1:8082"
	doc := credentialDocument{
		BaseURL: baseURL, StaffURL: baseURL + "/staff", KitchenURL: baseURL + "/kitchen", GuestURL: baseURL + "/",
		TenantID: fixture.TenantID, StoreID: fixture.StoreID, TableID: fixture.TableID,
		TableLocator: fixture.Locator, Schema: fixture.Schema, Credentials: credentials,
		PaymentFixture: paymentFixture, WebhookFixtureSecret: webhookSecret,
	}
	if catalogFixture {
		doc.CatalogURL = baseURL + "/catalog"
	}
	if demoShironagasukujira {
		doc.GuestURL = baseURL + "/?table=" + url.QueryEscape(fixture.Locator)
	}
	return doc
}

func rejectDemoStripeCommit(next http.Handler) http.Handler {
	type commitProbe struct {
		PaymentMethod string `json:"payment_method"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/guest/sessions/") || !strings.HasSuffix(r.URL.Path, "/cart/commit") {
			next.ServeHTTP(w, r)
			return
		}
		const maxProbeBytes = 16 << 10
		body, err := io.ReadAll(io.LimitReader(r.Body, maxProbeBytes+1))
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		if err == nil && len(body) <= maxProbeBytes {
			var probe commitProbe
			if json.Unmarshal(body, &probe) == nil && probe.PaymentMethod == "STRIPE" {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Referrer-Policy", "no-referrer")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "payment_unavailable"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type ownedCredentialsFile struct {
	path string
	info os.FileInfo
}

func writeCredentials(path string, doc credentialDocument) (*ownedCredentialsFile, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create credentials file without replacing an existing path: %w", err)
	}
	owned := &ownedCredentialsFile{path: path}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			owned.removeIfOwned()
		}
	}()
	owned.info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0o600); err != nil {
		return nil, err
	}
	if err = json.NewEncoder(file).Encode(doc); err != nil {
		return nil, err
	}
	if err = file.Sync(); err != nil {
		return nil, err
	}
	if owned.info.Mode().Perm() != 0o600 {
		return nil, errors.New("credentials file permissions are not 0600")
	}
	ok = true
	return owned, nil
}

func (f *ownedCredentialsFile) removeIfOwned() {
	if f == nil || f.info == nil {
		return
	}
	current, err := os.Lstat(f.path)
	if err == nil && os.SameFile(f.info, current) {
		_ = os.Remove(f.path)
	}
}

func validateWebRoot(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("built web directory unavailable: %w", err)
	}
	if !info.IsDir() {
		return errors.New("-web-dist must be a directory")
	}
	if _, err := os.Stat(filepath.Join(path, "index.html")); err != nil {
		return fmt.Errorf("built SPA index unavailable: %w", err)
	}
	return nil
}

func validateDemoWebRoot(path string) error {
	index, err := os.ReadFile(filepath.Join(path, "index.html"))
	if err != nil {
		return fmt.Errorf("demo SPA index unavailable: %w", err)
	}
	if _, err = injectDemoMarker(index); err != nil {
		return fmt.Errorf("demo SPA index cannot be safely gated: %w", err)
	}
	return nil
}

func validateCredentialsOutsideWebRoot(credentialsPath, webRoot string) error {
	root, err := filepath.Abs(webRoot)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	credential, err := filepath.Abs(credentialsPath)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(credential))
	if err != nil {
		return fmt.Errorf("credentials parent directory must already exist: %w", err)
	}
	credential = filepath.Join(parent, filepath.Base(credential))
	rel, err := filepath.Rel(root, credential)
	if err != nil {
		return err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
		return errors.New("credentials file must be outside the served web directory")
	}
	return nil
}

func spaHandler(root string) http.Handler {
	return newSPAHandler(root, false)
}

func demoSPAHandler(root string) http.Handler {
	return newSPAHandler(root, true)
}

func newSPAHandler(root string, demo bool) http.Handler {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return http.NotFoundHandler()
	}
	absRoot, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Clean("/" + r.URL.Path)
		candidate := filepath.Join(absRoot, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
		if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
			if rel, relErr := filepath.Rel(absRoot, resolved); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				if info, statErr := os.Stat(resolved); statErr == nil && !info.IsDir() {
					serveSPAFile(w, r, resolved, demo)
					return
				}
			}
		}
		index := filepath.Join(absRoot, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.NotFound(w, r)
			return
		}
		serveSPAFile(w, r, index, demo)
	})
}

func serveSPAFile(w http.ResponseWriter, r *http.Request, path string, demo bool) {
	if !demo || !strings.EqualFold(filepath.Ext(path), ".html") {
		http.ServeFile(w, r, path)
		return
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "demo page unavailable", http.StatusInternalServerError)
		return
	}
	contents, err = injectDemoMarker(contents)
	if err != nil {
		http.Error(w, "demo page cannot be safely gated", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(contents)
	}
}

func injectDemoMarker(contents []byte) ([]byte, error) {
	const rootTag = `<div id="root">`
	const markedRoot = `<div id="root" data-demo="shironagasukujira">`
	if bytes.Count(contents, []byte(`id="root"`)) != 1 {
		return nil, errors.New("missing or duplicate SPA root element")
	}
	if bytes.Count(contents, []byte(markedRoot)) == 1 {
		return append([]byte(nil), contents...), nil
	}
	if bytes.Count(contents, []byte(rootTag)) != 1 {
		return nil, errors.New("SPA root element does not match the pinned template")
	}
	return bytes.Replace(contents, []byte(rootTag), []byte(markedRoot), 1), nil
}
