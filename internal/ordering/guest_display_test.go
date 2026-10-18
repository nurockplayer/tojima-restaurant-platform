package ordering

import (
	"fmt"
	"testing"
)

func TestGuestDisplayState(t *testing.T) {
	payments := []string{"UNPAID", "PENDING", "PAID", "UNKNOWN", "REFUND_PENDING", "REFUNDED"}
	fulfillments := []string{"WAITING", "ACCEPTED", "PREPARING", "READY", "SERVED", "CANCELLED"}

	for _, payment := range payments {
		for _, fulfillment := range fulfillments {
			for _, review := range []bool{false, true} {
				for _, card := range []bool{false, true} {
					name := fmt.Sprintf("payment=%s/fulfillment=%s/review=%t/card=%t", payment, fulfillment, review, card)
					t.Run(name, func(t *testing.T) {
						want := "STANDARD"
						if fulfillment == "CANCELLED" || payment == "UNKNOWN" || payment == "REFUND_PENDING" || payment == "REFUNDED" {
							want = "UNKNOWN"
						} else if payment == "PAID" && review && !card {
							want = "PAID_UNDER_REVIEW"
						}
						if got := GuestDisplayState(payment, fulfillment, review, card); got != want {
							t.Errorf("GuestDisplayState(%q, %q, %t, %t) = %q, want %q", payment, fulfillment, review, card, got, want)
						}
					})
				}
			}
		}
	}

	for _, tc := range []struct {
		name, payment, fulfillment string
	}{
		{name: "future payment", payment: "FUTURE_PAYMENT", fulfillment: "SERVED"},
		{name: "future fulfillment", payment: "PAID", fulfillment: "FUTURE_FULFILLMENT"},
		{name: "both future", payment: "FUTURE_PAYMENT", fulfillment: "FUTURE_FULFILLMENT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, review := range []bool{false, true} {
				for _, card := range []bool{false, true} {
					if got := GuestDisplayState(tc.payment, tc.fulfillment, review, card); got != "UNKNOWN" {
						t.Errorf("GuestDisplayState(%q, %q, %t, %t) = %q, want UNKNOWN", tc.payment, tc.fulfillment, review, card, got)
					}
				}
			}
		})
	}
}
