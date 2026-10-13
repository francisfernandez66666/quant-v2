# -*- coding: utf-8 -*-
"""scripts/dataload_keepalive.py 单测（§DATA-OUTAGE 盘后保活）：

覆盖：target 最近交易日推导（周末回退、§M6-② 交易日历/节假日回退）、latest 新鲜度读取
（缺表/空库=空串安全）、one_round 全新鲜时零动作短路、缺日线时按序补数并在拉齐后判成、
§M6-① 指数表头字段名契约（与 cmd/pydata/server.py _INDEX_FIELDS 同源比对）、
§M6-① 源字段缺失/不可解析时显式弃权 + 非零退出（绝不静默写 0 报成功）。
§POOL-GAP（2026-10-12）盘口三池漏更日逐日回补：缺数日点名拨、上游给不出的日留在 still_missing、
"读不到行数"不算缺数、无日历一步不拨、单轮封顶 5 天。
§TARGET-NOT-CLOSED（2026-10-12）收盘前拨测归位上一开市日（含收盘后/非今日/日历不可得三反向腿）。
★ 该波的注入时钟 `now` 必须同时喂给 target 推导（last_trade_date）与归位判定
（target_day_reachable）两处：只喂归位一处时，夜里跨过零点复跑本文件会现形——注入的
"今天"是 10-12，而 last_trade_date() 按本机墙上钟取出 10-13（§CAL-COVER 已把日历窗
向未来延伸），target != today 让归位腿直接判"不是今天"永不触发。因此本文件所有
`last_trade_date` 桩都收 `today=None` 一个可选位参（与生产签名同形）。
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

    def _log_text(self):
        """读日志全文；**没写过日志就是空串**（负向腿要断言"这行没出现"，
        而旧写法 open() 会先 FileNotFoundError——那等于判据没能出场）。"""
        p = self._log_path()
        if not os.path.exists(p):
            return ""
        return open(p, encoding="utf-8").read()

    def insert(self, table, date):
        c = sqlite3.connect(self.db_path)
        c.execute("INSERT INTO %s (trade_date) VALUES (?)" % table, (date,))
        c.commit()
        c.close()

    def insert_board(self, date, suffixes=(".SH", ".SZ", ".BJ")):
        """daily 按板块播种：每个后缀一行（ts_code 带真实后缀，板块级判据按 LIKE 命中）。"""
        c = sqlite3.connect(self.db_path)
        for suf in suffixes:
            c.execute("INSERT INTO daily (ts_code, trade_date) VALUES (?, ?)", ("600001" + suf, date))
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
             mock.patch.object(k, "last_trade_date", lambda today=None: "20260930"), \
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
        按"最近工作日"= 假日本身，8 轮拉齐落空必 rc=1）。

        §POOL-GAP（2026-10-12）把"零动作"的口径从「最后一天到了」改成「窗口内每一天都在」：
        所以这个夹具必须连 0928/0929 两天的涨停池一起种上，否则该用例就是在验旧语义。
        「真齐 ⇒ 零外部调用」这条不变量本身保留——它由下面的漏更日用例配成反向腿。"""
        self._seed_cal()
        self.insert_board("20260930")  # §BOARD-COVER 判定腿：daily 三板块各自到上一开市日
        for t in ("index_daily", "ths_limit_up_daily"):
            self.insert(t, "20260930")
        for d in ("20260928", "20260929"):  # 15 日窗内的其它开市日也齐 ⇒ 才敢断言零调用
            self.insert("ths_limit_up_daily", d)
        calls = []
        with mock.patch.object(k, "last_trade_date", lambda today=None: "20260930"), \
             mock.patch.object(k, "run", lambda cmd, **kw: calls.append(cmd) or (0, "ok")), \
             mock.patch.object(k.time, "sleep", lambda s: None):
            self.assertEqual(k.main(now=datetime.datetime(2026, 10, 5, 17, 10)), 0)
        self.assertEqual(calls, [])  # 判定为齐（含无漏更日）→ 零外部调用

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

    def test_calendar_not_covering_today_falls_back_to_pydata(self):
        """§CAL-COVER 盲窗复演（2026-10-10 现网实录形态）：日历"开市日龄"完全新鲜
        （节前最后交易日 09-30 距今仅 9 天 < 12 天阈值），但日历末行只写到 10-07
        （旧自愈窗口 end=today 的产物）——10-08/09 两个真实开市日不在表内，旧行为
        把 target 洗成 09-30、此后每天"全部到齐"直接退出＝整段漏更且全闸门绿。
        现行为：末行不覆盖今日即降级 pydata，target 正确到 10-09，并留痕。"""
        c = sqlite3.connect(self.db_path)
        c.execute("CREATE TABLE trade_cal (cal_date TEXT PRIMARY KEY, is_open INTEGER)")
        for d, op in (("20260928", 1), ("20260929", 1), ("20260930", 1),
                      ("20261001", 0), ("20261002", 0), ("20261003", 0),
                      ("20261004", 0), ("20261005", 0), ("20261006", 0),
                      ("20261007", 0)):
            c.execute("INSERT INTO trade_cal VALUES (?,?)", (d, op))
        c.commit(); c.close()
        cal_csv = ("calendar_date,is_open\n"
                   "2026-09-30,1\n2026-10-01,0\n2026-10-08,1\n2026-10-09,1\n")
        with mock.patch("urllib.request.urlopen") as u:
            u.return_value.read.return_value = cal_csv.encode("utf-8")
            got = k.last_trade_date(today=datetime.date(2026, 10, 9))
        self.assertEqual(got, "20261009")
        with open(k.LOG, encoding="utf-8") as f:
            self.assertIn("不覆盖今日", f.read())  # 覆盖检查降级留痕，取证可查


# §0927KA（2026-09-27 部署实录）：现网 trade_cal 停在 20260818 无人再刷（本次事故里
# 第一源恒过旧、全靠降级腿）——pydata 降级取到日历后必须把窗口行回写自愈 trade_cal。
class TestCalendarSelfHeal(TempDbCase):
    def test_self_heal_window_extends_past_today(self):
        """§CAL-COVER：自愈请求窗口必须向未来延伸（旧形态 end=today 是盲窗根源：
        自愈次日日历就不再覆盖今天，而开市日龄判过旧还要等最多 12 天）；且未来
        开市日随自愈落库、target（best）必须夹在 ≤今日——否则三张判定表永远追
        不平，8 轮空烧（§0927KA 事故同形）。"""
        today = datetime.date(2026, 10, 9)
        cal_csv = ("calendar_date,is_open\n"
                   "2026-09-30,1\n2026-10-01,0\n2026-10-08,1\n2026-10-09,1\n"
                   "2026-10-12,1\n2026-11-25,1\n")  # 未来开市日也在返回窗内（baostock 实测支持）
        urls = []

        def fake_urlopen(url, timeout=None):
            urls.append(url)
            rr = mock.Mock()
            rr.read.return_value = cal_csv.encode("utf-8")
            return rr

        with mock.patch("urllib.request.urlopen", side_effect=fake_urlopen):
            got = k.last_trade_date(today=today)
        self.assertEqual(got, "20261009")  # 未来开市日（10-12、11-25）不抬高 target
        self.assertEqual(len(urls), 1)
        end = urls[0].split("end=")[1]
        self.assertGreaterEqual(end, "2026-11-20")  # ≥ today+CALENDAR_LOOKAHEAD_DAYS（10-09+45=11-23）
        c = sqlite3.connect(self.db_path)
        rows = dict(c.execute("SELECT cal_date, is_open FROM trade_cal").fetchall())
        c.close()
        self.assertEqual(rows.get("20261012"), 1)  # 未来开市日已随自愈入库
        self.assertEqual(rows.get("20261001"), 0)  # 休市日同样入库


    def test_pydata_window_upserted_back_into_trade_cal(self):
        """临时库无 trade_cal 表 + pydata 可用 → target 正确到节前交易日，
        且同批日历行（含休市日 0/开市日 1）落库、表被顺手建出来。"""
        cal_csv = ("calendar_date,is_open\n"
                   "2026-09-24,1\n2026-09-25,0\n2026-09-26,0\n")  # 中秋三连休的真实形态
        with mock.patch("urllib.request.urlopen") as u:
            u.return_value.read.return_value = cal_csv.encode("utf-8")
            got = k.last_trade_date(today=datetime.date(2026, 9, 26))
        self.assertEqual(got, "20260924")
        c = sqlite3.connect(self.db_path)
        rows = dict(c.execute("SELECT cal_date, is_open FROM trade_cal").fetchall())
        c.close()
        self.assertEqual(rows, {"20260924": 1, "20260925": 0, "20260926": 0})
        with open(k.LOG, encoding="utf-8") as f:
            self.assertIn("self-heal", f.read())  # 回写留痕可取证

    def test_self_heal_write_failure_never_breaks_target(self):
        """回写腿故障（这里用「DB 路径改指目录」令 connect 后 commit 必抛）不得影响
        target 推导与返回——自愈是顺路福利，不是主流程前置。"""
        cal_csv = "calendar_date,is_open\n2026-09-24,1\n"
        orig_db = k.DB
        k.DB = tempfile.mkdtemp()  # 目录当库文件用：sqlite 打开即抛，触发 except 分支
        try:
            with mock.patch("urllib.request.urlopen") as u:
                u.return_value.read.return_value = cal_csv.encode("utf-8")
                got = k.last_trade_date(today=datetime.date(2026, 9, 25))
        finally:
            k.DB = orig_db
        self.assertEqual(got, "20260924")


# §0927KA 空转放大器收口：09-26 实录里幻觉 target 令 8 轮×1.5h 全部拉齐落空，
# 整夜锁死 dataload.exe（挡掉一次部署）。修法=与目标无关的「连续两轮三表读数零变化」
# 早停（rc 仍 1，日志留痕），保证未来任何 target 失真最多烧两轮。
class TestNoProgressAbort(TempDbCase):
    def test_phantom_target_stops_after_two_no_progress_rounds(self):
        calls = []
        with mock.patch.object(k, "latest", lambda t, col="trade_date": "20260924"), \
             mock.patch.object(k, "run", lambda cmd, **kw: calls.append(cmd) or (0, "累计 0 行")), \
             mock.patch.object(k, "fetch_index", lambda s, e: (0, 0)), \
             mock.patch.object(k, "last_trade_date", lambda today=None: "20260925"), \
             mock.patch.object(k.time, "sleep", lambda s: None):
            rc = k.main()
        self.assertEqual(rc, 1)
        # 每轮 run 三连（daily/pools/daily-k-10d），2 轮即停＝6 次调用（旧形态 8 轮＝24 次）。
        self.assertEqual(len(calls), 6)
        with open(k.LOG, encoding="utf-8") as f:
            text = f.read()
        self.assertIn("no progress 2 rounds — abort early", text)

    def test_progress_resets_no_progress_counter(self):
        """反向保护：只要表读数在推进（真缺数的正常补数形态），早停不得误伤——
        模拟每轮都有进展的长补数，8 轮全部执行（未误早停）、耗尽后仍 rc=1。"""
        state = {"n": 0}
        def fake_latest(t, col="trade_date"):
            # 读数按真实日期轴单调前移（避免拼出 "20260932" 类假日期）
            d = datetime.date(2026, 9, 24) + datetime.timedelta(days=state["n"])
            return d.strftime("%Y%m%d")
        def advance(*a, **kw):
            state["n"] += 1  # 每个外部调用都令读数前移 → 轮轮有进展
            return (0, "mocked")
        with mock.patch.object(k, "latest", fake_latest), \
             mock.patch.object(k, "run", advance), \
             mock.patch.object(k, "fetch_index", lambda s, e: advance()[:0] or (0, 0)), \
             mock.patch.object(k, "last_trade_date", lambda today=None: "20261231"), \
             mock.patch.object(k.time, "sleep", lambda s: None):
            rc = k.main()
        self.assertEqual(rc, 1)      # 目标日永追不上 → 8 轮耗尽仍非零
        self.assertGreaterEqual(state["n"], 8)  # 8 轮全部执行过（未误早停）
        with open(k.LOG, encoding="utf-8") as f:
            text = f.read()
        self.assertNotIn("abort early", text)   # 负向锁：有进展时早停不得出手
        self.assertIn("after 8 rounds", text)   # 走的是旧的耗尽路径


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
        # §BOARD-COVER 判定腿：daily 必须三板块各自到 target（单行无后缀在板块口径下
        # 等于三块全缺，不得短路判真）。
        target = "20260911"
        self.insert_board(target)
        for t in ("index_daily", "ths_limit_up_daily"):
            self.insert(t, target)
        calls = []
        k.run = lambda *a, **kw: calls.append(a) or (0, "ok")
        k.fetch_index = lambda *a: calls.append(("idx",)) or 0
        self.assertTrue(k.one_round(target))
        self.assertEqual(calls, [])

    def test_stale_daily_triggers_load_and_completes(self):
        # 日线落后：one_round 触发 dataload daily（模拟补到位——三板块一次拉齐）；
        # 池/ths 增量也各一次；二轮全新鲜即短路判成（一锤子拉齐即退的行为基础）。
        target = "20260914"
        cmds = []

        def fake_run(cmd, *a, **kw):
            cmds.append(cmd)
            if "daily" in cmd[-1]:
                self.insert_board(target)
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


# §BOARD-COVER 判定腿（2026-10-12 当日实录）：one_round 的 daily 新鲜判定原读整库 MAX——
# .BJ 当天补进的 311 行把全库 MAX 垫到 target，.SH/.SZ 停在上一交易日也宣告「all fresh」
# 零调用退出。判定改按三板块各自 MAX 全达标；落后板块打归因读数。
class TestDailyBoardFreshness(TempDbCase):
    def test_board_lag_defeats_overall_max_verdict(self):
        """缺陷复现锁：整库 MAX 已到 target、但 .BJ 停在上一交易日 → 旧判据短路判真
        （零调用退出），新判据必须拨 daily 并判不成，且点名落后板块。"""
        target = "20261012"
        c = sqlite3.connect(self.db_path)
        c.execute("INSERT INTO daily (ts_code, trade_date) VALUES ('600001.SH', ?)", (target,))
        c.execute("INSERT INTO daily (ts_code, trade_date) VALUES ('000001.SZ', ?)", (target,))
        c.execute("INSERT INTO daily (ts_code, trade_date) VALUES ('920001.BJ', '20261009')")
        c.commit()
        c.close()
        for t in ("index_daily", "ths_limit_up_daily"):
            self.insert(t, target)  # 其余两表全新鲜：唯一卡点就是 .BJ 缺今日
        self.assertEqual(k.latest_board(".SH"), target)          # 整库 MAX 口径下"看起来齐了"
        self.assertFalse(k.daily_boards_fresh(target))           # 板块级判据揭穿
        calls = []
        with mock.patch.object(k, "run", lambda cmd, *a, **kw: calls.append(cmd) or (0, "mocked")):
            self.assertFalse(k.one_round(target))
        self.assertTrue(any(c[-1] == "daily" for c in calls), "板块落后必须触发 daily 补拨")
        self.assertIn("BJ[max=20261009]", self._log_text())      # 落后板块归因读数

    def test_missing_board_is_never_fresh(self):
        """反例：.BJ 一行都没有（unknown/空板块）——哪怕 .SH/.SZ 已到 target 也不算齐
        （unknown 不冒充达标）。"""
        target = "20261012"
        self.insert_board(target, suffixes=(".SH", ".SZ"))
        for t in ("index_daily", "ths_limit_up_daily"):
            self.insert(t, target)
        self.assertEqual(k.latest_board(".BJ"), "")
        self.assertFalse(k.daily_boards_fresh(target))

    def test_all_boards_fresh_short_circuits(self):
        """三板块全到 target + 其余两表到 target → 零调用短路判真（新判据不误伤正常态）。"""
        target = "20261012"
        self.insert_board(target)
        for t in ("index_daily", "ths_limit_up_daily"):
            self.insert(t, target)
        calls = []
        with mock.patch.object(k, "run", lambda cmd, *a, **kw: calls.append(cmd) or (0, "mocked")):
            self.assertTrue(k.one_round(target))
        self.assertEqual(calls, [])


# §POOL-GAP（2026-10-12）盘口三池漏更日逐日回补。缺陷本体：one_round 的新鲜判定是
# latest(表)>=target，即 MAX(trade_date) 对比——中间漏掉的那一天既看不见也永远不会再被请求，
# 而涨停池改成整日快照落库后"MAX 到了"与"每天都有"彻底是两件事。
class TestPoolGapBackfill(TempDbCase):
    def _seed_cal(self, days):
        c = sqlite3.connect(self.db_path)
        c.execute("CREATE TABLE trade_cal (cal_date TEXT PRIMARY KEY, is_open INTEGER)")
        for d, op in days:
            c.execute("INSERT INTO trade_cal VALUES (?,?)", (d, op))
        c.commit(); c.close()

    def _window(self):
        # 2026-10-08/09 开市、10-10~11 休市（周末）、target=10-09；15 日窗覆盖 09-24 起。
        return [("20260928", 1), ("20260929", 1), ("20260930", 1), ("20261001", 0),
                ("20261008", 1), ("20261009", 1), ("20261010", 0), ("20261011", 0)]

    def test_missing_days_are_listed_and_dialed(self):
        """正例：0928/0929 两天池子 0 行 ⇒ 各拨一次 pools --date，补上后读数点名 fixed。
        target（10-09）本身不在回补腿里拨——它与 one_round 的当日腿共用，两腿并发拨同一天
        会把同一日的两次整日替换叠在一次运行里。"""
        self._seed_cal(self._window())
        self.insert_board("20261009")  # §BOARD-COVER 判定腿：daily 按三板块种到 target
        for t in ("index_daily", "ths_limit_up_daily"):
            self.insert(t, "20261009")
        self.insert("ths_limit_up_daily", "20261008")
        self.insert("ths_limit_up_daily", "20260930")  # 窗内其它开市日齐：只留 0928/0929 两个缺
        calls = []

        def fake_run(cmd, *a, **kw):
            calls.append(cmd)
            if "--date" in cmd:  # 模拟上游确有该日数据：把行补进去
                self.insert("ths_limit_up_daily", cmd[-1])
            return (0, "mocked")

        with mock.patch.object(k, "last_trade_date", lambda today=None: "20261009"), \
             mock.patch.object(k, "run", fake_run), \
             mock.patch.object(k, "fetch_index", lambda s, e: (0, 0)), \
             mock.patch.object(k, "latest", lambda t, col="trade_date": "20261009"), \
             mock.patch.object(k.time, "sleep", lambda s: None):
            self.assertEqual(k.main(now=datetime.datetime(2026, 10, 12, 17, 10)), 0)
        pool_calls = [c for c in calls if "--kind" in c and "pools" in c]
        self.assertEqual([c[-1] for c in pool_calls], ["20260928", "20260929"])
        text = self._log_text()
        self.assertIn("pools_gap day=20260928 rc=0 rows_after=1", text)
        self.assertIn("pool gap: found=2 fixed=2 left=0", text)

    def test_days_upstream_cannot_supply_stay_listed_as_missing(self):
        """反向腿：上游给不出该日（rc=1、行数仍 0）时，读数必须写成 still_missing，
        绝不能因为"跑了这一腿"就算补上了——把失败兜成完成是 §降级链会兜死分支的老形态。"""
        self._seed_cal(self._window())
        self.insert("ths_limit_up_daily", "20261009")
        self.insert("ths_limit_up_daily", "20260930")
        self.insert("ths_limit_up_daily", "20261008")
        with mock.patch.object(k, "run", lambda cmd, *a, **kw: (1, "幻影日拦停")), \
             mock.patch.object(k, "latest", lambda t, col="trade_date": "20261009"), \
             mock.patch.object(k, "fetch_index", lambda s, e: (0, 0)):
            found, fixed, left = k.fill_pool_gaps("20261009")
        self.assertEqual((found, fixed, left), (2, 0, 2))
        text = self._log_text()
        self.assertIn("rows_after=0", text)
        self.assertIn("still_missing=2", text)

    def test_unreadable_count_is_not_a_gap(self):
        """行数为"无从判定"（-1，读库失败/缺表）时不得算漏更日：0 与"读不到"含义相反，
        把后者当缺数会在库锁死的夜里对每个交易日各拨一次上游。"""
        self._seed_cal(self._window())
        with mock.patch.object(k, "row_count_on", lambda t, d: -1):
            self.assertEqual(k.pool_gap_days("20261009"), [])

    def test_no_open_days_in_calendar_means_no_dial(self):
        """无 trade_cal（冷启动/表被删）⇒ 回补腿一步都不拨。缺数不猜：工作日近似会把
        休市日当成漏更日，而休市日的池子永远是空的 ⇒ 每晚必撞一次上游空回包。"""
        with mock.patch.object(k, "run", lambda cmd, *a, **kw: self.fail("日历不可用不得拨上游")):
            self.assertEqual(k.pool_gap_days("20261009"), [])
            self.assertEqual(k.fill_pool_gaps("20261009"), (0, 0, 0))

    def test_single_run_caps_at_five_days(self):
        """单轮上限（顶配额与停机窗口的约束）：窗内 6 个缺数日只拨前 5 个，剩下的次日继续。"""
        days = [("202610%02d" % d, 1) for d in range(1, 7)]  # 0001~0006 全开市
        self._seed_cal(days)
        with mock.patch.object(k, "row_count_on", lambda t, d: 0):
            gaps = k.pool_gap_days("20261007")
        self.assertEqual(len(gaps), k.POOL_GAP_MAX_DAYS_PER_RUN)
        self.assertEqual(gaps[:2], ["20261001", "20261002"])  # 升序：先补最老的


# §TARGET-NOT-CLOSED（2026-10-12）收盘前拨测归位：缺陷形态是"收盘前手工拨一次保活
# 必报失败退出"——当日数据本来就没出，把今天当目标就是恒追不平，8 轮后 rc=1。
class TestTargetNotClosed(TempDbCase):
    def _seed_cal(self):
        c = sqlite3.connect(self.db_path)
        c.execute("CREATE TABLE trade_cal (cal_date TEXT PRIMARY KEY, is_open INTEGER)")
        for d, op in [("20261008", 1), ("20261009", 1), ("20261012", 1), ("20261013", 1)]:
            c.execute("INSERT INTO trade_cal VALUES (?,?)", (d, op))
        c.commit(); c.close()

    def test_before_close_retreats_to_previous_open_day(self):
        self._seed_cal()
        got, retreated, note = k.target_day_reachable("20261012", now=datetime.datetime(2026, 10, 12, 10, 44))
        self.assertEqual((got, retreated, note), ("20261009", True, "20261009"))
        text = self._log_text()
        self.assertIn("target-not-closed day=20261012 now=10:44 retreat=20261009", text)

    def test_after_close_target_untouched(self):
        self._seed_cal()
        got, retreated, note = k.target_day_reachable("20261012", now=datetime.datetime(2026, 10, 12, 17, 10))
        self.assertEqual((got, retreated, note), ("20261012", False, ""))
        # 归位腿不得留痕（17:10 定时轮与收盘后人工重拨同形，都不该有这行）
        self.assertNotIn("target-not-closed", self._log_text())

    def test_other_day_target_never_retreated(self):
        """target 不是今天（节假日后/昨日补数）⇒ 归位这条腿不出场，否则会把
        合法的"补节前那天"改成更早的一天。"""
        self._seed_cal()
        got, retreated, _ = k.target_day_reachable("20261009", now=datetime.datetime(2026, 10, 12, 8, 0))
        self.assertEqual((got, retreated), ("20261009", False))

    def test_retreat_needs_a_real_previous_open_day(self):
        """上一开市日不可得（日历两源皆断）时**保持原 target**并留痕：
        归位是便利，把 target 猜成某天比追不平退出更糟。"""
        with mock.patch.object(k, "last_trade_date", lambda today=None: ""):
            got, retreated, _ = k.target_day_reachable("20261012", now=datetime.datetime(2026, 10, 12, 9, 0))
        self.assertEqual((got, retreated), ("20261012", False))
        self.assertIn("retreat=none", self._log_text())

    def test_main_pre_close_exits_0_after_refilling_prev_day(self):
        """端到端（本波的验收面）：收盘前拨一次，上一开市日缺数 ⇒ 补它，补齐即 rc=0。
        旧行为在此形态下把今天当目标、8 轮空烧后 rc=1。"""
        self._seed_cal()
        inserted = []

        def fake_run(cmd, *a, **kw):
            inserted.append(cmd[-1])
            self.insert("ths_limit_up_daily", "20261009")
            self.insert_board("20261009")  # 模拟补到位＝三板块一次拉齐
            self.insert("index_daily", "20261009")
            return (0, "mocked")

        state = {"n": 0}

        def fake_latest(table, col="trade_date"):
            state["n"] += 1
            return "20261009" if inserted else ""

        # 不 patch last_trade_date：夹具里的 trade_cal 就是真实降级链的第一源，
        # 归位腿必须真的从日历里取出"上一开市日"，否则这条端到端用例验的是桩不是代码。
        with mock.patch.object(k, "run", fake_run), \
             mock.patch.object(k, "latest", fake_latest), \
             mock.patch.object(k, "fetch_index", lambda s, e: (0, 0)), \
             mock.patch.object(k, "fill_pool_gaps", lambda t: (0, 0, 0)), \
             mock.patch.object(k.time, "sleep", lambda s: None):
            rc = k.main(now=datetime.datetime(2026, 10, 12, 10, 44))
        self.assertEqual(rc, 0)
        self.assertTrue(inserted, "收盘前归位后应真的去补上一开市日")
        text = self._log_text()
        self.assertIn("all fresh up to 20261009", text)
        self.assertIn("[retreated=20261009]", text)

    def test_main_feeds_injected_clock_to_target_derivation(self):
        """接线锁（与本机墙上钟无关）：main 必须把注入的 now 交给 last_trade_date。
        上一条端到端用例只在"本机日期 ≠ 注入日期"时才逼出这个缺陷（2026-10-13 00:10
        跨零点复跑现形，白天同形是绿的）＝日期依赖的锁；这条直接捕获桩收到的实参，
        摘掉接线在任何时刻都必红。"""
        seen = []

        def fake_ltd(today=None):
            seen.append(today)
            return "20261012"

        with mock.patch.object(k, "last_trade_date", fake_ltd), \
             mock.patch.object(k, "run", lambda cmd, *a, **kw: (0, "mocked")), \
             mock.patch.object(k, "fetch_index", lambda s, e: (0, 0)), \
             mock.patch.object(k, "fill_pool_gaps", lambda t: (0, 0, 0)), \
             mock.patch.object(k, "latest", lambda t, col="trade_date": "20261012"), \
             mock.patch.object(k, "daily_boards_fresh", lambda t: True), \
             mock.patch.object(k.time, "sleep", lambda s: None):
            self.assertEqual(k.main(now=datetime.datetime(2026, 10, 12, 10, 44)), 0)
        # 只看第一次调用＝target 推导那一处（归位腿内部取"上一开市日"是第二次调用，
        # 那里传的本来就是派生日期，不属于本锁的射程）。
        self.assertEqual(seen[:1], [datetime.date(2026, 10, 12)],
                         "target 推导收到的必须是注入时钟的那一天（got=%r）" % (seen,))


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


# §BOARD-COVER：按板块覆盖读数——`.BJ` 整板断更六周而全库 MAX 口径看不见的教训收编成
# 天天可见的读数。三态齐验：缺板可见 / 库读不到=unknown（绝不冒充 0）/ 空表=empty 显式态。
class TestBoardCoverage(TempDbCase):
    def _insert_daily(self, code, date):
        c = sqlite3.connect(self.db_path)
        c.execute("INSERT INTO daily (ts_code, trade_date) VALUES (?,?)", (code, date))
        c.commit(); c.close()

    def test_missing_board_visible_against_shared_latest(self):
        """正例：.SH/.SZ 都到 1012 而 .BJ 停在 0828 ⇒ BJ 行数读 0、max 停在断更日，
        与全库同日基线直接对照可见——这正是 .BJ 六周断更应该被看见的形态。"""
        for code in ("600001.SH", "600002.SH", "000001.SZ"):
            self._insert_daily(code, "20261012")
        self._insert_daily("920001.BJ", "20260828")
        line = k.board_coverage_line()
        self.assertIn("board_coverage latest=20261012", line)
        self.assertIn("SH[max=20261012 rows=2]", line)
        self.assertIn("SZ[max=20261012 rows=1]", line)
        self.assertIn("BJ[max=20260828 rows=0]", line)

    def test_unreadable_db_is_unknown_never_zero(self):
        """反向腿：库读不到必须显式 unknown。"读不到"冒充 0 行＝把断板洗成
        「确凿没数据」，与 0 在缺数判据里含义相反（§新鲜度 unknown-not-0 同族）。"""
        k.DB = self.db_path + ".nonexistent" + os.sep + "x.db"
        try:
            line = k.board_coverage_line()
        finally:
            k.DB = self.db_path
        self.assertIn("state=unknown", line)
        self.assertNotIn("rows=0", line)

    def test_empty_table_is_explicit_empty_state(self):
        """daily 全表无行（全新部署合法态）→ 显式 empty，而不是一条 rows=0 的假读数。"""
        line = k.board_coverage_line()
        self.assertIn("state=empty", line)

    def test_main_logs_coverage_once_per_run(self):
        """main 收尾必打一行 board_coverage（放补数轮之后＝读数即当日补完后的真实覆盖面）。"""
        with mock.patch.object(k, "last_trade_date", lambda today=None: "20261009"), \
             mock.patch.object(k, "target_day_reachable", lambda t, now=None: (t, False, "")), \
             mock.patch.object(k, "fill_pool_gaps", lambda t: (0, 0, 0)), \
             mock.patch.object(k, "one_round", lambda t: True), \
             mock.patch.object(k.time, "sleep", lambda s: None):
            self.assertEqual(k.main(now=datetime.datetime(2026, 10, 9, 17, 30)), 0)
        text = self._log_text()
        self.assertEqual(text.count("board_coverage "), 1, "board_coverage 行应恰好一条，实际日志：\n" + text)


if __name__ == "__main__":
    unittest.main()
