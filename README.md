# CLI Proxy API

English | [中文](README_CN.md) | [日本語](README_JA.md)

A proxy server that exposes OpenAI, Gemini, Claude, and Codex compatible API interfaces for CLI tools and SDKs. It supports OAuth-backed access for Claude Code, OpenAI Codex (GPT models), Qwen Code, iFlow, and other compatible clients, plus configurable upstream routing and multi-account load balancing.

> A fork of the original [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).

## Installation

Pick the install method for your platform. Each installer is interactive and walks you through config, service setup, and optional Postgres provisioning.

### macOS service

```bash
./install_mac.sh
```

### Linux user service

```bash
./install_linux.sh
```

### Windows

```powershell
pwsh -File .\install_windows.ps1
```

### Run locally with Go

```bash
cp config.example.yaml config.yaml   # then edit: port, auth-dir, api-keys, provider credentials
go build -o ./cli-caching-proxy-test ./cmd/server
./cli-caching-proxy-test
```

### Run with Docker Compose

```bash
docker compose up -d --build
```

The compose file mounts:
- `./config.yaml` → `/CLIProxyAPI/config.yaml`
- `./auths` → `/root/.cli-proxy-api`
- `./logs` → `/CLIProxyAPI/logs`

### Optional: Postgres-backed storage

Create a `.env` in the server working directory to back auth, config, and usage
statistics with Postgres:

```bash
cat > .env <<'EOF'
PGSTORE_DSN=postgresql://cheaprouter:cheaprouter@localhost:5432/cliproxy
PGSTORE_SCHEMA=public
PGSTORE_LOCAL_PATH=~/.cli-cache-proxy
EOF
```

On first startup the proxy uses the same `PGSTORE_*` surface for auth, config, and
usage statistics, importing local legacy auth/stats files from `PGSTORE_LOCAL_PATH`
when the Postgres tables are empty. The installers can also write these values into
the install-root `.env`, validate the DSN, and try to provision the Postgres
role/database automatically — printing the exact bash commands to run manually if
provisioning fails.

## Configuration

Review `config.yaml` before first run, especially:
- `port` — listen port (default `8317`)
- `auth-dir` — directory for OAuth auth files
- `api-keys` — client-facing API keys
- provider credentials / OAuth auth files
- `remote-management.panel-github-repository` — source repo for the management control panel

## Usage

### Basic startup

After configuring `config.yaml`, start the proxy and point your client at the configured port. Health endpoint:

```bash
curl http://127.0.0.1:8317/healthz
```

### Claude-compatible usage

```bash
ANTHROPIC_BASE_URL="http://127.0.0.1:8317" \
ANTHROPIC_AUTH_TOKEN="your-api-key-1" \
claude --model "claude-sonnet-4-5-20250929" -p 'respond to me exactly "hello"'
```

### GPT / Codex routing

```bash
ANTHROPIC_DEFAULT_OPUS_MODEL='gpt-5.4' \
ANTHROPIC_DEFAULT_SONNET_MODEL='gpt-5.3-codex' \
ANTHROPIC_DEFAULT_HAIKU_MODEL='gpt-5.3-codex' \
ANTHROPIC_BASE_URL="http://127.0.0.1:8317" \
ANTHROPIC_AUTH_TOKEN="your-api-key-1" \
claude --model 'gpt-5.4' -p 'respond to me exactly "hello"'
```

### Provider-specific routes

When you need the request/response shape of a specific backend family, use provider-specific paths instead of the merged `/v1/...` endpoints:

- `/api/provider/{provider}/v1/messages`
- `/api/provider/{provider}/v1beta/models/...`
- `/api/provider/{provider}/v1/chat/completions`

## Features

- OpenAI / Gemini / Claude / Grok compatible API endpoints for CLI models
- OpenAI Codex support (GPT models) via OAuth login
- Claude Code support via OAuth login
- Grok Build support via OAuth login
- Amazon Kiro (AWS CodeWhisperer) support via OAuth login (`--kiro-login`): free Claude/GLM/MiniMax/Qwen/DeepSeek models
- Amp CLI and IDE extensions support with provider routing
- Streaming and non-streaming responses
- Function calling / tool support
- Multimodal input (text and images)
- Multiple accounts with round-robin load balancing (Gemini, OpenAI, Claude, Grok)
- Simple CLI authentication flows (Gemini, OpenAI, Claude, Grok)
- Generative Language API Key support
- AI Studio Build, Gemini CLI, Claude Code, OpenAI Codex, and Grok Build multi-account load balancing
- OpenAI-compatible upstream providers via config (e.g., OpenRouter)
- Reusable Go SDK for embedding the proxy (see `docs/sdk-usage.md`)

## SDK

- SDK usage: [docs/sdk-usage.md](docs/sdk-usage.md)
- SDK advanced topics: [docs/sdk-advanced.md](docs/sdk-advanced.md)
- SDK access: [docs/sdk-access.md](docs/sdk-access.md)
- SDK watcher integration: [docs/sdk-watcher.md](docs/sdk-watcher.md)

## Management and logs

- Management API docs: [https://help.router-for.me/management/api](https://help.router-for.me/management/api)
- General guides: [https://help.router-for.me/](https://help.router-for.me/)

## Ecosystem

Projects built on or integrating with CLIProxyAPI:

- [霖君](https://github.com/wangdabaoqq/LinJun) — cross-platform desktop app for managing AI coding assistants (macOS/Windows/Linux), with local proxy for multi-account quota tracking and one-click config.
- [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard) — Next.js/React/PostgreSQL management dashboard with real-time log streaming, config editing, API key management, and OAuth provider integration.
- [All API Hub](https://github.com/qixing-jk/all-api-hub) — browser extension for managing New API-compatible relay accounts; integrates via the Management API for one-click provider import and config sync.
- [Shadow AI](https://github.com/HEUDavid/shadow-ai) — stealth AI assistant for restricted environments with cross-device LAN Q&A and control.
- [ProxyPal](https://github.com/buddingnewinsights/proxypal) — cross-platform desktop GUI wrapping CLIProxyAPI with usage analytics and auto-configuration for popular coding tools.
- [CLIProxyAPI Quota Inspector](https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector) — cross-platform quota inspector with per-account codex 5h/7d windows and multi-account analytics.
- [CodexCliPlus](https://github.com/C4AL/CodexCliPlus) — Windows-focused local-first management platform for Codex CLI.
- [CLIProxy Pool Watch](https://github.com/murasame612/CLIProxyPoolWidget) — native macOS SwiftUI app for monitoring ChatGPT/Codex account quotas through the Management API.

> [!NOTE]
> If you developed a project based on CLIProxyAPI, open a PR to add it here.

### Ports and inspired-by projects

- [9Router](https://github.com/decolua/9router) — Next.js implementation inspired by CLIProxyAPI with format translation, auto-fallback combos, and a web dashboard.
- [OmniRoute](https://github.com/diegosouzapw/OmniRoute) — AI gateway for multi-provider LLMs with smart routing, load balancing, retries, and fallbacks.
- [Playful Proxy API Panel (PPAP)](https://github.com/daishuge/playful-proxy-api-panel) — CLIProxyAPI-compatible fork with bundled management panel, restored usage statistics, cache hit rate, first-byte latency, and TPS tracking.
- [Codex Switch](https://github.com/9ycrooked/CodexSwitch) — Tauri 2 + Vue 3 tool for managing multiple OpenAI Codex desktop accounts.

> [!NOTE]
> If you developed a port of CLIProxyAPI or a project inspired by it, open a PR to add it here.

## License

This project is licensed under the MIT License — see the [LICENSE](LICENSE) file for details.
