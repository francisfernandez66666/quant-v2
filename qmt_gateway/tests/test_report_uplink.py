# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_report_uplink.py — 本地进程上行客户端的行为测试（§EXT-BRAIN 回报上行腿）。

覆盖 report_uplink 的四条硬规则，逐条对应模块 docstring：
  1. 信封形状对 golden（contract/report_fields.json）——发出键集必须是 Go 信封反射集的子集，
     事件类型与字段名与 handler.py 那代契约逐字同形（§M4 丢腿教训：两侧不同名即静默丢）。
  2. missing≠empty：导出文件缺席 ⇒ 该类一个事件都不发且原因落本地状态；
     空文件 ⇒ 允许发空快照；半文件（行数骤降）⇒ 不发、保留旧值。
  3. 只有 2xx 才推进"已报"基线（§FIX-20261028-F02）；瞬态失败进有界重试队列、
     永久 4xx 进死信（§P1-8 同口径）。
  4. 幂等去重：同内容哈希只发一次；心跳携带已安装策略版本 + 执行器版本/指纹。
外加一条红线测试：**钥匙字符串绝不出现**在任何日志流、标准输出/错误、或本模块写出的文件里。

测试传输一律注入假 opener（或本机 127.0.0.1 桩服务器），绝不打真实网络；
TLS 校验那条由代码审读保证（urllib 默认工厂，全模块无 unverified context）。
"""
import contextlib
import io
import json
import tempfile
import shutil
import logging
import os
import sqlite3
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import unittest

import report_uplink as up

_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
GOLDEN_PATH = os.path.join(_ROOT, "qmt_gateway", "contract", "report_fields.json")

FAKE_TOKEN = "FAKE-SECRET-TOKEN-0123456789abcdef"
FIXED_NOW = 1793772000.0  # 钉死的 epoch 秒：去重测试要求"同输入同哈希"，时钟必须可复现


def _load_golden():
    with open(GOLDEN_PATH, encoding="utf-8") as f:
        return json.load(f)


class _Fixture:
    """临时导出根 + config.xt.json + 假策略/执行器文件的一次性夹具。

    §CARRIER-SHADOW 起还带一本**执行器影子账本**（shadow_db）：上行腿对它是只读的，
    夹具因此必须能造出四种形态——没有文件 / 有文件但没表（旧载体）/ 有表但今天零行 /
    今天有行。这四种在上行侧的处置完全不同（前三者一个字节都不发），把它们压成一种
    就等于把"这台机器不会记影子账"和"今天没有影子判定"混成同一条读数。
    """

    def __init__(self, tmp, with_positions=True, positions_blank=False, positions_rows=6,
                 shadow_rows=2, shadow_state="present"):
        self.tmp = tmp
        self.export_dir = os.path.join(tmp, "exports")
        os.makedirs(self.export_dir, exist_ok=True)
        self.strategy_file = os.path.join(tmp, "installed_strategy.py")
        self.executor_file = os.path.join(tmp, "local_executor.py")
        with open(self.strategy_file, "w", encoding="utf-8") as fh:
            fh.write('STRATEGY_VERSION = "v02-20261104"\n')
        with open(self.executor_file, "w", encoding="utf-8") as fh:
            fh.write('STRATEGY_VERSION = "v01-20261103"\nX = 1\n')
        if with_positions:
            body = "代码,名称,数量,可用数量\n"
            if not positions_blank:
                for i in range(positions_rows):
                    body += "600519,贵州茅台,%d,%d\n" % (100 + i, 100 + i)
            self._write("持仓.csv", body)
        self._write("委托.csv", "代码,委托号,委托状态,买卖标志,价格,数量,时间,投资备注\n"
                                "600519,7701,已报,买入,10.0,100,2026-11-05 09:31:00,v24-FC600519\n"
                                "000001,7702,已成,卖出,8.5,200,2026-11-05 10:02:00,\n")
        # §TRADE-LEDGER（2026-11-13）：成交导出——台账的地基腿。备注列经 broker_reads
        # 新映射随行上行，Go 反解战法归因；费用列柜台不导出 → 不发（Go 按 0 落）。
        self._write("成交.csv", "代码,委托号,买卖标志,成交均价,成交数量,成交金额,成交时间,成交编号,投资备注\n"
                                "600519,7701,买入,10.05,100,1005.0,2026-11-13 09:31:05,T9001,v24-FC600519\n")
        self._write("资产.csv", "总资产,可用资金,证券市值\n100000.0,40000.0,60000.0\n")
        # 影子账本：与执行器 --state-db 同一张表名与列名（列名漂移＝上行一条都读不出，
        # 而读不出的表现是"今天没报"，所以这里必须复刻真表的形状，不能自编简化版）。
        self.shadow_db = os.path.join(tmp, "executor_state.db")
        self.build_shadow_journal(shadow_rows, shadow_state)
        self.config_path = os.path.join(tmp, "config.xt.json")
        with open(self.config_path, "w", encoding="utf-8") as fh:
            json.dump({"token": FAKE_TOKEN, "user_id": "u_leg01",
                       "report_url": "http://127.0.0.1:1",  # 缺省指向黑洞端口，测试自行注入 opener
                       "uplink_export_roots": [self.export_dir],
                       "uplink_strategy_file": self.strategy_file,
                       "uplink_executor_file": self.executor_file,
                       "uplink_state_dir": tmp,
                       "uplink_shadow_db": self.shadow_db}, fh)

    def build_shadow_journal(self, rows, state="present", day=None):
        """按形态造影子账本：absent=删文件 / table_absent=有库无表 / present=有表有行。

        day=None 时落在 FIXED_NOW 的北京交易日上（"今天"），传别的值就是历史日
        ——用于钉"表在而今天零行"这一支：账本里**有**行但都不是今天的，上行仍必须一个都不发。
        """
        if state == "absent":
            if os.path.exists(self.shadow_db):
                os.remove(self.shadow_db)
            return
        conn = sqlite3.connect(self.shadow_db)
        try:
            if state == "table_absent":
                # 旧载体（11-05 之前建的库）：有别的表，没有 shadow_decisions。
                conn.execute("CREATE TABLE IF NOT EXISTS sent_orders (day TEXT, short_code TEXT)")
                conn.commit()
                return
            conn.execute(
                "CREATE TABLE IF NOT EXISTS shadow_decisions ("
                "day TEXT NOT NULL, short_code TEXT NOT NULL, symbol TEXT NOT NULL,"
                " kind TEXT NOT NULL, side TEXT NOT NULL, qty INTEGER NOT NULL DEFAULT 0,"
                " price REAL NOT NULL DEFAULT 0, amount REAL,"
                " mode TEXT NOT NULL DEFAULT 'shadow', judgement TEXT NOT NULL DEFAULT '',"
                " first_ts REAL NOT NULL, last_ts REAL NOT NULL,"
                " PRIMARY KEY (day, short_code))")
            day = day or up._day_beijing(FIXED_NOW)
            for i in range(rows):
                conn.execute(
                    "INSERT OR REPLACE INTO shadow_decisions"
                    " (day, short_code, symbol, kind, side, qty, price, amount, mode,"
                    "  judgement, first_ts, last_ts) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
                    (day, "DR%03d" % i, "600519.SH" if i % 2 == 0 else "000001.SZ",
                     "dragon_return", "sell" if i % 2 else "buy", 100 + i, 10.5 + i,
                     None if i % 2 else 1500.0, "shadow",
                     '{"hard_block":false,"market_scale":0.6,"target":0.35,'
                     '"emotion_phase":"choppy"}',
                     FIXED_NOW - 3600 + i, FIXED_NOW - 600 + i))
            conn.commit()
        finally:
            conn.close()

    def _write(self, name, body):
        with open(os.path.join(self.export_dir, name), "w", encoding="utf-8") as fh:
            fh.write(body)

    def rewrite_positions(self, body):
        self._write("持仓.csv", body)

    def uplink(self, opener):
        return up.ReportUplink(self.config_path, now_fn=lambda: FIXED_NOW, opener=opener)



class _OkOpener:
    """假传输：全部回 200，并把 (url, token, body) 收集下来供形状断言。"""

    def __init__(self, status=200):
        self.status = status
        self.calls = []

    def __call__(self, url, token, body):
        self.calls.append((url, token, json.loads(body.decode("utf-8"))))
        return self.status


class TestCarrierShadowLeg(unittest.TestCase):
    """§CARRIER-SHADOW：影子判定这条上行腿的四态、映射、只读与幂等。

    这条腿存在的理由是"5 日对照窗口要有分子"，所以它的判据全都是**数的来路**而不是数本身：
      * 三种"没有"必须分开（没接账本 / 旧载体不会记 / 今天真没判定），折成一种就等于
        让静默日去凑满 5 天——那正是这枚开关要防的形状；
      * 行里的可空数值（amount / market_scale / target_scale）**缺席就不许出现在事件里**：
        Go 侧把它们读成 NULL=判断腿没接线，写成 0 就是"定权说了零仓位"这句假话；
      * 只读连接：上行腿写执行器账本一次，幂等真源就从执行器挪到了这里（两本账）；
      * 事件里的 at 由内容推出：at 取墙钟的话同一份账本每轮都是新哈希，去重账形同虚设。
    """

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        self.opener = _OkOpener()

    def tearDown(self):
        self._tmpdir.cleanup()

    def _fx(self, **kw):
        return _Fixture(self._tmpdir.name, **kw)

    def _shadow_events(self, fx):
        u = fx.uplink(self.opener)
        u.run_once()
        return [c[2] for c in self.opener.calls if c[2].get("type") == "carrier_shadow"]

    def test_journal_absent_sends_nothing_and_says_why(self):
        """账本文件不存在 ⇒ 一个都不发 + 原因 journal_missing（不是"今天零条"）。"""
        fx = self._fx(shadow_state="absent")
        self.assertEqual(self._shadow_events(fx), [])
        u = fx.uplink(_OkOpener())
        self.assertIsNone(u._read_shadow_rows())
        self.assertIn("journal_missing", [s["reason"] for s in u.skips if s["kind"] == "shadow"])

    def test_old_journal_without_table_is_not_zero_rows(self):
        """旧载体建的库（没有 shadow_decisions 表）⇒ journal_table_absent，修法是升级载体。

        这一支和 no_rows_today 混在一起就会把"这台机器的载体还不会记影子账"读成
        "今天没有影子判定"——前者要换载体，后者只需要等盘中，两种修复完全不同。
        """
        fx = self._fx(shadow_state="table_absent")
        self.assertEqual(self._shadow_events(fx), [])
        u = fx.uplink(_OkOpener())
        self.assertIsNone(u._read_shadow_rows())
        self.assertIn("journal_table_absent", [s["reason"] for s in u.skips if s["kind"] == "shadow"])

    def test_rows_only_on_other_day_sends_nothing(self):
        """表在、账本里有行，但都不是**今天**的 ⇒ no_rows_today，且一个都不发。

        发"昨天的行"会让 Go 把昨天记成今天报过（Go 侧按 day 落库、按窗口回看），
        而报空批次又会被 Go 按纪律 400 拒收——两边都拒绝，所以只能在这里就不发。
        """
        fx = self._fx(shadow_rows=0)
        fx.build_shadow_journal(3, "present", day="20261103")  # 昨天：主键是 (day, short_code)
        self.assertEqual(self._shadow_events(fx), [])
        u = fx.uplink(_OkOpener())
        self.assertIsNone(u._read_shadow_rows())
        self.assertIn("no_rows_today", [s["reason"] for s in u.skips if s["kind"] == "shadow"])

    def test_event_shape_mirrors_golden_and_keeps_nulls_absent(self):
        """事件键集＝golden 登记面；行键集 ⊆ shadow_row_fields 且**不含** user_id/seen_at。

        那两列是 Go 自己的（归属由钥匙反查、到达时刻由广州盖），上行发出它们就是
        在给服务端一个"看起来能信"的自报值——§安全 T1 在同一条写面上反复咬过我们。
        """
        golden = _load_golden()
        fx = self._fx(shadow_rows=4)  # 两行带 amount（买入）、两行不带（卖出常如此）
        events = self._shadow_events(fx)
        self.assertEqual(len(events), 1)
        ev = events[0]
        self.assertEqual(set(ev.keys()), set(golden["report_uplink_emitted_fields"]["carrier_shadow"]))
        self.assertEqual(ev["user_id"], "u_leg01")  # 信封里带是历史契约（Go 一律不信，归属仍取钥匙）
        rows = ev["shadow"]
        self.assertEqual(len(rows), 4)
        allowed = set(golden["shadow_row_emitted_fields"])
        for r in rows:
            self.assertTrue(set(r.keys()) <= allowed, "影子行发出了未登记的键 %s" % sorted(set(r.keys()) - allowed))
            self.assertNotIn("user_id", r, "行内自报归属：Go 侧会丢弃，但契约上就不该存在")
            self.assertNotIn("seen_at", r, "到达时刻只有广州知道")
            self.assertRegex(r["ts_code"], r"^\d{6}\.(SH|SZ|BJ)$")
        with_amount = [r for r in rows if "amount" in r]
        without = [r for r in rows if "amount" not in r]
        self.assertTrue(with_amount and without, "夹具应同时覆盖有金额与无金额两支")
        for r in without:
            self.assertNotIn("amount", r, "缺金额的卖出信号被写成了 0（missing 折成 empty）")
        # 判断腿读数：blob 里的 market_scale/target 改名上行，note 拼成一枚 ASCII 串
        for r in rows:
            self.assertEqual(r["market_scale"], 0.6)
            self.assertEqual(r["target_scale"], 0.35)
            self.assertIn("hard_block=False", r["sizing_note"])

    def test_judgement_without_sizing_leg_keeps_scale_columns_absent(self):
        """判断腿没接线（judgement 空串）⇒ market_scale/target_scale/sizing_note 全部缺席。

        这一支是这条腿最容易写错的地方：给个 0.0 看起来"更完整"，Go 侧读出来的却是
        "定权把这票判成了零仓位"，对照表会把它当成一条**分歧**计入 5 日窗口。
        """
        fx = self._fx(shadow_rows=0)
        conn = sqlite3.connect(fx.shadow_db)
        try:
            conn.execute(
                "INSERT INTO shadow_decisions (day, short_code, symbol, kind, side, qty, price,"
                " amount, mode, judgement, first_ts, last_ts) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
                (up._day_beijing(FIXED_NOW), "XX001", "600000.SH", "dragon", "buy",
                 100, 10.0, 1000.0, "shadow", "", FIXED_NOW - 60, FIXED_NOW - 30))
            conn.commit()
        finally:
            conn.close()
        rows = self._shadow_events(fx)[0]["shadow"]
        self.assertEqual(len(rows), 1)
        for key in ("market_scale", "target_scale", "sizing_note"):
            self.assertNotIn(key, rows[0], "空判断被写成了 %s=0（那是把缺数据当事实）" % key)

    def test_unreadable_journal_never_becomes_an_empty_batch(self):
        """账本打不开（这里是坏文件）⇒ 不发、并记 journal_unreadable，绝不发空列表。"""
        fx = self._fx(shadow_rows=0)
        with open(fx.shadow_db, "wb") as fh:
            fh.write(b"this is not a sqlite file at all")
        self.assertEqual(self._shadow_events(fx), [])
        u = fx.uplink(_OkOpener())
        self.assertIsNone(u._read_shadow_rows())
        reasons = [s["reason"] for s in u.skips if s["kind"] == "shadow"]
        self.assertTrue(any(r.startswith("journal_unreadable") or r.startswith("journal_query_failed")
                            for r in reasons), reasons)

    def test_uplink_never_writes_the_executor_journal(self):
        """只读连接：跑完整一轮后账本逐字节不变（幂等真源只能有一个）。"""
        fx = self._fx(shadow_rows=3)
        with open(fx.shadow_db, "rb") as fh:
            before = fh.read()
        self.assertEqual(len(self._shadow_events(fx)), 1)
        with open(fx.shadow_db, "rb") as fh:
            self.assertEqual(fh.read(), before, "上行腿写了执行器的账本：幂等键从此有了两本账")

    def test_same_journal_content_is_not_sent_twice(self):
        """幂等：同一份没变的账本，第二轮 skipped_dupe 挡住（at 由内容推出，不是墙钟）。

        如果 at 取 self._now_fn()，每轮哈希都不同⇒同一批判定会反复灌给广州；
        那正是"重启后不许重灌"要防的形状，所以这里必须钉住"零新增发送"。
        """
        fx = self._fx(shadow_rows=2)
        u = fx.uplink(self.opener)
        u.run_once()
        first = [c for c in self.opener.calls if c[2].get("type") == "carrier_shadow"]
        self.assertEqual(len(first), 1)
        report = u.run_once()
        again = [c for c in self.opener.calls if c[2].get("type") == "carrier_shadow"]
        self.assertEqual(len(again), 1, "同一份账本被重发：事件的 at 里混进了墙钟")
        self.assertGreaterEqual(report["skipped_dupe"], 1)

    def test_batches_respect_the_row_cap(self):
        """超单批上限必须**切批**而不是截断：Go 侧超 carrierShadowMaxRows 是整条 400。

        切错一刀（少发一批）是丢证据，整条被拒也是一天证据全丢，两种都要防；
        这里断言"事件数 = ⌈行/上限⌉"且"所有行都被报出且只报一次"。
        """
        fx = self._fx(shadow_rows=7)
        u = fx.uplink(self.opener)
        u.shadow_max_rows = 3
        events = u.build_events()
        shadow = [e for e in events if e.get("type") == "carrier_shadow"]
        self.assertEqual(len(shadow), 3, "7 行 / 每批 3 应当切成三批")
        codes = [r["short_code"] for e in shadow for r in e["shadow"]]
        self.assertEqual(len(codes), 7)
        self.assertEqual(len(set(codes)), 7, "同一行被切进两批（重发会灌窗口）")
        self.assertTrue(all(len(e["shadow"]) <= 3 for e in shadow))

    def test_bad_symbol_row_is_dropped_and_counted_not_guessed(self):
        """认不出交易所的行整行丢弃并记原因，绝不猜后缀（000001 沪指数/深个股同码的老坑）。"""
        fx = self._fx(shadow_rows=0)
        conn = sqlite3.connect(fx.shadow_db)
        try:
            conn.execute(
                "INSERT INTO shadow_decisions (day, short_code, symbol, kind, side, qty, price,"
                " amount, mode, judgement, first_ts, last_ts) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
                (up._day_beijing(FIXED_NOW), "BAD001", "NOT-A-CODE", "dragon", "buy",
                 100, 10.0, 1000.0, "shadow", "", FIXED_NOW - 60, FIXED_NOW - 30))
            conn.commit()
        finally:
            conn.close()
        u = fx.uplink(_OkOpener())
        self.assertEqual(u._build_shadow_events(), [])
        reasons = [s["reason"] for s in u.skips if s["kind"] == "shadow"]
        self.assertTrue(any(r.startswith("bad_code_row") for r in reasons), reasons)
        self.assertIn("all_rows_rejected", reasons,
                      "有行但全被摘掉必须留一条汇总，否则 dry-run 看不出这种最难自查的形态")

    def test_key_never_appears_in_shadow_payload(self):
        """红线：影子事件（含行内 sizing_note/decided_at）里不许出现钥匙串。"""
        fx = self._fx(shadow_rows=5)
        self._shadow_events(fx)
        for url, token, body in self.opener.calls:
            self.assertEqual(token, FAKE_TOKEN)  # 走 Authorization 头
            self.assertNotIn(FAKE_TOKEN, json.dumps(body, ensure_ascii=False))


class TestEnvelopeShape(unittest.TestCase):

    """规则 1：发出键集对 golden 的子集锁 + 字段名逐字镜像 handler.py 契约。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        self.fx = _Fixture(self._tmpdir.name)
        self.opener = _OkOpener()
        u = self.fx.uplink(self.opener)
        u.run_once()
        self.events = [c[2] for c in self.opener.calls]
        self.golden = _load_golden()

    def tearDown(self):
        self._tmpdir.cleanup()

    def _by_type(self, t):
        return [e for e in self.events if e.get("type") == t]

    def test_all_keys_within_envelope_golden(self):
        envelope = set(self.golden["report_event_fields"])
        for e in self.events:
            orphan = set(e.keys()) - envelope
            self.assertFalse(orphan, "事件 %s 发出了 Go 信封没有的键 %s（§M4 丢腿）"
                             % (e.get("type"), sorted(orphan)))

    def test_positions_account_order_field_names(self):
        pos = self._by_type("positions")
        self.assertEqual(len(pos), 1)
        self.assertEqual(set(pos[0].keys()),
                         {"type", "user_id", "positions", "synced", "data_ts"})
        self.assertIs(pos[0]["synced"], True)
        for row in pos[0]["positions"]:
            self.assertTrue(set(row.keys()) <= set(self.golden["positions_row_fields"]),
                            "对账行发出了 RealPosition 反射集外的键: %s" % sorted(row.keys()))
            self.assertRegex(row["ts_code"], r"^\d{6}\.(SH|SZ|BJ)$")  # Go §F2 整批拒收的红线形态
        acc = self._by_type("account")
        self.assertEqual(len(acc), 1)
        self.assertEqual(set(acc[0].keys()), {"type", "user_id", "asset", "synced", "data_ts"})
        self.assertEqual(acc[0]["asset"], {"total_asset": 100000.0, "cash": 40000.0,
                                           "market_value": 60000.0, "frozen_cash": 0.0})
        orders = self._by_type("order")
        self.assertEqual(len(orders), 2)
        # §TRADE-LEDGER（2026-11-13）：order 事件字段名对照**本腿自己的** golden 发出集——
        # 旧对照对象是 gateway_emitted_fields（handler.py 时代），但两条腿已合法分化：
        # uplink 的 order 带 remark（投资备注=唯一战法归因源），gateway 腿（盒上已死）不带。
        want = set(self.golden["report_uplink_emitted_fields"]["order"])
        for o in orders:
            self.assertEqual(set(o.keys()), want, "order 事件字段名必须逐字镜像既有契约")
        self.assertEqual(orders[0]["order_id"], "7701")
        self.assertEqual(orders[1]["code"], "000001.SZ")  # 裸 6 位码归一：0 开头=深市（平安银行形态）
        self.assertEqual(orders[0]["side"], "买入")

        # §TRADE-LEDGER（2026-11-13）：成交上行腿（台账地基）形状锁
        trades = self._by_type("trade")
        self.assertEqual(len(trades), 1)
        self.assertEqual(set(trades[0].keys()),
                         set(self.golden["report_uplink_emitted_fields"]["trade"]),
                         "trade 事件发出键集必须与 golden 逐字一致")
        self.assertEqual(trades[0]["code"], "600519.SH")
        self.assertEqual(trades[0]["side"], "买入")
        self.assertEqual(trades[0]["price"], 10.05)
        self.assertEqual(trades[0]["qty"], 100)
        self.assertEqual(trades[0]["trade_id"], "T9001")
        self.assertEqual(trades[0]["remark"], "v24-FC600519",
                         "投资备注必须随成交行上行（Go 反解战法归因的唯一来源）")
        self.assertEqual(trades[0]["signal_id"], "")

    def test_uplink_emit_sets_match_golden_section(self):
        # golden 的 report_uplink_emitted_fields 是这条腿自己的发出集（与 handler.py 的
        # gateway_emitted_fields 并列、互不冒充）——本模块加/删键不同批改 golden 即红。
        # diagnostic（§C3 2026-11-13）只在发生 skip 的轮次才发，主夹具四类齐全无 skip，
        # 由 test_diagnostic_event_on_missing_exports 单独钉住。
        want = self.golden["report_uplink_emitted_fields"]
        seen_types = set()
        for t, keys in want.items():
            if t == "diagnostic":
                continue
            for e in self._by_type(t):
                seen_types.add(t)
                self.assertEqual(set(e.keys()), set(keys),
                                 "事件 %s 发出键集与 golden.report_uplink_emitted_fields 不一致" % t)
        self.assertEqual(seen_types, set(want.keys()) - {"diagnostic"},
                         "夹具应逐类各至少发出一条（diagnostic 除外），都要被钉到")

    def test_diagnostic_event_on_missing_exports(self):
        """§C3（2026-11-13）：读数缺失的轮次必须发出一条 diagnostic 观测事件。

        旧实现 skip 原因只躺在盒子本地状态里、云端完全静默——#5 的四候选根因因此
        迟迟无法定案。锁三件事：①有 skip 才发（常态零噪音）；②键集与 golden 一致；
        ③skips 里每条都带 kind/reason/at 且 kind 覆盖了缺文件的类别。
        """
        import tempfile
        tmp2 = tempfile.TemporaryDirectory()
        try:
            # 导出根为空目录 ⇒ positions/account/order 全部 file_not_found
            fx2 = _Fixture(tmp2.name, with_positions=False)
            opener2 = _OkOpener()
            u2 = fx2.uplink(opener2)
            u2.run_once()
            events2 = [c[2] for c in opener2.calls]
            diags = [e for e in events2 if e.get("type") == "diagnostic"]
            self.assertEqual(len(diags), 1, "有 skip 的轮次应恰好发出一条 diagnostic")
            golden_keys = set(self.golden["report_uplink_emitted_fields"]["diagnostic"])
            self.assertEqual(set(diags[0].keys()), golden_keys,
                             "diagnostic 发出键集必须与 golden 逐字一致")
            skips = diags[0]["skips"]
            self.assertTrue(skips, "diagnostic 不许发空清单（那等于没有信息量）")
            for s in skips:
                self.assertTrue({"kind", "reason", "at"} <= set(s.keys()),
                                "skip 条目缺可取证字段: %r" % (s,))
            self.assertTrue(any(s["kind"] == "positions" for s in skips),
                             "持仓类缺失必须出现在诊断里")
        finally:
            tmp2.cleanup()

        # 对照组：四类齐全的轮次（主夹具）绝不多发 diagnostic（常态零噪音）
        self.assertEqual(self._by_type("diagnostic"), [],
                         "无 skip 的轮次不得发 diagnostic（观测腿不许刷屏）")

    def test_positions_row_uplink_keys_within_golden(self):
        # 2026-11-13: broker_reads.py now maps the export's cost / period-high /
        # first-buy columns, so the uplink actually carries them. Every one is
        # already in golden.positions_row_fields (Go RealPosition reflection set).
        allowed = {"ts_code", "name", "qty", "user_id", "can_use_qty", "updated_at",
                   "cost_price", "amount", "highest_price", "anchor_price"}
        pos = self._by_type("positions")[0]
        for row in pos["positions"]:
            self.assertTrue(set(row.keys()) <= allowed,
                            "上行对账行发出了未登记的键: %s" % sorted(set(row.keys()) - allowed))
            # 正向锁：登记了就必须真发（防「字段进了 golden 但停发」的假腿）。
            self.assertIn("cost_price", row, "成本价字段必须随持仓行上报")
            self.assertIn("amount", row, "成本×数量必须与成本价同源上报")

    def test_heartbeat_carries_strategy_versions(self):
        hb = self._by_type("heartbeat")
        self.assertEqual(len(hb), 1)
        # 已安装内置策略文件的 STRATEGY_VERSION 进信封新腿 strategy_version
        self.assertEqual(hb[0]["strategy_version"], "v02-20261104")
        # 执行器版本 + 指纹进 reason（ASCII 键值串，heartbeat 分支 Go 不消费、不误进账）
        self.assertIn("executor_version=v01-20261103", hb[0]["reason"])
        self.assertRegex(hb[0]["reason"], r"executor_fingerprint=[0-9a-f]{12}")
        self.assertRegex(hb[0]["reason"], r"uplink_fingerprint=[0-9a-f]{12}")

    def test_attribution_and_url_wiring(self):
        for url, token, _body in self.opener.calls:
            self.assertTrue(url.endswith("/api/qmt/report"))
            self.assertEqual(token, FAKE_TOKEN)  # 钥匙只走 Authorization 头，不进任何载荷体
            self.assertNotIn(FAKE_TOKEN, json.dumps(_body))


class TestMissingNotEmpty(unittest.TestCase):
    """规则 2：missing/empty/半文件 三种读数三种处置（§MANUAL-SYNC 边界的抄法）。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        self.tmp = self._tmpdir.name

    def tearDown(self):
        self._tmpdir.cleanup()

    def test_missing_file_sends_nothing_and_records_reason(self):
        fx = _Fixture(self.tmp, with_positions=False)
        opener = _OkOpener()
        fx.uplink(opener).run_once()
        types = [c[2].get("type") for c in opener.calls]
        self.assertNotIn("positions", types, "持仓导出缺席时发出了 positions 事件——missing 被当成空仓")
        with open(os.path.join(self.tmp, "uplink_state.json"), encoding="utf-8") as f:
            state = json.load(f)
        self.assertTrue([s for s in state["skips"]
                         if s["kind"] == "positions" and s["reason"] == "file_not_found"],
                        "缺席原因必须本地留痕（不发≠没发生）")

    def test_empty_file_sends_empty_snapshot(self):
        fx = _Fixture(self.tmp, positions_blank=True)
        opener = _OkOpener()
        fx.uplink(opener).run_once()
        pos = [c[2] for c in opener.calls if c[2].get("type") == "positions"]
        self.assertEqual(len(pos), 1, "文件在而零行是合法空读数，允许发空快照由 Go 守卫兜底")
        self.assertEqual(pos[0]["positions"], [])

    def test_half_written_export_sends_nothing_this_round(self):
        fx = _Fixture(self.tmp, positions_rows=6)
        opener = _OkOpener()
        u = fx.uplink(opener)
        u.run_once()  # 第一份 6 行可信读数已发
        del opener.calls[:]
        fx.rewrite_positions("代码,名称,数量,可用数量\n600519,贵州茅台,100,100\n")  # 6→1 骤降
        u.run_once()
        types = [c[2].get("type") for c in opener.calls]
        self.assertNotIn("positions", types, "行数骤降=半文件，本轮不得上送")
        skips = [s for s in u.skips if s["kind"] == "positions"]
        self.assertTrue(any(s["reason"].startswith("parse_fail:row_collapse") for s in skips))


class TestFailurePosture(unittest.TestCase):
    """规则 3/4：2xx 才推进基线、瞬态进有界重试、永久 4xx 进死信、同内容不重发。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        self.tmp = self._tmpdir.name

    def tearDown(self):
        self._tmpdir.cleanup()

    def test_only_2xx_advances_baseline(self):
        fx = _Fixture(self.tmp)
        failing = _OkOpener(status=500)
        u = fx.uplink(failing)
        rep = u.run_once()
        self.assertEqual(rep["sent"], 0)
        self.assertEqual(u.sent_hashes, [], "非 2xx 之前不得推进已报基线（§FIX-20261028-F02）")
        self.assertTrue(u._retry, "瞬态失败必须留在重试队列")
        ok = _OkOpener(status=200)
        u._opener = ok
        rep2 = u.run_once()
        self.assertGreater(rep2["sent"], 0, "恢复后积压应重发")

    def test_dedupe_same_content_not_reposted(self):
        fx = _Fixture(self.tmp)
        a = _OkOpener()
        u = fx.uplink(a)
        u.run_once()
        first_n = len(a.calls)
        b = _OkOpener()
        u2 = fx.uplink(b)  # 新进程实例、同状态文件：跨轮去重靠落盘的哈希账
        rep = u2.run_once()
        self.assertEqual(len(b.calls), 0, "同一份未变的导出不得二次上送（含重启后）")
        self.assertEqual(rep["skipped_dupe"], first_n)

    def test_permanent_4xx_dead_letters(self):
        fx = _Fixture(self.tmp)
        u = fx.uplink(_OkOpener(status=400))
        rep = u.run_once()
        self.assertGreater(rep["dead"], 0)
        self.assertEqual(rep["retry"], 0, "永久拒绝不是瞬态，不许占重试队列（§P1-8 毒丸）")
        self.assertEqual(u._retry and len(u._retry) or 0, 0)

    def test_retry_queue_is_bounded(self):
        fx = _Fixture(self.tmp)
        fx.rewrite_positions("代码,名称,数量,可用数量\n" +
                             "".join("600%03d,票%d,%d,%d\n" % (i, i, i, i) for i in range(50)))
        # 把委托导出改成 60 行，制造远超队列上限的事件量
        with open(os.path.join(fx.export_dir, "委托.csv"), "w", encoding="utf-8") as fh:
            fh.write("代码,委托号,委托状态,买卖标志,价格,数量,时间\n")
            for i in range(60):
                fh.write("600%03d,9%03d,已报,买入,10.0,100,2026-11-05 09:31:00\n" % (i, i))
        u = fx.uplink(_OkOpener(status=503))
        u.queue_max = 10
        u.run_once()
        self.assertLessEqual(len(u._retry), 10)
        self.assertGreater(u.stats["dropped_queue_overflow"], 0)


class TestNeverLeakTheKey(unittest.TestCase):
    """红线：钥匙字符串绝不进日志/stdout/stderr/任何本模块写出的文件（B-N2 / §TOKEN-PERAACCT）。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        self.tmp = self._tmpdir.name

    def tearDown(self):
        self._tmpdir.cleanup()

    def test_token_absent_from_every_output_channel(self):
        fx = _Fixture(self.tmp)
        # 通道一：真实失败路径（抛异常）——异常文本最容易顺手 str(e) 进日志
        def boom(url, token, body):
            raise OSError("connection refused")
        log_stream = io.StringIO()
        handler = logging.StreamHandler(log_stream)
        lg = logging.getLogger("qmt_gateway.report_uplink")
        lg.addHandler(handler)
        old_stdout, old_stderr = sys.stdout, sys.stderr
        out_buf, err_buf = io.StringIO(), io.StringIO()
        try:
            with contextlib.redirect_stdout(out_buf), contextlib.redirect_stderr(err_buf):
                u = fx.uplink(boom)
                u.run_once()
                # 通道二：成功路径 + 状态落盘
                u2 = fx.uplink(_OkOpener())
                u2.run_once()
        finally:
            sys.stdout, sys.stderr = old_stdout, old_stderr
            lg.removeHandler(handler)
        sinks = {"log": log_stream.getvalue(), "stdout": out_buf.getvalue(),
                 "stderr": err_buf.getvalue()}
        for name, text in sinks.items():
            self.assertNotIn(FAKE_TOKEN, text, "钥匙泄漏进 %s" % name)
        # 通道三：tmp 目录里本模块写出的每一个文件逐字节扫描（config.xt.json 是钥匙的**源**，排除）
        for root, _dirs, files in os.walk(self.tmp):
            for fn in files:
                p = os.path.join(root, fn)
                if os.path.abspath(p) == os.path.abspath(fx.config_path):
                    continue
                with open(p, "rb") as fh:
                    self.assertNotIn(FAKE_TOKEN.encode(), fh.read(), "钥匙泄漏进文件 %s" % p)

    def test_config_error_message_carries_no_content(self):
        bad = os.path.join(self.tmp, "broken.json")
        with open(bad, "w", encoding="utf-8") as fh:
            fh.write("{not json " + FAKE_TOKEN)
        with self.assertRaises(ValueError) as ctx:
            up.load_xt_config(bad)
        self.assertNotIn(FAKE_TOKEN, str(ctx.exception))


class TestStubServerEndToEnd(unittest.TestCase):
    """本机 127.0.0.1 桩服务器跑一次真 http.client 传输（urllib 真实腿，假地址）：
    验证 Bearer 头接线与 JSON 上线体，不留任何真实网络依赖。"""

    def setUp(self):
        import tempfile
        self._tmpdir = tempfile.TemporaryDirectory()
        received = []
        self.received = received

        class _Stub(BaseHTTPRequestHandler):
            def do_POST(self):
                n = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(n)
                # 闭包引用外层 setUp 的 received 列表：BaseHTTPRequestHandler 每个请求新建
                # 实例，绝不能在 do_POST 里用 self.received（那指的是 handler 自己）。
                received.append((self.headers.get("Authorization"),
                                 json.loads(body.decode("utf-8"))))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'{"ok":"1"}')

            def log_message(self, *a):  # 桩服务器闭嘴：测试输出里不该有访问日志
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), _Stub)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.fx = _Fixture(self._tmpdir.name)
        with open(self.fx.config_path, "r", encoding="utf-8") as fh:
            cfg = json.load(fh)
        port = self.server.server_address[1]
        cfg["report_url"] = "http://127.0.0.1:%d" % port
        with open(self.fx.config_path, "w", encoding="utf-8") as fh:
            json.dump(cfg, fh)

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self._tmpdir.cleanup()

    def test_real_urllib_posts_envelopes(self):
        u = up.ReportUplink(self.fx.config_path, now_fn=lambda: FIXED_NOW)
        rep = u.run_once()
        self.assertGreater(rep["sent"], 0)
        self.assertTrue(self.received, "桩服务器没有收到任何上报")
        for authh, body in self.received:
            self.assertEqual(authh, "Bearer " + FAKE_TOKEN)
            self.assertNotIn(FAKE_TOKEN, json.dumps(body))
        types = sorted({b.get("type") for _a, b in self.received})
        self.assertIn("positions", types)
        self.assertIn("account", types)
        self.assertIn("heartbeat", types)



class CommandLine(unittest.TestCase):
    """CLI 入口的用例（2026-11-04 补）。

    这一条腿写出来之后**没有任何调用点**：没有 CLI、没有 cron、没有执行器接线。
    一条只能被 import 的上行腿，在今晚的家用机测试里等于没有——所以入口和它的
    三条边界必须一起有测试：不发一个字节、不写状态、不泄一把钥匙。
    """

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="uplink-cli-")
        self.fx = _Fixture(self.tmp)

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def _run(self, argv):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = up.main(argv)
        return rc, buf.getvalue()

    def test_dry_run_builds_events_and_sends_nothing(self):
        rc, out = self._run(["--config", self.fx.config_path, "--once", "--dry-run"])
        self.assertEqual(rc, 0, out)
        self.assertIn("UPLINK_RESULT=DRYRUN sent=0 dryrun=1", out)
        self.assertIn("UPLINK_EVENTS n=", out)
        self.assertNotIn("kinds=none", out, "夹具里明明有持仓/委托/资产导出，事件数不该为零")

    def test_dry_run_writes_no_state_file(self):
        """不推进幂等基线：预览一轮之后，真跑那一轮仍然要把这批事件发出去。

        这条要是反了，"先看一眼"就会把当天读数吃掉——晚上预览一次、早上上线就什么都不发。
        """
        self._run(["--config", self.fx.config_path, "--once", "--dry-run"])
        self.assertFalse(os.path.exists(os.path.join(self.tmp, "uplink_state.json")),
                         "dry-run 写了状态文件：它会把自己看过的读数记成已上报")

    def test_cli_never_prints_the_token(self):
        rc, out = self._run(["--config", self.fx.config_path, "--once", "--dry-run"])
        self.assertNotIn(FAKE_TOKEN, out)
        self.assertNotIn(FAKE_TOKEN.replace("-", ""), out)
        self.assertIn("token_present=1", out, "只许报'有没有'，不许报'是什么'")

    def test_missing_exports_are_said_as_missing_not_as_empty(self):
        """missing != empty 的可读版本：缺件那一类必须点名，不能折成 0 事件。"""
        fx = _Fixture(tempfile.mkdtemp(prefix="uplink-cli-empty-"), with_positions=False)
        try:
            rc, out = self._run(["--config", fx.config_path, "--once", "--dry-run"])
            self.assertEqual(rc, 0, out)
            self.assertIn("UPLINK_SKIP kind=positions reason=file_not_found", out)
        finally:
            shutil.rmtree(fx.tmp, ignore_errors=True)

    def test_refuses_to_run_without_a_mode(self):
        """没给 --once/--dry-run 就拒跑：默认动作必须是"什么都不做"，不是"默默上一轮"。"""
        rc, out = self._run(["--config", self.fx.config_path])
        self.assertEqual(rc, 2)
        self.assertIn("reason=no_mode_given_use_once", out)

    def test_unconfigured_upstream_aborts_before_any_send(self):
        tmp = tempfile.mkdtemp(prefix="uplink-cli-nocfg-")
        cfg = os.path.join(tmp, "config.xt.json")
        with open(cfg, "w", encoding="utf-8") as fh:
            json.dump({"token": FAKE_TOKEN}, fh)  # 没有 report_url
        try:
            rc, out = self._run(["--config", cfg, "--once"])
            self.assertEqual(rc, 2, out)
            self.assertIn("reason=upstream_unconfigured", out)
            self.assertIn("host_present=0", out)
        finally:
            shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
