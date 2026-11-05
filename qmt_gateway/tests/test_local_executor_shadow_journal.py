# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_local_executor_shadow_journal.py — §CARRIER-SHADOW 执行器侧影子账本锁。

这本地账本是"移植规则必须与引擎对照跑满 ≥5 个交易日才准碰钱"这条纪律的**分子**：
以前 shadow 命中只打一行日志，落不了盘、上不了行，广州侧无论谁问"跑到第几天了"都只能
得到"不知道"，那枚"武装"按钮背后是零数据。所以本文件锁的全是**数的来路**：

  1. 同一枚意图当天重算 ⇒ 只有一行，first_ts 不动、judgement/last_ts 刷新。
     first_ts 是这一行在窗口里的身份；每轮都重置会把"跑了几天"越跑越少。
  2. 金额/判断腿读数拿不到 ⇒ 存 NULL，不存 0。0 在对照表上是"定权说了零仓位"这个**事实**，
     而 NULL 才是"这一版载体没接线"这个**缺口**（missing≠empty）。
  3. 按 (day, short_code) 分日：换交易日不复活、不累计（与全仓 day-keyed 纪律同源）。
  4. shadow 档**结构上不碰钱**：一条委托都不发，但也**不许**被折进 skipped 计数里消失。
  5. 判断腿缺席 ⇒ sizing_judgement 返回空 dict；腿在而抛异常 ⇒ 记下 judgement_error，
     两种"没有读数"的修法不同，不许混成一条空记录。
  6. 探针（probe）三态不许互相冒充，而且**问问题本身不许改变答案**：
     用 LocalState(path) 去问"账本在不在"会顺手把表建出来，下一次就问出"空表"了。

全部离线：不需要 xtquant、不需要 QMT、不需要网络。
English: locks for the carrier-side shadow journal — the numerator of the 5-day comparison.
"""

import json
import os
import sqlite3
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
sys.path.insert(0, GW_DIR)

import local_executor as lex  # noqa: E402

DAY = "20261105"


def _signal(short="DR600000", kind="dragon_return", side="buy", qty=300, price=10.0,
            amount=None, symbol="600000.SH"):
    """一条影子判定（与 make_signal 的形状一致：卖出常不带金额）。"""
    return {"short_code": short, "kind": kind, "side": side, "qty": qty,
            "price": price, "amount": amount, "symbol": symbol}


class TestShadowJournalRows(unittest.TestCase):
    """1/2/3：行的身份、NULL 语义与分日账。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.tmp.name, "state.db")
        self.state = lex.LocalState(self.path)

    def tearDown(self):
        self.tmp.cleanup()

    def test_same_intent_same_day_is_one_row_and_keeps_first_ts(self):
        self.state.record_shadow(DAY, _signal(qty=300, price=10.0),
                                 judgement={"market_scale": 0.6})
        first = self.state.shadow_rows(DAY)[0]
        # 同一枚短码在下一轮被重算：这是一条**更新**，不是新的一天、也不是新的一行
        rows_before = len(self.state.shadow_rows())
        reconnected = lex.LocalState(self.path)
        reconnected.record_shadow(DAY, _signal(qty=500, price=9.7),
                                  judgement={"market_scale": 0.35})
        rows = reconnected.shadow_rows(DAY)
        self.assertEqual(len(rows), 1, "同一意图同一天写成了两行：窗口分子被灌水")
        self.assertEqual(len(reconnected.shadow_rows()), rows_before)
        self.assertEqual(rows[0]["first_ts"], first["first_ts"],
                         "first_ts 被后续轮次改写：那一天会被反复往前推")
        self.assertGreaterEqual(rows[0]["last_ts"], first["first_ts"])
        # 刷新的是判定内容，不是行身份
        self.assertEqual(rows[0]["qty"], 500)
        self.assertEqual(json.loads(rows[0]["judgement"])["market_scale"], 0.35)

    def test_uncomputable_values_stay_null_not_zero(self):
        """卖出判定没有金额、判断腿没接线 ⇒ 落 NULL。

        这两枚如果落成 0.0，广州对照表读出来的是"载体说这笔值 0 元 / 定权给的是零仓位"，
        于是缺口被记成一条测量结果——本仓反复踩的那条线（missing≠empty）在这一列上最容易破。
        """
        self.state.record_shadow(DAY, _signal(side="sell", amount=None))
        row = self.state.shadow_rows(DAY)[0]
        self.assertIsNone(row["amount"], "缺金额被写成了 0")
        self.assertEqual(row["judgement"], "", "判断腿缺席被写成了一个空读数而不是缺口")
        conn = sqlite3.connect(self.path)
        try:
            raw = conn.execute("SELECT amount FROM shadow_decisions WHERE day = ?",
                               (DAY,)).fetchone()[0]
        finally:
            conn.close()
        self.assertIsNone(raw, "列上写成了 0.0：SQL 层就把它当成事实了")

    def test_days_do_not_revive_or_merge(self):
        self.state.record_shadow(DAY, _signal())
        self.state.record_shadow("20261106", _signal())
        days = dict(self.state.shadow_days())
        self.assertEqual(sorted(days), ["20261105", "20261106"])
        self.assertEqual(days["20261105"], 1)
        self.assertEqual(days["20261106"], 1)
        self.assertEqual(len(self.state.shadow_rows("20261106")), 1)

    def test_days_ledger_is_the_no_report_detector(self):
        """没报过的那一天**不出现**在 shadow_days 里——这就是"静默日"与"零命中日"的分界。

        对照表把 no_report 与 no_hits 分开判（internal/server/carrier_shadow.go），
        它的输入就是这本账的形状：如果空手而归也留一行，5 天窗口会被一串静默日凑满。
        """
        self.state.record_shadow(DAY, _signal())
        days = dict(self.state.shadow_days())
        self.assertNotIn("20261107", days, "从没跑过的一天出现在天数账里：静默被算成了数据")
        self.assertEqual(len(self.state.shadow_rows("20261107")), 0)

    def test_unserialisable_judgement_keeps_the_decision(self):
        """判断串坏掉只损失解释，不损失判定：行还在，并且行里写着 judgement_error。

        夹具用的是自引用 dict（json 会抛 ValueError: Circular reference detected）——
        default=str 什么都能串，普通怪对象进不去这个分支，拿它当夹具会测出一个假绿。
        """
        cyclic = {}
        cyclic["self"] = cyclic
        self.state.record_shadow(DAY, _signal(), judgement=cyclic)
        row = self.state.shadow_rows(DAY)[0]
        blob = json.loads(row["judgement"])
        self.assertEqual(blob.get("judgement_error"), "unserialisable")


class TestSizingJudgementLeg(unittest.TestCase):
    """5：判断腿"缺席"与"坏了"是两回事，读数必须能分开。"""

    def tearDown(self):
        lex._PORTED_LEGS.pop("sizing_readout", None)

    def test_missing_leg_returns_empty_dict_not_zero(self):
        lex._PORTED_LEGS.pop("sizing_readout", None)
        self.assertEqual(lex.sizing_judgement(lex.PARAMS, {}), {},
                         "判断腿没接线却给出了读数（哪怕是 0）")

    def test_typed_keys_are_mapped_and_absent_values_stay_absent(self):
        def fake(params, ctx):
            return {"market_scale": 0.6, "target": None, "hard_block": True,
                    "emotion_phase": "choppy", "not_a_contract_key": 1}
        lex._PORTED_LEGS["sizing_readout"] = fake
        out = lex.sizing_judgement({"x": 1}, {"symbol": "600000.SH"})
        self.assertEqual(out["market_scale"], 0.6)
        self.assertNotIn("target", out, "None 被当成了读数以 0/None 形态上行")
        self.assertEqual(out["hard_block"], True)
        self.assertNotIn("not_a_contract_key", out,
                         "契约外的键被带进账本：上行侧与 golden 就会漂移")

    def test_raising_leg_is_recorded_as_error_not_as_silence(self):
        def boom(params, ctx):
            raise KeyError("market_scale_input")
        lex._PORTED_LEGS["sizing_readout"] = boom
        out = lex.sizing_judgement({}, {})
        self.assertIn("judgement_error", out)
        self.assertIn("KeyError", out["judgement_error"])


class TestShadowModeDoesNotSpend(unittest.TestCase):
    """4：shadow 档一条委托都不发——而这条腿本身也不能有钱的动作。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.state = lex.LocalState(os.path.join(self.tmp.name, "state.db"))

    def tearDown(self):
        self.tmp.cleanup()

    def test_run_round_journals_the_hit_and_places_nothing(self):
        placed = []

        class Broker(object):
            def orders_today(self):
                return []

            def positions(self):
                return {}

            def available_cash(self):
                return 100000.0

            def place_order(self, signal, remark):
                placed.append((signal, remark))
                return True, "1", "ok"

        params = dict(lex.PARAMS)
        params.update({
            "symbols": ["600000.SH"],
            "rule_arm": {"__test_shadow_rule": "shadow"},
            "signal_codes": dict(lex.PARAMS.get("signal_codes") or {}),
        })
        params["signal_codes"]["__test_shadow_rule"] = "TS"

        executor = lex.Executor(params, FakeDataFlat(), Broker(), self.state,
                                clock=lambda: lex.datetime(2026, 11, 5, 10, 15))
        # 注入一条测试用的移植规则（产物里由 load_ported_modules 注册，机制同一条）
        lex._PORTED_RULES["__test_shadow_rule"] = (
            lambda p, ctx: _signal(short="TS600000", kind="__test_shadow_rule",
                                   qty=300, price=10.0, amount=3000.0),
            "test")
        try:
            summary = executor.run_round()
        finally:
            lex._PORTED_RULES.pop("__test_shadow_rule", None)

        self.assertEqual(placed, [], "shadow 档发出了委托")
        self.assertEqual(summary["placed"], [])
        self.assertEqual(len(summary["shadow"]), 1,
                         "影子命中没进 summary：这一类被折进了别的计数里")
        self.assertEqual(summary["skipped"], [], "shadow 命中被记成了 skipped")
        rows = self.state.shadow_rows("20261105")
        self.assertEqual(len(rows), 1, "影子命中没落账：5 日窗口从此没有分子")
        self.assertEqual(rows[0]["short_code"], "TS600000")
        self.assertEqual(rows[0]["mode"], "shadow")

    def test_off_mode_is_skipped_and_never_journalled(self):
        """off 档连账都不记：记了就会在对照表里冒充一次"载体判过"。"""
        placed = []

        class Broker(object):
            def orders_today(self):
                return []

            def positions(self):
                return {}

            def available_cash(self):
                return 100000.0

            def place_order(self, signal, remark):
                placed.append((signal, remark))
                return True, "1", "ok"

        # 参数取烘好的 PARAMS 副本再改三处：symbols 只留一只（别轮的规则不许一起参与判定）、
        # rule_arm 把这条测试规则钉成 off、signal_codes 补上它需要的短码——短码缺失时
        # make_signal 会直接返回 None，那这条用例就什么都没测到，所以必须登记。
        params = dict(lex.PARAMS)
        params.update({"symbols": ["600000.SH"], "rule_arm": {"__test_off_rule": "off"},
                       "signal_codes": dict(lex.PARAMS.get("signal_codes") or {})})
        params["signal_codes"]["__test_off_rule"] = "TF"
        executor = lex.Executor(params, FakeDataFlat(), Broker(), self.state,
                                clock=lambda: lex.datetime(2026, 11, 5, 10, 15))
        lex._PORTED_RULES["__test_off_rule"] = (
            lambda p, ctx: _signal(short="TF600000", kind="__test_off_rule",
                                   qty=300, price=10.0, amount=3000.0),
            "test")
        try:
            summary = executor.run_round()
        finally:
            lex._PORTED_RULES.pop("__test_off_rule", None)
        self.assertEqual(placed, [])
        self.assertEqual(self.state.shadow_rows("20261105"), [],
                         "off 档的判定进了账：窗口会被没在跑的规则填满")
        self.assertEqual([s["reason"] for s in summary["skipped"]], ["rule_not_armed"])


class TestShadowJournalProbe(unittest.TestCase):
    """6：三态分开，而且探针自己不许改变被探的东西。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = self.tmp.name

    def tearDown(self):
        self.tmp.cleanup()

    def test_missing_file_is_missing_and_stays_missing(self):
        path = os.path.join(self.root, "nope.db")
        probe = lex.shadow_journal_probe(path)
        self.assertEqual(probe["state"], "missing")
        self.assertFalse(os.path.exists(path),
                         "探针为了回答问题先把答案写了出来（建了库）")

    def test_old_journal_reports_table_absent_not_empty(self):
        """11-05 之前的载体建的库：表没有。报"空"就成了"今天没有影子判定"，是假话。"""
        path = os.path.join(self.root, "old.db")
        conn = sqlite3.connect(path)
        try:
            conn.execute("CREATE TABLE sent_orders (day TEXT, short_code TEXT)")
            conn.commit()
        finally:
            conn.close()
        self.assertEqual(lex.shadow_journal_probe(path)["state"], "table_absent")

    def test_present_but_empty_is_a_real_measurement(self):
        path = os.path.join(self.root, "fresh.db")
        lex.LocalState(path)  # 新载体建的库：表在、零行
        probe = lex.shadow_journal_probe(path)
        self.assertEqual(probe["state"], "present")
        self.assertEqual(probe["rows"], 0)

    def test_unreadable_is_not_reported_as_empty(self):
        path = os.path.join(self.root, "broken.db")
        with open(path, "wb") as fh:
            fh.write(b"not a sqlite file")
        probe = lex.shadow_journal_probe(path)
        self.assertEqual(probe["state"], "unreadable")
        self.assertIn("reason", probe)


class FakeDataFlat(object):
    """一条都不会触发内置四规则的行情：现价=昨收、无量比异常、持仓为空。"""

    def get_full_tick(self, symbols):
        return {s: {"lastPrice": 10.0, "lastClose": 10.0, "volume": 1000000.0,
                    "askPrice": [10.0], "bidPrice": [10.0]} for s in symbols}

    def get_market_data_ex(self, field_list, stock_list, period):
        return {"close": {s: [10.0] for s in stock_list},
                "volume": {s: [500000.0, 500000.0] for s in stock_list}}

    def moving_average(self, symbol, window, period):
        return 10.0


if __name__ == "__main__":
    unittest.main()
