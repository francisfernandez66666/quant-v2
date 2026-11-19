# test_hotreload_static_params.py -- §PARAMS-STATIC-BASE (2026-11-19) locks.
#
# 现网事故：仓库真源 stale_order_sec=600（10 分钟超时撤单）与 close_sweep_from=14:50
# 都已裁定，但 gz 上的运行态参数文件自 11-12 起不再随 rsync 更新，烘出来的产物一直
# 是 900 / 14:52 —— 撤单腿因此永不成立。这一组锁定合并方向：
#
#   M1 静态旋钮以 base（仓库真源）为准，运行态键以 gz 现值为准；
#   M2 只存在于运行态文件里的新键必须保留（11-12 把 600 池冲掉的形态）；
#   M3 顶层 strategy_version 必须保留运行态（版本号单调，不能被旧 base 盖回去）；
#   M4 每次修正都要回报，供日志逐键留痕（静默改活策略的参数是不允许的）。
import importlib.util
import json
import os
import unittest

HR_PATH = os.path.join(os.path.dirname(__file__), "..", "..", "scripts",
                       "carrier_hot_reload_gz.py")
spec = importlib.util.spec_from_file_location("hotreload_static_params_test", HR_PATH)
hr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hr)


def _doc(params, version="v109-20261119"):
    return {"strategy_version": version, "params": dict(params)}


class TestMergeStaticParams(unittest.TestCase):
    def test_m1_static_keys_follow_base_runtime_keys_follow_runtime(self):
        base = _doc({"stale_order_sec": 600, "close_sweep_from": "14:50",
                     "symbols": ["base-symbol"], "hot_pool": {"bullish": {}}})
        runtime = _doc({"stale_order_sec": 900, "close_sweep_from": "14:52",
                        "symbols": ["runtime-symbol"],
                        "hot_pool": {"bullish": {"000001": {"score": 90}}}})
        merged, patched = hr.merge_static_params(runtime, base)
        params = merged["params"]
        # 静态：base 赢
        self.assertEqual(params["stale_order_sec"], 600)
        self.assertEqual(params["close_sweep_from"], "14:50")
        # 运行态：runtime 赢
        self.assertEqual(params["symbols"], ["runtime-symbol"])
        self.assertEqual(params["hot_pool"], {"bullish": {"000001": {"score": 90}}})
        # 修正必须逐键回报（旧值 -> 新值）
        got = {k: (old, new) for k, old, new in patched}
        self.assertEqual(got["stale_order_sec"], (900, 600))
        self.assertEqual(got["close_sweep_from"], ("14:52", "14:50"))
        self.assertNotIn("symbols", got)

    def test_m2_runtime_only_key_survives(self):
        base = _doc({"stale_order_sec": 600})
        runtime = _doc({"stale_order_sec": 600, "brand_new_runtime": {"x": 1}})
        merged, _ = hr.merge_static_params(runtime, base)
        self.assertEqual(merged["params"]["brand_new_runtime"], {"x": 1})

    def test_m2b_new_static_key_lands_from_base(self):
        base = _doc({"stale_order_sec": 600, "new_static_knob": 7})
        runtime = _doc({"stale_order_sec": 600})
        merged, patched = hr.merge_static_params(runtime, base)
        self.assertEqual(merged["params"]["new_static_knob"], 7)
        self.assertEqual(patched, [])  # adding a key is not a re-tune of a live one

    def test_m3_top_level_version_kept_from_runtime(self):
        base = _doc({"stale_order_sec": 600}, version="v001-20260101")
        runtime = _doc({"stale_order_sec": 900}, version="v109-20261119")
        merged, _ = hr.merge_static_params(runtime, base)
        self.assertEqual(merged["strategy_version"], "v109-20261119")

    def test_m4_base_digest_changes_when_file_changes(self):
        # sha 载荷要能感知"只改了静态旋钮"：base 内容变 -> digest 变。
        import tempfile
        tmp = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False,
                                          encoding="utf-8")
        tmp.write(json.dumps(_doc({"stale_order_sec": 600})))
        tmp.close()
        old = hr._params_base_path
        hr._params_base_path = lambda: tmp.name
        try:
            first = hr.static_base_digest()
            with open(tmp.name, "w", encoding="utf-8") as fh:
                json.dump(_doc({"stale_order_sec": 300}), fh)
            second = hr.static_base_digest()
        finally:
            hr._params_base_path = old
            os.unlink(tmp.name)
        self.assertTrue(first)
        self.assertNotEqual(first, second)

    def test_m5_absent_base_is_reported_not_fatal(self):
        old_path = hr._params_base_path
        old_die = hr.die
        called = []
        hr._params_base_path = lambda: "/nonexistent/local_executor_params.base.json"
        hr.die = lambda reason: called.append(reason)
        try:
            n = hr.apply_static_params_base()
        finally:
            hr._params_base_path = old_path
            hr.die = old_die
        self.assertEqual(n, 0)
        self.assertEqual(called, [])


if __name__ == "__main__":
    unittest.main()
