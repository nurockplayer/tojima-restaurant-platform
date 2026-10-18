package ordering

// GuestDisplayState is a safe projection, never an operations reason or a
// payment transition. Unrecognized and refund states await separate display
// support and must not be presented as successful fulfillment.
func GuestDisplayState(payment, fulfillment string, review, kdsCardExists bool) string {
	switch payment {
	case "UNPAID", "PENDING", "PAID":
	default:
		return "UNKNOWN"
	}
	switch fulfillment {
	case "WAITING", "ACCEPTED", "PREPARING", "READY", "SERVED", "CANCELLED":
	default:
		return "UNKNOWN"
	}
	if payment == "PAID" && review && !kdsCardExists {
		return "PAID_UNDER_REVIEW"
	}
	return "STANDARD"
}
