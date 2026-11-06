#!/bin/sh
# Read-only full backup of the Semaphore installation (hard rule 2, MIG-2).
# Creates /opt/backups/semaphore-<timestamp>/ with a consistent DB dump,
# config.json, data dirs and checksums. Never modifies Semaphore.
#
# Usage: scripts/backup-semaphore.sh [compose-dir]
#   SEMAPHORE_APP=semaphore-semaphore-1 SEMAPHORE_DB=semaphore-mysql-1 (overridable)
set -eu

COMPOSE_DIR="${1:-/root/semaphore}"
APP="${SEMAPHORE_APP:-semaphore-semaphore-1}"
DBC="${SEMAPHORE_DB:-semaphore-mysql-1}"
DEST="/opt/backups/semaphore-$(date +%Y-%m-%d-%H%M)"

umask 077
mkdir -p "$DEST"

# DB dump: credentials are read inside the container, never printed.
# MYSQL_PWD avoids the password on the command line.
docker exec "$DBC" sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysqldump --single-transaction --routines --triggers --no-tablespaces -u"$MYSQL_USER" "$MYSQL_DATABASE"' \
  2>"$DEST/mysqldump.stderr" | gzip -9 >"$DEST/semaphore-db.sql.gz"
gzip -t "$DEST/semaphore-db.sql.gz"
zcat "$DEST/semaphore-db.sql.gz" | tail -n 1 | grep -q 'Dump completed' || { echo "dump incomplete" >&2; exit 1; }

docker cp "$APP:/etc/semaphore/config.json" "$DEST/config.json"
# SEMAPHORE_ACCESS_KEY_ENCRYPTION in the container environment overrides
# config.json; record the effective value (same sensitivity as config.json).
docker inspect "$APP" --format '{{range .Config.Env}}{{println .}}{{end}}' |
  sed -n 's/^SEMAPHORE_ACCESS_KEY_ENCRYPTION=//p' |
  python3 -c 'import json,sys; v=sys.stdin.read().strip(); print(json.dumps({"access_key_encryption": v} if v else {}))' \
  >"$DEST/semaphore-effective-config.json"
docker cp "$APP:/var/lib/semaphore" "$DEST/var-lib-semaphore"
[ -d "$COMPOSE_DIR" ] && tar -czf "$DEST/root-semaphore.tar.gz" -C "$(dirname "$COMPOSE_DIR")" "$(basename "$COMPOSE_DIR")"
docker image inspect "$(docker inspect "$APP" --format '{{.Config.Image}}')" --format '{{index .Config.Labels "org.opencontainers.image.version"}} {{.Id}}' >"$DEST/semaphore-version.txt" 2>/dev/null || true

(cd "$DEST" && find . -type f ! -name SHA256SUMS -exec sha256sum {} + >SHA256SUMS)
chmod -R go-rwx "$DEST"
echo "$DEST"
