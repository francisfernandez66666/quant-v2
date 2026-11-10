# -*- coding: utf-8 -*-
"""2026-11-10 replay regression (PLAN_CARRIER_RISK_REBUILD P3).

Replays the shape of the 11-10 09:30 incident against the rebuilt run_round.
Acceptance points are the GATE verdicts (blocked / allowed / amount), not
tick-level precision: the scenario fixtures stand in for the day's real
sequence, which the sandbox has no archived ticks for (PLAN section 6 item 6).
"""
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")

_T1 = datetime(2026, 11, 10, 9, 33, 0)
_T2 = datetime(2026, 11, 10, 9, 35, 0)   # >= buy_confirm_min_gap_sec after _T1


def build_sandbox(out_dir):
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox", "--out-dir", out_dir],
                          capture_output=True, text=True, cwd=REPO)
    path = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    return proc.returncode, proc.stdout + proc.stderr, path


class _Row(object):
    def __init__(self, **fields):
        for k, v in fields.items():
            setattr(self, k, v)


class _Counter(object):
    """Counter tables: ACCOUNT / POSITION / ORDER / DEAL, configurable per scenario."""

    def __init__(self, positions=(), deals=(), orders=(),
                 cash=500000.0, total=500000.0):
        self.positions = list(positions)
        self.deals = list(deals)
        self.orders = list(orders)
        self.cash = cash
        self.total = total

    def __call__(self, acct, kind, table):
        if table == "POSITION":
            return list(self.positions)
        if table == "ACCOUNT":
            return [_Row(m_dAvailable=self.cash, m_dBalance=self.total)]
        if table == "DEAL":
            return list(self.deals)
        return list(self.orders)


def _Pos(symbol, qty=1000, cost=10.0, can_use=0, mv=10000.0):
    return _Row(m_strInstrumentID=symbol, stock_code=symbol, m_nVolume=qty,
                volume=qty, m_dOpenPrice=cost, open_price=cost,
                m_nCanUseVolume=can_use, can_use_volume=can_use,
                m_dMarketValue=mv, market_value=mv)


class _Clock(object):
    """Controllable clock: run_round's confirm window and ops budget read
    time.time() through the module's own `time` binding, so tests swap it."""

    def __init__(self):
        self.now = 0.0

    def time(self):
        return self.now


class _Ctx(object):
    def __init__(self, ticks):
        self._ticks = ticks

    def get_full_tick(self, symbols):
        return dict((s, self._ticks.get(s, {})) for s in symbols)

    def get_market_data_ex(self, *args, **kwargs):
        return {}


def _tick(price=10.0, volume=200000, amount=2000000.0):
    """Raw QMT-shaped tick (normalize_tick reads lastPrice/lastClose/askPrice...)."""
    t = {"lastPrice": price, "lastClose": 10.0, "open": 10.0, "high": price,
         "low": 9.9, "askPrice": [price + 0.01], "bidPrice": [price]}
    if volume is not None:
        t["volume"] = volume
    if amount is not None:
        t["amount"] = amount
    return t


SYM = "600000.SH"


def _buy_signal(price=9.7, qty=1100, short="PB600000"):
    return {"side": "buy", "short_code": short, "qty": qty, "price": price,
            "amount": round(price * qty, 2), "symbol": SYM}


class ReplayTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = build_sandbox(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        spec = importlib.util.spec_from_file_location("replay_mod", path)
        cls.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)
        # Rule functions are patched per test; remember the originals so each
        # _reset starts from the artifact's own rule layer, never the previous
        # test's stubs.
        cls._orig_rules = {name: getattr(cls.mod, name)
                           for name in ("rule_pullback_buy", "rule_take_profit",
                                        "rule_stop_loss", "rule_breakdown_sell")}
        cls._orig_cap = cls.mod.PARAMS.get("order_amount_cap")

    def _reset(self, counter, symbol="600000.SH", tick=None, maps=True):
        mod = self.mod
        mod._SANDBOX_FNS.update({
            "passorder": lambda *a: self.placed.append(a),
            "cancel": lambda *a: None,
            "get_account": lambda *a: "8001",
            "get_trade_detail_data": counter})
        mod._STATE.update({"acct": "8001", "acct_type": "", "ops": [],
                           "cancel_tried": [], "warned": {}, "last_round": 0.0,
                           "sent": set(), "buy_intents": set(),
                           "buy_amounts": {}, "buy_seen": {}})
        self.placed = []
        for name, fn in self._orig_rules.items():
            setattr(self.mod, name, fn)
        self.mod.PARAMS["order_amount_cap"] = self._orig_cap
        mod.PARAMS.setdefault("symbols", [symbol])
        mod.PARAMS["symbols"] = [symbol]
        if maps:
            mod.PARAMS["industry_map"] = {symbol: "BANK"}
            mod.PARAMS["concept_map"] = {symbol: "CSI"}
        mod.PARAMS["emergency_stop"] = False
        mod.PARAMS["rule_arm"] = {}
        mod.PARAMS["_PORTED_RULES_FORCED_OFF"] = True
        # One deterministic buy signal per round, injected in place of the
        # pullback rule (gate behaviour is the acceptance point, not the rule).
        self._signal = _buy_signal()
        mod.rule_pullback_buy = lambda params, ctx: dict(self._signal)
        mod._PORTED_RULES.clear()
        self.counter = counter
        self.symbol = symbol
        self.tick = tick or _tick()
        self.clock = _Clock()
        mod.time = self.clock

    def _run_two_rounds(self):
        self.clock.now = _T1.timestamp()
        self.mod.run_round(_Ctx({self.symbol: self.tick}), _T1)
        self.clock.now = _T2.timestamp()
        return self.mod.run_round(_Ctx({self.symbol: self.tick}), _T2)

    # ---- scenario 1: a normal symbol buys AT MOST ONCE and within the 35% cap ----
    def test_s1_normal_symbol_single_buy_within_cap(self):
        self._reset(_Counter(
            positions=[_Pos(SYM, qty=0, cost=0.0, can_use=0, mv=0.0)]))
        summary = self._run_two_rounds()
        self.assertEqual(len(self.placed), 1, "a normal symbol must buy at most once")
        self.assertEqual(summary["placed"], [self._signal["short_code"]])
        amount = float(self.placed[0][5]) * int(self.placed[0][6])
        self.assertLessEqual(amount, 500000.0 * 0.35,
                             "single-stock value must stay within the 35%% cap")

    # ---- scenario 2: the regulated symbol (000520) buys NOTHING ----
    def test_s2_regulated_symbol_zero_orders(self):
        self._reset(_Counter(), symbol="000520.SZ")
        self.mod.PARAMS["risk_list_static"] = ["000520.SZ"]
        self._signal = _buy_signal(short="PB000520")
        summary = self._run_two_rounds()
        self.assertEqual(self.placed, [])
        reasons = [s["reason"] for s in summary["skipped"]]
        self.assertIn("static_risk_list", reasons)

    # ---- scenario 3: tick without amount/volume => VWAP missing => no buy ----
    def test_s3_missing_vwap_no_buy(self):
        self._reset(_Counter(), tick=_tick(volume=None, amount=None))
        summary = self._run_two_rounds()
        self.assertEqual(self.placed, [])
        reasons = [s["reason"] for s in summary["skipped"]]
        self.assertIn("vwap_missing", reasons)

    def test_s3b_suspect_vwap_no_buy(self):
        """amount/volume wildly off the last price => VWAP suspect => no buy."""
        self._reset(_Counter(), tick=_tick(price=10.0, volume=100, amount=100000000.0))
        summary = self._run_two_rounds()
        self.assertEqual(self.placed, [])
        self.assertTrue(any(s["reason"].startswith("vwap_")
                            for s in summary["skipped"]))

    # ---- scenario 4: a NEW position whose final lot-rounded amount < 10k => 0 orders ----
    def test_s4_new_position_below_min_amount_abandoned(self):
        self._reset(_Counter(), tick=_tick(price=33.0, amount=6600000.0))
        self.mod.PARAMS["order_amount_cap"] = 10000.0
        self._signal = _buy_signal(price=33.0, qty=300)  # 9900 < 10000: shred, abandon whole
        summary = self._run_two_rounds()
        self.assertEqual(self.placed, [], "a sub-10k NEW buy must be abandoned whole")
        self.assertTrue(any(s["reason"] == "min_order_amount" for s in summary["skipped"]))

    # ---- scenario 5a: an ADD below 10k is allowed (Q7 exemption), other gates live ----
    def test_s5a_add_below_min_amount_allowed(self):
        self._reset(_Counter(
            positions=[_Pos(SYM, qty=1000, cost=33.0, can_use=0, mv=33000.0)]),
            tick=_tick(price=33.0, amount=6600000.0))
        self.mod.PARAMS["order_amount_cap"] = 5000.0
        self._signal = _buy_signal(price=33.0, qty=100)  # 3300: small add, Q7 exemption
        summary = self._run_two_rounds()
        self.assertEqual(len(self.placed), 1, "an ADD may be small (Q7 exemption)")
        # ...but the single-stock cap still binds: 35% of 500k, held 10k.
        amount = float(self.placed[0][5]) * int(self.placed[0][6])
        self.assertLessEqual(amount, 500000.0 * 0.35 - 10000.0)

    # ---- scenario 5b: a T-leg small buy is allowed and skips the confirm window ----
    def test_s5b_t_leg_small_buy_allowed_first_round(self):
        self._reset(_Counter(
            positions=[_Pos(SYM, qty=1000, cost=10.0, can_use=0, mv=10000.0)]))
        self._signal = _buy_signal(price=9.7, qty=100, short="t0:T1")
        self.clock.now = _T1.timestamp()
        summary = self.mod.run_round(_Ctx({self.symbol: self.tick}), _T1)
        self.assertEqual(len(self.placed), 1, "a T-leg small buy runs on round one")
        self.assertEqual(summary["placed"], ["t0:T1"])

    # ---- scenario 5c: a small SELL is governed by can_use (T+1) ----
    def test_s5c_sell_clipped_to_can_use(self):
        self._reset(_Counter(
            positions=[_Pos(SYM, qty=1000, cost=10.0, can_use=200, mv=10000.0)]))
        self._signal = None

        def sell_signal(params, ctx):
            return {"side": "sell", "short_code": "TP600000", "qty": 1000,
                    "price": 10.5, "amount": 10500.0, "symbol": SYM}
        self.mod.rule_pullback_buy = lambda params, ctx: None
        self.mod.rule_take_profit = lambda params, ctx: sell_signal(params, ctx)
        self.clock.now = _T1.timestamp()
        summary = self.mod.run_round(_Ctx({self.symbol: self.tick}), _T1)
        self.assertEqual(len(self.placed), 1, "the sellable 200 shares are clipped and sent")
        self.assertEqual(int(self.placed[0][6]), 200, "qty must be clipped to can_use")

    def test_s5c2_sell_without_can_use_declines(self):
        self._reset(_Counter(
            positions=[_Pos(SYM, qty=1000, cost=10.0, can_use=None, mv=10000.0)]))
        self._signal = None

        def sell_signal(params, ctx):
            return {"side": "sell", "short_code": "TP600000", "qty": 1000,
                    "price": 10.5, "amount": 10500.0, "symbol": SYM}
        self.mod.rule_pullback_buy = lambda params, ctx: None
        self.mod.rule_take_profit = lambda params, ctx: sell_signal(params, ctx)
        self.mod.run_round(_Ctx({self.symbol: self.tick}), _T1)
        self.assertEqual(self.placed, [], "an unreadable can_use must refuse the exit")

    # ---- guard: emergency_stop refuses every buy, sells still run ----
    def test_s6_emergency_stop_blocks_buys_only(self):
        self._reset(_Counter())
        self.mod.PARAMS["emergency_stop"] = True
        summary = self._run_two_rounds()
        self.assertEqual(self.placed, [])
        self.assertTrue(any(s["reason"] == "emergency_stop" for s in summary["skipped"]))

    # ---- guard: minute-scale matched fills (1212/1818...) are NOT violations ----
    def test_s7_matched_fills_do_not_block_buys(self):
        """Small per-lot matched rows in DEAL are counter fill shapes, not
        rogue orders: a day of them with zero realized loss must not trip
        the day-loss fuse nor block a fresh buy."""
        deals = [_Row(m_strInstrumentID=SYM, stock_code=SYM,
                      m_dPrice=9.7, traded_price=9.7, m_nVolume=1212,
                      traded_volume=1212, m_dTradeAmount=11756.4, amount=11756.4,
                      m_nOrderType=23, order_type=23)
                 for _ in range(6)]
        self._reset(_Counter(deals=deals))
        summary = self._run_two_rounds()
        self.assertEqual(len(self.placed), 1, "matched fills are not violations")


if __name__ == "__main__":
    unittest.main()
