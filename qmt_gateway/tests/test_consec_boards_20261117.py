# test_consec_boards_20261117.py — 个股连板数烘值的行为锁（2026-11-17）。
#
# leader_decay_v2 是 live 卖出战法，两个否决项之一 consec_boards（今日之前连续
# 涨停天数）此前无任何源 ⇒ 恒 decline。方案 §11.2 明令：不能用 pool 的
# ladder_height（全市场池 max）代替，必须逐票从日K算。
# pregen 从 daily+stk_limit 逐日算（权威板价 up_limit，比 Go 按股票名猜
# 10/20/30% 更准）；盘前跑 ⇒ 最新日K即昨日 ⇒ 正好是盘中规则要的"今日之前"。
# 本文件钉死判据，任何"拿池 max 顶替 / 缺源编 0 / 把今天算进去"都必红。
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
        "pregen_consec_under_test", os.path.join(SCRIPTS, "pregen_carrier_factors_gz.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class TestConsecBoards(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = _load_pregen()

    def _patch(self, daily_rows, limit_rows):
        def fake_q(sql, args):
            s = " ".join(sql.split())
            if "FROM daily WHERE" in s:
                return daily_rows
            if "FROM stk_limit WHERE" in s:
                return limit_rows
            return None
        self.mod._q = fake_q

    def test_counts_consecutive_boards_from_newest_backward(self):
        # 最新(昨) 11.0/10.0涨停、10.0/9.09涨停、9.09 未达板 ⇒ 连板=2
        daily = [("20261116", 11.0), ("20261115", 10.0), ("20261114", 9.09)]
        limits = [("20261116", 11.0), ("20261115", 10.0), ("20261114", 10.0)]
        self._patch(daily, limits)
        self.assertEqual(self.mod.compute_consec_boards("001234.SZ", tol=0.995), 2)

    def test_streak_breaks_at_first_non_limit_day(self):
        daily = [("20261116", 11.0), ("20261115", 9.5), ("20261114", 8.7)]
        limits = [("20261116", 11.0), ("20261115", 10.0), ("20261114", 9.6)]
        self._patch(daily, limits)
        self.assertEqual(self.mod.compute_consec_boards("001234.SZ", tol=0.995), 1)

    def test_missing_up_limit_breaks_streak(self):
        # 昨日无 stk_limit 行 ⇒ 无法证明封板 ⇒ 连板=0（诚实，不跳过去数更早）
        daily = [("20261116", 11.0), ("20261115", 10.0)]
        limits = [("20261115", 10.0)]       # 缺 20261116 的板价
        self._patch(daily, limits)
        self.assertEqual(self.mod.compute_consec_boards("001234.SZ", tol=0.995), 0)

    def test_no_history_is_none(self):
        self._patch([], [])
        self.assertIsNone(self.mod.compute_consec_boards("001234.SZ"))

    def test_tolerance_boundary(self):
        up = 10.0
        edge = up * 0.995
        self._patch([("20261116", edge)], [("20261116", up)])
        self.assertEqual(self.mod.compute_consec_boards("001234.SZ", tol=0.995), 1)
        self._patch([("20261116", edge - 0.01)], [("20261116", up)])
        self.assertEqual(self.mod.compute_consec_boards("001234.SZ", tol=0.995), 0)

    def test_bake_writes_consec_boards(self):
        self._patch([("20261116", 11.0), ("20261115", 10.0)],
                    [("20261116", 11.0), ("20261115", 10.0)])
        out = self.mod.bake_bakeables("001234.SZ", {"precalc": {}, "hot_pool": {}})
        self.assertEqual(out.get("consec_boards"), 2.0)


if __name__ == "__main__":
    unittest.main()
