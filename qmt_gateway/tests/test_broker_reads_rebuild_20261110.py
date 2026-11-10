# -*- coding: utf-8 -*-
"""2026-11-10 rebuild regression tests (PLAN_CARRIER_RISK_REBUILD 1.3 item 12).

The W1 uplink produced ZERO events on 11-10 because of four stacked defects:
subdirectory exports invisible to a single-level scan, HK-Connect shell files
winning the mtime race, HKD column stealing market_value, and the QMT
"PositionStatics" header missing the qty alias. Each test here pins one fix.
"""
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import broker_reads  # noqa: E402


class TestRmbColumnPriority(unittest.TestCase):
    def test_rmb_wins_over_hkd(self):
        """总市值（港币） must NOT be the column that feeds market_value.

        The old _norm_header stripped the currency suffix, both columns
        collapsed to 总市值, and the contains-match grabbed the HKD column
        (value 0) -- frozen_cash then booked the whole portfolio as frozen.
        """
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "Account-20261110_100000.csv")
            header = ("证券公司,资金账号,冻结金额,总资产（人民币）,可用金额,"
                      "总市值（港币）,可取金额,交易日,股票总市值,净资产,总市值（人民币）")
            row = "东莞证券,002069008957,0,91000,1001,0,1001,20261110,90300,91000,90300"
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(header + "\n" + row + "\n")
            res = broker_reads.parse_asset(path)
            self.assertEqual(res.state, broker_reads.STATE_OK, res.reason)
            rec = res.rows[0]
            self.assertEqual(rec.get("total_assets"), 91000)
            self.assertEqual(rec.get("usable_cash"), 1001)
            # The RMB (or 股票总市值) reading, never the HKD 0.
            self.assertEqual(rec.get("market_value"), 90300)


class TestPositionStaticsAlias(unittest.TestCase):
    def test_dangqian_yonggu_is_qty(self):
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "PositionStatics-20261110_100000.csv")
            with open(path, "w", encoding="gbk") as fh:
                fh.write("证券代码,证券名称,当前拥股,可用拥股,参考成本价\n")
                fh.write("000520,凤凰航运,29800,0,3.03\n")
            res = broker_reads.parse_positions(path)
            self.assertEqual(res.state, broker_reads.STATE_OK, res.reason)
            self.assertEqual(res.rows[0].get("qty"), 29800)
            self.assertEqual(res.rows[0].get("usable_qty"), 0)


class TestRecursiveDiscovery(unittest.TestCase):
    def test_subdir_export_is_found(self):
        """Exports under D:\\qmt\\Stock (a subdirectory) must be discovered.

        The old single-level `root/*` scan returned file_not_found for every
        kind -- the whole-day zero-event incident.
        """
        with tempfile.TemporaryDirectory() as td:
            sub = os.path.join(td, "Stock")
            os.makedirs(sub)
            path = os.path.join(sub, "Account-20261110_100000.csv")
            with open(path, "w", encoding="utf-8") as fh:
                fh.write("总资产,可用资金\n91000,1001\n")
            hits = broker_reads.discover_exports_recursive([td], [td])
            kinds = [e.kind for e in hits]
            self.assertIn("asset", kinds)
            self.assertEqual(hits[0].path, os.path.realpath(path))

    def test_shell_roots_still_rejected(self):
        """Recursion must NOT widen the whitelist: outside roots stays invisible."""
        with tempfile.TemporaryDirectory() as outer, tempfile.TemporaryDirectory() as other:
            path = os.path.join(other, "Account-20261110.csv")
            with open(path, "w", encoding="utf-8") as fh:
                fh.write("总资产,可用资金\n1,1\n")
            hits = broker_reads.discover_exports_recursive([outer], [outer])
            self.assertEqual(hits, [])


class TestFundAccountFilter(unittest.TestCase):
    def _uplink(self, fund_account, rows):
        import report_uplink
        cfg = {"report_url": "", "report_token": "t", "uplink_fund_account": fund_account}
        up = report_uplink.ReportUplink.__new__(report_uplink.ReportUplink)
        up.fund_account = fund_account
        up.skips = []
        up.skip_keep = 50
        import time as _time
        up._now_fn = _time.time
        return up._filter_fund_account("asset", rows)

    def test_matching_rows_kept(self):
        rows = [{"fund_account": "002069008957", "total_assets": 91000},
                {"fund_account": "888888888888", "total_assets": 0}]
        out = self._uplink("002069008957", rows)
        self.assertEqual(len(out), 1)
        self.assertEqual(out[0]["total_assets"], 91000)

    def test_all_mismatch_is_missing(self):
        rows = [{"fund_account": "888888888888", "total_assets": 0}]
        self.assertIsNone(self._uplink("002069008957", rows))

    def test_no_column_no_filter(self):
        rows = [{"total_assets": 91000}]
        out = self._uplink("002069008957", rows)
        self.assertEqual(out, rows)

    def test_unconfigured_no_filter(self):
        rows = [{"fund_account": "888", "total_assets": 1}]
        out = self._uplink("", rows)
        self.assertEqual(out, rows)


if __name__ == "__main__":
    unittest.main()
