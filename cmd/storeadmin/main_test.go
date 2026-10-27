package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

var locatorPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)

// fakeStore keeps the same shapes the postgres store returns, so apply can run
// twice and observe its own writes.
type fakeStore struct {
	tenants map[string]named
	stores  map[string]storeRow
	tables  map[string][]tableRow
	cash    map[string]operations.CashSettings
	plans   []plan
	rotated int
	calls   int
	err     error
	// afterApply simulates a concurrent writer that changed rows mid-apply.
	afterApply func()
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tenants: map[string]named{},
		stores:  map[string]storeRow{},
		tables:  map[string][]tableRow{},
		cash:    map[string]operations.CashSettings{},
	}
}

func scopeKey(tenantID, storeID string) string { return tenantID + "/" + storeID }

func (f *fakeStore) Tenant(_ context.Context, tenantID string) (named, bool, error) {
	f.calls++
	tenant, ok := f.tenants[tenantID]
	return tenant, ok, nil
}

func (f *fakeStore) Store(_ context.Context, tenantID, storeID string) (storeRow, bool, error) {
	f.calls++
	store, ok := f.stores[scopeKey(tenantID, storeID)]
	return store, ok, nil
}

func (f *fakeStore) Tables(_ context.Context, tenantID, storeID string) ([]tableRow, error) {
	f.calls++
	return append([]tableRow(nil), f.tables[scopeKey(tenantID, storeID)]...), nil
}

func (f *fakeStore) Cash(_ context.Context, tenantID, storeID string) (operations.CashSettings, error) {
	f.calls++
	settings, ok := f.cash[scopeKey(tenantID, storeID)]
	if !ok {
		return operations.CashSettings{Mode: operations.Prepaid}, nil
	}
	return settings, nil
}

func (f *fakeStore) Apply(_ context.Context, tenantID string, write plan) error {
	f.calls++
	f.plans = append(f.plans, write)
	if f.err != nil {
		return f.err
	}
	f.tenants[tenantID] = write.Tenant
	key := scopeKey(tenantID, write.StoreID)
	store, exists := f.stores[key]
	if !exists {
		store.Mode = write.Mode
	}
	store.Name = write.StoreName
	if write.GuestDisplayName != nil {
		guest := *write.GuestDisplayName
		store.GuestDisplayName = &guest
	}
	if write.ModeChanged {
		store.Mode = write.Mode
	}
	f.stores[key] = store
	tables := append([]tableRow(nil), f.tables[key]...)
	tables = append(tables, write.NewTables...)
	for _, change := range write.Renamed {
		for i := range tables {
			if tables[i].ID == change.ID {
				tables[i].Name = change.Name
			}
		}
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].ID < tables[j].ID })
	f.tables[key] = tables
	if write.Cash != nil {
		f.cash[key] = operations.CashSettings{Enabled: write.Cash.Enabled, Mode: write.Cash.Mode, Version: write.Cash.Expected + 1}
	}
	if f.afterApply != nil {
		f.afterApply()
	}
	return nil
}

func (f *fakeStore) Rotate(_ context.Context, tenantID, storeID, tableID, locator string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	key := scopeKey(tenantID, storeID)
	for i := range f.tables[key] {
		if f.tables[key][i].ID == tableID {
			f.tables[key][i].Locator = locator
			f.rotated++
			return nil
		}
	}
	return errNotFound
}

func runCLI(t *testing.T, store storeAPI, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := run(context.Background(), args, &stdout, &stderr, store)
	return code, stdout.String(), stderr.String()
}

func writeSpec(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validSpec = `{"tenant":{"id":"tojima","name":"東島"},
 "store":{"id":"main","name":"本店","guest_display_name":"東島","qr_entry_mode":"STAFF_OPEN",
          "cash":{"enabled":true,"mode":"PAY_LATER"}},
 "tables":[{"id":"t01","name":"1番"},{"id":"t02","name":"2番"}]}`

func TestParseUsageErrorsDoNotCallStore(t *testing.T) {
	many := strings.Builder{}
	many.WriteString(`{"tenant":{"id":"tojima","name":"東島"},"store":{"id":"main","name":"本店","qr_entry_mode":"STAFF_OPEN"},"tables":[`)
	for i := 0; i <= maxTables; i++ {
		if i != 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"id":"t%03d","name":"テーブル%d"}`, i, i)
	}
	many.WriteString("]}")

	tests := []struct {
		name string
		args []string
	}{
		{"no subcommand", nil},
		{"unknown subcommand", []string{"list"}},
		{"apply without -f", []string{"apply"}},
		{"apply missing file", []string{"apply", "-f", filepath.Join(t.TempDir(), "absent.json")}},
		{"apply invalid json", []string{"apply", "-f", writeSpec(t, `{"tenant":`)}},
		{"apply cash without enabled", []string{"apply", "-f", writeSpec(t, `{"tenant":{"id":"tojima","name":"東島"},"store":{"id":"main","name":"本店","qr_entry_mode":"STAFF_OPEN","cash":{"mode":"PREPAID"}}}`)}},
		{"apply unknown field", []string{"apply", "-f", writeSpec(t, `{"tenant":{"id":"tojima","name":"東島"},"store":{"id":"main","name":"本店","qr_entry_mode":"STAFF_OPEN"},"extra":true}`)}},
		{"apply invalid tenant id", []string{"apply", "-f", writeSpec(t, strings.Replace(validSpec, `"id":"tojima"`, `"id":"Tojima"`, 1))}},
		{"apply blank tenant name", []string{"apply", "-f", writeSpec(t, strings.Replace(validSpec, `"name":"東島"`, `"name":"   "`, 1))}},
		{"apply unknown qr mode", []string{"apply", "-f", writeSpec(t, strings.Replace(validSpec, `"STAFF_OPEN"`, `"OPEN"`, 1))}},
		{"apply unknown cash mode", []string{"apply", "-f", writeSpec(t, strings.Replace(validSpec, `"mode":"PAY_LATER"`, `"mode":"FREE"`, 1))}},
		{"apply newline in guest display name", []string{"apply", "-f", writeSpec(t, strings.Replace(validSpec, `"guest_display_name":"東島"`, `"guest_display_name":"東\u000a島"`, 1))}},
		{"apply duplicate table id", []string{"apply", "-f", writeSpec(t, strings.Replace(validSpec, `{"id":"t02","name":"2番"}`, `{"id":"t01","name":"2番"}`, 1))}},
		{"apply too many tables", []string{"apply", "-f", writeSpec(t, many.String())}},
		{"apply bad origin", []string{"apply", "-f", writeSpec(t, validSpec), "-public-origin", "http://order.example.jp"}},
		{"apply origin with path", []string{"apply", "-f", writeSpec(t, validSpec), "-public-origin", "https://order.example.jp/guest"}},
		{"apply origin without host", []string{"apply", "-f", writeSpec(t, validSpec), "-public-origin", "https://:443"}},
		{"tables without tenant", []string{"tables", "-store", "main"}},
		{"tables invalid store id", []string{"tables", "-tenant", "tojima", "-store", "Main"}},
		{"rotate without table", []string{"rotate-locator", "-tenant", "tojima", "-store", "main"}},
		{"unexpected positional argument", []string{"tables", "-tenant", "tojima", "-store", "main", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			code, _, stderr := runCLI(t, store, tt.args...)
			if code != 2 || store.calls != 0 || stderr == "" {
				t.Fatalf("code=%d calls=%d stderr=%q", code, store.calls, stderr)
			}
		})
	}
}

func TestApplyCreatesThenReportsUnchanged(t *testing.T) {
	store := newFakeStore()
	path := writeSpec(t, validSpec)

	code, stdout, stderr := runCLI(t, store, "apply", "-f", path)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 5 {
		t.Fatalf("stdout=%q", stdout)
	}
	if lines[0] != "tenant tojima: created" || lines[1] != "store main: created" {
		t.Fatalf("stdout=%q", stdout)
	}
	locators := make([]string, 0, 2)
	for i, id := range []string{"t01", "t02"} {
		fields := strings.Fields(lines[2+i])
		if len(fields) != 6 || fields[0] != "table" || fields[1] != id || fields[5] != "created" {
			t.Fatalf("line=%q", lines[2+i])
		}
		if !locatorPattern.MatchString(fields[3]) || fields[3] != fields[4] {
			t.Fatalf("locator columns=%q url-or-locator=%q", fields[3], fields[4])
		}
		locators = append(locators, fields[3])
	}
	if lines[4] != "cash: enabled PAY_LATER (changed)" {
		t.Fatalf("cash line=%q", lines[4])
	}
	if got := store.cash[scopeKey("tojima", "main")]; !got.Enabled || got.Mode != operations.PayLater || got.Version != 1 {
		t.Fatalf("cash=%+v", got)
	}

	code, stdout, stderr = runCLI(t, store, "apply", "-f", path)
	if code != 0 || stderr != "" {
		t.Fatalf("second apply code=%d stderr=%q", code, stderr)
	}
	lines = strings.Split(strings.TrimSpace(stdout), "\n")
	want := []string{
		"tenant tojima: unchanged",
		"store main: unchanged",
		"table t01 1番 " + locators[0] + " " + locators[0] + " unchanged",
		"table t02 2番 " + locators[1] + " " + locators[1] + " unchanged",
		"cash: enabled PAY_LATER (unchanged)",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("stdout=%q", stdout)
	}
	if len(store.plans) != 1 {
		t.Fatalf("unchanged apply wrote %d plans", len(store.plans))
	}
}

func TestApplyPrintsTableURLsWithPublicOrigin(t *testing.T) {
	store := newFakeStore()
	code, stdout, stderr := runCLI(t, store, "apply", "-f", writeSpec(t, validSpec), "-public-origin", "https://order.example.jp/")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n")[2:4] {
		fields := strings.Fields(line)
		if fields[4] != "https://order.example.jp/?table="+fields[3] {
			t.Fatalf("line=%q", line)
		}
	}

	// Any loopback address is acceptable for an http origin, not just 127.0.0.1.
	code, stdout, stderr = runCLI(t, store, "tables", "-tenant", "tojima", "-store", "main", "-public-origin", "http://127.0.0.2:5173")
	if code != 0 || stderr != "" {
		t.Fatalf("tables code=%d stderr=%q", code, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		fields := strings.Fields(line)
		if fields[4] != "http://127.0.0.2:5173/?table="+fields[3] {
			t.Fatalf("line=%q", line)
		}
	}

	code, _, stderr = runCLI(t, store, "tables", "-tenant", "tojima", "-store", "main", "-public-origin", "http://[::1]:5173")
	if code != 0 || stderr != "" {
		t.Fatalf("ipv6 loopback code=%d stderr=%q", code, stderr)
	}
}

func TestApplyRenamesAddsKeepsAndChangesMode(t *testing.T) {
	store := newFakeStore()
	store.tenants["tojima"] = named{ID: "tojima", Name: "東島"}
	store.stores[scopeKey("tojima", "main")] = storeRow{Name: "本店", Mode: tablesession.StaffOpen}
	store.tables[scopeKey("tojima", "main")] = []tableRow{
		{ID: "t01", Name: "1番", Locator: "aaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{ID: "t02", Name: "2番", Locator: "bbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	spec := `{"tenant":{"id":"tojima","name":"東島"},
	 "store":{"id":"main","name":"本店","guest_display_name":"東島","qr_entry_mode":"DIRECT_SCAN"},
	 "tables":[{"id":"t01","name":"窓側1番"},{"id":"t03","name":"3番"}]}`

	code, stdout, stderr := runCLI(t, store, "apply", "-f", writeSpec(t, spec))
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 6 {
		t.Fatalf("stdout=%q", stdout)
	}
	if lines[0] != "tenant tojima: unchanged" || lines[1] != "store main: updated" {
		t.Fatalf("stdout=%q", stdout)
	}
	if lines[2] != "table t01 窓側1番 aaaaaaaaaaaaaaaaaaaaaaaaaa aaaaaaaaaaaaaaaaaaaaaaaaaa renamed" {
		t.Fatalf("renamed line=%q", lines[2])
	}
	if fields := strings.Fields(lines[3]); fields[1] != "t03" || fields[5] != "created" || !locatorPattern.MatchString(fields[3]) {
		t.Fatalf("created line=%q", lines[3])
	}
	if lines[4] != "not in file (kept): t02" || lines[5] != "cash: not in file (kept)" {
		t.Fatalf("stdout=%q", stdout)
	}
	if len(store.plans) != 1 {
		t.Fatalf("plans=%d", len(store.plans))
	}
	write := store.plans[0]
	if !write.ModeChanged || write.Mode != tablesession.DirectScan {
		t.Fatalf("mode: %+v", write)
	}
	if len(write.Renamed) != 1 || write.Renamed[0] != (rename{ID: "t01", Name: "窓側1番"}) {
		t.Fatalf("renamed: %+v", write.Renamed)
	}
	if len(write.NewTables) != 1 || write.NewTables[0].ID != "t03" {
		t.Fatalf("new tables: %+v", write.NewTables)
	}
	// t02 is still present: apply never deletes tables missing from the file.
	if len(store.tables[scopeKey("tojima", "main")]) != 3 {
		t.Fatalf("tables=%+v", store.tables[scopeKey("tojima", "main")])
	}
}

// A concurrent apply can insert the same table first; the printed QR locator
// must then be the persisted one, not the one this run generated.
func TestApplyPrintsPersistedLocatorAfterConcurrentCreate(t *testing.T) {
	store := newFakeStore()
	persisted := "cccccccccccccccccccccccccc"
	store.afterApply = func() {
		key := scopeKey("tojima", "main")
		for i := range store.tables[key] {
			store.tables[key][i].Locator = persisted
		}
	}
	code, stdout, stderr := runCLI(t, store, "apply", "-f", writeSpec(t, validSpec))
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n")[2:4] {
		fields := strings.Fields(line)
		if fields[3] != persisted || fields[4] != persisted {
			t.Fatalf("line=%q want persisted locator %s", line, persisted)
		}
	}
}

func TestRotateLocatorChangesOneTable(t *testing.T) {
	store := newFakeStore()
	store.tables[scopeKey("tojima", "main")] = []tableRow{
		{ID: "t01", Name: "1番", Locator: "aaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{ID: "t02", Name: "2番", Locator: "bbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	code, stdout, stderr := runCLI(t, store, "rotate-locator", "-tenant", "tojima", "-store", "main", "-table", "t02", "-public-origin", "https://order.example.jp")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	locator := strings.TrimSuffix(stdout, "\n")
	if !strings.HasPrefix(locator, "https://order.example.jp/?table=") {
		t.Fatalf("stdout=%q", stdout)
	}
	tables := store.tables[scopeKey("tojima", "main")]
	if tables[0].Locator != "aaaaaaaaaaaaaaaaaaaaaaaaaa" || !locatorPattern.MatchString(tables[1].Locator) || tables[1].Locator == "bbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("tables=%+v", tables)
	}
	if store.rotated != 1 {
		t.Fatalf("rotated=%d", store.rotated)
	}

	code, _, stderr = runCLI(t, store, "rotate-locator", "-tenant", "tojima", "-store", "main", "-table", "t09")
	if code != 1 || stderr != "storeadmin: store or table not found\n" {
		t.Fatalf("missing table code=%d stderr=%q", code, stderr)
	}
}

func TestTablesListAndCSV(t *testing.T) {
	store := newFakeStore()
	store.stores[scopeKey("tojima", "main")] = storeRow{Name: "本店", Mode: tablesession.StaffOpen}
	store.tables[scopeKey("tojima", "main")] = []tableRow{
		{ID: "t01", Name: "1番", Locator: "aaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{ID: "t02", Name: "2番", Locator: "bbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	code, stdout, stderr := runCLI(t, store, "tables", "-tenant", "tojima", "-store", "main")
	want := "table t01 1番 aaaaaaaaaaaaaaaaaaaaaaaaaa aaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"table t02 2番 bbbbbbbbbbbbbbbbbbbbbbbbbb bbbbbbbbbbbbbbbbbbbbbbbbbb\n"
	if code != 0 || stderr != "" || stdout != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI(t, store, "tables", "-tenant", "tojima", "-store", "main", "-csv")
	want = "table_id,name,url_or_locator\n" +
		"t01,1番,aaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"t02,2番,bbbbbbbbbbbbbbbbbbbbbbbbbb\n"
	if code != 0 || stderr != "" || stdout != want {
		t.Fatalf("csv code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, _, stderr = runCLI(t, store, "tables", "-tenant", "tojima", "-store", "gone")
	if code != 1 || stderr != "storeadmin: store or table not found\n" {
		t.Fatalf("missing store code=%d stderr=%q", code, stderr)
	}
}

func TestApplyErrorIsAFixedMessage(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("failed to connect to postgres://user:secret@db:5432/tojima")
	code, stdout, stderr := runCLI(t, store, "apply", "-f", writeSpec(t, validSpec))
	if code != 1 || stderr != "storeadmin: database operation failed\n" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout+stderr, "secret") {
		t.Fatalf("credential leaked: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestApplyDefaultsCashActor(t *testing.T) {
	store := newFakeStore()
	if code, _, stderr := runCLI(t, store, "apply", "-f", writeSpec(t, validSpec)); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if store.plans[0].Cash == nil || store.plans[0].Cash.Actor != defaultCashActor {
		t.Fatalf("cash=%+v", store.plans[0].Cash)
	}

	store = newFakeStore()
	if code, _, stderr := runCLI(t, store, "apply", "-f", writeSpec(t, validSpec), "-actor", "  operator-a  "); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if store.plans[0].Cash.Actor != "operator-a" {
		t.Fatalf("actor=%q", store.plans[0].Cash.Actor)
	}
}
