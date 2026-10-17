package operations

import (
	"testing"
	"time"
)

func TestCashInvoiceDateUsesJSTDayAtUTC15Boundary(t *testing.T) {
	before := cashInvoiceDate(time.Date(2026, time.October, 17, 14, 59, 59, 0, time.UTC))
	y, m, d := before.Date()
	if y != 2026 || m != time.October || d != 17 {
		t.Fatalf("one second before JST midnight should remain Oct 17, got %s", before)
	}

	after := cashInvoiceDate(time.Date(2026, time.October, 17, 15, 0, 0, 0, time.UTC))
	y, m, d = after.Date()
	if y != 2026 || m != time.October || d != 18 {
		t.Fatalf("UTC 15:00 should be JST Oct 18, got %s", after)
	}
	_, offset := after.Zone()
	if offset != 9*60*60 {
		t.Fatalf("invoice date timezone offset = %d, want JST +09:00", offset)
	}
}
