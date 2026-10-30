#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§ORDER-ID-REUSE / §ORDER-STATE-PULL（2026-10-30）回归锁。

背景（当天现网）：QMT 交易所委托号跨日复用，撞上网关 orders 表的历史行主键 →
upsert_order 抛 UNIQUE → handler.on_order 把整条终态回报丢弃 → Go 永久停在「撤单中」
并全额占用战略额度；另一笔则是重发时 /order 对终态 signal_id 回 200 + 死单号（假成功）。
本文件钉住四件事：
  ① 撞键不许丢状态（保留占位号，状态照落）；
  ② 落库失败仍要 _push（状态是事实，本地副本失败不能阻断上报）；
  ③ /order 对终态 signal_id 必须 409 + code=terminal；
  ④ GET /orders 只回当日、格式非法即 400（供决策端拉取对账）。
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from tests.test_manual_sync import _GatewayCase, new_db_path  # noqa: E402,F401

SID = "buy:300502:fac_150:20261030"


def _row(**kw):
    row = {"order_id": "seq:9", "signal_id": SID, "code": "300502.SZ", "side": "买入",
           "status": "已报", "price": 195.01, "qty": 100,
           "created_at": "2026-10-30 09:31:53", "user_id": ""}
    row.update(kw)
    return row


class TestOrderIdReuse(_GatewayCase):
    def test_update_collision_keeps_status(self):
        # 历史行占用真实号 xt1（另一天的另一笔委托）
        self.store.upsert_order({"order_id": "xt1", "signal_id": "buy:301226:fac_1:20260922",
                                 "code": "301226.SZ", "side": "买入", "status": "已报",
                                 "price": 34.29, "qty": 500,
                                 "created_at": "2026-09-22 13:54:58", "user_id": ""})
        self.store.upsert_order(_row())
        # 今日终态回报带着同一个真实号到达：不许抛异常、不许丢掉「废单」
        self.store.upsert_order(_row(order_id="xt1", status="废单"))
        row = self.store.order_by_signal(SID)
        self.assertEqual(row["status"], "废单", "撞键时状态必须照落")
        self.assertEqual(row["order_id"], "seq:9", "撞键时保留原占位号，不覆盖历史行")
        old = self.store.order_by_signal("buy:301226:fac_1:20260922")
        self.assertEqual(old["order_id"], "xt1", "历史行不得被改写")

    def test_insert_collision_with_real_id(self):
        self.store.upsert_order({"order_id": "xt2", "signal_id": "old-signal",
                                 "code": "301226.SZ", "side": "买入", "status": "已撤",
                                 "price": 1, "qty": 100, "created_at": "2026-09-22 10:00:00",
                                 "user_id": ""})
        # 全新信号、首个事件就带真实号（跨日复用）：降级为占位号落库，不抛
        self.store.upsert_order(_row(order_id="xt2", signal_id="buy:000333:fac_117:20261030",
                                     code="000333.SZ", status="已报"))
        row = self.store.order_by_signal("buy:000333:fac_117:20261030")
        self.assertIsNotNone(row)
        self.assertEqual(row["status"], "已报")
        self.assertEqual(row["order_id"], "pending:buy:000333:fac_117:20261030")

    def test_purge_stale_orders(self):
        self.store.upsert_order({"order_id": "old-t", "signal_id": "s-old-t", "code": "600000.SH",
                                 "side": "买入", "status": "已撤", "price": 1, "qty": 100,
                                 "created_at": "2026-09-01 10:00:00", "user_id": ""})
        self.store.upsert_order({"order_id": "old-o", "signal_id": "s-old-o", "code": "600001.SH",
                                 "side": "买入", "status": "已报", "price": 1, "qty": 100,
                                 "created_at": "2026-09-01 10:00:00", "user_id": ""})
        self.store.upsert_order(_row())
        n = self.store.purge_stale_orders("2026-10-30")
        self.assertEqual(n, 2, "终态>3天与在途>10天的跨日行都应被清理")
        self.assertIsNotNone(self.store.order_by_signal(SID), "当日行不得被清")


class TestHandlerPushOnStoreFailure(_GatewayCase):
    def test_push_happens_even_if_store_raises(self):
        pushed = []
        orig_upsert, orig_push = self.store.upsert_order, self.gw.handler._push

        def boom(_order):
            raise RuntimeError("simulated UNIQUE failure")

        self.store.upsert_order = boom
        self.gw.handler._push = lambda payload: pushed.append(payload)
        try:
            self.gw.handler.on_order(_row(status="废单"))
        finally:
            self.store.upsert_order = orig_upsert
            self.gw.handler._push = orig_push
        self.assertEqual(len(pushed), 1, "落库失败时事件仍必须进入上报队列")
        self.assertEqual(pushed[0]["status"], "废单")


class TestOrderTerminalIdempotency(_GatewayCase):
    def test_terminal_signal_returns_409_not_ok(self):
        self.store.upsert_order(_row(status="已撤", order_id="349"))
        status, payload = self.gw._do_order({
            "code": "300502.SZ", "side": "买入", "price": 195.01, "qty": 100,
            "signal_id": SID,
        })
        self.assertEqual(status, 409, "终态 signal_id 重发必须被拒，不能假成功")
        self.assertEqual(payload.get("code"), "terminal")
        self.assertEqual(payload.get("status"), "已撤")

    def test_in_flight_duplicate_still_returns_200(self):
        self.store.upsert_order(_row(status="已报", order_id="3632"))
        status, payload = self.gw._do_order({
            "code": "300502.SZ", "side": "买入", "price": 195.01, "qty": 100,
            "signal_id": SID,
        })
        self.assertEqual(status, 200)
        self.assertTrue(payload.get("ok"))
        self.assertEqual(payload.get("order_id"), "3632")


class TestOrdersEndpoint(_GatewayCase):
    def _req(self, path):
        class _R:
            pass
        r = _R()
        r.path = path
        return r

    def test_orders_filters_by_day(self):
        self.store.upsert_order(_row())
        self.store.upsert_order({"order_id": "y1", "signal_id": "s-y", "code": "600000.SH",
                                 "side": "买入", "status": "已撤", "price": 1, "qty": 100,
                                 "created_at": "2026-10-29 10:00:00", "user_id": ""})
        status, payload = self.gw._do_orders(self._req("/orders?day=2026-10-30"))
        self.assertEqual(status, 200)
        self.assertEqual(payload["n"], 1)
        self.assertEqual(payload["orders"][0]["signal_id"], SID)

    def test_orders_rejects_bad_day(self):
        status, _ = self.gw._do_orders(self._req("/orders?day=20261030"))
        self.assertEqual(status, 400)


if __name__ == "__main__":
    unittest.main()
