#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§A1/A2（AUDIT_FULLSTACK_20260918）网关侧独立风控 + 下单契约三点校验。

背景：Go OrderRequest 的 strategy_type/amount 等字段曾被网关静默忽略，全部资金/策略防线
集中在首尔单机——网关 token 泄露即无闸裸下单。本文件锁两件事：
  A1 行为面：max_order_amount 金额帽（买卖双向、amount 缺省回退 qty×price）、
     allowed_strategies 白名单（仅买入方向）、strict_fields 缺键 fail-close；
     且闸口拒单发生在 claim 之前，不消耗 signal_id 幂等占位。
  A2 契约面：contract/order_fields.json golden == gateway.CONTRACT_CONSUMED/IGNORED
     == _do_order/broker 源码实际字段引用（consumed 必须被引用、ignored 必须不被引用），
     配合 Go 侧 internal/trading/order_contract_test.go 形成三点闭环。
"""
import inspect
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from gateway import Gateway, CONTRACT_CONSUMED_FIELDS, CONTRACT_IGNORED_FIELDS  # noqa: E402
from broker import MockBroker, XtBroker  # noqa: E402

CONTRACT_PATH = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                             "contract", "order_fields.json")


def make_gw(**gate_cfg):
    """构造一台纯内存 mock 通道网关，闸口配置由 kwargs 注入（不启动 HTTP/回报线程）。"""
    fd, dbpath = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(dbpath)
    cfg = {"listen": "127.0.0.1:0", "token": "tk", "broker": "mock", "account": "M",
           "db": dbpath, "report_url": "", "reconcile_sec": 0, "seed": []}
    cfg.update(gate_cfg)
    return Gateway(cfg)


class TestGatewayAmountCap(unittest.TestCase):
    """§A1 金额帽：与首尔 risk.Gate.checkMaxOrderAmount 同语义（0=关，双向拒，回退 qty×price）。"""

    def test_cap_rejects_buy_by_amount_field(self):
        """amount 字段超帽 → 400 拒单，err 含 cap。"""
        gw = make_gw(max_order_amount=50000)
        try:
            status, body = gw._do_order({
                "signal_id": "CAP1", "code": "600519.SH", "side": "买入", "price_type": "limit",
                "price": 1500, "qty": 100, "amount": 150000, "created_at": "t",
            })
            self.assertEqual(status, 400)
            self.assertIn("cap", body["err"])
        finally:
            gw.stop()

    def test_cap_falls_back_to_qty_times_price(self):
        """amount 缺失/为 0 → 回退 qty×参考价判定（防拆字段绕过）。"""
        gw = make_gw(max_order_amount=50000)
        try:
            status, body = gw._do_order({
                "signal_id": "CAP2", "code": "600519.SH", "side": "买入", "price_type": "limit",
                "price": 1500, "qty": 100, "amount": 0, "created_at": "t",
            })
            self.assertEqual(status, 400)
            self.assertIn("150000", body["err"])
        finally:
            gw.stop()

    def test_cap_applies_to_sell_side_too(self):
        """卖出同样受帽（首尔同款双向语义）：先 seed 底仓再卖超帽金额。"""
        gw = make_gw(max_order_amount=50000, seed=[
            {"ts_code": "600519.SH", "name": "贵州茅台", "qty": 200,
             "cost_price": 1000, "highest_price": 1500}])
        try:
            status, body = gw._do_order({
                "signal_id": "CAP3", "code": "600519.SH", "side": "卖出", "price_type": "limit",
                "price": 1500, "qty": 100, "amount": 150000, "created_at": "t",
            })
            self.assertEqual(status, 400)
        finally:
            gw.stop()

    def test_cap_off_by_default(self):
        """默认 0=关闭：不设帽时大额单正常受理（存量行为零变化）。"""
        gw = make_gw()
        try:
            status, body = gw._do_order({
                "signal_id": "CAP4", "code": "600519.SH", "side": "买入", "price_type": "limit",
                "price": 1500, "qty": 100, "amount": 150000, "created_at": "t",
            })
            self.assertEqual(status, 200)
            self.assertTrue(body["ok"])
        finally:
            gw.stop()

    def test_gate_reject_does_not_consume_idempotency_slot(self):
        """闸口拒单发生在 claim 之前：同 signal_id 修正金额后重试必须真正受理。"""
        gw = make_gw(max_order_amount=50000)
        try:
            s1, _ = gw._do_order({
                "signal_id": "CAP5", "code": "600519.SH", "side": "买入", "price_type": "limit",
                "price": 1500, "qty": 100, "amount": 150000, "created_at": "t",
            })
            self.assertEqual(s1, 400)
            s2, b2 = gw._do_order({
                "signal_id": "CAP5", "code": "600519.SH", "side": "买入", "price_type": "limit",
                "price": 450, "qty": 100, "amount": 45000, "created_at": "t",
            })
            self.assertEqual(s2, 200)
            self.assertTrue(b2["ok"])
        finally:
            gw.stop()


class TestGatewayStrategyWhitelist(unittest.TestCase):
    """§A1 战法白名单：空=关闭；非空仅约束买入方向；strict_fields 决定缺键姿态。"""

    BUY = {"code": "600519.SH", "side": "买入", "price_type": "limit",
           "price": 10, "qty": 100, "amount": 1000, "created_at": "t"}

    def _order(self, gw, sid, **over):
        body = dict(self.BUY)
        body["signal_id"] = sid
        body.update(over)
        return gw._do_order(body)

    def test_unknown_strategy_rejected(self):
        """白名单非空时未登记的 strategy_type（momentum）买入 → 400，err 点名 whitelist。"""
        gw = make_gw(allowed_strategies=["dragon", "double_bump"])
        try:
            status, body = self._order(gw, "WL1", strategy_type="momentum")
            self.assertEqual(status, 400)
            self.assertIn("whitelist", body["err"])
        finally:
            gw.stop()

    def test_allowed_strategy_passes(self):
        """白名单内的 strategy_type（dragon）买入 → 200，闸门只拦未登记战法不误伤。"""
        gw = make_gw(allowed_strategies=["dragon", "double_bump"])
        try:
            status, body = self._order(gw, "WL2", strategy_type="dragon")
            self.assertEqual(status, 200)
            self.assertTrue(body["ok"])
        finally:
            gw.stop()

    def test_missing_key_fail_open_by_default(self):
        """缺 strategy_type 默认放行（旧客户端兼容窗口，与审计文档口径一致）。"""
        gw = make_gw(allowed_strategies=["dragon"])
        try:
            status, _ = self._order(gw, "WL3")
            self.assertEqual(status, 200)
        finally:
            gw.stop()

    def test_missing_key_fail_close_with_strict(self):
        """strict_fields=True 时缺 strategy_type 一律拒（400，错误里点名 strict_fields）。"""
        gw = make_gw(allowed_strategies=["dragon"], strict_fields=True)
        try:
            status, body = self._order(gw, "WL4")
            self.assertEqual(status, 400)
            self.assertIn("strict_fields", body["err"])
        finally:
            gw.stop()

    def test_empty_list_disables_gate_and_sell_bypasses(self):
        """空白名单=闸关；卖出方向不受白名单约束（signalctl 直通同款语义）。"""
        gw = make_gw(seed=[{"ts_code": "600519.SH", "name": "贵州茅台", "qty": 200,
                            "cost_price": 10, "highest_price": 10}])
        try:
            s1, _ = self._order(gw, "WL5", strategy_type="whatever")
            self.assertEqual(s1, 200)
            s2, _ = gw._do_order({"signal_id": "WL6", "code": "600519.SH", "side": "卖出",
                                  "price_type": "limit", "price": 11, "qty": 100,
                                  "amount": 1100, "created_at": "t"})
            self.assertEqual(s2, 200)
        finally:
            gw.stop()


class TestOrderContractGolden(unittest.TestCase):
    """§A2 golden 三点校验：字段集闭合 + consumed/ignored 声明与源码真实引用一致。"""

    @classmethod
    def setUpClass(cls):
        with open(CONTRACT_PATH, "r", encoding="utf-8") as f:
            cls.doc = json.load(f)

    def test_golden_sets_are_closed(self):
        """golden.fields == consumed ∪ ignored 且两集不相交。"""
        fields = set(self.doc["fields"])
        consumed = set(self.doc["consumed_by_gateway"])
        ignored = set(self.doc["ignored_by_gateway"])
        self.assertEqual(fields, consumed | ignored)
        self.assertEqual(consumed & ignored, set())

    def test_gateway_declared_sets_match_golden(self):
        """gateway.py 声明集与 golden 逐字一致（新增 Go 字段必须同步进 golden+声明集）。"""
        self.assertEqual(CONTRACT_CONSUMED_FIELDS, set(self.doc["consumed_by_gateway"]))
        self.assertEqual(CONTRACT_IGNORED_FIELDS, set(self.doc["ignored_by_gateway"]))

    def test_consumed_fields_actually_referenced(self):
        """consumed 字段必须被 _do_order 或 broker.place_order 源码真实读取（防口头消费）。"""
        src = inspect.getsource(Gateway._do_order)
        for b in (MockBroker.place_order, XtBroker.place_order):
            try:
                src += inspect.getsource(b)
            except (TypeError, OSError):
                pass
        for f in sorted(CONTRACT_CONSUMED_FIELDS):
            if ('"%s"' % f) not in src:
                self.fail("consumed 字段 %s 未在 _do_order/broker 源码中引用" % f)

    def test_ignored_fields_not_silently_consumed(self):
        """ignored 字段不得出现在 _do_order 源码（出现即分类漂移，须改声明并重生成 golden）。"""
        src = inspect.getsource(Gateway._do_order)
        for f in sorted(CONTRACT_IGNORED_FIELDS):
            if ('req.get("%s"' % f) in src:
                self.fail("字段 %s 已在 _do_order 消费，应移入 CONTRACT_CONSUMED_FIELDS" % f)


if __name__ == "__main__":
    unittest.main()
