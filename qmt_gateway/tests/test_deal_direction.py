#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""DEAL 成交方向解析单测（§P0 2026-09-18「买入卖出不分」事故回归）。

事故形态：09-17 一笔真实**手动卖出**（600580.SH 800股 @26.71）在成交流水里被记成
**买入**（quant 页成交流水「方向」列 = fills.side 直出）——成本/已实现盈亏/胜率全线污染。

根因面（本文件逐条钉死）：
 1. 旧实现 `drc in (0,48) → 买入`：只读 m_nDirection 一个字段，命中即判买，无视同行的
    m_nOffsetFlag / m_nOrderType 是否指向卖出（柜台枚举空间跨构建不一致，本项目已两次踩坑）；
 2. 卖出值集合漏了 50——项目自己的实证口径是 offset 48=买 / **50=卖**（见
    docs/archive/AUTO_TRADING_UAT_20260831.md §10.3 与 handler._side_of）；
 3. 未命中组合静默落 SELL，无任何留痕，缺陷因此潜伏数日。

覆盖：_deal_side 多枚举空间投票、冲突不盲判、未知组合留痕一次且落保守默认；
embed_trades_rows 端到端（真实卖出不得再被记成买入）。临时注入 _gtdd/_obj_code，
不需要 Windows/xtquant。
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import qmt_bridge_strategy as bs  # noqa: E402

BUY = bs.BUY   # 买入（文件内以 unicode 转义常量定义）
SELL = bs.SELL  # 卖出


def deal(**kw):
    """构造一条最小可用 DEAL 行对象（只带本次断言关心的字段）。"""
    attrs = {"m_dPrice": 26.71, "m_nVolume": 800, "m_dTradeAmount": 21368.0,
             "m_strRemark": "", "m_strTradeID": "T1", "m_strOrderSysID": "S1"}
    attrs.update(kw)
    return type("Deal", (), attrs)()


class TestDealSideEnumSpaces(unittest.TestCase):
    """柜台方向枚举空间（《AUTO_TRADING_UAT_20260831》§10.3 + drill-3 DEAL dump）。"""

    def test_direction_space_48_buy_49_sell(self):
        """m_nDirection：48（ASCII '0'）买 / 49（ASCII '1'）卖。"""
        self.assertEqual(bs._deal_side(deal(m_nDirection=48))[0], BUY)
        self.assertEqual(bs._deal_side(deal(m_nDirection=49))[0], SELL)

    def test_direction_space_accepts_ints_0_1(self):
        """同一字段的整型形态 0/1 也须识别。"""
        self.assertEqual(bs._deal_side(deal(m_nDirection=0))[0], BUY)
        self.assertEqual(bs._deal_side(deal(m_nDirection=1))[0], SELL)

    def test_offset_space_50_is_sell(self):
        """offset 口径 50=卖（本机实证）：卖出值集合必须含 50，否则会漏判。"""
        self.assertEqual(bs._deal_side(deal(m_nOffsetFlag=50))[0], SELL)
        self.assertEqual(bs._deal_side(deal(m_nOffsetFlag=48))[0], BUY)
        self.assertEqual(bs._deal_side(deal(offset_type=50))[0], SELL)

    def test_order_type_spaces(self):
        """order_type/m_nOrderType：本构建 23/24 与主流文档 1101/1102 两套空间。"""
        for ot, want in ((23, BUY), (24, SELL), (1101, BUY), (1102, SELL)):
            self.assertEqual(bs._deal_side(deal(m_nOrderType=ot))[0], want,
                             "order_type=%s 判反" % ot)
            self.assertEqual(bs._deal_side(deal(order_type=ot))[0], want,
                             "order_type=%s 判反" % ot)

    def test_conflicting_fields_do_not_force_buy(self):
        """核心回归：m_nDirection=48（读作买）与 m_nOffsetFlag=50（读作卖）冲突时，
        绝不因为"某个字段读出 48"就判买入——旧实现正是在这里把真实卖出记成买入。"""
        side, desc = bs._deal_side(deal(m_nDirection=48, m_nOffsetFlag=50))
        self.assertEqual(side, SELL, "冲突行不得盲判买入（desc=%s）" % desc)

    def test_conflict_falls_back_to_order_type(self):
        """冲突时以 order_type 为下一权威；仍不可判则落保守默认。"""
        self.assertEqual(
            bs._deal_side(deal(m_nDirection=48, m_nOffsetFlag=50, m_nOrderType=24))[0], SELL)
        self.assertEqual(
            bs._deal_side(deal(m_nDirection=48, m_nOffsetFlag=50, m_nOrderType=23))[0], BUY)

    def test_string_direction_probe(self):
        """末位兜底：显式方向字符串（各构建字段名不一）。"""
        self.assertEqual(bs._deal_side(deal(direction="BUY"))[0], BUY)
        self.assertEqual(bs._deal_side(deal(bs_type="SELL"))[0], SELL)
        self.assertEqual(bs._deal_side(deal(entrust_bs="0"))[0], BUY)
        self.assertEqual(bs._deal_side(deal(entrust_bs="1"))[0], SELL)

    def test_unknown_combination_is_traced_once(self):
        """全字段不可判：落保守默认 SELL（持仓由 30s 全量对账纠偏），
        但必须留痕一次且仅一次——旧实现的静默兜底正是缺陷潜伏数日的原因。"""
        bs._DIR_TRACE_SEEN.clear()
        d = deal(m_nDirection=99, m_nOffsetFlag=99, offset_type=99, m_nOrderType=99)
        first, desc1 = bs._deal_side(d)
        second, _ = bs._deal_side(d)
        self.assertEqual(first, SELL)
        self.assertEqual(second, SELL)
        self.assertEqual(len(bs._DIR_TRACE_SEEN), 1,
                         "同一未知组合只留痕一次（不得每次轮询刷屏）: %s" % desc1)

    def test_descriptor_is_ascii(self):
        """留痕串必须纯 ASCII：桥运行在 GBK 沙箱，非 ASCII 会写成乱码。"""
        _side, desc = bs._deal_side(deal(m_nDirection=99, m_nOrderType=99,
                                        direction="\u4e70"))
        desc.encode("ascii")  # 非 ASCII 直接抛 UnicodeEncodeError


class TestEmbedTradesRowsSide(unittest.TestCase):
    """端到端：DEAL 行 → embed_trades_rows → side（成交流水的方向来源）。"""

    def _ops_with(self, rows):
        ops = bs._XtOps(account="ACC", dry_run=True, xt_path="", session_id=2)
        ops._gtdd = lambda table: rows
        ops._obj_code = lambda o: "600580.SH"
        return ops

    def test_real_sell_with_offset_50_is_not_booked_as_buy(self):
        """事故直接回归：offset 口径的真实卖出（50）不得再输出"买入"。"""
        row = self._ops_with([deal(m_nOffsetFlag=50, m_nDirection=48)]).embed_trades_rows()[0]
        self.assertEqual(row["side"], SELL)
        self.assertEqual(row["code"], "600580.SH")
        self.assertEqual(row["qty"], 800)

    def test_real_buy_still_buy(self):
        """不误伤：买入仍判买入（drill-3 修复的原始诉求保持）。"""
        row = self._ops_with([deal(m_nDirection=48)]).embed_trades_rows()[0]
        self.assertEqual(row["side"], BUY)

    def test_unresolved_row_raises_no_exception(self):
        """未知枚举空间：不得抛异常打断整轮成交上报（其余成交仍须落账）。"""
        row = self._ops_with([deal(m_nDirection=77, m_nOrderType=77)]).embed_trades_rows()[0]
        self.assertIn(row["side"], (BUY, SELL))


if __name__ == "__main__":
    unittest.main()
