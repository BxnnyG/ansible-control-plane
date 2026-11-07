#!/bin/sh
# Restore an ACP backup into the given compose project. DESTRUCTIVE: replaces the database.
# Usage: scripts/restore-acp.sh -p <compose-project> -b <backup-dir> --yes
# The project must be given explicitly (no default). Needs the matching secrets/master.key.
# shellcheck disable=SC2016  # remote shell expands the container env on purpose
set -eu

PROJECT=""; BK=""; YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    -p) PROJECT="$2"; shift 2 ;;
    -b) BK="$2"; shift 2 ;;
    --yes) YES=1; shift ;;
    *) echo "usage: $0 -p <compose-project> -b <backup-dir> --yes" >&2; exit 2 ;;
  esac
done
[ -n "$PROJECT" ] && [ -n "$BK" ] || { echo "-p <compose-project> and -b <backup-dir> are required" >&2; exit 2; }
[ "$YES" = 1 ] || { echo "refusing: pass --yes to confirm replacing the database of project '$PROJECT'" >&2; exit 2; }
[ -f "$BK/acp.dump" ] || { echo "no acp.dump in $BK" >&2; exit 1; }

cd "$(dirname "$0")/.."
dc() { docker compose -p "$PROJECT" "$@"; }

if [ -f "$BK/manifest.txt" ]; then
  EXP=$(sed -n 's/^  \([0-9a-f]\{64\}\)  acp.dump$/\1/p' "$BK/manifest.txt")
  ACT=$(sha256sum "$BK/acp.dump" | cut -d' ' -f1)
  [ "$EXP" = "$ACT" ] || { echo "checksum mismatch for acp.dump" >&2; exit 1; }
else
  echo "warning: no manifest.txt, checksum not verified" >&2
fi

dc up -d --no-build postgres
dc exec -T postgres sh -c 'until pg_isready -q -U "$POSTGRES_USER"; do sleep 1; done'
dc stop app worker

# Recreate the database, then restore (no owner/ACL: the target role owns everything).
dc exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 \
  -c "drop database if exists \"$POSTGRES_DB\" with (force)" -c "create database \"$POSTGRES_DB\""'
dc exec -T postgres sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --no-acl --exit-on-error' <"$BK/acp.dump"

# The app applies pending migrations on start.
dc up -d --no-build app worker
i=0
until dc exec -T app wget -q -O /dev/null http://127.0.0.1:8080/readyz 2>/dev/null; do
  i=$((i+1)); [ "$i" -lt 60 ] || { echo "app not ready after restore" >&2; exit 1; }
  sleep 2
done
echo "restore complete, app ready (project: $PROJECT)"
