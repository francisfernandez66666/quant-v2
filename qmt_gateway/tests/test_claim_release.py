#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway 单测（unittest，零第三方依赖）：§CLAIMRELEASE（N-8 2026-09-22 晚批）。

锁住「网关占位释放不再拆掉唯一幂等防线」这条资金安全不变式，因果链逐环对应到用例：
  ① claim 落 pending:<sid> 占位（orders.signal_id UNIQUE）；
  ② 桥通道 QueuedBroker.place_order **入队即 return True**（订单已不可撤回地排队）；
  ③ 随后 ids.settle 抛错（DB busy / 装配 KeyError）；
  ④ 旧实现 finally 无条件 _release_order_claim → 占位被删 = 唯一幂等防线被拆；
  ⑤ 调用方（首尔 Go）有限重试再次 claim 成功；
  ⑥ dispatch 表当时没有 signal_id 唯一约束 → 同信号第二笔入队 = 双卖/双买。
现在的行为：未交给通道才删占位（§M-2 治死锁的原语义保留）；已交给通道则占位保留并转
第三态「待核对」，同 sid 一律 409 + error 告警，超时清理不许把它当普通 pending 删掉，
收敛出口＝回报到达（upsert_order）/ POST /admin/order-confirm 人工确认；
dispatch 侧另有部分唯一索引 idx_dispatch_signal_active 做 DB 层纵深防线。
"""
import inspect
import json
import logging
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from gateway import Gateway  # noqa: E402
from store import Store, UNRESOLVED_STATUS, DispatchDuplicateSignal, _now_cn  # noqa: E402


def new_db_path():
    """创建并删除临时 DB 路径，得到"尚不存在的新库"（Store/Gateway 首次建表用）。"""
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return path


class _LogCaptureMixin(unittest.TestCase):
    """让 assertLogs 在本文件里真的能抓到日志（脱离本文件的全局静音例外）。

    坑源（真实踩过，必须留字）：tests/test_xt_mapping.py 与
    tests/test_channel_position_fields.py 在 **import 期**执行
    `logging.disable(logging.CRITICAL)` 来静音自己的告警，而 pytest 会在同一个进程里
    收集全部测试模块 → 这个全局开关跑到本文件时仍然生效，stdlib 的 assertLogs 又不会
    解除 logging.disable（CPython 的已知行为），于是"网关确实告了警但断言说没有日志"，
    直接把这条资金安全不变式的断言变成假阴性。做法：用例期间临时解除、结束**还原原值**，
    既不破坏其他模块原有的静音意图，也不依赖它们的执行顺序。
    """

    def setUp(self):
        self._prev_log_disable = logging.Logger.manager.disable
        logging.disable(logging.NOTSET)
        self.addCleanup(logging.disable, self._prev_log_disable)


class TestClaimReleaseThirdState(_LogCaptureMixin):
    """§CLAIMRELEASE 主行为链：注入 ids.settle 抛错 → 占位转第三态、二次下单不再入队、有告警。

    告警断言一律用 stdlib 的 `self.assertLogs("qmt_gateway", level="ERROR")`：网关的资金安全
    告警出口就是这个名字的 logger（见 gateway.log / store.log），ERROR 级是"要人打钱去看"的档。
    """

    def _gw(self):
        """queued 通道网关（不启 HTTP/后台线程）：只有桥派发路径能直接观测"是否二次入队"。"""
        gw = Gateway({"listen": "127.0.0.1:0", "token": "tk", "broker": "queued",
                      "account": "Q1", "db": new_db_path(), "report_url": "",
                      "reconcile_sec": 0, "user_id": "u1",
                      "pending_stale_sec": 600, "pending_unresolved_alert_sec": 3600})
        self.addCleanup(gw.stop)
        return gw

    def _body(self, sid, side="卖出"):
        """600519 一手的卖出单：N-8 的原始后果是"双卖"，用例方向与事故一致。"""
        return {"signal_id": sid, "code": "600519.SH", "side": side, "price_type": "limit",
                "price": 1500, "qty": 100, "amount": 150000, "strategy_type": "test",
                "created_at": _now_cn()}

    def _dispatch_rows(self, gw, sid):
        """同 sid 的 order 类派发行（直接查库，绕开 dispatch_pending 的状态副作用）。"""
        with gw.store._lock:
            rows = gw.store._conn.execute(
                "SELECT * FROM dispatch WHERE kind='order' AND signal_id = ?", (sid,)).fetchall()
        return [dict(r) for r in rows]

    def test_settle_failure_keeps_claim_and_blocks_second_enqueue(self):
        """核心用例：settle 抛错 → 占位保留为「待核对」+ 告警；第二次 /order 回 409 且不入队。"""
        gw = self._gw()
        original = gw.ids.settle
        # ③ 注入结算失败（DB busy / 装配 KeyError 同构）：place_order 已成功，只有 settle 崩
        gw.ids.settle = lambda order: (_ for _ in ()).throw(RuntimeError("database is locked"))
        try:
            with self.assertLogs("qmt_gateway", level="ERROR") as first_call:
                s1, b1 = gw._do_order(self._body("N8-SELL"))
            self.assertEqual(s1, 500, "结算失败必须显式回 500（不得吞成 200 假装成功）")
            self.assertFalse(b1["ok"])
        finally:
            gw.ids.settle = original

        # ④ 不再拆防线：占位行仍在，且处于第三态
        row = gw.store.order_by_signal("N8-SELL")
        self.assertIsNotNone(row, "§CLAIMRELEASE：已交给通道的占位被删掉了（N-8 原缺陷现场）")
        self.assertEqual(row["status"], UNRESOLVED_STATUS,
                         "占位必须转第三态「待核对」，而不是停在 pending 或被判成正常态")
        self.assertTrue(str(row["order_id"]).startswith("pending:"),
                        "order_id 仍是占位引用，claim 的 UNIQUE 才是实际生效的防线")
        # ②③ 事实留痕：确实已经入队一笔（这是"第二次不得再入队"断言的分母）
        self.assertEqual(len(self._dispatch_rows(gw, "N8-SELL")), 1,
                         "place_order 成功那轮应当已写入 1 条派发单")
        # 告警出口：error 级日志、写清 reason 与「当时是否已入队」（旧实现两种情况同句，取证为零）
        text = "\n".join(first_call.output)
        self.assertIn("§CLAIMRELEASE", text, "第三态转换未走 error 告警出口")
        self.assertIn("sent_to_broker=True", text, "告警未写明「当时已入队」，事后无法取证")
        self.assertIn("database is locked", text, "告警未带 reason（结算失败原因丢失）")

        # ⑤⑥ 调用方重试：claim 再抢不到 → 409，且**绝不产生第二笔派发单**
        with self.assertLogs("qmt_gateway", level="ERROR") as second_call:
            s2, b2 = gw._do_order(self._body("N8-SELL"))
        self.assertEqual(s2, 409, "第三态信号必须一律拒绝（宁可拒单也不双卖）")
        self.assertFalse(b2["ok"])
        self.assertIn("unresolved", b2["err"])
        self.assertIn("结果未知", "\n".join(second_call.output), "重试拒绝未留告警痕迹")
        self.assertEqual(len(self._dispatch_rows(gw, "N8-SELL")), 1,
                         "§CLAIMRELEASE：重试又入队了一笔 → 双卖现场复现")
        self.assertEqual(len(gw.store.list_orders()), 1, "重试不得新增委托行")

    def test_unresolved_state_survives_stale_sweeps(self):
        """超时清理只认 status='pending'：超龄「待核对」行必须保留并持续告警（不许被绕过）。"""
        gw = self._gw()
        old = "2020-01-01T09:30:00+08:00"  # 远超 600s 的占位龄：若是普通 pending 必被巡检删掉
        self.assertTrue(gw.store.claim_order({
            "signal_id": "N8-OLD", "code": "600519.SH", "side": "卖出",
            "price": 1500, "qty": 100, "created_at": old})[0])
        self.assertEqual(gw.store.mark_pending_unresolved("N8-OLD"), 1)
        # 巡检：一条都不删（第三态不是普通 pending），但必须 error 告警
        with self.assertLogs("qmt_gateway", level="ERROR") as cap:
            n = gw._sweep_stale_pending("单测")
        self.assertEqual(n, 0, "§CLAIMRELEASE：第三态被当成普通 pending 清掉了（绕过硬防线）")
        self.assertIsNotNone(gw.store.order_by_signal("N8-OLD"))
        text = "\n".join(r.getMessage() for r in cap.records)
        self.assertIn("待核对", text, "超龄第三态未走告警出口")
        self.assertIn("3600", text, "告警未带独立阈值（pending_unresolved_alert_sec）")
        # 去重：60s 一轮的巡检不能对同一 signal_id 刷屏
        self.assertEqual(gw._alert_unresolved_pending("单测"), 0, "重复告警未去重")
        # 对照：同库里的普通超龄 pending 仍然照常解锁（§M-2 治死锁语义没被削弱）
        self.assertTrue(gw.store.claim_order({
            "signal_id": "N8-PLAIN-OLD", "code": "600519.SH", "side": "卖出",
            "price": 1500, "qty": 100, "created_at": old})[0])
        self.assertEqual(gw._sweep_stale_pending("单测"), 1)
        self.assertIsNone(gw.store.order_by_signal("N8-PLAIN-OLD"))
        self.assertIsNotNone(gw.store.order_by_signal("N8-OLD"))

    def test_report_arrival_converges_third_state(self):
        """收敛路径①（无需人工）：回报到达经 upsert_order 把第三态推进为正常终态。"""
        gw = self._gw()
        gw.store.claim_order({"signal_id": "N8-CONV", "code": "600519.SH", "side": "卖出",
                              "price": 1500, "qty": 100, "created_at": _now_cn()})
        gw.store.mark_pending_unresolved("N8-CONV")
        gw.handler.on_order({"order_id": "88001", "signal_id": "N8-CONV", "code": "600519.SH",
                             "side": "卖出", "status": "已报", "price": 1500, "qty": 100,
                             "created_at": _now_cn()})
        row = gw.store.order_by_signal("N8-CONV")
        self.assertEqual(row["status"], "已报", "回报到达后第三态必须自然收敛")
        self.assertEqual(gw.store.list_unresolved_pending(), [])
        # 收敛后同 sid 再下单：走幂等返回（200 + 原委托号），同样不会二次入队
        s, b = gw._do_order(self._body("N8-CONV"))
        self.assertEqual(s, 200)
        self.assertTrue(b["ok"])
        self.assertEqual(b["order_id"], "88001")
        self.assertEqual(len(self._dispatch_rows(gw, "N8-CONV")), 0)

    def test_admin_confirm_releases_third_state_for_ops(self):
        """收敛路径②（人工确认）：/admin/order-confirm 只认第三态行，确认后该信号可重新下单。"""
        gw = self._gw()
        gw.store.claim_order({"signal_id": "N8-OPS", "code": "600519.SH", "side": "卖出",
                              "price": 1500, "qty": 100, "created_at": _now_cn()})
        gw.store.mark_pending_unresolved("N8-OPS")
        # 误传/非第三态一律拒绝（正常委托行与普通 pending 占位都不许被这个端点顺手删掉）
        gw.store.claim_order({"signal_id": "N8-PLAIN", "code": "600519.SH", "side": "卖出",
                              "price": 1500, "qty": 100, "created_at": _now_cn()})
        s, b = gw._do_admin_order_confirm({"signal_id": "N8-PLAIN", "decision": "released"})
        self.assertEqual(s, 409, b)
        self.assertIsNotNone(gw.store.order_by_signal("N8-PLAIN"))
        # 确认柜台无此单 → 删第三态行、解锁
        s2, b2 = gw.handle("POST", "/admin/order-confirm",
                           {"signal_id": "N8-OPS", "decision": "released"}, None)
        self.assertEqual(s2, 200, b2)
        self.assertIsNone(gw.store.order_by_signal("N8-OPS"))
        s3, b3 = gw._do_order(self._body("N8-OPS"))
        self.assertEqual(s3, 200, "人工确认后仍不可受理：该信号被第三态永久锁死（回到 §M-2 死锁）")
        self.assertTrue(b3["ok"])
        self.assertEqual(len(self._dispatch_rows(gw, "N8-OPS")), 1)

    def test_admin_status_exposes_unresolved_and_guard(self):
        """/admin/status 观察位：待核对清单 + 派发纵深索引是否在位（运维一眼可见）。"""
        gw = self._gw()
        gw.store.claim_order({"signal_id": "N8-VIS", "code": "600519.SH", "side": "卖出",
                              "price": 1500, "qty": 100, "created_at": _now_cn()})
        gw.store.mark_pending_unresolved("N8-VIS")
        s, payload = gw.handle("GET", "/admin/status", None, None)
        self.assertEqual(s, 200)
        self.assertEqual(payload["unresolved_count"], 1)
        self.assertEqual(payload["unresolved_orders"][0]["signal_id"], "N8-VIS")
        self.assertTrue(payload["dispatch_signal_guard"],
                        "新建库必须带上 dispatch 部分唯一索引（纵深防线）")


class TestDispatchSignalIndex(_LogCaptureMixin):
    """§CLAIMRELEASE 纵深防线：dispatch 表同 signal_id 的在途 order 行在 DB 层唯一。"""

    def setUp(self):
        # 必须显式上溯 _LogCaptureMixin：本类自建 setUp 会遮蔽 mixin 的临时解除
        # `logging.disable(CRITICAL)`，一遮蔽，"存量重复行必须 loud log"这条资金安全
        # 不变式断言就随执行顺序变成假阴性（其他模块 import 期的全局静音仍在生效）。
        super().setUp()
        self.store = Store(new_db_path())

    def tearDown(self):
        self.store.close()

    def _enqueue(self, sid, side="卖出"):
        return self.store.dispatch_enqueue_order(
            {"signal_id": sid, "code": "600519.SH", "side": side, "price_type": "limit",
             "price": 1500, "qty": 100, "created_at": _now_cn()}, user_id="u1")

    def test_partial_unique_index_blocks_second_enqueue(self):
        """直接 enqueue 两次同 sid → 第二次被拒（可识别异常），且库里只有一行。"""
        self.assertTrue(self.store.dispatch_signal_guard, "新库未建立部分唯一索引")
        seq = self._enqueue("DUP-1")
        self.assertTrue(seq.startswith("seq:"))
        with self.assertRaises(DispatchDuplicateSignal) as cm:
            self._enqueue("DUP-1")
        self.assertIn("DUP-1", str(cm.exception))
        with self.store._lock:
            n = self.store._conn.execute(
                "SELECT COUNT(*) AS c FROM dispatch WHERE signal_id='DUP-1'").fetchone()["c"]
        self.assertEqual(n, 1, "重复入队拦不住 → 双卖门仍在")

    def test_index_predicate_is_inflight_inclusive(self):
        """谓词必须含 inflight：桥取走后（status=inflight）同 sid 仍不得二次入队。

        审计原文给的谓词是 status IN ('pending','已报')，而 dispatch.status 的取值集是
        pending|inflight|done（'已报' 属 orders 侧字面量）——只锁 pending 会漏掉最常见的
        "已被桥取走"这一格，等于没锁。本用例把这个差别钉住。
        """
        self._enqueue("DUP-2")
        taken = self.store.dispatch_pending(limit=5)
        self.assertEqual([r["signal_id"] for r in taken], ["DUP-2"])
        self.assertEqual(self.store.dispatch_get(taken[0]["seq"])["status"], "inflight")
        with self.assertRaises(DispatchDuplicateSignal):
            self._enqueue("DUP-2")

    def test_index_scope_does_not_block_others(self):
        """索引范围不过宽：不同 sid、撤单行（kind=cancel）、diag 行、已结算后的新信号都不受影响。"""
        seq = self._enqueue("DUP-3")
        self._enqueue("DUP-4")                      # 不同 signal_id
        self.store.dispatch_set_result(seq, {"ok": True, "order_id": "9001", "err": ""})
        cancel = self.store.dispatch_enqueue_cancel(seq, "9001", signal_id="DUP-3",
                                                    code="600519.SH", side="卖出")
        self.assertTrue(cancel.startswith("seq:"))
        diag = self.store.dispatch_enqueue_diag("DUP-3")  # kind=diag 同 sid 也允许
        self.assertTrue(diag.startswith("seq:"))
        pend = self.store.dispatch_pending(limit=10)
        self.assertEqual(sorted(r["kind"] for r in pend), ["cancel", "diag", "order"],
                         "非在途 order 行的同 sid 记录不该被索引拦住：" + str(pend))

    def test_legacy_duplicate_rows_do_not_break_startup(self):
        """老库里已有同 sid 两条在途行时：建索引失败必须 loud log + 降级，绝不炸掉网关。"""
        # 先删掉索引、再绕过 SQL 判重塞两条在途行（模拟 N-8 事故遗留库 / 旁路写入）
        with self.store._lock:
            self.store._conn.execute("DROP INDEX IF EXISTS idx_dispatch_signal_active")
            for i in (1, 2):
                self.store._conn.execute(
                    "INSERT INTO dispatch(seq, signal_id, kind, code, side, status, created_at) "
                    "VALUES(?, 'LEGACY-1', 'order', '600519.SH', '卖出', 'pending', ?)",
                    ("seq:leg%d" % i, _now_cn()))
            self.store._conn.commit()
        # 断言捕获挂在 root 上：store 模块在本仓存在 `store` 与 `qmt_gateway.store` 两个导入名
        # （取决于用例经 conftest 的 sys.path 还是包路径进来），两个 logger 名字不同、父级也不同，
        # 按名字捕获会让用例随执行顺序时红时绿。§CLAIMRELEASE 的 loud log 只要求「必有一条 ERROR」。
        box = self.assertLogs(level="ERROR")
        with box as cap:
            reopened = Store(self.store.path)  # 重开同一个库 = 走迁移路径（模拟网关重启）
            logged = "\n".join(cap.output)
        self.assertFalse(reopened.dispatch_signal_guard,
                         "存量重复行时索引建不起来，必须如实报 False 而不是假装已生效")
        self.assertIn("唯一索引", logged)
        # 索引缺失不影响判定：入队语句本身是"带 NOT EXISTS 的条件写入"（DB 层，非进程内标志位）
        with self.assertRaises(DispatchDuplicateSignal):
            reopened.dispatch_enqueue_order({"signal_id": "LEGACY-1", "code": "600519.SH",
                                             "side": "卖出"})
        with reopened._lock:
            n = reopened._conn.execute(
                "SELECT COUNT(*) AS c FROM dispatch WHERE signal_id='LEGACY-1'").fetchone()["c"]
        self.assertEqual(n, 2, "条件写入必须一行都不落")
        reopened.close()


class TestClaimReleaseWiring(unittest.TestCase):
    """接线与负向锁：释放点必须显式携带 sent_to_broker（漏传即 TypeError）。"""

    def test_release_call_site_carries_flag(self):
        """源码级负向锁：_do_order 的 finally 释放点必须带 sent_to_broker=（§20260922 负向锁）。"""
        from gateway import Gateway as G
        src = inspect.getsource(G._do_order)
        self.assertIn("sent_to_broker=sent_to_broker", src,
                      "释放点未显式传「是否已交给通道」→ 又一扇双卖门")
        self.assertIn("_release_order_claim(signal_id, why, sent_to_broker=", src)

    def test_bare_call_raises(self):
        """裸调（不传 sent_to_broker）必须 TypeError：默认值会让新调用点悄悄退回旧行为。"""
        from gateway import Gateway as G
        with self.assertRaises(TypeError):
            G._release_order_claim("sid-x", "why")
        sig = inspect.signature(G._release_order_claim)
        self.assertIs(sig.parameters["sent_to_broker"].kind, inspect.Parameter.KEYWORD_ONLY)
        self.assertIs(sig.parameters["sent_to_broker"].default, inspect.Parameter.empty)

    def test_timeout_cleanup_never_touches_third_state(self):
        """行为级负向锁：超时清理只认 status='pending'，第三态一行都不能被顺手清掉。

        这是 §CLAIMRELEASE 与 §M-2 共存的支点：release_pending / release_stale_pending 的删除
        谓词都是 status='pending'，所以周期巡检不会把「结果未知/待核对」当成僵死占位删掉
        （删了就等于把唯一的幂等防线再拆一次）。收敛只能靠真实回单/结算写回，或人工走
        POST /admin/order-confirm —— release_unresolved_pending 是唯一主动删除口。
        """
        path = new_db_path()
        s = Store(path)
        self.addCleanup(s.close)
        old = "2020-01-01T09:30:00+08:00"
        self.assertNotEqual(UNRESOLVED_STATUS, "pending", "第三态必须是独立取值，不能混同 pending")
        for sid in ("CLEAN-PENDING", "KEEP-UNRESOLVED"):
            self.assertTrue(s.claim_order({
                "signal_id": sid, "code": "600519.SH", "action": "SELL", "side": "卖出",
                "price": 1500, "qty": 100, "created_at": old})[0])
        self.assertEqual(s.mark_pending_unresolved("KEEP-UNRESOLVED"), 1)

        # 超时清理：只删普通 pending，第三态原地不动
        self.assertEqual(s.release_stale_pending(max_age_sec=1), 1, "应只清掉那条普通 pending")
        self.assertIsNone(s.order_by_signal("CLEAN-PENDING"))
        row = s.order_by_signal("KEEP-UNRESOLVED")
        self.assertIsNotNone(row, "待核对行不得被超时清理删除（§M-2 不变量① 已收紧）")
        self.assertEqual(row["status"], UNRESOLVED_STATUS)

        # 定向释放（§H5 语义：删 pending 占位）同样够不到它
        s.release_pending("KEEP-UNRESOLVED")
        self.assertEqual(s.order_by_signal("KEEP-UNRESOLVED")["status"], UNRESOLVED_STATUS)

        # 唯一主动出口：人工核对确认后放行，放行后该信号可重新接单
        self.assertEqual(s.release_unresolved_pending("KEEP-UNRESOLVED"), 1)
        self.assertIsNone(s.order_by_signal("KEEP-UNRESOLVED"))
        self.assertTrue(s.claim_order({
            "signal_id": "KEEP-UNRESOLVED", "code": "600519.SH", "action": "SELL",
            "side": "卖出", "price": 1500, "qty": 100})[0], "人工确认放行后应可重新接单")


if __name__ == "__main__":
    unittest.main()
