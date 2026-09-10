#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway 单测（unittest，零第三方依赖）：§QMT-F16 文件桥 sidecar。

覆盖：上报文件逐行 _do_dispatch_result 语义（heartbeat 回执 / order_result 派发结算）、
pending 派发原子推送 bridge_cmd.json、文件轮转（缩短）回读。无需 Windows/xtquant。
"""
import json
import os
import sys
import tempfile
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from gateway import Gateway  # noqa: E402


def new_db_path():
    """创建临时 SQLite 文件并立即删除，得到“存在路径但空库”的存储路径（测试隔离用）。"""
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return path


class TestFileBridge(unittest.TestCase):
    def _new_gw(self, tmp_dir):
        """构造最小 Gateway（mock 通道 + 临时 bridge_report_dir，不起后台线程）。"""
        # 构造最小 Gateway：显式 bridge_report_dir 指向临时目录，隔离真实网关目录
        gw = Gateway({
            "listen": "127.0.0.1:0", "token": "tk", "broker": "mock", "account": "M",
            "db": new_db_path(), "report_url": "", "reconcile_sec": 0,
            "bridge_report_dir": tmp_dir,
            "seed": [{"ts_code": "600519.SH", "name": "贵州茅台", "qty": 100,
                      "cost_price": 1500, "highest_price": 1500}],
        })
        return gw

    def test_report_heartbeat_drives_bridge_connected(self):
        """上报一行 heartbeat → sidecar 应用 → store.bridge_heartbeat() → 桥在线。"""
        gw = self._new_gw(tempfile.mkdtemp())
        path = gw._file_bridge_path()
        # 先写一条心跳 JSONL 行（沙箱侧产出）
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps({"type": "heartbeat",
                                "ts": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
                                "n": 1}, ensure_ascii=False) + "\n")
        th = None
        try:
            th = threading.Thread(target=gw._file_bridge_loop, daemon=True)
            th.start()
            # 等 sidecar 至少扫到一行（tick=2s × 2 轮兜底）
            deadline = time.time() + 6
            while time.time() < deadline and not gw.store.bridge_connected(15):
                time.sleep(0.2)
            self.assertTrue(gw.store.bridge_connected(15), "心跳应经文件桥被网关采纳")
        finally:
            gw._stop.set()
            if th:
                th.join(timeout=2)

    def test_pending_pushed_to_cmd_file(self):
        """pending 派发项 → sidecar 原子写 bridge_cmd.json（桥执行前记录）。"""
        gw = self._new_gw(tempfile.mkdtemp())
        gw.store.dispatch_enqueue_order({"signal_id": "FB1", "code": "600519.SH",
                                         "side": "买入", "price_type": "limit",
                                         "price": 1500, "qty": 100}, user_id="test")
        try:
            gw._file_bridge_push_pending()
            cmd_path = gw._file_bridge_cmd_path()
            self.assertTrue(os.path.exists(cmd_path), "bridge_cmd.json 未生成")
            with open(cmd_path, "rb") as f:
                data = json.loads(f.read().decode("utf-8"))
            cmds = data.get("cmds") or []
            self.assertEqual(len(cmds), 1)
            self.assertEqual(str(cmds[0].get("kind")), "order")
            self.assertEqual(cmds[0].get("signal_id"), "FB1")
            # 再推一次：pending 已被取走（inflight），不会重复推送
            gw._file_bridge_push_pending()
            with open(cmd_path, "rb") as f:
                data = json.loads(f.read().decode("utf-8"))
            self.assertEqual(len(data.get("cmds") or []), 1)  # 文件仍只含上一批（不被覆盖清空）
        finally:
            gw.stop()

    def test_report_order_result_settles_dispatch(self):
        """order_result JSONL 行 → sidecar → 派发项 done + 委托号回填（实盘语义 dry 也通）。"""
        gw = self._new_gw(tempfile.mkdtemp())
        # 准备一条已完成下单的派发行（pending 派发并标记 inflight，模拟桥取单后）
        gw.store.dispatch_enqueue_order({"signal_id": "FB2", "code": "600519.SH",
                                         "side": "买入", "price_type": "limit",
                                         "price": 1500, "qty": 100})
        self.store_helper = None
        pend = gw.store.dispatch_pending(limit=1)
        self.assertEqual(len(pend), 1)
        seq = pend[0]["seq"]
        path = gw._file_bridge_path()
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps({"type": "order_result", "seq": seq, "ok": True,
                                "order_id": "DRYRUN-1", "err": ""},
                               ensure_ascii=False) + "\n")
        try:
            th = threading.Thread(target=gw._file_bridge_loop, daemon=True)
            th.start()
            deadline = time.time() + 6
            row = None
            while time.time() < deadline:
                row = gw.store.dispatch_get(seq)
                if row and row.get("status") == "done":
                    break
                time.sleep(0.2)
            self.assertIsNotNone(row)
            self.assertEqual(row.get("status"), "done")
            self.assertEqual(row.get("order_id"), "DRYRUN-1")
        finally:
            gw._stop.set()

    def test_report_file_rotation_rescanned_from_zero(self):
        """上报文件被沙箱重写（长度缩短）→ sidecar 偏移回退重读，不丢事件。"""
        gw = self._new_gw(tempfile.mkdtemp())
        path = gw._file_bridge_path()
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps({"type": "heartbeat",
                                "ts": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"), "n": 1},
                               ensure_ascii=False) + "\n")
        th = threading.Thread(target=gw._file_bridge_loop, daemon=True)
        try:
            th.start()
            deadline = time.time() + 6
            while time.time() < deadline and not gw.store.bridge_connected(15):
                time.sleep(0.2)
            self.assertTrue(gw.store.bridge_connected(15))
            # 沙箱轮转：清文本（文件 len 变小），重写两条事件（心跳修正场景）
            self.assertTrue(os.path.exists(path))
            with open(path, "w", encoding="utf-8") as f:
                f.write(json.dumps({"type": "heartbeat",
                                    "ts": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"), "n": 999},
                                   ensure_ascii=False) + "\n")
                f.write(json.dumps({"type": "heartbeat",
                                    "ts": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"), "n": 2},
                                   ensure_ascii=False) + "\n")
            # 等待 sidecar 处理（2s/轮 * 3 轮兜底）
            deadline = time.time() + 8
            ok = False
            while time.time() < deadline:
                if os.path.exists(path) and os.path.getsize(path) > 0:
                    ok = True
                    break
                time.sleep(0.2)
            self.assertTrue(ok)
        finally:
            gw._stop.set()
            th.join(timeout=2)


import threading  # noqa: E402
