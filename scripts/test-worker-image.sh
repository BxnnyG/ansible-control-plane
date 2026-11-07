#!/bin/sh
# Builds the worker image and runs a localhost ping playbook, asserting the acp callback events.
set -eu
cd "$(dirname "$0")/.."
IMG=${1:-acp-worker:test}
docker build -q -f Dockerfile.worker -t "$IMG" . >/dev/null
docker run --rm --entrypoint sh -v "$PWD/testdata/worker-image:/pb:ro" "$IMG" -c '
set -eu
export ACP_EVENTS_FILE=/tmp/events.jsonl
ansible-playbook -i localhost, /pb/ping.yml >/tmp/out.txt 2>&1 || { tail -n 20 /tmp/out.txt; exit 1; }
python - <<PY
import json, os
ev = [json.loads(l) for l in open(os.environ["ACP_EVENTS_FILE"])]
kinds = [e["event"] for e in ev]
for k in ("playbook_start", "play_start", "task_start", "runner_ok", "runner_failed", "runner_skipped", "stats"):
    assert k in kinds, "missing " + k
assert any(e["event"] == "runner_failed" and e.get("ignored") for e in ev), "ignored flag"
assert any(e.get("no_log") for e in ev), "no_log flag"
assert "TOPSECRETVALUE" not in open(os.environ["ACP_EVENTS_FILE"]).read(), "leak"
assert ev[-1]["event"] == "stats" and ev[-1]["hosts"]["localhost"]["ok"] >= 2
print("events ok:", len(ev))
PY
# Bundle-style script inventory (internal/inventory Render shape) must parse without warnings.
cat >/tmp/inv.json <<JSON
{"all":{"hosts":[],"children":["g","ungrouped"],"vars":{}},"g":{"hosts":["h1"],"children":[],"vars":{}},"ungrouped":{"hosts":["lone"],"children":[],"vars":{}},"_meta":{"hostvars":{"h1":{"ansible_connection":"local"},"lone":{"ansible_connection":"local"}}}}
JSON
printf "#!/bin/sh\nexec cat /tmp/inv.json\n" >/tmp/inv.sh && chmod 700 /tmp/inv.sh
ansible-inventory -i /tmp/inv.sh --list >/tmp/inv.out 2>/tmp/inv.err || { tail -n 10 /tmp/inv.err; exit 1; }
! grep -qi "warning" /tmp/inv.err || { head -n 5 /tmp/inv.err; exit 1; }
ansible --version | head -n 1
pip show ansible | grep -i ^version
'
docker image inspect "$IMG" --format 'image size: {{.Size}} bytes'

# Job isolation prerequisites (ADR-0020): file capabilities only on the non-world-executable supervisor binary, job users exist.
docker run --rm --entrypoint sh "$IMG" -c '
set -eu
getcap /usr/libexec/acp/acp-worker | grep -q "cap_chown,cap_kill,cap_setgid,cap_setuid=ep"
[ -z "$(getcap /usr/local/bin/acp)" ]
[ "$(stat -c %a /usr/libexec/acp/acp-worker)" = 750 ]
id acpjob0 | grep -q "uid=10100" && id acpjob15 | grep -q "uid=10115"
echo "isolation prerequisites ok"
'
