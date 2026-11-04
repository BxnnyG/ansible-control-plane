#!/bin/sh
# Runs the DB-backed Go tests against a throwaway PostgreSQL, one database per
# package. Container and network are removed on exit.
set -eu
GO_IMAGE="${GO_IMAGE:-golang:1.27.2-alpine3.24}"
PG_IMAGE="${PG_IMAGE:-postgres:18.6-alpine}"
PACKAGES="${TEST_DB_PACKAGES:-db auth admin server projects inventory}"
root="$(cd "$(dirname "$0")/.." && pwd)"
suffix="$$"
net="acp-testdb-net-$suffix"
pg="acp-testdb-pg-$suffix"
pw="$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"

cleanup() {
  rm -f "${out:-}"
  docker rm -f "$pg" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker network create "$net" >/dev/null
docker run -d --name "$pg" --network "$net" -e POSTGRES_PASSWORD="$pw" "$PG_IMAGE" >/dev/null
i=0
until docker exec "$pg" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -le 60 ] || { echo "postgres did not become ready" >&2; exit 1; }
  sleep 1
done
sleep 1

out="$(mktemp)"
status=0
for p in $PACKAGES; do
  dbname="acp_test_$p"
  docker exec "$pg" psql -U postgres -h 127.0.0.1 -qc "CREATE DATABASE $dbname" >/dev/null
  echo "== internal/$p"
  docker run --rm --network "$net" -v "$root:/src" -w /src -e GOFLAGS=-buildvcs=false \
    -v acp-gocache:/go \
    -e ACP_TEST_DATABASE_URL="postgres://postgres:$pw@$pg:5432/$dbname?sslmode=disable" \
    "$GO_IMAGE" sh -c 'apk add --no-cache git openssh-client-default >/dev/null && go test -count=1 ./internal/'"$p"'/...' >"$out" 2>&1 || status=1
  tail -n 30 "$out"
done
exit "$status"
