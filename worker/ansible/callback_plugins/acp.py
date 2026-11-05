# SPDX-License-Identifier: AGPL-3.0-only
"""ACP JSON event callback: one JSON object per line written to $ACP_EVENTS_FILE.

Only status fields are emitted, never result payloads (ADR-0016). Tasks marked
no_log are reported with status only and without any task arguments.
"""
from __future__ import annotations

import json
import os
import time

from ansible.plugins.callback import CallbackBase

DOCUMENTATION = """
    name: acp
    type: notification
    short_description: Emit JSON events for ansible-control-plane
    description:
      - Writes one JSON object per line to the file named by ACP_EVENTS_FILE.
    requirements:
      - enable in ANSIBLE_CALLBACKS_ENABLED
"""


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "notification"
    CALLBACK_NAME = "acp"
    CALLBACK_NEEDS_ENABLED = True

    def __init__(self):
        super().__init__()
        self._path = os.environ.get("ACP_EVENTS_FILE", "")
        self._fh = None
        self._task_started = {}
        self._task = None

    def _emit(self, event, **fields):
        if not self._path:
            return
        if self._fh is None:
            self._fh = open(self._path, "a", encoding="utf-8", buffering=1)
        rec = {"event": event, "ts": round(time.time(), 3)}
        rec.update(fields)
        self._fh.write(json.dumps(rec, separators=(",", ":"), default=str) + "\n")
        self._fh.flush()

    def v2_playbook_on_start(self, playbook):
        self._emit("playbook_start", playbook=os.path.basename(playbook._file_name))

    def v2_playbook_on_play_start(self, play):
        self._emit("play_start", play=play.get_name().strip())

    def v2_playbook_on_task_start(self, task, is_conditional):
        self._task = task
        self._task_started[task._uuid] = time.time()
        self._emit("task_start", task=task.get_name().strip(), action=task.action)

    def v2_playbook_on_handler_task_start(self, task):
        self.v2_playbook_on_task_start(task, False)

    def _runner(self, status, result, **extra):
        task = result._task
        started = self._task_started.get(task._uuid)
        duration = round(time.time() - started, 3) if started else None
        self._emit(
            "runner_" + status,
            host=result._host.get_name(),
            task=task.get_name().strip(),
            action=task.action,
            duration=duration,
            no_log=bool(task.no_log),
            **extra,
        )

    def v2_runner_on_ok(self, result):
        self._runner("changed" if result._result.get("changed") else "ok", result)

    def v2_runner_on_failed(self, result, ignore_errors=False):
        self._runner("failed", result, ignored=bool(ignore_errors))

    def v2_runner_on_unreachable(self, result):
        self._runner("unreachable", result)

    def v2_runner_on_skipped(self, result):
        self._runner("skipped", result)

    def v2_playbook_on_stats(self, stats):
        hosts = {}
        for h in sorted(stats.processed.keys()):
            s = stats.summarize(h)
            hosts[h] = {k: int(s.get(k, 0)) for k in
                        ("ok", "changed", "failures", "unreachable", "skipped", "rescued", "ignored")}
        self._emit("stats", hosts=hosts)
        if self._fh:
            self._fh.close()
            self._fh = None
