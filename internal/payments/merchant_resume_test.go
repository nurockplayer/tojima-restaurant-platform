package payments

import (
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

func TestResumeRejectsProviderResultForDifferentAttempt(t *testing.T) {
	now := time.Now().UTC()
	row := resumeRow{tenant: "tenant", store: "store", account: "acct_test", checkout: "cs_existing", attempt: "original", amount: 330}
	result := stripeprovider.CheckoutResult{
		AccountID: row.account, SessionID: row.checkout, AmountJPY: row.amount,
		Status: stripeprovider.CheckoutOpen, PaymentStatus: stripeprovider.PaymentUnpaid,
		Currency: stripeprovider.CurrencyJPY, ExpiresAt: now.Add(time.Minute), URL: "https://checkout.example.invalid/existing",
		Metadata: map[string]string{"tenant_id": row.tenant, "store_id": row.store, "order_id": "order", "attempt_id": row.attempt},
	}
	if !validResumeResult(result, row, "order", now) {
		t.Fatal("existing open checkout unexpectedly denied")
	}
	result.Metadata["attempt_id"] = "different"
	if validResumeResult(result, row, "order", now) {
		t.Fatal("provider result for another attempt exposed a continuation URL")
	}
}
