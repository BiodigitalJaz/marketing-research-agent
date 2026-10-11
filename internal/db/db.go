// Package db opens Postgres connections and applies schema migrations.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// enumTypes are custom Postgres types (and their array types) that pgx must
// know to encode Go slices of them.
var enumTypes = []string{"lane", "_lane", "member_role", "opp_status", "job_status"}

// Open creates a pgx pool and verifies connectivity.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// Register enum array types so slices like []store.Lane encode as lane[].
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		types, err := conn.LoadTypes(ctx, enumTypes)
		if err != nil {
			// Before migrations run the types do not exist yet; that is fine.
			return nil //nolint:nilerr // optional registration
		}
		conn.TypeMap().RegisterTypes(types)
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
