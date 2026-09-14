#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway 单测：xtquant 回调对象映射层（§P2-15，2026-09-15）。

handler._side_of / _signal_of / _status 是历史上三次生产事故的源头
（side 全判卖 / signal 恒空 / status 丢拒因），此前零覆盖。
用 SimpleNamespace 模拟 xtquant 回调对象，逐枚举空间断言，无需 Windows/xtquant。
"""
import logging
import os
import sys
import types
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
logging.disable(logging.CRITICAL)

from handler import ReportHandler  # noqa: E402


def xt_obj(**kw):
    """构造模拟 xtquant 回调对象（缺失属性 getattr 默认值与真实对象一致）。"""
    return types.SimpleNamespace(**kw)


class TestSideOf(unittest.TestCase):
    """§FIX 2026-08-31 方向识别：多枚举空间探测（本机构建 23/24、主流 1101/1102、柜台 48/50）。"""

    def test_local_build_enum(self):
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=23)), "买入")
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=24)), "卖出")

    def test_legacy_doc_enum(self):
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=1101)), "买入")
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=1102)), "卖出")

    def test_counter_offset_enum(self):
        """柜台回推口径（实盘成交实证）：offset_type 48=买 / 50=卖。"""
        self.assertEqual(ReportHandler._side_of(xt_obj(offset_type=48)), "买入")
        self.assertEqual(ReportHandler._side_of(xt_obj(offset_type=50)), "卖出")

    def test_order_type_wins_over_offset(self):
        """order_type 优先于 offset_type。"""
        self.assertEqual(
            ReportHandler._side_of(xt_obj(order_type=23, offset_type=50)), "买入")

    def test_unrecognized_returns_empty(self):
        """无法识别返回空串（绝不默认走卖——旧事故形态）。"""
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=999, offset_type=999)), "")
        self.assertEqual(ReportHandler._side_of(xt_obj()), "")
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type="abc")), "")

    def test_string_numeric_tolerated(self):
        """柜台偶发字符串数字（"48"）可容忍。"""
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type="23")), "买入")


class TestSignalOf(unittest.TestCase):
    """§FIX 2026-08-31 signal_id 归因：strategy_name → order_remark → remark 首个非空。"""

    def test_prefers_strategy_name(self):
        self.assertEqual(
            ReportHandler._signal_of(xt_obj(strategy_name="S1", order_remark="S2")), "S1")

    def test_falls_back_to_remark(self):
        self.assertEqual(ReportHandler._signal_of(xt_obj(order_remark="buy:600000:dragon:d")), "buy:600000:dragon:d")
        self.assertEqual(ReportHandler._signal_of(xt_obj(remark="S3")), "S3")

    def test_empty_when_absent(self):
        self.assertEqual(ReportHandler._signal_of(xt_obj()), "")
        self.assertEqual(ReportHandler._signal_of(xt_obj(strategy_name="  ")), "")


class TestStatus(unittest.TestCase):
    """xtquant 委托状态码映射（handler.py:289-295）。"""

    def test_known_codes(self):
        cases = {48: "未报", 49: "待报", 50: "已报", 51: "已报待撤", 52: "部成待撤",
                 53: "部撤", 54: "已撤", 55: "部成", 56: "已成", 57: "废单", 255: "未知"}
        for code, want in cases.items():
            self.assertEqual(ReportHandler._status(code), want, "code=%s" % code)

    def test_string_code_and_unknown(self):
        self.assertEqual(ReportHandler._status("56"), "已成")
        self.assertEqual(ReportHandler._status(None), "已报")   # 空/未知保守回 已报
        self.assertEqual(ReportHandler._status(0), "已报")


if __name__ == "__main__":
    unittest.main()
