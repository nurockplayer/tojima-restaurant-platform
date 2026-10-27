// Command storeadmin provisions tenants, stores, dining tables and cash
// settings for the pilot. It is an idempotent operator CLI and is never exposed
// over HTTP.
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

const (
	usage              = "usage: storeadmin <apply|tables|rotate-locator> [flags]"
	defaultCashActor   = "storeadmin"
	maxActorByteLength = 200
)

var errNotFound = errors.New("store or table not found")

// named is one row of tenants.
type named struct {
	ID, Name string
}

// storeRow is the part of a store row this command reconciles.
type storeRow struct {
	Name             string
	GuestDisplayName *string
	Mode             tablesession.Mode
}

// tableRow is one dining table.
type tableRow struct {
	ID, Name, Locator string
}

type rename struct {
	ID, Name string
}

type cashChange struct {
	Actor    string
	Enabled  bool
	Mode     operations.CashMode
	Expected int64
}

// plan is the write set computed from the current database state. Applying it
// twice converges: every statement is an upsert or a conditional update.
type plan struct {
	Tenant           named
	StoreID          string
	StoreName        string
	GuestDisplayName *string // nil leaves an existing value untouched
	Mode             tablesession.Mode
	ModeChanged      bool
	NewTables        []tableRow
	Renamed          []rename
	Cash             *cashChange
}

// storeAPI is what the commands need; tests substitute an in-memory fake.
type storeAPI interface {
	Tenant(context.Context, string) (named, bool, error)
	Store(context.Context, string, string) (storeRow, bool, error)
	Tables(context.Context, string, string) ([]tableRow, error)
	Cash(context.Context, string, string) (operations.CashSettings, error)
	Apply(context.Context, string, plan) error
	Rotate(context.Context, string, string, string, string) error
}

type command struct {
	name   string
	tenant string
	store  string
	table  string
	actor  string
	origin string
	csv    bool
	spec   specFile
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	args := os.Args[1:]
	if _, err := parseArgs(args, io.Discard); err != nil {
		// Re-run the parser so the usage text reaches stderr.
		return run(context.Background(), args, os.Stdout, os.Stderr, nil)
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" && os.Getenv("PGHOST") == "" {
		fmt.Fprintln(os.Stderr, "storeadmin: DATABASE_URL or PGHOST is required")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "storeadmin: database connection failed")
		return 1
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "storeadmin: database connection failed")
		return 1
	}
	store, err := newPostgresStore(pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "storeadmin: database connection failed")
		return 1
	}
	return run(ctx, args, os.Stdout, os.Stderr, store)
}

func run(parent context.Context, args []string, stdout, stderr io.Writer, api storeAPI) int {
	cmd, err := parseArgs(args, stderr)
	if err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if api == nil {
		fmt.Fprintln(stderr, "storeadmin: database unavailable")
		return 1
	}

	switch cmd.name {
	case "apply":
		err = applyStore(ctx, cmd, stdout, api)
	case "tables":
		err = listTables(ctx, cmd, stdout, api)
	case "rotate-locator":
		err = rotateLocator(ctx, cmd, stdout, api)
	}
	if err != nil {
		fmt.Fprintln(stderr, operatorError(err))
		return 1
	}
	return 0
}

func parseArgs(args []string, stderr io.Writer) (command, error) {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return command{}, errors.New("subcommand required")
	}
	cmd := command{name: args[0]}
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var path string
	switch cmd.name {
	case "apply":
		fs.StringVar(&path, "f", "", "provisioning JSON file")
		fs.StringVar(&cmd.origin, "public-origin", "", "public SPA origin used to render table URLs")
		fs.StringVar(&cmd.actor, "actor", "", "operator name recorded in the cash audit")
	case "tables":
		fs.StringVar(&cmd.tenant, "tenant", "", "tenant ID")
		fs.StringVar(&cmd.store, "store", "", "store ID")
		fs.StringVar(&cmd.origin, "public-origin", "", "public SPA origin used to render table URLs")
		fs.BoolVar(&cmd.csv, "csv", false, "print table_id,name,url_or_locator")
	case "rotate-locator":
		fs.StringVar(&cmd.tenant, "tenant", "", "tenant ID")
		fs.StringVar(&cmd.store, "store", "", "store ID")
		fs.StringVar(&cmd.table, "table", "", "table ID")
		fs.StringVar(&cmd.origin, "public-origin", "", "public SPA origin used to render table URLs")
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", cmd.name)
		fmt.Fprintln(stderr, usage)
		return command{}, errors.New("unknown subcommand")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return command{}, err
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return command{}, errors.New("unexpected positional arguments")
	}
	origin, err := parseOrigin(cmd.origin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return command{}, err
	}
	cmd.origin = origin

	switch cmd.name {
	case "apply":
		if strings.TrimSpace(path) == "" {
			fmt.Fprintln(stderr, "--f is required")
			return command{}, errors.New("provisioning file required")
		}
		cmd.spec, err = loadSpec(path)
		if err != nil {
			fmt.Fprintf(stderr, "storeadmin: %s\n", err)
			return command{}, err
		}
		cmd.actor = strings.TrimSpace(cmd.actor)
		if cmd.actor == "" {
			cmd.actor = defaultCashActor
		}
		if len(cmd.actor) > maxActorByteLength {
			fmt.Fprintln(stderr, "--actor must be at most 200 bytes")
			return command{}, errors.New("actor too long")
		}
	case "tables":
		if err := requireFlags(stderr, map[string]string{"--tenant": cmd.tenant, "--store": cmd.store}); err != nil {
			return command{}, err
		}
	case "rotate-locator":
		if err := requireFlags(stderr, map[string]string{"--tenant": cmd.tenant, "--store": cmd.store, "--table": cmd.table}); err != nil {
			return command{}, err
		}
	}
	return cmd, nil
}

func requireFlags(stderr io.Writer, flags map[string]string) error {
	for _, name := range []string{"--tenant", "--store", "--table"} {
		value, ok := flags[name]
		if !ok {
			continue
		}
		if err := checkID(name, value); err != nil {
			fmt.Fprintln(stderr, err)
			return err
		}
	}
	return nil
}

// applyStore reads the current state, prints what changed and writes only the
// difference, so a repeated run reports every row as unchanged.
func applyStore(ctx context.Context, cmd command, stdout io.Writer, api storeAPI) error {
	spec := cmd.spec
	tenantID, storeID := spec.Tenant.ID, spec.Store.ID

	tenant, tenantExists, err := api.Tenant(ctx, tenantID)
	if err != nil {
		return err
	}
	store, storeExists, err := api.Store(ctx, tenantID, storeID)
	if err != nil {
		return err
	}
	existing, err := api.Tables(ctx, tenantID, storeID)
	if err != nil {
		return err
	}

	tenantStatus := "unchanged"
	switch {
	case !tenantExists:
		tenantStatus = "created"
	case tenant.Name != spec.Tenant.Name:
		tenantStatus = "updated"
	}

	modeChanged := storeExists && store.Mode != spec.Store.QREntryMode
	storeStatus := "unchanged"
	switch {
	case !storeExists:
		storeStatus = "created"
	case store.Name != spec.Store.Name, modeChanged, guestDisplayNameChanged(store.GuestDisplayName, spec.Store.GuestDisplayName):
		storeStatus = "updated"
	}

	existingByID := make(map[string]tableRow, len(existing))
	for _, table := range existing {
		existingByID[table.ID] = table
	}
	inFile := make(map[string]bool, len(spec.Tables))
	for _, table := range spec.Tables {
		inFile[table.ID] = true
	}

	write := plan{
		Tenant:           named{ID: tenantID, Name: spec.Tenant.Name},
		StoreID:          storeID,
		StoreName:        spec.Store.Name,
		GuestDisplayName: spec.Store.GuestDisplayName,
		Mode:             spec.Store.QREntryMode,
		ModeChanged:      modeChanged,
	}
	type tableLine struct{ id, name, locator, status string }
	lines := make([]tableLine, 0, len(spec.Tables))
	for _, table := range spec.Tables {
		current, exists := existingByID[table.ID]
		if !exists {
			locator, err := newLocator()
			if err != nil {
				return err
			}
			write.NewTables = append(write.NewTables, tableRow{ID: table.ID, Name: table.Name, Locator: locator})
			lines = append(lines, tableLine{table.ID, table.Name, locator, "created"})
			continue
		}
		status := "unchanged"
		if current.Name != table.Name {
			status = "renamed"
			write.Renamed = append(write.Renamed, rename{ID: table.ID, Name: table.Name})
		}
		lines = append(lines, tableLine{table.ID, table.Name, current.Locator, status})
	}
	var kept []tableRow
	for _, table := range existing {
		if !inFile[table.ID] {
			kept = append(kept, table)
		}
	}

	cashLine := "cash: not in file (kept)"
	if spec.Store.Cash != nil {
		// A store that does not exist yet has the same zero state the service
		// reports for a store without a settings row.
		current := operations.CashSettings{Mode: operations.Prepaid}
		if storeExists {
			if current, err = api.Cash(ctx, tenantID, storeID); err != nil {
				return err
			}
		}
		changed := current.Enabled != spec.Store.Cash.Enabled || current.Mode != spec.Store.Cash.Mode
		if changed {
			write.Cash = &cashChange{Actor: cmd.actor, Enabled: spec.Store.Cash.Enabled, Mode: spec.Store.Cash.Mode, Expected: current.Version}
		}
		cashLine = fmt.Sprintf("cash: %s %s (%s)", enabledWord(spec.Store.Cash.Enabled), spec.Store.Cash.Mode, changedWord(changed))
	}

	if tenantStatus != "unchanged" || storeStatus != "unchanged" || len(write.NewTables) != 0 || len(write.Renamed) != 0 || write.Cash != nil {
		if err := api.Apply(ctx, tenantID, write); err != nil {
			return err
		}
	}

	fmt.Fprintf(stdout, "tenant %s: %s\n", tenantID, tenantStatus)
	fmt.Fprintf(stdout, "store %s: %s\n", storeID, storeStatus)
	for _, line := range lines {
		fmt.Fprintf(stdout, "table %s %s %s %s %s\n", line.id, line.name, line.locator, tableURL(cmd.origin, line.locator), line.status)
	}
	for _, table := range kept {
		fmt.Fprintf(stdout, "not in file (kept): %s\n", table.ID)
	}
	fmt.Fprintln(stdout, cashLine)
	return nil
}

func listTables(ctx context.Context, cmd command, stdout io.Writer, api storeAPI) error {
	if _, exists, err := api.Store(ctx, cmd.tenant, cmd.store); err != nil {
		return err
	} else if !exists {
		return errNotFound
	}
	tables, err := api.Tables(ctx, cmd.tenant, cmd.store)
	if err != nil {
		return err
	}
	if cmd.csv {
		writer := csv.NewWriter(stdout)
		if err := writer.Write([]string{"table_id", "name", "url_or_locator"}); err != nil {
			return err
		}
		for _, table := range tables {
			if err := writer.Write([]string{table.ID, table.Name, tableURL(cmd.origin, table.Locator)}); err != nil {
				return err
			}
		}
		writer.Flush()
		return writer.Error()
	}
	for _, table := range tables {
		fmt.Fprintf(stdout, "table %s %s %s %s\n", table.ID, table.Name, table.Locator, tableURL(cmd.origin, table.Locator))
	}
	return nil
}

// rotateLocator replaces one table's public locator, for a QR label that leaked.
func rotateLocator(ctx context.Context, cmd command, stdout io.Writer, api storeAPI) error {
	locator, err := newLocator()
	if err != nil {
		return err
	}
	if err := api.Rotate(ctx, cmd.tenant, cmd.store, cmd.table, locator); err != nil {
		return err
	}
	fmt.Fprintln(stdout, tableURL(cmd.origin, locator))
	return nil
}

func guestDisplayNameChanged(current, wanted *string) bool {
	if wanted == nil {
		// Absent in the file: keep whatever the store already shows.
		return false
	}
	return current == nil || *current != *wanted
}

func enabledWord(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func changedWord(changed bool) string {
	if changed {
		return "changed"
	}
	return "unchanged"
}

// operatorError keeps driver detail (DSNs, SQL) out of operator output.
func operatorError(err error) string {
	switch {
	case errors.Is(err, errNotFound), errors.Is(err, tablesession.ErrNotFound):
		return "storeadmin: store or table not found"
	case errors.Is(err, operations.ErrConflict):
		return "storeadmin: cash settings changed concurrently; re-run apply"
	case errors.Is(err, operations.ErrDenied), errors.Is(err, tablesession.ErrDenied):
		return "storeadmin: store is disabled"
	case errors.Is(err, operations.ErrInvalid):
		return "storeadmin: invalid cash settings"
	}
	return "storeadmin: database operation failed"
}
