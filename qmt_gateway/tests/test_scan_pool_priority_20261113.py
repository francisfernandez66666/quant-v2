# test_scan_pool_priority_20261113.py — #7 裁决 10 ④：监视池三级优先级截断回归锁。
#
# 缺陷本体：旧实现 `sorted(pool)[:SIGNAL_POOL_MAX]` 是**字典序截断**——池子只增不减，
# 一旦超上限，000/001/002 开头的老代码被永久钉住，新信号 / 600xxx / 688xxx 永远进不来。
# owner 2026-11-13 裁决「两个池都改 1000」并明令「不能只把 600 改成 1000」——
# 必须先修截断：按「当日信号 > 评分 > 自选」三级优先级重建，超限老代码滚动淘汰。
#
# English: regression lock for the priority-based scan-pool truncation in
# scripts/carrier_hot_reload_gz.py::assemble_scan_pool.
import importlib.util
import os
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, REPO)


def _load_module():
    spec = importlib.util.spec_from_file_location(
        "carrier_hot_reload_gz_test", os.path.join(REPO, "scripts", "carrier_hot_reload_gz.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class ScanPoolPriorityTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = _load_module()

    def test_default_cap_is_1000(self):
        """裁决 10：两个池都改 1000（物理机逻辑也是 1000，经重烘下发）。"""
        self.assertEqual(self.mod.SIGNAL_POOL_MAX, 1000)

    def test_tier_order_signals_beat_scores_beat_watchlist(self):
        """三级优先级：当日信号 > 评分（高分在前）> 上一轮自选。"""
        pool = self.mod.assemble_scan_pool(
            prev_symbols=["000001", "000002", "600999"],
            signal_pool=["688001", "600519"],
            bullish={"300750": {"score": 88.5}, "002999": {"score": 91.0}},
            score_rank={"601318": 95.0, "600000": 70.0},
            pool_max=5)
        self.assertEqual(pool, ["600519", "688001", "601318", "002999", "300750"])

    def test_truncation_evicts_stale_watchlist_not_signals(self):
        """超上限截断淘汰 tier3 自选；tier1 信号与 tier2 高分必保留。

        旧字典序实现会把 000001 钉在最前（它字典序最小）——这正是缺陷本体。
        """
        pool = self.mod.assemble_scan_pool(
            prev_symbols=["000001"],
            signal_pool=["600519", "688001"],
            bullish={"300750": {"score": 88.5}},
            score_rank={"601318": 95.0},
            pool_max=4)
        self.assertEqual(pool, ["600519", "688001", "601318", "300750"])
        self.assertNotIn("000001", pool,
                         "旧字典序实现会把 000001 钉死在最前——淘汰语义失效")

    def test_low_prefix_old_codes_no_longer_pinned(self):
        """「字典序截断钉死老代码」的直锤：池满后一只新 600xxx 信号必须能进，
        被挤出去的是低优先级的 000/001 开头老自选，而不是新信号。"""
        prev = ["000%02d" % i for i in range(1, 12)]  # 11 只 000/001 开头老自选
        pool = self.mod.assemble_scan_pool(
            prev_symbols=prev,
            signal_pool=["600999"],       # 新信号（字典序排在 000 系后面）
            bullish={}, score_rank={},
            pool_max=11)
        self.assertIn("600999", pool,
                      "新信号被字典序截断挤出＝缺陷复活（000 系钉死在最前）")
        self.assertEqual(len(pool), 11)
        # 恰好多出一只被淘汰：被淘汰的应是自选（tier3），不是新信号
        evicted = [c for c in prev if c not in pool]
        self.assertEqual(len(evicted), 1)

    def test_idempotent_rerun_same_output(self):
        """重烘幂等：同一输入跑两遍逐字节一致（烘焙 sha 纪律依赖这一点）。"""
        args = dict(
            prev_symbols=["000001", "600000"],
            signal_pool=["600519"],
            bullish={"300750": {"score": 88.5}},
            score_rank={"601318": 95.0},
            pool_max=100)
        first = self.mod.assemble_scan_pool(**args)
        args2 = dict(args)
        args2["prev_symbols"] = first  # 上一轮产物回灌（真实的重烘形态）
        second = self.mod.assemble_scan_pool(**args2)
        self.assertEqual(first, second)

    def test_degenerate_inputs(self):
        """空/None 输入不炸：空池返回空清单；pool_max<=0 返回空（不冒充全池）。"""
        self.assertEqual(self.mod.assemble_scan_pool(None, None, None, None, 10), [])
        self.assertEqual(self.mod.assemble_scan_pool(["000001"], ["600519"], {}, {}, 0), [])
        # bullish 行缺 score / 非法 score 都按 0 处理，不抛异常
        pool = self.mod.assemble_scan_pool([], [], {"600000": {}}, {}, 5)
        self.assertEqual(pool, ["600000"])
        pool = self.mod.assemble_scan_pool([], [], {"600000": {"score": "bad"}}, {}, 5)
        self.assertEqual(pool, ["600000"])


if __name__ == "__main__":
    unittest.main()
