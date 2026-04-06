# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Common commands

- `cp config.example.yaml config.yaml` - bootstrap local config before first run.
- `go build -o ./cli-caching-proxy-test ./cmd/server` - build the main server binary used in the README local workflow.
- `./cli-caching-proxy-test` - run the proxy locally.
- `docker compose up -d --build` - run the proxy via Docker Compose.
- `./install_mac.sh` - guided macOS service install.
- `./install_linux.sh` - guided Linux user-service install.
- `pwsh -File .\\install_windows.ps1` - guided Windows install.
- `go test ./...` - run the full Go test suite.
- `go test ./path/to/package` - run tests for a single package.
- `go test ./path/to/package -run '^TestName$'` - run a single Go test.
- `go build -o test-output ./cmd/server` - matches the PR build check in CI.
- No dedicated lint command is defined in the repo or CI workflows; the enforced CI gate is the server build.

## Architecture

- `cmd/server/main.go` is the CLI entrypoint: it parses login/TUI/server flags, loads config and token-store backends, and blank-imports `internal/translator` so built-in translators self-register.
- `internal/cmd/run.go` bridges CLI startup into `sdk/cliproxy.Service`; the reusable embedding surface lives under `sdk/cliproxy`, while `internal/*` holds the concrete server/runtime implementation.
- `sdk/cliproxy/builder.go` wires config path, auth/access managers, selector strategy (`round-robin` by default, `fill-first` when configured), round-tripper provider, and server options.
- `sdk/cliproxy/service.go` owns lifecycle concerns: HTTP server startup, config/auth watching, auth update queue processing, websocket-backed provider registration, and hot reload/rebinding.
- `internal/api/server.go` builds the Gin server, middleware stack, `/v1` and `/v1beta` routes, OAuth callbacks, `/healthz`, and conditional `/v0/management` routes.
- `sdk/api/handlers/handlers.go` is the request bridge from HTTP handlers into `coreexecutor.Request` / `Options`; non-streaming, count, and streaming requests all flow through the core auth manager from here.
- `sdk/api/handlers/request_route.go` is the route classification layer that decides normalized model/provider routing before execution; start here when debugging Claude-via-GPT or provider-specific path behavior.
- `sdk/cliproxy/auth/conductor.go` is the runtime routing core: provider normalization, credential selection, cooldown/retry/failover, and executor dispatch.
- `internal/runtime/executor/*` and `internal/translator/*` implement provider-specific translation/execution; executors translate inbound payloads to upstream formats, call the upstream, then translate responses back.
- `sdk/translator` is the public translator registry; built-in translators are registered by the side-effect import in `internal/translator/init.go`.
- `docs/sdk-usage.md` and `docs/sdk-advanced.md` are the main references when embedding the proxy as a Go library instead of running `cmd/server`.
- Management routes are only mounted when `remote-management.secret-key`, `MANAGEMENT_PASSWORD`, or a local management password is present.
- Hot reload is a first-class behavior: changes to `config.yaml` and the auth directory are picked up automatically by the running service.
