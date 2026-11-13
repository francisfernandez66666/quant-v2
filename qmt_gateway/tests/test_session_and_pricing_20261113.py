# test_session_and_pricing_20261113.py -- 2026-11-13 实盘事故回归锁。
#
# Locks the three defects owner reported on 11-13:
#   1. overnight orders at 00:00 (the old no_*_windows were BLACKLISTS);
#   2. one round filling the whole budget (pending_buys was always empty);
#   3. "VWAP minus 0.7%" buy limit never existed.
#
# English: regression locks for the trading-session whitelist, the existing
# sizing chain's pending-buy reservation, and the VWAP pullback limit price.
import datetime
import importlib.util
import os
import subprocess
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")

WINDOWS = [["09:30", "11:30"], ["13:00", "14:52"]]


def _build(out_dir):
    proc = subprocess.run(
        [sys.executable, BUILDER, "--target", "sandbox", "--out-dir", out_dir],
        capture_output=True, text=True, cwd=REPO)
    path = ""
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    return proc.returncode, proc.stdout + proc.stderr, path


def _load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class SessionAndPricingTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Fixed OUTSIDE-repo dir instead of tempfile.mkdtemp(): this machine's
        # sitecustomize cannot resolve a system temp dir under the sandbox, and
        # the builder refuses an out_dir inside the repository (drift gate).
        # An environment failure must not be reported as a red gate.
        cls.dir = os.path.join(os.path.sep, "tmp", "quant_sandbox_20261113_tests")
        os.makedirs(cls.dir, exist_ok=True)
        rc, out, path = _build(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.mod = _load(path, "sandbox_20261113")
        cls.base = dict(cls.mod.PARAMS)

    def setUp(self):
        self.mod.PARAMS.clear()
        self.mod.PARAMS.update(self.base)
        self.mod.PARAMS["session_windows"] = [list(w) for w in WINDOWS]
        self.mod.PARAMS["vwap_pullback_pct"] = 0.7
        self.mod.PARAMS["entry_price_mode"] = "vwap_pullback"
        self.mod.PARAMS["min_cash_after_reserve_pct"] = 0.2
        self.mod.PARAMS["closed_days"] = []
        self.mod.PARAMS["non_trading_weekdays"] = [5, 6]
        self.mod._STATE["round_moment"] = None

    # ---- T2: trading-session whitelist -----------------------------------
    def test_session_matrix(self):
        """14 clock points + weekend: only the two cash sessions are open."""
        cases = [
            ((2026, 11, 13, 0, 0), False, "overnight 00:00 (the incident)"),
            ((2026, 11, 13, 8, 55), False, "pre-open"),
            ((2026, 11, 13, 9, 29), False, "call auction"),
            ((2026, 11, 13, 9, 30), True, "open"),
            ((2026, 11, 13, 10, 0), True, "morning"),
            ((2026, 11, 13, 11, 29), True, "morning tail"),
            ((2026, 11, 13, 11, 30), False, "lunch start"),
            ((2026, 11, 13, 12, 0), False, "lunch"),
            ((2026, 11, 13, 13, 0), True, "afternoon open"),
            ((2026, 11, 13, 14, 51), True, "last minute"),
            ((2026, 11, 13, 14, 52), False, "close gate"),
            ((2026, 11, 13, 14, 57), False, "no 14:57 cancel exception"),
            ((2026, 11, 13, 15, 30), False, "after close"),
            ((2026, 11, 13, 23, 59), False, "late night"),
            ((2026, 11, 14, 10, 0), False, "Saturday"),
        ]
        for tup, expect, label in cases:
            moment = datetime.datetime(*tup)
            ok, why = self.mod._trade_session_ok(moment)
            self.assertEqual(ok, expect, "%s %s -> %s (%s)" % (label, moment, ok, why))

    def test_missing_windows_fail_closed(self):
        self.mod.PARAMS.pop("session_windows", None)
        ok, why = self.mod._trade_session_ok(datetime.datetime(2026, 11, 13, 10, 0))
        self.assertFalse(ok)
        self.assertEqual(why, "session_windows_missing")
        self.mod.PARAMS["session_windows"] = []
        ok, why = self.mod._trade_session_ok(datetime.datetime(2026, 11, 13, 10, 0))
        self.assertFalse(ok, "an empty whitelist must refuse, not allow everything")

    def test_closed_day_is_refused(self):
        self.mod.PARAMS["closed_days"] = ["20261113"]
        ok, why = self.mod._trade_session_ok(datetime.datetime(2026, 11, 13, 10, 0))
        self.assertFalse(ok)
        self.assertEqual(why, "not_trading_day")

    def test_place_backstop_refuses_overnight(self):
        """The _place gate cannot be bypassed by calling it directly."""
        calls = []
        self.mod._SANDBOX_FNS.update({"passorder": lambda *a: calls.append(a)})
        self.mod._account = lambda: "8001"
        self.mod._STATE["round_moment"] = datetime.datetime(2026, 11, 13, 0, 0)
        ok, why = self.mod._place(None, {
            "side": "buy", "symbol": "600000.SH", "price": 10.0,
            "qty": 100, "amount": 1000.0, "short_code": "PB600000"})
        self.assertFalse(ok)
        self.assertEqual(why, "outside_trading_session")
        self.assertEqual(calls, [], "passorder must not be reached outside the session")

    def test_cancel_backstop_refuses_overnight(self):
        calls = []
        self.mod._SANDBOX_FNS.update({"cancel": lambda *a: calls.append(a)})
        self.mod._account = lambda: "8001"
        self.mod._STATE["round_moment"] = datetime.datetime(2026, 11, 13, 0, 0)
        ok, why = self.mod._cancel("12345")
        self.assertFalse(ok)
        self.assertEqual(why, "outside_trading_session")
        self.assertEqual(calls, [])

    def test_round_outside_session_places_nothing(self):
        calls = []
        self.mod._SANDBOX_FNS.update({
            "passorder": lambda *a: calls.append(a),
            "cancel": lambda *a: calls.append(a),
            "get_trade_detail_data": lambda *a: []})
        summary = self.mod.run_round(None, datetime.datetime(2026, 11, 13, 0, 0))
        self.assertEqual(calls, [])
        self.assertEqual(summary["placed"], [])
        self.assertEqual(summary["reason"], "outside_trading_session")

    # ---- T3: pending buys feed the EXISTING sizing chain -----------------
    def test_pending_buy_rows_only_count_our_open_buys(self):
        orders = [
            {"short_code": "PB600000", "terminal": False,
             "qty": 1000, "traded": 0, "price": 10.0},
            {"short_code": "PB600001", "terminal": True,
             "qty": 1000, "traded": 0, "price": 10.0},
            {"short_code": "XX600002", "terminal": False,
             "qty": 1000, "traded": 0, "price": 10.0},
            {"short_code": "PB600003", "terminal": False,
             "qty": 1000, "traded": 1000, "price": 10.0},
            {"short_code": "PB600004", "terminal": False,
             "qty": 1000, "traded": 400, "price": 10.0},
        ]
        rows = self.mod._pending_buy_rows(orders)
        self.assertEqual(len(rows), 2, "only open, partly/fully unfilled OUR buys")
        self.assertEqual(rows[0]["qty"], 1000.0)
        self.assertEqual(rows[1]["qty"], 600.0)
        self.assertEqual(rows[0]["price"], 10.0)
        self.assertIn("remain", rows[0], "rules_sizing reads qty/remain + price")

    def test_sizing_view_feeds_real_pending_buys(self):
        """The chain must see reservations, not an unconditional empty list."""
        rows = [{"qty": 100, "remain": 100, "price": 10.0}]
        self.mod._account_total_asset = lambda: 100000.0
        view = self.mod._sizing_view({}, {}, rows)
        self.assertEqual(view["pending_buys"], rows)
        empty = self.mod._sizing_view({}, {}, [])
        self.assertEqual(empty["pending_buys"], [])

    # ---- T4: VWAP minus 0.7% ---------------------------------------------
    def test_pullback_limit_price(self):
        self.assertEqual(self.mod._pullback_limit_price(10.0, 10.0), 9.93)
        self.assertEqual(self.mod._pullback_limit_price(10.0, 9.90), 9.90,
                         "never rest above the last price")
        self.assertIsNone(self.mod._pullback_limit_price(None, 10.0))
        self.assertIsNone(self.mod._pullback_limit_price(10.0, 0))

    def test_pullback_pct_is_a_percentage_not_a_fraction(self):
        """0.007 would be 0.7 basis point -- a 100x unit error."""
        self.mod.PARAMS["vwap_pullback_pct"] = 0.007
        self.assertNotEqual(self.mod._pullback_limit_price(10.0, 10.0), 9.93)


if __name__ == "__main__":
    unittest.main()
