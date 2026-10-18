package stripeprovider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type p84ErrorTransport struct {
	message string
	network bool
	calls   int
}

func (tr *p84ErrorTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.calls++
	if tr.network {
		return nil, errors.New(tr.message)
	}
	return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"api_error","message":"` + tr.message + `"}}`)), Request: r}, nil
}

func TestP84ReturnedProviderErrorRedactsSyntheticSecrets(t *testing.T) {
	markers := []string{testKey, "whsec_p84SyntheticOnly", "https://checkout.stripe.com/c/pay/cs_p84Private", "p84_private_provider_detail"}
	for _, network := range []bool{false, true} {
		name := "HTTP 500 body"
		if network {
			name = "transport error"
		}
		t.Run(name, func(t *testing.T) {
			transport := &p84ErrorTransport{message: strings.Join(markers, " "), network: network}
			p, err := New(testConfig(&http.Client{Timeout: time.Second, Transport: transport}))
			if err != nil {
				t.Fatal(err)
			}
			result, returnedErr := p.CreateCheckout(context.Background(), CreateCheckoutRequest{
				Account:  AccountContext{Mode: ModeConnectedDirectFixture, StripeAccountID: testAccount},
				TenantID: "tenant", StoreID: "store", OrderID: "order", AttemptID: "attempt",
				IdempotencyKey: "p84-redaction", AmountJPY: 330, Description: "Order",
			})
			if !errors.Is(returnedErr, ErrOutcomeUnknown) || transport.calls != 1 || result.URL != "" {
				t.Fatalf("ambiguous provider result: err=%v calls=%d URL=%q", returnedErr, transport.calls, result.URL)
			}
			// Inspect the error actually returned, including any wrapper text.
			for _, marker := range markers {
				if strings.Contains(returnedErr.Error(), marker) {
					t.Fatalf("returned provider error leaked synthetic marker %q: %v", marker, returnedErr)
				}
			}
		})
	}
}
