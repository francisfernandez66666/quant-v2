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
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from store import Store  # noqa: E402
from ids import Idempotency  # noqa: E402
from broker import MockBroker, XtBroker, build_broker  # noqa: E402
from handler import ReportHandler, post_report  # noqa: E402
from gateway import Gateway, _Handler  # noqa: E402
from http.server import ThreadingHTTPServer  # noqa: E402

CN_TZ = timezone(timedelta(hours=8))


def new_store():
    """创建临时 SQLite 文件并立即删除，得到“存在路径但空库”的 Store（测试隔离用）"""
    # 创建临时 SQLite 文件并立即删除，得到“存在路径但空库”的 Store（测试隔离用）
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return Store(path)


class TestStore(unittest.TestCase):
    def test_apply_fill_weighted_cost_and_highest(self):
        """网关/账本单测：test_apply_fill_weighted_cost_and_highest"""
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

    def test_user_id_isolated_in_store(self):
        """§P1-9 成交/委托落库携带归属账号 ID（多账号隔离）。"""
        # §P1-9 成交/委托落库携带归属账号 ID（多账号隔离）。
        s = new_store()
        s.apply_fill({"code": "600519.SH", "side": "买入", "price": 10, "qty": 100,
                      "amount": 1000, "traded_at": "t1", "signal_id": "S1", "user_id": "uA"})
        self.assertEqual(s.list_positions()[0]["user_id"], "uA")
        # 成交去重表里也带 user_id
        s.upsert_order({"signal_id": "S1", "code": "600519.SH", "side": "买入",
                        "status": "已报", "price": 10, "qty": 100, "created_at": "t1",
                        "user_id": "uA"})
        o = s.order_by_signal("S1")
        self.assertEqual(o["user_id"], "uA")
        # 对账持仓带 user_id；对账会用传入集合覆盖，故把该持仓一起纳入对账集合
        n = s.reconcile_positions([{"ts_code": "600519.SH", "qty": 100, "cost_price": 10,
                                    "amount": 1000, "user_id": "uA"},
                                   {"ts_code": "000001.SZ", "qty": 50, "cost_price": 5,
                                    "amount": 250, "user_id": "uB"}])
        self.assertEqual(n, 2)
        by_uB = [p for p in s.list_positions() if p["user_id"] == "uB"]
        self.assertEqual(len(by_uB), 1)
        by_uA = [p for p in s.list_positions() if p["user_id"] == "uA"]
        self.assertEqual(len(by_uA), 1)

    def test_reconcile_removes_absent(self):
        """网关/账本单测：test_reconcile_removes_absent"""
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
        """网关/账本单测：test_orders_unique_signal"""
        s = new_store()
        s.upsert_order({"order_id": "A", "signal_id": "S1", "code": "600519.SH", "side": "买入",
                        "status": "已报", "price": 10, "qty": 100, "created_at": "t"})
        # 同 signal_id 再插 → is_new=False（幂等）
        is_new = s.upsert_order({"order_id": "B", "signal_id": "S1", "code": "600519.SH",
                                 "side": "买入", "status": "已成", "price": 10, "qty": 100,
                                 "created_at": "t2"})
        self.assertFalse(is_new)
        # §G2 新语义：首个真实 order_id 固化不被覆盖；status 照常刷新
        row = s.order_by_signal("S1")
        self.assertEqual(row["order_id"], "A")
        self.assertEqual(row["status"], "已成")

    def test_claim_settle_release(self):
        """§G1 占位式幂等：claim 原子抢占、占位阻塞重复下单、settle 回填、release 释放。"""
        s = new_store()
        draft = {"signal_id": "S9", "code": "000001.SZ", "side": "买入",
                 "price": 10.0, "qty": 100, "created_at": "t"}
        claimed, existing = s.claim_order(draft)
        self.assertTrue(claimed)
        self.assertIsNone(existing)
        # 二次 claim → 抢不到，返回 pending 占位行
        claimed2, existing2 = s.claim_order(draft)
        self.assertFalse(claimed2)
        self.assertEqual(existing2["status"], "pending")
        # settle 回填真实委托号
        s.upsert_order({"order_id": "seq:12", "signal_id": "S9", "code": "000001.SZ",
                        "side": "买入", "status": "已报", "price": 10, "qty": 100,
                        "created_at": "t"})
        self.assertEqual(s.order_by_signal("S9")["order_id"], "seq:12")
        # 回报交易所委托号 → 替换 seq 占位引用（一次）
        s.upsert_order({"order_id": "2026082500001", "signal_id": "S9", "status": "已成"})
        row = s.order_by_signal("S9")
        self.assertEqual(row["order_id"], "2026082500001")
        self.assertEqual(row["status"], "已成")
        # 再来一条不同委托号的回报 → 不覆盖首个真实号
        s.upsert_order({"order_id": "OTHER", "signal_id": "S9", "status": "已成"})
        self.assertEqual(s.order_by_signal("S9")["order_id"], "2026082500001")
        # release：仅删未结算 pending
        d2 = dict(draft, signal_id="S10")
        claimed3, _ = s.claim_order(d2)
        self.assertTrue(claimed3)
        s.release_pending("S10")
        self.assertIsNone(s.order_by_signal("S10"))

    def test_apply_fill_duplicate_replay(self):
        """§G10 成交去重：时间窗内同 (order_id,side,price,qty) 重放不改持仓。"""
        s = new_store()
        f = {"order_id": "X1", "code": "600519.SH", "side": "买入", "price": 10, "qty": 100,
             "amount": 1000, "traded_at": datetime.now(CN_TZ).strftime("%Y-%m-%dT%H:%M:%S"),
             "signal_id": "S1"}
        pos, dup = s.apply_fill(f)
        self.assertFalse(dup)
        self.assertEqual(pos["qty"], 100)
        pos2, dup2 = s.apply_fill(dict(f))
        self.assertTrue(dup2)
        p = s.list_positions()[0]
        self.assertEqual(p["qty"], 100)  # 未翻倍

    def test_empty_positions_snapshot_guard(self):
        """§G5：单次空快照不清账本；连续两次才接受清空。"""
        s = new_store()
        h = ReportHandler(s, "", "")
        s.upsert_position({"ts_code": "600519.SH", "qty": 100, "cost_price": 1500})
        h.on_positions([])  # 第 1 次：跳过
        self.assertEqual(len(s.list_positions()), 1)
        h.on_positions([])  # 第 2 次：接受清空
        self.assertEqual(s.list_positions(), [])


class TestLotRule(unittest.TestCase):
    def test_lot_rule_by_board(self):
        """网关/账本单测：test_lot_rule_by_board"""
        from gateway import lot_rule
        self.assertEqual(lot_rule("600519.SH", "买入"), (100, 100))
        self.assertEqual(lot_rule("000001.SZ", "买入"), (100, 100))
        self.assertEqual(lot_rule("300750.SZ", "买入"), (100, 1))
        self.assertEqual(lot_rule("688160.SH", "买入"), (200, 1))

    def test_zero_lot_sell_allowed_by_gateway(self):
        """卖出允许零股（送转股清仓）：网关层放行，不按买入整手规则拦截。"""
        fd, dbpath = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        os.unlink(dbpath)
        gw = Gateway({
            "listen": "127.0.0.1:0", "token": "tk", "broker": "mock", "account": "M",
            "db": dbpath, "report_url": "", "reconcile_sec": 0,
            "seed": [{"ts_code": "600519.SH", "name": "贵州茅台", "qty": 100,
                      "cost_price": 1500, "highest_price": 1500}],
        })
        try:
            status, body = gw._do_order({
                "signal_id": "SL1", "code": "600519.SH", "side": "卖出",
                "price_type": "limit", "price": 1500, "qty": 50, "created_at": "t",
            })
            self.assertEqual(status, 200)
            self.assertTrue(body["ok"])
            # 50 股零股卖单已受理
            self.assertTrue(gw.store.order_by_signal("SL1"))
        finally:
            gw.stop()


class TestIdempotency(unittest.TestCase):
    def test_check_and_record(self):
        """网关/账本单测：test_check_and_record"""
        s = new_store()
        ids = Idempotency(s)
        self.assertTrue(ids.check("S1")[0])
        ids.record({"order_id": "A", "signal_id": "S1", "code": "600519.SH"})
        is_new, existing = ids.check("S1")
        self.assertFalse(is_new)
        self.assertEqual(existing["order_id"], "A")


class TestHandler(unittest.TestCase):
    def test_trade_push_and_disconnect(self):
        """网关/账本单测：test_trade_push_and_disconnect"""
        s = new_store()
        pushed = []
        captured = {}

        def fake_post(url, token, payload):
            """网关/账本单测：fake_post"""
            pushed.append(payload)
            return True

        old = post_report
        import handler as handler_mod
        handler_mod.post_report = fake_post
        # §FIX 2026-08-31：on_disconnected 仅交易时段推送（防非交易时段熔断误报）——
        # 该测试此前在收盘后跑必挂（时间敏感）。冻结为交易时段，测试不再依赖挂钟。
        old_session = handler_mod.is_active_trading_session
        handler_mod.is_active_trading_session = lambda: True
        try:
            h = ReportHandler(s, "http://seoul:8080", "tok")
            h._push = lambda p: pushed.append(p)  # 直接捕获
            h.on_trade({"order_id": "O1", "code": "600519.SH", "side": "买入", "price": 10,
                        "qty": 100, "amount": 1000, "traded_at": "t", "signal_id": "S1"})
            h.on_disconnected()
        finally:
            handler_mod.post_report = old
            handler_mod.is_active_trading_session = old_session
        types = [p["type"] for p in pushed]
        self.assertEqual(types, ["trade", "disconnect"])
        # 落库校验
        self.assertEqual(s.list_positions()[0]["qty"], 100)
        self.assertTrue(h.disconnected)


class TestMockBroker(unittest.TestCase):
    def test_place_order_fill_updates_position(self):
        """网关/账本单测：test_place_order_fill_updates_position"""
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

    def test_cash_model_decrements_and_replenishes(self):
        """§P1-17 显式现金模型：买入扣减 price*qty，卖出回补；初始资金=200000。"""
        # §P1-17 显式现金模型：买入扣减 price*qty，卖出回补；初始资金=200000。
        b = MockBroker(account_init=200000.0, delay_sec=0.05)
        b.connect()
        ok, oid, err = b.place_order({"signal_id": "S1", "code": "600519.SH",
                                      "name": "贵州茅台", "side": "买入", "price": 1000,
                                      "qty": 100, "amount": 100000})
        self.assertTrue(ok)
        time.sleep(0.3)
        # 买入 100@1000 = 100000，可用资金应降至 100000
        self.assertAlmostEqual(b.query_asset()["cash"], 100000.0)
        # 再卖出 100@1100 → 回补 110000，可用资金回到 210000
        ok, oid2, err = b.place_order({"signal_id": "S2", "code": "600519.SH",
                                       "name": "贵州茅台", "side": "卖出", "price": 1100,
                                       "qty": 100, "amount": 110000})
        self.assertTrue(ok)
        time.sleep(0.3)
        self.assertAlmostEqual(b.query_asset()["cash"], 210000.0)
        self.assertEqual(b.query_asset()["market_value"], 0.0)


class TestXtBrokerLazyImport(unittest.TestCase):
    def test_importable_without_xtquant(self):
        """无 xtquant 的环境应能实例化；connect 时才报错（延迟 import）"""
        # 无 xtquant 的环境应能实例化；connect 时才报错（延迟 import）
        b = XtBroker("A1")
        self.assertFalse(b.is_connected())
        try:
            b.connect()
            self.fail("expected RuntimeError without xtquant")
        except RuntimeError as e:
            self.assertIn("xtquant", str(e))

    def test_build_broker(self):
        """网关/账本单测：test_build_broker"""
        self.assertIsInstance(build_broker({"broker": "mock"}), MockBroker)
        self.assertIsInstance(build_broker({}), MockBroker)
        self.assertIsInstance(build_broker({"broker": "xt"}), XtBroker)


class TestGatewayHTTP(unittest.TestCase):
    def setUp(self):
        """网关/账本单测：setUp"""
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
        """网关/账本单测：tearDown"""
        self.gw._stop.set()
        self.server.shutdown()
        self.server.server_close()

    def _req(self, method, path, body=None, token="tk"):
        """构造一次本地 HTTP 请求，带 JSON 体与 Bearer 鉴权，返回 (status, json)"""
        # 构造一次本地 HTTP 请求，带 JSON 体与 Bearer 鉴权，返回 (status, json)
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
        """网关/账本单测：test_health_no_auth"""
        status, body = self._req("GET", "/health", token="")
        self.assertEqual(status, 200)
        self.assertTrue(body["ok"])

    def test_unauthorized(self):
        """网关/账本单测：test_unauthorized"""
        status, body = self._req("GET", "/state", token="wrong")
        self.assertEqual(status, 401)

    def test_order_state_and_idempotent(self):
        """网关/账本单测：test_order_state_and_idempotent"""
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
        self.assertEqual(state["orders"][0]["order_id"], order_id)
        self.assertEqual(state["positions"][0]["ts_code"], "600519.SH")
        self.assertEqual(state["positions"][0]["qty"], 100)

    def test_empty_signal_id_rejected(self):
        """§G2 空 signal_id 一律 400，不进入下单路径。"""
        status, body = self._req("POST", "/order", {
            "signal_id": "", "code": "600519.SH", "side": "买入", "price": 1510,
            "qty": 100, "created_at": "t",
        })
        self.assertEqual(status, 400)
        # 未产生任何订单
        _, state = self._req("GET", "/state")
        self.assertEqual(len(state["orders"]), 0)

    def test_sci_board_lot_rules(self):
        """§G7 科创板最低 200 股；主板整手。"""
        # 688xxx 150 股 → 拒
        status, _ = self._req("POST", "/order", {
            "signal_id": "SC1", "code": "688160.SH", "side": "买入", "price": 50,
            "qty": 150, "created_at": "t",
        })
        self.assertEqual(status, 400)
        # 688xxx 250 股 → 过
        status2, body2 = self._req("POST", "/order", {
            "signal_id": "SC2", "code": "688160.SH", "side": "买入", "price": 50,
            "qty": 250, "created_at": "t",
        })
        self.assertEqual(status2, 200)
        self.assertTrue(body2["ok"])

    def test_cancel_unknown_returns_err(self):
        """撤单结果不再恒 ok：未知委托返回错误。"""
        status, body = self._req("POST", "/cancel", {"order_id": "MOCK999999"})
        self.assertEqual(status, 409)
        self.assertFalse(body["ok"])

    def test_health_reports_broker_state(self):
        """网关/账本单测：test_health_reports_broker_state"""
        status, body = self._req("GET", "/health", token="")
        self.assertEqual(status, 200)
        self.assertTrue(body["ok"])
        self.assertIn("broker_connected", body)

    def test_bad_types_rejected_cleanly(self):
        """§G8 入参类型前置校验：非法类型返回 400 JSON 而非断连。"""
        status, body = self._req("POST", "/order", {
            "signal_id": "T1", "code": "600519.SH", "side": "买入",
            "price": "abc", "qty": "xyz", "created_at": "t",
        })
        self.assertEqual(status, 400)


if __name__ == "__main__":
    unittest.main()