# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_carrier_t_leg.py — 做 T 判断腿接进载体（2026-11-05）。

AGENTS 从 11-04 起一直挂着一条"未做"：`rules_t` / `rules_sizing` 的**调用点**没接进两个载体——
判断腿算得出数，但没人问它。sizing 那半今天前已接（`_PORTED_LEGS["sizing_readout"]`），
T 这半的实情更糟：`rules_t.py` 连 `JUDGEMENT_LEGS` 都没有声明，所以构建器那套
"显式绑名 + g7e 验 callable"的机制**结构上不可能**接上它——不是忘了调一行，是链条缺一环。

本文件锁四件事，全部是"接线是否真在"的形状：
  1. 腿**自己报**它暴露哪个读数（声明权在模块，不在构建器写一份清单＝第二本账）；
  2. 读数只**汇闸门**，不新造判据：`t_enabled=false` 必须回 `step0 t_disabled`，
     这句话只有真的转调 `t_gate_open` 才说得出（写死的桩说不出来）；
  3. 非 ASCII 的原因串一律**丢弃**而不是转码——那台机器码页 936，转码会得到一排 '?'，
     而 '?' 在对照账上看起来像一条正常读数（本仓反复踩的永久假绿）；
  4. 判断腿要的是**账户视图**：直接把每票 ctx 喂给它，所有账户级闸门都会弃权，
     于是"腿接上了"和"腿永远说不"在读面上分不出来。
English: locks for the carrier-side T judgement leg — declared by the module, composed from the
existing gates, ASCII-only reasons, and fed an account view so abstention means something.
"""

import os
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
sys.path.insert(0, GW_DIR)

import local_executor as lex  # noqa: E402
import rules_t  # noqa: E402


def real_params():
    """烘进来的真参数文件：判断腿吃的阈值一律来自它，测试不自带一份。"""
    import json
    with open(os.path.join(GW_DIR, "local_executor_params.json"), encoding="utf-8") as fh:
        return dict(json.load(fh)["params"])


def ctx(**over):
    base = {"symbol": "600000.SH", "day": "20261105", "hhmm": "10:15", "trading_day": True,
            "last_price": 9.9, "prev_close": 10.0, "vwap": 10.0, "volume": 1000000.0,
            "volume_ratio": 2.0, "position_qty": 1000, "available_cash": 50000.0,
            "daily_buy_total": 0.0, "ops_last_minute": 0}
    base.update(over)
    return base


class TestLegIsDeclaredByTheModule(unittest.TestCase):
    def test_rules_t_declares_its_own_leg(self):
        self.assertEqual(rules_t.JUDGEMENT_LEGS, ["t_readout"],
                         "声明权漂到构建器就是第二本账（sizing 那条同规）")

    def test_declared_leg_actually_exists_and_is_callable(self):
        for name in rules_t.JUDGEMENT_LEGS:
            self.assertTrue(callable(getattr(rules_t, name, None)),
                            "声明了 %s 但模块里没有它：g7e 会在构建期判红，这里先红一次" % name)

    def test_rules_module_stays_pure_ascii(self):
        # 这文件会被内联进按 GBK 解源码的产物：一个非 ASCII 字节就能让客户端加载失败
        with open(os.path.join(GW_DIR, "rules_t.py"), "rb") as fh:
            raw = fh.read()
        self.assertEqual([i for i, b in enumerate(raw) if b > 127], [],
                         "rules_t.py 里有非 ASCII 字节（g1 会拒建，这里提前红）")


class TestReadoutComposesExistingGates(unittest.TestCase):
    """2/3：读数只汇闸门，且原因串要么 ASCII 要么没有。"""

    def test_disabled_switch_reports_the_real_gate_reason(self):
        p = real_params()
        p["t_enabled"] = False
        out = rules_t.t_readout(p, ctx())
        self.assertFalse(out["t_open_allowed"])
        self.assertIn("t_disabled", out.get("t_block_reason", ""),
                      "没有走真闸门链的读数说不得出这句——写死的桩会在这里露馅")

    def test_direction_comes_from_the_vwap_gate_not_a_copy(self):
        p = real_params()
        below = rules_t.t_readout(p, ctx(last_price=9.5, vwap=10.0))
        above = rules_t.t_readout(p, ctx(last_price=10.5, vwap=10.0))
        self.assertTrue(below["t_dir_buy"] is True or below["t_dir_sell"] is True)
        self.assertNotEqual((below["t_dir_buy"], below["t_dir_sell"]),
                            (above["t_dir_buy"], above["t_dir_sell"]),
                            "方向与 VWAP 偏离的关系不再来自 t_direction")

    def test_no_number_in_returns_only_booleans_and_ascii(self):
        p = real_params()
        out = rules_t.t_readout(p, ctx())
        for key in ("t_open_allowed", "t_dir_buy", "t_dir_sell"):
            self.assertIsInstance(out[key], bool, "%s 必须是布尔：读数不许携带自造的数" % key)
        for key, val in out.items():
            if isinstance(val, str):
                self.assertTrue(val.isascii(), "原因串非 ASCII：%s=%r" % (key, val))

    def test_non_ascii_reason_is_dropped_not_transcoded(self):
        """非 ASCII 的原因串一律丢弃，不许转码：转码得到的问号在对照账上像一条正常读数。"""
        real = rules_t.t_gate_open
        rules_t.t_gate_open = lambda params, c: (False, "熔断 中文原因")
        try:
            out = rules_t.t_readout(real_params(), ctx())
        finally:
            rules_t.t_gate_open = real
        self.assertNotIn("t_block_reason", out,
                         "非 ASCII 原因被转码留下了（'??' 会被读成一条真读数）")
        self.assertIn("t_open_allowed", out, "丢掉解释不许连判定一起丢")


class TestAccountViewIsFed(unittest.TestCase):
    """4：没有账户视图时所有账户级闸门都弃权，那等于没接。"""

    def test_view_derives_asset_from_cash_and_book(self):
        view = lex._judgement_view(ctx(positions_live=None), {"600000.SH": {"qty": 1000, "cost": 9.0},
                                                              "600519.SH": {"qty": 100, "cost": 1500.0}},
                                   20000.0)
        # 有实时 tick 的那只用现价，其余按成本——这是 _judgement_view 写明的口径：
        # 低估空间允许，高估才是 Go 侧钉过的那条缺陷。
        self.assertAlmostEqual(view["holdings_value"], 1000 * 9.9 + 100 * 1500.0, places=2)
        self.assertAlmostEqual(view["total_asset"], view["holdings_value"] + 20000.0, places=2)
        self.assertEqual(len(view["positions_live"]), 2)

    def test_market_dims_are_not_invented_into_the_view(self):
        """市场维（情绪/广度/指数）不在这台盒子上，视图里就必须**没有**它们。"""
        view = lex._judgement_view(ctx(), {}, 1000.0)
        for key in ("emotion_phase", "market_state", "index_change_pct", "advancers", "regime"):
            self.assertNotIn(key, view, "%s 出现在视图里：那是造数，不是缺数" % key)

    def test_no_cash_means_no_total_asset(self):
        view = lex._judgement_view(ctx(available_cash=None), {}, None)
        self.assertNotIn("total_asset", view, "拿不到现金却给出总资产＝把缺口当事实")

    def test_disabled_leg_is_absent_not_zero(self):
        lex._PORTED_LEGS.pop("t_readout", None)
        self.assertEqual(lex.t_leg_judgement(real_params(), ctx()), {},
                         "腿没接线却给出了读数（哪怕是 0）")

    def test_bound_leg_produces_one_compact_key(self):
        lex._PORTED_LEGS["t_readout"] = rules_t.t_readout
        try:
            out = lex.t_leg_judgement(real_params(), lex._judgement_view(ctx(), {}, 1000.0))
        finally:
            lex._PORTED_LEGS.pop("t_readout", None)
        self.assertEqual(list(out.keys()), ["t_leg"], "五枚键压成一枚，才装得进 200 字节的说明列")
        self.assertIn("open_allowed=", out["t_leg"])
        self.assertTrue(out["t_leg"].isascii())

    def test_raising_leg_is_recorded_as_broken(self):
        def boom(params, view):
            raise KeyError("t_quota_denominator")
        lex._PORTED_LEGS["t_readout"] = boom
        try:
            out = lex.t_leg_judgement(real_params(), ctx())
        finally:
            lex._PORTED_LEGS.pop("t_readout", None)
        self.assertIn("t_leg_error", out["t_leg"],
                      "「腿在但坏了」与「腿不在」必须是两条不同读数")


class TestShadowRoundCarriesTheLeg(unittest.TestCase):
    """整轮：shadow 命中的那行账本里要真的带着 T 读数。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.state = lex.LocalState(os.path.join(self.tmp.name, "state.db"))

    def tearDown(self):
        lex._PORTED_RULES.pop("__t_leg_rule", None)
        lex._PORTED_LEGS.pop("t_readout", None)
        self.tmp.cleanup()

    def test_shadow_row_journals_the_t_reading(self):
        class Broker(object):
            def orders_today(self):
                return []

            def positions(self):
                return {}

            def available_cash(self):
                return 50000.0

            def place_order(self, signal, remark):
                raise AssertionError("shadow 档不许发单")

        p = real_params()
        p["symbols"] = ["600000.SH"]
        p["rule_arm"] = {"__t_leg_rule": "shadow"}
        p["signal_codes"] = dict(p.get("signal_codes") or {})
        p["signal_codes"]["__t_leg_rule"] = "TT"
        lex._PORTED_RULES["__t_leg_rule"] = (
            lambda params, c: {"short_code": "TT600000", "kind": "__t_leg_rule",
                               "side": "buy", "symbol": "600000.SH", "qty": 300,
                               "price": 9.9, "amount": 3000.0}, "test")
        lex._PORTED_LEGS["t_readout"] = rules_t.t_readout
        ex = lex.Executor(p, FlatData(), Broker(), self.state,
                          clock=lambda: lex.datetime(2026, 11, 5, 10, 15))
        summary = ex.run_round()
        self.assertEqual(len(summary["shadow"]), 1)
        rows = self.state.shadow_rows("20261105")
        self.assertEqual(len(rows), 1)
        self.assertIn("t_leg", str(rows[0]["judgement"]),
                      "T 判断腿没进影子账本：接了线但记不下数，等于还是没接")


class TestWireCarriesTheLeg(unittest.TestCase):
    """接了线还得能上行：T 读数走 sizing_note 那一列，Go 侧不加新字段（不加就要改 golden）。"""

    def test_note_maps_t_leg_into_sizing_note(self):
        import report_uplink as up
        note = up.shadow_judgement_note('{"market_scale":0.6,"target":0.35,'
                                        '"t_leg":"open_allowed=0 dir_buy=1 block_reason=step0_x"}')
        self.assertEqual(note["market_scale"], 0.6)
        self.assertIn("open_allowed=0", note["sizing_note"])
        self.assertIn("t_leg=", note["sizing_note"])

    def test_missing_leg_leaves_no_placeholder(self):
        import report_uplink as up
        note = up.shadow_judgement_note('{"market_scale":0.6}')
        self.assertNotIn("t_leg", note.get("sizing_note", ""),
                         "没有 T 读数时不许留一个空 t_leg 冒充已上报")


class FlatData(object):
    """一条都不触发内置四规则的行情（现价=昨收、无持仓），让移植规则被真的走到。"""

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
