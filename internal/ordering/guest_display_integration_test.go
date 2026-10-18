package ordering

import (
	"context"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
)

func TestReadOrderStatusTerminalMatrix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fixture := newOrderingFixture(t, ctx)

	cart, err := fixture.service.Mutate(ctx, Mutation{
		SessionID: fixture.session.ID, GrantToken: fixture.tokens[0], ExpectedGeneration: 1, ExpectedVersion: 1,
		IdempotencyKey: "terminal-matrix-add", Kind: AddLine, ProductKey: "tea", Quantity: 1,
		Options: []catalog.OptionSelection{{GroupKey: "extras", OptionKey: "milk"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := fixture.service.Preview(ctx, fixture.session.ID, fixture.tokens[0], cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := fixture.service.Commit(ctx, CommitRequest{
		SessionID: fixture.session.ID, GrantToken: fixture.tokens[0], ExpectedGeneration: cart.Generation,
		ExpectedVersion: cart.Version, IdempotencyKey: "terminal-matrix-commit", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Cash,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE operation_order_state DISABLE TRIGGER operation_order_state_transition`,
		`ALTER TABLE operation_kds_cards DISABLE TRIGGER operation_kds_card_gate`,
		`ALTER TABLE operation_kds_cards DISABLE TRIGGER operation_kds_cards_immutable`,
	} {
		if _, err = fixture.pool.Exec(ctx, statement); err != nil {
			t.Fatalf("disable scoped fixture trigger for projection matrix: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, statement := range []string{
			`ALTER TABLE operation_order_state ENABLE TRIGGER operation_order_state_transition`,
			`ALTER TABLE operation_kds_cards ENABLE TRIGGER operation_kds_card_gate`,
			`ALTER TABLE operation_kds_cards ENABLE TRIGGER operation_kds_cards_immutable`,
		} {
			if _, restoreErr := fixture.pool.Exec(context.Background(), statement); restoreErr != nil {
				t.Errorf("restore scoped fixture trigger: %v", restoreErr)
			}
		}
	})

	// Seeded snapshots deliberately bypass operation transitions so ReadOrderStatus
	// can be checked across the complete projection and terminal matrix. CHECK
	// constraints stay enabled; payment method and cash mode remain consistent.
	payments := []string{"UNPAID", "PENDING", "PAID", "UNKNOWN", "REFUND_PENDING", "REFUNDED", "VOIDED"}
	fulfillments := []string{"WAITING", "ACCEPTED", "PREPARING", "READY", "SERVED", "CANCELLED"}
	for _, payment := range payments {
		for _, fulfillment := range fulfillments {
			for _, hold := range []bool{false, true} {
				for _, review := range []bool{false, true} {
					for _, card := range []bool{false, true} {
						name := payment + "/" + fulfillment
						if hold {
							name += "/hold"
						}
						if review {
							name += "/review"
						}
						if card {
							name += "/card"
						}
						t.Run(name, func(t *testing.T) {
							if card {
								var exists bool
								if err = fixture.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_kds_cards WHERE order_id=$1)`, commit.Order.ID).Scan(&exists); err != nil {
									t.Fatal(err)
								}
								if !exists {
									if _, err = fixture.pool.Exec(ctx, `UPDATE operation_order_state SET payment_status='PAID',fulfillment_status='WAITING',operational_hold=false,payment_review_required=false,version=version+1 WHERE order_id=$1`, commit.Order.ID); err != nil {
										t.Fatal(err)
									}
									_, err = fixture.pool.Exec(ctx, `INSERT INTO operation_kds_cards(order_id,session_id,tenant_id,store_id,table_id) SELECT order_id,session_id,tenant_id,store_id,table_id FROM operation_order_state WHERE order_id=$1`, commit.Order.ID)
								}
							} else {
								_, err = fixture.pool.Exec(ctx, `DELETE FROM operation_kds_cards WHERE order_id=$1`, commit.Order.ID)
							}
							if err != nil {
								t.Fatalf("set card=%t: %v", card, err)
							}
							method, cashMode := "STRIPE", any(nil)
							if payment == "UNPAID" {
								method, cashMode = "CASH", "PREPAID"
							}
							_, err = fixture.pool.Exec(ctx, `UPDATE operation_order_state SET payment_method=$2,cash_mode=$3,payment_status=$4,fulfillment_status=$5,operational_hold=$6,payment_review_required=$7,version=version+1 WHERE order_id=$1`, commit.Order.ID, method, cashMode, payment, fulfillment, hold, review)
							if err != nil {
								t.Fatalf("set state: %v", err)
							}
							status, readErr := fixture.service.ReadOrderStatus(ctx, commit.Order.ID, commit.OrderReadGrant.Token)
							if readErr != nil {
								t.Fatalf("read: %v", readErr)
							}
							wantTerminal := payment == "PAID" && fulfillment == "SERVED" && !hold && !review
							if status.IsTerminal != wantTerminal {
								t.Errorf("payment=%s fulfillment=%s hold=%t review=%t card=%t: IsTerminal=%t, want %t", payment, fulfillment, hold, review, card, status.IsTerminal, wantTerminal)
							}
							wantDisplay := "STANDARD"
							if payment == "VOIDED" || fulfillment == "CANCELLED" {
								wantDisplay = "CANCELLED"
							} else if payment == "UNKNOWN" || payment == "REFUND_PENDING" || payment == "REFUNDED" {
								wantDisplay = "UNKNOWN"
							} else if payment == "PAID" && review && !card {
								wantDisplay = "PAID_UNDER_REVIEW"
							}
							if status.DisplayState != wantDisplay {
								t.Errorf("DisplayState=%q, want %q", status.DisplayState, wantDisplay)
							}
						})
					}
				}
			}
		}
	}
}
