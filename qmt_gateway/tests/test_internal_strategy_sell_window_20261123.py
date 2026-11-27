# test_internal_strategy_sell_window_20261123.py — 盒内卖出观察窗 + 阶梯基座（2026-11-23）。
#
# 现场两起：
#   000672：-3% 减半（09:31:12）→ 剩余 1200 股成本被柜台改写成 12.9658 →
#           58 秒后 -7.53% 命中清仓线（09:31:57）⇒ 一跳清仓。
#   000009：10:13 只因低于 MA5×0.96 一分钱就被已退役的破位腿清掉。
#
# 两处根因分别落成两个机制：
#   §SELL-BASIS-FREEZE：阶梯基座用**我们真正付出的成本**（_basis_of），减仓后冻结成本、
#     只跟数量；跨日重置。柜台重新摊薄剩余股的成本不再抬高清仓线。
#   §SELL-OBS-WINDOW：stop_loss / take_profit_clear / reduce 必须连续命中 ≥N 轮且 ≥S 秒才放单；
#     take_profit 不在本批；清仓线永不被 bull 信号延后；观察窗只推迟时机，不改方向与数量。
#
# English: locks for the in-box sell observation window and the frozen cost basis.
import importlib.util
import os
import sys
import tempfile
import unittest
from datetime import datetime

GW_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, GW_DIR)

_SPEC = importlib.util.spec_from_file_location(
    "qmt_strategy_sell_window", os.path.join(GW_DIR, "qmt_internal_strategy.py"))
mod = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(mod)


class ConfirmSellWindowTest(unittest.TestCase):
    """纯函数：轮数 + 秒数双闸；缺配置＝关（今天行为）；跨日重置。"""

    def setUp(self):
        mod.PARAMS = {"sell_confirm_rounds": 2, "sell_confirm_sec": 60}
        mod._STATE.update({"sell_obs": {}, "sell_obs_day": "", "warned": {}})

    def test_first_hit_is_pending(self):
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1000.0, 9.0))

    def test_rounds_met_but_seconds_not(self):
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1000.0, 9.0))
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1030.0, 9.0))

    def test_both_met_fires(self):
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1000.0, 9.0))
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1030.0, 9.0))
        self.assertTrue(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1061.0, 9.0))

    def test_clear_restarts_the_clock(self):
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1000.0, 9.0))
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1030.0, 9.0))
        mod._sell_obs_clear("20261123", "000672.SZ", "stop_loss")
        # 重新命中：轮数与起点都从 0 开始，不能靠"刚才那两轮"放行。
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1100.0, 9.0))

    def test_day_change_resets_everything(self):
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1000.0, 9.0))
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1030.0, 9.0))
        self.assertFalse(mod._confirm_sell("20261124", "000672.SZ", "stop_loss", 1061.0, 9.0))

    def test_kinds_are_independent(self):
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1000.0, 9.0))
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "take_profit_clear", 1000.0, 9.0))
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1030.0, 9.0))
        self.assertTrue(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1061.0, 9.0))
        # take_profit_clear 自己那本钟只走了 2 轮 / 30 秒 ⇒ 不能跟着 stop_loss 一起放行。
        self.assertFalse(mod._confirm_sell("20261123", "000672.SZ", "take_profit_clear", 1030.0, 9.0))

    def test_missing_config_turns_window_off(self):
        mod.PARAMS = {}
        self.assertTrue(mod._confirm_sell("20261123", "000672.SZ", "stop_loss", 1000.0, 9.0))
        self.assertTrue(mod._STATE["warned"].get("sell_confirm_cfg"),
                        "缺配置必须留一行告警，不能静默")


class BasisFreezeTest(unittest.TestCase):
    """§SELL-BASIS-FREEZE：减仓冻结成本、只跟数量；加仓采用加权成本；跨日重置。"""

    def setUp(self):
        mod._STATE.update({"basis": {}, "basis_day": ""})

    def test_reduce_freezes_cost(self):
        cost = mod._basis_of("000672.SZ", "20261123", 12.5039, 2300)
        self.assertAlmostEqual(cost, 12.5039)
        # 柜台把剩余 1200 股的成本改写为 12.9658 —— 不许采用。
        cost = mod._basis_of("000672.SZ", "20261123", 12.9658, 1200)
        self.assertAlmostEqual(cost, 12.5039, msg="减仓后成本被柜台抬高了")

    def test_add_adopts_the_counter_blend(self):
        """加仓（数量变大）：柜台给的加权成本就是真实基座，采用它。"""
        mod._basis_of("000001.SZ", "20261123", 10.0, 1000)
        cost = mod._basis_of("000001.SZ", "20261123", 10.5, 2000)
        self.assertAlmostEqual(cost, 10.5)

    def test_same_qty_keeps_basis(self):
        mod._basis_of("000001.SZ", "20261123", 10.0, 1000)
        self.assertAlmostEqual(mod._basis_of("000001.SZ", "20261123", 10.9, 1000), 10.0)

    def test_new_day_resets(self):
        mod._basis_of("000001.SZ", "20261123", 10.0, 1000)
        self.assertAlmostEqual(mod._basis_of("000001.SZ", "20261124", 12.0, 1000), 12.0)

    def test_unreadable_cost_abstains(self):
        self.assertIsNone(mod._basis_of("000001.SZ", "20261123", None, 1000))


# ---------------------------------------------------------------------------
# 行为腿：真跑 run_round，看观察窗在"命中但未确认"时不发单、"确认后"发单。
# ---------------------------------------------------------------------------
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


class _FakeContext(object):
    def __init__(self, ticks):
        self._ticks = ticks

    def get_full_tick(self, symbols):
        return dict((s, self._ticks.get(s, {})) for s in symbols)

    def get_market_data_ex(self, *args, **kwargs):
        return {}


_MID_SESSION = datetime(2026, 11, 23, 10, 30)


class SellWindowBehaviorTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.mod = load_module(path, "sandbox_sell_window_under_test")

    def setUp(self):
        # 每个用例都从同一份基线起跑：观察窗/基座都是按日键的模块状态，
        # 不复位就会让上一个用例的观察计数漏进下一个（"首见即计"会读到别人的时钟）。
        m = self.mod
        self.symbol = "000001.SZ"
        self.saved_params = dict(m.PARAMS)
        m.PARAMS["symbols"] = [self.symbol]
        m.PARAMS["hot_pool"] = {}
        m.PARAMS["sell_confirm_rounds"] = 2
        m.PARAMS["sell_confirm_sec"] = 60
        m._STATE.update({
            "acct": "8001", "acct_ok": True, "acct_type": "", "ops": [],
            "cancel_tried": [], "warned": {}, "last_round": 0.0,
            "sent": set(), "buy_intents": set(), "buy_amounts": {},
            "buy_seen": {}, "basis": {}, "basis_day": "",
            "sell_obs": {}, "sell_obs_day": "",
        })
        self.placed = []
        self.now = 1000.0
        # 一切副作用与取数都换成假函数：本用例只判观察窗。
        m._now_ts = lambda: self.now
        m._trade_session_ok = lambda *a, **k: (True, "")
        m._live_orders = lambda: ([], True)
        m._positions = lambda: {self.symbol: {"qty": 1000, "cost": 10.0,
                                              "can_use": 1000, "last_price": 9.0}}
        m._cash = lambda: 100000.0
        m._account_total_asset = lambda: 100000.0
        m._deals = lambda: []
        m._probe_price_channels = lambda *a, **k: None
        m._subscribe_held = lambda *a, **k: None
        m._subscribe_1m_held = lambda *a, **k: None
        m._batch_ticks = lambda *a, **k: None
        m._flow_round_prepare = lambda *a, **k: None
        m._flow_readout = lambda *a, **k: None
        m._pump_legacy_downloads = lambda *a, **k: None
        m._reconcile_session_intents = lambda *a, **k: None
        m._place = lambda ctx, signal: (self.placed.append(signal) or True, "")
        m.rule_pullback_buy = lambda p, c: None
        m.rule_take_profit = lambda p, c: None
        m.rule_reduce_position = lambda p, c: None
        m.rule_no_new_order_window = lambda p, c: (True, "")
        m.rule_ops_frequency_ok = lambda p, c: True
        m.rule_fillable = lambda p, c, side: (True, "")
        self.stop_hit = {"side": "sell", "symbol": self.symbol,
                         "short_code": "SL000001", "qty": 1000, "price": 9.0,
                         "amount": 9000.0}
        m.rule_stop_loss = lambda p, c: dict(self.stop_hit)

    def tearDown(self):
        self.mod.PARAMS.clear()
        self.mod.PARAMS.update(self.saved_params)

    def _round(self):
        ctx = _FakeContext({self.symbol: {"last_price": 9.0, "prev_close": 10.0,
                                          "volume": 100000, "ask1": 9.01, "bid1": 9.0}})
        return self.mod.run_round(ctx, _MID_SESSION)

    def test_pending_clear_does_not_sell_and_does_not_buy(self):
        first = self._round()
        self.assertEqual(self.placed, [], "第一跳就清仓")
        self.assertTrue([s for s in first["skipped"] if s["reason"] == "sell_confirm_pending"],
                        first)

    def test_confirmed_after_window_sells(self):
        self._round()
        self.now = 1010.0
        self._round()
        self.assertEqual(self.placed, [], "秒数未到就放单了")
        self.now = 1061.0
        summary = self._round()
        self.assertEqual(len(self.placed), 1, summary)
        self.assertEqual(self.placed[0]["short_code"], "SL000001")

    def test_bull_never_overrides_the_clear(self):
        self.mod.rule_pullback_buy = lambda p, c: {"side": "buy", "symbol": self.symbol,
                                                   "short_code": "PB000001", "qty": 100,
                                                   "price": 9.0, "amount": 900.0}
        self._round()
        self.now = 1010.0
        self._round()
        self.now = 1061.0
        summary = self._round()
        self.assertEqual(len(self.placed), 1, "bull 信号把清仓延后了：%s" % summary)
        self.assertEqual(self.placed[0]["side"], "sell")


    def test_take_profit_is_not_in_the_window(self):
        self.mod.rule_stop_loss = lambda p, c: None
        self.mod.rule_take_profit = lambda p, c: dict(
            self.stop_hit, short_code="TP000001")
        summary = self._round()
        self.assertEqual(len(self.placed), 1, "止盈也被观察窗拖住了：%s" % summary)


if __name__ == "__main__":
    unittest.main()
