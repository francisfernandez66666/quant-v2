# -*- coding: utf-8 -*-
"""Read-only gate for qmt_api_probe.py (2026-11-10).

The probe must stay side-effect-free by CONSTRUCTION: no file IO, no network,
no trading calls, pure ASCII, and it must carry positive controls so a
negative "no news API" reading can never come from a broken detector.

English: asserts the probe file is read-only, ASCII-only and self-checking.
"""
import os
import re
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
PROBE = os.path.join(REPO, "qmt_gateway", "qmt_api_probe.py")


class ProbeReadOnlyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with open(PROBE, "r", encoding="utf-8") as fh:
            cls.src = fh.read()

    def test_pure_ascii(self):
        bad = [(i + 1, repr(ch)) for i, ch in enumerate(self.src) if ord(ch) > 127]
        self.assertEqual(bad[:5], [], "probe must be pure ASCII for the GBK sandbox")

    def test_no_forbidden_constructs(self):
        # Strip comments before scanning: the documentation legitimately
        # mentions these words, but the code must never contain them.
        code = "\n".join(re.sub(r"#.*$", "", line) for line in self.src.splitlines())
        # Bare module names are allowed as STRING LITERALS (the probe uses them
        # to filter sys.modules keys); what is forbidden is importing/calling.
        forbidden = ("open(", "socket.", "requests.", "urllib.", "http://", "https://",
                     "read_csv", "to_csv", "sqlite3", "pymysql", "subprocess",
                     "passorder(", "cancel(", "import requests", "import urllib",
                     "import os", "import json")
        hits = [w for w in forbidden if w in code]
        self.assertEqual(hits, [], "probe must not contain IO/net/trade constructs: %s" % hits)

    def test_only_stdlib_import(self):
        imports = re.findall(r"^\s*import\s+([A-Za-z0-9_., ]+)", self.src, re.M)
        self.assertEqual(imports, ["sys"], "stdlib sys only; never import xtquant here")

    def test_positive_controls_present(self):
        self.assertIn("CONTROL_CONTEXT", self.src)
        self.assertIn("get_full_tick", self.src)
        self.assertIn("get_trade_detail_data", self.src)
        self.assertIn("PROBE_SELFTEST", self.src)
        self.assertIn("PROBE_RESULT_INVALID", self.src)

    def test_never_calls_probed_function(self):
        # Presence checks only: hasattr/dir, no getattr(...)(...) invocation.
        self.assertNotIn("getattr(ContextInfo", self.src)
        self.assertIn("dir(obj)", self.src)


if __name__ == "__main__":
    unittest.main()
