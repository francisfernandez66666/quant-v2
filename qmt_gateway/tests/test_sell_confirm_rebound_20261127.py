# test_sell_confirm_rebound_20261127.py — §SELL-REBOUND 行为腿（owner 2026-11-27）。
#
# 三笔现场把"确认窗"的真实语义暴露出来：它只确认"触发仍成立"，不等反弹；
# 000009 只因低于 MA5 一分钱就被清掉，卖完却反弹 1.5%。本批给观察窗加第二道闸：
# 价格较窗口最低点反弹 ≥0.7 个百分点就重置计时、保留最低点，跌回去才重新计数。
# 移植腿（BD / 因子战法等）原来说命中即卖，也接进同一道窗。
#
# 本文件只用真实临时目录 + 真跑 run_round，不靠 grep 静态锁说话。
import importlib.util
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime

GW_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REPO = os.path.dirname(GW_DIR)
sys.path.insert(0, GW_DIR)

_SPEC = importlib.util.spec_from_file_location(
    "qmt_strategy_rebound_20261127", os.path.join(GW_DIR, "qmt_internal_strategy.py"))
mod = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(mod)


class _FakeContext(object):
    """只提供 run_round 真正读取的行情面；其余取数在 setUp 里打桩。"""

    def __init__(self, ticks):
        self._ticks = ticks

    def get_full_tick(self, symbols):
        return dict((s, self._ticks.get(s, {})) for s in symbols)

    def get_market_data_ex(self, *args, **kwargs):
        return {}


_MID_SESSION = datetime(2026, 11, 27, 10, 30)


# 直接加载构建产物（而不是仓库源文件）：本批断言的是「烘进沙箱的那一份」的行为，
# 读源码只能证明我写了，不能证明它进了产物。
def _load_sandbox(out_dir):
    builder = os.path.join(REPO, "scripts", "build_local_executor.py")
    proc = subprocess.run([sys.executable, builder, "--target", "sandbox",
                           "--out-dir", out_dir], capture_output=True, text=True,
                          cwd=REPO)
    path = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    if proc.returncode != 0 or not path:
        raise AssertionError("sandbox build failed: %s" % (proc.stdout + proc.stderr)[-800:])
    spec = importlib.util.spec_from_file_location("sandbox_rebound_under_test", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ReboundGateTest(unittest.TestCase):
    """纯函数：反弹闸、阈值来源、关闸语义、per-key 最低点、跨日重置。"""

    def setUp(self):
        mod.PARAMS = {"sell_confirm_rounds": 2, "sell_confirm_sec": 60,
                      "sell_confirm_rebound_pct": 0.7}
        mod._STATE.update({"sell_obs": {}, "sell_obs_day": "", "warned": {}})

    def _check(self, price, when, kind="stop_loss"):
        return mod._confirm_sell("20261127", "000001.SZ", kind, when, price)

    def test_rebound_above_threshold_delays(self):
        self.assertFalse(self._check(9.00, 1000.0))
        self.assertFalse(self._check(9.07, 1061.0))
        entry = mod._STATE["sell_obs"][("20261127", "000001.SZ", "stop_loss")]
        self.assertEqual(entry["why"], "rebound")
        self.assertEqual(entry["rounds"], 1)
        self.assertAlmostEqual(entry["low"], 9.00)

    def test_rebound_below_threshold_confirms(self):
        self.assertFalse(self._check(9.00, 1000.0))
        self.assertFalse(self._check(9.05, 1030.0))
        self.assertTrue(self._check(9.05, 1061.0))

    def test_new_low_rearms_the_clock(self):
        self.assertFalse(self._check(9.00, 1000.0))
        self.assertFalse(self._check(8.99, 1030.0))
        self.assertTrue(self._check(8.98, 1061.0))

    def test_falling_back_to_the_low_rearms(self):
        self.assertFalse(self._check(9.00, 1000.0))
        self.assertFalse(self._check(9.07, 1030.0))  # 反弹：重置计时，保留 low=9.00
        self.assertFalse(self._check(9.01, 1061.0))
        self.assertTrue(self._check(9.01, 1122.0))

    def test_threshold_comes_from_params(self):
        self.assertFalse(self._check(9.00, 1000.0))
        mod.PARAMS["sell_confirm_rebound_pct"] = 5.0
        self.assertTrue(self._check(9.40, 1061.0), "5% 阈值下 +4.4% 不该延后")
        mod.PARAMS["sell_confirm_rebound_pct"] = 0.7
        self.assertFalse(self._check(9.40, 1122.0), "0.7% 阈值下 +4.4% 必须延后")

    def test_take_profit_clear_ignores_the_rebound(self):
        self.assertFalse(mod._confirm_sell("20261127", "000001.SZ",
                                           "take_profit_clear", 1000.0, 9.00,
                                           rebound=False))
        self.assertTrue(mod._confirm_sell("20261127", "000001.SZ",
                                          "take_profit_clear", 1061.0, 9.07,
                                          rebound=False))

    def test_missing_rebound_key_only_disables_the_gate(self):
        mod.PARAMS = {"sell_confirm_rounds": 2, "sell_confirm_sec": 60}
        self.assertFalse(self._check(9.00, 1000.0))
        self.assertTrue(self._check(9.07, 1061.0), "缺阈值只关反弹闸，不关观察窗")
        self.assertTrue(mod._STATE["warned"].get("sell_confirm_rebound_cfg"))

    def test_low_is_per_key(self):
        self.assertFalse(self._check(9.00, 1000.0, "stop_loss"))
        self.assertFalse(self._check(10.00, 1000.0, "reduce"))
        self.assertFalse(self._check(10.07, 1061.0, "reduce"), "reduce 按自己的 low 反弹")
        self.assertTrue(self._check(9.01, 1061.0, "stop_loss"))

    def test_day_change_resets_the_low(self):
        self.assertFalse(self._check(9.00, 1000.0))
        self.assertFalse(mod._confirm_sell("20261128", "000001.SZ",
                                           "stop_loss", 1061.0, 9.07))
        entry = mod._STATE["sell_obs"][("20261128", "000001.SZ", "stop_loss")]
        self.assertAlmostEqual(entry["low"], 9.07)

    def test_zero_threshold_turns_the_gate_off(self):
        mod.PARAMS["sell_confirm_rebound_pct"] = 0
        self.assertFalse(self._check(9.00, 1000.0))
        self.assertTrue(self._check(9.07, 1061.0), "0=关闸，不是恒反弹")
        entry = mod._STATE["sell_obs"][("20261127", "000001.SZ", "stop_loss")]
        self.assertNotEqual(entry.get("why"), "rebound")


class ReboundRoundTest(unittest.TestCase):
    """行为腿：真跑 run_round，看委托、skipped 原因和移植腿的两段式接线。"""

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp(prefix="qmtp-rebound-")
        cls.mod = _load_sandbox(cls.dir)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.dir, ignore_errors=True)

    # 每例都从 PARAMS 快照起步并逐键改：观察窗的等待状态挂在模块进程内，
    # 不清就会把上一例的时刻带进下一例——那是假绿的一种。
    def setUp(self):
        m = self.mod
        self.symbol = "000001.SZ"
        self.saved_params = dict(m.PARAMS)
        m.PARAMS["symbols"] = [self.symbol]
        m.PARAMS["hot_pool"] = {}
        m.PARAMS["sell_confirm_rounds"] = 2
        m.PARAMS["sell_confirm_sec"] = 60
        m.PARAMS["sell_confirm_rebound_pct"] = 0.7
        m.PARAMS["rule_arm"] = dict(m.PARAMS.get("rule_arm") or {})
        m._STATE.update({
            "acct": "8001", "acct_ok": True, "acct_type": "", "ops": [],
            "cancel_tried": [], "warned": {}, "last_round": 0.0,
            "sent": set(), "buy_intents": set(), "buy_amounts": {},
            "buy_seen": {}, "basis": {}, "basis_day": "",
            "sell_obs": {}, "sell_obs_day": "",
        })
        self.placed = []
        self.now = 1000.0
        self.price = 9.00
        m._now_ts = lambda: self.now
        m._trade_session_ok = lambda *a, **k: (True, "")
        m._live_orders = lambda: ([], True)
        m._positions = lambda: {self.symbol: {"qty": 1000, "cost": 10.0,
                                              "can_use": 1000, "last_price": self.price,
                                              "price": self.price}}
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
        # 本组只判"窗是否放行"，成交可能闸（行情字段形状）另行测试，统一放行；
        # 否则 FakeContext 的 lastPrice 命名差异会把窗的读数染成 no_price。
        m.rule_fillable = lambda p, c, side: (True, "")
        m._place = lambda ctx, signal: (self.placed.append(signal) or True, "")
        m.rule_pullback_buy = lambda p, c: None
        m.rule_take_profit = lambda p, c: None
        m.rule_take_profit_clear = lambda p, c: None
        m.rule_reduce_position = lambda p, c: None
        self.stop_hit = {"side": "sell", "symbol": self.symbol,
                         "short_code": "SL000001", "qty": 1000, "price": 9.0,
                         "amount": 9000.0}
        m.rule_stop_loss = lambda p, c: dict(self.stop_hit)
        m._PORTED_RULES = dict(m._PORTED_RULES)
        self._orig_ported = dict(m._PORTED_RULES)
        for kind in self._orig_ported:
            m.PARAMS["rule_arm"][kind] = "off"

    def tearDown(self):
        self.mod.PARAMS.clear()
        self.mod.PARAMS.update(self.saved_params)
        self.mod._PORTED_RULES.clear()
        self.mod._PORTED_RULES.update(self._orig_ported)

    def _round(self, price=None):
        if price is not None:
            self.price = price
        # normalize_tick() 只认 QMT 原生字段名（lastPrice / lastClose / askPrice / bidPrice）。
        ctx = _FakeContext({self.symbol: {"lastPrice": self.price,
                                          "lastClose": 10.0, "volume": 100000,
                                          "askPrice": [self.price + 0.01],
                                          "bidPrice": [self.price - 0.01]}})
        return self.mod.run_round(ctx, _MID_SESSION)

    def _port(self, kind, side, short="BD000001", mode="live"):
        self.mod.PARAMS["rule_arm"][kind] = mode
        self.mod._PORTED_RULES[kind] = (
            lambda p, c, s=side, sc=short: {"side": s, "symbol": self.symbol,
                                            "short_code": sc, "qty": 100,
                                            "price": 9.0, "amount": 900.0},
            "test")

    def test_round_level_rebound_blocks_the_order(self):
        first = self._round(9.00)
        self.assertEqual(self.placed, [])
        self.assertTrue([s for s in first["skipped"]
                         if s["reason"] == "sell_confirm_pending"], first)
        self.now = 1061.0
        second = self._round(9.07)
        self.assertEqual(self.placed, [])
        self.assertTrue([s for s in second["skipped"]
                         if s["reason"] == "sell_confirm_rebound"], second)
        # 9.01 距 low=9.00 仅 +0.11%，低于 0.7% 闸 ⇒ 反弹解除；
        # 从 9.07 那一轮重置的钟满 61 秒，新一轮确认完成，可以卖。
        self.now = 1122.0
        self._round(9.01)
        self.assertEqual(len(self.placed), 1, self.placed[-1:])

    def test_ported_sell_goes_through_the_window(self):
        self.mod.rule_stop_loss = lambda p, c: None
        self._port("break_down", "sell")
        first = self._round(9.00)
        self.assertEqual(self.placed, [])
        self.assertTrue([s for s in first["skipped"]
                         if s["reason"] == "sell_confirm_pending"], first)
        self.now = 1061.0
        second = self._round(9.00)
        self.assertEqual([s["short_code"] for s in self.placed], ["BD000001"], second)

    def test_ported_clock_survives_between_rounds(self):
        """清除循环若跑在移植腿之前，这本钟每轮都被清掉，本例必红。"""
        self.mod.rule_stop_loss = lambda p, c: None
        self._port("break_down", "sell")
        self._round(9.00)
        self.now = 1030.0
        self._round(9.00)
        self.assertEqual(self.placed, [], "秒数未到")
        self.now = 1061.0
        self._round(9.00)
        self.assertEqual(len(self.placed), 1, self.placed[-1:])

    def test_ported_sell_pending_blocks_a_same_round_buy(self):
        """high_churn(sell) 字母序在 dragon(buy) 后，两段式才能拦住同轮买单。"""
        self.mod.rule_stop_loss = lambda p, c: None
        self._port("high_churn", "sell", "HC000001")
        self._port("dragon", "buy", "D000001")
        self.mod._go_admitted = lambda *a, **k: True
        summary = self._round(9.00)
        self.assertEqual(self.placed, [])
        self.assertTrue([s for s in summary["skipped"]
                         if s["reason"] == "sell_pending_blocks_buy"], summary)

    def test_ported_shadow_sell_is_not_gated(self):
        self.mod.rule_stop_loss = lambda p, c: None
        self._port("break_down", "sell", mode="shadow")
        summary = self._round(9.00)
        self.assertEqual(self.placed, [])
        self.assertNotIn(("20261127", self.symbol, "break_down"),
                         (self.mod._STATE.get("sell_obs") or {}))
        self.assertTrue([s for s in summary["skipped"]
                         if s["reason"] == "rule_shadow"], summary)

    def test_breakdown_leg_is_gone(self):
        m = self.mod
        self.assertFalse(hasattr(m, "rule_breakdown_sell"))
        self.assertNotIn("breakdown_sell", m.PARAMS["builtin_rules"])
        self.assertNotIn("breakdown_sell", m.PARAMS["signal_codes"])
        self.assertNotIn("breakdown_ratio", m.PARAMS)
        self.assertNotIn("tp_breakdown_trim_on_profit", m.PARAMS)
        self.assertNotIn("breakdown_sell", [s[0] for s in m._PORTED_RULES.values()])


if __name__ == "__main__":
    unittest.main()
