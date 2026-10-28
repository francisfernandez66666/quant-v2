#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§MANUAL-SYNC（2026-10-28）手动委托/撤单/成交回传单测。

覆盖三件事：
  ① 券商 ORDER 表快照 → 手动单以 ext:<委托号> 落账、状态推进、消失收敛；
  ② 我方派发单被手动撤单后，本地行跟着终结（不再永挂已报、不再占额度）；
  ③ 手动成交必须「成交行方向 + 委托表方向」两源一致才入账，否则维持待核对；
  ④ 从未被桥取走的 pending 派发行有超时出口（回写发送失败，解开同 signal 重试）。
不依赖 Windows/xtquant：桥侧用假 ORDER 对象，网关侧直连 Store/handler。
"""
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import gateway as gw_mod  # noqa: E402
import store as store_mod  # noqa: E402
from gateway import Gateway  # noqa: E402
from store import Store  # noqa: E402
import qmt_bridge_strategy as bridge  # noqa: E402


def new_db_path():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return path


class TestOrderStatusGuard(unittest.TestCase):
    """网关侧状态守卫：只升不降 + 两条真实语义例外。"""

    def test_advance_matrix(self):
        self.assertTrue(store_mod.order_status_advance("已报", "部成"))
        self.assertTrue(store_mod.order_status_advance("部成", "已成"))
        self.assertFalse(store_mod.order_status_advance("已成", "已报"))
        self.assertFalse(store_mod.order_status_advance("已撤", "已报"))
        # 等状态放行：网关 upsert 用它决定"是否改状态"，等状态只是刷新价/量（无回退风险）
        self.assertTrue(store_mod.order_status_advance("已报", "已报"))
        self.assertFalse(store_mod.order_status_advance("已报", "未知(255)"))
        # 僵尸「已报」被柜台证伪 → 发送失败（这才是解开同 signal 重放的路径）
        self.assertTrue(store_mod.order_status_advance("已报", "发送失败"))
        # 迟到的真实回报证明券商确有此单 → 从发送失败回补
        self.assertTrue(store_mod.order_status_advance("发送失败", "已报"))
        self.assertTrue(store_mod.order_status_advance("发送失败", "部成"))
        # 占位/待核对是瞬态，任何回报都能推进
        self.assertTrue(store_mod.order_status_advance("pending", "已报"))
        self.assertTrue(store_mod.order_status_advance(store_mod.UNRESOLVED_STATUS, "已报"))


class _GatewayCase(unittest.TestCase):
    def setUp(self):
        cfg = {
            "listen": "127.0.0.1:0", "token": "tk", "broker": "mock", "account": "T0001",
            "db": new_db_path(), "report_url": "", "report_token": "", "reconcile_sec": 0,
            "failover_enable": False, "user_id": "uA",
        }
        self.gw = Gateway(cfg)
        self.store = self.gw.store

    def tearDown(self):
        try:
            self.gw.stop()
        except Exception:
            pass

    def _today(self):
        return gw_mod._now_cn()[:10]

    def _order_row(self, **kw):
        row = {"order_id": "M1", "code": "600000.SH", "side": "买入", "status": "已报",
               "qty": 100, "filled_qty": 0, "price": 10.0, "remark": "",
               "created_at": self._today() + " 09:31:00"}
        row.update(kw)
        return row

    def _snap(self, rows, synced=True):
        return self.gw._apply_orders_snapshot(
            {"type": "orders_snapshot", "orders": rows, "synced": synced,
             "data_ts": gw_mod._now_cn()})


class TestManualOrderSnapshot(_GatewayCase):
    """手动委托：首次出现落 ext 行，状态推进，手动撤单收敛为已撤。"""

    def test_manual_order_adopted_and_cancelled(self):
        self._snap([self._order_row(order_id="M1", status="已报")])
        row = self.store.order_by_signal("ext:M1")
        self.assertIsNotNone(row, "手动单应以 ext:<委托号> 落账")
        self.assertEqual(row["status"], "已报")
        self.assertEqual(row["code"], "600000.SH")
        self.assertEqual(row["side"], "买入")
        # 手动撤单：委托表状态变已撤 → 本地跟着终结
        self._snap([self._order_row(order_id="M1", status="已撤")])
        self.assertEqual(self.store.order_by_signal("ext:M1")["status"], "已撤")

    def test_compact_counter_timestamp_normalized(self):
        """柜台 8 位紧凑日期必须归一成 YYYY-MM-DD HH:MM:SS——否则量仔「当日」过滤永远看不到手动单。

        §FIX-20261028：旧用例把日期写死成 20261028，而断言里的"今天"取的是运行时北京日，
        跨过零点（或隔天跑）就必红——时间敏感的采集器缺陷，不是被测逻辑坏。
        现在两边都从 `self._today()` 派生，语义不变、跨日稳定。
        """
        compact = self._today().replace("-", "") + " 093100"
        self._snap([self._order_row(order_id="M7", created_at=compact)])
        row = self.store.order_by_signal("ext:M7")
        self.assertIsNotNone(row)
        self.assertEqual(row["created_at"], self._today() + " 09:31:00")
        self.assertTrue(row["created_at"].startswith(self._today()))

    def test_unsynced_and_empty_snapshot_do_nothing(self):
        self.store.claim_order({"signal_id": "S1", "code": "600000.SH", "side": "买入",
                                "price": 10, "qty": 100, "user_id": "uA"})
        self.store.upsert_order({"order_id": "888", "signal_id": "S1", "code": "600000.SH",
                                 "side": "买入", "status": "已报", "price": 10, "qty": 100,
                                 "created_at": self._today() + " 09:31:00", "user_id": "uA"})
        # 查询失败 / 空快照：绝不做消失判定（否则会误杀真实在途单）
        for _ in range(10):
            self._snap([], synced=False)
            self._snap([])
        self.assertEqual(self.store.order_by_signal("S1")["status"], "已报")

    def test_missing_order_closed_only_after_limit(self):
        self.store.upsert_order({"order_id": "999", "signal_id": "S2", "code": "600000.SH",
                                 "side": "买入", "status": "已报", "price": 10, "qty": 100,
                                 "created_at": self._today() + " 09:31:00", "user_id": "uA"})
        # 快照非空但始终没有 999：连续缺失到阈值才落终态
        other = self._order_row(order_id="M9")
        for i in range(self.gw.ORDER_MISS_LIMIT - 1):
            self._snap([other])
            self.assertEqual(self.store.order_by_signal("S2")["status"], "已报",
                             "第 %d 次缺失不得提前收敛" % (i + 1))
        self._snap([other])
        self.assertEqual(self.store.order_by_signal("S2")["status"], "已撤")

    def test_dispatch_order_cancelled_externally(self):
        """我方派发单被手动撤掉：委托表按 remark 归因（截断 wire 形态）推进为已撤。"""
        seq = self.store.dispatch_enqueue_order(
            {"signal_id": "buy:600000:fac_1:20261028", "code": "600000.SH",
             "side": "买入", "price": 10, "qty": 100, "created_at": self._today()},
            user_id="uA")
        self.store.dispatch_set_result(seq, {"ok": True, "order_id": "777"})
        self.store.upsert_order({"order_id": "777", "signal_id": "buy:600000:fac_1:20261028",
                                 "code": "600000.SH", "side": "买入", "status": "已报",
                                 "price": 10, "qty": 100,
                                 "created_at": self._today() + " 09:31:00", "user_id": "uA"})
        self._snap([self._order_row(order_id="777", status="已撤",
                                    remark="buy:600000:fac_1:2026102")])
        row = self.store.order_by_signal("buy:600000:fac_1:20261028")
        self.assertEqual(row["status"], "已撤")

    def test_stale_pending_dispatch_reaped(self):
        """从未被桥取走的 pending 派发行超时后回写发送失败（解开同 signal 重试死锁）。"""
        self.store.claim_order({"signal_id": "S3", "code": "600000.SH", "side": "买入",
                                "price": 10, "qty": 100, "user_id": "uA"})
        seq = self.store.dispatch_enqueue_order(
            {"signal_id": "S3", "code": "600000.SH", "side": "买入", "price": 10, "qty": 100},
            user_id="uA")
        self.gw.cfg["dispatch_pending_reap_sec"] = 600
        with self.store._lock:
            self.store._conn.execute(
                "UPDATE dispatch SET created_at = ? WHERE seq = ?",
                ("2020-01-01T00:00:00+08:00", seq))
            self.store._conn.commit()
        n = self.gw._reap_stale_pending_dispatch("测试")
        self.assertEqual(n, 1)
        self.assertEqual(self.store.dispatch_get(seq)["status"], "done")
        self.assertEqual(self.store.order_by_signal("S3")["status"], "发送失败")


class TestUnconfirmedConvergence(_GatewayCase):
    """§M-3 未确认单收敛：柜台始终没有这笔单 → 发送失败（可重试）；
    若柜台存在同形无备注行（无法证伪）则保留，避免把真实在途单判死。"""

    def _seed_unconfirmed(self):
        self.gw.cfg["manual_sync_unconfirmed_grace_sec"] = 60
        self.store.upsert_order({"order_id": "seq:5", "signal_id": "S5", "code": "600000.SH",
                                 "side": "买入", "status": "已报", "price": 10, "qty": 100,
                                 "created_at": self._today() + " 09:31:00", "user_id": "uA"})
        self.gw._order_unconfirmed["S5"] = gw_mod.time.time() - 120

    def test_unconfirmed_becomes_send_failed(self):
        self._seed_unconfirmed()
        self._snap([self._order_row(order_id="M9", code="000001.SZ")])
        self.assertEqual(self.store.order_by_signal("S5")["status"], "发送失败")

    def test_unconfirmed_kept_when_unattributed_lookalike(self):
        self._seed_unconfirmed()
        # 同代码+同方向+同数量且 remark 为空：无法证伪，不许判死
        self._snap([self._order_row(order_id="M9", remark="", code="600000.SH", side="买入",
                                    qty=100, status="已报")])
        self.assertEqual(self.store.order_by_signal("S5")["status"], "已报")


class TestManualFill(_GatewayCase):
    """手动成交：两源方向一致才入账（ext 键），不一致保持待核对。"""

    def _snap_manual(self, side):
        self._snap([self._order_row(order_id="M2", status="已成", side=side, qty=100,
                                    filled_qty=100)])

    def test_manual_fill_booked_when_sides_agree(self):
        self._snap_manual("买入")
        self.gw._apply_trade({"type": "trade", "order_id": "M2", "trade_id": "T2",
                              "code": "600000.SH", "side": "买入", "price": 10.0, "qty": 100,
                              "amount": 1000.0, "traded_at": self._today() + "T09:40:00+08:00"})
        fills = self.store.list_fills() if hasattr(self.store, "list_fills") else None
        if fills is None:  # 兜底：直接查表
            with self.store._lock:
                fills = [dict(r) for r in self.store._conn.execute("SELECT * FROM fills")]
        self.assertEqual(len(fills), 1)
        self.assertEqual(fills[0]["side"], "买入")
        self.assertEqual(fills[0]["signal_id"], "ext:M2")

    def test_manual_fill_unverified_when_sides_disagree(self):
        self._snap_manual("卖出")
        self.gw._apply_trade({"type": "trade", "order_id": "M2", "trade_id": "T3",
                              "code": "600000.SH", "side": "买入", "price": 10.0, "qty": 100,
                              "amount": 1000.0, "traded_at": self._today() + "T09:41:00+08:00"})
        with self.store._lock:
            fills = [dict(r) for r in self.store._conn.execute("SELECT * FROM fills")]
        self.assertEqual(len(fills), 1)
        self.assertEqual(fills[0]["side"], store_mod.UNRESOLVED_STATUS)
        self.assertNotEqual(fills[0]["signal_id"], "ext:M2")


class _FakeOrder:
    """一个最小 ORDER 行对象（字段名按桥侧 gtdd 实测族给出）。"""

    def __init__(self, **kw):
        self.m_strOrderSysID = kw.get("oid", "12345")
        self.m_strInstrumentID = kw.get("code", "600000")
        self.m_strExchangeID = kw.get("ex", "SH")
        self.m_nOrderStatus = kw.get("status", 50)
        self.m_nOrderVolume = kw.get("qty", 100)
        self.m_nVolumeTraded = kw.get("filled", 0)
        self.m_dOrderPrice = kw.get("price", 10.0)
        self.m_nOrderType = kw.get("ot", 23)
        self.m_strRemark = kw.get("remark", "")
        self.m_strInsertDate = kw.get("date", "20261028")
        self.m_strInsertTime = kw.get("time", "09:31:00")


class TestBridgeOrderSnapshot(unittest.TestCase):
    """桥侧 ORDER 表归一化：状态码、方向、成交号字段族。"""

    def test_order_status_map(self):
        self.assertEqual(bridge._order_status(50), "已报")
        self.assertEqual(bridge._order_status(55), "部成")
        self.assertEqual(bridge._order_status(56), "已成")
        self.assertEqual(bridge._order_status(54), "已撤")
        self.assertTrue(bridge._order_status(255).startswith("未知"))
        self.assertTrue(bridge._order_status(None).startswith("未知"))

    def test_embed_orders_norm(self):
        ad = bridge._XtOps(account="A", dry_run=False, xt_path="", session_id=2)
        ad._embed_ok = True
        ad._acct_type_ok = "STOCK"
        ad._xtc = None
        rows = [_FakeOrder(oid="12345", status=50),
                _FakeOrder(oid="12346", status=56, filled=100, ot=24, code="000001", ex="SZ")]
        ad._gtdd = lambda table: rows if table == "ORDER" else []
        out = ad.embed_orders_norm()
        self.assertEqual(len(out), 2)
        self.assertEqual(out[0]["order_id"], "12345")
        self.assertEqual(out[0]["code"], "600000.SH")
        self.assertEqual(out[0]["status"], "已报")
        self.assertEqual(out[0]["side"], "买入")
        self.assertEqual(out[0]["created_at"], "20261028 09:31:00")
        self.assertEqual(out[1]["code"], "000001.SZ")
        self.assertEqual(out[1]["status"], "已成")
        self.assertEqual(out[1]["side"], "卖出")

    def test_query_orders_reports_unsynced_on_error(self):
        ad = bridge._XtOps(account="A", dry_run=False, xt_path="", session_id=2)
        ad._embed_ok = False
        ad._import_tried = True
        ad._trader = None  # ensure() 会抛 RuntimeError -> query_orders 返回 ([], False)
        rows, synced = ad.query_orders()
        self.assertEqual(rows, [])
        self.assertFalse(synced)


if __name__ == "__main__":
    unittest.main()
