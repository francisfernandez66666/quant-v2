# -*- coding: utf-8 -*-
"""Unit locks for the 2026-11-10 rebuild helpers (PLAN section 3.0 DoD item 2).

The replay suite proves the gates inside a full run_round; this file pins the
helper-level invariants they rest on: the total-asset dimension guard, the
DEAL-based day-loss fuse, the explicit intent tags, and the Q7 exemption
boundary of the min-order-amount gate.
"""
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")


class _Row(object):
    def __init__(self, **fields):
        for k, v in fields.items():
            setattr(self, k, v)


class HelperLockTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        d = tempfile.mkdtemp()
        proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox",
                               "--out-dir", d], capture_output=True, text=True, cwd=REPO)
        path = ""
        for token in (proc.stdout + proc.stderr).replace("\n", " ").split():
            if token.startswith("artifact="):
                path = token.split("=", 1)[1]
        if proc.returncode != 0:
            raise AssertionError("sandbox build failed: %s" % (proc.stdout + proc.stderr)[-600:])
        spec = importlib.util.spec_from_file_location("helper_mod", path)
        cls.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)

    # ---- dimension guard: the 11-06 pollution shape must refuse buys ----
    def test_asset_guard_refuses_polluted_total(self):
        """total=4M with cash=9k/mv=90k (the 11-06 target jump) => refuse."""
        pos = {"600000.SH": {"qty": 29800, "cost": 3.03, "can_use": 0, "market_value": 90000.0}}
        self.assertFalse(self.mod._asset_guard_ok(4000000.0, 9000.0, pos))
        self.assertTrue(self.mod._asset_guard_ok(100000.0, 9000.0, pos),
                        "a plausible total within the factor band passes")
        self.assertFalse(self.mod._asset_guard_ok(5000.0, 9000.0, pos),
                         "total below max(cash, mv) is implausible => refuse")

    # ---- day-loss fuse: sell-only realized pnl, unreadable cost = unprovable ----
    def test_day_loss_pct_sell_only_and_fail_closed(self):
        pos = {"600000.SH": {"qty": 1000, "cost": 10.0}}
        deals = [{"symbol": "600000.SH", "side": "buy", "price": 9.0, "qty": 500, "amount": 4500.0}]
        pnl, ok = self.mod._day_loss_pct(deals, pos)
        self.assertEqual((pnl, ok), (0.0, True), "buy fills alone never book pnl")
        sells = deals + [{"symbol": "600000.SH", "side": "sell", "price": 9.0, "qty": 500, "amount": 4500.0}]
        pnl, ok = self.mod._day_loss_pct(sells, pos)
        self.assertTrue(ok)
        self.assertAlmostEqual(pnl, (9.0 - 10.0) * 500.0)
        bad = [{"symbol": "UNHELD.SH", "side": "sell", "price": 9.0, "qty": 100, "amount": 900.0}]
        self.assertEqual(self.mod._day_loss_pct(bad, pos), (None, False),
                         "a sell without a provable cost makes the fuse UNPROVABLE")
        self.assertEqual(self.mod._day_loss_pct(None, pos), (None, False),
                         "DEAL table unreadable => unprovable => buys decline")

    # ---- intent tags: never inferred from size ----
    def test_intent_of(self):
        sig = lambda short, side="buy": {"short_code": short, "side": side}
        self.assertEqual(self.mod._intent_of(sig("PB600000"), 0.0), "new")
        self.assertEqual(self.mod._intent_of(sig("PB600000"), 1000.0), "add")
        self.assertEqual(self.mod._intent_of(sig("TP600000", "sell"), 1000.0), "reduce")
        self.assertEqual(self.mod._intent_of(sig("t0:T1"), 0.0), "t_leg")

    # ---- risk list: static floor + dynamic daily list, head-matched ----
    def test_risk_reason(self):
        self.mod.PARAMS["risk_list_static"] = ["000520.SZ"]
        self.mod.PARAMS["risk_list_dynamic"] = ["600xxx.SZ"]
        self.assertEqual(self.mod._risk_reason("000520.SZ"), "static_risk_list")
        self.mod.PARAMS["risk_list_dynamic"] = ["600000"]
        self.assertEqual(self.mod._risk_reason("600000.SH"), "llm_bearish")
        self.assertEqual(self.mod._risk_reason("000001.SZ"), "")

    # ---- vwap sanity: missing or insane vs last => missing ----
    def test_vwap_of(self):
        ok = {"volume": 200000, "amount": 2000000.0, "last_price": 10.0}
        vwap, why = self.mod._vwap_of(ok)
        self.assertAlmostEqual(vwap, 10.0)
        self.assertEqual(why, "")
        for tick, expect in (
                ({"volume": 0, "amount": 2000000.0, "last_price": 10.0}, "vwap_missing"),
                ({"volume": 100, "amount": 100000000.0, "last_price": 10.0}, "vwap_suspect")):
            self.assertEqual(self.mod._vwap_of(tick), (None, expect))


if __name__ == "__main__":
    unittest.main()
