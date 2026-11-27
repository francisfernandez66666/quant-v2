# 接线完备性门禁（施工图 §11.4 / 2026-11-17）：build_context 必须把 live 战法与
# factor_composite 白名单因子所需的 ctx 键全部装配出来。这是 A 组"全套自闭环接全接透"
# 的结构锁——任一 live 战法 input 键从 build_context 消失（删键/改名），本用例即红。
#
# 为什么是白名单契约而不是"build_context ⊇ 所有规则读的 ctx 键"：
# 规则层读的 ctx 键里有大量**运行时注入**（_sizing_view 的 pending_buys/positions_live/
# t_*_leg_value/industry_value/cluster_value/risk_tier）与**内部中间量/输出键**
# （_passed_but_no_budget*/advancers/decliners/regime/holiday/macro_events/breadth_unknown/
# d1_blocked/window_stop_losses）——它们本就不该由 build_context 提供。字面全集覆盖会
# 误报 19 项。所以这里锁定的是**经过核对的 live 战法/因子 input 契约键集**：每个键都
# 有明确的业务来源（pregen 烘值或 tick），且当前 48 键 build_context 全部覆盖。
#
# English: structural contract that build_context assembles every ctx input the live
# tactics/factors need. Not "all keys any rule reads" (many are runtime-injected); this
# is the reviewed live-tactic input set, each with a real source.
import datetime
import os
import re
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GATEWAY = os.path.dirname(HERE)
REPO = os.path.dirname(GATEWAY)
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")
STRATEGY_SRC = os.path.join(GATEWAY, "qmt_internal_strategy.py")

# 经核对的 live 战法 / factor_composite 白名单因子 input 契约。
# 新增 live 战法引入新 ctx 键时，必须同步把 build_context 补键 + 更新此集合（人工审查点）。
LIVE_TACTIC_CTX_INPUTS = {
    "symbol", "trading_day", "hhmm", "day", "weekday",
    "last_price", "prev_close", "bars", "ma5", "ma20", "today_vol_vs_5d",
    "position_qty", "position_cost", "can_use_qty", "available_cash",
    # factor_composite / 移植战法的烘值 input（A 组透传 + B 组 pregen 烘）
    "valuation_series", "turnover_series", "total_share_series", "fin_pit",
    "flow_today_pct", "flow_5d_pct", "news_event", "concept_heat",
    "limit_board_count", "is_leader", "ladder_height", "consec_boards", "afternoon_reseal",
    "up_stop", "index_change_pct", "limit_up_count", "limit_up_count_history",
    "broken_board_ratio", "broken_board_count", "broken_board_ratio_history",
    "broken_board_count_history", "ladder_height_history",
    "seal_amount_ratio", "first_seal_minute", "sector_limit_up_drop_pct",
    "emotion_phase", "emotion_day",
}


def _build_context_keys():
    """静态解析产物里 build_context 返回字典的键集（引号感知的字面键）。"""
    out = tempfile.mkdtemp()
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox", "--out-dir", out],
                          capture_output=True, text=True, cwd=REPO)
    if proc.returncode != 0:
        raise AssertionError("sandbox build failed: %s" % (proc.stdout + proc.stderr)[-800:])
    artifact = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            artifact = token.split("=", 1)[1]
    src = open(artifact, encoding="utf-8", errors="replace").read()
    start = src.index("def build_context(")
    body = src[start:]
    nxt = body.find("\ndef ", 10)
    if nxt != -1:
        body = body[:nxt]
    return set(re.findall(r'^\s*"([a-z_0-9]+)":', body, re.M))


def _build_and_load():
    """跑真构建器产沙箱产物再 load 成模块（行为腿只能对"真产物"取证）。"""
    import importlib.util
    out_dir = tempfile.mkdtemp()
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox",
                           "--out-dir", out_dir],
                          capture_output=True, text=True, cwd=REPO)
    if proc.returncode != 0:
        raise AssertionError("sandbox build failed: %s" % (proc.stdout + proc.stderr)[-800:])
    path = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    if not path:
        raise AssertionError("sandbox build printed no artifact= line")
    spec = importlib.util.spec_from_file_location("wiring_completeness_artifact", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class WiringCompleteness20261117(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bc_keys = _build_context_keys()

    def test_build_context_covers_all_live_tactic_inputs(self):
        missing = sorted(LIVE_TACTIC_CTX_INPUTS - self.bc_keys)
        self.assertFalse(missing,
                         "build_context 缺 live 战法/因子 input 键（接线被拆）：%s" % missing)

    def test_contract_is_not_trivially_empty(self):
        # 防止有人把白名单清空让上一条恒绿。
        self.assertGreaterEqual(len(LIVE_TACTIC_CTX_INPUTS), 30,
                                "契约键集被缩小到失去意义")


class PrecalcHistoryPassthrough20261127(unittest.TestCase):
    """§F1（2026-11-27）行为腿：两个情绪历史键必须真的从 precalc 流到 ctx。

    为什么不能只做 grep 锁：本缺陷的形状正是"键名写对了、装配没写"——
    pregen 早就烘了 ladder_height_history / broken_board_count_history，
    build_context 的字典字面量里却从来没有它们，于是 emo_max_board /
    emo_blast_cnt 的 input 恒 None、整条规则静默 declined，读数上完全看不出来。
    所以这里跑真产物、真调 build_context，断言**值**相等而不是键存在。
    """

    @classmethod
    def setUpClass(cls):
        cls.mod = _build_and_load()

    # §F1：把两条历史塞进 precalc，真跑产物里的 build_context，断言值逐位透传。
    def test_precalc_history_keys_reach_build_context(self):
        mod = self.mod
        symbol = "600000.SH"
        ladder_hist = [3.0, 4.0, 2.0, 5.0, 3.0]
        broken_hist = [11.0, 9.0, 14.0, 8.0, 12.0]
        saved = mod.PARAMS
        try:
            table = dict(saved)
            precalc = dict(table.get("precalc") or {})
            row = dict(precalc.get(symbol) or {})
            row["ladder_height_history"] = ladder_hist
            row["broken_board_count_history"] = broken_hist
            precalc[symbol] = row
            table["precalc"] = precalc
            mod.PARAMS = table

            class _NoTick(object):
                """没有任何行情/历史读数：precalc 透传腿必须自己把值带出来。"""

                def get_full_tick(self, codes):
                    return {}

            ctx = mod.build_context(_NoTick(), symbol,
                                    datetime.datetime(2026, 11, 27, 10, 15, 0),
                                    [], {}, 100000.0)
            self.assertEqual(ctx.get("ladder_height_history"), ladder_hist,
                             "ladder_height_history 没流到 ctx（emo_max_board 会恒弃权）")
            self.assertEqual(ctx.get("broken_board_count_history"), broken_hist,
                             "broken_board_count_history 没流到 ctx（emo_blast_cnt 会恒弃权）")
        finally:
            mod.PARAMS = saved

    def test_absent_precalc_keeps_the_keys_none(self):
        """缺 precalc 时两键仍是 None：只增透传，不许把"没有读数"变成 0/空表。"""
        mod = self.mod
        saved = mod.PARAMS
        try:
            table = dict(saved)
            precalc = dict(table.get("precalc") or {})
            precalc.pop("600000.SH", None)
            table["precalc"] = precalc
            mod.PARAMS = table

            class _NoTick(object):
                def get_full_tick(self, codes):
                    return {}

            ctx = mod.build_context(_NoTick(), "600000.SH",
                                    datetime.datetime(2026, 11, 27, 10, 15, 0),
                                    [], {}, 100000.0)
            self.assertIsNone(ctx.get("ladder_height_history"))
            self.assertIsNone(ctx.get("broken_board_count_history"))
        finally:
            mod.PARAMS = saved


class VwapDevWiring20261127(unittest.TestCase):
    """P2（2026-11-27）行为腿：vwap_dev 必须由盒内 tick 现算，单位是百分比。

    为什么钉在真产物上：`_vwap_of` 自带量纲自纠与 sanity 带，vwap_dev 又只是它的
    一层派生。用桩替掉 `_vwap_of` 就测不到"这条腿在盒上到底会不会出数"——而这
    正是它存在的全部意义（盒内拿不到 1m K，只能靠 tick 累计额/量）。
    """

    @classmethod
    def setUpClass(cls):
        cls.mod = _build_and_load()

    def _ctx_with_tick(self, tick):
        mod = self.mod
        symbol = "600000.SH"

        class _Tick(object):
            def get_full_tick(self, codes):
                return {code: tick for code in codes}

        return mod.build_context(_Tick(), symbol,
                                 datetime.datetime(2026, 11, 27, 10, 15, 0),
                                 [], {}, 100000.0)

    def test_price_above_vwap_reads_positive_percent(self):
        # 1000 股 × 10.00 = 10000 元累计额，现价 10.50 ⇒ 高于 VWAP 5%
        ctx = self._ctx_with_tick({"lastPrice": 10.5, "lastClose": 10.0,
                                   "volume": 1000.0, "amount": 10000.0})
        self.assertAlmostEqual(ctx.get("vwap_dev"), 5.0, places=6)

    def test_price_below_vwap_reads_negative_percent(self):
        ctx = self._ctx_with_tick({"lastPrice": 9.5, "lastClose": 10.0,
                                   "volume": 1000.0, "amount": 10000.0})
        self.assertAlmostEqual(ctx.get("vwap_dev"), -5.0, places=6)

    def test_no_tick_keeps_it_none(self):
        """没有 tick 时必须是 None（诚实弃权），不是 0（0 会被读成"正好在 VWAP"）。"""
        mod = self.mod
        saved = mod.PARAMS
        try:
            table = dict(saved)
            precalc = dict(table.get("precalc") or {})
            precalc.pop("600000.SH", None)
            table["precalc"] = precalc
            mod.PARAMS = table

            class _NoTick(object):
                def get_full_tick(self, codes):
                    return {}

            ctx = mod.build_context(_NoTick(), "600000.SH",
                                    datetime.datetime(2026, 11, 27, 10, 15, 0),
                                    [], {}, 100000.0)
            self.assertIsNone(ctx.get("vwap_dev"))
        finally:
            mod.PARAMS = saved



if __name__ == "__main__":
    unittest.main()
