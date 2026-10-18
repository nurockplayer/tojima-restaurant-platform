package ordering

import "testing"

func TestGuestDisplayState(t *testing.T) {
	for _, tc := range []struct {
		payment, fulfillment string
		review, card         bool
		want                 string
	}{
		{"PAID", "WAITING", true, false, "PAID_UNDER_REVIEW"},
		{"PAID", "PREPARING", true, false, "PAID_UNDER_REVIEW"},
		{"PAID", "ACCEPTED", false, true, "STANDARD"},
		{"PAID", "PREPARING", true, true, "STANDARD"},
		{"PAID", "WAITING", false, false, "STANDARD"},
		{"UNPAID", "WAITING", true, false, "STANDARD"},
		{"PENDING", "WAITING", false, false, "STANDARD"},
		{"UNKNOWN", "SERVED", false, true, "UNKNOWN"},
		{"REFUND_PENDING", "SERVED", false, true, "UNKNOWN"},
		{"REFUNDED", "SERVED", false, true, "UNKNOWN"},
		{"FUTURE_PAYMENT", "SERVED", false, true, "UNKNOWN"},
		{"PAID", "FUTURE_KITCHEN", false, true, "UNKNOWN"},
	} {
		if got := GuestDisplayState(tc.payment, tc.fulfillment, tc.review, tc.card); got != tc.want {
			t.Errorf("%+v: got %s", tc, got)
		}
	}
}
