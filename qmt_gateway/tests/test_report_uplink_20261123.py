# -*- coding: utf-8 -*-
"""2026-11-23 盒子修复批的上行腿用例（§UPLINK-SINGLE-WALK / §UPLINK-CADENCE / §UPLINK-NOISE）。

三条互相独立的现场缺陷，全部在 2026-11-23 盒子日志/XML 里取证：
  * §UPLINK-SINGLE-WALK：一轮对四类读数各走一遍 15.5 万文件的树（单轮 494 秒）。
    现在一轮只走一次，且先按**文件名日期**剪枝（不 stat 就跳过旧文件）。
  * §UPLINK-CADENCE：计划任务 `IgnoreNew` 把重叠轮次静默拒绝（0x800710E0，日志无痕）。
    现在进程锁把它变成一行 `UPLINK_RESULT=SKIP busy=1`。
  * §UPLINK-NOISE：diagnostic 的 skip 条目带 `at` ⇒ 内容每轮都变 ⇒ 每分钟一条；
    申购行（754163 圣晖申购）每轮一条 bad_code_row。
"""
import io
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import broker_reads as br  # noqa: E402
import report_uplink as up  # noqa: E402

FIXED_NOW = 1793772000.0  # 与 test_report_uplink 同一枚钉死的钟（北京 2026-11-04 13:50）
TODAY = "20261104"


def _write(path, body):
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(body)


class _ExportTree:
    """临时导出根：既能造带日期文件，也能造无日期文件（fail-open 那一支）。"""

    def __init__(self, tmp):
        self.tmp = tmp
        self.root = os.path.join(tmp, "exports")
        os.makedirs(self.root, exist_ok=True)

    def put(self, name, body="代码,名称,数量,可用数量\n600519,贵州茅台,100,100\n"):
        path = os.path.join(self.root, name)
        _write(path, body)
        return path


class TestRecentDiscovery(unittest.TestCase):
    """§UPLINK-SINGLE-WALK 的发现层：文件名日期剪枝必须发生在任何 stat 之前。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.tree = _ExportTree(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_stale_filename_is_pruned_before_stat(self):
        stale = self.tree.put("Order-20261020_1.csv")
        kept = self.tree.put("Order-20261103_1.csv")
        today = self.tree.put("Order-20261104_1.csv")
        statted = []
        real_stat = os.stat

        def spy(path, *a, **kw):
            statted.append(os.path.basename(str(path)))
            return real_stat(path, *a, **kw)

        with mock.patch.object(br.os, "stat", side_effect=spy):
            hits = br.discover_exports_recent([self.tree.root], [self.tree.root],
                                              today=TODAY, max_age_days=2)
        names = [os.path.basename(h.path) for h in hits]
        self.assertIn(os.path.basename(kept), names)
        self.assertIn(os.path.basename(today), names)
        self.assertNotIn(os.path.basename(stale), names)
        self.assertNotIn(os.path.basename(stale), statted,
                         "旧文件名必须在 stat 之前被剪掉（否则 15.5 万次 stat 照旧）")

    def test_undated_file_is_kept_fail_open(self):
        undated = self.tree.put("持仓.csv")
        hits = br.discover_exports_recent([self.tree.root], [self.tree.root],
                                          today=TODAY, max_age_days=2)
        self.assertIn(os.path.basename(undated), [os.path.basename(h.path) for h in hits],
                      "解析不出日期 ⇒ 证明不了它旧 ⇒ 必须保留（fail-open）")

    def test_cross_midnight_still_sees_yesterday(self):
        yesterday = self.tree.put("Account-20261103_1.csv")
        hits = br.discover_exports_recent([self.tree.root], [self.tree.root],
                                          today="20261104", max_age_days=2)
        self.assertIn(os.path.basename(yesterday), [os.path.basename(h.path) for h in hits],
                      "00:05 那一轮仍要能取到昨天的收盘导出")

    def test_max_age_days_one_keeps_only_today(self):
        yesterday = self.tree.put("Order-20261103_1.csv")
        today = self.tree.put("Order-20261104_1.csv")
        hits = br.discover_exports_recent([self.tree.root], [self.tree.root],
                                          today=TODAY, max_age_days=1)
        names = [os.path.basename(h.path) for h in hits]
        self.assertIn(os.path.basename(today), names)
        self.assertNotIn(os.path.basename(yesterday), names)


class _Fixture:
    """最小上行夹具：配置 + 导出根 + 假 opener。"""

    def __init__(self, tmp, files=None):
        self.tmp = tmp
        self.root = os.path.join(tmp, "exports")
        os.makedirs(self.root, exist_ok=True)
        for name, body in (files or {}).items():
            _write(os.path.join(self.root, name), body)
        self.config_path = os.path.join(tmp, "config.xt.json")
        with open(self.config_path, "w", encoding="utf-8") as fh:
            json.dump({"token": "T0KEN", "user_id": "u_leg01",
                       "report_url": "http://127.0.0.1:1",
                       "uplink_export_roots": [self.root],
                       "uplink_state_dir": tmp}, fh)

    def uplink(self, opener):
        return up.ReportUplink(self.config_path, now_fn=lambda: FIXED_NOW, opener=opener)


class _Opener:
    def __init__(self, status=200):
        self.status = status
        self.calls = []

    def __call__(self, url, token, body):
        self.calls.append(json.loads(body.decode("utf-8")))
        return self.status


class TestSingleWalk(unittest.TestCase):
    """一轮只遍历一次：四类读数共享同一份扫描结果。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name, {
            "持仓.csv": "代码,名称,数量,可用数量\n600519,贵州茅台,100,100\n",
            "委托.csv": "代码,委托号,委托状态,买卖标志,价格,数量,时间,投资备注\n"
                        "600519,7701,已报,买入,10.0,100,2026-11-04 09:31:00,v24-FC600519\n",
            "成交.csv": "代码,委托号,买卖标志,成交均价,成交数量,成交金额,成交时间,成交编号\n"
                        "600519,7701,买入,10.05,100,1005.0,2026-11-04 09:31:05,T9001\n",
            "资产.csv": "总资产,可用资金,证券市值\n100000.0,40000.0,60000.0\n",
        })

    def tearDown(self):
        self._tmp.cleanup()

    def test_one_scan_per_round(self):
        calls = []
        real = up.broker_reads.discover_exports_recent

        def counting(*a, **kw):
            calls.append(1)
            return real(*a, **kw)

        u = self.fx.uplink(_Opener())
        with mock.patch.object(up.broker_reads, "discover_exports_recent",
                               side_effect=counting):
            u.build_events()
        self.assertEqual(len(calls), 1,
                         "四类读数共享一次扫描；每类重扫就是 494 秒的真因")

    def test_scan_is_reset_between_rounds(self):
        calls = []
        real = up.broker_reads.discover_exports_recent

        def counting(*a, **kw):
            calls.append(1)
            return real(*a, **kw)

        u = self.fx.uplink(_Opener())
        with mock.patch.object(up.broker_reads, "discover_exports_recent",
                               side_effect=counting):
            u.build_events()
            u.build_events()
        self.assertEqual(len(calls), 2, "轮级缓存必须每轮重置（否则新导出永远看不到）")

    def test_kinds_resolve_from_single_scan(self):
        u = self.fx.uplink(_Opener())
        events = u.build_events()
        kinds = {e.get("type") for e in events}
        self.assertTrue({"positions", "account", "order", "trade"} <= kinds, kinds)


class TestRunLock(unittest.TestCase):
    """§UPLINK-CADENCE：被计划任务静默拒绝的那一轮，必须留下一行读数。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_live_pid_blocks_the_round(self):
        lock = os.path.join(self._tmp.name, up.UPLINK_LOCK_NAME)
        _write(lock, "%d|%d" % (os.getpid(), int(FIXED_NOW)))
        opener = _Opener()
        buf = io.StringIO()
        with mock.patch.object(up, "_urllib_opener", opener), redirect_stdout(buf):
            rc = up.main(["--config", self.fx.config_path, "--once"])
        out = buf.getvalue()
        self.assertEqual(rc, 0, "被锁挡住不是错误（计划任务的重叠拒绝也是 exit 0）")
        self.assertIn("UPLINK_RESULT=SKIP busy=1", out)
        self.assertEqual(opener.calls, [], "拿不到锁绝不许发任何事件")

    def test_stale_lock_is_taken_over(self):
        lock = os.path.join(self._tmp.name, up.UPLINK_LOCK_NAME)
        _write(lock, "999999|%d" % int(FIXED_NOW - 600))
        acquired, path, info = up.acquire_run_lock(self._tmp.name, now_fn=lambda: FIXED_NOW)
        try:
            self.assertTrue(acquired, "持有者 pid 已死 ⇒ 必须接管（否则一次崩溃永久停摆）")
            with open(path, "r", encoding="utf-8") as fh:
                self.assertEqual(int(fh.read().split("|")[0]), os.getpid())
        finally:
            up.release_run_lock(path)

    def test_release_removes_the_lock(self):
        acquired, path, _ = up.acquire_run_lock(self._tmp.name, now_fn=lambda: FIXED_NOW)
        self.assertTrue(acquired)
        self.assertTrue(os.path.exists(path))
        up.release_run_lock(path)
        self.assertFalse(os.path.exists(path))

    def test_second_acquire_while_held_is_busy(self):
        acquired, path, _ = up.acquire_run_lock(self._tmp.name, now_fn=lambda: FIXED_NOW)
        try:
            self.assertTrue(acquired)
            again, _, info = up.acquire_run_lock(self._tmp.name, now_fn=lambda: FIXED_NOW)
            self.assertFalse(again)
            self.assertEqual(info.get("busy"), 1)
        finally:
            up.release_run_lock(path)


class TestDiagnosticNoise(unittest.TestCase):
    """§UPLINK-NOISE：状态不变 ⇒ diagnostic 只发一次；状态变了才重发。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmp.name)  # 空导出根 ⇒ 四类全部 file_not_found

    def tearDown(self):
        self._tmp.cleanup()

    def _diags(self, opener):
        return [e for e in opener.calls if e.get("type") == "diagnostic"]

    def test_skip_entries_carry_count_and_no_at(self):
        opener = _Opener()
        u = self.fx.uplink(opener)
        u.run_once()
        diags = self._diags(opener)
        self.assertEqual(len(diags), 1)
        for s in diags[0]["skips"]:
            self.assertIn("count", s)
            self.assertNotIn("at", s)

    def test_unchanged_state_sends_diagnostic_once(self):
        opener = _Opener()
        u = self.fx.uplink(opener)
        u.run_once()
        self.assertEqual(len(self._diags(opener)), 1)
        u.run_once()
        self.assertEqual(len(self._diags(opener)), 1,
                         "同一状态第二轮又发 diagnostic ⇒ 哈希里混进了时间戳/count")

    def test_changed_skip_set_is_resent(self):
        opener = _Opener()
        u = self.fx.uplink(opener)
        u.run_once()
        u._note_skip("custom", "new_reason")
        u.run_once()
        self.assertEqual(len(self._diags(opener)), 2,
                         "类别集合变了必须重发（否则新故障永远到不了云端）")

    def test_hash_ignores_count(self):
        a = {"type": "diagnostic", "user_id": "u", "skips": [
            {"kind": "positions", "reason": "file_not_found", "count": 1}]}
        b = {"type": "diagnostic", "user_id": "u", "skips": [
            {"kind": "positions", "reason": "file_not_found", "count": 97}]}
        self.assertEqual(up._event_hash(a), up._event_hash(b))
        c = {"type": "diagnostic", "user_id": "u", "skips": [
            {"kind": "positions", "reason": "parse_fail", "count": 1}]}
        self.assertNotEqual(up._event_hash(a), up._event_hash(c))


class TestSubscriptionRows(unittest.TestCase):
    """申购行不是股票代码：分类必须排在 normalize_code 之前（否则每轮一条噪音）。"""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()

    def tearDown(self):
        self._tmp.cleanup()

    def test_subscription_row_is_classified_not_bad_code(self):
        fx = _Fixture(self._tmp.name, {
            "委托.csv": "代码,委托号,委托状态,买卖标志,价格,数量,时间,投资备注\n"
                        "754163,8801,已报,申购,100.0,1000,2026-11-04 09:31:00,\n",
        })
        opener = _Opener()
        u = fx.uplink(opener)
        u.run_once()
        reasons = [s["reason"] for s in u.skips if s["kind"] == "orders"]
        self.assertIn("subscription_row:754163", reasons)
        self.assertFalse([r for r in reasons if r.startswith("bad_code_row")], reasons)
        # 申购行本来就不该产生委托事件（不是可交易代码）
        self.assertFalse([e for e in opener.calls if e.get("type") == "order"])


if __name__ == "__main__":
    unittest.main()
