#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root: sudo bash scripts/deploy.sh" >&2
  exit 1
fi

if ! command -v python3.5 >/dev/null 2>&1; then
  echo "Python 3.5 is required but python3.5 was not found." >&2
  exit 1
fi

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR=/opt/ip2region-automation
DATA_DIR=/var/lib/ip2region
VENV_DIR="$APP_DIR/venv"

if ! getent group ip2region >/dev/null 2>&1; then
  groupadd --system ip2region
fi
if ! id ip2region >/dev/null 2>&1; then
  useradd --system --gid ip2region --home-dir "$DATA_DIR" --shell /usr/sbin/nologin ip2region
fi

install -d -o root -g root "$APP_DIR"
install -m 0644 "$REPO_DIR/updater.py" "$APP_DIR/updater.py"

if [[ ! -x "$VENV_DIR/bin/python" ]]; then
  python3.5 -m venv "$VENV_DIR" || {
    echo "Could not create the venv. Install the Python 3.5 venv package and rerun." >&2
    exit 1
  }
fi

install -m 0644 "$REPO_DIR/systemd/ip2region-update.service" /etc/systemd/system/ip2region-update.service
install -m 0644 "$REPO_DIR/systemd/ip2region-update.timer" /etc/systemd/system/ip2region-update.timer

if [[ ! -e /etc/default/ip2region-update ]]; then
  install -o root -g ip2region -m 0640 \
    "$REPO_DIR/systemd/ip2region-update.env.example" \
    /etc/default/ip2region-update
fi

systemctl daemon-reload
systemctl enable ip2region-update.timer
systemctl restart ip2region-update.timer

echo "Deployment complete. Timer status:"
systemctl --no-pager status ip2region-update.timer || true
echo "The first automatic check runs about five minutes after the timer starts."
