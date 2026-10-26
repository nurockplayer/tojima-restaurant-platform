// Command staffadmin is the pilot provisioning tool described by ADR-0008 §11.
// It is an operator CLI and is never exposed over HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/staffauth"
)

type storeAPI interface {
	Grant(context.Context, string, string, string, authz.Role, string) error
	RevokeMembership(context.Context, string, string, string, authz.Role, string) (bool, error)
	RevokeSubjectSessions(context.Context, string, string, ...string) (int64, error)
	Hold(context.Context, string, string, string) error
	Restore(context.Context, string, string) error
	Status(context.Context, string) (staffauth.SubjectStatus, error)
}

type command struct {
	name, actor, subject, tenant, store, reason string
	role                                        authz.Role
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	args := os.Args[1:]
	if _, err := parseArgs(args, io.Discard); err != nil {
		return run(context.Background(), args, os.Stdout, os.Stderr, nil)
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" && os.Getenv("PGHOST") == "" {
		fmt.Fprintln(os.Stderr, "staffadmin: DATABASE_URL or PGHOST is required")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "staffadmin: database connection failed")
		return 1
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "staffadmin: database connection failed")
		return 1
	}
	store, err := staffauth.NewStore(pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "staffadmin: database connection failed")
		return 1
	}
	return run(ctx, args, os.Stdout, os.Stderr, store)
}

func run(parent context.Context, args []string, stdout, stderr io.Writer, store storeAPI) int {
	cmd, err := parseArgs(args, stderr)
	if err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if store == nil {
		fmt.Fprintln(stderr, "staffadmin: store unavailable")
		return 1
	}

	switch cmd.name {
	case "grant":
		err = store.Grant(ctx, cmd.subject, cmd.tenant, cmd.store, cmd.role, cmd.actor)
	case "revoke":
		var changed bool
		changed, err = store.RevokeMembership(ctx, cmd.subject, cmd.tenant, cmd.store, cmd.role, cmd.actor)
		if err == nil {
			if changed {
				fmt.Fprintln(stdout, "membership revoked")
			} else {
				fmt.Fprintln(stdout, "no matching membership")
			}
		}
	case "revoke-sessions":
		var count int64
		count, err = store.RevokeSubjectSessions(ctx, cmd.subject, cmd.reason, cmd.actor)
		if err == nil {
			fmt.Fprintf(stdout, "revoked sessions: %d\n", count)
		}
	case "hold":
		err = store.Hold(ctx, cmd.subject, cmd.reason, cmd.actor)
		if err == nil {
			fmt.Fprintf(stdout, "Next: change the credential in Keycloak, then run: staffadmin restore --actor %s --subject %s\n", shellQuote(cmd.actor), shellQuote(cmd.subject))
		}
	case "restore":
		err = store.Restore(ctx, cmd.subject, cmd.actor)
	case "status":
		var status staffauth.SubjectStatus
		status, err = store.Status(ctx, cmd.subject)
		if err == nil {
			printStatus(stdout, status)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "staffadmin: %v\n", err)
		return 1
	}
	return 0
}

func parseArgs(args []string, stderr io.Writer) (command, error) {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: staffadmin <grant|revoke|revoke-sessions|hold|restore|status> [flags]")
		return command{}, errors.New("subcommand required")
	}
	cmd := command{name: args[0]}
	fs := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch cmd.name {
	case "grant", "revoke":
		fs.StringVar(&cmd.actor, "actor", "", "operator name")
		fs.StringVar(&cmd.subject, "subject", "", "Keycloak subject")
		fs.StringVar(&cmd.tenant, "tenant", "", "tenant ID")
		fs.StringVar(&cmd.store, "store", "", "store ID")
		role := fs.String("role", "", "staff role")
		if err := fs.Parse(args[1:]); err != nil {
			return command{}, err
		}
		cmd.role = authz.Role(*role)
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "unexpected positional arguments")
			return command{}, errors.New("unexpected positional arguments")
		}
		if err := requireMutation(cmd, stderr); err != nil {
			return command{}, err
		}
		if !validRole(cmd.role) {
			fmt.Fprintln(stderr, "role must be one of STAFF_CASHIER, STORE_MANAGER, KITCHEN, TENANT_ADMIN, FINANCE_VIEWER")
			return command{}, errors.New("invalid role")
		}
		if cmd.store != "" && strings.TrimSpace(cmd.store) == "" {
			fmt.Fprintln(stderr, "--store cannot contain only whitespace")
			return command{}, errors.New("invalid store")
		}
		if cmd.store == "" && cmd.role != authz.TenantAdmin {
			fmt.Fprintln(stderr, "--store may be empty only for TENANT_ADMIN")
			return command{}, errors.New("store is required for this role")
		}
	case "revoke-sessions":
		fs.StringVar(&cmd.actor, "actor", "", "operator name")
		fs.StringVar(&cmd.subject, "subject", "", "Keycloak subject")
		fs.StringVar(&cmd.reason, "reason", "operator", "revocation reason")
		if err := fs.Parse(args[1:]); err != nil {
			return command{}, err
		}
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "unexpected positional arguments")
			return command{}, errors.New("unexpected positional arguments")
		}
		if err := requireMutation(cmd, stderr); err != nil {
			return command{}, err
		}
	case "hold":
		fs.StringVar(&cmd.actor, "actor", "", "operator name")
		fs.StringVar(&cmd.subject, "subject", "", "Keycloak subject")
		fs.StringVar(&cmd.reason, "reason", "", "hold reason")
		if err := fs.Parse(args[1:]); err != nil {
			return command{}, err
		}
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "unexpected positional arguments")
			return command{}, errors.New("unexpected positional arguments")
		}
		if err := requireMutation(cmd, stderr); err != nil {
			return command{}, err
		}
		if strings.TrimSpace(cmd.reason) == "" {
			fmt.Fprintln(stderr, "--reason is required")
			return command{}, errors.New("hold reason required")
		}
	case "restore":
		fs.StringVar(&cmd.actor, "actor", "", "operator name")
		fs.StringVar(&cmd.subject, "subject", "", "Keycloak subject")
		if err := fs.Parse(args[1:]); err != nil {
			return command{}, err
		}
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "unexpected positional arguments")
			return command{}, errors.New("unexpected positional arguments")
		}
		if err := requireMutation(cmd, stderr); err != nil {
			return command{}, err
		}
	case "status":
		fs.StringVar(&cmd.subject, "subject", "", "Keycloak subject")
		if err := fs.Parse(args[1:]); err != nil {
			return command{}, err
		}
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "unexpected positional arguments")
			return command{}, errors.New("unexpected positional arguments")
		}
		if strings.TrimSpace(cmd.subject) == "" {
			fmt.Fprintln(stderr, "--subject is required")
			return command{}, errors.New("subject required")
		}
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", cmd.name)
		fmt.Fprintln(stderr, "usage: staffadmin <grant|revoke|revoke-sessions|hold|restore|status> [flags]")
		return command{}, errors.New("unknown subcommand")
	}
	return cmd, nil
}

func requireMutation(cmd command, stderr io.Writer) error {
	if strings.TrimSpace(cmd.actor) == "" {
		fmt.Fprintln(stderr, "--actor is required")
		return errors.New("actor required")
	}
	if strings.TrimSpace(cmd.subject) == "" {
		fmt.Fprintln(stderr, "--subject is required")
		return errors.New("subject required")
	}
	if cmd.name == "grant" || cmd.name == "revoke" {
		if strings.TrimSpace(cmd.tenant) == "" {
			fmt.Fprintln(stderr, "--tenant is required")
			return errors.New("tenant required")
		}
	}
	return nil
}

func validRole(role authz.Role) bool {
	switch role {
	case authz.Staff, authz.StoreManager, authz.Kitchen, authz.TenantAdmin, authz.FinanceViewer:
		return true
	default:
		return false
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func printStatus(w io.Writer, status staffauth.SubjectStatus) {
	fmt.Fprintf(w, "Subject: %s\nExists: %t\n", status.SubjectID, status.Exists)
	if status.HoldAt == nil {
		fmt.Fprintln(w, "Hold: none")
	} else {
		jst, err := time.LoadLocation("Asia/Tokyo")
		if err != nil {
			jst = time.FixedZone("JST", 9*60*60)
		}
		fmt.Fprintf(w, "Hold: %s reason=%q\n", status.HoldAt.In(jst).Format(time.RFC3339), status.HoldReason)
	}
	fmt.Fprintf(w, "Auth epoch: %d\nLive sessions: %d\n", status.AuthEpoch, status.LiveSessions)
	for _, membership := range status.Memberships {
		store := membership.StoreID
		if store == "" {
			store = "*"
		}
		fmt.Fprintf(w, "Membership: %s/%s %s\n", membership.TenantID, store, membership.Role)
	}
}
