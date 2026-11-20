#!/usr/bin/env bash
set -euo pipefail

DATA_DIR="${IP2REGION_DATA_DIR:-/var/lib/ip2region}"
PORT="${PORT:-28080}"
PATH_PREFIX="${PATH_PREFIX:-/ifconfig}"
CACHE_POLICY="${CACHE_POLICY:-vindex}"
SEARCHERS="${SEARCHERS:-50}"
UPDATE_INTERVAL="${UPDATE_INTERVAL:-6h}"
RELOAD_URL="http://127.0.0.1:${PORT}${PATH_PREFIX}/reload"
IP2REGION_RELOAD_COMMAND="${IP2REGION_RELOAD_COMMAND:-curl -s -f -X POST ${RELOAD_URL}}"

# Convert UPDATE_INTERVAL (e.g. 6h, 30m, 3600) to seconds
parse_interval() {
  local val="$1"
  if [[ "$val" =~ ^([0-9]+)h$ ]]; then
    echo $((${BASH_REMATCH[1]} * 3600))
  elif [[ "$val" =~ ^([0-9]+)m$ ]]; then
    echo $((${BASH_REMATCH[1]} * 60))
  elif [[ "$val" =~ ^([0-9]+)s?$ ]]; then
    echo "${BASH_REMATCH[1]}"
  else
    echo 21600 # 6 hours default
  fi
}

INTERVAL_SECONDS=$(parse_interval "$UPDATE_INTERVAL")

echo "=========================================================="
echo " Starting ip2region service"
echo " Data dir:        $DATA_DIR"
echo " Port:            $PORT"
echo " Path prefix:     $PATH_PREFIX"
echo " Cache policy:    $CACHE_POLICY"
echo " Searchers:       $SEARCHERS"
echo " Update interval: $UPDATE_INTERVAL (${INTERVAL_SECONDS}s)"
echo " Reload command:  $IP2REGION_RELOAD_COMMAND"
echo "=========================================================="

mkdir -p "$DATA_DIR"

# 1. Launch Go query server first so health check & port are immediately open
export IP2REGION_DATA_DIR="$DATA_DIR"
export PORT="$PORT"
export PATH_PREFIX="$PATH_PREFIX"
export CACHE_POLICY="$CACHE_POLICY"
export SEARCHERS="$SEARCHERS"

/usr/local/bin/ip2region-server &
SERVER_PID=$!

# Trap signals for graceful shutdown
cleanup() {
  echo "[STOP] Received signal, shutting down..."
  if kill -0 "$SERVER_PID" 2>/dev/null; then
    kill -TERM "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  exit 0
}
trap cleanup SIGTERM SIGINT

# 2. If current database does not exist, run initial sync in background and reload
if [[ ! -e "$DATA_DIR/current" ]]; then
  echo "[INIT] No database found at $DATA_DIR/current. Triggering initial download..."
  (
    python3 /app/updater.py --data-dir "$DATA_DIR" --reload-command "$IP2REGION_RELOAD_COMMAND" || {
      echo "[WARN] Initial download failed (network issue?). Will retry in scheduled loop."
    }
  ) &
fi

# 3. Background periodic updater loop
(
  while true; do
    sleep "$INTERVAL_SECONDS"
    echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')] Checking for updates..."
    python3 /app/updater.py --data-dir "$DATA_DIR" --reload-command "$IP2REGION_RELOAD_COMMAND" || {
      echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')] Updater check failed; will retry next cycle."
    }
    # Run cleanup to keep current + 2 old releases
    bash /app/scripts/cleanup.sh --data-dir "$DATA_DIR" --keep 2 --yes >/dev/null 2>&1 || true
  done
) &
UPDATER_PID=$!

# Wait on server process
wait "$SERVER_PID"
