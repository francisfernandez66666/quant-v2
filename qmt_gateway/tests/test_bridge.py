#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_bridge 单测：Bridge 主循环（dry-run 适配器）与真实网关全协议交互。

无需 Windows/xtquant：用 XtAdapter(dry_run=True) 走通「心跳→快照→取单→下单回报」
真实代码路径，验证桥侧协议（GET /dispatch/pending + POST /dispatch/result）与网关侧
（入队/结算/回报/心跳连通）端到端闭环。真机仅在 XtAdapter 内部换真实 xttrader。
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

from qmt_bridge import Bridge  # noqa: E402
from gateway import Gateway, _Handler  # noqa: E402
from http.server import ThreadingHTTPServer  # noqa: E402


def _tmp():
    """创建并删除临时 DB 文件路径（Store 首次建库用）。"""
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return path


class TestBridgeLoop(unittest.TestCase):
    """桥 dry-run 全流程：下单入队→桥取单执行→回报→委托已报；心跳驱动连通。"""

    def setUp(self):
        cfg = {
            "listen": "127.0.0.1:0",
            "token": "tk",
            "broker": "queued",
            "account": "T0001",
            "db": _tmp(),
            "report_url": "",
            "reconcile_sec": 0,
            "failover_enable": False,
            "bridge_heartbeat_timeout_sec": 3,
            "user_id": "uA",
        }
        self.gw = Gateway(cfg)
        _Handler.gateway = self.gw
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        self.port = self.server.server_address[1]
        self.gw.start()
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        # 桥：dry_run 走真实代码路径（仅下单/查询由适配器降级为不落柜台的模拟返回）
        self.bridge = Bridge("http://127.0.0.1:%d" % self.port, token="tk",
                             account="T0001", poll_sec=0.05, heartbeat_sec=0.1,
                             positions_sec=5.0, dry_run=True)

    def tearDown(self):
        self.gw.stop()  # §UAT-D8 走完整优雅停机
        self.server.shutdown()
        self.server.server_close()

    # 测试用 HTTP 客户端：自带 Bearer tk，返回 (状态码, 解析后的 JSON)；
    # 4xx/5xx 由 HTTPError 分支同样返回而不抛异常，方便断言网关的拒绝姿态。
    def _req(self, method, path, body=None):
        url = "http://127.0.0.1:%d%s" % (self.port, path)
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", "Bearer tk")
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                return resp.status, json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read().decode("utf-8"))

    def test_heartbeat_drives_connectivity(self):
        """桥心跳上线 → queued 通道在线（/health queued_connected=true）。"""
        # 启动后尚未心跳：离线
        _, h = self._req("GET", "/health")
        self.assertFalse(h.get("queued_connected"))
        # 桥跑三轮（首轮即心跳）
        for _ in range(3):
            self.bridge.run_once()
        self.assertTrue(self.gw.store.bridge_connected(3))

    def test_full_loop_order_placed_via_bridge(self):
        """下单入队→桥取单→(dry-run)回报成交委托号→委托已报。"""
        status, _ = self._req("POST", "/order", {
            "signal_id": "S1", "code": "600519.SH", "name": "贵州茅台", "side": "买入",
            "price_type": "market", "price": 1510, "qty": 100, "amount": 151000,
            "created_at": "t"})
        self.assertEqual(status, 503)  # 桥尚未心跳，订单拒绝（安全语义）
        # 桥上线后重下单
        for _ in range(5):
            self.bridge.run_once()
            time.sleep(0.05)
        status, body = self._req("POST", "/order", {
            "signal_id": "S2", "code": "600519.SH", "name": "贵州茅台", "side": "买入",
            "price_type": "market", "price": 1510, "qty": 100, "amount": 151000,
            "created_at": "t"})
        self.assertEqual(status, 200)
        order_id = body["order_id"]
        # 桥取单并执行（dry-run：DRYRUN-* 委托号回报）
        for _ in range(5):
            self.bridge.run_once()
            time.sleep(0.05)
        _, pend = self._req("GET", "/dispatch/pending")
        self.assertEqual(pend["items"], [])  # 已全部取走执行
        _, state = self._req("GET", "/state")
        self.assertEqual(state["orders"][0]["status"], "已报")
        self.assertTrue(str(state["orders"][0]["order_id"]).startswith("DRYRUN-"),
                        state["orders"][0]["order_id"])
        self.assertEqual(state["orders"][0]["signal_id"], "S2")

    def test_rejected_order_reported_waste(self):
        """桥回报下单失败 → 委托置已废（dry-run 对空 signal/issues 的拒单路径）。"""
        # 直接探一条会被桥拒的单：无 signal_id 的派发行由桥回报 err（协议层保护）
        self.bridge.adapter.dry_run = True  # 保持 dry-run
        for _ in range(3):
            self.bridge.run_once()
            time.sleep(0.02)
        # 已有心跳，正常下单后由桥执行成功路径已在上例覆盖；此处验证 reject 分支：
        self.bridge._execute_order = lambda item: self.bridge._post(
            "/dispatch/result", {"type": "order_result", "seq": item["seq"],
                                 "ok": False, "err": "模拟拒单"})
        self._req("POST", "/order", {
            "signal_id": "S3", "code": "000001.SZ", "side": "买入",
            "price": 10, "qty": 100, "created_at": "t"})
        for _ in range(5):
            self.bridge.run_once()
            time.sleep(0.03)
        _, state = self._req("GET", "/state")
        self.assertEqual(state["orders"][0]["status"], "已废")

    # ── §M16（2026-09-22）：HTTP 桥 inflight 收割配套——diag kind 识别 + unknown kind 负回执 ──

    def test_diag_dispatch_end_to_end_no_more_hang(self):
        """§M16：/dispatch/enqueue 注入 diag → 桥取走并回 type=diag → 派发行结算 done。

        旧实现桥不识别 diag kind（只 warn 不回执），该行永久卡 inflight——本用例即回归锁。
        """
        status, body = self._req("POST", "/dispatch/enqueue", {"kind": "diag", "signal_id": "DIAG-M16"})
        self.assertEqual(status, 200, body)
        seq = body["seq"]
        for _ in range(5):
            self.bridge.run_once()
            time.sleep(0.03)
        row = self.gw.store.dispatch_get(seq)
        self.assertEqual(row["status"], "done", row)
        self.assertEqual(self.gw.store.dispatch_stats().get("inflight", 0), 0)

    def test_unknown_kind_negative_ack(self):
        """§M16：unknown kind 也必须负回执（order_result ok=false），网关据此判废结算。"""
        posts = []
        self.bridge._get = lambda path: (200, {"ok": True, "items": [{"seq": "seq:77", "kind": "bogus"}]})
        self.bridge._post = lambda path, payload: (posts.append((path, payload)), (200, {"ok": True}))[1]
        self.bridge._process_pending()
        acks = [p for (path, p) in posts if path == "/dispatch/result"]
        self.assertEqual(len(acks), 1, posts)
        self.assertEqual(acks[0]["type"], "order_result")
        self.assertEqual(acks[0]["seq"], "seq:77")
        self.assertFalse(acks[0]["ok"])
        self.assertIn("unknown kind", acks[0]["err"])


if __name__ == "__main__":
    unittest.main()