package stripeprovider

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

// ReadWebhookBody performs a bounded read for the HTTP ingress before
// signature verification. It reads at most WebhookMaxBytes+1 bytes so an
// oversized body is detected without an unbounded allocation.
func ReadWebhookBody(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, ErrWebhook
	}
	body, err := io.ReadAll(io.LimitReader(r, WebhookMaxBytes+1))
	if err != nil || len(body) == 0 || len(body) > WebhookMaxBytes {
		return nil, ErrWebhook
	}
	return body, nil
}

// VerifyWebhook authenticates the exact raw bytes before parsing object data.
// Active secrets may contain the current and previous endpoint secret during
// a controlled rotation. It enforces a symmetric timestamp window because the
// Stripe SDK's default tolerance only rejects old timestamps.
func VerifyWebhook(raw []byte, signature string, activeSecrets []string, now time.Time) (VerifiedEvent, error) {
	if len(raw) == 0 || len(raw) > WebhookMaxBytes || len(activeSecrets) == 0 || strings.TrimSpace(signature) == "" || now.IsZero() {
		return VerifiedEvent{}, ErrWebhook
	}
	timestamp, ok := signatureTimestamp(signature)
	if !ok {
		return VerifiedEvent{}, ErrWebhook
	}
	when := time.Unix(timestamp, 0)
	delta := now.Sub(when)
	if delta > WebhookTolerance || delta < -WebhookTolerance {
		return VerifiedEvent{}, ErrWebhook
	}
	var event stripe.Event
	verified := false
	for _, secret := range activeSecrets {
		if strings.TrimSpace(secret) == "" {
			continue
		}
		// Tolerance was enforced against the injected clock above; avoid the
		// SDK's additional wall-clock check while retaining signature verification.
		candidate, err := stripe.ConstructEvent(raw, signature, secret, stripe.WithIgnoreTolerance())
		if err == nil {
			event = candidate
			verified = true
			break
		}
	}
	if !verified || event.ID == "" || string(event.Type) == "" || event.APIVersion != APIVersion || event.Livemode {
		return VerifiedEvent{}, ErrWebhook
	}
	out := VerifiedEvent{
		ID:         event.ID,
		Type:       string(event.Type),
		AccountID:  event.Account,
		APIVersion: event.APIVersion,
		CreatedAt:  time.Unix(event.Created, 0).UTC(),
		LiveMode:   event.Livemode,
	}
	if event.Data == nil || len(event.Data.Raw) == 0 {
		return VerifiedEvent{}, ErrWebhook
	}
	switch {
	case strings.HasPrefix(out.Type, "checkout.session."):
		var s stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &s); err != nil {
			return VerifiedEvent{}, ErrWebhook
		}
		checkout, err := checkoutResult(&s, event.Account)
		if err != nil {
			return VerifiedEvent{}, ErrWebhook
		}
		// A webhook is only a state signal. Checkout URLs belong solely to the
		// private winning-request response and are not propagated to handlers.
		checkout.URL = ""
		out.ObjectID = checkout.SessionID
		out.Checkout = &checkout
	case strings.HasPrefix(out.Type, "payment_intent."):
		var pi stripe.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			return VerifiedEvent{}, ErrWebhook
		}
		result, err := paymentIntentResult(&pi, event.Account)
		if err != nil {
			return VerifiedEvent{}, ErrWebhook
		}
		out.ObjectID = result.ID
		out.PaymentIntent = &result
	case out.Type == "charge.refunded":
		var charge stripe.Charge
		if err := json.Unmarshal(event.Data.Raw, &charge); err != nil || !validStripeObjectID(charge.ID, "ch") || charge.Livemode || charge.Refunds == nil {
			return VerifiedEvent{}, ErrWebhook
		}
		piID := ""
		if charge.PaymentIntent != nil {
			piID = charge.PaymentIntent.ID
		}
		if !validStripeObjectID(piID, "pi") {
			return VerifiedEvent{}, ErrWebhook
		}
		out.ObjectID = charge.ID
		out.RefundsHasMore = charge.Refunds.HasMore
		for _, refund := range charge.Refunds.Data {
			result, err := refundResult(refund, event.Account)
			if err != nil {
				return VerifiedEvent{}, ErrWebhook
			}
			if result.PaymentIntentID == "" {
				result.PaymentIntentID = piID
			}
			if result.PaymentIntentID != piID {
				return VerifiedEvent{}, ErrWebhook
			}
			out.Refunds = append(out.Refunds, result)
		}
		if len(out.Refunds) == 0 {
			return VerifiedEvent{}, ErrWebhook
		}
	case out.Type == "refund.updated" || out.Type == "refund.failed":
		var refund stripe.Refund
		if err := json.Unmarshal(event.Data.Raw, &refund); err != nil {
			return VerifiedEvent{}, ErrWebhook
		}
		result, err := refundResult(&refund, event.Account)
		if err != nil || result.PaymentIntentID == "" {
			return VerifiedEvent{}, ErrWebhook
		}
		out.ObjectID = result.ID
		out.Refund = &result
	default:
		return VerifiedEvent{}, ErrWebhook
	}
	return out, nil
}

func signatureTimestamp(header string) (int64, bool) {
	var found bool
	var ts int64
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || key != "t" {
			continue
		}
		if found {
			return 0, false
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, false
		}
		ts, found = parsed, true
	}
	return ts, found
}
