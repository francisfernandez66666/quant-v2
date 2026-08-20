#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway 单测（unittest，零第三方依赖）。

覆盖：store 加权成本/最高价单调/清仓删行、全量对账、signal_id 幂等、handler 回报推送、
HTTP 端到端（/order → mock 成交 → handler 推送首尔 → /state 校验）。无需 Windows/xtquant。
"""
import json
import os
import sys
import tempfile
import threading
import time
import unittest
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from store import Store  # noqa: E402
from ids import Idempotency  # noqa: E402
from broker import MockBroker, XtBroker, build_broker  # noqa: E402
from handler import ReportHandler, post_report  # noqa: E402
from gateway import Gateway, _Handler  # noqa: E402
from http.server import ThreadingHTTPServer  # noqa: E402


def new_store():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return Store(path)


class TestStore(unittest.TestCase):
    def test_apply_fill_weighted_cost_and_highest(self):
        s = new_store()
        # 建仓 100@10
        s.apply_fill({"code": "600519.SH", "side": "买入", "price": 10, "qty": 100,
                      "amount": 1000, "traded_at": "t1", "signal_id": "S1"})
        p = s.list_positions()[0]
        self.assertEqual(p["qty"], 100)
        self.assertAlmostEqual(p["cost_price"], 10)
        self.assertAlmostEqual(p["highest_price"], 10)
        # 加仓 100@12 → 加权成本 11，最高价 12
        s.apply_fill({"code": "600519.SH", "side": "买入", "price": 12, "qty": 100,
                      "amount": 1200, "traded_at": "t2", "signal_id": "S2"})
        p = s.list_positions()[0]
        self.assertEqual(p["qty"], 200)
        self.assertAlmostEqual(p["cost_price"], 11)
        self.assertAlmostEqual(p["highest_price"], 12)
        # 减仓 50@13 → 剩 150，成本不变
        s.apply_fill({"code": "600519.SH", "side": "卖出", "price": 13, "qty": 50,
                      "amount": 650, "traded_at": "t3", "signal_id": "S3"})
        p = s.list_positions()[0]
        self.assertEqual(p["qty"], 150)
        self.assertAlmostEqual(p["cost_price"], 11)
        # 清仓 150 → 删行
        s.apply_fill({"code": "600519.SH", "side": "卖出", "price": 13, "qty": 150,
                      "amount": 1950, "traded_at": "t4", "signal_id": "S4"})
        self.assertEqual(s.list_positions(), [])

    def test_reconcile_removes_absent(self):
        s = new_store()
        s.upsert_position({"ts_code": "000001.SZ", "qty": 100, "cost_price": 10})
        s.upsert_position({"ts_code": "600519.SH", "qty": 200, "cost_price": 1500})
        n = s.reconcile_positions([
            {"ts_code": "600519.SH", "qty": 200, "cost_price": 1500, "amount": 300000},
        ])
        self.assertEqual(n, 1)
        codes = [p["ts_code"] for p in s.list_positions()]
        self.assertEqual(codes, ["600519.SH"])

    def test_orders_unique_signal(self):
        s = new_store()
        s.upsert_order({"order_id": "A", "signal_id": "S1", "code": "600519.SH", "side": "买入",
                        "status": "已报", "price": 10, "qty": 100, "created_at": "t"})
        # 同 signal_id 再插 → is_new=False（幂等）
        is_new = s.upsert_order({"order_id": "B", "signal_id": "S1", "code": "600519.SH",
                                 "side": "买入", "status": "已成", "price": 10, "qty": 100,
                                 "created_at": "t2"})
        self.assertFalse(is_new)
        self.assertEqual(s.order_by_signal("S1")["order_id"], "B")


class TestIdempotency(unittest.TestCase):
    def test_check_and_record(self):
        s = new_store()
        ids = Idempotency(s)
        self.assertTrue(ids.check("S1")[0])
        ids.record({"order_id": "A", "signal_id": "S1", "code": "600519.SH"})
        is_new, existing = ids.check("S1")
        self.assertFalse(is_new)
        self.assertEqual(existing["order_id"], "A")


class TestHandler(unittest.TestCase):
    def test_trade_push_and_disconnect(self):
        s = new_store()
        pushed = []
        captured = {}

        def fake_post(url, token, payload):
            pushed.append(payload)
            return True

        old = post_report
        import handler as handler_mod
        handler_mod.post_report = fake_post
        try:
            h = ReportHandler(s, "http://seoul:8080", "tok")
            h._push = lambda p: pushed.append(p)  # 直接捕获
            h.on_trade({"order_id": "O1", "code": "600519.SH", "side": "买入", "price": 10,
                        "qty": 100, "amount": 1000, "traded_at": "t", "signal_id": "S1"})
            h.on_disconnected()
        finally:
            handler_mod.post_report = old
        types = [p["type"] for p in pushed]
        self.assertEqual(types, ["trade", "disconnect"])
        # 落库校验
        self.assertEqual(s.list_positions()[0]["qty"], 100)
        self.assertTrue(h.disconnected)


class TestMockBroker(unittest.TestCase):
    def test_place_order_fill_updates_position(self):
        b = MockBroker(seed=[{"ts_code": "600519.SH", "name": "贵州茅台", "qty": 100,
                              "cost_price": 1500, "highest_price": 1500}])
        b.connect()
        ok, order_id, err = b.place_order({"signal_id": "S1", "code": "600519.SH",
                                           "name": "贵州茅台", "side": "买入", "price": 1510,
                                           "qty": 100, "amount": 151000})
        self.assertTrue(ok)
        time.sleep(1.2)
        p = b.query_positions()[0]
        self.assertEqual(p["qty"], 200)
        self.assertAlmostEqual(p["cost_price"], 1505)
        self.assertAlmostEqual(p["highest_price"], 1510)


class TestXtBrokerLazyImport(unittest.TestCase):
    def test_importable_without_xtquant(self):
        # 无 xtquant 的环境应能实例化；connect 时才报错（延迟 import）
        b = XtBroker("A1")
        self.assertFalse(b.is_connected())
        try:
            b.connect()
            self.fail("expected RuntimeError without xtquant")
        except RuntimeError as e:
            self.assertIn("xtquant", str(e))

    def test_build_broker(self):
        self.assertIsInstance(build_broker({"broker": "mock"}), MockBroker)
        self.assertIsInstance(build_broker({}), MockBroker)
        self.assertIsInstance(build_broker({"broker": "xt"}), XtBroker)


class TestGatewayHTTP(unittest.TestCase):
    def setUp(self):
        fd, dbpath = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        os.unlink(dbpath)
        cfg = {
            "listen": "127.0.0.1:0",
            "token": "tk",
            "broker": "mock",
            "account": "MOCK0001",
            "db": dbpath,
            "report_url": "",
            "reconcile_sec": 0,
            "seed": [],
        }
        self.gw = Gateway(cfg)
        _Handler.gateway = self.gw
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        self.port = self.server.server_address[1]
        self.gw.start()
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.gw._stop.set()
        self.server.shutdown()
        self.server.server_close()

    def _req(self, method, path, body=None, token="tk"):
        url = "http://127.0.0.1:%d%s" % (self.port, path)
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", "Bearer " + token)
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                return resp.status, json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read().decode("utf-8"))

    def test_health_no_auth(self):
        status, body = self._req("GET", "/health", token="")
        self.assertEqual(status, 200)
        self.assertTrue(body["ok"])

    def test_unauthorized(self):
        status, body = self._req("GET", "/state", token="wrong")
        self.assertEqual(status, 401)

    def test_order_state_and_idempotent(self):
        status, body = self._req("POST", "/order", {
            "signal_id": "S1", "code": "600519.SH", "name": "贵州茅台", "side": "买入",
            "price_type": "market", "price": 1510, "qty": 100, "amount": 151000,
            "created_at": "t",
        })
        self.assertEqual(status, 200)
        self.assertTrue(body["ok"])
        order_id = body["order_id"]

        # 幂等：同 signal_id 重发 → 返回原 order_id，不重复下单
        status, body2 = self._req("POST", "/order", {
            "signal_id": "S1", "code": "600519.SH", "side": "买入", "price": 1510,
            "qty": 100, "amount": 151000, "created_at": "t",
        })
        self.assertEqual(status, 200)
        self.assertEqual(body2["order_id"], order_id)

        # 非整手拒绝
        status, body3 = self._req("POST", "/order", {
            "signal_id": "S2", "code": "600519.SH", "side": "买入", "price": 10,
            "qty": 50, "amount": 500, "created_at": "t",
        })
        self.assertEqual(status, 400)

        # 等 mock 成交后状态含持仓
        time.sleep(1.2)
        status, state = self._req("GET", "/state")
        self.assertTrue(state["connected"])
        self.assertEqual(len(state["orders"]), 1)
        self.assertEqual(state["orders"][0]["status"], "已成")
        self.assertEqual(state["positions"][0]["ts_code"], "600519.SH")
        self.assertEqual(state["positions"][0]["qty"], 100)


if __name__ == "__main__":
    unittest.main()