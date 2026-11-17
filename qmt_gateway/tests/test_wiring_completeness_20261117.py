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


if __name__ == "__main__":
    unittest.main()
