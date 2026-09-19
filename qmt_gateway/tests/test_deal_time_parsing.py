#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""_XtOps.embed_trades_rows 的 DEAL 时间解析单测（§生产 2026-09-16 幽灵时间事故）。

背景：柜台 m_strTradeTime 在 9 点段丢前导零（"09:57:20"→"95720" 共 5 位），
旧代码按固定 6 位 HHMMSS 切片产出 "95:72:20" 幽灵时间并落进引擎流水。
覆盖：5 位补零回正、6 位常规路径、仅时间无日期路径。临时注入 _gtdd/_obj_code，
不需要 Windows/xtquant。
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import qmt_bridge_strategy as bs  # noqa: E402


def deal_row(td, tt, price=4.35, qty=100):
    """构造一条最小可用 DEAL 行对象（字段名对齐柜台属性）。"""
    return type("Deal", (), {
        "m_strTradeDate": td, "m_strTradeTime": tt,
        "m_dPrice": price, "m_nVolume": qty, "m_dTradeAmount": price * qty,
        "m_nDirection": 48, "m_nOrderType": 0, "m_strRemark": "",
        "m_strTradeID": "T1", "m_strOrderSysID": "S1",
    })()


# 成交时间字段解析回归测试：不同 xtquant build 的时间格式统一归一化。
class TestDealTimeParsing(unittest.TestCase):
    def _ops_with(self, rows):
        ops = bs._XtOps(account="ACC", dry_run=True, xt_path="", session_id=2)
        ops._gtdd = lambda table: rows
        ops._obj_code = lambda o: "600279.SH"
        return ops

    def test_five_digit_morning_time_backfilled(self):
        """5 位 "95720" 必须补零成 09:57:20，不得产出 95:72 幽灵。"""
        row = self._ops_with([deal_row("20260915", "95720")]).embed_trades_rows()[0]
        self.assertEqual(row["traded_at"], "2026-09-15T09:57:20+08:00")

    def test_five_digit_no_seconds_loss(self):
        """5 位 "95700"（整分成交）→ 09:57:00。"""
        row = self._ops_with([deal_row("20260915", "95700")]).embed_trades_rows()[0]
        self.assertEqual(row["traded_at"], "2026-09-15T09:57:00+08:00")

    def test_six_digit_reg_path_unchanged(self):
        """常规 6 位（2026-09-14 修复的原路径）不回退。"""
        row = self._ops_with([deal_row("20260914", "131351")]).embed_trades_rows()[0]
        self.assertEqual(row["traded_at"], "2026-09-14T13:13:51+08:00")

    def test_time_only_branch(self):
        """无日期仅 5 位时间：也须补零（日期取当天）。"""
        row = self._ops_with([deal_row("", "95720")]).embed_trades_rows()[0]
        self.assertTrue(row["traded_at"].endswith("T09:57:20+08:00"))

    def test_empty_time_stays_date_only(self):
        """时间整体缺失：不得被 zfill 伪造成 00:00:00，退回纯日期。"""
        row = self._ops_with([deal_row("20260915", "")]).embed_trades_rows()[0]
        self.assertEqual(row["traded_at"], "20260915")


if __name__ == "__main__":
    unittest.main()
