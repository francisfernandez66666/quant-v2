# -*- coding: utf-8 -*-
"""§BOX-HEARTBEAT（2026-11-23）：上行心跳带"QMT 开没开、策略在不在说话"两枚只读读数。

现场（owner 2026-11-23）：「我的 57516 账户登录后前端不显示进程，那我怎么知道我开没开
qmt 并开启自动交易，有没有连云端。」盒上 QMT 跑在 SYSTEM/session 0，登录用户看不到窗口，
前端也没有客户端进程读数。修法不是让前端去拨盒子（15s 轮询 × 跨 WG 太重），而是让已有的
5 分钟心跳顺带两枚读数：

  * `client_proc`：XtItClient 三态 running/stopped/unknown（unknown ≠ stopped：
    "进程没了"要去拉起，"探测不了"要去查任务身份）；
  * `strategy_log_at` / `strategy_log_age_sec`：最近一份 XtClient_FormulaOutput 日志的
    mtime 与年龄。日志读不到就**整键缺席**（missing≠empty：不许拿心跳时间冒充日志时间）。

English: heartbeat extras for box liveness — process tri-state plus the newest strategy-log
mtime; a missing log omits the keys instead of faking them.
"""
import json
import os
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import report_uplink as up  # noqa: E402

FIXED_NOW = 1793772000.0  # 与 test_report_uplink 同一枚钉死的钟
LOG_AGE = 42


class _Proc:
    def __init__(self, text, rc=0):
        self.stdout = text.encode("utf-8")
        self.stderr = b""
        self.returncode = rc


def _runner(text):
    return lambda argv, **kw: _Proc(text)


def _make_qmt_tree(tmp):
    """造 QMT 安装树：<root>/python/<策略>.py + <root>/userdata/log/。"""
    root = os.path.join(tmp, "QMT")
    py_dir = os.path.join(root, "python")
    log_dir = os.path.join(root, "userdata", "log")
    os.makedirs(py_dir, exist_ok=True)
    os.makedirs(log_dir, exist_ok=True)
    strategy = os.path.join(py_dir, "新建策略文件.py")
    with open(strategy, "w", encoding="utf-8") as fh:
        fh.write('STRATEGY_VERSION = "v165-20261123"\n')
    return strategy, log_dir


class ClientProcessStateTest(unittest.TestCase):
    """三态判据：unknown 与 stopped 的处置方向不同，不许折叠。"""

    def test_non_windows_is_unknown(self):
        with mock.patch.object(up.sys, "platform", "darwin"):
            self.assertEqual(up.client_process_state(), "unknown")

    def test_running_when_tasklist_lists_it(self):
        with mock.patch.object(up.sys, "platform", "win32"):
            self.assertEqual(
                up.client_process_state("XtItClient.exe",
                                        _runner("XtItClient.exe  6392 Console  0  120,000 K")),
                "running")

    def test_stopped_when_tasklist_empty(self):
        with mock.patch.object(up.sys, "platform", "win32"):
            self.assertEqual(
                up.client_process_state("XtItClient.exe",
                                        _runner("INFO: No tasks are running which match the specified criteria.")),
                "stopped")

    def test_probe_failure_is_unknown_not_stopped(self):
        def boom(argv, **kw):
            raise OSError("tasklist missing")

        with mock.patch.object(up.sys, "platform", "win32"):
            self.assertEqual(up.client_process_state("XtItClient.exe", boom), "unknown")


class StrategyLogSnapshotTest(unittest.TestCase):
    """日志位置从已安装策略文件推，不把安装目录写死。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.strategy, self.log_dir = _make_qmt_tree(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def _put(self, name, mtime):
        path = os.path.join(self.log_dir, name)
        with open(path, "w", encoding="utf-8") as fh:
            fh.write("x")
        os.utime(path, (mtime, mtime))
        return path

    def test_newest_formula_output_wins(self):
        old = self._put("XtClient_FormulaOutput_20261122.log", FIXED_NOW - 9000)
        new = self._put("XtClient_FormulaOutput_20261123.log", FIXED_NOW - LOG_AGE)
        self._put("other.log", FIXED_NOW)
        snap = up.strategy_log_snapshot(self.strategy)
        self.assertIsNotNone(snap)
        self.assertEqual(snap[0], new)
        self.assertAlmostEqual(snap[1], FIXED_NOW - LOG_AGE)
        self.assertNotEqual(snap[0], old)

    def test_missing_dir_is_none(self):
        self.assertIsNone(up.strategy_log_snapshot(
            os.path.join(self._tmp.name, "QMT", "python", "nope.py")))

    def test_empty_strategy_file_is_none(self):
        self.assertIsNone(up.strategy_log_snapshot(""))


class HeartbeatFieldsTest(unittest.TestCase):
    """心跳装配：两枚读数各报各的，日志缺失不拖累进程读数。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        tmp = self._tmp.name
        self.strategy, log_dir = _make_qmt_tree(tmp)
        self.log_path = os.path.join(log_dir, "XtClient_FormulaOutput_20261123.log")
        with open(self.log_path, "w", encoding="utf-8") as fh:
            fh.write("x")
        os.utime(self.log_path, (FIXED_NOW - LOG_AGE, FIXED_NOW - LOG_AGE))
        exports = os.path.join(tmp, "exports")
        os.makedirs(exports, exist_ok=True)
        self.config_path = os.path.join(tmp, "config.xt.json")
        with open(self.config_path, "w", encoding="utf-8") as fh:
            json.dump({"token": "t", "user_id": "u_1",
                       "report_url": "http://127.0.0.1:1",
                       "uplink_export_roots": [exports],
                       "uplink_strategy_file": self.strategy,
                       "uplink_state_dir": tmp}, fh)

    def tearDown(self):
        self._tmp.cleanup()

    def _uplink(self):
        u = up.ReportUplink(self.config_path, now_fn=lambda: FIXED_NOW)
        u._proc_runner = _runner("XtItClient.exe  6392 Console  0  120,000 K")
        return u

    def test_heartbeat_carries_proc_and_log_age(self):
        u = self._uplink()
        with mock.patch.object(up.sys, "platform", "win32"):
            hb = u._build_heartbeat()
        self.assertEqual(hb["type"], "heartbeat")
        self.assertEqual(hb["client_proc"], "running")
        self.assertEqual(hb["strategy_log_age_sec"], LOG_AGE)
        self.assertTrue(hb["strategy_log_at"].startswith("2026-11-"),
                        "日志时间必须是 RFC3339，Go 侧按 data_ts 同族解析")
        self.assertEqual(hb["strategy_version"], "v165-20261123")

    def test_log_missing_omits_keys_not_fake(self):
        os.remove(self.log_path)
        u = self._uplink()
        with mock.patch.object(up.sys, "platform", "win32"):
            hb = u._build_heartbeat()
        self.assertNotIn("strategy_log_at", hb)
        self.assertNotIn("strategy_log_age_sec", hb)
        self.assertEqual(hb["client_proc"], "running",
                         "日志缺失与进程读数是两件事，不许一起沉默")

    def test_stopped_process_still_reports_log(self):
        u = self._uplink()
        u._proc_runner = _runner("INFO: No tasks are running which match the specified criteria.")
        with mock.patch.object(up.sys, "platform", "win32"):
            hb = u._build_heartbeat()
        self.assertEqual(hb["client_proc"], "stopped")
        self.assertEqual(hb["strategy_log_age_sec"], LOG_AGE)


if __name__ == "__main__":
    unittest.main()