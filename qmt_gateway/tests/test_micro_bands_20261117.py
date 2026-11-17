# test_micro_bands_20261117.py — 个股微结构因子 band 标定的行为锁（2026-11-17）。
#
# 方案 §9.3：情绪/涨停池类 6 因子走路线B(percentile)，但个股微结构因子
# lu_board/lu_board3/lu_early_seal 无全市场时序概念，仍走 A 的数值 band。
# 方案 §3（自查修订后）给的 band：lu_board>=2、lu_board3>=3、first_seal<600。
# lu_seal_strength 因 circ_mv 全空算不出比值，诚实弃权不标。
#
# ★ 方向语义（必须钉死，否则容易被误读为标反）：这些因子在 fac_ 规则里
# direction=-1，score_factor_rule 对负方向算 contrib*(1-pct)——即 band 命中
# (pct=1)反而贡献 0、未命中贡献满权重。这是现网规则的既有方向，本测试把它
# 如实锁定：band 让因子"能打分"，但打分如何进入合成由 direction 决定，
# 标定者改 direction 属另一笔资金路径决策，不在本次 band 标定范围。
import importlib.util
import json
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GATEWAY = os.path.dirname(HERE)
REPO = os.path.dirname(GATEWAY)
sys.path.insert(0, GATEWAY)

import rules_factor as rf  # noqa: E402

PARAMS_PATH = os.path.join(GATEWAY, "local_executor_params.json")


def _baked_params():
    return json.load(open(PARAMS_PATH, encoding="utf-8"))["params"]


class TestMicroBandsBaked(unittest.TestCase):
    def test_three_bands_present_with_plan_values(self):
        bands = _baked_params()["scalar_bands"]
        self.assertEqual(bands.get("lu_board"), {"min": 2, "max": 100})
        self.assertEqual(bands.get("lu_board3"), {"min": 3, "max": 100})
        self.assertEqual(bands.get("lu_early_seal"), {"min": 0, "max": 600})

    def test_seal_strength_stays_unbaked(self):
        # 数据不可得(circ_mv 全空)⇒ 方案明定暂不启用，绝不编一个 band。
        bands = _baked_params()["scalar_bands"]
        self.assertNotIn("lu_seal_strength", bands)


class TestMicroBandScoring(unittest.TestCase):
    def _score(self, fid, value):
        p = _baked_params()
        spec = p["factor_defs"][fid]
        return rf._scalar_score(p, fid, value, spec)

    def test_lu_board3_hit(self):
        self.assertEqual(self._score("lu_board3", 4.0)["score"], 1.0)

    def test_lu_board3_miss(self):
        self.assertEqual(self._score("lu_board3", 2.0)["score"], 0.0)

    def test_lu_board_hit(self):
        self.assertEqual(self._score("lu_board", 3.0)["score"], 1.0)

    def test_lu_early_seal_hit_before_1000(self):
        self.assertEqual(self._score("lu_early_seal", 574.0)["score"], 1.0)

    def test_lu_early_seal_miss_after_1000(self):
        self.assertEqual(self._score("lu_early_seal", 700.0)["score"], 0.0)

    def test_missing_value_still_declines(self):
        # band 在但没值 ⇒ 仍不可打分（绝不制造 0）。
        self.assertIsNone(self._score("lu_board3", None))

    def test_negative_direction_semantics_locked(self):
        """负方向：band 命中(pct=1)贡献 0，未命中贡献满权重 — 现网既有语义。"""
        p = _baked_params()
        rule = {"id": "fac_x", "name": "factor_composite",
                "factors": ["lu_board3"], "weights": {"lu_board3": 0.5},
                "directions": {"lu_board3": -1}, "buy_threshold": 70}
        hit = rf.score_factor_rule(p, rule, {"limit_board_count": 4.0})
        miss = rf.score_factor_rule(p, rule, {"limit_board_count": 2.0})
        self.assertAlmostEqual(hit["details"]["lu_board3"], 1.0)
        self.assertAlmostEqual(miss["details"]["lu_board3"], 0.0)
        # score = (total/used + 1)/2*100 : hit -> 50, miss -> 100
        self.assertLess(hit["score"], miss["score"])


if __name__ == "__main__":
    unittest.main()
