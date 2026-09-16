#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""scripts/dataload_keepalive.py 单测（§DATA-OUTAGE 盘后保活）：

覆盖：target 最近交易日推导（周末回退）、latest 新鲜度读取（缺表/空库=空串安全）、
one_round 全新鲜时零动作短路、缺日线时按序补数并在拉齐后判成。
无需网络/真实 trading.db（sqlite 临时库 + monkeypatch 外部调用）。
"""
import datetime
import importlib.util
import os
import sqlite3
import tempfile
import unittest

_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
_SPEC = importlib.util.spec_from_file_location(
    "dataload_keepalive", os.path.join(_ROOT, "scripts", "dataload_keepalive.py"))
k = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(k)


class TempDbCase(unittest.TestCase):
    """公共夹具：临时 sqlite 研究库骨架 + 日志重定向，用例结束清理。"""

    def setUp(self):
        fd, self.db_path = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        self._orig_db, self._orig_log = k.DB, k.LOG
        k.DB = self.db_path
        k.LOG = self.db_path + ".log"
        c = sqlite3.connect(self.db_path)
        c.execute("CREATE TABLE daily (ts_code TEXT, trade_date TEXT)")
        c.execute("CREATE TABLE index_daily (ts_code TEXT, trade_date TEXT)")
        c.execute("CREATE TABLE ths_limit_up_daily (trade_date TEXT)")
        c.execute("CREATE TABLE ths_daily (trade_date TEXT)")
        c.execute("INSERT INTO ths_daily (trade_date) VALUES ('20260999')")  # 主源视为常新，聚焦三表判定
        c.commit()
        c.close()

    def tearDown(self):
        k.DB, k.LOG = self._orig_db, self._orig_log
        os.unlink(self.db_path)
        if os.path.exists(k.LOG):
            os.unlink(self._log_path())

    def _log_path(self):
        return self.db_path + ".log"

    def insert(self, table, date):
        c = sqlite3.connect(self.db_path)
        c.execute("INSERT INTO %s (trade_date) VALUES (?)" % table, (date,))
        c.commit()
        c.close()


class TestLastTradeDate(TempDbCase):
    def test_format_and_no_weekend(self):
        # target 恒为工作日（周末自动回退到周五），格式 yyyyMMdd。
        d = datetime.datetime.strptime(k.last_trade_date(), "%Y%m%d").date()
        self.assertLess(d.weekday(), 5)


class TestLatestFreshness(TempDbCase):
    def test_max_and_missing_table(self):
        # 空表 → 空串；有行 → 最大 trade_date；缺表 → 空串（查询异常安全弃权）。
        self.assertEqual(k.latest("daily"), "")
        self.insert("daily", "20260910")
        self.insert("daily", "20260912")
        self.assertEqual(k.latest("daily"), "20260912")
        self.assertEqual(k.latest("no_such_table"), "")


class TestOneRoundShortCircuit(TempDbCase):
    def test_all_fresh_no_calls(self):
        # 三表都已到 target：不发任何外部调用，直接判 True。
        target = "20260911"
        for t in ("daily", "index_daily", "ths_limit_up_daily"):
            self.insert(t, target)
        calls = []
        k.run = lambda *a, **kw: calls.append(a) or (0, "ok")
        k.fetch_index = lambda *a: calls.append(("idx",)) or 0
        self.assertTrue(k.one_round(target))
        self.assertEqual(calls, [])

    def test_stale_daily_triggers_load_and_completes(self):
        # 日线落后：one_round 触发 dataload daily（模拟补到位）；池/ths 增量也各一次；
        # 二轮全新鲜即短路判成（一锤子拉齐即退的行为基础）。
        target = "20260914"
        cmds = []

        def fake_run(cmd, *a, **kw):
            cmds.append(cmd)
            if "daily" in cmd[-1]:
                self.insert("daily", target)
            return (0, "mocked")

        k.run = fake_run
        k.fetch_index = lambda s, e: self.insert("index_daily", target) or 0
        self.insert("ths_limit_up_daily", "20260913")
        first = k.one_round(target)
        self.assertFalse(first)  # daily-k-10d 尝试轮后池未到 target 前不判成
        self.assertTrue(any(c[-1] == "daily" for c in [x for x in cmds]))
        self.assertTrue(any("--kind" in c and "pools" in c for c in cmds))
        # 池补到 target 后二轮短路判成。
        self.insert("ths_limit_up_daily", target)
        cmds[:] = []
        self.assertTrue(k.one_round(target))
        self.assertEqual(cmds, [])


class TestLogResilience(unittest.TestCase):
    """§生产 2026-09-16：cmd 重定向独占同名文件句柄下 open(LOG,'a') 必报 Errno 13。

    旧 log() 未捕获 → 进程崩在第一行日志上、任务静默失败 5 日。修复后写文件失败
    须降级 stdout，绝不抛出。
    """

    def test_log_open_failure_not_fatal(self):
        orig = k.LOG
        d = tempfile.mkdtemp()
        k.LOG = d  # 目录路径：open(dir,'a') 必抛 IsADirectoryError，且不落任何字节
        try:
            k.log("resilience probe")  # 不得抛
        finally:
            k.LOG = orig
            os.rmdir(d)


if __name__ == "__main__":
    unittest.main()
