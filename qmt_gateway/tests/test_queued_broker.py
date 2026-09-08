#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway 单测（§QMT-DUAL）：QueuedBroker 派发队列 + 双路径切换 + 假桥端到端。

无需 Windows/xtquant：桥侧用 FakeAdapter（直连网关 /dispatch 协议）驱动，
验证「/order 入队 → 桥取单执行 → 回报成交 → 量仔侧事件落库」全链路 + 自动翻转。
"""
import json
import os
import sys
import tempfile
import threading
import time
import unittest
import urllib.request
import urllib.error

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import gateway as gw_mod  # noqa: E402
from store import Store  # noqa: E402
from gateway import Gateway, _Handler  # noqa: E402
from broker import QueuedBroker  # noqa: E402
from http.server import ThreadingHTTPServer  # noqa: E402


def new_db_path():
    """创建并删除临时 DB 文件路径，返回一个不存在的路径（Store 首次建库用）。"""
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return path


class TestQueuedBrokerStore(unittest.TestCase):
    """QueuedBroker 与 store.dispatch/bridge_state 语义单测。"""

    def setUp(self):
        self.store = Store(new_db_path())

    def test_dispatch_enqueue_pending_inflight(self):
        """下单入队返回 seq 占位；pending 原子取单并标记 inflight；二次取单为空。"""
        seq = self.store.dispatch_enqueue_order(
            {"signal_id": "S1", "code": "600519.SH", "side": "买入", "price_type": "market",
             "price": 1510, "qty": 100, "strategy": "dragon", "created_at": "t"}, user_id="uA")
        self.assertTrue(seq.startswith("seq:"))
        items = self.store.dispatch_pending()
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["signal_id"], "S1")
        self.assertEqual(items[0]["kind"], "order")
        # 二次取单为空（已 inflight）
        self.assertEqual(self.store.dispatch_pending(), [])

    def test_dispatch_set_result_resolves_exchange_id(self):
        """下单结果回填交易所委托号；撤单解析走该映射。"""
        seq = self.store.dispatch_enqueue_order({"signal_id": "S1", "code": "600519.SH",
                                                 "side": "买入"})
        row = self.store.dispatch_set_result(seq, {"ok": True, "order_id": "888", "err": ""})
        self.assertEqual(row["status"], "pending")  # 返回的是结算前快照行
        done = self.store.dispatch_get(seq)
        self.assertEqual(done["status"], "done")
        self.assertEqual(done["order_id"], "888")
        # 撤单目标解析：seq 占位 → 交易所委托号
        b = QueuedBroker(self.store, account="A", user_id="uA")
        exchange, sid, code, side = b._resolve_cancel_target(seq)
        self.assertEqual(exchange, "888")
        self.assertEqual(sid, "S1")
        # 撤单入队
        ok, err = b.cancel(seq)
        self.assertTrue(ok, err)
        cancels = [i for i in self.store.dispatch_pending() if i["kind"] == "cancel"]
        self.assertEqual(len(cancels), 1)
        self.assertEqual(cancels[0]["order_id"], "888")

    def test_cancel_unresolved_rejected(self):
        """交易所委托号未回报前不可撤（与 XtBroker 语义一致）。"""
        seq = self.store.dispatch_enqueue_order({"signal_id": "S2", "code": "000001.SZ",
                                                 "side": "买入"})
        b = QueuedBroker(self.store)
        ok, err = b.cancel(seq)
        self.assertFalse(ok)
        self.assertIn("尚未回报", err)

    def test_bridge_heartbeat_connected(self):
        """桥心跳驱动连通性：无心跳→False；有心跳且新鲜→True；超时→False。"""
        b = QueuedBroker(self.store, heartbeat_timeout_sec=1)
        self.assertFalse(b.is_connected())
        self.store.bridge_heartbeat()
        self.assertTrue(b.is_connected())
        time.sleep(1.2)
        self.assertFalse(b.is_connected())

    def test_bridge_snapshot_positions_asset(self):
        """桥持仓/资产快照 → query_positions/query_asset。"""
        self.store.bridge_snapshot_set("positions", [{"ts_code": "600519.SH", "qty": 100}])
        self.store.bridge_snapshot_set("asset", {"cash": 100000, "total_asset": 300000})
        b = QueuedBroker(self.store)
        self.assertEqual(b.query_positions()[0]["qty"], 100)
        self.assertEqual(b.query_asset()["cash"], 100000)


class TestQueuedHTTP(unittest.TestCase):
    """HTTP 端到端：queued 模式 /order → 派发 → 假桥取单成交 → 量仔事件 → /state。"""

    def setUp(self):
        cfg = {
            "listen": "127.0.0.1:0",
            "token": "tk",
            "broker": "queued",
            "account": "T0001",
            "db": new_db_path(),
            "report_url": "",
            "reconcile_sec": 0,
            "failover_enable": False,
            "bridge_heartbeat_timeout_sec": 15,
            "user_id": "uA",
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

    def _bridge_online(self):
        """模拟桥心跳在线（否则 queued /order 一律 503）。"""
        status, body = self._req("POST", "/dispatch/result", {"type": "heartbeat"})
        self.assertEqual(status, 200)
        return body

    def test_order_requires_bridge_online(self):
        """queued 模式桥离线时 /order 返回 503（安全：不盲目入队）。"""
        status, _ = self._req("POST", "/order", {
            "signal_id": "S0", "code": "600519.SH", "side": "买入",
            "price": 1510, "qty": 100, "created_at": "t"})
        self.assertEqual(status, 503)

    def test_order_queued_bridge_execute_full_loop(self):
        """全链路：/order 入队(200+seq) → 桥取单 → order_result(交易所号) → trade → /state 已成。"""
        self._bridge_online()
        status, body = self._req("POST", "/order", {
            "signal_id": "S1", "code": "600519.SH", "name": "贵州茅台", "side": "买入",
            "price_type": "market", "price": 1510, "qty": 100, "amount": 151000,
            "created_at": "t"})
        self.assertEqual(status, 200)
        self.assertTrue(body["ok"])
        order_id = body["order_id"]
        self.assertTrue(order_id.startswith("seq:"))

        # 桥取单
        status, pend = self._req("GET", "/dispatch/pending")
        self.assertEqual(status, 200)
        self.assertEqual(len(pend["items"]), 1)
        item = pend["items"][0]
        self.assertEqual(item["signal_id"], "S1")
        self.assertEqual(item["kind"], "order")

        # 桥回报：下单成功（交易所委托号 888）
        status, _ = self._req("POST", "/dispatch/result", {
            "type": "order_result", "seq": item["seq"], "ok": True, "order_id": "888", "err": ""})
        self.assertEqual(status, 200)

        # 桥回报：成交（trade）
        status, _ = self._req("POST", "/dispatch/result", {
            "type": "trade", "seq": item["seq"], "order_id": "888", "trade_id": "TR1",
            "name": "贵州茅台", "code": "600519.SH", "side": "买入",
            "price": 1510, "qty": 100, "amount": 151000, "traded_at": "t+1",
            "signal_id": "S1"})
        self.assertEqual(status, 200)

        # 桥周期快照：持仓（queued 模式 /state 的持仓源 = 桥快照，即柜台真实持仓）
        status, _ = self._req("POST", "/dispatch/result", {
            "type": "positions",
            "positions": [{"ts_code": "600519.SH", "name": "贵州茅台", "qty": 100,
                           "cost_price": 1510, "amount": 151000, "highest_price": 1510,
                           "updated_at": "t+1"}]})
        self.assertEqual(status, 200)

        # 状态：委托已成 + 持仓（来自桥快照）
        status, state = self._req("GET", "/state")
        self.assertEqual(status, 200)
        self.assertEqual(state["broker_mode"], "queued")
        self.assertEqual(state["orders"][0]["status"], "已成")
        self.assertEqual(state["orders"][0]["order_id"], "888")
        self.assertEqual(state["positions"][0]["ts_code"], "600519.SH")
        self.assertEqual(state["positions"][0]["qty"], 100)

    def test_order_async_rejection_marks_waste(self):
        """桥回报下单失败 → 委托置已废（带拒因），量仔侧可感知，不产生持仓。"""
        self._bridge_online()
        status, _ = self._req("POST", "/order", {
            "signal_id": "S2", "code": "000001.SZ", "side": "买入",
            "price": 10, "qty": 100, "created_at": "t"})
        self.assertEqual(status, 200)
        status, pend = self._req("GET", "/dispatch/pending")
        item = pend["items"][0]
        status, _ = self._req("POST", "/dispatch/result", {
            "type": "order_result", "seq": item["seq"], "ok": False, "err": "柜台拒绝"})
        self.assertEqual(status, 200)
        _, state = self._req("GET", "/state")
        self.assertEqual(state["orders"][0]["status"], "已废")
        self.assertEqual(state["positions"], [])

    def test_cancel_flow_via_bridge(self):
        """撤单：/cancel → 桥取到 cancel 项 → cancel_result → 委托已撤。"""
        self._bridge_online()
        status, body = self._req("POST", "/order", {
            "signal_id": "S3", "code": "600519.SH", "side": "买入",
            "price": 1510, "qty": 100, "created_at": "t"})
        seq = body["order_id"]
        # 先让桥确认下单（拿到交易所号），否则撤单解析失败
        status, pend = self._req("GET", "/dispatch/pending")
        item = [i for i in pend["items"] if i["kind"] == "order"][0]
        self._req("POST", "/dispatch/result", {
            "type": "order_result", "seq": item["seq"], "ok": True, "order_id": "999", "err": ""})
        # 撤单入队
        status, cbody = self._req("POST", "/cancel", {"order_id": seq})
        self.assertEqual(status, 200, cbody)
        status, pend = self._req("GET", "/dispatch/pending")
        cancels = [i for i in pend["items"] if i["kind"] == "cancel"]
        self.assertEqual(len(cancels), 1)
        self.assertEqual(cancels[0]["order_id"], "999")
        # 桥回报撤单成功
        status, _ = self._req("POST", "/dispatch/result", {
            "type": "cancel_result", "seq": cancels[0]["seq"], "ok": True, "err": ""})
        self.assertEqual(status, 200)
        _, state = self._req("GET", "/state")
        self.assertEqual(state["orders"][0]["status"], "已撤")

    def test_admin_broker_switch(self):
        """/admin/broker 切换 active 通道；/health 透出双状态。"""
        self._bridge_online()
        # 初始 queued
        _, health = self._req("GET", "/health")
        self.assertEqual(health["broker"], "queued")
        self.assertTrue(health["queued_connected"])
        self.assertFalse(health["xt_connected"])
        # 切到 xt（本机 xt 未连 → broker_connected=false，但切换成功）
        status, body = self._req("POST", "/admin/broker", {"broker": "xt"})
        self.assertEqual(status, 200)
        self.assertEqual(body["broker"], "xt")
        _, health = self._req("GET", "/health")
        self.assertEqual(health["broker"], "xt")
        self.assertFalse(health["broker_connected"])
        # 切回 queued
        status, _ = self._req("POST", "/admin/broker", {"broker": "queued"})
        self.assertEqual(status, 200)
        _, health = self._req("GET", "/health")
        self.assertEqual(health["broker"], "queued")
        # 未知通道 400
        status, _ = self._req("POST", "/admin/broker", {"broker": "bogus"})
        self.assertEqual(status, 400)


class TestFailover(unittest.TestCase):
    """自动翻转：交易时段 + xt 断连 ≥ failover_sec + 桥心跳新鲜 → 自动切 queued。"""

    def _make(self, failover_enable=True, failover_sec=1):
        cfg = {
            "listen": "127.0.0.1:0",
            "token": "tk",
            "broker": "xt",
            "account": "T0001",
            "db": new_db_path(),
            "report_url": "",
            "reconcile_sec": 0,
            "failover_enable": failover_enable,
            "failover_sec": failover_sec,
            "bridge_heartbeat_timeout_sec": 15,
        }
        gw = Gateway(cfg)
        _Handler.gateway = gw
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        self.port = self.server.server_address[1]
        gw.start()
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        return gw

    def tearDown(self):
        if getattr(self, "gw", None) is not None:
            self.gw._stop.set()
            self.server.shutdown()
            self.server.server_close()

    def test_failover_when_xt_down_and_bridge_healthy(self):
        """交易时段 + xt 断连超时 + 桥在线 → 自动切 queued。"""
        self.gw = self._make()
        gw_mod.is_active_trading_session = lambda: True  # 强制交易时段
        self.gw.store.bridge_heartbeat()                  # 桥在线
        self.gw._xt_last_connected = time.time() - 5      # 已断连 5s ≥ failover_sec=1
        self.assertEqual(self.gw.active_key, "xt")
        self.gw._maybe_failover()
        self.assertEqual(self.gw.active_key, "queued")

    def test_no_failover_off_hours(self):
        """非交易时段断连不翻转（qmtctl 杀客户端属预期）。"""
        self.gw = self._make()
        gw_mod.is_active_trading_session = lambda: False
        self.gw.store.bridge_heartbeat()
        self.gw._xt_last_connected = time.time() - 999
        self.gw._maybe_failover()
        self.assertEqual(self.gw.active_key, "xt")

    def test_no_failover_without_flag(self):
        """failover_enable=false 不自动翻转。"""
        self.gw = self._make(failover_enable=False)
        gw_mod.is_active_trading_session = lambda: True
        self.gw.store.bridge_heartbeat()
        self.gw._xt_last_connected = time.time() - 999
        self.gw._maybe_failover()
        self.assertEqual(self.gw.active_key, "xt")

    def test_no_failover_when_bridge_down(self):
        """桥也不在线时不翻转（保持 xt 并继续观察）。"""
        self.gw = self._make()
        gw_mod.is_active_trading_session = lambda: True
        self.gw._xt_last_connected = time.time() - 999
        self.gw._maybe_failover()  # 无桥心跳
        self.assertEqual(self.gw.active_key, "xt")


if __name__ == "__main__":
    unittest.main()
