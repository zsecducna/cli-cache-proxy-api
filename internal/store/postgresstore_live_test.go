package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

func TestLiveConfiguredPostgresBootstrapSeedsConfigAndAuth(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLI_PROXY_LIVE_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLI_PROXY_LIVE_POSTGRES_DSN is not set")
	}

	schema := strings.TrimSpace(os.Getenv("CLI_PROXY_LIVE_POSTGRES_SCHEMA"))
	if schema == "" {
		schema = "public"
	}

	localPath := strings.TrimSpace(os.Getenv("CLI_PROXY_LIVE_LOCAL_PATH"))
	if localPath == "" {
		localPath = "~/.cli-cache-proxy"
	}
	seedRoot, err := util.ResolveAuthDir(localPath)
	if err != nil {
		t.Fatalf("ResolveAuthDir(%q) error = %v", localPath, err)
	}

	authFiles, err := collectSeedAuthFiles(filepath.Join(seedRoot, "auth"))
	if err != nil {
		t.Fatalf("collect seed auth files error = %v", err)
	}
	if len(authFiles) == 0 {
		t.Fatal("no local auth json files found to seed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

	if err := store.Bootstrap(ctx, ""); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	pgdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open(pgx) error = %v", err)
	}
	defer func() { _ = pgdb.Close() }()

	var configCount int
	if err := pgdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(schema)+`.`+quoteIdentifier(defaultConfigTable)).Scan(&configCount); err != nil {
		t.Fatalf("query config count error = %v", err)
	}
	if configCount < 1 {
		t.Fatalf("config row count = %d, want at least 1", configCount)
	}

	var authCount int
	if err := pgdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(schema)+`.`+quoteIdentifier(defaultAuthTable)).Scan(&authCount); err != nil {
		t.Fatalf("query auth count error = %v", err)
	}
	if authCount < len(authFiles) {
		t.Fatalf("auth row count = %d, want at least %d local auth json files mirrored", authCount, len(authFiles))
	}

	relID := filepath.ToSlash(strings.TrimPrefix(authFiles[0], filepath.Join(seedRoot, "auth")+string(os.PathSeparator)))
	var seeded int
	if err := pgdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(schema)+`.`+quoteIdentifier(defaultAuthTable)+` WHERE id = $1`, relID).Scan(&seeded); err != nil {
		t.Fatalf("query seeded auth row error = %v", err)
	}
	if seeded != 1 {
		t.Fatalf("seeded auth row count for %q = %d, want 1", relID, seeded)
	}

	if _, err := os.Stat(filepath.Join(store.AuthDir(), filepath.FromSlash(relID))); err != nil {
		t.Fatalf("expected spooled auth file %q to exist: %v", relID, err)
	}
}

func collectSeedAuthFiles(root string) ([]string, error) {
	files := make([]string, 0)
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && d.Name() == "logs" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}
