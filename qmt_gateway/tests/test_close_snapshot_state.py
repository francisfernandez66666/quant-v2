#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.tests.test_close_snapshot_state — §CLOSE-RECON-STATE 收盘快照读面行为锁。

要钉住的一件事（2026-11-04 owner 令："收盘快照 /state 盘后回 broker 要做"）：
11-05 桥断之后，收盘快照是唯一的手工对账基线，而它恰恰只在**盘后 broker 已断开**时
才需要读。旧路由把 GET /state 排在连接闸之后，盘后一律 503 "broker not connected"，
等于把这条基线连同观测面一起关掉。本文件锁的是改完之后四件事：

  1. 断连时 /state 仍回 200，且显式 connected=false / positions_trusted=false
     ——空持仓是"没读到"，不是"没有持仓"（§MANUAL-SYNC 空快照不做消失判定同族）；
  2. close_snapshot 五键恒在位，day 为空如实空串，**绝不折成"今日无成交"**；
  3. /state 与 /admin/status 共用同一个装配点（改一面必红）；
  4. 只读端点前移**不放行资本动作**：断连时 POST /order 仍被 503 挡在闸后。

测试一律直接调 Gateway.handle（纯路由层），不起 HTTP 服务、不碰真柜台、不落任何账本文件。
English: behaviour locks for the read-only /state face after a broker disconnect; no
network, no real broker, no ledger file is touched.
"""

import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from gateway import Gateway  # noqa: E402


class _DisconnectedBroker(object):
    """断连桩：query_positions 一旦被调用就炸，用来证明断连时根本没去问柜台。"""

    def is_connected(self):
        return False

    def query_positions(self):
        raise AssertionError("断连时不应查询柜台持仓")


class _ConnectedBroker(object):
    """连接桩：只给一份最小持仓，验证连接态既有字段逐字不变。"""

    def is_connected(self):
        return True

    def query_positions(self):
        return [{"ts_code": "600519.SH", "qty": 100}]


def _new_gateway():
    """构造一台不启动后台线程的网关，专供路由层断言。"""
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
    gw = Gateway(cfg)
    gw._dispose_db = dbpath  # 仅供 tearDown 清理，不参与任何判据
    return gw


class TestCloseSnapshotState(unittest.TestCase):
    def tearDown(self):
        """删掉本用例自己建的那份临时库路径，不碰任何生产/测试数据目录。"""
        path = getattr(self, "gw", None) and getattr(self.gw, "_dispose_db", "")
        if path and os.path.exists(path):
            os.unlink(path)

    def test_state_answers_when_broker_disconnected(self):
        """盘后断连：/state 必须 200 + 空快照显式不可信，不许再是 503。"""
        self.gw = _new_gateway()
        self.gw.active_broker = _DisconnectedBroker()
        status, body = self.gw.handle("GET", "/state", None, None)
        self.assertEqual(status, 200)
        self.assertFalse(body["connected"])
        self.assertEqual(body["positions"], [])
        self.assertFalse(body["positions_trusted"])
        # 既有四键一个不少（契约是加法，不是改写）
        for key in ("account", "orders", "broker_mode", "orders_snapshot_at", "orders_snapshot_n"):
            self.assertIn(key, body)

    def test_close_snapshot_is_always_present_and_never_pretends_zero(self):
        """没收到过快照：五键在册且 day/at 为空串，不许折成"今日无成交"。"""
        self.gw = _new_gateway()
        status, body = self.gw.handle("GET", "/state", None, None)
        self.assertEqual(status, 200)
        snap = body["close_snapshot"]
        self.assertEqual(set(snap), {"day", "at", "orders_n", "positions_n", "trades_n"})
        self.assertEqual(snap["day"], "")
        self.assertEqual(snap["at"], "")
        self.assertEqual(snap["orders_n"], 0)

    def test_state_and_admin_status_share_one_assembler(self):
        """两个读面同源：喂一份收盘快照，/state 与 /admin/status 必须逐值相等。"""
        self.gw = _new_gateway()
        self.gw._close_snap = {
            "at": "2026-11-04 15:01:03", "day": "2026-11-04", "ts": 1.0,
            "n": 40, "positions": 3, "trades": 2,
        }
        _, state = self.gw.handle("GET", "/state", None, None)
        _, admin = self.gw.handle("GET", "/admin/status", None, None)
        self.assertEqual(state["close_snapshot"], admin["close_snapshot"])
        self.assertEqual(state["close_snapshot"]["day"], "2026-11-04")
        self.assertEqual(state["close_snapshot"]["orders_n"], 40)
        self.assertEqual(state["close_snapshot"]["positions_n"], 3)
        self.assertEqual(state["close_snapshot"]["trades_n"], 2)

    def test_capital_actions_still_gated_after_read_only_move(self):
        """只读端点前移不许变成放行口子：断连时 /order 仍 503 broker not connected。"""
        self.gw = _new_gateway()
        self.gw.active_broker = _DisconnectedBroker()
        self.gw._contract_ok = True
        status, body = self.gw.handle("POST", "/order", {"signal_id": "S1"}, None)
        self.assertEqual(status, 503)
        self.assertEqual(body.get("err"), "broker not connected")

    def test_connected_state_keeps_existing_shape_and_marks_trusted(self):
        """连接态：既有字段不变，只多出两枚纯增量读数。"""
        self.gw = _new_gateway()
        self.gw.active_broker = _ConnectedBroker()
        status, body = self.gw.handle("GET", "/state", None, None)
        self.assertEqual(status, 200)
        self.assertTrue(body["connected"])
        self.assertTrue(body["positions_trusted"])
        self.assertEqual(body["positions"][0]["ts_code"], "600519.SH")
        self.assertIn("close_snapshot", body)


if __name__ == "__main__":
    unittest.main()
