#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§CARRIER-PARITY-1105（2026-11-05）载体与现网同款的三条钱路径行为锁。

owner 裁决「载体也要实现同款撤单/改价/预算行为」。本文件锁的是**行为**，
不是函数存在：每个用例都调真的 `_maybe_send` / `cancel_leg` / 纯判据，
用 FakeBroker 收真动作、用 sqlite journal 收真留痕。

变异反证（写用例时逐个改回旧行为确认过会红）：
  * `rule_room_limit` 把 `room - others` 改回 `room + others` ⇒ 同轮第二笔放行；
  * 撤单理由里去掉 `gone` 分支 ⇒ 信号消失的在途单永远留着；
  * `freshest` 停摆判据去掉 ⇒ 整条链没跑时把全部在途单当"信号消失"撤光；
  * VWAP 缺失时把 None 当 0 ⇒ 每笔挂单都被判为"偏离无穷"而撤掉。

English: behaviour locks for same-round budget reservation, signal-gone
cancellation and VWAP repricing on the physical-box local executor.
"""

import os
import sys
import time
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
sys.path.insert(0, GW_DIR)

import local_executor as lex  # noqa: E402


def P(**overrides):
    """A copy of the baked PARAMS with optional overrides."""
    out = dict(lex.PARAMS)
    out.update(overrides)
    return out


def C(**overrides):
    """A rule-layer context shaped like context_for's output."""
    base = {
        "symbol": "600000.SH", "day": "20261105", "weekday": 3,
        "closed_days": [], "hhmm": "10:15", "trading_day": True,
        "last_price": 10.0, "prev_close": 10.0, "volume": 1000000.0,
        "ask1": 10.0, "bid1": 10.0, "ref_price": 10.0,
        "volume_ratio": 2.0, "position_qty": 0.0, "position_cost": None,
        "available_cash": 100000.0, "daily_buy_total": 0.0,
        "ops_last_minute": 0, "round_reserved": 0.0,
    }
    base.update(overrides)
    return base


class Broker(object):
    """Minimal broker: records the two actions that cost money."""

    def __init__(self, orders=None):
        self.orders = list(orders or [])
        self.placed = []
        self.cancelled = []

    def orders_today(self):
        return list(self.orders)

    def positions(self):
        return {}

    def available_cash(self):
        return 100000.0

    def place_order(self, signal, remark):
        self.placed.append((signal, remark))
        return True, str(len(self.placed)), "ok"

    def cancel_order(self, order_id):
        self.cancelled.append(order_id)
        return True, "ok"


class Data(object):
    """Tick source with a controllable cumulative amount (=> VWAP)."""

    def __init__(self, tick=None):
        self.tick = tick or {"lastPrice": 10.0, "lastClose": 10.0,
                             "volume": 1000.0, "amount": 900000.0,
                             "askPrice": [10.0], "bidPrice": [10.0]}

    def get_full_tick(self, symbols):
        return {s: dict(self.tick) for s in symbols}

    def get_market_data_ex(self, field_list, stock_list, period):
        return {"close": {s: [10.0] for s in stock_list},
                "volume": {s: [500000.0, 500000.0] for s in stock_list}}

    def moving_average(self, symbol, window, period):
        return 10.0


def buy_signal(short):
    return {"short_code": short, "kind": "pullback_buy", "side": "buy",
            "symbol": "600000.SH", "qty": 1000, "price": 10.0,
            "amount": 20000.0}


def resting_buy(**overrides):
    row = {"order_id": "77", "symbol": "600000.SH",
           "remark": lex.build_remark("PB600000"), "terminal": False,
           "qty": 100, "price": 10.0, "traded": 0, "order_time": None}
    row.update(overrides)
    return row


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.state = lex.LocalState(os.path.join(self.tmp.name, "state.db"))
        self.day = "20261105"
        self.moment = lex.datetime(2026, 11, 5, 10, 15)

    def tearDown(self):
        self.tmp.cleanup()

    def executor(self, p, broker, data=None):
        return lex.Executor(p, data or Data(), broker, self.state,
                            clock=lambda: self.moment)


# ---------------------------------------------------------------------------
# (1) 预算算术：别人的预占必须压低上限，两把尺都没有才放行
# ---------------------------------------------------------------------------
class TestRoomArithmetic(Base):
    def test_others_subtract_not_add(self):
        p = P()
        limit, ok = lex.rule_room_limit(p, 30000.0, True, 20000.0, 0.0, False)
        self.assertTrue(ok)
        self.assertEqual(limit, 10000.0)

    def test_cash_is_the_fallback_ruler(self):
        limit, ok = lex.rule_room_limit(P(), 0.0, False, 5000.0, 40000.0, True)
        self.assertTrue(ok)
        self.assertEqual(limit, 35000.0)

    def test_no_ruler_returns_no_limit(self):
        limit, ok = lex.rule_room_limit(P(), None, False, 0.0, None, False)
        self.assertFalse(ok)
        self.assertEqual(limit, 0.0)

    def test_reference_price_missing_is_not_zero(self):
        dev, ok = lex.rule_reprice_deviation_pct(P(), 10.0, None)
        self.assertFalse(ok)
        self.assertIsNone(dev)

    def test_reprice_is_symmetric_and_threshold_gated(self):
        p = P()
        dev, ok = lex.rule_reprice_deviation_pct(p, 11.0, 10.0)
        self.assertTrue(ok)
        self.assertTrue(lex.rule_reprice_worth_cancelling(p, dev, 1.0))
        self.assertFalse(lex.rule_reprice_worth_cancelling(p, dev, 50.0))
        low, _ = lex.rule_reprice_deviation_pct(p, 9.0, 10.0)
        self.assertTrue(lex.rule_reprice_worth_cancelling(p, low, 1.0))

    def test_switched_off_legs_have_no_threshold(self):
        p = P(signal_gone_cancel_sec=0, vwap_reprice_pct=0)
        self.assertIsNone(lex.rule_leg_seconds(p, "signal_gone_cancel_sec"))
        self.assertIsNone(lex.rule_leg_seconds(p, "vwap_reprice_pct"))


# ---------------------------------------------------------------------------
# (2) 同轮两笔买入不得穿透预算（走真的 _maybe_send）
# ---------------------------------------------------------------------------
class TestSameRoundBudget(Base):
    def test_second_buy_is_refused_by_the_room_guard(self):
        p = P(daily_buy_cap=25000.0, order_amount_cap=20000.0,
              min_cash_reserve=0.0, min_order_amount=1.0,
              buy_room_guard=True)
        broker = Broker()
        ex = self.executor(p, broker)
        c = C()
        budget = {"reserved": 0.0}
        first = ex._maybe_send(c, buy_signal("PB600000"), set(), self.day, budget)
        second = ex._maybe_send(c, buy_signal("PB600001"), set(), self.day, budget)
        self.assertTrue(first["sent"])
        self.assertEqual(budget["reserved"], 20000.0)
        self.assertFalse(second["sent"])
        self.assertEqual(second["reason"], "buy_room_guard")
        self.assertEqual(len(broker.placed), 1)

    def test_guard_can_be_turned_off_for_an_instant_rollback(self):
        p = P(daily_buy_cap=25000.0, order_amount_cap=20000.0,
              min_cash_reserve=0.0, min_order_amount=1.0,
              buy_room_guard=False)
        broker = Broker()
        ex = self.executor(p, broker)
        c = C()
        budget = {"reserved": 0.0}
        ex._maybe_send(c, buy_signal("PB600000"), set(), self.day, budget)
        second = ex._maybe_send(c, buy_signal("PB600001"), set(), self.day, budget)
        self.assertTrue(second["sent"])
        self.assertEqual(len(broker.placed), 2)


# ---------------------------------------------------------------------------
# (3) 信号消失即撤 / 整链停摆不撤 / VWAP 改价（走真的 cancel_leg）
# ---------------------------------------------------------------------------
class TestCancelReasons(Base):
    def _cancel(self, p, rows, broker, data=None, live=None, freshest_shift=0.0):
        now = time.time()
        ex = self.executor(p, broker, data)
        return ex.cancel_leg(
            self.day, rows, self.moment, set(),
            live_shorts=live if live is not None else set(),
            now_ts=now, freshest=now - freshest_shift)

    def test_signal_gone_cancels_a_resting_buy(self):
        p = P(signal_gone_cancel_sec=30, vwap_reprice_pct=0)
        self.state.record_signal_seen(self.day, "PB600000", time.time() - 120)
        broker = Broker([resting_buy()])
        out = self._cancel(p, [resting_buy()], broker)
        self.assertEqual(broker.cancelled, ["77"])
        self.assertEqual(out["cancelled"][0]["why"], "signal_gone")

    def test_stalled_chain_is_not_read_as_every_signal_vanished(self):
        p = P(signal_gone_cancel_sec=30, vwap_reprice_pct=0)
        self.state.record_signal_seen(self.day, "PB600000", time.time() - 120)
        broker = Broker([resting_buy()])
        # freshest 120s old => the decision chain itself looks stalled
        out = self._cancel(p, [resting_buy()], broker, freshest_shift=120.0)
        self.assertEqual(broker.cancelled, [])
        self.assertEqual(out["cancelled"], [])

    def test_unknown_last_seen_never_cancels(self):
        p = P(signal_gone_cancel_sec=30, vwap_reprice_pct=0)
        # no signal_seen row at all: missing != gone
        broker = Broker([resting_buy()])
        out = self._cancel(p, [resting_buy()], broker)
        self.assertEqual(broker.cancelled, [])
        self.assertEqual(out["cancelled"], [])

    def test_vwap_reprice_cancels_and_counts_from_the_journal(self):
        p = P(signal_gone_cancel_sec=0, vwap_reprice_pct=1.0,
              reprice_max_per_day=2)
        broker = Broker([resting_buy()])
        out = self._cancel(p, [resting_buy()], broker)
        self.assertEqual(broker.cancelled, ["77"])
        self.assertEqual(out["cancelled"][0]["why"], "vwap_reprice")
        self.assertEqual(self.state.reprice_count(self.day, "600000.SH"), 1)

    def test_reprice_daily_cap_survives_in_the_journal(self):
        p = P(signal_gone_cancel_sec=0, vwap_reprice_pct=1.0,
              reprice_max_per_day=2)
        self.state.record_reprice(self.day, "600000.SH")
        self.state.record_reprice(self.day, "600000.SH")
        broker = Broker([resting_buy()])
        out = self._cancel(p, [resting_buy()], broker)
        self.assertEqual(broker.cancelled, [])
        self.assertEqual(out["cancelled"], [])

    def test_partial_fill_is_never_repriced(self):
        p = P(signal_gone_cancel_sec=0, vwap_reprice_pct=1.0)
        row = resting_buy(traded=50)
        broker = Broker([row])
        out = self._cancel(p, [row], broker)
        self.assertEqual(broker.cancelled, [])
        self.assertEqual(out["cancelled"], [])

    def test_no_vwap_reference_never_cancels(self):
        p = P(signal_gone_cancel_sec=0, vwap_reprice_pct=1.0)
        data = Data({"lastPrice": 10.0, "lastClose": 10.0,
                     "volume": 1000.0, "askPrice": [10.0], "bidPrice": [10.0]})
        broker = Broker([resting_buy()])
        out = self._cancel(p, [resting_buy()], broker, data=data)
        self.assertEqual(broker.cancelled, [])
        self.assertEqual(out["cancelled"], [])

    def test_switched_off_legs_leave_today_unchanged(self):
        p = P(signal_gone_cancel_sec=0, vwap_reprice_pct=0)
        self.state.record_signal_seen(self.day, "PB600000", time.time() - 999)
        broker = Broker([resting_buy()])
        out = self._cancel(p, [resting_buy()], broker)
        self.assertEqual(broker.cancelled, [])
        self.assertEqual(out["cancelled"], [])

    def test_terminal_and_unknown_rows_are_still_untouched(self):
        p = P(signal_gone_cancel_sec=30, vwap_reprice_pct=1.0)
        self.state.record_signal_seen(self.day, "PB600000", time.time() - 120)
        rows = [resting_buy(order_id="t1", terminal=True),
                resting_buy(order_id="u1", terminal=None)]
        broker = Broker(rows)
        out = self._cancel(p, rows, broker)
        self.assertEqual(broker.cancelled, [])
        self.assertEqual(out["cancelled"], [])


# ---------------------------------------------------------------------------
# (4) CUTOVER-1105 互锁：桥还活着时执行器绝不许碰柜台
# ---------------------------------------------------------------------------
class TestCutoverInterlock(Base):
    def _write(self, text):
        path = os.path.join(self.tmp.name, "execution_mode.json")
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)
        return path

    def test_no_interlock_configured_keeps_hand_run_behaviour(self):
        self.assertEqual(lex.execution_mode_from(""), "local")

    def test_missing_file_defaults_to_bridge(self):
        self.assertEqual(
            lex.execution_mode_from(os.path.join(self.tmp.name, "absent.json")), "bridge")

    def test_malformed_and_unknown_states_default_to_bridge(self):
        self.assertEqual(lex.execution_mode_from(self._write("{not json")), "bridge")
        self.assertEqual(lex.execution_mode_from(self._write('{"mode":"auto"}')), "bridge")
        self.assertEqual(lex.execution_mode_from(self._write("[]")), "bridge")

    def test_explicit_local_is_the_only_trading_state(self):
        self.assertEqual(lex.execution_mode_from(self._write('{"mode":"local"}')), "local")
        self.assertEqual(lex.execution_mode_from(self._write('{"mode":"bridge"}')), "bridge")

    def test_main_refuses_to_trade_while_bridge_is_live(self):
        # A real main() call: it must return before touching the broker at all.
        path = self._write('{"mode":"bridge"}')
        rc = lex.main(["--account", "123456", "--execution-mode-file", path, "--once"])
        self.assertEqual(rc, 4)

    def test_selftest_still_works_on_a_bridge_box(self):
        # Health/version readouts must not be blocked by the interlock.
        path = self._write('{"mode":"bridge"}')
        rc = lex.main(["--selftest", "--execution-mode-file", path])
        self.assertEqual(rc, 0)


if __name__ == "__main__":
    unittest.main()
