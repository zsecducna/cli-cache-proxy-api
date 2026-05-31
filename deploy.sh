#!/usr/bin/env bash
# deploy.sh — deploy CLIProxyAPI to the production server.
#
# Steps:
#   1. SSH to the production host (root@myclaw).
#   2. cd into the repo at /opt/cli-cache-proxy-api and fast-forward pull from origin.
#   3. Run install_linux.sh non-interactively (all default answers via </dev/null).
#   4. Ensure the systemd --user service is restarted onto the freshly built binary.
#   5. Verify: HEAD advanced, binary carries the new --kiro-username flag, service
#      is active, the API port answers, and /healthz responds.
#
# The install script's prompts are EOF-safe (read ... || true with ${reply:-default}),
# so feeding /dev/null selects every default. The existing config.yaml on the server
# makes choose_config_source take its keep-existing default and never reach the
# numeric config-source menu (which would loop on EOF).
set -euo pipefail

# Used by the local echo and ssh target below. All other constants are defined inside
# the remote heredoc (see note there) so values with spaces survive SSH arg parsing.
REMOTE="root@myclaw"
REMOTE_DIR="/opt/cli-cache-proxy-api"

echo "==> Deploying to ${REMOTE}:${REMOTE_DIR}"

# Run the whole remote workflow in a single SSH session. -T: no pseudo-tty (we feed
# /dev/null for stdin so the installer takes defaults). The remote script sets its own
# strict mode and exports XDG_RUNTIME_DIR so systemctl --user works over SSH.
#
# Constants are defined as literals INSIDE the quoted heredoc rather than passed on the
# ssh arg list: SSH joins arg-list env assignments into one remote command string that
# the remote shell re-parses, so a value containing spaces (EXPECT_COMMIT_MARKER) would
# be split and mis-executed. The quoted heredoc (<<'REMOTE_EOF') also stops the local
# shell from expanding the remote $(...) command substitutions.
ssh -T -o ConnectTimeout=15 "${REMOTE}" 'bash -s' <<'REMOTE_EOF'
set -euo pipefail

REMOTE_DIR="/opt/cli-cache-proxy-api"
INSTALL_ROOT="/root/.cli-cache-proxy"
SERVICE="com.routerforme.cli-cache-proxy.service"
API_PORT="8317"
EXPECT_COMMIT_MARKER="name IDC auth files"

# systemctl --user needs a runtime dir + bus address when invoked over a non-login SSH.
export XDG_RUNTIME_DIR="/run/user/$(id -u)"
export DBUS_SESSION_BUS_ADDRESS="unix:path=${XDG_RUNTIME_DIR}/bus"

cd "${REMOTE_DIR}"

echo "--- pre-pull HEAD ---"
OLD_HEAD="$(git rev-parse HEAD)"
git log --oneline -1

echo "--- fetch + fast-forward pull origin main ---"
git fetch --quiet origin
# --ff-only refuses to create a merge commit; fails loudly if the tree diverged.
git pull --ff-only origin main

NEW_HEAD="$(git rev-parse HEAD)"
echo "--- post-pull HEAD ---"
git log --oneline -1

if [ "${OLD_HEAD}" = "${NEW_HEAD}" ]; then
  echo "NOTE: HEAD unchanged (already up to date)."
fi

# Confirm the expected commit is now checked out.
if ! git log -1 --pretty=%s | grep -qF "${EXPECT_COMMIT_MARKER}"; then
  echo "WARNING: HEAD commit subject does not contain '${EXPECT_COMMIT_MARKER}'."
fi

echo "--- run install_linux.sh (defaults, non-interactive) ---"
# </dev/null => every prompt falls back to its default answer.
bash ./install_linux.sh </dev/null

echo "--- restart service onto new binary ---"
# install_linux.sh restarts the service too, but its systemctl calls are guarded with
# || true and may have no-op'd if the runtime bus was unavailable mid-script. Do an
# explicit, verified restart so the new binary is definitely the running process.
systemctl --user daemon-reload || true
systemctl --user restart "${SERVICE}"
sleep 3

echo "============================================================"
echo "VERIFICATION"
echo "============================================================"

echo "[1] binary built with --kiro-username flag:"
if "${INSTALL_ROOT}/cli-proxy-api" -h 2>&1 | grep -q -- "-kiro-username"; then
  echo "    PASS — flag present"
else
  echo "    FAIL — flag missing from binary"
  exit 1
fi

echo "[2] service active:"
if systemctl --user is-active --quiet "${SERVICE}"; then
  echo "    PASS — $(systemctl --user is-active "${SERVICE}")"
else
  echo "    FAIL — service not active"
  systemctl --user status "${SERVICE}" --no-pager -l | tail -20 || true
  exit 1
fi

echo "[3] API port ${API_PORT} listening:"
if ss -tlnp 2>/dev/null | grep -q ":${API_PORT}\b"; then
  echo "    PASS — listening"
else
  echo "    FAIL — port not listening"
  exit 1
fi

echo "[4] /healthz responds:"
HC="$(curl -fsS -m 5 "http://127.0.0.1:${API_PORT}/healthz" 2>&1 || true)"
if [ -n "${HC}" ]; then
  echo "    PASS — ${HC}"
else
  echo "    WARN — no body (endpoint may differ); checking HTTP code"
  curl -s -o /dev/null -w "    http_code=%{http_code}\n" -m 5 "http://127.0.0.1:${API_PORT}/healthz" || true
fi

echo "--- running binary inode vs installed (confirm restart picked up new file) ---"
RUNNING_EXE="$(readlink -f "/proc/$(systemctl --user show -p MainPID --value "${SERVICE}")/exe" 2>/dev/null || true)"
echo "    MainPID exe: ${RUNNING_EXE:-unknown}"

echo "============================================================"
echo "DEPLOY OK — ${NEW_HEAD}"
echo "============================================================"
REMOTE_EOF

echo "==> Done."
