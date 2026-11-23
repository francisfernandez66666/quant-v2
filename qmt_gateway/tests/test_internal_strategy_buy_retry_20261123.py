# test_internal_strategy_buy_retry_20261123.py — 同票重发闸（2026-11-23）。
#
# 现场：000088（盐田港）13:23:13 / 13:33:47 / 13:44:46 / 13:54:06 四次买单，
# 限价全是 4.56（VWAP×(1-0.7%) 且不高于现价），而当天最低 4.57 ⇒ 永远不可能成交。
# 每次 600 秒被陈旧撤单后 `_reconcile_session_intents` 释放当日买意向（这一步是对的：
# 手动撤单必须能重来），下一轮又按同一个价重发，17k~20k 资金被锁了半小时。
#
# 修法（owner 裁决「同一个票短时间内价格不反转就不要重复下单占用资金」）：
# 撤单释放意向保留，但再发必须**价格真的往下走**（至少低 buy_retry_improve_pct），
# 且同一只票当日最多 buy_retry_max_per_day 次。
#
# English: locks for the same-symbol buy retry gate (no repeat at an unfillable price).
import importlib.util
import os
import sys
import unittest

GW_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, GW_DIR)

_SPEC = importlib.util.spec_from_file_location(
    "qmt_strategy_buy_retry", os.path.join(GW_DIR, "qmt_internal_strategy.py"))
mod = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(mod)

DAY = "20261123"
SYM = "000088.SZ"


class BuyRetryGateTest(unittest.TestCase):
    """纯函数：首单放行、同价/微跌拦住、真回调放行、当日次数上限、跨日重置。"""

    def setUp(self):
        mod.PARAMS = {"buy_retry_improve_pct": 0.2, "buy_retry_max_per_day": 3}
        mod._STATE.update({"buy_tries": {}, "buy_tries_day": "", "warned": {}})

    def test_first_attempt_always_allowed(self):
        self.assertIsNone(mod._buy_retry_gate(DAY, SYM, 4.56))

    def test_same_price_blocked_after_one_attempt(self):
        mod._buy_retry_record(DAY, SYM, 4.56)
        self.assertEqual(mod._buy_retry_gate(DAY, SYM, 4.56), "buy_retry_no_improve")

    def test_higher_price_blocked(self):
        mod._buy_retry_record(DAY, SYM, 4.56)
        self.assertEqual(mod._buy_retry_gate(DAY, SYM, 4.60), "buy_retry_no_improve")

    def test_sub_threshold_dip_blocked(self):
        """4.56 → 4.555 只降了 0.11%，不到 0.2% 的改善线 ⇒ 仍算同一个价。"""
        mod._buy_retry_record(DAY, SYM, 4.56)
        self.assertEqual(mod._buy_retry_gate(DAY, SYM, 4.555), "buy_retry_no_improve")

    def test_real_pullback_allowed(self):
        mod._buy_retry_record(DAY, SYM, 4.56)
        self.assertIsNone(mod._buy_retry_gate(DAY, SYM, 4.55))

    def test_exact_threshold_allowed(self):
        mod._buy_retry_record(DAY, SYM, 5.0)
        self.assertIsNone(mod._buy_retry_gate(DAY, SYM, 4.99))

    def test_daily_cap(self):
        for price in (4.56, 4.55, 4.54):
            self.assertIsNone(mod._buy_retry_gate(DAY, SYM, price))
            mod._buy_retry_record(DAY, SYM, price)
        # 第三次已用满（含首单共 3 次）⇒ 再低的价格也不许发。
        self.assertEqual(mod._buy_retry_gate(DAY, SYM, 4.40), "buy_retry_cap")

    def test_cap_is_per_symbol(self):
        for price in (4.56, 4.55, 4.54):
            mod._buy_retry_record(DAY, SYM, price)
        self.assertIsNone(mod._buy_retry_gate(DAY, "000009.SZ", 4.54))

    def test_day_change_resets(self):
        for price in (4.56, 4.55, 4.54):
            mod._buy_retry_record(DAY, SYM, price)
        self.assertEqual(mod._buy_retry_gate(DAY, SYM, 4.40), "buy_retry_cap")
        self.assertIsNone(mod._buy_retry_gate("20261124", SYM, 4.40))

    def test_unreadable_price_on_retry_fails_closed(self):
        """看不见价不许变成"照发"：只有**再发**这一步 fail-closed，首单不受影响。"""
        self.assertIsNone(mod._buy_retry_gate(DAY, SYM, None))
        mod._buy_retry_record(DAY, SYM, 4.56)
        self.assertEqual(mod._buy_retry_gate(DAY, SYM, None), "buy_retry_price_unknown")
        self.assertEqual(mod._buy_retry_gate(DAY, SYM, 0), "buy_retry_price_unknown")

    def test_missing_config_turns_gate_off(self):
        mod._buy_retry_record(DAY, SYM, 4.56)
        mod.PARAMS = {}
        self.assertIsNone(mod._buy_retry_gate(DAY, SYM, 4.56))
        self.assertTrue(mod._STATE["warned"].get("buy_retry_cfg"),
                        "缺配置必须留一行告警，不能静默")


class BuyRetryWiringTest(unittest.TestCase):
    """静态接线：闸在 _place 之前、t_leg 豁免、只在发单成功后记账。"""

    def _src(self):
        with open(os.path.join(GW_DIR, "qmt_internal_strategy.py"), "r") as fh:
            return fh.read()

    def test_gate_runs_before_place(self):
        src = self._src()
        gate = src.index("_buy_retry_gate(day, symbol, signal.get(\"price\"))")
        place = src.index("ok, why = _place(ContextInfo, signal)", gate - 4000)
        self.assertLess(gate, place, "闸必须排在 _place 之前")

    def test_gate_skips_t_leg(self):
        src = self._src()
        gate = src.index("_buy_retry_gate(day, symbol, signal.get(\"price\"))")
        window = src[max(0, gate - 400):gate]
        self.assertIn('if intent != "t_leg":', window,
                      "T 腿有自己的账本，不许被这道闸拦住")

    def test_record_only_after_success(self):
        src = self._src()
        ok_at = src.index("_buy_retry_record(day, symbol, signal.get(\"price\"))")
        success = src.rindex("if ok:", 0, ok_at)
        self.assertLess(success, ok_at, "只有发单成功才记账")
        self.assertLess(ok_at - success, 400, "记账必须落在成功分支内")

    def test_reconcile_does_not_release_tries(self):
        """撤单释放当日买意向是对的，但不许把重发闸的记忆一起清掉。"""
        src = self._src()
        start = src.index("def _reconcile_session_intents(")
        end = src.index("\ndef ", start + 10)
        body = src[start:end]
        self.assertNotIn("buy_tries", body,
                         "撤单释放把重发闸也放了 ⇒ 同价循环原样回来")


if __name__ == "__main__":
    unittest.main()
