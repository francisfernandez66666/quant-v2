# 2026-11-17 owner 令「全套自闭环、接全接透」A 组机内 py 接线的行为锁。
#
# 靶子（每条都对应施工图 §11/§12 一个"做了一半"缺陷，故意破坏必须让这条红）：
#  T1a pos_view 给移植 rules_t 补 can_use_qty（柜台行只写 can_use，旧版做T 全哑）
#  T1b holdings_value 用 market_value（旧 `price or cost` 在 pos_view 行退化成本价）
#  P0-A 订阅真接 getattr + _short_id 把 -1 判 bad（旧 _fn 恒 None + -1 冒充成功编号）
#  C1  news_event 组成 good_news_fade 要的 dict（旧透传 float，形状不匹配恒 abstain）
#  leader_decay 读数 build_context 透传（旧从不写 is_leader/ladder 等，live 战法结构性死）
#  stop_loss 只在订到价后才出信号（这条同时证伪「阈值有问题」与「订阅没问题」）
#
# 英文：behaviour locks for the A-group wiring; each is paired with a mutation that
# must turn it red (see the gate §191/§194 negative locks too).

import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GATEWAY = os.path.dirname(HERE)
REPO = os.path.dirname(GATEWAY)
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")
STRATEGY_SRC = os.path.join(GATEWAY, "qmt_internal_strategy.py")


# 跑真构建器产沙箱产物再 load 成模块：A 组接线全是机内 py 里的函数，只能对"真产物"取证。
def _build_and_load():
    out_dir = tempfile.mkdtemp()
    proc = subprocess.run(
        [sys.executable, BUILDER, "--target", "sandbox", "--out-dir", out_dir],
        capture_output=True, text=True, cwd=REPO)
    if proc.returncode != 0:
        raise AssertionError("sandbox build failed: %s" % (proc.stdout + proc.stderr)[-800:])
    path = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    spec = importlib.util.spec_from_file_location("wiring_artifact_under_test", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class _FakeContextInfo:
    """Minimal ContextInfo surface; subscribe fns are opt-in to test the ladder."""

    def __init__(self, ticks=None, with_subscribe=True, whole_mode="list"):
        self._ticks = ticks or {}
        self.whole_calls = []
        self.quote_calls = []
        if with_subscribe:
            if whole_mode == "list":
                self.subscribe_whole_quote = lambda cl: (self.whole_calls.append(cl), 11)[1]
            elif whole_mode == "raise":
                def _bad(_cl):
                    raise RuntimeError("boom")
                self.subscribe_whole_quote = _bad
            # whole_mode == "absent" -> no attr set at all
            self.subscribe_quote = lambda code, period="", typ="": (
                self.quote_calls.append(code), 7)[1]

    def get_full_tick(self, codes):
        return {c: self._ticks[c] for c in codes if c in self._ticks}


class WiringLocks20261117(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = _build_and_load()

    # ---- short id: -1 is a failure sentinel, must not read like a number ----
    def test_short_id_treats_negative_as_bad_not_a_number(self):
        sid = self.mod._short_id
        self.assertEqual(sid(-1), "bad")
        self.assertEqual(sid(None), "none")
        self.assertEqual(sid("x"), "none")
        self.assertEqual(sid(0), "0")
        self.assertEqual(sid(3), "3")

    # ---- P0-A: subscribe resolved by getattr, never via _fn whitelist ----
    def test_subscribe_fns_present_and_absent_are_distinguished(self):
        present = _FakeContextInfo(with_subscribe=True)
        _whole, _per, api = self.mod._subscribe_fns(present)
        self.assertEqual(api, "present")
        absent = _FakeContextInfo(with_subscribe=False)
        _w2, _p2, api2 = self.mod._subscribe_fns(absent)
        self.assertEqual(api2, "absent")

    def test_subscribe_market_calls_the_literal_fn_not_the_fn_helper(self):
        ci = _FakeContextInfo(with_subscribe=True, whole_mode="list")
        self.mod._subscribe_market(ci)
        # the list-market ladder must have actually been invoked
        self.assertTrue(ci.whole_calls, "whole_quote never called -> the empty-wiring bug is back")

    # ---- C1: news_event composed into the dict good_news_fade needs ----
    def test_news_event_obj_builds_dict_from_score_plus_extras(self):
        obj = self.mod._news_event_obj
        full = obj({"news_event": 0.7, "news_day": "20261116", "news_propagation": True})
        self.assertIsInstance(full, dict)
        self.assertTrue(full["positive"])
        self.assertEqual(full["day"], "20261116")
        self.assertTrue(full["propagation"])
        bear = obj({"news_event": -0.5, "news_day": "20261116", "news_propagation": False})
        self.assertFalse(bear["positive"])
        # score alone, no event metadata -> abstain rather than fabricate propagation/day
        self.assertIsNone(obj({"news_event": 0.9}))

    # ---- leader_decay inputs must be on build_context output ----
    def test_build_context_passes_the_leader_and_emo_inputs(self):
        src = open(STRATEGY_SRC, encoding="utf-8").read()
        for key in ('"is_leader"', '"ladder_height"', '"afternoon_reseal"',
                    '"up_stop"', '"total_share_series"',
                    '"limit_up_count"', '"broken_board_ratio"', '"seal_amount_ratio"'):
            self.assertIn(key, src,
                          "build_context must mirror the input_key of the live rules "
                          "that were structurally dead (got None -> honest decline)")

    # ---- the linchpin: stop_loss only fires once a real price is present ----
    def test_stop_loss_fires_only_after_subscription(self):
        P = self.mod.PARAMS
        P = dict(P)
        # ensure a non-zero stop ratio so the rule can fire
        P["stop_loss_ratio"] = 0.97
        blind_ctx = {"trading_day": True, "last_price": None,
                     "position_cost": 10.0, "position_qty": 1000.0}
        self.assertIsNone(self.mod.rule_stop_loss(P, blind_ctx),
                          "stop_loss must return None when there is no price")
        hit_ctx = dict(blind_ctx)
        hit_ctx["last_price"] = 9.5  # below 10*0.97 stop line
        sig = self.mod.rule_stop_loss(P, hit_ctx)
        self.assertIsNotNone(sig, "stop_loss must fire once price is subscribed and past the line")
        self.assertEqual(sig["side"], "sell")

    # ---- artifact carries no forbidden empty-wiring helper for subscribe ----
    def test_artifact_has_no_fn_subscribe_helper(self):
        self.assertNotIn('_fn("subscribe', self.mod.__doc__ or "")
        text = open(self.mod.__file__, encoding="utf-8", errors="replace").read()
        self.assertNotIn('_fn("subscribe', text)
        self.assertIn('getattr(ContextInfo, "subscribe_whole_quote"', text)


class StaleCancelFix20261117(unittest.TestCase):
    """STALE-CANCEL-FIX: the box could never compute order age, so unfilled orders
    were only ever cleared by the 14:50 close sweep (owner report; log showed
    cancelled=[] every round). The fix lives in _order_submit_epoch turning the
    counter's m_strInsertTime into a usable epoch. Each input shape is pinned."""

    @classmethod
    def setUpClass(cls):
        cls.mod = _build_and_load()

    class _Item:
        pass

    def _epoch_at(self, insert_time, now_ts):
        m = self.mod
        item = StaleCancelFix20261117._Item()
        item.m_strInsertTime = insert_time
        orig = m._now_ts
        m._now_ts = lambda: now_ts
        try:
            return m._order_submit_epoch(item)
        finally:
            m._now_ts = orig

    def test_string_insert_time_yields_computable_age(self):
        # an order stamped 13:05:00, round clock at 13:20:00 (Beijing) -> age 900s.
        # now_ts = today Beijing 13:20:00 epoch; build it as midnight + 13h20m.
        sod = lambda h, mi, s: h * 3600 + mi * 60 + s
        midnight = 1763000000 - ((1763000000 + 28800) % 86400)  # today's BJ midnight epoch
        now = midnight + sod(13, 20, 0)
        epoch = self._epoch_at("13:05:00", now)
        self.assertIsNotNone(epoch, "string time must yield an epoch, not None")
        self.assertAlmostEqual(now - epoch, 900.0, places=2)
        self.assertGreaterEqual(now - epoch, 600.0, "old enough => stale fires (>=stale_order_sec)")

    def test_epoch_like_mnordertime_used_directly(self):
        m = self.mod
        item = StaleCancelFix20261117._Item()
        item.m_nOrderTime = 1763000000
        item.m_strInsertTime = ""
        orig = m._now_ts
        m._now_ts = lambda: 1763006000
        try:
            self.assertEqual(m._order_submit_epoch(item), 1763000000)
        finally:
            m._now_ts = orig

    def test_unparseable_time_is_none_never_wrongly_old(self):
        # fail-safe: no clock => None => age unknown => stale must NOT fire (never
        # wrongly cancel). This is the direction that protects live orders.
        self.assertIsNone(self._epoch_at("", 1763000000))
        self.assertIsNone(self._epoch_at("xx:yy", 1763000000))

    def test_counter_codes_treats_cancelled_unfilled_as_replayable(self):
        # the sandbox build must filter cancel_like rows out of the same-day block
        # set (owner "re-place after cancel") while keeping open/filled blocked.
        text = open(self.mod.__file__, encoding="utf-8", errors="replace").read()
        self.assertIn('if not row.get("cancel_like"))', text,
                      "counter_codes must drop only cancel-and-unfilled rows")
        self.assertIn('order_time": _order_submit_epoch', text,
                      "_live_orders must parse order time, not float a missing field")


class TEquityFallback20261117(unittest.TestCase):
    """T cap-base equity: counter first, else a FRESH baked total_asset.

    This broker's ACCOUNT table does not fill m_dBalance intraday -> the old
    t_cap_base had no denominator and T was dead all day. The fix adds a
    freshness-gated fallback to the outer-brain-baked total_asset, scoped to
    the T ctx only (strategic buy sizing untouched).
    """

    @classmethod
    def setUpClass(cls):
        cls.mod = _build_and_load()

    def test_counter_reading_always_wins(self):
        # a real counter number must never be replaced by a baked one
        self.assertEqual(
            self.mod._t_equity_basis(123456.78, {"total_asset": 999.0, "asof_age_sec": 1}, 900),
            123456.78)

    def test_missing_counter_uses_fresh_baked_total_asset(self):
        self.assertEqual(
            self.mod._t_equity_basis(None, {"total_asset": 100403.53, "asof_age_sec": 30}, 900),
            100403.53)

    def test_stale_baked_equity_is_refused(self):
        # old equity must never size a live T leg -> fail closed
        self.assertIsNone(
            self.mod._t_equity_basis(None, {"total_asset": 100403.53, "asof_age_sec": 1200}, 900))

    def test_missing_age_is_refused(self):
        self.assertIsNone(
            self.mod._t_equity_basis(None, {"total_asset": 100403.53}, 900))

    def test_no_freshness_window_refuses_baked(self):
        # stale_sec<=0 means "no trust window" -> do not size off a baked number
        self.assertIsNone(
            self.mod._t_equity_basis(None, {"total_asset": 100403.53, "asof_age_sec": 5}, 0))

    def test_baked_artifact_carries_the_fallback_wire(self):
        # the baked sandbox artifact must actually contain the fallback wire,
        # not just the template source (builder inlines/port rules)
        text = open(self.mod.__file__, encoding="utf-8", errors="replace").read()
        self.assertIn("_t_equity_basis", text)
        self.assertIn('"total_asset": t_total', text)


if __name__ == "__main__":
    unittest.main()
