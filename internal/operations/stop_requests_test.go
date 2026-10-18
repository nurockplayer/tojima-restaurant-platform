package operations

import "testing"

func TestStopRequestLineEligibility(t *testing.T) {
	for _, tc := range []struct {
		name     string
		quantity int64
		requests []CancellationRequest
		want     StopRequestEligibility
	}{
		{"remaining", 1, nil, StopEligible},
		{"no remaining", 0, nil, StopNoRemainingQuantity},
		{"requested", 1, []CancellationRequest{{LineIndex: 1, Status: "REQUESTED"}}, StopPending},
		{"confirmed", 1, []CancellationRequest{{LineIndex: 1, Status: "KITCHEN_CONFIRMED"}}, StopPending},
		{"rejected without reason", 1, []CancellationRequest{{LineIndex: 1, Status: "REJECTED"}}, StopManualException},
		{"withdrawn manual exception", 1, []CancellationRequest{{LineIndex: 1, Status: "WITHDRAWN", ManualExceptionReason: "uncertain"}}, StopManualException},
		{"confirmed manual exception", 1, []CancellationRequest{{LineIndex: 1, Status: "KITCHEN_CONFIRMED", ManualExceptionReason: "uncertain"}}, StopManualException},
		{"withdrawn", 1, []CancellationRequest{{LineIndex: 1, Status: "WITHDRAWN"}}, StopEligible},
		{"partially finalized", 1, []CancellationRequest{{LineIndex: 1, Status: "FINALIZED"}}, StopEligible},
		{"fully finalized", 0, []CancellationRequest{{LineIndex: 1, Status: "FINALIZED"}}, StopNoRemainingQuantity},
		{"another line rejected", 1, []CancellationRequest{{LineIndex: 0, Status: "REJECTED", ManualExceptionReason: "plated"}}, StopEligible},
		{"another line pending", 1, []CancellationRequest{{LineIndex: 0, Status: "REQUESTED"}}, StopEligible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stopRequestEligibility(OrderLine{LineIndex: 1, Quantity: tc.quantity}, tc.requests); got != tc.want {
				t.Fatalf("eligibility=%s want=%s", got, tc.want)
			}
		})
	}
}
