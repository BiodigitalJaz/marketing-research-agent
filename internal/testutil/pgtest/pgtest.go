// Package pgtest provides throwaway Postgres databases for integration
// tests, backed by one pgvector testcontainer per test binary.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/db"
)

// Image matches docker-compose.yml and the cluster's PostgreSQL major version.
const Image = "pgvector/pgvector:pg17"

var (
	once     sync.Once
	adminURL string
	startErr error
)

func start() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		startErr = fmt.Errorf("start postgres container: %w", err)
		return
	}
	// The container is reaped by testcontainers' Ryuk sidecar when the test
	// process exits.
	adminURL, startErr = c.ConnectionString(ctx, "sslmode=disable")
}

// NewDB creates an empty database (no migrations) and returns a pool to it.
// Skips the test under -short.
func NewDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: skipped with -short")
	}
	once.Do(start)
	if startErr != nil {
		t.Fatal(startErr)
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "t_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// NewMigratedDB returns a pool to a fresh database with all migrations applied.
func NewMigratedDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := NewDB(t)
	if err := db.Migrate(context.Background(), pool, DiscardLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Drop connections opened before the enum types existed so new ones
	// register them (as the api/worker do after `migrate` has run).
	pool.Reset()
	return pool
}

// DiscardLogger returns a logger that drops everything.
func DiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
