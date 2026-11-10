# test_carrier_gates_20261110.py — per-gate three-state tests for the 2026-11-10
# carrier risk-rebuild gates (PLAN_CARRIER_RISK_REBUILD section 1.3 / section 2).
#
# These functions live in qmt_gateway/qmt_internal_strategy.py. They are pure
# (only read PARAMS / _STATE and their explicit args), so they are unit-tested
# directly against the SOURCE module with PARAMS / _STATE set and the two
# build-injected helpers (rule_strategy_admit, _counter_rows) monkeypatched.
# This is the "每闸三态用例（放行/命中/缺数据）齐备" half of the DoD; the
# run_round end-to-end behaviour is still covered by test_internal_strategy_sandbox.py.
#
# Run: python3 -m unittest tests.test_carrier_gates_20261110 -v   (no pytest needed)
import importlib.util
import os
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SRC = os.path.join(REPO, "qmt_gateway", "qmt_internal_strategy.py")

spec = importlib.util.spec_from_file_location("qis_20261110", SRC)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def _reset_state():
    # The rebuild added these session registries; keep tests isolated.
    for k in ("sent", "buy_intents", "buy_amounts", "buy_seen"):
        m._STATE[k] = set() if k in ("sent", "buy_intents") else {}


class _Row:
    """A fake QMT DEAL-row object carrying the field families _deals probes."""
    def __init__(self, code, price, volume, amount, otype):
        self.m_strInstrumentID = code
        self.m_dPrice = price
        self.m_nVolume = volume
        self.m_dTradeAmount = amount
        self.m_nOrderType = otype


class GateThreeStateTest(unittest.TestCase):
    def setUp(self):
        # A realistic PARAMS snapshot; every key the gates read is present.
        m.PARAMS = {
            "buy_confirm_rounds": 2,
            "buy_confirm_min_gap_sec": 60,
            "vwap_sanity_lo": 0.5,
            "vwap_sanity_hi": 2.0,
            "risk_list_static": ["000520.SZ"],
            "risk_list_dynamic": ["600000.SH"],
            "asset_guard_factor": 10.0,
            "industry_map": {"000001.SZ": "银行", "600000.SH": "银行"},
            "concept_map": {"000001.SZ": "沪深300", "600000.SH": "上证50"},
            "strategy_kind_to_go": {"ma_cross": "stra_ma_cross"},
            "factor_id_prefix": "fac_",
            "pattern_id_prefix": "pat_",
            "op_type_buy": 23,
            "op_type_sell": 24,
        }
        _reset_state()

    # ---- Q1 confirm window -------------------------------------------------
    def test_buy_confirmed_pass(self):
        ok, why = m._buy_confirmed("000001.SZ", 1000.0)
        self.assertFalse(ok)  # only 1 round seen yet
        ok, why = m._buy_confirmed("000001.SZ", 1000.0 + 70.0)
        self.assertTrue(ok)
        self.assertEqual(why, "")

    def test_buy_confirmed_not_enough_rounds(self):
        ok, why = m._buy_confirmed("X", 1000.0)
        self.assertFalse(ok)
        self.assertTrue(why.startswith("confirm_rounds_1_of_2"))

    def test_buy_confirmed_gap_too_small(self):
        m._buy_confirmed("X", 1000.0)
        ok, why = m._buy_confirmed("X", 1000.0 + 10.0)  # gap < 60
        self.assertFalse(ok)
        self.assertEqual(why, "confirm_gap_pending")

    # ---- Q6 VWAP ----------------------------------------------------------
    def test_vwap_pass(self):
        v, why = m._vwap_of({"volume": 1000.0, "amount": 10000.0, "last_price": 10.0})
        self.assertEqual(v, 10.0)
        self.assertEqual(why, "")

    def test_vwap_missing(self):
        v, why = m._vwap_of({"volume": 0.0, "amount": 10000.0, "last_price": 10.0})
        self.assertIsNone(v)
        self.assertEqual(why, "vwap_missing")

    def test_vwap_suspect(self):
        v, why = m._vwap_of({"volume": 1000.0, "amount": 100000.0, "last_price": 10.0})
        # vwap = 100 >> last*2.0 -> sanity reject
        self.assertIsNone(v)
        self.assertEqual(why, "vwap_suspect")

    # ---- Q3 risk lists ----------------------------------------------------
    def test_risk_reason_pass(self):
        self.assertEqual(m._risk_reason("000001.SZ"), "")

    def test_risk_reason_static(self):
        self.assertEqual(m._risk_reason("000520.SZ"), "static_risk_list")

    def test_risk_reason_dynamic(self):
        self.assertEqual(m._risk_reason("600000.SH"), "llm_bearish")

    # ---- day-loss fuse ----------------------------------------------------
    def test_day_loss_unprovable_none(self):
        pnl, ok = m._day_loss_pct(None, {})
        self.assertIsNone(pnl)
        self.assertFalse(ok)

    def test_day_loss_cost_unreadable(self):
        deals = [{"symbol": "000001.SZ", "side": "sell", "price": 11.0, "qty": 100.0, "amount": 1100.0}]
        pnl, ok = m._day_loss_pct(deals, {"000001.SZ": {"cost": 0.0}})
        self.assertIsNone(pnl)
        self.assertFalse(ok)

    def test_day_loss_provable(self):
        deals = [{"symbol": "000001.SZ", "side": "sell", "price": 11.0, "qty": 100.0, "amount": 1100.0}]
        pnl, ok = m._day_loss_pct(deals, {"000001.SZ": {"cost": 10.0}})
        self.assertTrue(ok)
        self.assertEqual(pnl, (11.0 - 10.0) * 100.0)  # +100

    def test_day_loss_ignores_buys(self):
        deals = [
            {"symbol": "A", "side": "buy", "price": 5.0, "qty": 100.0, "amount": 500.0},
            {"symbol": "A", "side": "sell", "price": 11.0, "qty": 100.0, "amount": 1100.0},
        ]
        pnl, ok = m._day_loss_pct(deals, {"A": {"cost": 10.0}})
        self.assertTrue(ok)
        self.assertEqual(pnl, 100.0)  # buy row contributes nothing

    # ---- §1.4 asset guard -------------------------------------------------
    def test_asset_guard_pass(self):
        positions = {"000001.SZ": {"qty": 100.0, "market_value": 30000.0}}
        self.assertTrue(m._asset_guard_ok(100000.0, 50000.0, positions))

    def test_asset_guard_below_floor(self):
        positions = {"000001.SZ": {"qty": 100.0, "market_value": 30000.0}}
        # total 40000 < floor(max(cash,mv)=50000)
        self.assertFalse(m._asset_guard_ok(40000.0, 50000.0, positions))

    def test_asset_guard_above_factor(self):
        positions = {"000001.SZ": {"qty": 100.0, "market_value": 30000.0}}
        # upper = 50000*10 + 50000 + 30000 = 580000; 600000 exceeds
        self.assertFalse(m._asset_guard_ok(600000.0, 50000.0, positions))

    # ---- concentration buckets -------------------------------------------
    def test_bucket_known(self):
        positions = {"600000.SH": {"qty": 200.0, "market_value": 20000.0}}
        b = m._bucket_view("000001.SZ", positions, {"last_price": 10.0}, 5000.0)
        self.assertTrue(b["industry_known"])
        self.assertTrue(b["cluster_set"])
        # stock_value = session 5000 + own 100*10 (held != symbol) ... 600000.SH mv=20000
        self.assertGreater(b["stock_value"], 5000.0)
        self.assertGreater(b["industry_value"], 0.0)
        self.assertGreater(b["cluster_value"], 0.0)

    def test_bucket_unknown_industry(self):
        positions = {}
        b = m._bucket_view("999999.SZ", positions, {"last_price": 10.0}, 0.0)
        self.assertFalse(b["industry_known"])
        self.assertFalse(b["cluster_set"])

    def test_bucket_known_industry_unknown_concept(self):
        # symbol has industry but no concept bucket
        m.PARAMS["concept_map"] = {}
        positions = {}
        b = m._bucket_view("000001.SZ", positions, {"last_price": 10.0}, 0.0)
        self.assertTrue(b["industry_known"])
        self.assertFalse(b["cluster_set"])

    # ---- Q7 intent --------------------------------------------------------
    def test_intent_new(self):
        self.assertEqual(m._intent_of({"short_code": "abc", "side": "buy"}, 0.0), "new")

    def test_intent_add(self):
        self.assertEqual(m._intent_of({"short_code": "abc", "side": "buy"}, 100.0), "add")

    def test_intent_reduce(self):
        self.assertEqual(m._intent_of({"short_code": "abc", "side": "sell"}, 100.0), "reduce")

    def test_intent_t_leg(self):
        self.assertEqual(m._intent_of({"short_code": "t0:abc", "side": "buy"}, 0.0), "t_leg")
        self.assertEqual(m._intent_of({"short_code": "t:abc", "side": "buy"}, 0.0), "t_leg")

    # ---- Q12 strategy whitelist ------------------------------------------
    def test_go_admitted_mapped_and_allowed(self):
        m.rule_strategy_admit = lambda params, go_id: go_id == "stra_ma_cross"
        self.assertTrue(m._go_admitted("ma_cross"))

    def test_go_admitted_unmappable_refused(self):
        m.rule_strategy_admit = lambda params, go_id: True
        self.assertFalse(m._go_admitted("unknown_kind"))  # not mapped, no prefix

    def test_go_admitted_whitelist_denies(self):
        m.rule_strategy_admit = lambda params, go_id: False
        self.assertFalse(m._go_admitted("ma_cross"))

    def test_go_admitted_factor_prefix_passthrough(self):
        m.rule_strategy_admit = lambda params, go_id: go_id == "fac_momentum"
        self.assertTrue(m._go_admitted("fac_momentum"))

    # ---- DEAL parsing -----------------------------------------------------
    def test_deals_pass(self):
        m._counter_rows = lambda table: [
            _Row("000001.SZ", 10.0, 100.0, 1000.0, 23),  # buy
            _Row("000001.SZ", 11.0, 50.0, 550.0, 24),    # sell
        ]
        out = m._deals()
        self.assertEqual(len(out), 2)
        self.assertEqual(out[0]["side"], "buy")
        self.assertEqual(out[1]["side"], "sell")

    def test_deals_unreadable(self):
        m._counter_rows = lambda table: None
        self.assertIsNone(m._deals())

    def test_deals_empty(self):
        m._counter_rows = lambda table: []
        self.assertEqual(m._deals(), [])


if __name__ == "__main__":
    unittest.main()
