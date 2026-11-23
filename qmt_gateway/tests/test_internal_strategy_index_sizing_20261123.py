# test_internal_strategy_index_sizing_20261123.py — 盒内指数涨跌 + 云端上限（2026-11-23）。
#
# 现场：盒上烘焙 market_state_enabled=False、emotion_enabled=True、
# missing_key_target_cap=0.5，而 index_change_pct 从来没有盒内读数 ⇒ 每一轮
# dims=['index_change'] ⇒ market_scale 恒 0.5000 ⇒ cash_probe room 只剩两千，
# 整天 hard_cap_below_floor，一笔新仓都开不出来。
#
# 改法两步（owner 2026-11-23 裁决）：
#   1. 指数日 K 盒内自算 index_change_pct（同源两根收盘价，不乘 100）；
#   2. 云端 market_scale 当上限再打 0.9 折扣（今日 0.7946×0.9=0.7152），
#      外脑读数缺失/过期 ⇒ 退回盲区帽 0.5（断更只会更保守）。
# 反转的边界：只有 index_change 例外；breadth / macro / state 继续弃权。
#
# English: locks for the in-box index change reading and the cloud-derived ceiling.
import importlib.util
import os
import sys
import tempfile
import unittest
from datetime import datetime

GW_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, GW_DIR)
sys.path.insert(0, os.path.dirname(GW_DIR))

import rules_sizing  # noqa: E402

_MOMENT = datetime(2026, 11, 23, 10, 30)
_TODAY = "20261123"


def build_sandbox(out_dir):
    import subprocess
    builder = os.path.join(os.path.dirname(GW_DIR), "scripts", "build_local_executor.py")
    proc = subprocess.run([sys.executable, builder, "--target", "sandbox",
                           "--out-dir", out_dir], capture_output=True, text=True,
                          cwd=os.path.dirname(GW_DIR))
    path = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    return proc.returncode, proc.stdout + proc.stderr, path


def load_module(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class _Entry(object):
    def __init__(self, closes, times):
        self.close = closes
        self.volume = [1 for _ in closes]
        self.amount = [1 for _ in closes]
        self._times = times

    @property
    def index(self):
        return self._times


class _FakeContext(object):
    def __init__(self, bars):
        self._bars = bars

    def get_market_data_ex(self, *args, **kwargs):
        return self._bars


class IndexSizingTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.mod = load_module(path, "sandbox_index_sizing_under_test")

    def setUp(self):
        self.mod._STATE.update({"index_change_state": {}, "warned": {}})
        self.saved = dict(self.mod.PARAMS)

    def tearDown(self):
        self.mod.PARAMS.clear()
        self.mod.PARAMS.update(self.saved)

    def _ctx(self, closes, times):
        code = "000001.SH"
        return _FakeContext({code: _Entry(closes, times)})

    def test_two_bars_give_todays_change(self):
        ctx = self._ctx([10.0, 10.185], ["202611201500", "202611231030"])
        self.assertAlmostEqual(self.mod._index_change_pct(ctx, _MOMENT), 1.85, places=4)

    def test_last_bar_not_today_abstains(self):
        ctx = self._ctx([10.0, 10.185], ["2026111901500", "202611201500"])
        self.assertIsNone(self.mod._index_change_pct(ctx, _MOMENT),
                          "拿昨天的涨跌冒充今天")

    def test_single_bar_abstains(self):
        ctx = self._ctx([10.0], ["202611231030"])
        self.assertIsNone(self.mod._index_change_pct(ctx, _MOMENT))

    def test_no_bars_abstains(self):
        self.assertIsNone(self.mod._index_change_pct(_FakeContext({}), _MOMENT))

    def test_missing_index_key_keeps_the_blind_spot_cap(self):
        ctx = {"emotion_phase": "ferment"}
        scale, dims = rules_sizing.market_scale(self.mod.PARAMS, ctx)
        self.assertAlmostEqual(scale, 0.5, places=6)
        self.assertIn("index_change", dims)

    def test_real_index_reading_raises_the_scale(self):
        ctx = {"emotion_phase": "ferment", "index_change_pct": -1.085}
        scale, dims = rules_sizing.market_scale(self.mod.PARAMS, ctx)
        self.assertAlmostEqual(scale, 0.8955515053110695, places=6)
        self.assertEqual(dims, [])

    def test_cloud_ceiling_clamps_to_the_conservative_tier(self):
        ceiling = 0.7945711681887514 * 0.9
        ctx = {"emotion_phase": "ferment", "index_change_pct": -1.085,
               "market_scale_ceiling": ceiling}
        scale, _dims = rules_sizing.market_scale(self.mod.PARAMS, ctx)
        self.assertAlmostEqual(scale, ceiling, places=9)

    def test_ceiling_helper_uses_cloud_scale_times_margin(self):
        self.mod.PARAMS["t_macro"] = {"market_scale": 0.7945711681887514,
                                      "asof_age_sec": 60}
        self.assertAlmostEqual(self.mod._market_scale_ceiling(),
                               0.7945711681887514 * 0.9, places=9)

    def test_stale_cloud_snapshot_falls_back_to_blind_cap(self):
        self.mod.PARAMS["t_macro"] = {"market_scale": 0.7945711681887514,
                                      "asof_age_sec": 10 ** 6}
        self.assertAlmostEqual(self.mod._market_scale_ceiling(), 0.5)

    def test_missing_cloud_snapshot_falls_back_to_blind_cap(self):
        self.mod.PARAMS.pop("t_macro", None)
        self.assertAlmostEqual(self.mod._market_scale_ceiling(), 0.5)

    def test_missing_margin_never_gives_an_unbounded_ceiling(self):
        self.mod.PARAMS["t_macro"] = {"market_scale": 0.7945711681887514,
                                      "asof_age_sec": 60}
        self.mod.PARAMS.pop("market_scale_ceiling_margin", None)
        self.assertAlmostEqual(self.mod._market_scale_ceiling(), 0.5)
        self.assertTrue(self.mod._STATE["warned"].get("ms_ceiling_cfg"))

    def test_only_the_index_dimension_was_reversed(self):
        """11-17 纪律：breadth / macro / market_state 仍缺席，不许喂默认值。"""
        text = open(os.path.join(GW_DIR, "qmt_internal_strategy.py"),
                    encoding="utf-8").read()
        start = text.index("def _sizing_view(")
        end = text.index("def ", start + 10)
        body = text[start:end]
        for key in ('out["breadth_up_ratio"]', 'out["macro_events"]',
                    'out["market_state"]', 'out["risk_tier"]'):
            self.assertNotIn(key, body, "给弃权维度喂了值：%s" % key)
        self.assertIn('out["market_scale_ceiling"]', body)


if __name__ == "__main__":
    unittest.main()
