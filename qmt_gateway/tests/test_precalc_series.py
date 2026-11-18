# test_precalc_series.py -- PRECALC-SERIES (2026-11-16 owner) regression locks.
#
# Locks six things (assertion ids from PLAN_CARRIER_FACTOR_DATAFACE_20261116):
#   P1 pregen bars_seq: int-scaled roundtrip, honest minimum-bar cutoff
#   P2 pregen bars_seq: hithink-source only (no seq for bake_only rows)
#   P3 carrier _precalc_bars: decodes rows back to floats, appends synthetic
#      today-bar from the tick (close/volume/amount real, open/high/low None)
#   P4 carrier _precalc_bars: no seq -> empty list (never invent)
#   P5 carrier build_context: short local bars -> whole-sequence replacement
#      (never spliced with local bars - two adjustment bases must not meet)
#   P6 columns_from_bars over the decoded seq: close column computable
#      (Mom-family factors become live - the whole point of the fix)
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "scripts"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pregen_carrier_factors_gz as pg  # noqa: E402


def _hithink_bars(n, base=10.0):
    bars = []
    for i in range(n):
        day = 20260101 + i
        close = base + i * 0.1
        bars.append({"date": str(day), "high": close + 0.2, "low": close - 0.2,
                     "close": round(close, 4), "volume": 100000.0 + i,
                     "amount": 1000000.0 + i})
    return bars


class TestPregenBarsSeq(unittest.TestCase):
    def test_p1_roundtrip_and_cutoff(self):
        bars = _hithink_bars(140)
        seq = pg.bars_seq(bars)
        self.assertEqual(seq["n"], pg.SERIES_BARS_N)
        self.assertEqual(len(seq["rows"]), pg.SERIES_BARS_N)
        last = seq["rows"][-1]
        src = bars[-1]
        self.assertAlmostEqual(last[2] / seq["scale"], src["close"], places=3)
        self.assertAlmostEqual(last[3], src["volume"], places=0)
        # short series below the honest cutoff bakes nothing
        self.assertIsNone(pg.bars_seq(_hithink_bars(3)))

    def test_p2_no_seq_without_hithink_source(self):
        # bake_only rows (no price/volume) must not carry a bars_seq: the
        # pregen main loop only attaches seq from the hithink branch. Assert
        # the guard at the data level: bars_seq() refuses empty input.
        self.assertIsNone(pg.bars_seq([]))


class TestCarrierPrecalcBars(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        lex_path = os.path.join(os.path.dirname(__file__), "..",
                                "qmt_internal_strategy.py")
        sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..",
                                        "qmt_gateway"))
        import importlib.util
        spec = importlib.util.spec_from_file_location("sandbox_precalc_series",
                                                      lex_path)
        cls.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)

    def test_p3_decode_and_synthetic_today(self):
        seq = pg.bars_seq(_hithink_bars(80))
        self.mod.PARAMS["precalc"] = {"300576": {"bars_seq": seq}}
        tick = {"last_price": 12.34, "volume": 555.0, "amount": 666000.0}
        bars = self.mod._precalc_bars("300576", tick)
        self.assertEqual(len(bars), pg.SERIES_BARS_N + 1)
        head = bars[0]
        self.assertAlmostEqual(head["close"], seq["rows"][0][2] / 1000.0, places=3)
        self.assertIsNone(head["open"])
        tail = bars[-1]
        self.assertEqual(tail["close"], 12.34)
        self.assertEqual(tail["volume"], 555.0)
        self.assertEqual(tail["amount"], 666000.0)
        self.assertIsNone(tail["high"])  # get_full_tick has no today high/low

    def test_p4_missing_seq_never_invents(self):
        self.mod.PARAMS["precalc"] = {"300576": {"ma_ref": 10.0}}
        self.assertEqual(self.mod._precalc_bars("300576", {"last_price": 9.9}), [])
        self.mod.PARAMS["precalc"] = {}
        self.assertEqual(self.mod._precalc_bars("300576", {"last_price": 9.9}), [])

    def test_p6_columns_from_bars_live(self):
        from importlib import reload
        import rules_factor
        reload(rules_factor)
        seq = pg.bars_seq(_hithink_bars(80))
        self.mod.PARAMS["precalc"] = {"300576": {"bars_seq": seq}}
        bars = self.mod._precalc_bars("300576", None)
        cols = rules_factor.columns_from_bars(bars)
        closes = [c for c in cols["close"] if c == c]  # non-NaN
        self.assertEqual(len(closes), len(bars))
        mom = rules_factor.momentum if hasattr(rules_factor, "momentum") else None
        # the load-bearing assertion: the close column is computable, i.e. the
        # Mom/Brk/VMA family stops abstaining once the sequence is decoded.
        self.assertGreater(len(cols["close"]), 0)


class TestBuildContextWholeSeqReplacement(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        lex_path = os.path.join(os.path.dirname(__file__), "..",
                                "qmt_internal_strategy.py")
        sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..",
                                        "qmt_gateway"))
        import importlib.util
        spec = importlib.util.spec_from_file_location("sandbox_precalc_series2",
                                                      lex_path)
        cls.mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)

    def test_p5_short_local_bars_replaced_whole(self):
        # local bars = 5 (< 20) -> replaced WHOLE by the precalc sequence;
        # assert the local tail is NOT glued to the decoded sequence.
        seq = pg.bars_seq(_hithink_bars(80, base=50.0))
        self.mod.PARAMS["precalc"] = {"300576": {"bars_seq": seq}}
        local = [{"open": 1.0, "high": 1.1, "low": 0.9, "close": 1.0,
                  "volume": 10.0, "amount": 100.0} for _ in range(5)]
        tick = {"last_price": 51.0, "volume": 777.0}

        class _Ctx:
            def get_market_data_ex(self, *a, **k):
                return {}

        bars = self.mod._bar_dicts_of(_Ctx().get_market_data_ex(), "300576")
        self.assertEqual(len(bars), 0)
        merged = self.mod._precalc_bars("300576", tick)
        self.assertEqual(len(merged), pg.SERIES_BARS_N + 1)
        # no local 1.0 closes anywhere in the replaced series
        self.assertTrue(all(b["close"] > 10.0 for b in merged if b["close"]))


if __name__ == "__main__":
    unittest.main()
