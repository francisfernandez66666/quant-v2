# test_hotreload_buy_codes.py -- §INTRADAY-BUYSIG (2026-11-16 owner) locks.
#
#   B1 extract_buy_codes: action/kind=="buy" only; watch/N形观察 and 做空/利空
#      rows are excluded; non-6-digit codes dropped.
#   B2 diff semantics live in the caller (main): state file holds the LAST
#      PUBLISH baseline; the file-level contract here locks the parser against
#      the real signals_today row shape (action OR kind, direction filter).
import importlib.util
import json
import os
import sys
import tempfile
import unittest

HR_PATH = os.path.join(os.path.dirname(__file__), "..", "..", "scripts",
                       "carrier_hot_reload_gz.py")
spec = importlib.util.spec_from_file_location("hotreload_for_test", HR_PATH)
hr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hr)


class TestExtractBuyCodes(unittest.TestCase):
    def _run(self, rows):
        tmp = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False,
                                          encoding="utf-8")
        json.dump(rows, tmp, ensure_ascii=False)
        tmp.close()
        old = hr.SIGNALS_TODAY
        hr.SIGNALS_TODAY = tmp.name
        try:
            return hr.extract_buy_codes()
        finally:
            hr.SIGNALS_TODAY = old
            os.unlink(tmp.name)

    def test_b1_buy_only_shape(self):
        rows = [{"code": "600335", "kind": "buy", "confidence": 0.93},
                {"code": "002371", "kind": "watch"},                     # not buy
                {"code": "000520", "action": "buy", "direction": "利空"},  # filtered
                {"code": "12ab", "kind": "buy"},                          # bad code
                {"code": "300576", "kind": "buy", "confidence": 0.99}]
        self.assertEqual(self._run(rows), {"600335", "300576"})

    def test_b1b_unreadable_file_empty(self):
        hr.SIGNALS_TODAY = "/nonexistent/signals_today.json"
        self.assertEqual(hr.extract_buy_codes(), set())

    def test_b2_state_path_registered(self):
        # the diff baseline must exist as a module constant (written back only
        # on the publish leg - the caller contract, locked in §176).
        self.assertTrue(hasattr(hr, "BUY_STATE_PATH"))


if __name__ == "__main__":
    unittest.main()
