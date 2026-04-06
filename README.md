# CLI Proxy API

English | [中文](README_CN.md) | [日本語](README_JA.md)

A proxy server that provides OpenAI, Gemini, Claude, and Codex compatible API interfaces for CLI tools and SDKs.

It supports OAuth-backed access for Claude Code, OpenAI Codex (GPT models), Qwen Code, iFlow, and other compatible clients, plus configurable upstream routing and multi-account usage.

## Features

- OpenAI, Gemini, Claude, and Codex compatible API endpoints
- Claude Code and OpenAI Codex support via OAuth login
- Qwen Code and iFlow support via OAuth login
- OpenAI-compatible upstream providers via config
- Streaming and non-streaming responses
- Function calling / tool support
- Multimodal input support (text and images)
- Multiple accounts with load balancing and retry/failover
- Management API and web control panel
- Reusable Go SDK for embedding the proxy

## Installation

### Option 1: Run locally with Go

1. Copy the example config and update it for your environment:

```bash
cp config.example.yaml config.yaml
```

2. Review `config.yaml`, especially:
- `port`
- `auth-dir`
- `api-keys`
- provider credentials / OAuth auth files

3. Build and start the server:

```bash
go build -o ./cli-caching-proxy-test ./cmd/server
./cli-caching-proxy-test
```

### Option 2: Run with Docker Compose

```bash
docker compose up -d --build
```

The compose file mounts:
- `./config.yaml` → `/CLIProxyAPI/config.yaml`
- `./auths` → `/root/.cli-proxy-api`
- `./logs` → `/CLIProxyAPI/logs`

### Option 3: Install as a macOS service

For a guided local installation on macOS, use:

```bash
./install_mac.sh
```

### Option 4: Install as a Linux user service

For a guided local installation on Linux, use:

```bash
./install_linux.sh
```

### Option 5: Install on Windows

For a guided local installation on Windows, use:

```powershell
pwsh -File .\install_windows.ps1
```

## Usage

### Basic startup

After configuring `config.yaml`, start the proxy and point your client at the configured port.

Default local health endpoint:

```bash
curl http://127.0.0.1:18317/healthz
```

### Example Claude-compatible usage

```bash
ANTHROPIC_BASE_URL="http://127.0.0.1:18317" \
ANTHROPIC_AUTH_TOKEN="your-api-key-1" \
claude --model "claude-sonnet-4-5-20250929" -p 'respond to me exactly "hello"'
```

### Example GPT / Codex routing usage

```bash
ANTHROPIC_DEFAULT_OPUS_MODEL='gpt-5.4' \
ANTHROPIC_DEFAULT_SONNET_MODEL='gpt-5.3-codex' \
ANTHROPIC_DEFAULT_HAIKU_MODEL='gpt-5.3-codex' \
ANTHROPIC_BASE_URL="http://127.0.0.1:18317" \
ANTHROPIC_AUTH_TOKEN="your-api-key-1" \
claude --model 'gpt-5.4' -p 'respond to me exactly "hello"'
```

### Provider-specific routes

When you need the request or response shape of a specific backend family, use provider-specific paths instead of the merged `/v1/...` endpoints:

- `/api/provider/{provider}/v1/messages`
- `/api/provider/{provider}/v1beta/models/...`
- `/api/provider/{provider}/v1/chat/completions`

### SDK usage

- SDK usage: [docs/sdk-usage.md](docs/sdk-usage.md)
- SDK advanced topics: [docs/sdk-advanced.md](docs/sdk-advanced.md)
- SDK access: [docs/sdk-access.md](docs/sdk-access.md)
- SDK watcher integration: [docs/sdk-watcher.md](docs/sdk-watcher.md)

### Management and logs

- Management API docs: [https://help.router-for.me/management/api](https://help.router-for.me/management/api)
- General guides: [https://help.router-for.me/](https://help.router-for.me/)
