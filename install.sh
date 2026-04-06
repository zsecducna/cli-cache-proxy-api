#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf 'install.sh is deprecated; redirecting to install_mac.sh\n' >&2
exec bash "$script_dir/install_mac.sh" "$@"
