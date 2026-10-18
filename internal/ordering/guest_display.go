package ordering

// GuestDisplayState is a safe projection, never an operations reason or a
// payment transition. Refund success is conveyed only by GuestRefund.Status,
// never inferred from payment or fulfillment labels.
func GuestDisplayState(payment, fulfillment string, review, kdsCardExists bool) string {
	switch payment {
	case "UNPAID", "PENDING", "PAID", "UNKNOWN", "REFUND_PENDING", "REFUNDED", "VOIDED":
	default:
		return "UNKNOWN"
	}
	if payment == "VOIDED" || fulfillment == "CANCELLED" {
		return "CANCELLED"
	}
	if payment == "UNKNOWN" || payment == "REFUND_PENDING" || payment == "REFUNDED" {
		return "UNKNOWN"
	}
	switch fulfillment {
	case "WAITING", "ACCEPTED", "PREPARING", "READY", "SERVED":
	default:
		return "UNKNOWN"
	}
	if payment == "PAID" && review && !kdsCardExists {
		return "PAID_UNDER_REVIEW"
	}
	return "STANDARD"
}

// GuestOrderDetails contains only customer-visible order and refund facts. It is
// shared by the capability read and the in-session history, not staff DTOs.
type GuestOrderDetails struct {
	TotalGrossJPY          int64                `json:"total_gross_jpy"`
	EffectiveTotalGrossJPY int64                `json:"effective_total_gross_jpy"`
	EffectiveLines         []GuestEffectiveLine `json:"effective_lines"`
	Adjusted               bool                 `json:"adjusted"`
	Refunds                []GuestRefund        `json:"refunds"`
	RefundedTotalJPY       int64                `json:"refunded_total_jpy"`
}

type GuestEffectiveLine struct {
	ProductName  string   `json:"product_name"`
	OptionNames  []string `json:"option_names"`
	Quantity     int64    `json:"quantity"`
	LineTotalJPY int64    `json:"line_total_jpy"`
}

type GuestRefund struct {
	Status    string `json:"status"`
	AmountJPY int64  `json:"amount_jpy"`
	Method    string `json:"method"`
}

// GuestOrderDetailsSQL projects GuestOrderDetails from table_orders o and
// operation_order_state st in the caller's authorized SELECT. C1's persisted
// total already includes server-side tax recomputation. Gross line amounts use
// committed prices (including options) and finalized cancelled quantities.
// C2 request rows are deliberately absent. Build refund objects explicitly so
// future staff/provider columns can never enter either guest response.
const GuestOrderDetailsSQL = `jsonb_build_object(
  'total_gross_jpy',o.total_gross_jpy,
  'effective_total_gross_jpy',coalesce(st.effective_total_gross_jpy,o.total_gross_jpy),
  'adjusted',st.effective_total_gross_jpy IS NOT NULL,
  'effective_lines',coalesce((SELECT jsonb_agg(jsonb_build_object(
    'product_name',l.value->>'name',
    'option_names',coalesce((SELECT jsonb_agg(opt.value->>'name' ORDER BY opt.ordinality)
      FROM jsonb_array_elements(l.value->'options') WITH ORDINALITY opt),'[]'::jsonb),
    'quantity',(l.value->>'quantity')::bigint-coalesce((st.cancelled_quantities->>(l.ordinality::int-1))::bigint,0),
    'line_total_jpy',((l.value->>'base_gross_jpy')::numeric+coalesce((SELECT sum((opt->>'price_gross_jpy')::numeric)
      FROM jsonb_array_elements(l.value->'options') opt),0)) *
      ((l.value->>'quantity')::bigint-coalesce((st.cancelled_quantities->>(l.ordinality::int-1))::bigint,0)))
    ORDER BY l.ordinality) FROM jsonb_array_elements(o.quote_snapshot->'quote'->'lines') WITH ORDINALITY l),'[]'::jsonb),
  'refunds',coalesce((SELECT jsonb_agg(jsonb_build_object('status',r.status,'amount_jpy',r.amount_jpy,'method',r.method)
    ORDER BY r.created_at,r.refund_id) FROM operation_refunds r WHERE r.order_id=o.order_id),'[]'::jsonb),
  'refunded_total_jpy',coalesce((SELECT sum(r.amount_jpy) FROM operation_refunds r WHERE r.order_id=o.order_id AND r.status='SUCCEEDED'),0))`
