package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	defaultRefreshCommandConfig = "config.yaml"
	defaultRefreshWindowHours   = 48
)

// refreshExecutor narrows the production executor surface to the refresh path used by this cron command.
type refreshExecutor interface {
	Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error)
}

// commandResources bundles the runtime config, store, and cleanup hook needed by the cron command.
type commandResources struct {
	cfg     *config.Config
	store   cliproxyauth.Store
	storeID string
	cleanup func() error
}

// refreshStats captures the cron run result in a machine-readable summary for logs and cron output.
type refreshStats struct {
	Store               string   `json:"store"`
	DryRun              bool     `json:"dry_run"`
	Scanned             int      `json:"scanned"`
	Due                 int      `json:"due"`
	Refreshed           int      `json:"refreshed"`
	Failed              int      `json:"failed"`
	SkippedNoRefresh    int      `json:"skipped_no_refresh_token"`
	SkippedNoExpiry     int      `json:"skipped_no_expiry"`
	SkippedFutureExpiry int      `json:"skipped_future_expiry"`
	RefreshedIDs        []string `json:"refreshed_ids,omitempty"`
	FailedIDs           []string `json:"failed_ids,omitempty"`
	FutureExpiryIDs     []string `json:"future_expiry_ids,omitempty"`
	NoExpiryIDs         []string `json:"no_expiry_ids,omitempty"`
	NoRefreshTokenIDs   []string `json:"no_refresh_token_ids,omitempty"`
}

// refreshOptions captures operator overrides for targeted or forced refresh runs.
type refreshOptions struct {
	AuthIDs       map[string]struct{}
	Force         bool
	RefreshWindow time.Duration
}

// main parses flags, loads the configured auth store, refreshes due Codex auths, and prints JSON for cron logs.
func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}

// run is split from main so tests can execute the command flow without forking a subprocess.
func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("refresh_codex_auths", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	// Allow cron to point at an explicit config while still honoring the repo's .env bootstrap.
	configPathFlag := fs.String("config", "", "Optional config path. Defaults to config.yaml in the working directory unless PGSTORE_DSN rewires config through the Postgres spool.")
	// Dry-run lets operators verify which auths would refresh before wiring the command into cron.
	dryRun := fs.Bool("dry-run", false, "Inspect due Codex auths without refreshing or persisting them.")
	// refresh-window defaults to 48 hours so the cron can renew Codex auths ahead of expiry instead of waiting for the final day.
	refreshWindowHours := fs.Int("refresh-window-hours", defaultRefreshWindowHours, "Refresh Codex auths whose access_token expires within the next N hours.")
	// auth-id narrows the run to one or more specific auth records for live verification or emergency repair.
	var authIDs multiStringFlag
	fs.Var(&authIDs, "auth-id", "Refresh only the specified auth ID. Repeat the flag for multiple auths.")
	// force bypasses the date-based due filter so operators can verify one record end-to-end.
	force := fs.Bool("force", false, "Refresh selected Codex auths even when their JWT exp is not due today.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resources, err := loadCommandResources(ctx, strings.TrimSpace(*configPathFlag))
	if err != nil {
		return err
	}
	if resources.cleanup != nil {
		defer func() {
			if errCleanup := resources.cleanup(); errCleanup != nil {
				log.WithError(errCleanup).Warn("refresh_codex_auths: cleanup failed")
			}
		}()
	}

	stats, err := refreshDueCodexAuths(
		ctx,
		resources.store,
		executor.NewCodexAutoExecutor(resources.cfg),
		time.Now(),
		*dryRun,
		refreshOptions{
			AuthIDs:       authIDs.set(),
			Force:         *force,
			RefreshWindow: time.Duration(*refreshWindowHours) * time.Hour,
		},
	)
	if err != nil {
		return err
	}
	stats.Store = resources.storeID
	stats.DryRun = *dryRun

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(stats)
}

// multiStringFlag collects repeated string flags without forcing callers into comma-separated parsing.
type multiStringFlag []string

// String formats the repeated flag values for the standard library flag package.
func (f *multiStringFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

// Set appends one auth ID value from the CLI.
func (f *multiStringFlag) Set(value string) error {
	*f = append(*f, strings.TrimSpace(value))
	return nil
}

// set normalizes repeated flag input into a membership map for fast auth filtering.
func (f multiStringFlag) set() map[string]struct{} {
	if len(f) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(f))
	for _, item := range f {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out[trimmed] = struct{}{}
		}
	}
	return out
}

// loadCommandResources reproduces the server's env/bootstrap rules so cron reads the same store as the running service.
func loadCommandResources(ctx context.Context, explicitConfigPath string) (*commandResources, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("refresh_codex_auths: getwd failed: %w", err)
	}

	// Match the main server behavior: load .env from the working directory when present.
	if errLoad := godotenv.Load(filepath.Join(wd, ".env")); errLoad != nil && !errors.Is(errLoad, os.ErrNotExist) {
		log.WithError(errLoad).Warn("refresh_codex_auths: failed to load .env file")
	}

	lookupEnv := func(keys ...string) (string, bool) {
		for _, key := range keys {
			if value, ok := os.LookupEnv(key); ok {
				return value, true
			}
		}
		return "", false
	}

	if dsn, ok := lookupEnv("PGSTORE_DSN", "pgstore_dsn"); ok && strings.TrimSpace(dsn) != "" {
		return loadPostgresResources(ctx, wd, explicitConfigPath, lookupEnv, dsn)
	}

	return loadFileResources(wd, explicitConfigPath)
}

// loadPostgresResources bootstraps the Postgres spool exactly like cmd/server so cron sees the authoritative auth set.
func loadPostgresResources(
	ctx context.Context,
	wd string,
	explicitConfigPath string,
	lookupEnv func(...string) (string, bool),
	dsn string,
) (*commandResources, error) {
	pgStoreSchema := ""
	if value, ok := lookupEnv("PGSTORE_SCHEMA", "pgstore_schema"); ok {
		pgStoreSchema = value
	}

	pgStoreLocalPath := ""
	if value, ok := lookupEnv("PGSTORE_LOCAL_PATH", "pgstore_local_path"); ok {
		pgStoreLocalPath = value
	}
	if resolved, errResolve := util.ResolveAuthDir(pgStoreLocalPath); errResolve == nil && resolved != "" {
		pgStoreLocalPath = resolved
	}
	if pgStoreLocalPath == "" {
		if writable := util.WritablePath(); writable != "" {
			pgStoreLocalPath = writable
		} else {
			pgStoreLocalPath = wd
		}
	}
	seedRoot := pgStoreLocalPath
	spoolDir := filepath.Join(pgStoreLocalPath, "pgstore")

	pgStore, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:      dsn,
		Schema:   pgStoreSchema,
		SpoolDir: spoolDir,
		SeedRoot: seedRoot,
	})
	if err != nil {
		return nil, fmt.Errorf("refresh_codex_auths: init postgres store failed: %w", err)
	}

	examplePath := filepath.Join(wd, "config.example.yaml")
	if explicitConfigPath != "" {
		examplePath = explicitConfigPath
	}
	if err = pgStore.Bootstrap(ctx, examplePath); err != nil {
		_ = pgStore.Close()
		return nil, fmt.Errorf("refresh_codex_auths: bootstrap postgres store failed: %w", err)
	}

	cfg, err := config.LoadConfigOptional(pgStore.ConfigPath(), false)
	if err != nil {
		_ = pgStore.Close()
		return nil, fmt.Errorf("refresh_codex_auths: load postgres-backed config failed: %w", err)
	}
	cfg.AuthDir = pgStore.AuthDir()

	return &commandResources{
		cfg:     cfg,
		store:   pgStore,
		storeID: "pgstore",
		cleanup: pgStore.Close,
	}, nil
}

// loadFileResources initializes the legacy file-backed token store for non-Postgres environments.
func loadFileResources(wd, explicitConfigPath string) (*commandResources, error) {
	configPath := explicitConfigPath
	if configPath == "" {
		configPath = filepath.Join(wd, defaultRefreshCommandConfig)
	}

	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		return nil, fmt.Errorf("refresh_codex_auths: load config failed: %w", err)
	}
	authDir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err == nil && authDir != "" {
		cfg.AuthDir = authDir
	}

	fileStore := sdkauth.NewFileTokenStore()
	fileStore.SetBaseDir(cfg.AuthDir)

	return &commandResources{
		cfg:     cfg,
		store:   fileStore,
		storeID: "file",
		cleanup: nil,
	}, nil
}

// refreshDueCodexAuths scans the store, refreshes only Codex auths whose access_token expires inside the configured window, and upserts them.
func refreshDueCodexAuths(
	ctx context.Context,
	store cliproxyauth.Store,
	exec refreshExecutor,
	now time.Time,
	dryRun bool,
	opts refreshOptions,
) (*refreshStats, error) {
	if store == nil {
		return nil, fmt.Errorf("refresh_codex_auths: store is nil")
	}
	if exec == nil {
		return nil, fmt.Errorf("refresh_codex_auths: executor is nil")
	}

	auths, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("refresh_codex_auths: list auths failed: %w", err)
	}

	stats := &refreshStats{Scanned: len(auths)}
	for _, auth := range auths {
		if !isCodexOAuthAuth(auth) {
			continue
		}
		if !matchesRequestedAuth(auth, opts.AuthIDs) {
			continue
		}

		expiry, ok := accessTokenExpiry(auth)
		if !ok {
			stats.SkippedNoExpiry++
			stats.NoExpiryIDs = append(stats.NoExpiryIDs, auth.ID)
			continue
		}
		if !opts.Force && !expiresWithinWindow(now, expiry, opts.RefreshWindow) {
			stats.SkippedFutureExpiry++
			stats.FutureExpiryIDs = append(stats.FutureExpiryIDs, auth.ID)
			continue
		}

		// Count the auth as due as soon as its access token falls into the configured refresh window.
		stats.Due++

		refreshToken := metadataString(auth, "refresh_token")
		if strings.TrimSpace(refreshToken) == "" {
			stats.SkippedNoRefresh++
			stats.NoRefreshTokenIDs = append(stats.NoRefreshTokenIDs, auth.ID)
			continue
		}
		if dryRun {
			stats.RefreshedIDs = append(stats.RefreshedIDs, auth.ID)
			continue
		}

		updated, errRefresh := exec.Refresh(ctx, auth.Clone())
		if errRefresh != nil {
			stats.Failed++
			stats.FailedIDs = append(stats.FailedIDs, auth.ID)
			log.WithError(errRefresh).Warnf("refresh_codex_auths: refresh failed for %s", auth.ID)
			continue
		}
		if updated == nil {
			updated = auth.Clone()
		}

		if _, errSave := store.Save(ctx, updated); errSave != nil {
			stats.Failed++
			stats.FailedIDs = append(stats.FailedIDs, auth.ID)
			log.WithError(errSave).Warnf("refresh_codex_auths: save failed for %s", auth.ID)
			continue
		}

		stats.Refreshed++
		stats.RefreshedIDs = append(stats.RefreshedIDs, auth.ID)
	}

	return stats, nil
}

// isCodexOAuthAuth keeps the cron narrowly scoped to file-backed OAuth Codex credentials only.
func isCodexOAuthAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "codex")
}

// matchesRequestedAuth keeps default runs broad while letting operators target one record for live verification.
func matchesRequestedAuth(auth *cliproxyauth.Auth, requested map[string]struct{}) bool {
	if auth == nil {
		return false
	}
	if len(requested) == 0 {
		return true
	}
	_, ok := requested[strings.TrimSpace(auth.ID)]
	return ok
}

// accessTokenExpiry extracts the access token JWT exp claim and falls back to Auth.ExpirationTime when needed.
func accessTokenExpiry(auth *cliproxyauth.Auth) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	accessToken := metadataString(auth, "access_token")
	if strings.TrimSpace(accessToken) != "" {
		claims, err := codexauth.ParseJWTToken(accessToken)
		if err == nil && claims != nil && claims.Exp > 0 {
			return time.Unix(int64(claims.Exp), 0), true
		}
	}
	return auth.ExpirationTime()
}

// expiresWithinWindow treats any token that expires within the configured look-ahead window as due for cron refresh.
func expiresWithinWindow(now, expiry time.Time, window time.Duration) bool {
	if expiry.IsZero() {
		return false
	}
	if window <= 0 {
		window = defaultRefreshWindowHours * time.Hour
	}
	return !expiry.After(now.Add(window))
}

// metadataString reads one top-level metadata string field without assuming the map exists.
func metadataString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}
