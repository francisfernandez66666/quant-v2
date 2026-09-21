# -*- coding: utf-8 -*-
"""scripts/dataload_keepalive.py 单测（§DATA-OUTAGE 盘后保活）：

覆盖：target 最近交易日推导（周末回退、§M6-② 交易日历/节假日回退）、latest 新鲜度读取
（缺表/空库=空串安全）、one_round 全新鲜时零动作短路、缺日线时按序补数并在拉齐后判成、
§M6-① 指数表头字段名契约（与 cmd/pydata/server.py _INDEX_FIELDS 同源比对）、
§M6-① 源字段缺失/不可解析时显式弃权 + 非零退出（绝不静默写 0 报成功）。
无需网络/真实 trading.db（sqlite 临时库 + monkeypatch 外部调用）。
"""
import datetime
import importlib.util
import os
import re
import sqlite3
import tempfile
import unittest
from unittest import mock

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
        # §M6：index_daily 用生产全列骨架（fetch_index 的 INSERT 列集），
        # 以便断言 change/pct_chg 真落源值而非恒 0。
        c.execute("CREATE TABLE index_daily (ts_code TEXT PRIMARY KEY, trade_date TEXT, "
                  "open REAL, high REAL, low REAL, close REAL, pre_close REAL, "
                  "change REAL, pct_chg REAL, vol REAL, amount REAL)")
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


# last_trade_date 单测：无日历两源可用时回退工作日近似（周末回退周五）。
class TestLastTradeDate(TempDbCase):
    def test_format_and_no_weekend(self):
        # target 恒为工作日（周末自动回退到周五），格式 yyyyMMdd。
        # §M6-②：本临时库无 trade_cal、并 patch urlopen 抛错令 pydata 也不可达 →
        # 走最后防线工作日近似路径，锁"日历全不可用时行为不回退到旧语义之外"。
        with mock.patch("urllib.request.urlopen", side_effect=OSError("no sidecar")):
            got = k.last_trade_date()
        d = datetime.datetime.strptime(got, "%Y%m%d").date()
        self.assertLess(d.weekday(), 5)
        self.assertRegex(got, r"^\d{8}$")


# §M6-① 表头字段名契约：keepalive 消费键集必须逐字命中 cmd/pydata/server.py 的
# _INDEX_FIELDS 同源定义（DictReader 大小写敏感——旧读 "pctchg" vs 契约 "pctChg"
# 即恒 0 事故本体）。server.py 顶部 import baostock（测试机可能未装）→ 源码正则提取，不 import。
class TestIndexHeaderContract(TempDbCase):
    def _server_index_fields(self):
        src = open(os.path.join(_ROOT, "cmd", "pydata", "server.py"), encoding="utf-8").read()
        m = re.search(r"_INDEX_FIELDS\s*=\s*\(([^)]*)\)", src)
        self.assertIsNotNone(m, "cmd/pydata/server.py _INDEX_FIELDS 定义漂移（契约源头改名？）")
        return [x.strip().strip('",') for x in m.group(1).split(",") if x.strip().strip('",')]

    def test_keepalive_keys_match_server_header(self):
        fields = self._server_index_fields()
        # 契约源头本身必须含驼峰 pctChg 且不含全小写 pctchg（旧 bug 的键名）。
        self.assertIn("pctChg", fields)
        self.assertNotIn("pctchg", [f.lower() for f in fields if f != "pctChg"])
        for key in k.INDEX_REQUIRED_FIELDS:
            self.assertIn(key, fields, "keepalive 消费字段 %s 不在 server 表头契约中" % key)
        self.assertEqual(k.INDEX_PCT_COL, "pctChg")

    def test_full_zero_abstention_never_writes(self):
        """反例锁（旧缺陷复现）：表头用旧全小写 pctchg（server 真实表头是 pctChg，
        此处模拟契约漂移/换源），fetch_index 必须整源弃权——0 行落库、rejected>0、
        one_round 判不成、main 非零退出；绝不出现"change/pct_chg 写 0 且报成功"。"""
        bad_csv = ("date,code,open,high,low,close,preclose,volume,amount,pctchg\n"
                   "2026-09-30,sh.000300,3.5,3.6,3.4,3.45,3.5,100,200,0.5\n")
        with mock.patch("urllib.request.urlopen") as u:
            u.return_value.read.return_value = bad_csv.encode("utf-8")
            written, rejected = k.fetch_index("2026-09-01", "2026-09-30")
        self.assertEqual(written, 0)
        self.assertGreaterEqual(rejected, len(k.IDX))
        c = sqlite3.connect(self.db_path)
        self.assertEqual(c.execute("SELECT COUNT(*) FROM index_daily").fetchone()[0], 0)
        c.close()
        # rc 断言：弃权轮次令 one_round=False；8 轮耗尽 → main() 返回 1（非零退出）。
        def fake_latest(table, col="trade_date"):
            return "" if table == "index_daily" else "20260930"

        with mock.patch.object(k, "latest", fake_latest), \
             mock.patch.object(k, "run", lambda cmd, **kw: (0, "mocked")), \
             mock.patch.object(k, "fetch_index", lambda s, e: (0, len(k.IDX))), \
             mock.patch.object(k, "last_trade_date", lambda: "20260930"), \
             mock.patch.object(k.time, "sleep", lambda s: None):
            self.assertFalse(k.one_round("20260930"))
            self.assertEqual(k.main(), 1, "源字段契约破坏必须非零退出（§M6 绝不静默报成功）")

    def test_good_header_writes_real_pctchg(self):
        """正例锁：表头/字段全对时 change/pct_chg 落源值 pctChg（非 0）、vol=volume/100、
        trade_date 归一 8 位——旧代码在此形态下写恒 0（大小写错键）。"""
        good_csv = ("date,code,open,high,low,close,preclose,volume,amount,adjustflag,"
                    "turn,tradestatus,pctChg\n"
                    "2026-09-30,sh.000300,3.5,3.6,3.4,3.45,3.5,100000,200000,3,0.8,1,-1.42\n")
        with mock.patch("urllib.request.urlopen") as u:
            u.return_value.read.return_value = good_csv.encode("utf-8")
            written, rejected = k.fetch_index("2026-09-01", "2026-09-30")
        self.assertEqual(rejected, 0)
        self.assertEqual(written, len(k.IDX))  # 三个指数共用同一 mock 响应
        c = sqlite3.connect(self.db_path)
        rows = c.execute("SELECT trade_date,close,pre_close,change,pct_chg,vol FROM index_daily").fetchall()
        c.close()
        self.assertEqual(len(rows), len(k.IDX))
        for td, close, pre, chg, pct, vol in rows:
            self.assertEqual(td, "20260930")
            self.assertAlmostEqual(pct, -1.42)   # §M6 核心断言：pct_chg 恒 0 的复现点
            self.assertAlmostEqual(chg, -1.42)  # 与 Go bsLoadIndex 口径一致（change=pctChg）
            self.assertAlmostEqual(vol, 1000.0)


# §M6-② 交易日历回退：逢法定节假日 target 必须回退到节前交易日且当日 rc=0，
# 旧"最近工作日近似"在节假日必 rc=1 的反例即此锁的复现点。
class TestTradeCalendarFallback(TempDbCase):
    def _seed_cal(self):
        c = sqlite3.connect(self.db_path)
        c.execute("CREATE TABLE trade_cal (cal_date TEXT PRIMARY KEY, is_open INTEGER)")
        for d, open_ in [("20260928", 1), ("20260929", 1), ("20260930", 1),
                         ("20261001", 0), ("20261002", 0), ("20261005", 0),  # 10-05 周一，国庆休市
                         ("20261006", 0), ("20261007", 0), ("20261008", 0), ("20261009", 1)]:
            c.execute("INSERT INTO trade_cal VALUES (?,?)", (d, open_))
        c.commit()
        c.close()

    def test_holiday_target_from_trade_cal(self):
        self._seed_cal()
        got = k.last_trade_date(today=datetime.date(2026, 10, 5))  # 节假日（周一）
        self.assertEqual(got, "20260930")                          # → 节前最后交易日

    def test_holiday_rc0_when_data_fresh_to_prev_open_day(self):
        """节假日 rc 断言：数据齐到节前交易日 → main() 返回 0（旧实现节假日当天
        按"最近工作日"= 假日本身，8 轮拉齐落空必 rc=1）。"""
        self._seed_cal()
        for t in ("daily", "index_daily", "ths_limit_up_daily"):
            self.insert(t, "20260930")
        calls = []
        with mock.patch.object(k, "last_trade_date", lambda: "20260930"), \
             mock.patch.object(k, "run", lambda cmd, **kw: calls.append(cmd) or (0, "ok")), \
             mock.patch.object(k.time, "sleep", lambda s: None):
            self.assertEqual(k.main(), 0)
        self.assertEqual(calls, [])  # 判定为齐 → 零外部调用

    def test_stale_calendar_falls_back_to_pydata(self):
        """trade_cal 过旧（开市日落后今日 12 天以上）→ 降级 pydata /trade_days。"""
        c = sqlite3.connect(self.db_path)
        c.execute("CREATE TABLE trade_cal (cal_date TEXT PRIMARY KEY, is_open INTEGER)")
        c.execute("INSERT INTO trade_cal VALUES ('20260801', 1)")  # 远超 12 天
        c.commit(); c.close()
        cal_csv = ("calendar_date,is_open\n"
                   "2026-09-30,1\n2026-10-01,0\n2026-10-05,0\n")
        with mock.patch("urllib.request.urlopen") as u:
            u.return_value.read.return_value = cal_csv.encode("utf-8")
            got = k.last_trade_date(today=datetime.date(2026, 10, 5))
        self.assertEqual(got, "20260930")

    def test_no_calendar_at_all_keeps_weekday_approx_with_trace(self):
        """两源皆不可用（无 trade_cal 表 + sidecar 拒绝连接）→ 工作日近似 + 日志留痕。"""
        with mock.patch("urllib.request.urlopen", side_effect=OSError("refused")):
            got = k.last_trade_date(today=datetime.date(2026, 10, 5))  # 周一：近似=当日
        self.assertEqual(got, "20261005")
        with open(k.LOG, encoding="utf-8") as f:
            self.assertIn("回退工作日近似", f.read())  # 显式留痕，取证可查


# latest 查询单测：空表/缺表安全弃权，有行取最大交易日。
class TestLatestFreshness(TempDbCase):
    def test_max_and_missing_table(self):
        # 空表 → 空串；有行 → 最大 trade_date；缺表 → 空串（查询异常安全弃权）。
        self.assertEqual(k.latest("daily"), "")
        self.insert("daily", "20260910")
        self.insert("daily", "20260912")
        self.assertEqual(k.latest("daily"), "20260912")
        self.assertEqual(k.latest("no_such_table"), "")


# 单轮检查短路：三表均已到目标日则零外部调用直接判真。
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
        # §M6：fetch_index 返回 (written, rejected) 二元组，桩同步改形态
        k.fetch_index = lambda s, e: self.insert("index_daily", target) or (0, 0)
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
