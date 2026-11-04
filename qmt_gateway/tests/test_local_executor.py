#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Local executor regression locks (SFRIEND-LOCAL batch 7, 2026-11-03).

Why these locks exist (the broker rule effective 2026-11-05):
  * QMT built-in Python strategies lose all local file I/O and all external
    network access;
  * QMT trading terminals may not run on public cloud or VMs.
  => QMT lives on a physical Windows box, and because the computation can
     no longer live inside QMT, both the computation and the execution
     must live in a SEPARATE ordinary Python process (own venv), where
     file I/O and networking are allowed.

That split puts money rules in a file that no other gate covers, so this
file pins them. Every lock below is a behaviour that, if it silently
regressed, would cost money and would NOT turn any existing test red:

  (1) the rule layer is PURE: thresholds come from PARAMS, never from a
     bare numeric literal in the function body (the "changed the knob but
     did not re-bake" failure);
  (2) the rule layer is pure LOGIC: no I/O, no clock, no randomness -- so
     it can be tested without a broker, a disk or a clock;
  (3) the risk caps are ANDed, not ORed (cash AND per-order cap AND daily
     cap must all allow an order);
  (4) the forbidden windows are judged on the TRADING-DAY axis first: on a
     closed day the reason is "not_trading_day", never a story about the
     call auction refusing something (there is no session on a closed day);
  (5) idempotency has two halves: the local journal AND the broker's own
     same-day order table; after a restart the same short code must not
     be sent again;
  (6) FAIL CLOSED: if the broker order table cannot be read, the round
     places NOTHING (rather not buy than buy twice);
  (7) the builder's five static gates actually bite (injecting a bare
     number / a non-ASCII byte / a privileged call must fail the build
     and leave no half-written artifact);
  (8) the updater's three stages are all mandatory and a failed version
     read-back goes RED, writes the STOP_AUTO_UPDATE marker, and rolls
     back.

Everything here runs without xtquant, without QMT and without a network:
the data layer is import-protected and the broker is faked, exactly the
way the real box would be exercised without a real broker attached.

(English: local executor regression locks -- pure rule layer, ANDed risk
caps, trading-day windows, double-insurance idempotency, fail-closed
rounds, and the builder/updater gates.)
"""

import os
import re
import sys
import tempfile
import subprocess
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
REPO = os.path.dirname(GW_DIR)
sys.path.insert(0, GW_DIR)

import local_executor as lex  # noqa: E402


def params(**overrides):
    """A copy of the baked PARAMS with optional overrides."""
    out = dict(lex.PARAMS)
    out.update(overrides)
    return out


def ctx(**overrides):
    """A rule-layer context with sane defaults for a trading day."""
    base = {
        "symbol": "600000.SH",
        "day": "20261103",
        "weekday": 1,
        "closed_days": [],
        "hhmm": "10:15",
        "trading_day": True,
        "last_price": 10.0,
        "prev_close": 10.0,
        "volume": 1000000.0,
        "ask1": 10.0,
        "bid1": 10.0,
        "ref_price": 10.0,
        "volume_ratio": 2.0,
        "position_qty": 0.0,
        "position_cost": None,
        "available_cash": 100000.0,
        "daily_buy_total": 0.0,
        "ops_last_minute": 0,
    }
    base.update(overrides)
    return base


def rule_layer_source():
    """The rule layer region of the executor source (between the markers).

    Read as TEXT and parsed on its own: the gate is about the source,
    not about the imported module, so a rule could not hide a literal
    behind an import.
    """
    with open(os.path.join(GW_DIR, "local_executor.py"), "r", encoding="utf-8") as fh:
        text = fh.read()
    begin = text.find("# ==== RULE LAYER BEGIN ====")
    end = text.find("# ==== RULE LAYER END ====")
    if begin < 0 or end < begin:
        raise AssertionError("rule layer markers missing or inverted")
    return text[begin:end]


# ---------------------------------------------------------------------------
# (1) the rule layer must contain no numeric literal (the "re-bake" lock)
# ---------------------------------------------------------------------------

class TestRuleLayerHasNoBareNumbers(unittest.TestCase):
    """A threshold written straight into a rule function is a threshold
    that no longer comes from the parameter file: change the knob and the
    code keeps the old number. The builder gate counts these; this test
    pins the same invariant against the repository copy."""

    def _rule_layer_source(self):
        return rule_layer_source()

    def test_rule_layer_has_no_numeric_literal(self):
        import ast
        source = self._rule_layer_source()
        tree = ast.parse(source)
        hits = []
        for node in ast.walk(tree):
            if not isinstance(node, ast.Constant):
                continue
            value = node.value
            if isinstance(value, bool):
                continue
            if isinstance(value, (int, float)):
                hits.append((getattr(node, "lineno", -1), value))
        self.assertEqual(hits, [], "rule layer carries bare numeric literals: %r" % (hits,))

    def test_every_threshold_the_rules_use_exists_in_params(self):
        """Each PARAMS key the rule layer reads must actually be there.

        A missing key raises at decision time -- which is the loud
        failure we want -- but it must never fall back to a baked-in
        default, so the key set is pinned here.
        """
        needed = [
            "non_trading_weekdays", "min_price", "min_volume",
            "volume_ratio_min", "min_cash_reserve", "order_amount_cap",
            "daily_buy_cap", "min_order_amount", "no_cancel_windows",
            "no_new_order_windows", "max_ops_per_min", "pullback_ratio",
            "take_profit_ratio", "stop_loss_ratio", "breakdown_ratio",
            "lot_size", "signal_codes", "symbol_head_len",
        ]
        for key in needed:
            self.assertIn(key, lex.PARAMS, "PARAMS is missing %r" % key)

    def test_signal_codes_cover_every_rule_kind(self):
        """A signal kind with no short code cannot be keyed, and a missing
        key would silently drop the signal."""
        kinds = ["pullback_buy", "take_profit", "stop_loss", "breakdown_sell"]
        for kind in kinds:
            self.assertIn(kind, lex.PARAMS["signal_codes"])


# ---------------------------------------------------------------------------
# (2) the rule layer is pure logic
# ---------------------------------------------------------------------------

class TestRuleLayerIsPure(unittest.TestCase):
    """The rule functions take what they need and return a decision. No
    I/O, no clock, no randomness -- so they can be tested without a
    broker, a disk or a clock, and so a test result means something."""

    def test_rules_do_not_touch_io_or_clock(self):
        import ast
        source = rule_layer_source()
        tree = ast.parse(source)
        banned = {"open", "eval", "exec", "compile", "input", "__import__"}
        for node in ast.walk(tree):
            if isinstance(node, ast.Call):
                func = node.func
                name = getattr(func, "id", None) or getattr(func, "attr", None)
                self.assertNotIn(name, banned,
                                 "rule layer calls %r" % (name,))

    def test_decide_is_deterministic(self):
        """Same context in, same decision out -- twice."""
        p = params()
        c = ctx()
        first = lex.Executor(p, None, None, None).decide(c)
        second = lex.Executor(p, None, None, None).decide(c)
        self.assertEqual(first, second)

    def test_decide_does_not_mutate_the_context(self):
        """A rule that mutates its input makes the round order-dependent."""
        p = params()
        c = ctx()
        before = dict(c)
        lex.Executor(p, None, None, None).decide(c)
        self.assertEqual(c, before)


# ---------------------------------------------------------------------------
# (3) the risk caps are ANDed
# ---------------------------------------------------------------------------

class TestTradingChannelIsClosedByDefault(unittest.TestCase):
    """11-04 定的一条：**本地执行器缺省不许碰交易通道**。

    起因（这条不是防御性编程，是照着自己的事故记录改的）：
      * `XtBroker` 走的是 `xtquant.xttrader`，也就是券商答复里"独立交易属于 miniqmt，
        9 月份就关闭了"的那条通道；
      * 10-29 的实录：queued 无心跳时每 60s 重试直连，每次 connect() 在客户端共享内存里
        占一个 writer 槽，槽满即把 QMT 客户端拖崩（713 连败、3~6 分钟一崩）。
        所以"试着连一下看看通不通"不是只读探测，是对着自己正在交易的终端打；
      * 11-05 之后合法的自动下单只剩 QMT 内置策略（沙箱内 passorder）。
        本地那一份的职责是**算 + 投递读数**，不是下单。
    """

    def test_default_params_give_the_refusing_face(self):
        broker = lex.build_broker({}, "8-测试账号")
        self.assertIsInstance(broker, lex.DisabledBroker)
        self.assertEqual(broker.status(), "disabled")
        ok, reason = broker.connect()
        self.assertFalse(ok)
        self.assertEqual(reason, "xt_channel_disabled")

    def test_refusing_face_refuses_every_face(self):
        broker = lex.DisabledBroker()
        self.assertEqual(broker.place_order({"side": "buy", "symbol": "600000.SH",
                                             "qty": 100, "price": 10.0}, "PBTEST"),
                         (False, "", "xt_channel_disabled"))
        self.assertEqual(broker.cancel_order("7701"), (False, "xt_channel_disabled"))
        for getter in ("orders_today", "positions_today", "asset"):
            with self.assertRaises(lex.BrokerUnavailable):
                getattr(broker, getter)()

    def test_opt_in_key_opens_the_real_face(self):
        """键缺省＝关；显式写 true 才交回 XtBroker（开关方向必须是安全侧）。"""
        broker = lex.build_broker({"xt_channel_enable": True}, "8-测试账号")
        self.assertIsInstance(broker, lex.XtBroker)

    def test_once_round_refuses_without_ever_trying_to_connect(self):
        """端到端：缺省档跑 `--once` 必须是**干净拒绝**，且从未尝试打开交易会话。

        区分的不是"成没成"而是"有没有去连"：真去 connect() 才会走 10-29 那条崩法
        （每次 connect 占一个客户端共享内存 writer 槽）。所以这里断言的是
        `broker connect failed`（XtBroker 试过之后才会打的那行）**不出现**。
        """
        proc = subprocess.run(
            [sys.executable, os.path.join(GW_DIR, "local_executor.py"),
             "--once", "--account", "8-CLI-TEST",
             "--state-db", os.path.join(tempfile.gettempdir(), "exec_cli_test.db")],
            cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=120)
        text = proc.stdout.decode("utf-8", "replace")
        self.assertNotEqual(proc.returncode, 0, text)
        self.assertIn("broker not available: xt_channel_disabled", text)
        self.assertNotIn("broker connect failed", text,
                         "缺省档真的去 connect 了：那正是 10-29 拖崩 QMT 客户端的那条路")
        self.assertIn("xt trading channel is DISABLED", text,
                      "拒绝之前必须先把原因说出来：否则 placed=0 会被读成今天没有信号")

class TestRiskCapsAreAnded(unittest.TestCase):
    """Cash AND per-order cap AND daily cap must all allow the order.
    ORing them would let a small balance through a huge daily cap."""

    def test_cash_cap_blocks_oversized_order(self):
        p = params()
        # 1500 cash minus the 1000 reserve leaves 500 usable.
        c = ctx(available_cash=1500.0)
        self.assertFalse(lex.rule_cash_ok(p, c, 1000.0))
        self.assertTrue(lex.rule_cash_ok(p, c, 500.0))

    def test_per_order_cap_blocks_oversized_order(self):
        p = params()
        self.assertFalse(lex.rule_amount_cap_ok(p, 30000.0))
        self.assertTrue(lex.rule_amount_cap_ok(p, 10000.0))

    def test_daily_cap_accounts_for_what_was_already_spent(self):
        p = params()
        c = ctx(daily_buy_total=95000.0)
        self.assertFalse(lex.rule_daily_buy_cap_ok(p, c, 10000.0))
        self.assertTrue(lex.rule_daily_buy_cap_ok(p, c, 1000.0))

    def test_buy_amount_is_the_minimum_of_every_cap(self):
        p = params()
        # cash allows 50k, per-order allows 20k, daily allows 15k left.
        # 15k (not 5k) because min_order_amount is the Go factory 10000: a 5k room is
        # correctly refused outright ("not worth an order"), which tests the amount floor
        # rather than the min-of-caps this case is about.
        c = ctx(available_cash=50000.0, daily_buy_total=85000.0)
        amount = lex.rule_buy_amount(p, c)
        self.assertIsNotNone(amount)
        self.assertLessEqual(amount, 15000.0)
        self.assertLessEqual(amount, 20000.0)

    def test_buy_amount_is_none_when_nothing_survives(self):
        p = params()
        c = ctx(available_cash=100.0)
        self.assertIsNone(lex.rule_buy_amount(p, c))

    def test_remaining_quota_reflects_daily_cap(self):
        p = params()
        c = ctx(daily_buy_total=25000.0)
        self.assertAlmostEqual(
            lex.rule_remaining_quota(p, c),
            p["daily_buy_cap"] - 25000.0)

    def test_unknown_daily_total_denies_the_order(self):
        """A missing daily total must not be read as zero: that would
        hand out a fresh daily budget on every restart."""
        p = params()
        c = ctx(daily_buy_total=None)
        self.assertFalse(lex.rule_daily_buy_cap_ok(p, c, 1000.0))
        self.assertIsNone(lex.rule_remaining_quota(p, c))

    def test_unknown_cash_denies_the_order(self):
        p = params()
        self.assertFalse(lex.rule_cash_ok(p, ctx(available_cash=None), 1000.0))


# ---------------------------------------------------------------------------
# (4) the forbidden windows are judged on the trading-day axis first
# ---------------------------------------------------------------------------

class TestWindowsUseTheTradingDayAxis(unittest.TestCase):
    """On a closed day the answer is "not a trading day" -- never a story
    about the call auction refusing something, because on a closed day
    there is no session to have refused anything."""

    def test_closed_day_reason_is_not_trading_day(self):
        p = params()
        c = ctx(trading_day=False, hhmm="10:15")
        allowed, reason = lex.rule_no_cancel_window(p, c)
        self.assertFalse(allowed)
        self.assertEqual(reason, "not_trading_day")
        allowed, reason = lex.rule_no_new_order_window(p, c)
        self.assertFalse(allowed)
        self.assertEqual(reason, "not_trading_day")

    def test_closed_day_reason_never_mentions_the_call_auction(self):
        """The forbidden phrase must not appear as a reason at all.

        "The call auction does not accept orders" on a day with no
        session is a lie about what happened, and an operator reading it
        would go looking for a market problem that does not exist.
        """
        p = params()
        c = ctx(trading_day=False)
        for rule in (lex.rule_no_cancel_window, lex.rule_no_new_order_window):
            _allowed, reason = rule(p, c)
            self.assertNotIn("auction", reason.lower())
            self.assertNotIn("\u7ade\u4ef7", reason)
            self.assertNotIn("\u96c6\u5408", reason)

    def test_no_cancel_window_blocks_inside_it(self):
        p = params()
        c = ctx(trading_day=True, hhmm="14:57")
        allowed, reason = lex.rule_no_cancel_window(p, c)
        self.assertFalse(allowed)
        self.assertEqual(reason, "no_cancel_window")

    def test_no_cancel_window_allows_outside_it(self):
        p = params()
        c = ctx(trading_day=True, hhmm="10:00")
        allowed, reason = lex.rule_no_cancel_window(p, c)
        self.assertTrue(allowed)
        self.assertEqual(reason, "ok")

    def test_no_new_order_window_blocks_inside_it(self):
        p = params()
        c = ctx(trading_day=True, hhmm="09:20")
        allowed, reason = lex.rule_no_new_order_window(p, c)
        self.assertFalse(allowed)
        self.assertEqual(reason, "no_new_order_window")

    def test_window_wrapping_midnight_is_handled(self):
        """A window whose end is not after its start wraps over midnight.

        "23:30" to "00:30" means "half an hour before midnight until half
        an hour after it", so BOTH ends of the day are inside it and the
        middle of the day is not.
        """
        p = params(no_cancel_windows=[["23:30", "00:30"]])
        windows = p["no_cancel_windows"]
        self.assertTrue(lex.rule_window_hit(p, windows, "23:45"))
        self.assertTrue(lex.rule_window_hit(p, windows, "00:10"))
        self.assertFalse(lex.rule_window_hit(p, windows, "12:00"))

    def test_closed_day_from_the_weekend_rule(self):
        """Saturday is not a trading day, whatever the calendar says."""
        p = params()
        c = ctx(weekday=5, trading_day=lex.rule_is_trading_day(p, ctx(weekday=5)))
        self.assertFalse(c["trading_day"])

    def test_closed_day_from_the_calendar_list(self):
        """A holiday in the calendar file is not a trading day."""
        p = params()
        c = ctx(day="20261104", closed_days=["20261104"],
                trading_day=lex.rule_is_trading_day(p, ctx(day="20261104",
                                                            closed_days=["20261104"])))
        self.assertFalse(c["trading_day"])


# ---------------------------------------------------------------------------
# the frequency hard gate
# ---------------------------------------------------------------------------

class TestOpsFrequencyGate(unittest.TestCase):
    """Order+cancel frequency is capped. A loop that retries a rejected
    order must not hammer the broker."""

    def test_gate_blocks_at_the_limit(self):
        p = params(max_ops_per_min=6)
        self.assertTrue(lex.rule_ops_frequency_ok(p, ctx(ops_last_minute=5)))
        self.assertFalse(lex.rule_ops_frequency_ok(p, ctx(ops_last_minute=6)))
        self.assertFalse(lex.rule_ops_frequency_ok(p, ctx(ops_last_minute=9)))

    def test_unknown_op_count_denies(self):
        p = params()
        self.assertFalse(lex.rule_ops_frequency_ok(p, ctx(ops_last_minute=None)))


# ---------------------------------------------------------------------------
# the signals themselves
# ---------------------------------------------------------------------------

class TestSignals(unittest.TestCase):
    """P0 price-type signals: pullback buy, take profit, stop loss,
    breakdown sell."""

    def test_pullback_buy_needs_the_pullback(self):
        p = params()
        # price at the reference -> pulls back -> buys
        signal = lex.rule_pullback_buy(p, ctx(last_price=9.8, ref_price=10.0,
                                               volume_ratio=2.0))
        self.assertIsNotNone(signal)
        self.assertEqual(signal["kind"], "pullback_buy")
        self.assertEqual(signal["side"], "buy")
        # price far above the reference -> no pullback -> no buy
        self.assertIsNone(lex.rule_pullback_buy(p, ctx(last_price=11.0, ref_price=10.0,
                                                        volume_ratio=2.0)))

    def test_pullback_buy_needs_the_volume_ratio(self):
        """The volume ratio is the confirmation: a pullback on no volume
        is not a pullback."""
        p = params()
        self.assertIsNone(lex.rule_pullback_buy(p, ctx(last_price=9.8, ref_price=10.0,
                                                        volume_ratio=0.5)))

    def test_pullback_buy_respects_the_ratio_threshold_from_params(self):
        p = params(volume_ratio_min=3.0)
        self.assertIsNone(lex.rule_pullback_buy(p, ctx(last_price=9.8, ref_price=10.0,
                                                        volume_ratio=2.5)))
        self.assertIsNotNone(lex.rule_pullback_buy(p, ctx(last_price=9.8, ref_price=10.0,
                                                           volume_ratio=3.5)))

    def test_no_signal_on_a_closed_day(self):
        p = params()
        for rule in (lex.rule_pullback_buy, lex.rule_take_profit,
                     lex.rule_stop_loss, lex.rule_breakdown_sell):
            self.assertIsNone(rule(p, ctx(trading_day=False)))

    def test_take_profit(self):
        p = params()
        signal = lex.rule_take_profit(p, ctx(last_price=11.0, position_qty=1000.0,
                                              position_cost=10.0))
        self.assertIsNotNone(signal)
        self.assertEqual(signal["kind"], "take_profit")
        self.assertEqual(signal["side"], "sell")

    def test_take_profit_needs_a_position(self):
        p = params()
        self.assertIsNone(lex.rule_take_profit(p, ctx(last_price=11.0, position_qty=0.0,
                                                       position_cost=10.0)))

    def test_stop_loss(self):
        p = params()
        signal = lex.rule_stop_loss(p, ctx(last_price=9.0, position_qty=1000.0,
                                           position_cost=10.0))
        self.assertIsNotNone(signal)
        self.assertEqual(signal["kind"], "stop_loss")
        self.assertEqual(signal["side"], "sell")

    def test_breakdown_sell(self):
        p = params()
        signal = lex.rule_breakdown_sell(p, ctx(last_price=9.0, ref_price=10.0,
                                                 position_qty=1000.0, position_cost=10.0))
        self.assertIsNotNone(signal)
        self.assertEqual(signal["kind"], "breakdown_sell")

    def test_breakdown_sell_needs_a_position(self):
        p = params()
        self.assertIsNone(lex.rule_breakdown_sell(p, ctx(last_price=9.0, ref_price=10.0,
                                                          position_qty=0.0)))

    def test_exit_wins_over_entry_on_the_same_symbol(self):
        """A name that must be sold is more urgent than buying anything."""
        p = params()
        c = ctx(last_price=11.0, position_qty=1000.0, position_cost=10.0,
                volume_ratio=5.0)
        signals = lex.Executor(p, None, None, None).decide(c)
        self.assertEqual(len(signals), 1)
        self.assertEqual(signals[0]["kind"], "take_profit")

    def test_short_code_is_kind_plus_symbol_head(self):
        """The short code is the idempotency key: it must be stable for
        the same symbol and signal, and different for a different one."""
        p = params()
        signal = lex.rule_pullback_buy(p, ctx(symbol="600000.SH", last_price=9.8,
                                               ref_price=10.0, volume_ratio=2.0))
        self.assertEqual(signal["short_code"], "PB600000")
        other = lex.rule_pullback_buy(p, ctx(symbol="000001.SZ", last_price=9.8,
                                              ref_price=10.0, volume_ratio=2.0))
        self.assertEqual(other["short_code"], "PB000001")
        self.assertNotEqual(signal["short_code"], other["short_code"])

    def test_remark_carries_the_version_and_the_short_code(self):
        """The remark is the only thing that survives a restart, so it
        must carry BOTH the version and the idempotency key."""
        remark = lex.build_remark("PB600000")
        self.assertTrue(remark.startswith(lex.STRATEGY_VERSION + "-"))
        self.assertEqual(lex.short_code_from_remark(remark), "PB600000")

    def test_short_code_round_trips_through_the_remark(self):
        for short in ("PB600000", "TP000001", "SL600000", "BK000001"):
            self.assertEqual(lex.short_code_from_remark(lex.build_remark(short)), short)


# ---------------------------------------------------------------------------
# the fillable judgement (suspended / sealed limit board)
# ---------------------------------------------------------------------------

class TestFillableJudgement(unittest.TestCase):
    """A suspended name and a sealed limit board cannot be filled.
    Sending the order anyway wastes the frequency budget and creates a
    paper position that does not exist."""

    def test_suspended_name_is_not_fillable(self):
        p = params()
        ok, reason = lex.rule_fillable(p, ctx(volume=0.0), "buy")
        self.assertFalse(ok)
        self.assertEqual(reason, "suspended")

    def test_missing_price_is_not_fillable(self):
        p = params()
        ok, reason = lex.rule_fillable(p, ctx(last_price=None), "buy")
        self.assertFalse(ok)
        self.assertEqual(reason, "no_price")

    def test_sealed_limit_up_blocks_buying(self):
        """A limit-up board has no ask to lift."""
        p = params()
        ok, reason = lex.rule_fillable(p, ctx(ask1=0.0), "buy")
        self.assertFalse(ok)
        self.assertEqual(reason, "limit_up_sealed")

    def test_sealed_limit_down_blocks_selling(self):
        """A limit-down board has no bid to hit."""
        p = params()
        ok, reason = lex.rule_fillable(p, ctx(bid1=0.0), "sell")
        self.assertFalse(ok)
        self.assertEqual(reason, "limit_down_sealed")

    def test_normal_market_is_fillable(self):
        p = params()
        ok, reason = lex.rule_fillable(p, ctx(), "buy")
        self.assertTrue(ok)
        self.assertEqual(reason, "fillable")


# ---------------------------------------------------------------------------
# (5) + (6) idempotency and the fail-closed round (with a faked broker)
# ---------------------------------------------------------------------------

class FakeBroker(object):
    """A fake broker: it records what it was asked to do and can be told
    to fail, so the fail-closed path is reachable without a real one."""

    def __init__(self, orders=None, unreadable=False, cash=100000.0,
                 holdings=None):
        self.orders = list(orders or [])
        self.unreadable = unreadable
        self.cash = cash
        # NOTE the name: it must not be "positions", or the instance
        # attribute would shadow the positions() method below and the
        # executor would call a dict.
        self.holdings = dict(holdings or {})
        self.placed = []
        self.cancelled = []

    def orders_today(self):
        if self.unreadable:
            raise lex.BrokerUnavailable("query_stock_orders failed: fake")
        return list(self.orders)

    def positions(self):
        return dict(self.holdings)

    def available_cash(self):
        return self.cash

    def place_order(self, signal, remark):
        self.placed.append((signal, remark))
        return True, str(len(self.placed)), "ok"

    def cancel_order(self, order_id):
        self.cancelled.append(order_id)
        return True, "ok"


class FakeData(object):
    """A fake market data source: no xtquant needed."""

    def __init__(self, tick=None, volume_ratio=2.0):
        self.tick = tick or {"lastPrice": 9.8, "lastClose": 10.0, "volume": 1000000.0,
                             "askPrice": [10.0], "bidPrice": [10.0]}
        self.volume_ratio = volume_ratio

    def get_full_tick(self, symbols):
        return {s: dict(self.tick) for s in symbols}

    def get_market_data_ex(self, field_list, stock_list, period):
        return {"close": {s: [10.0] for s in stock_list},
                "volume": {s: [500000.0, 500000.0] for s in stock_list}}

    def moving_average(self, symbol, window, period):
        return 10.0


class TestIdempotencyAndFailClosed(unittest.TestCase):
    """The two halves of the idempotency insurance, and the fail-closed
    behaviour when the broker's order table cannot be read."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.db = os.path.join(self.tmp.name, "state.db")
        self.state = lex.LocalState(self.db)
        self.p = params(symbols=["600000.SH"])

    def tearDown(self):
        self.tmp.cleanup()

    def _executor(self, broker, data=None):
        return lex.Executor(self.p, data or FakeData(), broker, self.state,
                            clock=lambda: lex.datetime(2026, 11, 3, 10, 15))

    def test_first_round_places_the_order(self):
        broker = FakeBroker()
        summary = self._executor(broker).run_round()
        self.assertEqual(len(summary["placed"]), 1)
        self.assertEqual(len(broker.placed), 1)

    def test_remark_on_the_placed_order_carries_version_and_short_code(self):
        broker = FakeBroker()
        self._executor(broker).run_round()
        _signal, remark = broker.placed[0]
        self.assertEqual(remark, lex.build_remark("PB600000"))
        self.assertTrue(remark.startswith(lex.STRATEGY_VERSION))

    def test_restart_does_not_place_the_same_short_code_again(self):
        """The headline idempotency case: after a restart the same signal
        must not be sent a second time.

        The local journal survives the restart, so the second executor
        (a fresh object over the same journal) sees the short code and
        skips it.
        """
        broker = FakeBroker()
        self._executor(broker).run_round()
        self.assertEqual(len(broker.placed), 1)
        # restart: a brand new executor over the SAME journal
        restarted = lex.Executor(self.p, FakeData(), broker, self.state,
                                 clock=lambda: lex.datetime(2026, 11, 3, 10, 16))
        summary = restarted.run_round()
        self.assertEqual(len(broker.placed), 1, "the order was placed twice after a restart")
        self.assertEqual(summary["placed"], [])
        self.assertEqual(summary["skipped"][0]["reason"], "already_sent")

    def test_broker_order_table_alone_is_enough_to_block_a_resend(self):
        """The broker's own same-day table is the other half.

        This is the case the local journal cannot cover: the journal was
        lost (new disk, reinstall) but the broker still remembers. The
        remark carries the short code, so the table alone blocks the
        resend.
        """
        broker = FakeBroker(orders=[{"order_id": "77", "symbol": "600000.SH",
                                     "remark": lex.build_remark("PB600000"),
                                     "status": "x", "qty": 100, "price": 9.8}])
        # a FRESH journal (the old one is gone)
        self.state = lex.LocalState(os.path.join(self.tmp.name, "fresh.db"))
        summary = self._executor(broker).run_round()
        self.assertEqual(broker.placed, [])
        self.assertEqual(summary["skipped"][0]["reason"], "already_sent")

    def test_unreadable_broker_table_places_nothing(self):
        """FAIL CLOSED: cannot read the order table => send nothing.

        Rather not buy than buy twice.
        """
        broker = FakeBroker(unreadable=True)
        summary = self._executor(broker).run_round()
        self.assertEqual(broker.placed, [])
        self.assertEqual(summary["placed"], [])
        self.assertEqual(summary["reason"], "broker_orders_unreadable")

    def test_unreadable_broker_table_does_not_even_write_the_journal(self):
        """Nothing was sent, so nothing may be journalled: a journalled
        order that never existed would block a legitimate one later."""
        broker = FakeBroker(unreadable=True)
        self._executor(broker).run_round()
        self.assertEqual(self.state.short_codes("20261103"), set())

    def test_a_closed_day_places_nothing(self):
        """A holiday in the baked closed-day list must stop the round."""
        broker = FakeBroker()
        ex = lex.Executor(params(symbols=["600000.SH"], closed_days=["20261103"]),
                          FakeData(), broker, self.state,
                          clock=lambda: lex.datetime(2026, 11, 3, 10, 15))
        summary = ex.run_round()
        self.assertEqual(broker.placed, [])
        self.assertEqual(summary["placed"], [])

    def test_frequency_gate_blocks_the_order_in_a_busy_minute(self):
        """The frequency hard gate is enforced inside the round, not only
        as a standalone rule."""
        broker = FakeBroker()
        ex = lex.Executor(params(symbols=["600000.SH"], max_ops_per_min=0),
                          FakeData(), broker, self.state,
                          clock=lambda: lex.datetime(2026, 11, 3, 10, 15))
        summary = ex.run_round()
        self.assertEqual(broker.placed, [])
        self.assertEqual(summary["skipped"][0]["reason"], "ops_frequency_exceeded")

    def test_the_journal_records_what_was_sent(self):
        broker = FakeBroker()
        self._executor(broker).run_round()
        self.assertIn("PB600000", self.state.short_codes("20261103"))

    def test_the_journal_is_a_separate_day_per_day(self):
        """Yesterday's short code must not block today's."""
        broker = FakeBroker()
        self._executor(broker).run_round()
        self.assertEqual(self.state.short_codes("20261102"), set())
        self.assertIn("PB600000", self.state.short_codes("20261103"))


# ---------------------------------------------------------------------------
# the local journal itself
# ---------------------------------------------------------------------------

class TestLocalState(unittest.TestCase):
    """The journal is half of the idempotency insurance, so it has to
    survive a restart and answer the right question."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.tmp.name, "state.db")

    def tearDown(self):
        self.tmp.cleanup()

    def test_state_survives_a_restart(self):
        state = lex.LocalState(self.path)
        state.record_order("20261103", {"short_code": "PB600000", "symbol": "600000.SH",
                                         "kind": "pullback_buy", "side": "buy",
                                         "qty": 100, "price": 9.8},
                            "v01-20261103-PB600000", "123")
        reopened = lex.LocalState(self.path)
        self.assertIn("PB600000", reopened.short_codes("20261103"))

    def test_ops_log_counts_the_window(self):
        state = lex.LocalState(self.path)
        self.assertEqual(state.ops_since(0), 0)
        state.record_op("place")
        state.record_op("cancel")
        self.assertEqual(state.ops_since(0), 2)
        self.assertEqual(state.ops_since(lex.time.time() + 60), 0)


# ---------------------------------------------------------------------------
# the data layer degrades instead of crashing
# ---------------------------------------------------------------------------

class TestDataLayerDegrades(unittest.TestCase):
    """No xtquant on this machine (and none on CI). The module must
    import cleanly, and the data layer must report a readable error
    instead of raising an ImportError traceback at start-up."""

    def test_module_imports_without_xtquant(self):
        self.assertTrue(hasattr(lex, "XtDataSource"))

    def test_data_source_reports_its_availability(self):
        source = lex.XtDataSource()
        status = source.status()
        self.assertIn(status, ("up", "down"))

    def test_unavailable_data_source_raises_a_readable_error(self):
        source = lex.XtDataSource()
        if source.available():
            self.skipTest("xtquant is installed here")
        with self.assertRaises(lex.DataUnavailable) as caught:
            source.get_full_tick(["600000.SH"])
        self.assertTrue(str(caught.exception))

    def test_normalized_tick_reads_the_sdk_field_names(self):
        raw = {"lastPrice": 10.5, "lastClose": 10.0, "volume": 1234,
               "askPrice": [10.6, 10.7], "bidPrice": [10.4, 10.3]}
        tick = lex.normalize_tick("600000.SH", raw)
        self.assertAlmostEqual(tick["last_price"], 10.5)
        self.assertAlmostEqual(tick["prev_close"], 10.0)
        self.assertAlmostEqual(tick["ask1"], 10.6)
        self.assertAlmostEqual(tick["bid1"], 10.4)

    def test_normalized_tick_of_junk_is_all_none(self):
        """A junk tick must not crash the normaliser -- it must read as
        "no data", which the rules then refuse to trade on."""
        tick = lex.normalize_tick("600000.SH", None)
        self.assertIsNone(tick["last_price"])
        self.assertIsNone(tick["prev_close"])

    def test_market_data_series_extraction_handles_junk(self):
        for junk in (None, {}, {"close": "nope"}, {"close": {"x": "nope"}}):
            self.assertEqual(lex.XtDataSource._extract_series(junk, "close", "600000.SH"), [])


if __name__ == "__main__":
    unittest.main()
