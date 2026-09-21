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

    def test_dispatch_reap_stale_inflight(self):
        """§M16：取单落 inflight_at；超龄 inflight 收割判废落 done，新鲜不误收。"""
        seq = self.store.dispatch_enqueue_order(
            {"signal_id": "S9", "code": "600519.SH", "side": "买入", "qty": 100})
        items = self.store.dispatch_pending()
        self.assertEqual(len(items), 1)
        row = self.store.dispatch_get(seq)
        self.assertEqual(row["status"], "inflight")
        self.assertTrue(row["inflight_at"])  # §M16 转 inflight 即落取单时刻
        # 阈值内：一条不收（回报窗口未过）
        self.assertEqual(self.store.dispatch_reap_stale_inflight(1800), [])
        # 人为推老取单时刻 → 收割：status done、result 判废留痕
        with self.store._lock:
            self.store._conn.execute(
                "UPDATE dispatch SET inflight_at = ?", ("2020-01-01T00:00:00+08:00",))
            self.store._conn.commit()
        reaped = self.store.dispatch_reap_stale_inflight(1800)
        self.assertEqual([r["seq"] for r in reaped], [seq])
        done = self.store.dispatch_get(seq)
        self.assertEqual(done["status"], "done")
        result = json.loads(done["result"])
        self.assertFalse(result["ok"])
        self.assertTrue(result.get("reaped"))
        self.assertEqual(self.store.dispatch_stats().get("inflight", 0), 0)

    def test_dispatch_reap_fallback_created_at_and_disabled(self):
        """§M16：老行（无 inflight_at）回退 created_at 判龄；阈值<=0 视为关闭收割。"""
        seq = self.store.dispatch_enqueue_order(
            {"signal_id": "S10", "code": "000001.SZ", "side": "买入"})
        self.store.dispatch_pending()
        with self.store._lock:
            self.store._conn.execute(
                "UPDATE dispatch SET inflight_at = '', created_at = ? WHERE seq = ?",
                ("2020-01-01T00:00:00+08:00", seq))
            self.store._conn.commit()
        # 关闭态（<=0）不收割
        self.assertEqual(self.store.dispatch_reap_stale_inflight(0), [])
        self.assertEqual(self.store.dispatch_get(seq)["status"], "inflight")
        # 打开后按 created_at 龄收割
        reaped = self.store.dispatch_reap_stale_inflight(1800)
        self.assertEqual([r["seq"] for r in reaped], [seq])
        self.assertEqual(self.store.dispatch_get(seq)["status"], "done")


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
        self.gw.stop()  # §UAT-D8 走完整优雅停机
        self.server.shutdown()
        self.server.server_close()

    # 测试用 HTTP 客户端：token 可覆写（默认 tk），返回 (状态码, 解析后的 JSON)；
    # 4xx/5xx 走 HTTPError 分支同样以 (code, body) 返回，便于断言 401/403/503 拒绝码。
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

    def test_reap_stale_inflight_downgrades_order(self):
        """§M16：桥取单后回报丢失、inflight 超龄 → 网关收割并回写 orders「已废」，不再永挂。"""
        self._bridge_online()
        status, body = self._req("POST", "/order", {
            "signal_id": "S9", "code": "600519.SH", "side": "买入",
            "price": 1510, "qty": 100, "created_at": "t"})
        self.assertEqual(status, 200)
        seq = body["order_id"]
        # 桥取单（inflight），随后"回报丢失"（不再回 order_result）
        status, pend = self._req("GET", "/dispatch/pending")
        self.assertEqual([i["seq"] for i in pend["items"]], [seq])
        # 收割龄推到超龄（测试不等 30min 钟）
        with self.gw.store._lock:
            self.gw.store._conn.execute(
                "UPDATE dispatch SET inflight_at = ?", ("2020-01-01T00:00:00+08:00",))
            self.gw.store._conn.commit()
        n = self.gw._reap_dispatch_inflight("测试")
        self.assertEqual(n, 1)
        # orders 侧降级：已废（对账闭环），dispatch 队列 inflight 清零
        _, state = self._req("GET", "/state")
        self.assertEqual(state["orders"][0]["status"], "已废")
        stats = self.gw.store.dispatch_stats()
        self.assertEqual(stats.get("inflight", 0), 0)
        self.assertEqual(self.gw.store.dispatch_get(seq)["status"], "done")

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
    """自动翻转（2026-09-11 语义反转）：queued（文件桥）主 / xt 备。

    交易时段 active=queued 且桥心跳断 ≥ failover_sec 且 xt 在线 → 翻 xt 顶班；
    active=xt 且桥恢复 → 自动回切 queued。
    §P2-12（2026-09-15）：旧用例设置的是反转后的死字段 _xt_last_connected，
    实际全走 failback 分支蒙混通过——「queued 断连→xt 顶班」此前零真实覆盖。
    """

    def _make(self, failover_enable=True, failover_sec=1, active="queued"):
        cfg = {
            "listen": "127.0.0.1:0",
            "token": "tk",
            "broker": active,
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
            self.gw.stop()  # §UAT-D8 走完整优雅停机
            self.server.shutdown()
            self.server.server_close()

    def test_failover_when_bridge_stale_and_xt_up(self):
        """交易时段 + 主接线 queued 桥心跳断 ≥ failover_sec + xt 在线 → 翻 xt 顶班。"""
        self.gw = self._make(active="queued")
        gw_mod.is_active_trading_session = lambda: True  # 强制交易时段
        # 桥心跳缺失（bridge_state 无记录 → queued.is_connected()=False），
        # 观察窗口已过（上次心跳时刻推到 5s 前 ≥ failover_sec=1）
        self.gw._queued_last_connected = time.time() - 5
        self.gw.brokers["xt"]._connected = True  # xt 备通道可用
        self.assertEqual(self.gw.active_key, "queued")
        self.gw._maybe_failover()
        self.assertEqual(self.gw.active_key, "xt")

    def test_no_failover_when_xt_down(self):
        """主接线断桥但 xt 备也不在线 → 保持 queued 继续观察，绝不翻到死通道。"""
        self.gw = self._make(active="queued")
        gw_mod.is_active_trading_session = lambda: True
        self.gw._queued_last_connected = time.time() - 999
        # xt._connected 保持 False（延迟 import 未连接）
        self.gw._maybe_failover()
        self.assertEqual(self.gw.active_key, "queued")

    def test_failback_when_bridge_recovers(self):
        """active=xt（顶班中）且桥心跳恢复新鲜 → 自动回切主接线 queued。"""
        self.gw = self._make(active="xt")
        gw_mod.is_active_trading_session = lambda: True
        self.gw.store.bridge_heartbeat()  # 桥恢复在线
        self.gw._maybe_failover()
        self.assertEqual(self.gw.active_key, "queued")

    def test_no_failover_off_hours(self):
        """非交易时段断连不翻转（qmtctl 杀客户端属预期）。"""
        self.gw = self._make(active="queued")
        gw_mod.is_active_trading_session = lambda: False
        self.gw._queued_last_connected = time.time() - 999
        self.gw.brokers["xt"]._connected = True
        self.gw._maybe_failover()
        self.assertEqual(self.gw.active_key, "queued")

    def test_no_failover_without_flag(self):
        """failover_enable=false 不自动翻转。"""
        self.gw = self._make(failover_enable=False, active="queued")
        gw_mod.is_active_trading_session = lambda: True
        self.gw._queued_last_connected = time.time() - 999
        self.gw.brokers["xt"]._connected = True
        self.gw._maybe_failover()
        self.assertEqual(self.gw.active_key, "queued")

    def test_no_failover_during_observation_window(self):
        """启动后首次探测的观察窗口内不翻转（_queued_last_connected=0 → 先给窗口）。"""
        self.gw = self._make(active="queued")
        gw_mod.is_active_trading_session = lambda: True
        self.gw.brokers["xt"]._connected = True
        self.gw._maybe_failover()  # 无心跳记录且首次探测 → 只记观察起点
        self.assertEqual(self.gw.active_key, "queued")


if __name__ == "__main__":
    unittest.main()
