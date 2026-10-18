package testpayments

import (
	"context"
	"errors"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

func TestRefundFixtureUnknownRetryAndDelayedFailure(t *testing.T) {
	p := NewProvider()
	req := stripeprovider.CreateRefundRequest{
		Account:  stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_test123"},
		TenantID: "tenant-1", StoreID: "store-1", OrderID: "order-1", RefundID: "refund-1",
		PaymentIntentID: "pi_test123", IdempotencyKey: "refund-key-1", AmountJPY: 500,
	}
	p.SetNextRefundOutcome("pending", true)
	if _, err := p.CreateRefund(context.Background(), req); !errors.Is(err, stripeprovider.ErrOutcomeUnknown) {
		t.Fatalf("create error=%v", err)
	}
	got, err := p.CreateRefund(context.Background(), req)
	if err != nil || got.Status != "pending" || got.AmountJPY != 500 {
		t.Fatalf("retry=%#v err=%v", got, err)
	}
	if changed, err := p.SetRefundOutcome(got.ID, "failed"); err != nil || changed.Status != "failed" {
		t.Fatalf("failure transition=%#v err=%v", changed, err)
	}
	other := req
	other.AmountJPY = 600
	if _, err := p.CreateRefund(context.Background(), other); !errors.Is(err, stripeprovider.ErrInvalid) {
		t.Fatalf("changed idempotent request error=%v", err)
	}
	creates, retrieves := p.RefundOperationCounts()
	if creates != 3 || retrieves != 0 {
		t.Fatalf("counts create=%d retrieve=%d", creates, retrieves)
	}
}

func TestRefundFixtureSucceededThenFailed(t *testing.T) {
	p := NewProvider()
	req := stripeprovider.CreateRefundRequest{
		Account:  stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: "acct_test123"},
		TenantID: "tenant-1", StoreID: "store-1", OrderID: "order-1", RefundID: "refund-1",
		PaymentIntentID: "pi_test123", IdempotencyKey: "refund-key-1", AmountJPY: 500,
	}
	p.SetNextRefundOutcome("succeeded_then_failed", false)
	created, err := p.CreateRefund(context.Background(), req)
	if err != nil || created.Status != "succeeded" {
		t.Fatalf("create=%+v %v", created, err)
	}
	current, err := p.RetrieveRefund(context.Background(), req.Account, created.ID)
	if err != nil || current.Status != "failed" || current.ID != created.ID {
		t.Fatalf("late failure=%+v %v", current, err)
	}
}
