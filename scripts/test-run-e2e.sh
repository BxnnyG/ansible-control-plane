#!/bin/sh
# End-to-end smoke test of run execution (ADR-0016) on a throwaway compose
# project: postgres, a dumb-HTTP git server with a fixture repo, app and worker.
# Seeds a workspace/project/inventory/template via SQL, syncs the project with
# `acp admin project sync`, then checks a successful run and a cancelled run.
# Uses its own project name, volumes and images (acp-e2e-*:test); the live stack
# is never touched. Output is truncated.
set -eu
cd "$(dirname "$0")/.."
ROOT=$(pwd)
PROJ="acp-e2e-$$"
E2E_DIR=$(mktemp -d)
E2E_DB_PASSWORD=$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')
export E2E_DIR E2E_DB_PASSWORD
DC="docker compose -p $PROJ -f $ROOT/scripts/e2e/compose.yml"
fail=0

cleanup() {
  [ "${KEEP:-0}" = 1 ] && { echo "KEEP=1: project $PROJ, dir $E2E_DIR left in place"; return; }
  $DC down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$E2E_DIR"
}
trap cleanup EXIT INT TERM

step() { echo "== $*"; }
die() { echo "FAIL: $*" >&2; fail=1; $DC logs --tail 25 app worker 2>&1 | cut -c1-200 | tail -n 50 >&2; exit 1; }
psql_() { $DC exec -T postgres psql -U acp -d acp -v ON_ERROR_STOP=1 -qtAX "$@"; }

step "build images (distinct tags, live images untouched)"
docker build -q -t acp-e2e-app:test -f Dockerfile . >/dev/null
docker build -q -t acp-e2e-worker:test -f Dockerfile.worker . >/dev/null

step "prepare secrets and git fixture"
mkdir -p "$E2E_DIR/secrets" "$E2E_DIR/git"
(umask 077
 head -c 32 /dev/urandom | base64 > "$E2E_DIR/secrets/master.key"
 head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$E2E_DIR/secrets/worker.token")
chown 10001:10001 "$E2E_DIR/secrets/master.key" "$E2E_DIR/secrets/worker.token"
work="$E2E_DIR/work"
git init -q -b main "$work"
cp scripts/e2e/fixture/*.yml "$work/"
git -C "$work" -c user.name=e2e -c user.email=e2e@example.test add -A
git -C "$work" -c user.name=e2e -c user.email=e2e@example.test commit -q -m fixture
git clone -q --bare "$work" "$E2E_DIR/git/fixture.git"
git -C "$E2E_DIR/git/fixture.git" update-server-info
chmod -R a+rX "$E2E_DIR/git"

step "start stack ($PROJ)"
$DC up -d --wait --wait-timeout 180 postgres gitsrv app >/dev/null 2>&1 || die "stack did not become healthy"
$DC up -d worker >/dev/null 2>&1 || die "worker did not start"

step "seed workspace, project, inventory, template"
psql_ <<'SQL' >/dev/null
INSERT INTO workspaces (slug, name) VALUES ('e2e', 'E2E');
INSERT INTO projects (workspace_id, name, slug, url, default_ref)
  SELECT id, 'fixture', 'fixture', 'http://gitsrv:8000/fixture.git', 'main' FROM workspaces WHERE slug = 'e2e';
INSERT INTO inventories (workspace_id, name, slug, mode)
  SELECT id, 'inv', 'inv', 'db' FROM workspaces WHERE slug = 'e2e';
INSERT INTO inventory_hosts (inventory_id, name, vars)
  SELECT id, 'localhost', '{"ansible_connection":"local","ansible_python_interpreter":"{{ ansible_playbook_python }}"}'::jsonb
  FROM inventories WHERE slug = 'inv';
INSERT INTO templates (workspace_id, project_id, inventory_id, name, slug, playbook)
  SELECT w.id, p.id, i.id, 'site', 'site', 'site.yml'
  FROM workspaces w, projects p, inventories i WHERE w.slug = 'e2e' AND p.slug = 'fixture' AND i.slug = 'inv';
INSERT INTO templates (workspace_id, project_id, inventory_id, name, slug, playbook)
  SELECT w.id, p.id, i.id, 'slow', 'slow', 'slow.yml'
  FROM workspaces w, projects p, inventories i WHERE w.slug = 'e2e' AND p.slug = 'fixture' AND i.slug = 'inv';
SQL
$DC exec -T app acp admin project sync --workspace e2e --project fixture 2>&1 | cut -c1-200 | tail -n 5

enqueue() { # template slug -> run id
  psql_ -c "
INSERT INTO runs (workspace_id, template_id, template_name, project_id, project_revision, trigger, params)
SELECT t.workspace_id, t.id, t.name, t.project_id, r.sha, 'api',
  jsonb_build_object('playbook', t.playbook, 'project_id', t.project_id, 'project_revision', r.sha,
    'inventory_id', t.inventory_id, 'limit', '', 'hosts', jsonb_build_array('localhost'), 'tags', '', 'skip_tags', '',
    'extra_vars', '{}'::jsonb, 'env_vars', '{\"E2E_MARKER\":\"yes\"}'::jsonb, 'check', false, 'diff', false,
    'verbosity', 0, 'ssh_secret_id', null, 'become_secret_id', null, 'vault_identity_ids', '[]'::jsonb)
FROM templates t JOIN LATERAL (SELECT sha FROM project_revisions WHERE project_id = t.project_id ORDER BY synced_at DESC LIMIT 1) r ON true
WHERE t.slug = '$1' RETURNING id;" | head -n 1
}

wait_status() { # run id, regex of wanted statuses, timeout seconds
  i=0
  while [ "$i" -lt "$3" ]; do
    s=$(psql_ -c "SELECT status FROM runs WHERE id = '$1'")
    if echo "$s" | grep -Eq "$2"; then echo "$s"; return 0; fi
    i=$((i + 1)); sleep 1
  done
  echo "$s"; return 1
}

step "run 1: successful playbook"
run1=$(enqueue site)
[ -n "$run1" ] || die "could not enqueue (project not synced?)"
st=$(wait_status "$run1" '^(succeeded|failed|cancelled|lost)$' 180) || die "run 1 did not finish (status: $st)"
[ "$st" = succeeded ] || die "run 1 status $st"
row=$(psql_ -c "SELECT exit_code, hosts_ok, hosts_changed, bundle_served_at IS NOT NULL FROM runs WHERE id = '$run1'")
[ "$row" = "0|1|1|t" ] || die "run 1 summary unexpected: $row"
log=$(psql_ -c "SELECT string_agg(data, '' ORDER BY seq) FROM run_log_chunks WHERE run_id = '$run1'")
echo "$log" | grep -q "PLAY RECAP" || die "log lacks PLAY RECAP"
echo "$log" | grep -q "e2e run ok on localhost" || die "log lacks debug output"
ct=$(psql_ -c "SELECT changed_tasks FROM run_host_results WHERE run_id = '$run1' AND host = 'localhost'")
echo "$ct" | grep -q "Change something" || die "changed task not recorded: $ct"
left=$($DC exec -T worker sh -c 'ls /tmp/runs 2>/dev/null | wc -l' | tr -d '\r ')
[ "$left" = 0 ] || die "run directory not removed ($left entries)"
echo "run 1 ok: $row, changed_tasks=$ct"

step "run 2: cancel a running playbook"
run2=$(enqueue slow)
wait_status "$run2" '^running$' 60 >/dev/null || die "run 2 did not start"
i=0
until psql_ -c "SELECT string_agg(data, '') FROM run_log_chunks WHERE run_id = '$run2'" | grep -q "e2e slow started"; do
  i=$((i + 1)); [ "$i" -lt 60 ] || die "run 2 produced no log"; sleep 1
done
psql_ -c "UPDATE runs SET cancel_requested_at = now() WHERE id = '$run2'" >/dev/null
st=$(wait_status "$run2" '^(succeeded|failed|cancelled|lost)$' 60) || die "run 2 did not stop (status: $st)"
[ "$st" = cancelled ] || die "run 2 status $st"
echo "run 2 ok: $st"

step "result"
echo "PASS: e2e run execution (success + cancel)"
