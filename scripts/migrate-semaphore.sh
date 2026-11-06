#!/bin/sh
# Semaphore migration helper (ADR-0017). Restores the dump of a verified backup
# into a throwaway MySQL container (tmpfs, random password, removed on exit) and
# runs `acp migrate semaphore` against it inside the ACP compose project.
# The live Semaphore installation is never contacted.
#
# Usage: scripts/migrate-semaphore.sh <backup-dir> [acp migrate semaphore flags...]
# Example: scripts/migrate-semaphore.sh /opt/backups/semaphore-2026-11-05-1200 --workspace default --json
set -eu

[ "$#" -ge 1 ] || { echo "usage: $0 <backup-dir> [acp migrate semaphore flags...]" >&2; exit 2; }
backup="$(cd "$1" && pwd)"
shift

MYSQL_IMAGE="${MYSQL_IMAGE:-mysql:8.0}"
root="$(cd "$(dirname "$0")/.." && pwd)"
[ -f "$backup/semaphore-db.sql.gz" ] || { echo "missing $backup/semaphore-db.sql.gz" >&2; exit 1; }
[ -f "$backup/SHA256SUMS" ] || { echo "missing $backup/SHA256SUMS" >&2; exit 1; }

(cd "$backup" && sha256sum -c --quiet SHA256SUMS) || { echo "backup checksum verification failed" >&2; exit 1; }

name="acp-migrate-mysql-$$"
pw="$(head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n')"
net=""
stage=""

cleanup() {
  [ -z "$net" ] || docker network disconnect "$net" "$name" >/dev/null 2>&1 || true
  docker rm -f "$name" >/dev/null 2>&1 || true
  [ -z "$stage" ] || rm -rf "$stage"
}
trap cleanup EXIT INT TERM

# The app container runs as uid 10001 and cannot read the root-only backup.
# Stage a private copy for it instead of loosening the backup's permissions.
stage="$(mktemp -d)"
chmod 700 "$stage"
cp -a "$backup/." "$stage/"
chown -R "${ACP_UID:-10001}:${ACP_UID:-10001}" "$stage"

# The password only lives in this process and the throwaway container.
docker run -d --name "$name" --tmpfs /var/lib/mysql:size=512m \
  -e MYSQL_ROOT_PASSWORD="$pw" -e MYSQL_DATABASE=semaphore "$MYSQL_IMAGE" >/dev/null

i=0
until docker exec -e MYSQL_PWD="$pw" "$name" mysql -h127.0.0.1 -uroot -e 'SELECT 1' >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -le 90 ] || { echo "mysql did not become ready" >&2; exit 1; }
  sleep 1
done

echo "restoring dump into throwaway mysql" >&2
zcat "$backup/semaphore-db.sql.gz" | docker exec -i -e MYSQL_PWD="$pw" "$name" mysql -uroot semaphore

# Make the throwaway database reachable for the app container.
net="$(docker network ls -q --filter label=com.docker.compose.project=acp --filter label=com.docker.compose.network=internal | head -n 1)"
[ -n "$net" ] || { echo "ACP compose network not found (is the stack up?)" >&2; exit 1; }
docker network connect "$net" "$name"

ACP_MIGRATE_SOURCE_DSN="root:$pw@tcp($name:3306)/semaphore"
export ACP_MIGRATE_SOURCE_DSN

cd "$root"
docker compose run --rm --no-deps \
  -v "$stage:/backup:ro" \
  -e ACP_MIGRATE_SOURCE_DSN \
  app migrate semaphore --backup-dir /backup "$@"
