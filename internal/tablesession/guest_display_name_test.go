package tablesession

import (
	"encoding/json"
	"testing"
)

func TestPublicTableGuestDisplayNameJSON(t *testing.T) {
	for _, name := range []string{"", "公開店舗 <店>"} {
		data, err := json.Marshal(PublicTable{Mode: StaffOpen, StoreDisplayName: name})
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err = json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		got, present := body["store_display_name"]
		if present != (name != "") || (present && got != name) {
			t.Fatalf("name %q: %s", name, data)
		}
	}
}
