# test_project_account_params.py — 账号真源投影层单测（P1，PLAN_CARRIER_RISK_REBUILD §1.1）
#
# 验证「账号导出 → 载体顶层 PARAMS」的投影正确性 + 漂移门禁能抓住
# "sizing_enabled 漂回出厂默认 True" 这类事故。导出基线以 inline fixture 承载
# （docs/EXPORT_ACCOUNT_RULES_20261110.json 未入库，CI 取不到，故不依赖文件）。
import os
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import project_account_params as pap  # noqa: E402

# 与 docs/EXPORT_ACCOUNT_RULES_20261110.json 相关的账号真值子集（脱敏后）。
EXPORT_FIXTURE = {
    "qmt": {
        "max_positions": 6,
        "daily_budget_amount": 150000,
        "min_order_amount": 10000,
        "max_signal_vwap_premium_pct": 5,
        "entry_timeout_sec": 300,
        "entry_pullback_pct": 0.7,
        "strategies": [
            "dragon", "n_shape", "fac_1", "fac_118", "fac_117", "fac_136",
            "fac_131", "fac_137", "fac_123", "fac_150", "fac_153", "fac_154",
            "fac_155", "fac_157", "fac_156", "fac_160", "fac_163", "fac_162",
        ],
        "money": {"cash_reserve_ratio": 0},
        "risk_gate": {
            "day_loss_limit_pct": 3.5,
            "sector_value_pct": 35,
            "concept_value_pct": 35,
            "single_stock_value_pct": 25,
            "risk_per_trade_pct": 0.5,
            "risk_budget_shadow": True,
            "limit_up_block_buy": True,
            "limit_down_block_sell": True,
        },
    },
    "sizing": {"enabled": False},
}


class ProjectionTest(unittest.TestCase):
    def setUp(self):
        self.p = pap.project_account_rules(EXPORT_FIXTURE)

    def test_scalars_from_account(self):
        self.assertEqual(self.p["max_positions"], 6)
        self.assertEqual(self.p["daily_buy_cap"], 150000)
        self.assertEqual(self.p["min_order_amount"], 10000)
        self.assertEqual(self.p["max_signal_vwap_premium_pct"], 5)
        self.assertEqual(self.p["entry_timeout_sec"], 300)
        self.assertEqual(self.p["entry_pullback_pct"], 0.7)
        self.assertEqual(self.p["day_loss_limit_pct"], 3.5)
        self.assertEqual(self.p["industry_cap_pct"], 35)
        self.assertEqual(self.p["risk_per_trade_pct"], 0.5)
        self.assertTrue(self.p["risk_budget_shadow"])
        self.assertTrue(self.p["limit_up_block_buy"])
        self.assertTrue(self.p["limit_down_block_sell"])
        self.assertEqual(self.p["min_cash_reserve_ratio"], 0)

    def test_sizing_enabled_override_true(self):
        # §3.0 Q2′ 终裁：账号 sizing.enabled=false（全套 0）视为"未配置"而非"要关"，
        # 恢复 Go 出厂动态仓位 ⇒ override 为 True；与 §1.1 早期"与账号反向"初判相反，以终裁为准。
        self.assertTrue(self.p["sizing_enabled"])

    def test_owner_override_single_stock_35(self):
        # 账号 25，owner Q4 终裁 35 覆盖。
        self.assertEqual(self.p["single_stock_cap_pct"], 35)

    def test_owner_override_cluster_35(self):
        # 账号 concept 35，终裁 35（§1.1 表的 40 被 Q4 取代）。
        self.assertEqual(self.p["cluster_cap_pct"], 35)

    def test_enabled_strategies_all_18(self):
        self.assertEqual(len(self.p["enabled_strategies"]), 18)
        self.assertIn("fac_163", self.p["enabled_strategies"])
        self.assertIn("fac_162", self.p["enabled_strategies"])


class DriftGateTest(unittest.TestCase):
    def test_catches_sizing_enabled_false_drift(self):
        # §3.0 Q2′ 终裁 sizing_enabled 必须为 True（恢复 Go 出厂动态仓位）；
        # 若烘参漂回账号全 0 对应的 False，漂移门禁必须报 CRITICAL。
        baked = {"sizing_enabled": False, "single_stock_cap_pct": 35.0,
                 "industry_cap_pct": 35.0, "cluster_cap_pct": 40.0,
                 "day_loss_limit_pct": 3.5, "max_positions": 6,
                 "min_order_amount": 10000, "max_signal_vwap_premium_pct": 5,
                 "enabled_strategies": EXPORT_FIXTURE["qmt"]["strategies"][:16]}
        projection = pap.project_account_rules(EXPORT_FIXTURE)
        drift = pap.check_projection_drift(baked, projection)
        by_key = {k: (b, p, s) for k, b, p, s in drift}
        # sizing_enabled 漂回 False（与 §3.0 终裁 True 相反）必须被抓为 CRITICAL
        self.assertIn("sizing_enabled", by_key)
        self.assertEqual(by_key["sizing_enabled"][2], "CRITICAL")
        # cluster_cap 烘参 40 vs 终裁 35 应被标出（WARN 即可，属覆盖登记）
        self.assertIn("cluster_cap_pct", by_key)
        # enabled_strategies 缺 2 项应被标出
        self.assertIn("enabled_strategies", by_key)

    def test_no_drift_when_baked_matches(self):
        projection = pap.project_account_rules(EXPORT_FIXTURE)
        baked = dict(projection)  # 完全一致
        self.assertEqual(pap.check_projection_drift(baked, projection), [])


if __name__ == "__main__":
    unittest.main()
