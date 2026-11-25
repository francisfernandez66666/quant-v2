# test_t_leg_wiring_20261125.py — T 腿接线 F1–F7 的锁（2026-11-25）。
#
# 背景（全是 2026-11-24 盒上实读，不是推测）：255 轮 T 腿、零笔委托，逐条倒下的位置是：
#   * `step0 equity_or_holdings_value_unavailable` 46/58 轮 —— 分母两条兜底都在，缺的是归因；
#   * `strategic_room_or_frozen_cash_or_unavailable` —— 新腿分支走的是**裸 base_ctx**，
#     `context_for` 一次都没调用 ⇒ `t_rounds_used`（回合帽）与 `t_frozen_cash`（T 冻结现金）
#     两个键根本不在 ctx 里，于是"回合帽永不计数、买入腿永远被拒"；
#   * `vwap_untrustworthy` 修好之后改为倒向 `volume_ratio_unavailable` / `amplitude`——
#     因为 `base_ctx` 从来没带 `prev_close` / `high` / `low` / `volume_ratio` 四个 tick 字段；
#   * `cost_gate_missing_expected_spread` —— `expected_spread_pct` 无生产者。
#
# 本文件锁两层：
#   1. **结构层**：那几处接线必须还在（改回裸 base_ctx / 改回写死 0 / 去掉 anchored= 都会红）；
#   2. **行为层**：真的跑一遍 `_run_t`（用构建出来的产物，不是模板），检查它交给
#      `t_round_state` 的那个 ctx 里**真的有**这些读数——结构锁只能证明"写了"，
#      行为锁才能证明"送到了"。
#
# 另外 F3（上市天数 A/B 档）与 F7（分母三态）只吃 PARAMS + ctx，直接把产物里的纯函数
# 拉出来喂输入即可，不需要柜台。
#
# English: structural + behavioural locks for the T-leg wiring (F1–F7): the new-leg
# branch goes through TRoundBook.context_for, base_ctx carries the four tick fields,
# the cost gate gets a designed spread, the T-leg money is no longer a hardcoded zero,
# and the two denominator readings name their source.
import importlib.util
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
REPO = os.path.dirname(GW_DIR)
sys.path.insert(0, GW_DIR)

import rules_t as rt  # noqa: E402

TEMPLATE = os.path.join(GW_DIR, "qmt_internal_strategy.py")
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")
PARAMS_PATH = os.path.join(GW_DIR, "local_executor_params.json")

# 固定时刻：连续竞价中段。用真实钟会让"14:52 之后收摊腿真的撤单"那一类断言随时刻漂移
# （§BOX-FIX 的教训：注入固定时刻不是放松断言）。
MOMENT = datetime(2026, 11, 25, 10, 30)
DAY = "20261125"
SYMBOL = "600000.SH"


def _read(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()


def _func_body(source, name):
    """Source lines of one top-level def, up to the next top-level def."""
    lines = source.splitlines()
    start = None
    for i, line in enumerate(lines):
        if line.startswith("def %s(" % name):
            start = i
            break
    if start is None:
        raise AssertionError("function not found: %s" % name)
    out = [lines[start]]
    for line in lines[start + 1:]:
        if line.startswith("def "):
            break
        out.append(line)
    return "\n".join(out)


def build_artifact(out_dir):
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox", "--out-dir", out_dir],
                          capture_output=True, text=True, cwd=REPO)
    path = ""
    for token in (proc.stdout + proc.stderr).replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    return proc.returncode, proc.stdout + proc.stderr, path


def load_module(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _real_params():
    with open(PARAMS_PATH, encoding="utf-8") as fh:
        return dict(json.load(fh)["params"])


class StructureLocks(unittest.TestCase):
    """1：接线还在（改回旧形状必红）。"""

    @classmethod
    def setUpClass(cls):
        cls.src = _read(TEMPLATE)
        cls.run_t = _func_body(cls.src, "_run_t")

    def test_new_leg_branch_goes_through_context_for(self):
        # F1：新腿分支必须在 `t_round_state(PARAMS, ctx, None)` 之前调 context_for。
        # 反例（11-24 的真实形状）：`ctx = base_ctx(symbol)` 紧接着 `t_round_state(...)`。
        body = self.run_t
        tail = body.index("t_round_state(PARAMS, ctx, None)")
        window = body[:tail].splitlines()[-8:]
        self.assertTrue(any("book.context_for(" in line for line in window),
                        "新腿分支没走 context_for：回合帽与 T 冻结现金会一起缺席")
        lines = body.splitlines()
        for i, line in enumerate(lines[:-1]):
            if line.strip() == "ctx = base_ctx(symbol)":
                self.assertNotIn("t_round_state(", lines[i + 1],
                                 "裸 base_ctx 直接喂闸门（F1 之前的形状）不许回潮")

    def test_in_flight_branch_also_merges_the_whole_ctx(self):
        # 旧形状 `ctx["positions_live"] = book.context_for(ctx, symbol)` 把"书里的读数"
        # 写进了 positions 表 ⇒ 在途回合拿到一张不是持仓表的持仓表。
        self.assertIn("ctx = book.context_for(base_ctx(symbol), symbol)", self.run_t)
        self.assertNotIn('ctx["positions_live"] = book.context_for(', self.run_t)

    def test_base_ctx_carries_the_four_tick_fields(self):
        # F2：方向门/振幅门/量比门读的就是这四个键。
        for key in ('"prev_close"', '"high"', '"low"', '"volume_ratio"'):
            self.assertIn(key, self.run_t, "base_ctx 少了 %s" % key)

    def test_expected_spread_comes_from_the_param(self):
        # F4：设计价差只许来自 PARAMS，不许就地用 |price-vwap| 造一个（那是入场门）。
        self.assertIn('"expected_spread_pct": PARAMS.get("t_min_spread_pct")', self.run_t)
        cost_gate = _func_body(_read(os.path.join(GW_DIR, "rules_t.py")), "t_cost_gate")
        self.assertNotIn('abs(', cost_gate, "t_cost_gate 自己算价差＝第二本账")

    def test_sizing_view_no_longer_hardcodes_the_t_leg_money(self):
        # F5：写死 0 会让战略 room 偏大（同一笔钱被两条腿各花一次）。
        view = _func_body(self.src, "_sizing_view")
        self.assertIn('out["t_buy_leg_value"] = _t_leg_value("buy"', view)
        self.assertIn('out["t_sell_leg_value"] = _t_leg_value("sell"', view)

    def test_readouts_name_anchored_and_denominator_sources(self):
        # F6：三条读数。缺一条，"T 腿看起来没跑"与"跑了但一只票都没锚"就同形。
        self.assertIn("anchored=%d/missing=%d", self.run_t)
        self.assertIn("t_denom equity=", self.run_t)
        self.assertIn("src=%s/%s", self.run_t)
        self.assertIn("t_round anchored=0/missing=%d reason=no_provable_anchor", self.run_t)

    def test_no_literal_tables_leak_into_the_template(self):
        # F5 的四个"状态类"键必须来自读数函数，不是就地字面量。
        for name in ("_strategic_busy_for", "_t_opposite_leg_blocked",
                     "_strategic_room_of", "_discipline_stop_loss_pct"):
            self.assertIn("def %s(" % name, self.src, "%s 不在了：F5 的读数没有生产者" % name)


class BehaviourHarness(unittest.TestCase):
    """2：真跑一遍 `_run_t`，看它交出去的 ctx 里有没有这些读数。"""

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_artifact(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.art = load_module(path, "t_leg_wiring_artifact")

    @classmethod
    def tearDownClass(cls):
        # 测试夹具只活在这一次 pytest：sandbox 产物与临时库必须回收，
        # 否则 /tmp 会攒下一堆没人认领的旧策略副本（测试数据清理）。
        shutil.rmtree(cls.dir, ignore_errors=True)

    def setUp(self):
        art = self.art
        self.lines = []
        self.captured = []
        # PARAMS：真参数文件 + 三份烘参（锚点/宏观/止盈计划）。真参数文件保证
        # 阈值的"唯一家"仍是 Go 常量，这里一个数字都不新造。
        art.PARAMS = dict(_real_params())
        art.PARAMS.update({
            "t_enabled": True,
            "t_shadow": False,
            "t_signal_codes": {"t_buy_first": "TB", "t_buy_first_pair": "TS",
                               "t_sell_first": "XB", "t_sell_pair": "XS"},
            "t_anchor_book": {"600000": {"anchor_price": 10.0,
                                         "anchor_source": "cost_fallback"}},
            "t_macro": {"market_scale": 0.80, "index_change_pct": 0.30,
                        "market_state": "range", "market_state_age_sec": 60.0,
                        "total_asset": 100000.0, "asof_age_sec": 60.0},
            "precalc": {"600000": {"ma_ref": 10.0, "vol_ma5": 1000000.0,
                                   "listed_days": 900, "float_shares": 100000000.0,
                                   "prev_close": 10.0}},
            "tp_plan": {DAY: {"600000": {"strategy": "dragon", "open_date": DAY,
                                         "stage": 0, "manual": False, "tp_pct": 30.0,
                                         "open_price": 10.0, "peak": 10.5}}},
        })
        art._STATE.clear()
        # 柜台的 ACCOUNT 表在测试里不存在：把总资产钉成 10 万，因为 F5 的
        # strategic_room 走的就是「总资产 × 目标比例」这条链，缺分母会返回 None
        # （None 是正确的 fail-closed，但那样就测不到 room 被真的算出来了）。
        art._account_total_asset = lambda: 100000.0
        art._warn_once = lambda key, text: None
        art._log = lambda text: self.lines.append(text)
        art.t_round_state = lambda params, ctx, rnd=None: (
            self.captured.append(ctx) or {"action": "hold", "reason": "captured"})
        art._tick_of = lambda ci, sym: {
            "last_price": 10.20, "prev_close": 10.00, "high": 10.50, "low": 9.80,
            "volume": 2000000.0, "amount": 20400000.0, "open": 10.00,
        }

    def _run(self, positions=None, orders=None, deals=None, total=98000.0):
        positions = positions if positions is not None else {
            SYMBOL: {"qty": 1000, "can_use": 1000, "cost": 10.0}}
        self.art._run_t(object(), MOMENT, orders or [], positions, deals or [],
                        50000.0, total, DAY, {})
        return self.captured

    def test_base_ctx_reaches_the_gate_with_every_reading(self):
        ctx = self._run()[0]
        # F2：四个 tick 字段（量比来自 _ref_and_volume → precalc.vol_ma5，2.0 倍）。
        self.assertEqual(ctx["prev_close"], 10.0)
        self.assertEqual(ctx["high"], 10.5)
        self.assertEqual(ctx["low"], 9.8)
        self.assertAlmostEqual(ctx["volume_ratio"], 2.0, places=6)
        # F1：context_for 那三样（回合帽/冻结现金/当日成交量）真的在。
        self.assertEqual(ctx["t_rounds_used"], 0)
        self.assertEqual(ctx["t_frozen_cash"], 0.0)
        self.assertFalse(ctx["t_has_open_leg"])
        # F4：设计价差来自参数。
        self.assertEqual(ctx["expected_spread_pct"], 2)
        # F5：四个状态类键 + 战法止盈点。
        self.assertFalse(ctx["strategic_busy"])
        self.assertFalse(ctx["opposite_leg_blocked"])
        self.assertIsNotNone(ctx["strategic_room"])
        self.assertAlmostEqual(ctx["discipline_stop_loss_pct"], 6.0, places=6)
        self.assertEqual(ctx["discipline_take_profit_pct"], 30.0)
        # F3：上市天数与覆盖率一起送到（覆盖率 100% ⇒ B 档）。
        self.assertEqual(ctx["listed_days"], 900)
        self.assertAlmostEqual(ctx["listed_days_coverage_pct"], 100.0, places=6)
        # orders/fills 真的带上了（_leg_aggregate 就靠它们对账在途腿）。
        self.assertEqual(ctx["orders_today"], [])
        self.assertEqual(ctx["fills_today"], [])

    def test_readout_lines_are_printed_every_round(self):
        self._run()
        joined = "\n".join(self.lines)
        self.assertIn("t_denom equity=", joined)
        self.assertIn("src=counter/cost", joined)  # 柜台行没带 market_value ⇒ 成本腿
        self.assertIn("t_round actions=", joined)
        self.assertIn("anchored=1/missing=0", joined)

    def test_no_anchor_prints_a_line_instead_of_returning_silently(self):
        # F6：`if not pos_view: return` 以前一行都不打 —— 这是"看起来像没跑"的第二个成因。
        self.art.PARAMS["t_anchor_book"] = {}
        self.assertEqual(self._run(), [])
        joined = "\n".join(self.lines)
        self.assertIn("anchored=0/missing=1", joined)
        self.assertIn("no_provable_anchor", joined)

    def test_t_leg_money_is_published_for_the_strategic_chain(self):
        # F5：`_run_t` 每轮写一次快照（含 0），战略腿读它；跨日快照读成 0。
        from types import SimpleNamespace
        book = SimpleNamespace(rounds={
            "r1": {"symbol": SYMBOL, "day": DAY, "status": rt.T_STATE_OPEN,
                   "direction": rt.T_DIR_BUY_FIRST, "buy_filled": 1000, "buy_price": 9.9}})
        snap = self.art._t_leg_values(book, DAY, lambda sym: 10.0)
        self.assertEqual(snap, {"day": DAY, "buy": 10000.0, "sell": 0.0})
        self.art._STATE["t_leg_snapshot"] = snap
        self.assertEqual(self.art._t_leg_value("buy", DAY), 10000.0)
        self.assertEqual(self.art._t_leg_value("sell", DAY), 0.0)
        # 跨日：昨天的腿不许把今天的现金永久占住。
        self.assertEqual(self.art._t_leg_value("buy", "20261126"), 0.0)

    def test_denominator_attribution_three_states(self):
        # F7：三态各一条。`none` 是"一条腿都给不出数"，与"数很小"是两件事。
        basis = self.art._t_equity_basis_src
        self.assertEqual(basis(98000.0, {}, 900.0), (98000.0, "counter"))
        self.assertEqual(basis(None, {"total_asset": 99000.0, "asof_age_sec": 60.0}, 900.0),
                         (99000.0, "baked"))
        self.assertEqual(basis(None, {}, 900.0), (None, "none"))
        # 过期烘参不许当分母（fail-closed 的方向）。
        self.assertEqual(basis(None, {"total_asset": 99000.0, "asof_age_sec": 4000.0}, 900.0),
                         (None, "none"))
        hv = self.art._holdings_value_src
        self.assertEqual(hv({SYMBOL: {"qty": 100, "market_value": 1234.0}}, lambda s: 0.0),
                         (1234.0, "mv"))
        self.assertEqual(hv({SYMBOL: {"qty": 100, "cost": 10.0}}, lambda s: 0.0),
                         (1000.0, "cost"))
        self.assertEqual(hv({SYMBOL: {"qty": 100}}, lambda s: 11.0), (1100.0, "last"))
        self.assertEqual(hv({SYMBOL: {"qty": 100}}, lambda s: None), (0.0, "none"))


class GateEndToEnd(unittest.TestCase):
    """F3/F4 的闸门行为：用真参数文件跑 `t_gate_open`，看它是否真的走到 open。"""

    def setUp(self):
        self.params = _real_params()
        self.params.update({"t_enabled": True, "t_shadow": False})

    def _ctx(self, **over):
        base = {
            "symbol": SYMBOL, "day": DAY, "hhmm": "10:30", "trading_day": True,
            "last_price": 9.85, "prev_close": 10.0, "high": 10.4, "low": 9.8,
            "volume": 4000000.0, "amount": 40000000.0, "name": "SP BANK",
            "turnover_pct": 2.0, "listed_days": 900, "volume_ratio": 1.2,
            "market_scale": 0.6, "index_change_pct": 0.5, "market_state": "range",
            "market_state_age_sec": 60.0, "sizing_missing_dims": [],
            "total_asset": 200000.0, "holdings_value": 100000.0,
            "available_cash": 50000.0, "strategic_room": 10000.0,
            "t_frozen_cash": 0.0, "t_sell_notional": 0.0, "t_rounds_used": 0,
            "t_has_open_leg": False, "fresh_buy_signal_id": "sig-1",
            "consumed_signal_ids": [], "expected_spread_pct": 2.0,
            "today_realized_pnl": 0.0, "discipline_stop_loss_pct": 6.0,
            "discipline_take_profit_pct": 30.0,
            "positions_live": {SYMBOL: {"qty": 1000, "can_use_qty": 1000, "cost": 10.0,
                                        "anchor_price": 10.0,
                                        "anchor_source": "cost_fallback"}},
            "orders_today": [], "fills_today": [],
        }
        base.update(over)
        return base

    def test_gate_open_reaches_an_open(self):
        # F11：一整套读数齐了就必须到 open，不是任何一个 step* 弃权。
        allowed, reason = rt.t_gate_open(self.params, self._ctx())
        self.assertTrue(allowed, reason)
        self.assertIn("buy_first", reason)
        action = rt.t_round_state(self.params, self._ctx(), None)
        self.assertEqual(action["action"], rt.ACTION_OPEN, action.get("reason"))
        self.assertGreater(action.get("qty") or 0, 0)

    def test_cost_line_prints_the_designed_spread(self):
        # F4：两个数都在 ⇒ 成本线是有读数的判据。要求 2% 的价差时应当放行。
        ok, _why, detail = rt.t_cost_gate(self.params, self._ctx(t_plan_qty=1000))
        self.assertTrue(ok)
        self.assertLess(detail["required_spread_pct"], detail["expected_spread_pct"])

    def test_listed_days_tiers_use_the_real_params_file(self):
        # F3：真参数文件是 A 档的出厂态（覆盖 0 ⇒ 闸关），但固定门读得到。
        self.assertTrue(self.params["t_listed_days_auto"])
        self.assertEqual(self.params["t_listed_days_coverage_min_pct"], 95)
        self.assertEqual(self.params["t_min_listed_days"], 60)
        floor, tag = rt.t_listed_days_floor(self.params, self._ctx(listed_days_coverage_pct=0.0))
        self.assertEqual(floor, 0.0)
        self.assertIn("off(auto", tag)


if __name__ == "__main__":
    unittest.main()
