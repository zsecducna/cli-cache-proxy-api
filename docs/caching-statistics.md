# Caching Statistics

CLIProxyAPI persists per-request caching statistics to a local SQLite database when `usage-statistics-enabled` is enabled.

## What is stored

Each request/turn stores:

- timestamp
- provider
- model
- source
- auth id / auth index
- latency
- failed/success
- input tokens
- output tokens
- reasoning tokens
- cached tokens
- total tokens
- `prompt_cache_key`
- `previous_response_id`
- `response_id`
- `prompt_cache_retention`

These internal fields are stored on disk for analysis, but the management API redacts sensitive identifiers before returning the browser-facing snapshot.

## Management API

Requires the management key.

### Cache statistics snapshot

```http
GET /v0/management/cache-statistics?days=14&limit=50&model_limit=10
Authorization: Bearer <MANAGEMENT_KEY>
```

Query parameters:

- `days`: time window applied to the summary, model table, daily trend, and recent requests
- `limit`: maximum number of recent requests returned
- `model_limit`: maximum number of model summary rows returned

Returns:

- overall summary for the requested time window
- model summaries for the requested time window
- daily trend summary for the requested time window
- recent requests for the requested time window

## Management UI

Open:

```text
/management.html#cache-statistics
```

The cache statistics UI is injected into the existing management.html surface when remote management routes are enabled and the control panel is not disabled. It adds an inline launcher attached to the `Dashboard > Usage Overview` section when that section is present, opens a built-in drawer for the dashboard, prompts for a management key, queries the management API directly from the browser, keeps the key only in memory for the current tab, auto-refreshes every 3 seconds while the drawer is open, exposes quick time presets for `Today`, `Last 7 Days`, and `This Month`, and renders compact built-in charts for daily cached tokens and cache ratio trends.

For backward compatibility, `GET /cache-statistics.html` redirects to `GET /management.html#cache-statistics`.

## Persistence location

The database file is resolved in this order:

1. `WRITABLE_PATH/stats/cache-statistics.sqlite`
2. `<config-dir>/stats/cache-statistics.sqlite`
3. `<auth-dir>/stats/cache-statistics.sqlite`
4. `<cwd>/stats/cache-statistics.sqlite`

The stats directory is created with owner-only permissions when possible, and the database file is tightened to owner-only permissions after initialization.

## Notes

- Persistence is tied to `usage-statistics-enabled`.
- Existing in-memory usage statistics remain available at `GET /v0/management/usage`.
- The cache statistics experience is merged into the served `management.html` page by injecting a repo-owned enhancement layer at response time.
