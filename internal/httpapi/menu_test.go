package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
)

func TestSafeMenuHidesInactiveItemsOnlyFromGuests(t *testing.T) {
	menu := catalog.PublishedMenu{Mode: "LINKED", Revision: 4, CatalogData: catalog.CatalogData{Categories: []catalog.Category{{
		Key: "main", Name: "Main", Products: []catalog.Product{
			{Key: "visible", Name: "Visible dish", Active: true, Groups: []catalog.OptionGroup{{Key: "size", Name: "Size", Options: []catalog.Option{{Key: "large", Name: "Large", Active: true}, {Key: "secret-option", Name: "Hidden option", Active: false}}}}},
			{Key: "hidden", Name: "Hidden product name", Active: false},
		}}, {Key: "hidden-category", Name: "Hidden category", Products: []catalog.Product{{Key: "only-hidden", Name: "Hidden too", Active: false}}}}}}
	guest, err := json.Marshal(safeGuestMenu(menu))
	if err != nil {
		t.Fatal(err)
	}
	guestBody := string(guest)
	for _, hidden := range []string{"Hidden product name", "Hidden option", "Hidden too", "Hidden category"} {
		if strings.Contains(guestBody, hidden) {
			t.Errorf("guest menu leaked %q: %s", hidden, guestBody)
		}
	}
	admin, err := json.Marshal(safeAdminStoreMenu(menu))
	if err != nil {
		t.Fatal(err)
	}
	adminBody := string(admin)
	for _, retained := range []string{"Hidden product name", "Hidden option", "Hidden too", "Hidden category"} {
		if !strings.Contains(adminBody, retained) {
			t.Errorf("store manager menu omitted %q: %s", retained, adminBody)
		}
	}
	if strings.Contains(guestBody, "availability_version") || strings.Contains(guestBody, "price_override_version") {
		t.Fatalf("guest menu exposed admin metadata: %s", guestBody)
	}
}
