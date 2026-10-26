// Command migrate applies the embedded PostgreSQL migrations once and exits.
// It is the one-shot migrate service in compose.yaml and reuses the same
// exported runner (migrations/postgres.Up) as the local test scripts, so the
// schema ledger stays identical no matter how the database was provisioned.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func main() {
	// DATABASE_URL wins; otherwise pgx falls back to the libpq PG* variables,
	// which compose sets so passwords need no URL encoding.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" && os.Getenv("PGHOST") == "" {
		log.Fatal("DATABASE_URL or PGHOST is required")
	}
	if err := run(context.Background(), dsn); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Print("migrations applied")
}

func run(parent context.Context, dsn string) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()

	conn, err := connectWithRetry(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	if err := postgres.Up(ctx, conn); err != nil {
		return err
	}
	var applied int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		return err
	}
	log.Printf("schema_migrations rows: %d", applied)
	return nil
}

// connectWithRetry tolerates a database that is still starting even though
// compose already waits for its healthcheck.
func connectWithRetry(ctx context.Context, dsn string) (*pgx.Conn, error) {
	var lastErr error
	for {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(2 * time.Second):
		}
	}
}
