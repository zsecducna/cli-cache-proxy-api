#!/usr/bin/env bash

set -euo pipefail

INSTALLER_LABEL="com.routerforme.cli-cache-proxy"
DEFAULT_INSTALL_ROOT="~/.cli-cache-proxy"
DEFAULT_SOURCE_CONFIG="/tmp/cli-proxy-api-test/config.yaml"
DEFAULT_SOURCE_STATS_DIR="~/Desktop/CLIProxyAPI/stats"

SKIP_LAUNCHD="${CLI_PROXY_INSTALLER_SKIP_LAUNCHD:-0}"
SKIP_POSTGRES_PROVISION="${CLI_PROXY_INSTALLER_SKIP_POSTGRES_PROVISION:-0}"
SOURCE_CONFIG_OVERRIDE="${CLI_PROXY_INSTALLER_SOURCE_CONFIG:-}"
SOURCE_STATS_OVERRIDE="${CLI_PROXY_INSTALLER_SOURCE_STATS:-}"
BINARY_EXEC_TIMEOUT_SECONDS="${CLI_PROXY_INSTALLER_BINARY_EXEC_TIMEOUT_SECONDS:-5}"

CONFIG_SOURCES=()
AUTH_SOURCES=()
DB_SOURCES=()
BINARY_SOURCES=()

say() {
  printf '%s\n' "$*"
}

say_err() {
  printf '%s\n' "$*" >&2
}

warn() {
  printf 'Warning: %s\n' "$*" >&2
}

die() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

timestamp_utc() {
  date -u +%Y%m%d-%H%M%S
}

expand_path() {
  local raw="${1:-}"
  if [[ -z "$raw" ]]; then
    printf '%s' "$raw"
    return
  fi
  case "$raw" in
    \~)
      raw="$HOME"
      ;;
    \~/*)
      raw="$HOME/${raw#\~/}"
      ;;
  esac
  if [[ "$raw" != /* ]]; then
    raw="$(pwd)/$raw"
  fi
  while [[ "$raw" == */ && "$raw" != "/" ]]; do
    raw="${raw%/}"
  done
  printf '%s' "$raw"
}

prompt_with_default() {
  local label="$1"
  local default_value="$2"
  local reply=""
  read -r -p "$label [$default_value]: " reply || true
  printf '%s' "${reply:-$default_value}"
}

confirm_yes_no() {
  local prompt="$1"
  local default_answer="${2:-Y}"
  local hint="[Y/n]"
  local reply=""

  if [[ "$default_answer" =~ ^[Nn]$ ]]; then
    hint="[y/N]"
  fi

  while true; do
    read -r -p "$prompt $hint: " reply || true
    reply="${reply:-$default_answer}"
    case "$reply" in
      [Yy]|[Yy][Ee][Ss])
        return 0
        ;;
      [Nn]|[Nn][Oo])
        return 1
        ;;
      *)
        say "Please answer yes or no."
        ;;
    esac
  done
}

ensure_dir() {
  mkdir -p "$1"
}

escape_yaml_double() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s' "$value"
}

xml_escape() {
  local value="$1"
  value="${value//&/&amp;}"
  value="${value//</&lt;}"
  value="${value//>/&gt;}"
  value="${value//\"/&quot;}"
  value="${value//\'/&apos;}"
  printf '%s' "$value"
}

sql_quote() {
  local value="$1"
  value="$(printf '%s' "$value" | sed "s/'/''/g")"
  printf '%s' "$value"
}

env_quote() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '"%s"' "$value"
}

quote_pg_identifier() {
  local value="$1"
  value="${value//\"/\"\"}"
  printf '"%s"' "$value"
}

require_tools() {
  local missing=0
  local tool=""
  for tool in bash git go sqlite3 launchctl; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      warn "Missing required tool: $tool"
      missing=1
    fi
  done
  if [[ "$missing" -ne 0 ]]; then
    die "Install the missing tools and rerun ./install_mac.sh"
  fi
}

require_postgres_tools() {
  if ! command -v psql >/dev/null 2>&1; then
    die "Postgres setup requires psql. Install PostgreSQL client tools and rerun ./install_mac.sh"
  fi
}

resolve_repo_root() {
  local script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  printf '%s' "$script_dir"
}

add_unique_path() {
  local array_name="$1"
  local candidate="$2"
  local existing=""

  [[ -n "$candidate" ]] || return 0
  candidate="$(expand_path "$candidate")"
  [[ -e "$candidate" ]] || return 0

  eval "for existing in \"\${${array_name}[@]:-}\"; do
    if [[ \"\$existing\" == \"\$candidate\" ]]; then
      return 0
    fi
  done"

  eval "${array_name}+=(\"\$candidate\")"
}

append_sources_excluding_target() {
  local source_array_name="$1"
  local target_path="$2"
  local source=""
  local filtered=()

  target_path="$(expand_path "$target_path")"
  eval "for source in \"\${${source_array_name}[@]:-}\"; do
    if [[ \"\$source\" != \"\$target_path\" ]]; then
      filtered+=(\"\$source\")
    fi
  done"

  if ((${#filtered[@]})); then
    printf '%s\n' "${filtered[@]}"
  fi
}

extract_auth_dir_from_config() {
  local config_path="$1"
  local line=""
  local value=""

  [[ -f "$config_path" ]] || return 1
  line="$(grep -E '^[[:space:]]*auth-dir:' "$config_path" | head -n1 || true)"
  [[ -n "$line" ]] || return 1
  value="${line#*:}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  value="${value%\"}"
  value="${value#\"}"
  value="${value%\'}"
  value="${value#\'}"
  value="$(expand_path "$value")"
  [[ -d "$value" ]] || return 1
  printf '%s' "$value"
}

db_count_and_range() {
  local db_path="$1"
  sqlite3 -readonly "$db_path" \
    "SELECT COUNT(*), COALESCE(MIN(requested_at), ''), COALESCE(MAX(requested_at), '') FROM cache_statistics_requests;" \
    2>/dev/null || printf 'unreadable||'
}

maybe_add_config_source() {
  local candidate="$1"
  candidate="$(expand_path "$candidate")"
  if [[ -f "$candidate" ]]; then
    add_unique_path CONFIG_SOURCES "$candidate"
  elif [[ -d "$candidate" && -f "$candidate/config.yaml" ]]; then
    add_unique_path CONFIG_SOURCES "$candidate/config.yaml"
  fi
}

maybe_add_db_source() {
  local candidate="$1"
  candidate="$(expand_path "$candidate")"
  if [[ -f "$candidate" ]]; then
    add_unique_path DB_SOURCES "$candidate"
    return
  fi
  if [[ -d "$candidate" && -f "$candidate/cache-statistics.sqlite" ]]; then
    add_unique_path DB_SOURCES "$candidate/cache-statistics.sqlite"
    return
  fi
  if [[ -d "$candidate" && -f "$candidate/stats/cache-statistics.sqlite" ]]; then
    add_unique_path DB_SOURCES "$candidate/stats/cache-statistics.sqlite"
  fi
}

detect_sources() {
  local install_root="$1"
  local config_path=""
  local auth_path=""

  CONFIG_SOURCES=()
  AUTH_SOURCES=()
  DB_SOURCES=()
  BINARY_SOURCES=()

  if [[ -x "$install_root/cli-proxy-api" ]]; then
    add_unique_path BINARY_SOURCES "$install_root/cli-proxy-api"
  fi

  maybe_add_config_source "$install_root/config.yaml"

  if [[ -n "$SOURCE_CONFIG_OVERRIDE" ]]; then
    maybe_add_config_source "$SOURCE_CONFIG_OVERRIDE"
  else
    maybe_add_config_source "$DEFAULT_SOURCE_CONFIG"
  fi

  if [[ -d "$install_root/auth" ]]; then
    add_unique_path AUTH_SOURCES "$install_root/auth"
  fi

  for config_path in "${CONFIG_SOURCES[@]:-}"; do
    auth_path="$(extract_auth_dir_from_config "$config_path" || true)"
    if [[ -n "$auth_path" ]]; then
      add_unique_path AUTH_SOURCES "$auth_path"
    fi
  done

  maybe_add_db_source "$install_root/stats/cache-statistics.sqlite"
  if [[ -n "$SOURCE_STATS_OVERRIDE" ]]; then
    maybe_add_db_source "$SOURCE_STATS_OVERRIDE"
  else
    maybe_add_db_source "$DEFAULT_SOURCE_STATS_DIR"
  fi
}

print_detection_summary() {
  local item=""
  local idx=0
  local count=""
  local first_ts=""
  local last_ts=""

  say
  say "Detected existing installation data:"

  say "  Binary sources:"
  if [[ "${#BINARY_SOURCES[@]}" -eq 0 ]]; then
    say "    (none)"
  else
    idx=1
    for item in "${BINARY_SOURCES[@]}"; do
      say "    [$idx] $item"
      idx=$((idx + 1))
    done
  fi

  say "  Config sources:"
  if [[ "${#CONFIG_SOURCES[@]}" -eq 0 ]]; then
    say "    (none)"
  else
    idx=1
    for item in "${CONFIG_SOURCES[@]}"; do
      say "    [$idx] $item"
      idx=$((idx + 1))
    done
  fi

  say "  Auth sources:"
  if [[ "${#AUTH_SOURCES[@]}" -eq 0 ]]; then
    say "    (none)"
  else
    idx=1
    for item in "${AUTH_SOURCES[@]}"; do
      say "    [$idx] $item"
      idx=$((idx + 1))
    done
  fi

  say "  Cache statistics databases:"
  if [[ "${#DB_SOURCES[@]}" -eq 0 ]]; then
    say "    (none)"
  else
    idx=1
    for item in "${DB_SOURCES[@]}"; do
      IFS='|' read -r count first_ts last_ts <<EOF
$(db_count_and_range "$item")
EOF
      say "    [$idx] $item"
      say "         rows=$count first=$first_ts last=$last_ts"
      idx=$((idx + 1))
    done
  fi
}

choose_config_source() {
  local target_config="${1:-}"
  local normalized_target=""
  local choice=""
  local idx=""
  if [[ "${#CONFIG_SOURCES[@]}" -eq 0 ]]; then
    return 1
  fi

  normalized_target="$(expand_path "$target_config")"
  if [[ -n "$normalized_target" && -f "$normalized_target" ]]; then
    if [[ "${#CONFIG_SOURCES[@]}" -eq 1 && "${CONFIG_SOURCES[0]}" == "$normalized_target" ]]; then
      printf '%s' "$normalized_target"
      return 0
    fi
    if ! confirm_yes_no "Import a different detected config into the install location? Current target config at $normalized_target will be kept by default." "N"; then
      printf '%s' "$normalized_target"
      return 0
    fi
  elif ! confirm_yes_no "Copy a detected config into the install location?" "Y"; then
    return 1
  fi
  if [[ "${#CONFIG_SOURCES[@]}" -eq 1 ]]; then
    printf '%s' "${CONFIG_SOURCES[0]}"
    return 0
  fi

  say_err "Select a config source:"
  idx=1
  for choice in "${CONFIG_SOURCES[@]}"; do
    say_err "  [$idx] $choice"
    idx=$((idx + 1))
  done

  while true; do
    read -r -p "Config source number [1-${#CONFIG_SOURCES[@]}]: " choice || true
    if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#CONFIG_SOURCES[@]} )); then
      printf '%s' "${CONFIG_SOURCES[$((choice - 1))]}"
      return 0
    fi
    say_err "Please enter a valid config source number."
  done
}

ensure_target_config_parent() {
  local config_path="$1"
  ensure_dir "$(dirname "$config_path")"
}

backup_file_if_exists() {
  local file_path="$1"
  local suffix="$2"
  local backup_path=""
  if [[ -f "$file_path" ]]; then
    backup_path="${file_path}.${suffix}.bak"
    cp -f "$file_path" "$backup_path"
    say "Backed up $file_path -> $backup_path"
  fi
}

set_yaml_scalar() {
  local file_path="$1"
  local key="$2"
  local value="$3"
  local tmp_path="${file_path}.tmp.$$"
  local found=0
  local line=""

  : > "$tmp_path"
  if [[ -f "$file_path" ]]; then
    while IFS= read -r line || [[ -n "$line" ]]; do
      if [[ "$line" =~ ^[[:space:]]*$key: ]]; then
        printf '%s: "%s"\n' "$key" "$(escape_yaml_double "$value")" >> "$tmp_path"
        found=1
      else
        printf '%s\n' "$line" >> "$tmp_path"
      fi
    done < "$file_path"
  fi

  if [[ "$found" -eq 0 ]]; then
    printf '%s: "%s"\n' "$key" "$(escape_yaml_double "$value")" >> "$tmp_path"
  fi

  mv "$tmp_path" "$file_path"
}

write_minimal_config() {
  local file_path="$1"
  local auth_dir="$2"
  cat > "$file_path" <<EOF
auth-dir: "$(escape_yaml_double "$auth_dir")"
usage-statistics-enabled: true
EOF
}

read_env_scalar() {
  local file_path="$1"
  local key="$2"
  local line=""
  local value=""

  [[ -f "$file_path" ]] || return 1
  line="$(grep -E "^[[:space:]]*$key=" "$file_path" | tail -n1 || true)"
  [[ -n "$line" ]] || return 1
  value="${line#*=}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  value="${value%\"}"
  value="${value#\"}"
  value="${value%\'}"
  value="${value#\'}"
  printf '%s' "$value"
}

set_env_scalar() {
  local file_path="$1"
  local key="$2"
  local value="$3"
  local tmp_path="${file_path}.tmp.$$"
  local found=0
  local line=""

  ensure_dir "$(dirname "$file_path")"
  : > "$tmp_path"
  if [[ -f "$file_path" ]]; then
    while IFS= read -r line || [[ -n "$line" ]]; do
      if [[ "$line" =~ ^[[:space:]]*$key= ]]; then
        printf '%s=%s\n' "$key" "$(env_quote "$value")" >> "$tmp_path"
        found=1
      else
        printf '%s\n' "$line" >> "$tmp_path"
      fi
    done < "$file_path"
  fi

  if [[ "$found" -eq 0 ]]; then
    printf '%s=%s\n' "$key" "$(env_quote "$value")" >> "$tmp_path"
  fi

  mv "$tmp_path" "$file_path"
}

remove_env_keys() {
  local file_path="$1"
  shift
  local tmp_path="${file_path}.tmp.$$"
  local line=""
  local key=""
  local remove=0

  [[ -f "$file_path" ]] || return 0

  : > "$tmp_path"
  while IFS= read -r line || [[ -n "$line" ]]; do
    remove=0
    for key in "$@"; do
      if [[ "$line" =~ ^[[:space:]]*$key= ]]; then
        remove=1
        break
      fi
    done
    if [[ "$remove" -eq 0 ]]; then
      printf '%s\n' "$line" >> "$tmp_path"
    fi
  done < "$file_path"

  if [[ -s "$tmp_path" ]]; then
    mv "$tmp_path" "$file_path"
  else
    rm -f "$tmp_path" "$file_path"
  fi
}

write_pgstore_env() {
  local env_path="$1"
  local dsn="$2"
  local schema="$3"
  local local_path="$4"

  set_env_scalar "$env_path" "PGSTORE_DSN" "$dsn"
  set_env_scalar "$env_path" "PGSTORE_SCHEMA" "$schema"
  set_env_scalar "$env_path" "PGSTORE_LOCAL_PATH" "$local_path"
}

clear_pgstore_env() {
  local env_path="$1"
  remove_env_keys "$env_path" "PGSTORE_DSN" "PGSTORE_SCHEMA" "PGSTORE_LOCAL_PATH"
}

prompt_required_value() {
  local label="$1"
  local default_value="$2"
  local value=""

  while true; do
    value="$(prompt_with_default "$label" "$default_value")"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    if [[ -n "$value" ]]; then
      printf '%s' "$value"
      return 0
    fi
    say "A value is required."
  done
}

parse_postgres_db_name() {
  local dsn="$1"
  local prefix="${dsn%%\?*}"
  local db_name=""

  case "$prefix" in
    postgres://*|postgresql://*)
      ;;
    *)
      return 1
      ;;
  esac

  db_name="${prefix##*/}"
  db_name="${db_name%%\#*}"
  [[ -n "$db_name" ]] || return 1
  printf '%s' "$db_name"
}

parse_postgres_user_name() {
  local dsn="$1"
  local prefix="${dsn%%\?*}"
  local authority="${prefix#*://}"
  local credentials=""
  local user_name=""

  authority="${authority%%/*}"
  [[ "$authority" == *"@"* ]] || return 1
  credentials="${authority%@*}"
  user_name="${credentials%%:*}"
  [[ -n "$user_name" ]] || return 1
  printf '%s' "$user_name"
}

parse_postgres_password() {
  local dsn="$1"
  local prefix="${dsn%%\?*}"
  local authority="${prefix#*://}"
  local credentials=""

  authority="${authority%%/*}"
  [[ "$authority" == *"@"* ]] || return 1
  credentials="${authority%@*}"
  [[ "$credentials" == *:* ]] || return 1
  printf '%s' "${credentials#*:}"
}

build_postgres_maintenance_dsn() {
  local dsn="$1"
  local prefix="${dsn%%\?*}"
  local suffix=""

  if [[ "$dsn" == *\?* ]]; then
    suffix="?${dsn#*\?}"
  fi
  [[ "$prefix" == */* ]] || return 1
  printf '%s/postgres%s' "${prefix%/*}" "$suffix"
}

print_postgres_manual_init() {
  local target_dsn="$1"
  local maintenance_dsn="$2"
  local role_name="$3"
  local role_password="$4"
  local db_name="$5"
  local create_role_sql=""
  local create_db_sql=""

  say "Could not provision Postgres automatically with the provided DSN."
  say "Run these bash commands, then rerun ./install_mac.sh:"
  say "  psql \"$maintenance_dsn\" -Atqc \"SELECT 1;\""
  if [[ -n "$role_name" ]]; then
    create_role_sql="DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$(sql_quote "$role_name")') THEN CREATE ROLE $(quote_pg_identifier "$role_name") LOGIN"
    if [[ -n "$role_password" ]]; then
      create_role_sql="$create_role_sql PASSWORD '$(sql_quote "$role_password")'"
    fi
    create_role_sql="$create_role_sql; END IF; END \$\$;"
    say "  psql \"$maintenance_dsn\" -v ON_ERROR_STOP=1 -c \"${create_role_sql}\""
    create_db_sql="SELECT 'CREATE DATABASE $(quote_pg_identifier "$db_name") OWNER $(quote_pg_identifier "$role_name")' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = '$(sql_quote "$db_name")') \\gexec"
  else
    create_db_sql="SELECT 'CREATE DATABASE $(quote_pg_identifier "$db_name")' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = '$(sql_quote "$db_name")') \\gexec"
  fi
  say "  psql \"$maintenance_dsn\" -v ON_ERROR_STOP=1 -c \"${create_db_sql}\""
  say "  psql \"$target_dsn\" -Atqc \"SELECT 1;\""
}

ensure_postgres_database() {
  local target_dsn="$1"
  local db_name=""
  local maintenance_dsn=""
  local exists=""
  local role_name=""
  local role_password=""
  local create_role_sql=""
  local create_db_sql=""

  if [[ "$SKIP_POSTGRES_PROVISION" == "1" ]]; then
    say "Skipping Postgres provisioning because CLI_PROXY_INSTALLER_SKIP_POSTGRES_PROVISION=1"
    return 0
  fi

  db_name="$(parse_postgres_db_name "$target_dsn")" || die "Could not determine database name from Postgres DSN: $target_dsn"
  role_name="$(parse_postgres_user_name "$target_dsn" || true)"
  role_password="$(parse_postgres_password "$target_dsn" || true)"
  if psql "$target_dsn" -Atqc "SELECT 1;" >/dev/null 2>&1; then
    say "Validated Postgres DSN and detected existing Postgres database $db_name"
    return 0
  fi

  maintenance_dsn="$(build_postgres_maintenance_dsn "$target_dsn")" || die "Could not derive maintenance DSN from Postgres DSN: $target_dsn"
  if ! psql "$maintenance_dsn" -Atqc "SELECT 1;" >/dev/null 2>&1; then
    print_postgres_manual_init "$target_dsn" "$maintenance_dsn" "$role_name" "$role_password" "$db_name"
    die "Could not reach PostgreSQL maintenance database using $maintenance_dsn"
  fi

  if [[ -n "$role_name" ]]; then
    exists="$(psql "$maintenance_dsn" -Atqc "SELECT 1 FROM pg_roles WHERE rolname = '$(sql_quote "$role_name")';" 2>/dev/null || true)"
    if [[ "$exists" != "1" ]]; then
      say "Creating Postgres role $role_name"
      create_role_sql="DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$(sql_quote "$role_name")') THEN CREATE ROLE $(quote_pg_identifier "$role_name") LOGIN"
      if [[ -n "$role_password" ]]; then
        create_role_sql="$create_role_sql PASSWORD '$(sql_quote "$role_password")'"
      fi
      create_role_sql="$create_role_sql; END IF; END \$\$;"
      if ! psql "$maintenance_dsn" -v ON_ERROR_STOP=1 -c "$create_role_sql" >/dev/null 2>&1; then
        print_postgres_manual_init "$target_dsn" "$maintenance_dsn" "$role_name" "$role_password" "$db_name"
        die "Failed creating Postgres role $role_name using $maintenance_dsn"
      fi
    fi
  fi

  exists="$(psql "$maintenance_dsn" -Atqc "SELECT 1 FROM pg_database WHERE datname = '$(sql_quote "$db_name")';" 2>/dev/null || true)"
  if [[ "$exists" == "1" ]]; then
    print_postgres_manual_init "$target_dsn" "$maintenance_dsn" "$role_name" "$role_password" "$db_name"
    die "Postgres database $db_name already exists but the target DSN is not reachable: $target_dsn"
  fi

  say "Creating Postgres database $db_name"
  create_db_sql="CREATE DATABASE $(quote_pg_identifier "$db_name")"
  if [[ -n "$role_name" ]]; then
    create_db_sql="$create_db_sql OWNER $(quote_pg_identifier "$role_name")"
  fi
  create_db_sql="$create_db_sql;"
  if ! psql "$maintenance_dsn" -v ON_ERROR_STOP=1 -c "$create_db_sql" >/dev/null 2>&1; then
    print_postgres_manual_init "$target_dsn" "$maintenance_dsn" "$role_name" "$role_password" "$db_name"
    die "Failed creating Postgres database $db_name using $maintenance_dsn"
  fi
  if ! psql "$target_dsn" -Atqc "SELECT 1;" >/dev/null 2>&1; then
    print_postgres_manual_init "$target_dsn" "$maintenance_dsn" "$role_name" "$role_password" "$db_name"
    die "Created Postgres database $db_name but failed to connect using target DSN"
  fi
  say "Validated Postgres DSN after provisioning $db_name"
}

dir_has_entries() {
  local dir_path="$1"
  local first_entry=""

  [[ -d "$dir_path" ]] || return 1
  first_entry="$(find "$dir_path" -mindepth 1 -print -quit 2>/dev/null || true)"
  [[ -n "$first_entry" ]]
}

restore_sqlite_backup() {
  local backup_path="$1"
  local target_db="$2"
  cp -f "$backup_path" "$target_db" || die "Failed restoring $target_db from backup $backup_path"
}

copy_or_patch_config() {
  local source_config="${1:-}"
  local target_config="$2"
  local auth_dir="$3"
  local suffix
  local replace_target=1

  ensure_target_config_parent "$target_config"
  suffix="$(timestamp_utc)"

  if [[ -n "$source_config" ]]; then
    if [[ "$source_config" != "$target_config" && -f "$target_config" ]]; then
      if confirm_yes_no "Target config already exists. Replace it with selected config from $source_config?" "N"; then
        backup_file_if_exists "$target_config" "$suffix"
      else
        replace_target=0
        say "Keeping existing target config at $target_config"
      fi
    fi
    if [[ "$source_config" != "$target_config" && "$replace_target" -eq 1 ]]; then
      cp -f "$source_config" "$target_config"
      say "Copied config from $source_config"
    fi
    set_yaml_scalar "$target_config" "auth-dir" "$auth_dir"
  else
    if [[ -f "$target_config" ]]; then
      if confirm_yes_no "Target config already exists. Replace it with a minimal config?" "N"; then
        backup_file_if_exists "$target_config" "$suffix"
        write_minimal_config "$target_config" "$auth_dir"
      else
        set_yaml_scalar "$target_config" "auth-dir" "$auth_dir"
      fi
    else
      write_minimal_config "$target_config" "$auth_dir"
    fi
  fi
}

merge_auth_dir() {
  local source_dir="$1"
  local target_dir="$2"
  local source_path=""
  local rel_path=""
  local target_path=""
  [[ -d "$source_dir" ]] || return 0
  [[ "$source_dir" != "$target_dir" ]] || return 0
  ensure_dir "$target_dir"

  while IFS= read -r -d '' source_path; do
    rel_path="${source_path#$source_dir/}"
    target_path="$target_dir/$rel_path"
    if [[ -e "$target_path" || -L "$target_path" ]]; then
      continue
    fi
    ensure_dir "$(dirname "$target_path")"
    cp -pP "$source_path" "$target_path"
  done < <(find "$source_dir" -mindepth 1 \( -type f -o -type l \) -print0)

  say "Merged auth files from $source_dir without overwriting existing target files"
}

has_sqlite_table() {
  local db_path="$1"
  local table_name="$2"
  sqlite3 -readonly "$db_path" \
    "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = '$(sql_quote "$table_name")';" \
    2>/dev/null || printf '0'
}

ensure_cache_stats_schema() {
  local db_path="$1"
  local has_reasoning=""

  ensure_dir "$(dirname "$db_path")"
  sqlite3 "$db_path" <<'SQL'
.timeout 15000
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

  has_reasoning="$(sqlite3 -readonly "$db_path" \
    "SELECT COUNT(*) FROM pragma_table_info('cache_statistics_requests') WHERE name = 'reasoning_effort';")"
  if [[ "$has_reasoning" == "0" ]]; then
    sqlite3 "$db_path" <<'SQL' >/dev/null
.timeout 15000
ALTER TABLE cache_statistics_requests ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT '';
SQL
  fi
}

backup_sqlite_db() {
  local db_path="$1"
  local backup_path=""
  backup_path="$(dirname "$db_path")/cache-statistics-backup-$(timestamp_utc).sqlite"
  cp -f "$db_path" "$backup_path" || return 1
  [[ -f "$backup_path" ]] || return 1
  printf '%s' "$backup_path"
}

merge_prompt_cache_index() {
  local source_db="$1"
  local target_db="$2"
  if [[ "$(has_sqlite_table "$source_db" "prompt_cache_response_index")" != "1" ]]; then
    return 0
  fi
  sqlite3 "$target_db" <<SQL >/dev/null
.timeout 15000
ATTACH '$(sql_quote "$source_db")' AS src;
INSERT OR REPLACE INTO main.prompt_cache_response_index (
  response_id,
  prompt_cache_key,
  expires_at,
  updated_at
)
SELECT
  response_id,
  prompt_cache_key,
  expires_at,
  updated_at
FROM src.prompt_cache_response_index;
DETACH src;
SQL
}

merge_cache_statistics_db() {
  local source_db="$1"
  local target_db="$2"
  local backup_path=""
  local merged_count=""
  local source_has_reasoning=""
  local source_reasoning_expr="''"

  [[ -f "$source_db" ]] || return 0
  [[ "$source_db" != "$target_db" ]] || return 0

  ensure_cache_stats_schema "$target_db"
  if ! backup_path="$(backup_sqlite_db "$target_db")"; then
    die "Failed creating backup for $target_db before merge"
  fi
  source_has_reasoning="$(sqlite3 -readonly "$source_db" \
    "SELECT COUNT(*) FROM pragma_table_info('cache_statistics_requests') WHERE name = 'reasoning_effort';" \
    2>/dev/null || printf '0')"
  if [[ "$source_has_reasoning" == "1" ]]; then
    source_reasoning_expr="COALESCE(s.reasoning_effort, '')"
  fi

  if ! merged_count="$(
    sqlite3 "$target_db" <<SQL
.timeout 15000
ATTACH '$(sql_quote "$source_db")' AS src;
INSERT INTO main.cache_statistics_requests (
  requested_at,
  provider,
  model,
  source,
  auth_id,
  auth_index,
  latency_ms,
  failed,
  input_tokens,
  output_tokens,
  reasoning_tokens,
  cached_tokens,
  total_tokens,
  prompt_cache_key,
  previous_response_id,
  response_id,
  prompt_cache_retention,
  reasoning_effort
)
SELECT
  s.requested_at,
  s.provider,
  s.model,
  s.source,
  s.auth_id,
  s.auth_index,
  s.latency_ms,
  s.failed,
  s.input_tokens,
  s.output_tokens,
  s.reasoning_tokens,
  s.cached_tokens,
  s.total_tokens,
  s.prompt_cache_key,
  s.previous_response_id,
  s.response_id,
  s.prompt_cache_retention,
  $source_reasoning_expr
FROM src.cache_statistics_requests s
WHERE NOT EXISTS (
  SELECT 1
  FROM main.cache_statistics_requests t
  WHERE t.requested_at = s.requested_at
    AND t.provider = s.provider
    AND t.model = s.model
    AND t.source = s.source
    AND t.auth_id = s.auth_id
    AND t.auth_index = s.auth_index
    AND t.latency_ms = s.latency_ms
    AND t.failed = s.failed
    AND t.input_tokens = s.input_tokens
    AND t.output_tokens = s.output_tokens
    AND t.reasoning_tokens = s.reasoning_tokens
    AND t.cached_tokens = s.cached_tokens
    AND t.total_tokens = s.total_tokens
    AND t.prompt_cache_key = s.prompt_cache_key
    AND t.previous_response_id = s.previous_response_id
    AND t.response_id = s.response_id
    AND t.prompt_cache_retention = s.prompt_cache_retention
    AND COALESCE(t.reasoning_effort, '') = $source_reasoning_expr
);
SELECT changes();
DETACH src;
SQL
  )"; then
    restore_sqlite_backup "$backup_path" "$target_db"
    die "Failed merging cache-statistics DB $source_db; restored $target_db from $backup_path"
  fi

  merged_count="$(printf '%s\n' "$merged_count" | tail -n1 | tr -d '[:space:]')"
  if ! merge_prompt_cache_index "$source_db" "$target_db"; then
    restore_sqlite_backup "$backup_path" "$target_db"
    die "Failed merging prompt-cache index from $source_db; restored $target_db from $backup_path"
  fi
  say "Merged $merged_count rows from $source_db"
  say "Backed up target DB to $backup_path"
}

build_binary() {
  local repo_root="$1"
  local default_config_path="$2"
  local output_path="$3"
  local version=""
  local commit=""
  local build_date=""
  local ldflags=""
  local build_cmd=""

  version="$(git -C "$repo_root" describe --tags --always --dirty)"
  commit="$(git -C "$repo_root" rev-parse HEAD)"
  build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  ldflags="-s -w"
  ldflags="$ldflags -X \"main.Version=$version\""
  ldflags="$ldflags -X \"main.Commit=$commit\""
  ldflags="$ldflags -X \"main.BuildDate=$build_date\""
  ldflags="$ldflags -X \"main.DefaultConfigPath=$default_config_path\""
  build_cmd="go build -trimpath -ldflags $ldflags -o $output_path ./cmd/server"

  if ! (
    cd "$repo_root"
    go build -trimpath -ldflags "$ldflags" -o "$output_path" ./cmd/server
  ); then
    warn "Build failed while running:"
    warn "  $build_cmd"
    return 1
  fi
  validate_binary_exec "$output_path" || {
    rm -f "$output_path"
    return 1
  }
}

validate_binary_exec() {
  local binary_path="$1"
  local timeout_seconds="${2:-$BINARY_EXEC_TIMEOUT_SECONDS}"
  local stdout_path=""
  local stderr_path=""
  local pid=""
  local deadline=0
  local status=0

  if [[ ! -x "$binary_path" ]]; then
    warn "Binary validation failed: $binary_path is not executable"
    return 1
  fi
  stdout_path="$(mktemp "${TMPDIR:-/tmp}/cli-proxy-binary-check.stdout.XXXXXX")"
  stderr_path="$(mktemp "${TMPDIR:-/tmp}/cli-proxy-binary-check.stderr.XXXXXX")"
  "$binary_path" -h >"$stdout_path" 2>"$stderr_path" &
  pid="$!"
  deadline=$((SECONDS + timeout_seconds))
  while kill -0 "$pid" >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
      kill "$pid" >/dev/null 2>&1 || true
      sleep 1
      kill -9 "$pid" >/dev/null 2>&1 || true
      wait "$pid" >/dev/null 2>&1 || true
      rm -f "$stdout_path" "$stderr_path"
      warn "Binary validation timed out after ${timeout_seconds}s: $binary_path"
      return 1
    fi
    sleep 0.2
  done
  wait "$pid"
  status="$?"
  if (( status != 0 )); then
    warn "Binary validation failed with exit code $status: $binary_path"
    if [[ -s "$stderr_path" ]]; then
      sed 's/^/  /' "$stderr_path" >&2
    fi
    rm -f "$stdout_path" "$stderr_path"
    return 1
  fi
  rm -f "$stdout_path" "$stderr_path"
}

install_binary() {
  local staging_path="$1"
  local target_path="$2"
  local suffix=""
  local backup_path=""

  ensure_dir "$(dirname "$target_path")"
  suffix="$(timestamp_utc)"
  if [[ -f "$target_path" ]]; then
    backup_path="${target_path}.${suffix}.bak"
    mv "$target_path" "$backup_path"
    say "Backed up existing binary to $backup_path"
  fi
  mv "$staging_path" "$target_path"
  chmod +x "$target_path"
  if ! validate_binary_exec "$target_path"; then
    rm -f "$target_path"
    if [[ -n "$backup_path" && -f "$backup_path" ]]; then
      mv "$backup_path" "$target_path"
      chmod +x "$target_path"
      warn "Restored previous binary from $backup_path"
    fi
    return 1
  fi
}

write_launchd_plist() {
  local install_root="$1"
  local binary_path="$2"
  local config_path="$3"
  local plist_path="$HOME/Library/LaunchAgents/${INSTALLER_LABEL}.plist"
  local xml_label=""
  local xml_binary_path=""
  local xml_config_path=""
  local xml_install_root=""
  local xml_stdout_path=""
  local xml_stderr_path=""

  ensure_dir "$HOME/Library/LaunchAgents"
  ensure_dir "$install_root/logs"

  xml_label="$(xml_escape "$INSTALLER_LABEL")"
  xml_binary_path="$(xml_escape "$binary_path")"
  xml_config_path="$(xml_escape "$config_path")"
  xml_install_root="$(xml_escape "$install_root")"
  xml_stdout_path="$(xml_escape "$install_root/logs/launchd.stdout.log")"
  xml_stderr_path="$(xml_escape "$install_root/logs/launchd.stderr.log")"

  cat > "$plist_path" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${xml_label}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${xml_binary_path}</string>
    <string>-config</string>
    <string>${xml_config_path}</string>
  </array>
  <key>WorkingDirectory</key>
  <string>${xml_install_root}</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${xml_stdout_path}</string>
  <key>StandardErrorPath</key>
  <string>${xml_stderr_path}</string>
</dict>
</plist>
EOF

  printf '%s' "$plist_path"
}

bootout_launchd_service() {
  local plist_path="$1"
  local domain="gui/$UID"

  if [[ "$SKIP_LAUNCHD" == "1" ]]; then
    return 0
  fi

  launchctl bootout "$domain" "$plist_path" >/dev/null 2>&1 || true
}

start_launchd_service() {
  local plist_path="$1"
  local domain="gui/$UID"

  if [[ "$SKIP_LAUNCHD" == "1" ]]; then
    say "Skipping launchctl calls because CLI_PROXY_INSTALLER_SKIP_LAUNCHD=1"
    return 0
  fi

  if ! launchctl bootstrap "$domain" "$plist_path"; then
    warn "launchctl bootstrap failed for $plist_path. Installation will continue; use the manual recovery commands below."
    return 1
  fi
  if ! launchctl kickstart -k "$domain/$INSTALLER_LABEL"; then
    warn "launchctl kickstart failed for $INSTALLER_LABEL. Installation will continue; use the manual recovery commands below."
    return 1
  fi
}

print_next_steps() {
  local install_root="$1"
  local binary_path="$2"
  local config_path="$3"
  local auth_dir="$4"
  local stats_db="$5"
  local env_path="${6:-}"
  local plist_path="${7:-}"

  say
  say "Installation complete."
  say "  Install root: $install_root"
  say "  Binary: $binary_path"
  say "  Config: $config_path"
  say "  Auth dir: $auth_dir"
  say "  Stats DB: $stats_db"
  if [[ -f "$env_path" ]]; then
    say "  Postgres env: $env_path"
  fi
  say "  Logs dir: $install_root/logs"
  if [[ -n "$plist_path" ]]; then
    say "  Launchd plist: $plist_path"
    say "  Start service manually:"
    say "    launchctl bootstrap gui/$UID \"$plist_path\""
    say "    launchctl kickstart -k gui/$UID/$INSTALLER_LABEL"
    say "  Stop service manually:"
    say "    launchctl bootout gui/$UID \"$plist_path\""
  fi
  say "  Run manually:"
  say "    cd \"$install_root\" && \"./$(basename "$binary_path")\""
}

main() {
  local repo_root=""
  local install_root_input=""
  local install_root=""
  local auth_dir_input=""
  local auth_dir=""
  local build_now=0
  local create_service=0
  local start_service=0
  local selected_config=""
  local config_path=""
  local binary_path=""
  local stats_db_path=""
  local staging_binary=""
  local env_path=""
  local plist_path=""
  local source=""
  local auth_merge_sources=()
  local db_merge_sources=()
  local use_postgres=0
  local pgstore_dsn=""
  local pgstore_schema=""
  local pgstore_local_path=""
  local existing_pgstore_dsn=""
  local existing_pgstore_schema=""
  local existing_pgstore_local_path=""
  local postgres_default_answer="N"

  require_tools
  repo_root="$(resolve_repo_root)"

  install_root_input="$(prompt_with_default "Install location" "$DEFAULT_INSTALL_ROOT")"
  install_root="$(expand_path "$install_root_input")"
  auth_dir_input="$(prompt_with_default "Auth folder" "$install_root/auth")"
  auth_dir="$(expand_path "$auth_dir_input")"
  if confirm_yes_no "Build binary from source now?" "Y"; then
    build_now=1
  fi
  if confirm_yes_no "Create launchd service?" "Y"; then
    create_service=1
    if confirm_yes_no "Start service after install?" "Y"; then
      start_service=1
    fi
  fi

  detect_sources "$install_root"
  print_detection_summary
  selected_config="$(choose_config_source "$install_root/config.yaml" || true)"

  ensure_dir "$install_root"
  ensure_dir "$auth_dir"
  ensure_dir "$install_root/stats"
  ensure_dir "$install_root/logs"

  config_path="$install_root/config.yaml"
  binary_path="$install_root/cli-proxy-api"
  stats_db_path="$install_root/stats/cache-statistics.sqlite"
  env_path="$install_root/.env"
  while IFS= read -r source; do
    [[ -n "$source" ]] || continue
    auth_merge_sources+=("$source")
  done < <(append_sources_excluding_target AUTH_SOURCES "$auth_dir")
  while IFS= read -r source; do
    [[ -n "$source" ]] || continue
    db_merge_sources+=("$source")
  done < <(append_sources_excluding_target DB_SOURCES "$stats_db_path")

  copy_or_patch_config "$selected_config" "$config_path" "$auth_dir"

  if [[ "${#auth_merge_sources[@]}" -gt 0 ]]; then
    if dir_has_entries "$auth_dir"; then
      if confirm_yes_no "Target auth folder already has files. Merge detected auth files into $auth_dir without overwriting existing files?" "N"; then
        for source in "${auth_merge_sources[@]}"; do
          merge_auth_dir "$source" "$auth_dir"
        done
      fi
    elif confirm_yes_no "Merge detected auth files into $auth_dir?" "Y"; then
      for source in "${auth_merge_sources[@]}"; do
        merge_auth_dir "$source" "$auth_dir"
      done
    fi
  fi

  if [[ "${#db_merge_sources[@]}" -gt 0 ]] && confirm_yes_no "Merge detected cache-statistics databases into $stats_db_path?" "Y"; then
    for source in "${db_merge_sources[@]}"; do
      merge_cache_statistics_db "$source" "$stats_db_path"
    done
  else
    ensure_cache_stats_schema "$stats_db_path"
  fi

  existing_pgstore_dsn="$(read_env_scalar "$env_path" "PGSTORE_DSN" || true)"
  existing_pgstore_schema="$(read_env_scalar "$env_path" "PGSTORE_SCHEMA" || true)"
  existing_pgstore_local_path="$(read_env_scalar "$env_path" "PGSTORE_LOCAL_PATH" || true)"
  if [[ -z "$existing_pgstore_schema" ]]; then
    existing_pgstore_schema="public"
  fi
  if [[ -z "$existing_pgstore_local_path" ]]; then
    existing_pgstore_local_path="$install_root"
  fi
  if [[ -n "$existing_pgstore_dsn" ]]; then
    postgres_default_answer="Y"
  fi

  if confirm_yes_no "Configure Postgres-backed auth/config/statistics store?" "$postgres_default_answer"; then
    use_postgres=1
    require_postgres_tools
    pgstore_dsn="$(prompt_required_value "Postgres DSN" "$existing_pgstore_dsn")"
    pgstore_schema="$(prompt_required_value "Postgres schema" "$existing_pgstore_schema")"
    pgstore_local_path="$(prompt_required_value "Local migration seed path" "$existing_pgstore_local_path")"
    pgstore_local_path="$(expand_path "$pgstore_local_path")"
    ensure_postgres_database "$pgstore_dsn"
    write_pgstore_env "$env_path" "$pgstore_dsn" "$pgstore_schema" "$pgstore_local_path"
  else
    clear_pgstore_env "$env_path"
  fi

  if [[ "$build_now" -eq 1 ]]; then
    staging_binary="$(mktemp "$install_root/cli-proxy-api.staging.XXXXXX")"
    build_binary "$repo_root" "$config_path" "$staging_binary"
    install_binary "$staging_binary" "$binary_path"
  elif [[ -x "$binary_path" ]]; then
    die "Build was skipped, but the existing binary at $binary_path would hide source changes. Re-run install_mac.sh and choose to build from source so UI changes are installed."
  else
    die "No existing binary found at $binary_path and build was skipped."
  fi

  if [[ "$create_service" -eq 1 ]]; then
    plist_path="$HOME/Library/LaunchAgents/${INSTALLER_LABEL}.plist"
    bootout_launchd_service "$plist_path"
    plist_path="$(write_launchd_plist "$install_root" "$binary_path" "$config_path")"
    if [[ "$start_service" -eq 1 ]]; then
      start_launchd_service "$plist_path" || true
    fi
  fi

  print_next_steps "$install_root" "$binary_path" "$config_path" "$auth_dir" "$stats_db_path" "$env_path" "$plist_path"
}

main "$@"
