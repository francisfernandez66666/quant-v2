# test_sector_drop_20261117.py — 板块涨停家数降幅烘值的行为锁（2026-11-17）。
#
# owner 定案：sector_limit_up_drop_pct（leader_decay 的退潮加分项）走云端烘——
# 机内不能读文件/网络、_STATE 重启即丢，"昨日板块涨停家数"无源，所以由 pregen
# 从权威表 sector_history(limitup_cnt) 算好今昨降幅，按个股行业烘进 precalc。
#
# 口径（Go shortbase.go:32 "empty = 0"）：降幅无定义（昨=0/缺板块/缺两日）⇒
# 不烘，消费侧按 0 处理；今日涨停增加⇒负降幅照烘（自然不触发 >=0.5 判据）。
# 本文件逐条钉死，任何"给缺失编一个降幅"的改动都会红。
import importlib.util
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GATEWAY = os.path.dirname(HERE)
REPO = os.path.dirname(GATEWAY)
SCRIPTS = os.path.join(REPO, "scripts")


def _load_pregen():
    sys.path.insert(0, GATEWAY)
    spec = importlib.util.spec_from_file_location(
        "pregen_sector_drop_under_test", os.path.join(SCRIPTS, "pregen_carrier_factors_gz.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class TestSectorDropMap(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = _load_pregen()

    def _patch_q(self, rows_by_fragment):
        """按 SQL 片段返回假行；不连任何真实库。"""
        original = self.mod._q

        def fake_q(sql, args):
            s = " ".join(sql.split())
            for fragment, rows in rows_by_fragment.items():
                if fragment in s:
                    return rows
            return original(sql, args)  # 其余走原（本测试不触发）

        self.mod._q = fake_q
        return original

    def test_drop_ratio_and_sign(self):
        # 两交易日：今日 20261117、昨日 20261116；args[0] 区分今昨查询。
        def fake_q(sql, args):
            s = " ".join(sql.split())
            if "SELECT DISTINCT trade_date" in s:
                return [("20261117",), ("20261116",)]
            # 今日查询带 limitup_cnt，args[0] 区分日期
            if "COALESCE(limitup_cnt,0) FROM sector_history" in s:
                if args and args[0] == "20261117":
                    return [("银行", 5), ("证券", 12), ("地产", 0)]
                if args and args[0] == "20261116":
                    return [("银行", 10), ("证券", 10), ("地产", 8)]
            return None
        self.mod._q = fake_q
        result = self.mod.fetch_sector_drop_map("20261117")
        # 银行 (10-5)/10=0.5；证券 (10-12)/10=-0.2；地产昨8今0=(8-0)/8=1.0
        self.assertAlmostEqual(result["银行"], 0.5)
        self.assertAlmostEqual(result["证券"], -0.2)
        self.assertAlmostEqual(result["地产"], 1.0)

    def test_yesterday_zero_is_not_baked(self):
        def fake_q(sql, args):
            s = " ".join(sql.split())
            if "SELECT DISTINCT trade_date" in s:
                return [("20261117",), ("20261116",)]
            if "COALESCE(limitup_cnt,0) FROM sector_history" in s:
                if args[0] == "20261117":
                    return [("新板块", 5)]
                if args[0] == "20261116":
                    return [("新板块", 0)]        # 昨日 0 ⇒ 降幅无定义
            return None
        self.mod._q = fake_q
        self.assertIsNone(self.mod.fetch_sector_drop_map("20261117"))

    def test_missing_two_days_is_none(self):
        self.mod._q = lambda sql, args: [("20261117",)] if "DISTINCT trade_date" in sql else None
        self.assertIsNone(self.mod.fetch_sector_drop_map("20261117"))

    def test_industry_map(self):
        self.mod._q = lambda sql, args: [("600001.SH", "银行"), ("000001.SZ", "证券")]
        result = self.mod.fetch_industry_map()
        self.assertEqual(result, {"600001.SH": "银行", "000001.SZ": "证券"})

    def test_industry_map_empty_stays_none(self):
        self.mod._q = lambda sql, args: []
        self.assertIsNone(self.mod.fetch_industry_map())


class TestSectorDropBake(unittest.TestCase):
    """bake_bakeables 按 market 里两 map 给个股烘 sector_limit_up_drop_pct。"""

    @classmethod
    def setUpClass(cls):
        cls.mod = _load_pregen()

    def _minimal_params(self):
        return {"precalc": {}, "hot_pool": {}}

    def test_per_symbol_drop_baked(self):
        market = {"sector_drop_map": {"银行": 0.5},
                  "industry_map": {"600001.SH": "银行"}}
        out = self.mod.bake_bakeables("600001.SH", self._minimal_params(), market=market)
        self.assertAlmostEqual(out["sector_limit_up_drop_pct"], 0.5)

    def test_unknown_industry_not_baked(self):
        market = {"sector_drop_map": {"银行": 0.5},
                  "industry_map": {"600001.SH": "证券"}}   # 证券无降幅
        out = self.mod.bake_bakeables("600001.SH", self._minimal_params(), market=market)
        self.assertNotIn("sector_limit_up_drop_pct", out)

    def test_no_maps_not_baked(self):
        out = self.mod.bake_bakeables("600001.SH", self._minimal_params(), market={})
        self.assertNotIn("sector_limit_up_drop_pct", out)


if __name__ == "__main__":
    unittest.main()
