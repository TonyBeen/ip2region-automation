#!/usr/bin/env bash
set -euo pipefail

DATA_DIR=/var/lib/ip2region
KEEP_OLD=2
AUTO_CONFIRM=0

usage() {
  cat <<'EOF'
Usage: sudo bash scripts/cleanup.sh [--data-dir PATH] [--keep COUNT] [--yes]

Keep the current release and the newest COUNT other releases (default: 2).
Remove older releases and abandoned staging directories. Without --yes, ask
for confirmation after showing the removal list.
EOF
}

while (($#)); do
  case "$1" in
    --data-dir)
      (($# >= 2)) || { usage >&2; exit 2; }
      DATA_DIR=$2
      shift 2
      ;;
    --keep)
      (($# >= 2)) || { usage >&2; exit 2; }
      KEEP_OLD=$2
      shift 2
      ;;
    --yes)
      AUTO_CONFIRM=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root: sudo bash scripts/cleanup.sh" >&2
  exit 1
fi
if [[ ! "$KEEP_OLD" =~ ^[0-9]+$ ]]; then
  echo "--keep must be a non-negative integer." >&2
  exit 2
fi

DATA_DIR="$(cd "$DATA_DIR" 2>/dev/null && pwd -P)" || {
  echo "Data directory does not exist: $DATA_DIR" >&2
  exit 1
}
RELEASES_DIR="$DATA_DIR/releases"
STAGING_DIR="$DATA_DIR/staging"
if [[ ! -d "$RELEASES_DIR" ]]; then
  echo "No releases directory found at $RELEASES_DIR. Nothing to clean."
  exit 0
fi

# Share the updater lock so cleanup cannot race with a download or activation.
touch "$DATA_DIR/.updater.lock"
chown root:ip2region "$DATA_DIR/.updater.lock"
chmod 0660 "$DATA_DIR/.updater.lock"
exec 9>"$DATA_DIR/.updater.lock"
if ! flock -n 9; then
  echo "An update is running; stop and retry cleanup later." >&2
  exit 1
fi

CURRENT=""
if [[ -L "$DATA_DIR/current" ]]; then
  CURRENT="$(readlink -f "$DATA_DIR/current" || true)"
  if [[ ! -d "$CURRENT" ]]; then
    echo "current points to a missing release; refusing cleanup." >&2
    exit 1
  fi
fi

mapfile -t RELEASES < <(
  find "$RELEASES_DIR" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' \
    | sort -rn \
    | cut -d ' ' -f 2-
)
if (( ${#RELEASES[@]} > 0 )) && [[ -z "$CURRENT" ]]; then
  echo "current symlink is missing; refusing to clean releases." >&2
  exit 1
fi

KEEP_COUNT=0
REMOVE_RELEASES=()
for RELEASE in "${RELEASES[@]}"; do
  if [[ -n "$CURRENT" && "$RELEASE" == "$CURRENT" ]]; then
    continue
  fi
  if (( KEEP_COUNT < KEEP_OLD )); then
    KEEP_COUNT=$((KEEP_COUNT + 1))
    continue
  fi
  REMOVE_RELEASES+=("$RELEASE")
done

mapfile -t STAGING_DIRS < <(
  if [[ -d "$STAGING_DIR" ]]; then
    find "$STAGING_DIR" -mindepth 1 -maxdepth 1 -type d -print
  fi
)

if (( ${#REMOVE_RELEASES[@]} == 0 && ${#STAGING_DIRS[@]} == 0 )); then
  echo "No old releases or abandoned staging directories to remove."
  exit 0
fi

if (( ${#REMOVE_RELEASES[@]} )); then
  printf 'Old releases to remove:\n'
  printf '  %s\n' "${REMOVE_RELEASES[@]}"
fi
if (( ${#STAGING_DIRS[@]} )); then
  printf 'Staging directories to remove:\n'
  printf '  %s\n' "${STAGING_DIRS[@]}"
fi

if (( ! AUTO_CONFIRM )); then
  read -r -p 'Delete the listed directories? [y/N] ' ANSWER
  case "$ANSWER" in
    y|Y|yes|YES) ;;
    *) echo "Cancelled."; exit 0 ;;
  esac
fi

for PATH_TO_REMOVE in "${REMOVE_RELEASES[@]}" "${STAGING_DIRS[@]}"; do
  [[ -n "$PATH_TO_REMOVE" ]] || continue
  rm -rf -- "$PATH_TO_REMOVE"
  echo "Deleted $PATH_TO_REMOVE"
done
