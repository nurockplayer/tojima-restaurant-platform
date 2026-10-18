package payments

import (
	"errors"
	"strings"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
)

func TestCompanyHoldActorAuditLimitPostgres(t *testing.T) {
	for _, character := range []string{"a", "界"} {
		t.Run(character, func(t *testing.T) {
			h := newHoldFixture(t)
			order, _ := h.commit(h.Locator, "teishoku", ordering.Stripe)
			// Include the existing CREATE work/event and full attempt in the
			// snapshot so a rejected hold cannot quietly change payment state.
			snapshot := func() string {
				t.Helper()
				var state string
				if err := h.Pool.QueryRow(h.ctx, `SELECT jsonb_build_object(
					'holds',(SELECT jsonb_agg(x) FROM company_product_emergency_holds x),
					'audit',(SELECT jsonb_agg(x ORDER BY audit_id) FROM menu_admin_audit x),
					'work',(SELECT jsonb_agg(x ORDER BY work_id) FROM stripe_payment_work x),
					'events',(SELECT jsonb_agg(x ORDER BY event_key) FROM stripe_payment_attempt_events x),
					'attempts',(SELECT jsonb_agg(x ORDER BY order_id) FROM stripe_payment_attempts x)
				)::text`).Scan(&state); err != nil {
					t.Fatal(err)
				}
				return state
			}
			before := snapshot()
			if _, err := h.Menus.SetCompanyProductEmergencyHold(h.ctx, h.TenantID, "teishoku", strings.Repeat(character, 201), true, "safety review", 0, "actor-limit"); !errors.Is(err, catalog.ErrInvalid) {
				t.Fatalf("201-character actor: got %v, want catalog.ErrInvalid", err)
			}
			if after := snapshot(); after != before {
				t.Fatal("rejected actor changed hold, audit, work, events, or payment attempt")
			}

			actor := strings.Repeat(character, 200)
			hold, err := h.Menus.SetCompanyProductEmergencyHold(h.ctx, h.TenantID, "teishoku", actor, true, "safety review", 0, "actor-limit")
			if err != nil || !hold.Active || hold.Version != 1 || hold.ActivatedBy != actor {
				t.Fatalf("200-character actor: hold=%+v err=%v", hold, err)
			}
			var eventActor, auditActor string
			var workCount int
			if err = h.Pool.QueryRow(h.ctx, `SELECT actor_id FROM stripe_payment_attempt_events WHERE order_id=$1 AND event_key=$2`, order.Order.ID, "company-hold-expire:"+order.Order.ID).Scan(&eventActor); err != nil {
				t.Fatal(err)
			}
			if err = h.Pool.QueryRow(h.ctx, `SELECT actor FROM menu_admin_audit WHERE tenant_id=$1 AND request_key='actor-limit'`, h.TenantID).Scan(&auditActor); err != nil {
				t.Fatal(err)
			}
			if err = h.Pool.QueryRow(h.ctx, `SELECT count(*) FROM stripe_payment_work WHERE order_id=$1 AND work_type='EXPIRE'`, order.Order.ID).Scan(&workCount); err != nil {
				t.Fatal(err)
			}
			if eventActor != actor || auditActor != actor || workCount != 1 {
				t.Fatalf("actor was altered or expiry work missing: event=%q audit=%q work=%d", eventActor, auditActor, workCount)
			}
		})
	}
}
