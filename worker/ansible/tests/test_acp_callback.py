# SPDX-License-Identifier: AGPL-3.0-only
"""Unit tests for the probe-result gating of the acp callback plugin.

Run inside the worker image: scripts/test-callback.sh
"""
import json
import os
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "callback_plugins"))
import acp  # noqa: E402


class FakeStats:
    def __init__(self, custom):
        self.processed = {"src1": 1, "src2": 1}
        self.custom = custom

    def summarize(self, host):
        return {"ok": 1}


def run_stats(custom, probe):
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "ev.jsonl")
        env = {"ACP_EVENTS_FILE": path}
        if probe is not None:
            env["ACP_PROBE_RUN"] = probe
        with mock.patch.dict(os.environ, env, clear=False):
            if probe is None:
                os.environ.pop("ACP_PROBE_RUN", None)
            cb = acp.CallbackModule()
            cb.v2_playbook_on_stats(FakeStats(custom))
        with open(path, encoding="utf-8") as fh:
            return [json.loads(line) for line in fh if line.strip()]


class ProbeGatingTest(unittest.TestCase):
    CUSTOM = {
        "src1": {"acp_probe": {"t1": {"status": "ok", "latency_ms": 1, "detail": ""}}},
        "src2": {"other": 1},
        "_run": {"acp_probe": {"x": {}}},
    }

    def test_forwarded_for_probe_run(self):
        evs = run_stats(self.CUSTOM, "1")
        self.assertEqual([e["event"] for e in evs], ["stats", "probe_result"])
        self.assertEqual(evs[1]["host"], "src1")
        self.assertEqual(evs[1]["results"]["t1"]["status"], "ok")

    def test_not_forwarded_without_flag(self):
        for flag in (None, "", "0", "true"):
            evs = run_stats(self.CUSTOM, flag)
            self.assertEqual([e["event"] for e in evs], ["stats"], flag)

    def test_non_dict_payload_ignored(self):
        evs = run_stats({"src1": {"acp_probe": "oops"}}, "1")
        self.assertEqual([e["event"] for e in evs], ["stats"])


if __name__ == "__main__":
    unittest.main()
