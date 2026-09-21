#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§M5（2026-09-22 修复批）持仓回报两通道字段集一致性锁。

背景（docs/FIX_PLAN_20260922.md §3 M5）：持仓对账存在两条上报通道——
  ① xt 直连：broker.XtBroker.query_positions（xtquant query_stock_positions 映射）；
  ② 策略桥：qmt_bridge_strategy 的 query_positions / embed_positions（文件桥内嵌与回调两形态）。
旧实现 xt 直连通道映射缺 can_use_qty（T+1 可卖量）与 open_price——同一账户走不同通道
回报字段集不同，可卖量在直连通道丢失。本测用 AST 静态提取三处映射的字典字面量键集，
锁三者 diff 为空；另用假 trader 验证 XtBroker 映射行为（can_use_qty 真实取值）。
English: §M5 — AST-locks the position-dict field sets of the xt-direct channel and the
strategy bridge channel to be identical, and behavior-tests can_use_qty mapping.
"""
import ast
import logging
import os
import sys
import types
import unittest

BASE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, BASE)
logging.disable(logging.CRITICAL)

from broker import XtBroker  # noqa: E402


def _func_node(tree, name, class_name=None):
    """按（可选）类名+函数名定位 AST FunctionDef。"""
    if class_name is None:
        candidates = [n for n in ast.walk(tree) if isinstance(n, ast.FunctionDef) and n.name == name]
    else:
        candidates = [
            m for cls in [n for n in ast.walk(tree) if isinstance(n, ast.ClassDef) and n.name == class_name]
            for m in cls.body if isinstance(m, ast.FunctionDef) and m.name == name
        ]
    assert candidates, "未找到函数 %s%s" % (class_name or "", name)
    return candidates[0]


def _dict_keys_of_append(func):
    """提取函数体内 out.append({...}) 字面量的全部键（按源码序）。"""
    for node in ast.walk(func):
        if (isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute)
                and node.func.attr == "append" and node.args
                and isinstance(node.args[0], ast.Dict)):
            return [k.value for k in node.args[0].keys]
    raise AssertionError("函数内未找到 dict 字面量 append")


def _parse(path):
    with open(path, encoding="utf-8") as f:
        return ast.parse(f.read(), filename=path)


class TestChannelFieldSetParity(unittest.TestCase):
    """三处持仓映射（xt 直连 / 策略桥 query / 策略桥 embed）字段集必须完全一致。"""

    @classmethod
    def setUpClass(cls):
        broker_tree = _parse(os.path.join(BASE, "broker.py"))
        bridge_tree = _parse(os.path.join(BASE, "qmt_bridge_strategy.py"))
        cls.xt_keys = set(_dict_keys_of_append(_func_node(broker_tree, "query_positions", "XtBroker")))
        cls.bridge_q_keys = set(_dict_keys_of_append(_func_node(bridge_tree, "query_positions")))
        cls.bridge_e_keys = set(_dict_keys_of_append(_func_node(bridge_tree, "embed_positions")))

    def test_xt_vs_bridge_query_diff_empty(self):
        diff = self.xt_keys.symmetric_difference(self.bridge_q_keys)
        self.assertFalse(diff, "xt 直连与策略桥 query_positions 字段集出现漂移: %s" % sorted(diff))

    def test_xt_vs_bridge_embed_diff_empty(self):
        diff = self.xt_keys.symmetric_difference(self.bridge_e_keys)
        self.assertFalse(diff, "xt 直连与策略桥 embed_positions 字段集出现漂移: %s" % sorted(diff))

    def test_can_use_qty_present_everywhere(self):
        for name, keys in (("xt", self.xt_keys), ("bridge_q", self.bridge_q_keys), ("bridge_e", self.bridge_e_keys)):
            self.assertIn("can_use_qty", keys, "%s 通道缺 can_use_qty（T+1 可卖量丢失，§M5）" % name)


class TestXtBrokerCanUseQtyMapping(unittest.TestCase):
    """行为面：XtBroker.query_positions 必须把 can_use_volume 映射为 can_use_qty。"""

    def _snapshot(self, **attrs):
        b = XtBroker.__new__(XtBroker)  # 绕开真实 connect，仅测映射段
        b._connected = True
        b._acc = "A1"
        pos = types.SimpleNamespace(**attrs)
        b._trader = types.SimpleNamespace(query_stock_positions=lambda acc: [pos])
        return b.query_positions()[0]

    def test_can_use_qty_mapped(self):
        d = self._snapshot(stock_code="600000.SH", stock_name="浦发银行", volume=500,
                           can_use_volume=300, open_price=10.5, market_value=5300.0)
        self.assertEqual(d["qty"], 500)
        self.assertEqual(d["can_use_qty"], 300, "T+1 可卖量必须在直连通道可见")
        self.assertEqual(d["open_price"], 10.5)
        self.assertEqual(d["cost_price"], 10.5)

    def test_missing_attr_defaults_zero(self):
        d = self._snapshot(stock_code="000001.SZ", stock_name="平安银行", volume=100, open_price=12.0)
        self.assertEqual(d["can_use_qty"], 0)


if __name__ == "__main__":
    unittest.main()
