package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresStoreBootstrapSeedsLocalConfigAndAuthWhenDatabaseEmpty(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLI_PROXY_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLI_PROXY_TEST_POSTGRES_DSN is not set")
	}

	seedRoot := t.TempDir()
	configPath := filepath.Join(seedRoot, "config.yaml")
	configBody := []byte("port: 8317\nusage-statistics-enabled: true\n")
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatalf("write seed config error = %v", err)
	}

	authDir := filepath.Join(seedRoot, "auth")
	if err := os.MkdirAll(filepath.Join(authDir, "logs"), 0o700); err != nil {
		t.Fatalf("create auth dir error = %v", err)
	}
	authBody := []byte(`{"type":"codex","email":"seed@example.com","disabled":false}`)
	if err := os.WriteFile(filepath.Join(authDir, "seed-auth.json"), authBody, 0o600); err != nil {
		t.Fatalf("write seed auth error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "config.yaml"), []byte("not-auth"), 0o600); err != nil {
		t.Fatalf("write auth config noise error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "logs", "error.log"), []byte("noise"), 0o600); err != nil {
		t.Fatalf("write auth log noise error = %v", err)
	}

	schema := fmt.Sprintf("postgres_store_it_%d", time.Now().UTC().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:      dsn,
		Schema:   schema,
		SpoolDir: filepath.Join(seedRoot, "pgstore"),
		SeedRoot: seedRoot,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	pgdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open(pgx) error = %v", err)
	}
	defer func() { _ = pgdb.Close() }()
	t.Cleanup(func() {
		_, _ = pgdb.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteIdentifier(schema)+` CASCADE`)
	})

	examplePath := filepath.Join(seedRoot, "config.example.yaml")
	if err := os.WriteFile(examplePath, []byte("port: 9999\n"), 0o600); err != nil {
		t.Fatalf("write example config error = %v", err)
	}
	if err := store.Bootstrap(ctx, examplePath); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	var configContent string
	if err := pgdb.QueryRowContext(ctx, `SELECT content FROM `+quoteIdentifier(schema)+`.`+quoteIdentifier(defaultConfigTable)+` WHERE id = $1`, defaultConfigKey).Scan(&configContent); err != nil {
		t.Fatalf("query config row error = %v", err)
	}
	if configContent != string(configBody) {
		t.Fatalf("config content = %q, want %q", configContent, string(configBody))
	}

	var authCount int
	if err := pgdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(schema)+`.`+quoteIdentifier(defaultAuthTable)).Scan(&authCount); err != nil {
		t.Fatalf("query auth count error = %v", err)
	}
	if authCount != 1 {
		t.Fatalf("auth row count = %d, want 1", authCount)
	}

	spooledAuth, err := os.ReadFile(filepath.Join(store.AuthDir(), "seed-auth.json"))
	if err != nil {
		t.Fatalf("read spooled auth error = %v", err)
	}
	if !jsonEqual(spooledAuth, authBody) {
		t.Fatalf("spooled auth content = %q, want json-equivalent %q", string(spooledAuth), string(authBody))
	}
	if _, err := os.Stat(filepath.Join(store.AuthDir(), "logs", "error.log")); !os.IsNotExist(err) {
		t.Fatalf("expected auth logs to be ignored during bootstrap, stat err = %v", err)
	}
}
