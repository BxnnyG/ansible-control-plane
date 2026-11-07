#!/bin/sh
# Backup of the ACP database (the only state store). Never includes secrets/master.key.
# Usage: scripts/backup-acp.sh [-p compose-project] [-o backup-root]
#   Writes <root>/acp-<YYYY-MM-DD-HHMM>/{acp.dump,manifest.txt}. Default root: /opt/backups.
# Format is compatible with earlier ad hoc backups (acp.dump = pg_dump -Fc).
# shellcheck disable=SC2016  # remote shell expands the container env on purpose
set -eu

PROJECT=""
ROOT="/opt/backups"
while getopts "p:o:" o; do
  case "$o" in
    p) PROJECT="$OPTARG" ;;
    o) ROOT="$OPTARG" ;;
    *) echo "usage: $0 [-p compose-project] [-o backup-root]" >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")/.."
dc() { if [ -n "$PROJECT" ]; then docker compose -p "$PROJECT" "$@"; else docker compose "$@"; fi; }

DEST="$ROOT/acp-$(date +%Y-%m-%d-%H%M)"
[ ! -e "$DEST" ] || { echo "backup dir exists: $DEST" >&2; exit 1; }
umask 077
mkdir -p "$DEST"
trap 'rm -rf "$DEST"' EXIT

dc exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' >"$DEST/acp.dump"
# Validate: archive must be readable.
dc exec -T postgres pg_restore -l <"$DEST/acp.dump" >/dev/null

MIG=$(dc exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "select coalesce(max(version_id),0) from goose_db_version where is_applied"' | tr -d '\r')
VER=$(dc exec -T app acp version 2>/dev/null | head -n 1 | tr -d '\r' || true)
[ -n "$VER" ] || VER="unknown"
{
  echo "created_utc: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "app_version: $VER"
  echo "goose_version: $MIG"
  echo "format: pg_dump custom (pg_restore)"
  echo "excluded: secrets/master.key (back up separately), acpdata volume (re-created by project sync)"
  echo "sha256:"
  (cd "$DEST" && sha256sum acp.dump | sed 's/^/  /')
} >"$DEST/manifest.txt"

trap - EXIT
echo "backup written: $DEST"
echo "REMINDER: secrets/master.key is NOT included; without it stored secrets cannot be decrypted."
