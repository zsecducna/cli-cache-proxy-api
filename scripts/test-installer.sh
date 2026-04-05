#!/usr/bin/env bash

set -euo pipefail

cleanup_tmp_root() {
  local root="${1:-}"

  [[ -n "$root" ]] || return 0
  chmod -R u+w "$root" 2>/dev/null || true
  rm -rf "$root"
}

schema_sql() {
  cat <<'SQL'
CREATE TABLE IF NOT EXISTS cache_statistics_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    requested_at TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    source TEXT NOT NULL,
    auth_id TEXT NOT NULL,
    auth_index TEXT NOT NULL,
    latency_ms INTEGER NOT NULL,
    failed INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    reasoning_tokens INTEGER NOT NULL,
    cached_tokens INTEGER NOT NULL,
    total_tokens INTEGER NOT NULL,
    prompt_cache_key TEXT NOT NULL,
    previous_response_id TEXT NOT NULL,
    response_id TEXT NOT NULL,
    prompt_cache_retention TEXT NOT NULL,
    reasoning_effort TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_requested_at ON cache_statistics_requests(requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_model ON cache_statistics_requests(model);
CREATE TABLE IF NOT EXISTS prompt_cache_response_index (
    response_id TEXT PRIMARY KEY,
    prompt_cache_key TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_prompt_cache_response_index_expires_at ON prompt_cache_response_index(expires_at);
SQL
}

seed_db() {
  local db_path="$1"
  local row_type="$2"

  sqlite3 "$db_path" "$(schema_sql)"
  if [[ "$row_type" == "target" ]]; then
    sqlite3 "$db_path" <<'SQL'
INSERT INTO cache_statistics_requests (
  requested_at, provider, model, source, auth_id, auth_index, latency_ms, failed,
  input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
  prompt_cache_key, previous_response_id, response_id, prompt_cache_retention, reasoning_effort
) VALUES
('2026-04-05T10:00:00Z', 'codex', 'gpt-5.4', 'source-a', 'auth-a', 'idx-a', 111, 0, 100, 10, 3, 90, 110, 'cache-a', '', 'resp-a', '', 'medium');
SQL
  else
    sqlite3 "$db_path" <<'SQL'
INSERT INTO cache_statistics_requests (
  requested_at, provider, model, source, auth_id, auth_index, latency_ms, failed,
  input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
  prompt_cache_key, previous_response_id, response_id, prompt_cache_retention, reasoning_effort
) VALUES
('2026-04-05T10:00:00Z', 'codex', 'gpt-5.4', 'source-a', 'auth-a', 'idx-a', 111, 0, 100, 10, 3, 90, 110, 'cache-a', '', 'resp-a', '', 'medium'),
('2026-04-05T11:00:00Z', 'codex', 'gpt-5.3-codex', 'source-b', 'auth-b', 'idx-b', 222, 0, 120, 15, 5, 80, 135, 'cache-b', '', 'resp-b', '', 'high');

INSERT INTO prompt_cache_response_index (
  response_id, prompt_cache_key, expires_at, updated_at
) VALUES
('resp-b', 'cache-b', '2026-04-06T00:00:00Z', '2026-04-05T11:00:00Z');
SQL
  fi
}

seed_broken_prompt_cache_db() {
  local db_path="$1"

  sqlite3 "$db_path" <<'SQL'
CREATE TABLE IF NOT EXISTS cache_statistics_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    requested_at TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    source TEXT NOT NULL,
    auth_id TEXT NOT NULL,
    auth_index TEXT NOT NULL,
    latency_ms INTEGER NOT NULL,
    failed INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    reasoning_tokens INTEGER NOT NULL,
    cached_tokens INTEGER NOT NULL,
    total_tokens INTEGER NOT NULL,
    prompt_cache_key TEXT NOT NULL,
    previous_response_id TEXT NOT NULL,
    response_id TEXT NOT NULL,
    prompt_cache_retention TEXT NOT NULL,
    reasoning_effort TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_requested_at ON cache_statistics_requests(requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_model ON cache_statistics_requests(model);
INSERT INTO cache_statistics_requests (
  requested_at, provider, model, source, auth_id, auth_index, latency_ms, failed,
  input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
  prompt_cache_key, previous_response_id, response_id, prompt_cache_retention, reasoning_effort
) VALUES
('2026-04-05T10:00:00Z', 'codex', 'gpt-5.4', 'source-a', 'auth-a', 'idx-a', 111, 0, 100, 10, 3, 90, 110, 'cache-a', '', 'resp-a', '', 'medium'),
('2026-04-05T12:00:00Z', 'codex', 'gpt-5.4-mini', 'source-c', 'auth-c', 'idx-c', 333, 0, 140, 20, 8, 70, 160, 'cache-c', '', 'resp-c', '', 'low');
CREATE TABLE prompt_cache_response_index (
    response_id TEXT PRIMARY KEY,
    prompt_cache_key TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
INSERT INTO prompt_cache_response_index (
  response_id, prompt_cache_key, expires_at
) VALUES
('resp-c', 'cache-c', '2026-04-07T00:00:00Z');
SQL
}

write_source_config() {
  local config_path="$1"
  local auth_dir="$2"
  local port="${3:-18317}"

  cat > "$config_path" <<EOF
port: $port
auth-dir: "$auth_dir"
usage-statistics-enabled: false
EOF
}

make_fake_binary() {
  local binary_path="$1"

  cat > "$binary_path" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "-h" ]]; then
  printf '%s\n' "$0 uses config ${CLI_PROXY_FAKE_CONFIG_HINT:-unknown}"
  exit 0
fi
printf 'fake cli-proxy-api\n'
EOF
  chmod +x "$binary_path"
}

run_installer_capture() {
  local repo_root="$1"
  local answers_path="$2"
  local output_path="$3"
  shift 3

  set +e
  env "$@" bash "$repo_root/install.sh" <"$answers_path" >"$output_path" 2>&1
  local status=$?
  set -e
  return "$status"
}

write_answers() {
  local answers_path="$1"
  shift
  printf '%s\n' "$@" > "$answers_path"
}

restore_file_or_remove_created() {
  local target_path="$1"
  local backup_path="${2:-}"
  local should_remove="${3:-0}"

  if [[ -n "$backup_path" && -f "$backup_path" ]]; then
    mkdir -p "$(dirname "$target_path")"
    mv -f "$backup_path" "$target_path"
    return 0
  fi
  if [[ "$should_remove" == "1" && -f "$target_path" ]]; then
    rm -f "$target_path"
    rmdir "$(dirname "$target_path")" 2>/dev/null || true
  fi
}

test_smoke_install() {
  local repo_root tmp_root home_root install_root source_auth_dir source_config source_stats_dir source_db target_db
  local target_config target_auth target_stats plist_path merged_count backup_count
  local gocache_dir gomodcache_dir answers_path output_path

  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  tmp_root="$(mktemp -d)"
  trap "cleanup_tmp_root '$tmp_root'" RETURN

  home_root="$tmp_root/home"
  install_root="$home_root/.cli-cache-proxy-test"
  source_auth_dir="$tmp_root/source-auth"
  source_config="$tmp_root/source-config.yaml"
  source_stats_dir="$tmp_root/source-stats"
  source_db="$source_stats_dir/cache-statistics.sqlite"
  target_db="$install_root/stats/cache-statistics.sqlite"
  gocache_dir="$tmp_root/gocache"
  gomodcache_dir="$tmp_root/gomodcache"
  answers_path="$tmp_root/answers.txt"
  output_path="$tmp_root/install.log"

  mkdir -p "$home_root" "$source_auth_dir" "$source_stats_dir" "$install_root/stats" "$gocache_dir" "$gomodcache_dir"
  printf 'oauth-token\n' > "$source_auth_dir/sample-token.txt"
  write_source_config "$source_config" "$source_auth_dir"

  seed_db "$target_db" "target"
  seed_db "$source_db" "source"

  write_answers \
    "$answers_path" \
    "$install_root/" \
    "$install_root/auth/" \
    "y" \
    "y" \
    "n" \
    "y" \
    "y" \
    "y"

  run_installer_capture \
    "$repo_root" \
    "$answers_path" \
    "$output_path" \
    HOME="$home_root" \
    GOCACHE="$gocache_dir" \
    GOMODCACHE="$gomodcache_dir" \
    CLI_PROXY_INSTALLER_SKIP_LAUNCHD=1 \
    CLI_PROXY_INSTALLER_SOURCE_CONFIG="$source_config" \
    CLI_PROXY_INSTALLER_SOURCE_STATS="$source_stats_dir"

  target_config="$install_root/config.yaml"
  target_auth="$install_root/auth"
  target_stats="$install_root/stats"
  plist_path="$home_root/Library/LaunchAgents/com.routerforme.cli-cache-proxy.plist"

  test -x "$install_root/cli-proxy-api"
  test -f "$target_config"
  test -d "$target_auth"
  test -d "$target_stats"
  test -d "$install_root/logs"
  test -f "$plist_path"
  grep -F "auth-dir: \"$install_root/auth\"" "$target_config" >/dev/null
  test -f "$target_auth/sample-token.txt"
  grep -F "$source_config" "$output_path" >/dev/null
  grep -F "$source_auth_dir" "$output_path" >/dev/null
  grep -F "$target_db" "$output_path" >/dev/null
  grep -F "$source_db" "$output_path" >/dev/null
  grep -F "rows=1" "$output_path" >/dev/null
  grep -F "rows=2" "$output_path" >/dev/null

  "$install_root/cli-proxy-api" -h 2>&1 | grep -F "$install_root/config.yaml" >/dev/null
  grep -F "$install_root/cli-proxy-api" "$plist_path" >/dev/null
  grep -F "$install_root/config.yaml" "$plist_path" >/dev/null
  grep -F "$install_root/logs/launchd.stdout.log" "$plist_path" >/dev/null
  grep -F "$install_root/logs/launchd.stderr.log" "$plist_path" >/dev/null

  merged_count="$(sqlite3 -readonly "$target_db" 'SELECT COUNT(*) FROM cache_statistics_requests;')"
  test "$merged_count" = "2"

  backup_count="$(find "$target_stats" -maxdepth 1 -name 'cache-statistics-backup-*.sqlite' | wc -l | tr -d ' ')"
  test "$backup_count" -ge 1

  sqlite3 -readonly "$target_db" "SELECT COUNT(*) FROM prompt_cache_response_index WHERE response_id = 'resp-b';" | grep -Fx '1' >/dev/null
  if grep -F "$install_root//" "$output_path" >/dev/null; then
    printf 'installer output retained an unnormalized trailing slash in install paths\n' >&2
    return 1
  fi
}

test_existing_target_config_requires_replace_confirmation() {
  local repo_root tmp_root home_root install_root source_auth_dir source_config empty_stats_dir
  local answers_path output_path target_config existing_binary

  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  tmp_root="$(mktemp -d)"
  trap "cleanup_tmp_root '$tmp_root'" RETURN

  home_root="$tmp_root/home"
  install_root="$home_root/.cli-cache-proxy-test"
  source_auth_dir="$tmp_root/source-auth"
  source_config="$tmp_root/source-config.yaml"
  empty_stats_dir="$tmp_root/empty-stats"
  answers_path="$tmp_root/answers.txt"
  output_path="$tmp_root/install.log"
  target_config="$install_root/config.yaml"
  existing_binary="$install_root/cli-proxy-api"

  mkdir -p "$home_root" "$install_root" "$install_root/auth" "$empty_stats_dir" "$source_auth_dir"
  printf 'source-token\n' > "$source_auth_dir/source-token.txt"
  write_source_config "$source_config" "$source_auth_dir" "18317"
  make_fake_binary "$existing_binary"
  cat > "$target_config" <<EOF
port: 9000
auth-dir: "$home_root/existing-auth"
usage-statistics-enabled: true
EOF

  write_answers \
    "$answers_path" \
    "$install_root" \
    "$install_root/auth" \
    "n" \
    "n" \
    "y" \
    "2" \
    "n" \
    "n"

  run_installer_capture \
    "$repo_root" \
    "$answers_path" \
    "$output_path" \
    HOME="$home_root" \
    CLI_PROXY_INSTALLER_SKIP_LAUNCHD=1 \
    CLI_PROXY_INSTALLER_SOURCE_CONFIG="$source_config" \
    CLI_PROXY_INSTALLER_SOURCE_STATS="$empty_stats_dir" \
    CLI_PROXY_FAKE_CONFIG_HINT="$target_config"

  grep -F "port: 9000" "$target_config" >/dev/null
  grep -F "auth-dir: \"$install_root/auth\"" "$target_config" >/dev/null
  if grep -F "port: 18317" "$target_config" >/dev/null; then
    printf 'selected source config overwrote the existing target config unexpectedly\n' >&2
    return 1
  fi
}

test_auth_merge_preserves_existing_target_files() {
  local repo_root tmp_root home_root install_root source_auth_dir source_config empty_stats_dir
  local answers_path output_path target_auth existing_binary

  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  tmp_root="$(mktemp -d)"
  trap "cleanup_tmp_root '$tmp_root'" RETURN

  home_root="$tmp_root/home"
  install_root="$home_root/.cli-cache-proxy-test"
  source_auth_dir="$tmp_root/source-auth"
  source_config="$tmp_root/source-config.yaml"
  empty_stats_dir="$tmp_root/empty-stats"
  answers_path="$tmp_root/answers.txt"
  output_path="$tmp_root/install.log"
  target_auth="$install_root/auth"
  existing_binary="$install_root/cli-proxy-api"

  mkdir -p "$home_root" "$target_auth" "$empty_stats_dir" "$source_auth_dir"
  printf 'target-keeps-this\n' > "$target_auth/shared-token.txt"
  printf 'target-only\n' > "$target_auth/local-only.txt"
  printf 'source-would-overwrite\n' > "$source_auth_dir/shared-token.txt"
  printf 'source-only\n' > "$source_auth_dir/source-only.txt"
  write_source_config "$source_config" "$source_auth_dir" "18317"
  make_fake_binary "$existing_binary"

  write_answers \
    "$answers_path" \
    "$install_root" \
    "$target_auth" \
    "n" \
    "n" \
    "y" \
    "y"

  run_installer_capture \
    "$repo_root" \
    "$answers_path" \
    "$output_path" \
    HOME="$home_root" \
    CLI_PROXY_INSTALLER_SKIP_LAUNCHD=1 \
    CLI_PROXY_INSTALLER_SOURCE_CONFIG="$source_config" \
    CLI_PROXY_INSTALLER_SOURCE_STATS="$empty_stats_dir" \
    CLI_PROXY_FAKE_CONFIG_HINT="$install_root/config.yaml"

  grep -Fx "target-keeps-this" "$target_auth/shared-token.txt" >/dev/null
  grep -Fx "target-only" "$target_auth/local-only.txt" >/dev/null
  grep -Fx "source-only" "$target_auth/source-only.txt" >/dev/null
}

test_db_merge_restores_backup_on_prompt_cache_failure() {
  local repo_root tmp_root home_root install_root source_stats_dir source_db target_db
  local answers_path output_path existing_binary status row_count backup_count

  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  tmp_root="$(mktemp -d)"
  trap "cleanup_tmp_root '$tmp_root'" RETURN

  home_root="$tmp_root/home"
  install_root="$home_root/.cli-cache-proxy-o'malley"
  source_stats_dir="$tmp_root/source-stats"
  source_db="$source_stats_dir/cache-statistics.sqlite"
  target_db="$install_root/stats/cache-statistics.sqlite"
  answers_path="$tmp_root/answers.txt"
  output_path="$tmp_root/install.log"
  existing_binary="$install_root/cli-proxy-api"

  mkdir -p "$home_root" "$install_root/stats" "$source_stats_dir"
  make_fake_binary "$existing_binary"
  seed_db "$target_db" "target"
  seed_broken_prompt_cache_db "$source_db"

  write_answers \
    "$answers_path" \
    "$install_root" \
    "$install_root/auth" \
    "n" \
    "n" \
    "y"

  if run_installer_capture \
    "$repo_root" \
    "$answers_path" \
    "$output_path" \
    HOME="$home_root" \
    CLI_PROXY_INSTALLER_SKIP_LAUNCHD=1 \
    CLI_PROXY_INSTALLER_SOURCE_CONFIG="$tmp_root/missing-config.yaml" \
    CLI_PROXY_INSTALLER_SOURCE_STATS="$source_stats_dir" \
    CLI_PROXY_FAKE_CONFIG_HINT="$install_root/config.yaml"; then
    printf 'installer unexpectedly succeeded after prompt-cache merge failure\n' >&2
    return 1
  fi

  grep -F "restored" "$output_path" >/dev/null
  row_count="$(sqlite3 -readonly "$target_db" 'SELECT COUNT(*) FROM cache_statistics_requests;')"
  test "$row_count" = "1"
  backup_count="$(find "$install_root/stats" -maxdepth 1 -name 'cache-statistics-backup-*.sqlite' | wc -l | tr -d ' ')"
  test "$backup_count" -ge 1
}

test_plist_escapes_xml_significant_paths() {
  local repo_root tmp_root home_root install_root empty_stats_dir
  local answers_path output_path plist_path existing_binary

  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  tmp_root="$(mktemp -d)"
  trap "cleanup_tmp_root '$tmp_root'" RETURN

  home_root="$tmp_root/home"
  install_root="$home_root/.cli-cache-proxy & <xml> > test"
  empty_stats_dir="$tmp_root/empty-stats"
  answers_path="$tmp_root/answers.txt"
  output_path="$tmp_root/install.log"
  plist_path="$home_root/Library/LaunchAgents/com.routerforme.cli-cache-proxy.plist"
  existing_binary="$install_root/cli-proxy-api"

  mkdir -p "$home_root" "$install_root" "$empty_stats_dir"
  make_fake_binary "$existing_binary"

  write_answers \
    "$answers_path" \
    "$install_root" \
    "$install_root/auth" \
    "n" \
    "y" \
    "n"

  run_installer_capture \
    "$repo_root" \
    "$answers_path" \
    "$output_path" \
    HOME="$home_root" \
    CLI_PROXY_INSTALLER_SKIP_LAUNCHD=1 \
    CLI_PROXY_INSTALLER_SOURCE_CONFIG="$tmp_root/missing-config.yaml" \
    CLI_PROXY_INSTALLER_SOURCE_STATS="$empty_stats_dir" \
    CLI_PROXY_FAKE_CONFIG_HINT="$install_root/config.yaml"

  test -f "$plist_path"
  grep -F "&amp;" "$plist_path" >/dev/null
  grep -F "&lt;" "$plist_path" >/dev/null
  grep -F "&gt;" "$plist_path" >/dev/null
  if command -v plutil >/dev/null 2>&1; then
    plutil -lint "$plist_path" >/dev/null
  fi
}

test_detection_uses_spec_default_fallbacks() {
  local repo_root tmp_root home_root install_root default_stats_dir default_stats_db default_auth_dir
  local default_config_dir default_config created_default_config answers_path output_path existing_binary
  local default_config_backup=""

  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  tmp_root="$(mktemp -d)"
  home_root="$tmp_root/home"
  install_root="$home_root/.cli-cache-proxy-test"
  default_stats_dir="$home_root/Desktop/CLIProxyAPI/stats"
  default_stats_db="$default_stats_dir/cache-statistics.sqlite"
  default_auth_dir="$tmp_root/default-fallback-auth"
  default_config_dir="/tmp/cli-proxy-api-test"
  default_config="$default_config_dir/config.yaml"
  created_default_config=0
  answers_path="$tmp_root/answers.txt"
  output_path="$tmp_root/install.log"
  existing_binary="$install_root/cli-proxy-api"

  trap "cleanup_tmp_root '$tmp_root'; restore_file_or_remove_created '$default_config' '$default_config_backup' '$created_default_config'" RETURN

  mkdir -p "$home_root" "$install_root" "$default_stats_dir" "$default_auth_dir"
  mkdir -p "$default_config_dir"
  if [[ -f "$default_config" ]]; then
    default_config_backup="$tmp_root/default-config.backup.yaml"
    cp -f "$default_config" "$default_config_backup"
  else
    created_default_config=1
  fi
  write_source_config "$default_config" "$default_auth_dir" "19321"
  seed_db "$default_stats_db" "source"
  make_fake_binary "$existing_binary"

  write_answers \
    "$answers_path" \
    "$install_root" \
    "$install_root/auth" \
    "n" \
    "n"

  run_installer_capture \
    "$repo_root" \
    "$answers_path" \
    "$output_path" \
    HOME="$home_root" \
    CLI_PROXY_FAKE_CONFIG_HINT="$install_root/config.yaml"

  grep -F "  Config sources:" "$output_path" >/dev/null
  grep -F "  Cache statistics databases:" "$output_path" >/dev/null
  grep -F "$default_config" "$output_path" >/dev/null
  grep -F "$default_auth_dir" "$output_path" >/dev/null
  grep -F "$default_stats_db" "$output_path" >/dev/null
}

test_launchctl_start_failure_is_non_fatal() {
  local repo_root tmp_root home_root install_root fake_bin empty_stats_dir
  local answers_path output_path plist_path existing_binary

  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  tmp_root="$(mktemp -d)"
  trap "cleanup_tmp_root '$tmp_root'" RETURN

  home_root="$tmp_root/home"
  install_root="$home_root/.cli-cache-proxy-test"
  fake_bin="$tmp_root/fake-bin"
  empty_stats_dir="$tmp_root/empty-stats"
  answers_path="$tmp_root/answers.txt"
  output_path="$tmp_root/install.log"
  plist_path="$home_root/Library/LaunchAgents/com.routerforme.cli-cache-proxy.plist"
  existing_binary="$install_root/cli-proxy-api"

  mkdir -p "$home_root" "$install_root" "$fake_bin" "$empty_stats_dir"
  make_fake_binary "$existing_binary"
  cat > "$fake_bin/launchctl" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  bootout)
    exit 0
    ;;
  bootstrap)
    printf 'bootstrap failed for %s\n' "${3:-unknown}" >&2
    exit 1
    ;;
  kickstart)
    printf 'kickstart failed for %s\n' "${3:-unknown}" >&2
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
EOF
  chmod +x "$fake_bin/launchctl"

  write_answers \
    "$answers_path" \
    "$install_root" \
    "$install_root/auth" \
    "n" \
    "y" \
    "y"

  run_installer_capture \
    "$repo_root" \
    "$answers_path" \
    "$output_path" \
    HOME="$home_root" \
    PATH="$fake_bin:$PATH" \
    CLI_PROXY_INSTALLER_SOURCE_CONFIG="$tmp_root/missing-config.yaml" \
    CLI_PROXY_INSTALLER_SOURCE_STATS="$empty_stats_dir" \
    CLI_PROXY_FAKE_CONFIG_HINT="$install_root/config.yaml"

  test -f "$plist_path"
  grep -F "Warning:" "$output_path" >/dev/null
  grep -F "launchctl bootstrap gui/" "$output_path" >/dev/null
  grep -F "launchctl kickstart -k gui/" "$output_path" >/dev/null
  grep -F "Installation complete." "$output_path" >/dev/null
}

test_launchctl_bootout_runs_before_plist_rewrite_without_start() {
  local repo_root tmp_root home_root install_root fake_bin empty_stats_dir
  local answers_path output_path plist_path launchctl_log existing_binary

  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  tmp_root="$(mktemp -d)"
  trap "cleanup_tmp_root '$tmp_root'" RETURN

  home_root="$tmp_root/home"
  install_root="$home_root/.cli-cache-proxy-test"
  fake_bin="$tmp_root/fake-bin"
  empty_stats_dir="$tmp_root/empty-stats"
  answers_path="$tmp_root/answers.txt"
  output_path="$tmp_root/install.log"
  launchctl_log="$tmp_root/launchctl.log"
  plist_path="$home_root/Library/LaunchAgents/com.routerforme.cli-cache-proxy.plist"
  existing_binary="$install_root/cli-proxy-api"

  mkdir -p "$home_root/Library/LaunchAgents" "$install_root" "$fake_bin" "$empty_stats_dir"
  make_fake_binary "$existing_binary"
  cat > "$plist_path" <<'EOF'
OLD-MARKER
EOF
  cat > "$fake_bin/launchctl" <<EOF
#!/usr/bin/env bash
log_path="$launchctl_log"
case "\${1:-}" in
  bootout)
    if grep -F "OLD-MARKER" "\${3:-}" >/dev/null 2>&1; then
      printf 'bootout old-marker %s\n' "\${3:-}" >> "\$log_path"
    else
      printf 'bootout rewritten %s\n' "\${3:-}" >> "\$log_path"
    fi
    exit 0
    ;;
  bootstrap|kickstart)
    printf '%s %s\n' "\${1:-}" "\${3:-}" >> "\$log_path"
    exit 0
    ;;
  *)
    printf '%s\n' "\${1:-unknown}" >> "\$log_path"
    exit 0
    ;;
esac
EOF
  chmod +x "$fake_bin/launchctl"

  write_answers \
    "$answers_path" \
    "$install_root" \
    "$install_root/auth" \
    "n" \
    "y" \
    "n"

  run_installer_capture \
    "$repo_root" \
    "$answers_path" \
    "$output_path" \
    HOME="$home_root" \
    PATH="$fake_bin:$PATH" \
    CLI_PROXY_INSTALLER_SOURCE_CONFIG="$tmp_root/missing-config.yaml" \
    CLI_PROXY_INSTALLER_SOURCE_STATS="$empty_stats_dir" \
    CLI_PROXY_FAKE_CONFIG_HINT="$install_root/config.yaml"

  test -f "$plist_path"
  grep -F "bootout old-marker $plist_path" "$launchctl_log" >/dev/null
  if grep -E '^(bootstrap|kickstart) ' "$launchctl_log" >/dev/null; then
    printf 'bootstrap or kickstart ran even though start was not requested\n' >&2
    return 1
  fi
  if grep -F "OLD-MARKER" "$plist_path" >/dev/null; then
    printf 'plist was not rewritten after bootout\n' >&2
    return 1
  fi
}

main() {
  test_smoke_install
  test_existing_target_config_requires_replace_confirmation
  test_auth_merge_preserves_existing_target_files
  test_db_merge_restores_backup_on_prompt_cache_failure
  test_plist_escapes_xml_significant_paths
  test_detection_uses_spec_default_fallbacks
  test_launchctl_bootout_runs_before_plist_rewrite_without_start
  test_launchctl_start_failure_is_non_fatal
  printf 'Installer smoke test passed.\n'
}

main "$@"
