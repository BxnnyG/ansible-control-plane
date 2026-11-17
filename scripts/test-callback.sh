#!/bin/sh
# Unit tests of the acp Ansible callback plugin; runs inside the worker image
# (needs ansible). Build it first: docker compose build worker.
set -eu
cd "$(dirname "$0")/.."
IMAGE="${ACP_WORKER_IMAGE:-acp-worker:local}"
exec docker run --rm --entrypoint python3 -v "$PWD/worker/ansible:/a:ro" -e PYTHONDONTWRITEBYTECODE=1 "$IMAGE" /a/tests/test_acp_callback.py
