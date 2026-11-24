# test_tp_ladder_20261124.py — §TP-LADDER 两段式止盈的行为锁（2026-11-24）。
#
# 现场（2026-11-24 上午，全部盒上实读）：002579 +13.8%、000678 +9.9%、000009 +6.4% 各自
# 被一条 `take_profit_ratio=1.05` 的**全平**腿在开盘三分钟内清仓；10:10 000797 又被破位腿
# 在 +2.2% 整仓清掉，只剩 1100 股孤仓。owner 九条裁决把这条腿改写成两段：
#   Stage 0 到各战法自己的止盈点卖**一半**；Stage 1 剩余的跟着**峰值**走，回吐到线再清；
#   没有战法止盈点的票走"峰值回撤"两档；龙头另加"封过板 + 开板 15 分钟不回封就清"。
#
# 本文件锁四件事（全部跑**构建出来的产物**，不读模板源码——模板对了产物仍可能错）：
#   1. 全平腿真的没了：命中只卖一半，`take_profit_ratio` 不在 PARAMS 里；
#   2. 阶梯的两种尺：有止盈点的按**百分点**（30→25），没有的按**相对峰值价回撤**（3%/5%）；
#      板宽（10cm/20cm/30cm）取各档阈值，顺序 BSE → wide → main；
#   3. 弃权方向：没有烘入计划（tp_known=false）、手动仓、Stage 0 判 TT —— 一律不动手，
#      绝不把"看不见"读成"没有止盈点"（那会立刻按回撤档卖）；
#   4. 盒内可证的龙头判据：今日封过板 + 末根不在板上 + 开板满 15 分钟；任一读数缺失 ⇒ None。
#
# English: behaviour locks for the two-stage take-profit ladder, exercised against the
# built sandbox artifact (a template-level grep cannot see the product).
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")
PARAMS_PATH = os.path.join(REPO, "qmt_gateway", "local_executor_params.json")

SYM = "600519.SH"
SYM_20CM = "300750.SZ"
SYM_30CM = "830799.BJ"
SYM_BJ92 = "920062.SH"
DAY = "20261124"


def _build(out_dir):
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox", "--out-dir", out_dir],
                          capture_output=True, text=True, cwd=REPO)
    path = ""
    for token in (proc.stdout + proc.stderr).replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    return proc.returncode, proc.stdout + proc.stderr, path


def _load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def ctx(**overrides):
    """A rule-layer context with sane defaults for a held, profitable position."""
    # 默认值＝持仓且浮盈（10.30 卖出那条腿最容易被误伤的位置）；用例只改自己关心的键。
    base = {
        "symbol": SYM,
        "day": DAY,
        "weekday": 1,
        "closed_days": [],
        "hhmm": "10:30",
        "trading_day": True,
        "last_price": 13.0,
        "prev_close": 12.0,
        "ref_price": 12.0,
        "volume_ratio": 2.0,
        "position_qty": 1000,
        "position_cost": 10.0,
        "tp_known": True,
        "tp_pct": 30,
        "tp_peak": 13.0,
        "tp_stage": 0,
        "tp_manual": False,
        "tp_dragon_open_no_reseal": None,
    }
    base.update(overrides)
    return base


class _Entry(object):
    """Minimal stand-in for the QMT market-data DataFrame entry."""

    def __init__(self, highs, closes, stamps):
        self.high = list(highs)
        self.close = list(closes)
        self.stime = list(stamps)


class TakeProfitLadderTest(unittest.TestCase):
    """The ladder's rules and the box-side readings, against the built artifact."""

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = _build(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.mod = _load(path, "sandbox_tp_ladder_under_test")
        cls.baked_params = dict(cls.mod.PARAMS)
        with open(PARAMS_PATH, encoding="utf-8") as fh:
            cls.params_doc = json.load(fh)

    def setUp(self):
        self.mod.PARAMS.clear()
        self.mod.PARAMS.update(self.baked_params)
        self.mod._STATE.clear()

    # ---- ① 全平腿没了 -----------------------------------------------------
    def test_flat_full_exit_is_gone(self):
        """`take_profit_ratio` must not exist and a hit must never clear the whole book."""
        params = self.mod.PARAMS
        self.assertNotIn("take_profit_ratio", params)
        self.assertNotIn("take_profit_ratio", self.params_doc["params"])
        self.assertIn("take_profit_clear", params["builtin_rules"])
        self.assertEqual(params["signal_codes"]["take_profit"], "TP")
        self.assertEqual(params["signal_codes"]["take_profit_clear"], "TT")
        hit = self.mod.rule_take_profit(params, ctx())
        self.assertEqual(hit["qty"], 500, "stage 0 sells HALF, never the whole position")

    # ---- ② 有止盈点的票：按百分点 -----------------------------------------
    def test_strategy_point_uses_points(self):
        params = self.mod.PARAMS
        self.assertIsNone(self.mod.rule_take_profit(
            params, ctx(last_price=12.99)), "+29.9% is short of a 30-point target")
        hit = self.mod.rule_take_profit(params, ctx(last_price=13.0))
        self.assertEqual(hit["kind"], "take_profit")
        self.assertEqual(hit["short_code"], "TP600519")

    def test_dragon_uses_its_own_thirty_points(self):
        """Paper's dragon take-profit is 10 points; the carrier leg is 30 (owner ruling 9)."""
        self.assertEqual(self.mod.PARAMS["tp_dragon_take_profit_pct"], 30)
        self.assertEqual(self.mod.PARAMS["tp_double_bump_take_profit_pct"], 15)
        self.assertEqual(self.mod.PARAMS["tp_dragon_return_take_profit_pct"], 25)

    # ---- ③ 没有止盈点的票：按峰值回撤 -------------------------------------
    def test_no_point_uses_peak_retrace(self):
        params = self.mod.PARAMS
        base = dict(tp_pct=None, position_cost=10.0, tp_peak=10.5)
        # (10.5-10.21)/10.5 = 2.76% < 3% -> hold
        self.assertIsNone(self.mod.rule_take_profit(
            params, ctx(last_price=10.21, **base)))
        # (10.5-10.15)/10.5 = 3.33% >= 3% -> sell half
        hit = self.mod.rule_take_profit(params, ctx(last_price=10.15, **base))
        self.assertEqual(hit["kind"], "take_profit")
        self.assertEqual(hit["qty"], 500)

    def test_no_point_needs_a_peak_above_cost(self):
        """`tp_peak_arm_pct` = 0 means "peak must at least stand on cost", not "always armed"."""
        params = self.mod.PARAMS
        self.assertIsNone(self.mod.rule_take_profit(
            params, ctx(last_price=9.5, position_cost=10.0, tp_pct=None, tp_peak=9.6)))

    def test_board_width_tables(self):
        """10cm 3/5, 20cm 5/8, 30cm 6/10 — and BSE wins over the wide table."""
        params = self.mod.PARAMS
        self.assertEqual(
            (params["tp_trail_half_retr_pct"], params["tp_trail_clear_retr_pct"]), (3, 5))
        self.assertEqual(
            (params["tp_trail_half_retr_pct_20cm"], params["tp_trail_clear_retr_pct_20cm"]), (5, 8))
        self.assertEqual(
            (params["tp_trail_half_retr_pct_30cm"], params["tp_trail_clear_retr_pct_30cm"]), (6, 10))
        for symbol in (SYM_20CM,):
            self.assertEqual(self.mod._tp_thresholds(params, symbol),
                             {"half": 5, "clear": 8}, symbol)
        for symbol in (SYM_30CM, SYM_BJ92):
            self.assertEqual(self.mod._tp_thresholds(params, symbol),
                             {"half": 6, "clear": 10}, symbol)
        self.assertEqual(self.mod._tp_thresholds(params, SYM), {"half": 3, "clear": 5})

    def test_twenty_centimeter_clear_line(self):
        params = self.mod.PARAMS
        base = dict(symbol=SYM_20CM, tp_pct=None, position_cost=10.0, tp_peak=10.5,
                    tp_stage=params["tp_stage_half"])
        # (10.5-9.71)/10.5 = 7.52% < 8% -> hold
        self.assertIsNone(self.mod.rule_take_profit_clear(
            params, ctx(last_price=9.71, **base)))
        # (10.5-9.65)/10.5 = 8.10% >= 8% -> clear
        hit = self.mod.rule_take_profit_clear(params, ctx(last_price=9.65, **base))
        self.assertEqual(hit["kind"], "take_profit_clear")
        self.assertEqual(hit["qty"], 1000)

    # ---- ④ 卖半的整手算术 -------------------------------------------------
    def test_half_qty_rules(self):
        params = self.mod.PARAMS
        self.assertEqual(self.mod._tp_half_qty(params, 1000), 500)
        self.assertEqual(self.mod._tp_half_qty(params, 1100), 500, "floor to whole lots")
        self.assertEqual(self.mod._tp_half_qty(params, 100), 100,
                         "one lot has no half: sell it whole (owner ruling 5)")
        self.assertIsNone(self.mod._tp_half_qty(params, 50),
                          "less than a lot: leave it to the clear line")

    # ---- ⑤ Stage 1：跟峰值 ------------------------------------------------
    def test_stage_one_giveback_in_points(self):
        params = self.mod.PARAMS
        base = dict(tp_pct=30, position_cost=10.0, tp_peak=13.0, tp_stage=1)
        # peak +30 pts, last +26 pts -> 4 pts given back < 5 -> hold
        self.assertIsNone(self.mod.rule_take_profit_clear(params, ctx(last_price=12.6, **base)))
        # last +24 pts -> 6 pts given back >= 5 -> clear the rest
        hit = self.mod.rule_take_profit_clear(params, ctx(last_price=12.4, **base))
        self.assertEqual(hit["qty"], 1000)

    def test_stage_zero_never_trails(self):
        """Before the half sale the trail leg must not fire (no stage, no clear)."""
        params = self.mod.PARAMS
        self.assertIsNone(self.mod.rule_take_profit_clear(
            params, ctx(tp_pct=None, position_cost=10.0, tp_peak=10.5, last_price=9.0)))

    def test_stage_one_blocks_stage_zero(self):
        params = self.mod.PARAMS
        self.assertIsNone(self.mod.rule_take_profit(params, ctx(tp_stage=1)))

    # ---- ⑥ 龙头开板清（任何阶段、不论盈亏）--------------------------------
    def test_dragon_open_board_clears_at_any_stage(self):
        params = self.mod.PARAMS
        for stage in (0, 1):
            hit = self.mod.rule_take_profit_clear(
                params, ctx(tp_dragon_open_no_reseal=True, tp_stage=stage,
                            last_price=9.0, position_cost=10.0))
            self.assertIsNotNone(hit, "stage %r" % (stage,))
            self.assertEqual(hit["qty"], 1000)

    # ---- ⑦ 弃权方向 -------------------------------------------------------
    def test_unknown_plan_abstains(self):
        params = self.mod.PARAMS
        base = dict(tp_known=None, tp_pct=None, last_price=9.0,
                    position_cost=10.0, tp_peak=10.0)
        self.assertIsNone(self.mod.rule_take_profit(params, ctx(**base)))
        self.assertIsNone(self.mod.rule_take_profit_clear(
            params, ctx(tp_stage=1, **base)))

    def test_manual_position_is_left_alone(self):
        params = self.mod.PARAMS
        self.assertIsNone(self.mod.rule_take_profit(params, ctx(tp_manual=True)))
        self.assertIsNone(self.mod.rule_take_profit_clear(
            params, ctx(tp_manual=True, tp_stage=1, last_price=9.0)))

    # ---- ⑧ 破位腿：有浮盈只减半 -------------------------------------------
    def test_breakdown_trims_half_when_in_profit(self):
        params = self.mod.PARAMS
        base = dict(last_price=9.5, ref_price=10.0, position_cost=9.3)
        hit = self.mod.rule_breakdown_sell(params, ctx(**base))
        self.assertEqual(hit["qty"], 500, "+2.2% must not be cleared whole (000797 target)")

    def test_breakdown_still_clears_at_a_loss(self):
        params = self.mod.PARAMS
        hit = self.mod.rule_breakdown_sell(
            params, ctx(last_price=9.5, ref_price=10.0, position_cost=9.8))
        self.assertEqual(hit["qty"], 1000)

    def test_breakdown_never_trims_a_manual_position(self):
        params = self.mod.PARAMS
        hit = self.mod.rule_breakdown_sell(
            params, ctx(last_price=9.5, ref_price=10.0, position_cost=9.3, tp_manual=True))
        self.assertEqual(hit["qty"], 1000)

    def test_breakdown_one_lot_in_profit_sells_whole(self):
        params = self.mod.PARAMS
        hit = self.mod.rule_breakdown_sell(
            params, ctx(last_price=9.5, ref_price=10.0, position_cost=9.3, position_qty=100))
        self.assertEqual(hit["qty"], 100)

    # ---- ⑨ 盒内可证的三件事 -----------------------------------------------
    def test_plan_reading(self):
        mod = self.mod
        mod.PARAMS["tp_plan"] = {DAY: {"600519": {"strategy": "dragon", "stage": 1}}}
        self.assertEqual(mod._tp_plan_of(DAY, SYM)["stage"], 1)
        self.assertIsNone(mod._tp_plan_of("20261123", SYM), "a stale day must not leak")
        self.assertIsNone(mod._tp_plan_of(DAY, "000001.SZ"))
        mod.PARAMS.pop("tp_plan")
        self.assertIsNone(mod._tp_plan_of(DAY, SYM))

    def test_peak_is_max_of_baked_and_today(self):
        mod = self.mod
        entry = _Entry([12.0, 13.4], [12.0, 13.1], ["202611241030", "202611241031"])
        peak = mod._tp_peak_of({SYM: entry}, SYM, {"peak": 13.2})
        self.assertEqual(peak, 13.4, "today's high above the baked peak must win")
        peak = mod._tp_peak_of({SYM: entry}, SYM, {"peak": 13.9})
        self.assertEqual(peak, 13.9, "a higher baked peak (yesterday) still counts")

    def test_stage_is_the_max_of_three_sources(self):
        mod = self.mod
        orders = [{"symbol": SYM, "short_code": "TP600519", "cancel_like": False}]
        # plan says 0, memory says nothing, counter says a TP order exists -> 1
        self.assertEqual(mod._tp_stage_of(DAY, SYM, {"stage": 0}, orders), 1)
        # a remembered half-sale outranks a stale plan
        mod._STATE["tp_stage_mem"] = {(DAY, "600519"): 1}
        self.assertEqual(mod._tp_stage_of(DAY, SYM, {"stage": 0}, []), 1)
        mod._STATE["tp_stage_mem"] = {}
        self.assertEqual(mod._tp_stage_of(DAY, SYM, {"stage": 0}, []), 0)

    def test_same_round_rebuy_is_blocked_before_the_counter_sees_it(self):
        """Owner ruling 7 must hold from the second the order goes out.

        The counter's ORDER table lags by seconds; sells are walked before buys
        inside one round, so an "add" on the name we just take-profited would
        slip through if `_tp_fired` only read `orders`. The in-process set is
        the correctness leg, not an optimisation (plan §6.2.8).
        """
        mod = self.mod
        self.assertFalse(mod._tp_fired(DAY, SYM, []))
        mod._tp_note_placed(DAY, SYM, "TP600519")
        self.assertTrue(mod._tp_fired(DAY, SYM, []), "同轮回买必须被本进程记忆挡住")
        # day-keyed: tomorrow starts clean
        self.assertFalse(mod._tp_fired("20261125", SYM, []))
        # and it is the TP/TT codes that set it, not any short code
        mod._STATE.clear()
        mod._tp_note_placed(DAY, SYM, "BK600519")
        self.assertFalse(mod._tp_fired(DAY, SYM, []))

    def test_other_short_codes_are_not_take_profit_evidence(self):
        mod = self.mod
        for short in ("BK600519", "SL600519", "RD600519", "PB600519", "TP6005"):
            orders = [{"symbol": SYM, "short_code": short}]
            self.assertFalse(mod._tp_fired(DAY, SYM, orders), short)
        self.assertTrue(mod._tp_fired(DAY, SYM, [{"symbol": SYM, "short_code": "TT600519"}]))
        # another symbol's TP row must not promote this one
        self.assertFalse(mod._tp_fired(DAY, SYM, [{"symbol": "000001.SZ", "short_code": "TP000001"}]))

    def test_dragon_board_reading(self):
        """Sealed then off the board for 15 minutes: True. Anything unprovable: None."""
        mod = self.mod
        up_stop = 13.53
        sealed = _Entry([13.53, 13.53, 13.10], [13.53, 13.53, 13.10],
                        ["202611240930", "202611241000", "202611241016"])
        self.assertIs(mod._tp_open_no_reseal_from(
            {SYM: sealed}, SYM, up_stop, 900, datetime(2026, 11, 24, 10, 16)), True)
        # only 16 minutes after 10:00 is 960 s; at 10:14 it is 840 s -> not yet
        self.assertIsNone(mod._tp_open_no_reseal_from(
            {SYM: sealed}, SYM, up_stop, 900, datetime(2026, 11, 24, 10, 14)))
        # still on the board in the last bar -> never fires
        on_board = _Entry([13.53, 13.53], [13.53, 13.53], ["202611240930", "202611241016"])
        self.assertIsNone(mod._tp_open_no_reseal_from(
            {SYM: on_board}, SYM, up_stop, 900, datetime(2026, 11, 24, 10, 16)))
        # no sealed bar at all -> unprovable
        never = _Entry([13.0, 13.1], [13.0, 13.1], ["202611240930", "202611241016"])
        self.assertIsNone(mod._tp_open_no_reseal_from(
            {SYM: never}, SYM, up_stop, 900, datetime(2026, 11, 24, 10, 16)))
        # missing limit price / missing bars / missing clock -> unprovable
        self.assertIsNone(mod._tp_open_no_reseal_from(
            {SYM: sealed}, SYM, None, 900, datetime(2026, 11, 24, 10, 16)))
        self.assertIsNone(mod._tp_open_no_reseal_from(
            {}, SYM, up_stop, 900, datetime(2026, 11, 24, 10, 16)))
        self.assertIsNone(mod._tp_open_no_reseal_from({SYM: sealed}, SYM, up_stop, 900, None))

    def test_hourly_readout_names_coverage(self):
        """The readout must say how many held names the cloud did NOT cover."""
        # 小时行必须点名：covered（云端给了计划的持仓）/ unknown（弃权的那几只）/
        # stage1（已卖过一半）/ dragon_open（龙头在板下够久）/ blocked（当日禁回买挡住）/
        # peak_raised（盒内峰值抬过了烘入值）。unknown 非零＝那些票整条阶梯弃权。
        mod = self.mod
        mod._STATE["tp_tally"] = {
            "known": {"000797.SZ", "000680.SZ", "000521.SZ"},
            "unknown": {"000415.SZ"},
            "stage1": {"000680.SZ"},
            "dragon_open": set(),
            "blocked": {"000678.SZ"},
            "peak_raised": {"000797.SZ"},
        }
        lines = []
        original = mod._log
        mod._log = lambda text: lines.append(text)
        try:
            mod._tp_readout()
        finally:
            mod._log = original
        self.assertEqual(len(lines), 1)
        self.assertIn("covered=3", lines[0])
        self.assertIn("unknown=1", lines[0])
        self.assertIn("stage1=1", lines[0])
        self.assertIn("dragon_open=0", lines[0])
        self.assertIn("blocked=1", lines[0])
        self.assertIn("peak_raised=1", lines[0])
        self.assertEqual(mod._STATE["tp_tally"], {},
                         "the tally resets with the line, otherwise it can never be read per hour")

    def test_tally_bucket_counts_positions_not_rounds(self):
        """A set of codes, not a counter: three rounds over one name still count 1."""
        # 读数口径：小时行读的是"有几只持仓在这个状态"，不是"这个小时发生了几次"。
        # 计数器会让 4 只持仓在 10 轮里显示 covered=40，读的人会以为云端烘了 40 份计划。
        mod = self.mod
        mod._STATE.pop("tp_tally", None)
        for _ in range(3):
            mod._tp_tally_bucket("unknown").add("000415.SZ")
        self.assertEqual(len(mod._STATE["tp_tally"]["unknown"]), 1,
                         "the line reads positions per hour; a counter would say 3")


if __name__ == "__main__":
    unittest.main()
