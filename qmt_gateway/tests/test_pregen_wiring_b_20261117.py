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
                return [(0.5, 12.0, 12.0, 6.0, 30.0, 8.0, 40.0, 5.0)]
            if "FROM d1_scores" in s:
                return [(35.0, "2026-11-16")]  # score, date
            if "FROM stk_limit" in s:
                return [(11.0,)]
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

    def test_news_propagation_stays_absent_not_fabricated(self):
        # d1_scores has no Level column: we must NOT invent propagation from text.
        m = self.mod
        orig_q = m._q
        m._q = self._fake_q()
        try:
            out = m.bake_bakeables("600206", {"hot_pool": {"bullish": {}}}, lu_boards={}, market={})
        finally:
            m._q = orig_q
        self.assertNotIn("news_propagation", out,
                         "propagation must stay absent (no Level source) -> good_news_fade declines honestly")


if __name__ == "__main__":
    unittest.main()
