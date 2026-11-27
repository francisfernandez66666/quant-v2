# 2026-11-17 B 组 pregen 重算的行为锁（monkeypatch _q，绝不连 gz live 库）。
#
# 靶子：把"会动钱却哑火"的情绪/涨停池/估值类读数从新鲜日线/池派生补齐——
#  pcf_ttm/dv_ttm 进 valuation_series（修 CFP_ttm 0.299 那条 fac_ 恒 declined）
#  total_share_series（LnMktCap）
#  news_day（good_news_fade 定位事件 bar；propagation 仍无源→诚实弃权，见施工图 §16）
#  up_stop（stk_limit 板价）、is_leader（sector top_stocks）、limit_up_count/ladder_height
# （东财池派生）——market 由 main() 算一次传入。
#
# 故意破坏反证：删 SELECT 里的 pcf_ttm/dv_ttm/total_share、或不再传 market，下面断言必红。

import importlib.util
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GATEWAY = os.path.dirname(HERE)
REPO = os.path.dirname(GATEWAY)
SCRIPTS = os.path.join(REPO, "scripts")


def _load_pregen():
    sys.path.insert(0, GATEWAY)  # report_uplink 等被 pregen 间接 import
    spec = importlib.util.spec_from_file_location(
        "pregen_under_test", os.path.join(SCRIPTS, "pregen_carrier_factors_gz.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class PregenWiringB(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = _load_pregen()

    def _fake_q(self):
        def q(sql, args):
            s = " ".join(sql.split())
            if "FROM daily_basic" in s and "pe_ttm" in s:
                # (trade_date,pe,pb,ps,pcf,dv,turnover,total_share)
                return [("20261116", 10.0, 1.2, 2.0, 30.0, 2.5, 4.1, 1000.0),
                        ("20261115", 10.5, 1.1, 2.1, 31.0, 2.4, 3.9, 1000.0)]
            if "circ_mv" in s:
                return [(5000.0,)]
            if "FROM fina_indicator" in s:
                # §P3 2026-11-27：财务档改 10 列（尾部加 ann_date,end_date），
                # bake 侧按有效公告日过滤后才取最新一档（前视闸）。
                return [(0.5, 12.0, 12.0, 6.0, 30.0, 8.0, 40.0, 5.0,
                         "20261031", "20260930")]
            if "FROM d1_scores" in s:
                # §D1-LEVEL：带 level（个股级），测 news_propagation=False 烘值。
                return [(35.0, "2026-11-16", "个股")]  # score, date, level
            if "FROM stk_limit" in s:
                return [(11.0,)]
            if "FROM ths_limit_up_daily" in s:
                # (continue_cnt, first_seal_time, max_seal_money) —— 涨停事件日
                return [(2, "09:41", 3e7)]
            return None
        return q

    # B 组派生键全验一遍：valuation 加 pcf/dv、total_share、up_stop、news_day、
    # 以及 market 传入的 limit_up_count/ladder_height/is_leader——任一漏烘即对应因子弃权。
    def test_bake_bakeables_emits_the_recompute_keys(self):
        m = self.mod
        orig_q = m._q
        m._q = self._fake_q()
        try:
            out = m.bake_bakeables(
                "600206", {"hot_pool": {"bullish": {}}},
                lu_boards={"600206": 3, "000001": 1},
                market={"limit_up_count": 33.0, "ladder_height": 5.0,
                        "leader_set": {"600206"}})
        finally:
            m._q = orig_q
        val = out.get("valuation_series", {})
        self.assertIn("pcf_ttm", val, "pcf_ttm missing -> CFP_ttm fac rule stays declined")
        self.assertIn("dv_ttm", val, "dv_ttm missing -> DP factor stays declined")
        self.assertIn("total_share_series", out)
        self.assertEqual(out.get("up_stop"), 11.0)
        self.assertEqual(out.get("news_day"), "20261116")
        self.assertEqual(out.get("limit_up_count"), 33.0)
        self.assertEqual(out.get("ladder_height"), 5.0)
        self.assertTrue(out.get("is_leader"), "600206 in leader_set -> True")
        self.assertEqual(out.get("limit_board_count"), 3.0)  # existing per-code boards
        # seal/first_seal 与 assemble.go 同口径：峰值封单/流通市值(万元×1e4)、首封 HH:MM→分钟。
        # max_seal=3e7, circ_mv=5000万=5e7元 -> ratio=0.6; "09:41" -> 581 分钟。
        self.assertAlmostEqual(out.get("seal_amount_ratio"), 0.6, places=6,
                               msg="seal_amount_ratio 必须用峰值 max_seal_money/circ_mv，非东财当前 fund")
        self.assertEqual(out.get("first_seal_minute"), 581.0)

    def test_no_ths_event_row_does_not_fabricate_seal(self):
        # 池里没有该票涨停事件行（非涨停日 / ths 停摆缺行）时绝不烘 ratio=0——
        # 0 与 absent 在 lu_* 分档里语义不同，烘 0 会把"没数据"冒充"封单为零"。
        m = self.mod
        orig_q = m._q

        def q_none(sql, args):
            s = " ".join(sql.split())
            if "FROM ths_limit_up_daily" in s:
                return []  # 无涨停事件行
            return self._fake_q()(sql, args)
        m._q = q_none
        try:
            out = m.bake_bakeables("600206", {"hot_pool": {"bullish": {}}}, lu_boards={}, market={})
        finally:
            m._q = orig_q
        self.assertNotIn("seal_amount_ratio", out)
        self.assertNotIn("first_seal_minute", out)

    def test_is_leader_is_explicit_bool_not_none(self):
        m = self.mod
        orig_q = m._q
        m._q = self._fake_q()
        try:
            out = m.bake_bakeables("300394", {"hot_pool": {"bullish": {}}},
                                   lu_boards={}, market={"leader_set": {"600206"}})
        finally:
            m._q = orig_q
        # a non-leader must be explicit False (not None), else leader_decay's ctx read
        # cannot tell "not a leader" from "no data".
        self.assertIs(out.get("is_leader"), False)

    def test_news_propagation_from_level_three_states(self):
        # §D1-LEVEL 2026-11-17：propagation 由当日 D1 的 Level 烘出（Go adapter 同口径：
        # 个股级=False、板块/宏观=True、Level 空=不烘→诚实弃权）。不靠 reason 文本猜。
        m = self.mod
        orig_q = m._q
        for level, expected, present in [
            ("个股", False, True),
            ("板块", True, True),
            ("宏观", True, True),
            ("", None, False),   # 旧行/未知 Level：不烘
        ]:
            def q(sql, args, _lv=level):
                s = " ".join(sql.split())
                if "FROM d1_scores" in s:
                    return [(35.0, "2026-11-16", _lv)] if "level" in s else [(35.0, "2026-11-16")]
                return self._fake_q()(sql, args)
            m._q = q
            try:
                out = m.bake_bakeables("600206", {"hot_pool": {"bullish": {}}}, lu_boards={}, market={})
            finally:
                pass
            if present:
                self.assertIn("news_propagation", out, "Level=%r 应烘 propagation" % level)
                self.assertIs(out["news_propagation"], expected,
                              "Level=%r propagation 应为 %r" % (level, expected))
            else:
                self.assertNotIn("news_propagation", out,
                                 "Level 空应不烘 propagation（诚实弃权，不猜）")
        m._q = orig_q

    # §WIRING-B+ fetch_market_history：从权威落库表逐日聚合涨停/炸板/炸板率多日序列。
    def test_fetch_market_history_orders_old_to_new_and_ratio_math(self):
        m = self.mod
        orig_q = m._q
        # 故意返回 DESC（新→旧），函数须翻成旧→新；炸板表只有两天有行，缺的那天
        # **整条炸板序列跳过**（§F4 2026-11-27：绝不补 0 冒充"没人炸板"）。
        def q(sql, args):
            s = " ".join(sql.split())
            if "FROM ths_limit_up_daily" in s and "GROUP BY trade_date" in s:
                # (trade_date, count, max_continue_cnt) —— 路线B 要烘连板高度历史
                return [("20261116", 30, 5), ("20261115", 20, 3), ("20261114", 10, 2)]
            if "FROM ths_break_pool_daily" in s and "GROUP BY trade_date" in s:
                return [("20261116", 10), ("20261114", 2)]  # 20261115 缺行 -> 该天不烘炸板
            return None
        m._q = q
        try:
            h = m.fetch_market_history("20261116", lookback=20)
        finally:
            m._q = orig_q
        self.assertIsNotNone(h)
        self.assertEqual(h["limit_up_count_history"], [10.0, 20.0, 30.0])  # old->new
        # 炸板序列只剩真有行的两天：20261114 2/(10+2)、20261116 10/(30+10)。
        self.assertEqual(h["broken_board_count_history"], [2.0, 10.0])
        self.assertEqual(h["broken_board_ratio_history"], [round(2.0 / 12.0, 6), 0.25])
        # latest-session scalars + as-of day（最新一天有行 ⇒ 标量照烘）
        self.assertEqual(h["broken_board_count"], 10.0)
        self.assertAlmostEqual(h["broken_board_ratio"], 0.25, places=6)
        self.assertEqual(h["emotion_day"], "20261116")
        # 路线B：连板高度历史也须烘出（old->new，涨停池有自己的行就保留）
        self.assertEqual(h["ladder_height_history"], [2.0, 3.0, 5.0])
        self.assertEqual(h["ladder_height"], 5.0)

    def test_fetch_market_history_stale_break_pool_is_not_zero_filled(self):
        """§F4 2026-11-27：炸板池表停更（现网自 20261117 起无新行）时的形状。

        真实现场：涨停池 fresh 到当天、炸板池停在 7 个交易日前。旧实现把缺行补 0，
        `broken_board_ratio_history` 尾部就是一串编出来的 0.0，而 emo_blast_rate 方向 -1
        会把假 0 读成"历史最低炸板率"→ 分位 0 → 反向满分贡献，新规则 fac_173 反而更容易
        过阈值。这条用例钉住三件事：炸板序列只含真实日、最新一天缺行时两个标量不烘、
        涨停池那条序列仍然 fresh。
        """
        m = self.mod
        orig_q = m._q
        printed = []

        def q(sql, args):
            s = " ".join(sql.split())
            if "FROM ths_limit_up_daily" in s and "GROUP BY trade_date" in s:
                return [("20261127", 68, 5), ("20261126", 57, 4), ("20261117", 36, 3)]
            if "FROM ths_break_pool_daily" in s and "GROUP BY trade_date" in s:
                return [("20261117", 20)]  # 之后 7 个交易日全缺行
            return None

        m._q = q
        try:
            import builtins
            real_print = builtins.print
            builtins.print = lambda *a, **k: printed.append(" ".join(str(x) for x in a))
            try:
                h = m.fetch_market_history("20261127", lookback=20)
            finally:
                builtins.print = real_print
        finally:
            m._q = orig_q
        self.assertIsNotNone(h)
        # 涨停池 fresh：三天都在
        self.assertEqual(h["limit_up_count_history"], [36.0, 57.0, 68.0])
        self.assertEqual(h["emotion_day"], "20261127")
        # 炸板序列只有真实的那一天，绝不出现编出来的 0.0
        self.assertEqual(h["broken_board_count_history"], [20.0])
        self.assertEqual(h["broken_board_ratio_history"], [round(20.0 / 56.0, 6)])
        # 最新一天缺行 ⇒ 两个标量整键缺席（消费侧 None 弃权，而不是 0）
        self.assertNotIn("broken_board_count", h)
        self.assertNotIn("broken_board_ratio", h)
        # 陈旧要留痕：烘焙日志必须点名两个 as-of 的差距
        self.assertTrue(any("PREGEN_BLAST_STALE" in line for line in printed),
                        "炸板池停更时须打 PREGEN_BLAST_STALE，实际输出=%r" % (printed,))

    def test_fetch_market_history_returns_none_when_pool_table_empty(self):
        # 盘前池未同步 / 表不存在：_q 返回 None/空 ⇒ 整个历史 None，绝不烘一个 0 序列。
        m = self.mod
        orig_q = m._q
        m._q = lambda sql, args: None
        try:
            self.assertIsNone(m.fetch_market_history("20261116"))
        finally:
            m._q = orig_q

    def test_market_history_flows_into_bake_out(self):
        # market 聚合一次，须按账户级标量逐票写进 out（消费端 rules_factor 从 ctx 读这些键）。
        m = self.mod
        orig_q = m._q
        m._q = self._fake_q()
        try:
            out = m.bake_bakeables(
                "600206", {"hot_pool": {"bullish": {}}}, lu_boards={},
                market={"limit_up_count_history": [10.0, 20.0],
                        "broken_board_ratio_history": [0.1, 0.2],
                        "broken_board_count": 4.0, "broken_board_ratio": 0.2,
                        "emotion_day": "20261116"})
        finally:
            m._q = orig_q
        self.assertEqual(out.get("limit_up_count_history"), [10.0, 20.0])
        self.assertEqual(out.get("broken_board_ratio_history"), [0.1, 0.2])
        self.assertEqual(out.get("broken_board_ratio"), 0.2)
        self.assertEqual(out.get("emotion_day"), "20261116")
        # absent market keys must NOT be materialised as 0/None into out.
        self.assertNotIn("afternoon_reseal", out)


if __name__ == "__main__":
    unittest.main()
