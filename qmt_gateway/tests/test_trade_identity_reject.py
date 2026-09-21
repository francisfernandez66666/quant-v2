# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_trade_identity_reject.py — §REJECT 反例锁（2026-09-22 修复批，
LOW「trade_id 空且 order_id 空的回报拒收」）。

缺陷本体：两把身份锚皆空的成交回报旧样落库——
  · store._fill_is_duplicate 无 trade_id 且无 order_id 时恒判"非重放"→ 通道抖动一次
    多记一笔，持仓/盈亏全线污染且无从对账归因；
  · /settlement 装配出的行 order_id/serial 双空，Go 侧物理事实键退化成
    "f:@@side"（同向互相覆盖，见 §P0-1b 史）。
修法双防线：gateway._apply_trade 拒收回 400（HTTP/文件桥回报路径，与 §F5 Go 侧
order 回报缺 order_id 拒 400 同口径）；handler.on_trade 入库入口拒收返回 False
（mock/xt 直调路径）。本文件锁四态：皆空拒、只带 trade_id 收、只带 order_id 收、
seq→派发行可回填委托号者收（不误伤）。report_fields.json 契约字段集不变。
（English: §REJECT regression lock — fills with neither trade_id nor order_id are
refused at both the gateway report endpoint (400) and the handler ingest entry.）
"""
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from store import Store  # noqa: E402
from handler import ReportHandler  # noqa: E402
from gateway import Gateway  # noqa: E402

_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def _new_store():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return Store(path)


def _new_gw():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    cfg = {"listen": "127.0.0.1:0", "token": "t", "broker": "mock", "account": "M",
           "db": path, "report_url": "", "report_token": "", "user_id": ""}
    return Gateway(cfg)


def _fills_count(store):
    return store._conn.execute("SELECT COUNT(*) c FROM fills").fetchone()["c"]


class TestHandlerReject(unittest.TestCase):
    """第一道防线：handler.on_trade 入库入口拒收。"""

    def setUp(self):
        self.s = _new_store()
        self.h = ReportHandler(self.s, "http://seoul.invalid", "tok", user_id="u1")
        self.pushed = []
        self.h._push = self.pushed.append  # 拦截 outbox 入队，聚焦落库/拒收判定

    def _ev(self, **over):
        ev = {"code": "600519.SH", "side": "买入", "price": 10.0, "qty": 100,
              "amount": 1000.0, "traded_at": "2026-09-22T09:30:00+08:00",
              "signal_id": "S1", "trade_id": "", "order_id": ""}
        ev.update(over)
        return ev

    def test_both_empty_rejected(self):
        # 反例锁（旧缺陷复现点）：两锚皆空 → 拒收，不落 fills、不入 outbox。
        self.assertFalse(self.h.on_trade(self._ev()))
        self.assertEqual(_fills_count(self.s), 0)
        self.assertEqual(self.pushed, [])
        self.assertEqual(self.s.list_positions(), [])  # 持仓未被无身份成交污染

    def test_trade_id_only_accepted(self):
        self.assertTrue(self.h.on_trade(self._ev(trade_id="T-77")))
        self.assertEqual(_fills_count(self.s), 1)
        self.assertEqual(self.pushed[0]["trade_id"], "T-77")

    def test_order_id_only_accepted(self):
        self.assertTrue(self.h.on_trade(self._ev(order_id="O-77")))
        self.assertEqual(_fills_count(self.s), 1)

    def test_whitespace_only_counts_as_empty(self):
        # "  " 视同空串：不允许用空白字符绕过身份锚校验
        self.assertFalse(self.h.on_trade(self._ev(trade_id="  ", order_id=None)))
        self.assertEqual(_fills_count(self.s), 0)


class TestGatewayApplyTradeReject(unittest.TestCase):
    """第二道防线：/dispatch/result 桥回报路径 400 拒收。"""

    def test_both_empty_400(self):
        gw = _new_gw()
        code, resp = gw._do_dispatch_result({
            "type": "trade", "code": "600519.SH", "side": "买入",
            "price": 10.0, "qty": 100, "amount": 1000.0,
        })
        self.assertEqual(code, 400)
        self.assertFalse(resp["ok"])
        self.assertIn("trade_id", resp["err"])
        self.assertEqual(_fills_count(gw.store), 0)

    def test_order_id_present_200(self):
        gw = _new_gw()
        code, resp = gw._do_dispatch_result({
            "type": "trade", "order_id": "EXC-9", "code": "600519.SH", "side": "买入",
            "price": 10.0, "qty": 100, "amount": 1000.0,
        })
        self.assertEqual(code, 200, resp)
        self.assertEqual(_fills_count(gw.store), 1)

    def test_seq_backfills_dispatch_row_order_id(self):
        """不误伤：回报只带 seq，但派发行已回填交易所委托号 → 身份锚存在 → 受理。"""
        gw = _new_gw()
        seq = gw.store.dispatch_enqueue_order(
            {"signal_id": "S-BF", "code": "600519.SH", "side": "买入",
             "price_type": "limit", "price": 10.0, "qty": 100, "strategy": "t"})
        # 模拟下单结算：派发行拿到真实交易所委托号
        gw.store.dispatch_set_result(seq, {"ok": True, "order_id": "EXC-BF", "err": ""})
        code, resp = gw._do_dispatch_result({
            "type": "trade", "seq": seq, "code": "600519.SH", "side": "买入",
            "price": 10.0, "qty": 100, "amount": 1000.0,  # 无 order_id/trade_id
        })
        self.assertEqual(code, 200, resp)
        self.assertEqual(_fills_count(gw.store), 1)
        row = gw.store._conn.execute("SELECT order_id FROM fills").fetchone()
        self.assertEqual(row["order_id"], "EXC-BF")


class TestReportFieldsContractIntact(unittest.TestCase):
    """§REJECT 只收紧"两键皆空"非法组合，report_fields.json 契约字段集不得变动
    （F2 golden 三方锁的网关侧一极）。"""

    def test_contract_sets_unchanged(self):
        with open(os.path.join(_ROOT, "qmt_gateway", "contract", "report_fields.json"),
                  encoding="utf-8") as f:
            c = json.load(f)
        self.assertEqual(
            sorted(c["report_event_fields"]),
            sorted(["amount", "asset", "at", "broker", "code", "fee", "from", "order_id",
                    "positions", "price", "qty", "reason", "side", "signal_id", "stamp_tax",
                    "status", "traded_at", "type", "user_id"]))
        self.assertIn("order_id", c["consumed_by_event"]["trade"])  # 身份锚语义保持


if __name__ == "__main__":
    unittest.main()
