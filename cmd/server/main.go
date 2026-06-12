// Package main provides the entry point for the CLI Proxy API server.
// This server acts as a proxy that provides OpenAI/Gemini/Claude compatible API interfaces
// for CLI models, allowing CLI models to be used with tools and libraries designed for standard AI APIs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	configaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/cmd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/tui"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var (
	Version           = "dev"
	Commit            = "none"
	BuildDate         = "unknown"
	DefaultConfigPath = ""
)

// init initializes the shared logger setup.
func init() {
	logging.SetupBaseLogger()
	buildinfo.Version = Version
	buildinfo.Commit = Commit
	buildinfo.BuildDate = BuildDate
}

// main is the entry point of the application.
// It parses command-line flags, loads configuration, and starts the appropriate
// service based on the provided flags (login, codex-login, or server mode).
func main() {
	fmt.Printf("CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)

	// Command-line flags to control the application's behavior.
	var login bool
	var codexLogin bool
	var codexDeviceLogin bool
	var claudeLogin bool
	var noBrowser bool
	var oauthCallbackPort int
	var antigravityLogin bool
	var kimiLogin bool
	var kiroLogin bool
	var kiroImport string
	var kiroIDCStartURL string
	var kiroRegion string
	var kiroUsername string
	var xaiLogin bool
	var projectID string
	var vertexImport string
	var vertexImportPrefix string
	var configPath string
	var password string
	var homeJWT string
	var homeDisableClusterDiscovery bool
	var tuiMode bool
	var standalone bool
	var localModel bool

	// Define command-line flags for different operation modes.
	flag.BoolVar(&login, "login", false, "Login Google Account")
	flag.BoolVar(&codexLogin, "codex-login", false, "Login to Codex using OAuth")
	flag.BoolVar(&codexDeviceLogin, "codex-device-login", false, "Login to Codex using device code flow")
	flag.BoolVar(&claudeLogin, "claude-login", false, "Login to Claude using OAuth")
	flag.BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically for OAuth")
	flag.IntVar(&oauthCallbackPort, "oauth-callback-port", 0, "Override OAuth callback port (defaults to provider-specific port)")
	flag.BoolVar(&antigravityLogin, "antigravity-login", false, "Login to Antigravity using OAuth")
	flag.BoolVar(&kimiLogin, "kimi-login", false, "Login to Kimi using OAuth")
	flag.BoolVar(&kiroLogin, "kiro-login", false, "Login to Kiro (AWS CodeWhisperer) using OAuth")
	flag.StringVar(&kiroImport, "kiro-import", "", "Import a Kiro refresh token (empty value auto-detects from ~/.aws/sso/cache)")
	flag.StringVar(&kiroIDCStartURL, "kiro-idc-start-url", "", "Kiro IAM Identity Center start URL (enables the IDC login method)")
	flag.StringVar(&kiroRegion, "kiro-region", "", "AWS region for Kiro OIDC endpoints (defaults to us-east-1)")
	flag.StringVar(&kiroUsername, "kiro-username", "", "Account label for the Kiro auth file (required for IDC login; names kiro-<username>-<directoryID>.json)")
	flag.BoolVar(&xaiLogin, "xai-login", false, "Login to xAI using OAuth")
	flag.StringVar(&projectID, "project_id", "", "Project ID (Gemini only, not required)")
	flag.StringVar(&configPath, "config", DefaultConfigPath, "Configure File Path")
	flag.StringVar(&vertexImport, "vertex-import", "", "Import Vertex service account key JSON file")
	flag.StringVar(&vertexImportPrefix, "vertex-import-prefix", "", "Prefix for Vertex model namespacing (use with -vertex-import)")
	flag.StringVar(&password, "password", "", "")
	flag.StringVar(&homeJWT, "home-jwt", "", "Home control plane JWT for mTLS certificate bootstrap and connection")
	flag.BoolVar(&homeDisableClusterDiscovery, "home-disable-cluster-discovery", false, "Disable Home CLUSTER NODES discovery and keep using the configured -home-jwt address")
	flag.BoolVar(&tuiMode, "tui", false, "Start with terminal management UI")
	flag.BoolVar(&standalone, "standalone", false, "In TUI mode, start an embedded local server")
	flag.BoolVar(&localModel, "local-model", false, "Use embedded model catalog only, skip remote model fetching")

	flag.CommandLine.Usage = func() {
		out := flag.CommandLine.Output()
		_, _ = fmt.Fprintf(out, "Usage of %s\n", os.Args[0])
		flag.CommandLine.VisitAll(func(f *flag.Flag) {
			if f.Name == "password" {
				return
			}
			s := fmt.Sprintf("  -%s", f.Name)
			name, unquoteUsage := flag.UnquoteUsage(f)
			if name != "" {
				s += " " + name
			}
			if len(s) <= 4 {
				s += "	"
			} else {
				s += "\n    "
			}
			if unquoteUsage != "" {
				s += unquoteUsage
			}
			if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
				s += fmt.Sprintf(" (default %s)", f.DefValue)
			}
			_, _ = fmt.Fprint(out, s+"\n")
		})
	}

	// Parse the command-line flags.
	flag.Parse()

	// Core application variables.
	var err error
	var cfg *config.Config
	var isCloudDeploy bool
	var configLoadedFromHome bool
	var (
		usePostgresStore      bool
		pgStoreDSN            string
		pgStoreSchema         string
		pgStoreLocalPath      string
		pgStoreStatsLocalPath string
		pgStoreInst           *store.PostgresStore
		useGitStore           bool
		gitStoreRemoteURL     string
		gitStoreUser          string
		gitStorePassword      string
		gitStoreBranch        string
		gitStoreLocalPath     string
		gitStoreInst          *store.GitTokenStore
		gitStoreRoot          string
		useObjectStore        bool
		objectStoreEndpoint   string
		objectStoreAccess     string
		objectStoreSecret     string
		objectStoreBucket     string
		objectStoreLocalPath  string
		objectStoreInst       *store.ObjectTokenStore
	)

	wd, err := os.Getwd()
	if err != nil {
		log.Errorf("failed to get working directory: %v", err)
		return
	}

	// Load environment variables from .env if present.
	if errLoad := godotenv.Load(filepath.Join(wd, ".env")); errLoad != nil {
		if !errors.Is(errLoad, os.ErrNotExist) {
			log.WithError(errLoad).Warn("failed to load .env file")
		}
	}

	lookupEnv := func(keys ...string) (string, bool) {
		for _, key := range keys {
			if value, ok := os.LookupEnv(key); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					return trimmed, true
				}
			}
		}
		return "", false
	}
	writableBase := util.WritablePath()

	if strings.TrimSpace(homeJWT) == "" {
		if v, ok := lookupEnv("HOME_JWT", "home_jwt"); ok {
			homeJWT = v
		}
	}

	if value, ok := lookupEnv("PGSTORE_DSN", "pgstore_dsn"); ok {
		usePostgresStore = true
		pgStoreDSN = value
	}
	if usePostgresStore {
		if value, ok := lookupEnv("PGSTORE_SCHEMA", "pgstore_schema"); ok {
			pgStoreSchema = value
		}
		if value, ok := lookupEnv("PGSTORE_LOCAL_PATH", "pgstore_local_path"); ok {
			pgStoreLocalPath = value
		}
		if resolvedPath, errResolvePath := util.ResolveAuthDir(pgStoreLocalPath); errResolvePath == nil && resolvedPath != "" {
			pgStoreLocalPath = resolvedPath
		}
		if pgStoreLocalPath == "" {
			if writableBase != "" {
				pgStoreLocalPath = writableBase
			} else {
				pgStoreLocalPath = wd
			}
		}
		pgStoreStatsLocalPath = pgStoreLocalPath
		useGitStore = false
	}
	if value, ok := lookupEnv("GITSTORE_GIT_URL", "gitstore_git_url"); ok {
		useGitStore = true
		gitStoreRemoteURL = value
	}
	if value, ok := lookupEnv("GITSTORE_GIT_USERNAME", "gitstore_git_username"); ok {
		gitStoreUser = value
	}
	if value, ok := lookupEnv("GITSTORE_GIT_TOKEN", "gitstore_git_token"); ok {
		gitStorePassword = value
	}
	if value, ok := lookupEnv("GITSTORE_LOCAL_PATH", "gitstore_local_path"); ok {
		gitStoreLocalPath = value
	}
	if value, ok := lookupEnv("GITSTORE_GIT_BRANCH", "gitstore_git_branch"); ok {
		gitStoreBranch = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_ENDPOINT", "objectstore_endpoint"); ok {
		useObjectStore = true
		objectStoreEndpoint = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_ACCESS_KEY", "objectstore_access_key"); ok {
		objectStoreAccess = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_SECRET_KEY", "objectstore_secret_key"); ok {
		objectStoreSecret = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_BUCKET", "objectstore_bucket"); ok {
		objectStoreBucket = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_LOCAL_PATH", "objectstore_local_path"); ok {
		objectStoreLocalPath = value
	}

	// Check for cloud deploy mode only on first execution
	// Read env var name in uppercase: DEPLOY
	deployEnv := os.Getenv("DEPLOY")
	if deployEnv == "cloud" {
		isCloudDeploy = true
	}

	// Determine and load the configuration file.
	// Prefer the Postgres store when configured, otherwise fallback to git or local files.
	var configFilePath string
	// fallbackToLocalStore degrades to the local file-backed config/token store when a remote
	// store backend (postgres/object/git) cannot be initialized. Without this, a stale or
	// unreachable PGSTORE_DSN (or object/git config) makes startup return early, and under
	// launchd KeepAlive / systemd Restart=always the service crash-loops instead of coming up.
	// Falling back keeps the proxy bootable for users who do not run those backends.
	fallbackToLocalStore := func(backend string, cause error) {
		log.Warnf("%s unavailable (%v); falling back to local file-backed store — auth/config/usage state stays local to this node and will not sync to the remote store", backend, cause)
		// Clear every remote-store selection so both token-store registration and
		// usage-statistics persistence resolve to local backends later in startup.
		usePostgresStore = false
		useObjectStore = false
		useGitStore = false
		pgStoreDSN = ""
		pgStoreInst = nil
		objectStoreInst = nil
		gitStoreInst = nil
		// Mirror the no-store default branch: prefer an explicit -config path, else wd/config.yaml.
		if strings.TrimSpace(configPath) != "" {
			configFilePath = configPath
		} else {
			configFilePath = filepath.Join(wd, "config.yaml")
		}
		cfg, err = config.LoadConfigOptional(configFilePath, isCloudDeploy)
	}
	if strings.TrimSpace(homeJWT) != "" {
		configLoadedFromHome = true
		ctxHome, cancelHome := context.WithTimeout(context.Background(), 30*time.Second)
		homeCfg, errHomeCfg := home.ConfigFromJWT(ctxHome, homeJWT)
		cancelHome()
		if errHomeCfg != nil {
			log.Errorf("invalid -home-jwt: %v", errHomeCfg)
			return
		}
		if homeDisableClusterDiscovery {
			homeCfg.DisableClusterDiscovery = true
		}
		homeClient := home.New(homeCfg)
		defer homeClient.Close()

		ctxHomeConfig, cancelHomeConfig := context.WithTimeout(context.Background(), 30*time.Second)
		raw, errGetConfig := homeClient.GetConfig(ctxHomeConfig)
		cancelHomeConfig()
		if errGetConfig != nil {
			log.Errorf("failed to fetch config from home: %v", errGetConfig)
			return
		}

		parsed, errParseConfig := config.ParseConfigBytes(raw)
		if errParseConfig != nil {
			log.Errorf("failed to parse config payload from home: %v", errParseConfig)
			return
		}
		if parsed == nil {
			parsed = &config.Config{}
		}
		parsed.Home = homeCfg
		parsed.Port = 8317 // Default to 8317 for home mode, can be overridden by home config
		parsed.UsageStatisticsEnabled = true
		cfg = parsed

		// Keep a non-empty config path for downstream components (log paths, management assets, etc),
		// but do not require the file to exist when loading config from home.
		if strings.TrimSpace(configPath) != "" {
			configFilePath = configPath
		} else {
			configFilePath = filepath.Join(wd, "config.yaml")
		}

		// Local stores are intentionally disabled when config is loaded from home.
		usePostgresStore = false
		useObjectStore = false
		useGitStore = false
	} else if usePostgresStore {
		// Initialize the postgres-backed store inside a closure so any failure can degrade to
		// the local file store (fallbackToLocalStore) instead of aborting startup.
		if errStore := func() error {
			if pgStoreLocalPath == "" {
				pgStoreLocalPath = wd
			}
			pgStoreLocalPath = filepath.Join(pgStoreLocalPath, "pgstore")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			var errInit error
			pgStoreInst, errInit = store.NewPostgresStore(ctx, store.PostgresStoreConfig{
				DSN:      pgStoreDSN,
				Schema:   pgStoreSchema,
				SpoolDir: pgStoreLocalPath,
				SeedRoot: pgStoreStatsLocalPath,
			})
			cancel()
			if errInit != nil {
				return fmt.Errorf("initialize postgres token store: %w", errInit)
			}
			examplePath := filepath.Join(wd, "config.example.yaml")
			ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
			if errBootstrap := pgStoreInst.Bootstrap(ctx, examplePath); errBootstrap != nil {
				cancel()
				return fmt.Errorf("bootstrap postgres-backed config: %w", errBootstrap)
			}
			cancel()
			configFilePath = pgStoreInst.ConfigPath()
			cfg, err = config.LoadConfigOptional(configFilePath, isCloudDeploy)
			if err == nil {
				cfg.AuthDir = pgStoreInst.AuthDir()
				log.Infof("postgres-backed token store enabled, workspace path: %s", pgStoreInst.WorkDir())
			}
			return nil
		}(); errStore != nil {
			fallbackToLocalStore("postgres token store", errStore)
		}
	} else if useObjectStore {
		// Initialize the object-backed store inside a closure so any failure (bad endpoint,
		// unreachable bucket, bootstrap error) degrades to the local file store instead of
		// aborting startup.
		if errStore := func() error {
			if objectStoreLocalPath == "" {
				if writableBase != "" {
					objectStoreLocalPath = writableBase
				} else {
					objectStoreLocalPath = wd
				}
			}
			objectStoreRoot := filepath.Join(objectStoreLocalPath, "objectstore")
			resolvedEndpoint := strings.TrimSpace(objectStoreEndpoint)
			useSSL := true
			if strings.Contains(resolvedEndpoint, "://") {
				parsed, errParse := url.Parse(resolvedEndpoint)
				if errParse != nil {
					return fmt.Errorf("parse object store endpoint %q: %w", objectStoreEndpoint, errParse)
				}
				switch strings.ToLower(parsed.Scheme) {
				case "http":
					useSSL = false
				case "https":
					useSSL = true
				default:
					return fmt.Errorf("unsupported object store scheme %q (only http and https are allowed)", parsed.Scheme)
				}
				if parsed.Host == "" {
					return fmt.Errorf("object store endpoint %q is missing host information", objectStoreEndpoint)
				}
				resolvedEndpoint = parsed.Host
				if parsed.Path != "" && parsed.Path != "/" {
					resolvedEndpoint = strings.TrimSuffix(parsed.Host+parsed.Path, "/")
				}
			}
			resolvedEndpoint = strings.TrimRight(resolvedEndpoint, "/")
			objCfg := store.ObjectStoreConfig{
				Endpoint:  resolvedEndpoint,
				Bucket:    objectStoreBucket,
				AccessKey: objectStoreAccess,
				SecretKey: objectStoreSecret,
				LocalRoot: objectStoreRoot,
				UseSSL:    useSSL,
				PathStyle: true,
			}
			var errInit error
			objectStoreInst, errInit = store.NewObjectTokenStore(objCfg)
			if errInit != nil {
				return fmt.Errorf("initialize object token store: %w", errInit)
			}
			examplePath := filepath.Join(wd, "config.example.yaml")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if errBootstrap := objectStoreInst.Bootstrap(ctx, examplePath); errBootstrap != nil {
				cancel()
				return fmt.Errorf("bootstrap object-backed config: %w", errBootstrap)
			}
			cancel()
			configFilePath = objectStoreInst.ConfigPath()
			cfg, err = config.LoadConfigOptional(configFilePath, isCloudDeploy)
			if err == nil {
				if cfg == nil {
					cfg = &config.Config{}
				}
				cfg.AuthDir = objectStoreInst.AuthDir()
				log.Infof("object-backed token store enabled, bucket: %s", objectStoreBucket)
			}
			return nil
		}(); errStore != nil {
			fallbackToLocalStore("object token store", errStore)
		}
	} else if useGitStore {
		// Initialize the git-backed store inside a closure so any failure (clone/commit error,
		// missing template) degrades to the local file store instead of aborting startup.
		if errStore := func() error {
			if gitStoreLocalPath == "" {
				if writableBase != "" {
					gitStoreLocalPath = writableBase
				} else {
					gitStoreLocalPath = wd
				}
			}
			gitStoreRoot = filepath.Join(gitStoreLocalPath, "gitstore")
			authDir := filepath.Join(gitStoreRoot, "auths")
			gitStoreInst = store.NewGitTokenStore(gitStoreRemoteURL, gitStoreUser, gitStorePassword, gitStoreBranch)
			gitStoreInst.SetBaseDir(authDir)
			if errRepo := gitStoreInst.EnsureRepository(); errRepo != nil {
				return fmt.Errorf("prepare git token store: %w", errRepo)
			}
			configFilePath = gitStoreInst.ConfigPath()
			if configFilePath == "" {
				configFilePath = filepath.Join(gitStoreRoot, "config", "config.yaml")
			}
			if _, statErr := os.Stat(configFilePath); errors.Is(statErr, fs.ErrNotExist) {
				examplePath := filepath.Join(wd, "config.example.yaml")
				if _, errExample := os.Stat(examplePath); errExample != nil {
					return fmt.Errorf("find template config file: %w", errExample)
				}
				if errCopy := misc.CopyConfigTemplate(examplePath, configFilePath); errCopy != nil {
					return fmt.Errorf("bootstrap git-backed config: %w", errCopy)
				}
				if errCommit := gitStoreInst.PersistConfig(context.Background()); errCommit != nil {
					return fmt.Errorf("commit initial git-backed config: %w", errCommit)
				}
				log.Infof("git-backed config initialized from template: %s", configFilePath)
			} else if statErr != nil {
				return fmt.Errorf("inspect git-backed config: %w", statErr)
			}
			cfg, err = config.LoadConfigOptional(configFilePath, isCloudDeploy)
			if err == nil {
				cfg.AuthDir = gitStoreInst.AuthDir()
				log.Infof("git-backed token store enabled, repository path: %s", gitStoreRoot)
			}
			return nil
		}(); errStore != nil {
			fallbackToLocalStore("git token store", errStore)
		}
	} else if configPath != "" {
		configFilePath = configPath
		cfg, err = config.LoadConfigOptional(configPath, isCloudDeploy)
	} else {
		wd, err = os.Getwd()
		if err != nil {
			log.Errorf("failed to get working directory: %v", err)
			return
		}
		configFilePath = filepath.Join(wd, "config.yaml")
		cfg, err = config.LoadConfigOptional(configFilePath, isCloudDeploy)
	}
	if err != nil {
		log.Errorf("failed to load config: %v", err)
		return
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	// In cloud deploy mode, check if we have a valid configuration
	var configFileExists bool
	if isCloudDeploy {
		if configLoadedFromHome && cfg != nil {
			configFileExists = cfg.Port != 0
		} else {
			if info, errStat := os.Stat(configFilePath); errStat != nil {
				// Don't mislead: API server will not start until configuration is provided.
				log.Info("Cloud deploy mode: No configuration file detected; standing by for configuration")
				configFileExists = false
			} else if info.IsDir() {
				log.Info("Cloud deploy mode: Config path is a directory; standing by for configuration")
				configFileExists = false
			} else if cfg.Port == 0 {
				// LoadConfigOptional returns empty config when file is empty or invalid.
				// Config file exists but is empty or invalid; treat as missing config
				log.Info("Cloud deploy mode: Configuration file is empty or invalid; standing by for valid configuration")
				configFileExists = false
			} else {
				log.Info("Cloud deploy mode: Configuration file detected; starting service")
				configFileExists = true
			}
		}
	}
	// Default the listen port for ordinary installs when the config omits it (e.g. the installer's
	// minimal config writes only auth-dir + usage stats). Without this the server binds ":0" and
	// the kernel assigns a random ephemeral port, leaving the proxy unreachable at a known address.
	// Cloud-deploy mode is skipped on purpose: it treats Port == 0 as the "no valid config yet"
	// sentinel above and fills the port from configuration delivered later.
	if !isCloudDeploy && cfg.Port == 0 {
		cfg.Port = 8317
		log.Infof("no listen port configured; defaulting to %d", cfg.Port)
	}
	usage.SetStatisticsEnabled(cfg.UsageStatisticsEnabled)
	redisqueue.SetRetentionSeconds(cfg.RedisUsageQueueRetentionSeconds)
	coreauth.SetQuotaCooldownDisabled(cfg.DisableCooling)

	if err = logging.ConfigureLogOutput(cfg); err != nil {
		log.Errorf("failed to configure log output: %v", err)
		return
	}

	log.Infof("CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)

	// Set the log level based on the configuration.
	util.SetLogLevel(cfg)

	if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
		log.Errorf("failed to resolve auth directory: %v", errResolveAuthDir)
		return
	} else {
		cfg.AuthDir = resolvedAuthDir
	}
	usage.SetPersistentStoreOptions(usage.PersistentStoreOptions{
		PostgresDSN:       pgStoreDSN,
		PostgresSchema:    pgStoreSchema,
		PostgresLocalPath: pgStoreStatsLocalPath,
		RequirePostgres:   usePostgresStore,
	})
	managementasset.SetCurrentConfig(cfg)

	// Create login options to be used in authentication flows.
	options := &cmd.LoginOptions{
		NoBrowser:    noBrowser,
		CallbackPort: oauthCallbackPort,
	}

	// Register the shared token store once so all components use the same persistence backend.
	if usePostgresStore {
		sdkAuth.RegisterTokenStore(pgStoreInst)
	} else if useObjectStore {
		sdkAuth.RegisterTokenStore(objectStoreInst)
	} else if useGitStore {
		sdkAuth.RegisterTokenStore(gitStoreInst)
	} else {
		sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())
	}

	// Register built-in access providers before constructing services.
	configaccess.Register(&cfg.SDKConfig)

	// Handle different command modes based on the provided flags.

	if vertexImport != "" {
		// Handle Vertex service account import
		cmd.DoVertexImport(cfg, vertexImport, vertexImportPrefix)
	} else if login {
		// Handle Google/Gemini login
		cmd.DoLogin(cfg, projectID, options)
	} else if antigravityLogin {
		// Handle Antigravity login
		cmd.DoAntigravityLogin(cfg, options)
	} else if codexLogin {
		// Handle Codex login
		cmd.DoCodexLogin(cfg, options)
	} else if codexDeviceLogin {
		// Handle Codex device-code login
		cmd.DoCodexDeviceLogin(cfg, options)
	} else if claudeLogin {
		// Handle Claude login
		cmd.DoClaudeLogin(cfg, options)
	} else if kimiLogin {
		cmd.DoKimiLogin(cfg, options)
	} else if kiroLogin {
		cmd.DoKiroLogin(cfg, options, kiroIDCStartURL, kiroRegion, kiroImport, kiroUsername)
	} else if xaiLogin {
		cmd.DoXAILogin(cfg, options)
	} else {
		// In cloud deploy mode without config file, just wait for shutdown signals
		if isCloudDeploy && !configFileExists {
			// No config file available, just wait for shutdown
			cmd.WaitForCloudDeploy()
			return
		}
		if localModel && (!tuiMode || standalone) {
			log.Info("Local model mode: using embedded model catalog, remote model updates disabled")
		}
		if tuiMode {
			if standalone {
				// Standalone mode: start an embedded local server and connect TUI client to it.
				managementasset.StartAutoUpdater(context.Background(), configFilePath)
				misc.StartAntigravityVersionUpdater(context.Background())
				if !localModel && !cfg.Home.Enabled {
					registry.StartModelsUpdater(context.Background())
				} else if cfg.Home.Enabled {
					log.Info("Home mode: remote model updates disabled")
				}
				hook := tui.NewLogHook(2000)
				hook.SetFormatter(&logging.LogFormatter{})
				log.AddHook(hook)

				origStdout := os.Stdout
				origStderr := os.Stderr
				origLogOutput := log.StandardLogger().Out
				log.SetOutput(io.Discard)

				devNull, errOpenDevNull := os.Open(os.DevNull)
				if errOpenDevNull == nil {
					os.Stdout = devNull
					os.Stderr = devNull
				}

				restoreIO := func() {
					os.Stdout = origStdout
					os.Stderr = origStderr
					log.SetOutput(origLogOutput)
					if devNull != nil {
						_ = devNull.Close()
					}
				}

				localMgmtPassword := fmt.Sprintf("tui-%d-%d", os.Getpid(), time.Now().UnixNano())
				if password == "" {
					password = localMgmtPassword
				}

				cancel, done := cmd.StartServiceBackground(cfg, configFilePath, password)

				client := tui.NewClient(cfg.Port, password)
				ready := false
				backoff := 100 * time.Millisecond
				for i := 0; i < 30; i++ {
					if _, errGetConfig := client.GetConfig(); errGetConfig == nil {
						ready = true
						break
					}
					time.Sleep(backoff)
					if backoff < time.Second {
						backoff = time.Duration(float64(backoff) * 1.5)
					}
				}

				if !ready {
					restoreIO()
					cancel()
					<-done
					fmt.Fprintf(os.Stderr, "TUI error: embedded server is not ready\n")
					return
				}

				if errRun := tui.Run(cfg.Port, password, hook, origStdout); errRun != nil {
					restoreIO()
					fmt.Fprintf(os.Stderr, "TUI error: %v\n", errRun)
				} else {
					restoreIO()
				}

				cancel()
				<-done
			} else {
				// Default TUI mode: pure management client.
				// The proxy server must already be running.
				if errRun := tui.Run(cfg.Port, password, nil, os.Stdout); errRun != nil {
					fmt.Fprintf(os.Stderr, "TUI error: %v\n", errRun)
				}
			}
		} else {
			// Start the main proxy service
			managementasset.StartAutoUpdater(context.Background(), configFilePath)
			misc.StartAntigravityVersionUpdater(context.Background())
			if !localModel && !cfg.Home.Enabled {
				registry.StartModelsUpdater(context.Background())
			} else if cfg.Home.Enabled {
				log.Info("Home mode: remote model updates disabled")
			}
			cmd.StartService(cfg, configFilePath, password)
		}
	}
}
