# CLI Proxy API

English | [中文](README_CN.md) | [日本語](README_JA.md)

A proxy server that provides OpenAI, Gemini, Claude, and Codex compatible API interfaces for CLI tools and SDKs.

It supports OAuth-backed access for Claude Code, OpenAI Codex (GPT models), Qwen Code, iFlow, and other compatible clients, plus configurable upstream routing and multi-account usage.

## Sponsor

[![https://www.packyapi.com/register?aff=cliproxyapi](./assets/packycode-en.png)](https://www.packyapi.com/register?aff=cliproxyapi)

Thanks to PackyCode for sponsoring this project!

PackyCode is a reliable and efficient API relay service provider, offering relay services for Claude Code, Codex, Gemini, and more.

PackyCode provides special discounts for our software users: register using <a href="https://www.packyapi.com/register?aff=cliproxyapi">this link</a> and enter the "cliproxyapi" promo code during recharge to get 10% off.

---

<table>
<tbody>
<tr>
<td width="180"><a href="https://www.aicodemirror.com/register?invitecode=TJNAIF"><img src="./assets/aicodemirror.png" alt="AICodeMirror" width="150"></a></td>
<td>Thanks to AICodeMirror for sponsoring this project! AICodeMirror provides official high-stability relay services for Claude Code / Codex / Gemini CLI, with enterprise-grade concurrency, fast invoicing, and 24/7 dedicated technical support. Claude Code / Codex / Gemini official channels at 38% / 2% / 9% of original price, with extra discounts on top-ups! AICodeMirror offers special benefits for CLIProxyAPI users: register via <a href="https://www.aicodemirror.com/register?invitecode=TJNAIF">this link</a> to enjoy 20% off your first top-up, and enterprise customers can get up to 25% off!</td>
</tr>
<tr>
<td width="180"><a href="https://shop.bmoplus.com/?utm_source=github"><img src="./assets/bmoplus.png" alt="BmoPlus" width="150"></a></td>
<td>Huge thanks to BmoPlus for sponsoring this project! BmoPlus is a highly reliable AI account provider built strictly for heavy AI users and developers. They offer rock-solid, ready-to-use accounts and official top-up services for ChatGPT Plus / ChatGPT Pro (Full Warranty) / Claude Pro / Super Grok / Gemini Pro. By registering and ordering through <a href="https://shop.bmoplus.com/?utm_source=github">BmoPlus - Premium AI Accounts & Top-ups</a>, users can unlock the mind-blowing rate of <b>10% of the official GPT subscription price (90% OFF)</b>!</td>
</tr>
<tr>
<td width="180"><a href="https://poixe.com/i/m8kvep"><img src="./assets/poixeai.png" alt="PoixeAI" width="150"></a></td>
<td>Thanks to Poixe AI for sponsoring this project! Poixe AI provides reliable LLM API services. You can leverage the platform's API endpoints to seamlessly build AI-powered products. Additionally, you can become a vendor by providing AI API resources to the platform and earn revenue. Register through the exclusive CLIProxyAPI <a href="https://poixe.com/i/m8kvep">referral link</a> and receive a bonus of $5 USD on your first top-up.</td>
</tr>
<tr>
<td width="180"><a href="https://coder.visioncoder.cn"><img src="./assets/visioncoder.png" alt="VisionCoder" width="150"></a></td>
<td>Thanks to <b>VisionCoder</b> for supporting this project. <a href="https://coder.visioncoder.cn" target="_blank">VisionCoder Developer Platform</a> is a reliable and efficient API relay service provider, offering access to mainstream AI models such as Claude Code, Codex, and Gemini. It helps developers and teams integrate AI capabilities more easily and improve productivity.
<p></p>
VisionCoder is also offering our users a limited-time <a href="https://coder.visioncoder.cn" target="_blank">Token Plan</a> promotion: <b>buy 1 month and get 1 month free</b>.</td>
</tr>
<tr>
<td width="180"><a href="https://apikey.fun/register?aff=CLIProxyAPI"><img src="./assets/apikey.png" alt="APIKEY.FUN" width="150"></a></td>
<td>Thanks to APIKEY.FUN for sponsoring this project! APIKEY.FUN is a professional enterprise-grade AI relay platform dedicated to providing stable, efficient, and low-cost AI model API access for enterprises and individual developers. The platform supports popular mainstream models such as Claude, OpenAI, and Gemini, with prices as low as 7% of the official price. Register through this project's <a href="https://apikey.fun/register?aff=CLIProxyAPI">exclusive link</a> to enjoy a special <b>permanent 5% top-up discount</b>.</td>
</tr>
</tbody>
</table>

## Features

- OpenAI/Gemini/Claude/Grok compatible API endpoints for CLI models
- OpenAI Codex support (GPT models) via OAuth login
- Claude Code support via OAuth login
- Grok Build support via OAuth login
- Amp CLI and IDE extensions support with provider routing
- Streaming and non-streaming responses
- Function calling / tool support
- Multimodal input support (text and images)
- Multiple accounts with round-robin load balancing (Gemini, OpenAI, Claude, Grok)
- Simple CLI authentication flows (Gemini, OpenAI, Claude, Grok)
- Generative Language API Key support
- AI Studio Build multi-account load balancing
- Gemini CLI multi-account load balancing
- Claude Code multi-account load balancing
- OpenAI Codex multi-account load balancing
- Grok Build multi-account load balancing
- OpenAI-compatible upstream providers via config (e.g., OpenRouter)
- Reusable Go SDK for embedding the proxy (see `docs/sdk-usage.md`)

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

Optional: enable Postgres-backed auth/config/statistics storage by creating a `.env`
file in the server working directory:

```bash
cat > .env <<'EOF'
PGSTORE_DSN=postgresql://cheaprouter:cheaprouter@localhost:5432/cliproxy
PGSTORE_SCHEMA=public
PGSTORE_LOCAL_PATH=~/.cli-cache-proxy
EOF
```

On first startup, the proxy will keep using the same `PGSTORE_*` surface for auth,
config, and usage statistics, and it will import local legacy auth/stats files from
`PGSTORE_LOCAL_PATH` when the Postgres tables are empty.

The installers can also write the same `PGSTORE_*` values into the install root `.env`,
validate the DSN, and try to create the Postgres role/database automatically. If the
installer cannot provision Postgres with the supplied credentials, it will print the
exact bash commands needed to initialize the role/database manually.

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
curl http://127.0.0.1:8317/healthz
```

### Example Claude-compatible usage

```bash
ANTHROPIC_BASE_URL="http://127.0.0.1:8317" \
ANTHROPIC_AUTH_TOKEN="your-api-key-1" \
claude --model "claude-sonnet-4-5-20250929" -p 'respond to me exactly "hello"'
```

### Example GPT / Codex routing usage

```bash
ANTHROPIC_DEFAULT_OPUS_MODEL='gpt-5.4' \
ANTHROPIC_DEFAULT_SONNET_MODEL='gpt-5.3-codex' \
ANTHROPIC_DEFAULT_HAIKU_MODEL='gpt-5.3-codex' \
ANTHROPIC_BASE_URL="http://127.0.0.1:8317" \
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
### [霖君](https://github.com/wangdabaoqq/LinJun)

霖君 is a cross-platform desktop application for managing AI programming assistants, supporting macOS, Windows, and Linux systems. Unified management of Claude Code, Gemini CLI, OpenAI Codex, and other AI coding tools, with local proxy for multi-account quota tracking and one-click configuration.

### [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard)

A modern web-based management dashboard for CLIProxyAPI built with Next.js, React, and PostgreSQL. Features real-time log streaming, structured configuration editing, API key management, OAuth provider integration for Claude/Gemini/Codex, usage analytics, container management, and config sync with OpenCode via companion plugin - no manual YAML editing needed.

### [All API Hub](https://github.com/qixing-jk/all-api-hub)

Browser extension for one-stop management of New API-compatible relay site accounts, featuring balance and usage dashboards, auto check-in, one-click key export to common apps, in-page API availability testing, and channel/model sync and redirection. It integrates with CLIProxyAPI through the Management API for one-click provider import and config sync.

### [Shadow AI](https://github.com/HEUDavid/shadow-ai)

Shadow AI is an AI assistant tool designed specifically for restricted environments. It provides a stealthy operation
mode without windows or traces, and enables cross-device AI Q&A interaction and control via the local area network (
LAN). Essentially, it is an automated collaboration layer of "screen/audio capture + AI inference + low-friction delivery",
helping users to immersively use AI assistants across applications on controlled devices or in restricted environments.

### [ProxyPal](https://github.com/buddingnewinsights/proxypal)

Cross-platform desktop app (macOS, Windows, Linux) wrapping CLIProxyAPI with a native GUI. Connects Claude, ChatGPT, Gemini, GitHub Copilot, and custom OpenAI-compatible endpoints with usage analytics, request monitoring, and auto-configuration for popular coding tools - no API keys needed.

### [CLIProxyAPI Quota Inspector](https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector)

Ready-to-use cross-platform quota inspector for CLIProxyAPI, supporting per-account codex 5h/7d quota windows, plan-based sorting, status coloring, and multi-account summary analytics.

### [CodexCliPlus](https://github.com/C4AL/CodexCliPlus)

Windows-focused, local-first desktop management platform for Codex CLI built on CLIProxyAPI, focused on simplifying local setup, account and runtime management, and providing a more complete Codex CLI experience for local users.

### [CLIProxy Pool Watch](https://github.com/murasame612/CLIProxyPoolWidget)

Native macOS SwiftUI app for monitoring ChatGPT/Codex account quotas in CLIProxyAPI pools. Displays account availability, Plus-base capacity, 5-hour and weekly quota bars, plan weights, and restore forecasts through the Management API.

> [!NOTE]  
> If you developed a project based on CLIProxyAPI, please open a PR to add it to this list.

## More choices

Those projects are ports of CLIProxyAPI or inspired by it:

### [9Router](https://github.com/decolua/9router)

A Next.js implementation inspired by CLIProxyAPI, easy to install and use, built from scratch with format translation (OpenAI/Claude/Gemini/Ollama), combo system with auto-fallback, multi-account management with exponential backoff, a Next.js web dashboard, and support for CLI tools (Cursor, Claude Code, Cline, RooCode) - no API keys needed.

### [OmniRoute](https://github.com/diegosouzapw/OmniRoute)

Never stop coding. Smart routing to FREE & low-cost AI models with automatic fallback.

OmniRoute is an AI gateway for multi-provider LLMs: an OpenAI-compatible endpoint with smart routing, load balancing, retries, and fallbacks. Add policies, rate limits, caching, and observability for reliable, cost-aware inference.

### [Playful Proxy API Panel (PPAP)](https://github.com/daishuge/playful-proxy-api-panel)

A public CLIProxyAPI-compatible fork and bundled management panel. It keeps upstream-style usage while restoring built-in usage statistics, adding cache hit rate, first-byte latency, TPS tracking, and Docker-oriented self-hosted installation docs.

### [Codex Switch](https://github.com/9ycrooked/CodexSwitch)

This is a tool built with Tauri 2 + Vue 3 for managing multiple OpenAI Codex desktop accounts. Switch between saved ChatGPT/Codex certification profiles, check 5-hour and weekly quota usage in real time, verify token health, view active account details, and import or save auth.json files without manual copying.

> [!NOTE]  
> If you have developed a port of CLIProxyAPI or a project inspired by it, please open a PR to add it to this list.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
