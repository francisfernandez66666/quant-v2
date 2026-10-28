#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§CANCEL-STATE（2026-10-28）网关侧撤单状态机单测。

本仓踩过的坑（2026-10-28 600362 事故）：网关把「撤单请求被 QMT 客户端受理」当成
「交易所已撤」推给量仔，于是本地那笔单永远停在「已撤」，实际成交既没有 fills 也
没有持仓——买入列表里没有这只票，而钱已经花出去了。

这批用例锁三件事：
  ① cancel_result ok=True → 只推「撤单中」（不是已撤）；
  ② cancel_result ok=False → 推带 §CANCEL-REJECT 前缀的拒因，允许回退在途态；
  ③ 委托表快照：撤单中 + 仍在途 → 保持「撤单中」；连续 N 轮仍在途 → 显式推撤单被拒；
     撤单中 + 快照终态 → 正常推进终态。
不依赖 Windows/xtquant：直连 Store/Gateway。
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


def new_db_path():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return path


class TestCancelStateMatrix(unittest.TestCase):
    """状态守卫：撤单中的进入/退出路径。"""

    def test_advance_to_cancel_pending(self):
        for cur in ("已报", "部成", "已报待撤", "部成待撤"):
            self.assertTrue(
                store_mod.order_status_advance(cur, store_mod.CANCEL_PENDING_STATUS),
                "%s → 撤单中 应放行" % cur)
        # 已撤是终态：撤单请求改不动它
        self.assertFalse(store_mod.order_status_advance("已撤", store_mod.CANCEL_PENDING_STATUS))

    def test_cancel_pending_to_terminal(self):
        for term in ("已撤", "部撤", "已成", "废单"):
            self.assertTrue(
                store_mod.order_status_advance(store_mod.CANCEL_PENDING_STATUS, term),
                "撤单中 → %s 应放行（交易所确认）" % term)

    def test_cancel_pending_back_to_inflight_requires_reject_event(self):
        # 没有 §CANCEL-REJECT 显式事件：迟到的快照/回报不许把撤单中改回已报
        self.assertFalse(store_mod.order_status_advance(store_mod.CANCEL_PENDING_STATUS, "已报"))
        self.assertFalse(store_mod.order_status_advance(store_mod.CANCEL_PENDING_STATUS, "部成"))
        # 带显式拒因才放行（唯一合法回退口）
        self.assertTrue(store_mod.order_status_advance(
            store_mod.CANCEL_PENDING_STATUS, "已报", store_mod.CANCEL_REJECT_PREFIX + " 收盘集合竞价不接受撤单"))

    def test_rank_ordering(self):
        r = store_mod.order_status_rank
        self.assertGreater(r("已撤"), r(store_mod.CANCEL_PENDING_STATUS))
        self.assertGreater(r(store_mod.CANCEL_PENDING_STATUS), r("部成"))
        self.assertGreater(r("部成"), r("已报"))


class _GatewayCase(unittest.TestCase):
    def setUp(self):
        cfg = {
            "listen": "127.0.0.1:0", "token": "tk", "broker": "mock", "account": "T0001",
            "db": new_db_path(), "report_url": "", "report_token": "", "reconcile_sec": 0,
            "failover_enable": False, "user_id": "uA",
        }
        self.gw = Gateway(cfg)
        self.store = self.gw.store
        self.pushed = []
        self.gw.handler._push = lambda payload: self.pushed.append(payload)

    def tearDown(self):
        try:
            self.gw.stop()
        except Exception:
            pass

    def _today(self):
        return gw_mod._now_cn()[:10]

    def _order_row(self, **kw):
        row = {"order_id": "C1", "code": "600362.SH", "side": "买入", "status": "已报",
               "qty": 400, "filled_qty": 0, "price": 36.56, "remark": "S1",
               "created_at": self._today() + " 14:57:48"}
        row.update(kw)
        return row

    def _snap(self, rows, synced=True):
        return self.gw._apply_orders_snapshot(
            {"type": "orders_snapshot", "orders": rows, "synced": synced,
             "data_ts": gw_mod._now_cn()})

    def _seed_dispatch(self, signal_id="S1", order_id="C1", code="600362.SH", side="买入"):
        """落一条派发项（订单腿），使快照行能按 signal_id 归因。"""
        seq = self.store.dispatch_enqueue_order(
            {"signal_id": signal_id, "code": code, "side": side, "price": 36.56,
             "qty": 400, "created_at": self._today() + " 14:57:48"},
            user_id="uA")
        self.store.dispatch_set_result(seq, {"ok": True, "order_id": order_id, "err": ""})
        return seq

    def _push_cancel_result(self, ok=True, err=""):
        """造一条 cancel 派发项并回 cancel_result（走网关真实路径）。"""
        seq = self.store.dispatch_enqueue_cancel(
            "C1", "C1", signal_id="S1", code="600362.SH", side="买入", user_id="uA")
        status, body = self.gw._do_dispatch_result(
            {"type": "cancel_result", "seq": seq, "ok": ok, "err": err})
        self.assertEqual(status, 200, body)
        return seq


class TestCancelResultSemantics(_GatewayCase):
    """cancel_result：受理 ≠ 确认。"""

    def test_cancel_ok_pushes_cancel_pending_not_cancelled(self):
        self._seed_dispatch()
        self._snap([self._order_row(status="已报")])
        self._push_cancel_result(ok=True)
        row = self.store.order_by_signal("S1")
        self.assertIsNotNone(row)
        self.assertEqual(row["status"], store_mod.CANCEL_PENDING_STATUS,
                         "撤单请求被受理后只推「撤单中」，不得推「已撤」")
        # 推给量仔的事件也是「撤单中」
        order_events = [p for p in self.pushed if p.get("type") == "order"]
        self.assertTrue(order_events)
        self.assertEqual(order_events[-1]["status"], store_mod.CANCEL_PENDING_STATUS)

    def test_cancel_rejected_pushes_explicit_reject_reason(self):
        self._seed_dispatch()
        self._snap([self._order_row(status="已报")])
        self._push_cancel_result(ok=True)
        # 撤单被交易所拒绝（收盘集合竞价）
        seq = self.store.dispatch_enqueue_cancel(
            "C1", "C1", signal_id="S1", code="600362.SH", side="买入", user_id="uA")
        self.gw._do_dispatch_result({
            "type": "cancel_result", "seq": seq, "ok": False, "err": "收盘集合竞价不接受撤单"})
        row = self.store.order_by_signal("S1")
        self.assertEqual(row["status"], "已报",
                         "显式撤单被拒事件允许把撤单中退回在途态")
        order_events = [p for p in self.pushed if p.get("type") == "order"]
        self.assertTrue(order_events[-1]["reason"].startswith(store_mod.CANCEL_REJECT_PREFIX),
                        "撤单被拒必须带 §CANCEL-REJECT 前缀（唯一合法回退钥匙）")


class TestCancelPendingSnapshotConvergence(_GatewayCase):
    """委托表快照：撤单中的收敛/回退。"""

    def test_snapshot_terminal_advances_cancel_pending(self):
        self._seed_dispatch()
        self._snap([self._order_row(status="已报")])
        self._push_cancel_result(ok=True)
        self.assertEqual(self.store.order_by_signal("S1")["status"], store_mod.CANCEL_PENDING_STATUS)
        # 券商委托表给出终态 → 推进
        self._snap([self._order_row(status="已撤")])
        self.assertEqual(self.store.order_by_signal("S1")["status"], "已撤")

    def test_snapshot_still_inflight_keeps_cancel_pending(self):
        """快照仍显示在途 → 保持「撤单中」，不被无声无息改回已报。"""
        self._seed_dispatch()
        self._snap([self._order_row(status="已报")])
        self._push_cancel_result(ok=True)
        limit = int(self.gw.cfg.get("cancel_reject_miss_limit", self.gw.CANCEL_REJECT_LIMIT) or 0)
        for _ in range(max(0, limit - 1)):
            self._snap([self._order_row(status="已报")])
            self.assertEqual(self.store.order_by_signal("S1")["status"], store_mod.CANCEL_PENDING_STATUS,
                             "观察窗内必须保持撤单中")

    def test_snapshot_still_inflight_too_long_emits_reject(self):
        """连续 N 轮仍在途 → 显式推撤单被拒并回退在途态（不能永远卡在撤单中）。"""
        self._seed_dispatch()
        self._snap([self._order_row(status="已报")])
        self._push_cancel_result(ok=True)
        limit = int(self.gw.cfg.get("cancel_reject_miss_limit", self.gw.CANCEL_REJECT_LIMIT) or 0)
        for _ in range(limit):
            self._snap([self._order_row(status="已报")])
        row = self.store.order_by_signal("S1")
        self.assertEqual(row["status"], "已报",
                         "撤单连续未生效后应回退在途态并带拒因")
        order_events = [p for p in self.pushed if p.get("type") == "order"]
        rejects = [p for p in order_events if str(p.get("reason", "")).startswith(store_mod.CANCEL_REJECT_PREFIX)]
        self.assertTrue(rejects, "撤单未生效必须留下显式 §CANCEL-REJECT 事件")


if __name__ == "__main__":
    unittest.main()
