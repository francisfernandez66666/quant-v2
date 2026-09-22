#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway 单测（unittest，零第三方依赖）。

覆盖：store 加权成本/最高价单调/清仓删行、全量对账、signal_id 幂等、handler 回报推送、
HTTP 端到端（/order → mock 成交 → handler 推送首尔 → /state 校验）。无需 Windows/xtquant。
§M-2（2026-09-22 修复批）：见 TestM2PendingLeak——下单窗口抛异常不再遗留 pending 占位
（同 signal_id 可再次受理），运行期巡检只解锁超龄本地占位、绝不重发。
"""
import json
import inspect
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


# 本地账本单测：加权成本/最高价回填等成交落账规则。
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

    def test_fill_fee_leg_persisted_and_settled(self):
        """§P2-FEE 20260918：成交费用腿入库并在 /settlement 装配输出。

        此前 fills 表无 fee 列、settlement_trades 不输出费用 → Go 三方对账费用差腿恒 0。
        现回报带 fee/stamp_tax 则落库，settlement_trades 逐行输出；缺省（旧格式回报）为 0，
        与历史口径字节兼容。
        """
        s = new_store()
        day = "2026-09-18"
        # 先建仓（无持仓的卖出按设计不入账，见 sell-no-position 语义）
        s.apply_fill({"code": "600519.SH", "side": "买入", "price": 1300, "qty": 100,
                      "amount": 130000, "traded_at": day + "T09:30:00+08:00",
                      "signal_id": "S-BUY"})  # 旧格式回报：不带费用字段 → 0
        s.apply_fill({"code": "600519.SH", "side": "卖出", "price": 1300, "qty": 100,
                      "amount": 130000, "traded_at": day + "T09:31:00+08:00",
                      "signal_id": "S-FEE", "trade_id": "TID-FEE",
                      "fee": 32.5, "stamp_tax": 65.0})
        rows = {r["signal_id"]: r for r in s.settlement_trades(day)}
        self.assertAlmostEqual(rows["S-FEE"]["fee"], 32.5)
        self.assertAlmostEqual(rows["S-FEE"]["stamp_tax"], 65.0)
        self.assertAlmostEqual(rows["S-BUY"]["fee"], 0.0)
        self.assertAlmostEqual(rows["S-BUY"]["stamp_tax"], 0.0)

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
        """§G5：单次空快照不清账本；连续两次才接受清空。

        §清算 Guard（2026-09-11）：资产回报显示无持仓市值（真清仓）时连续
        空快照才接受清空；有市值/未回报时保守保留（详见 test_clear_guard.py）。
        """
        s = new_store()
        h = ReportHandler(s, "", "")
        # 资产回报明确 market_value=0（真清仓）→ 维持 §G5 旧语义：连续两次空快照接受清空
        h.on_account({"cash": 90000.0, "frozen_cash": 0.0,
                      "total_asset": 90000.0, "market_value": 0.0})
        s.upsert_position({"ts_code": "600519.SH", "qty": 100, "cost_price": 1500})
        h.on_positions([])  # 第 1 次：跳过
        self.assertEqual(len(s.list_positions()), 1)
        h.on_positions([])  # 第 2 次：接受清空（资产回报证实已无持仓市值）
        self.assertEqual(s.list_positions(), [])


# 整手规则单测：按板块（主板/科创/北交所）判定最小申报单位。
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


# 幂等守卫单测：check_and_record 防同一 signal 重复下单。
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


# HTTP 处理器单测：路由/鉴权/成交推送与断连行为。
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

    def test_fee_of_multi_name_probe(self):
        """§P2-FEE 20260918：费用字段多命名探测（xtquant 各构建字段名不一）。

        commission/fee/trade_fee/total_fee 依序取正数；全不命中/非法值 → 0（绝不臆造费率）。
        """
        from handler import ReportHandler

        class _T(object):
            pass

        t1 = _T()
        t1.commission = 8.88
        self.assertAlmostEqual(ReportHandler._fee_of(t1), 8.88)
        t2 = _T()
        t2.commission = 0
        t2.total_fee = 1.5
        self.assertAlmostEqual(ReportHandler._fee_of(t2), 1.5)
        t3 = _T()
        t3.fee = "bad"  # 非数值不炸，继续探测
        self.assertAlmostEqual(ReportHandler._fee_of(t3), 0.0)
        self.assertAlmostEqual(ReportHandler._fee_of(_T()), 0.0)

    def test_trade_report_carries_fee(self):
        """§P2-FEE 20260918：回报 payload 与网关账本同源携带费用腿（Go 落 fills.fee）。"""
        s = new_store()
        pushed = []
        h = None
        import handler as handler_mod
        old = handler_mod.post_report
        handler_mod.post_report = lambda *a, **k: True
        try:
            h = handler_mod.ReportHandler(s, "http://seoul:8080", "tok")
            h._push = lambda p: pushed.append(p)
            h.on_trade({"order_id": "O1", "code": "600519.SH", "side": "买入", "price": 10,
                        "qty": 100, "amount": 1000, "traded_at": "2026-09-18T09:31:00+08:00",
                        "signal_id": "S1", "trade_id": "T1", "fee": 5.0, "stamp_tax": 5.0})
        finally:
            handler_mod.post_report = old
        self.assertEqual(pushed[0]["type"], "trade")
        self.assertAlmostEqual(pushed[0]["fee"], 5.0)
        rows = s.settlement_trades("2026-09-18")
        self.assertAlmostEqual(rows[0]["fee"], 5.0)
        self.assertAlmostEqual(rows[0]["stamp_tax"], 5.0)

    def test_outbox_permanent_reject_dead_letter(self):
        """§P1-8（2026-09-15）毒丸防护回归：首尔永久性拒绝（400）的回报落死信表，
        队列继续消费后续回报——旧实现滞留队首无限重试，单条毒丸卡死整个 FIFO。"""
        import handler as handler_mod
        s = new_store()
        h = handler_mod.ReportHandler(s, "http://seoul:8080", "tok")
        delivered = []

        def fake_status(url, token, payload, retries=3):
            """首尔侧模拟：type=bad 永久 400，其余成功。"""
            if payload.get("type") == "bad":
                return False, 400
            delivered.append(payload)
            return True, 200

        # 打桩：临时替换模块级 post_report_status 模拟首尔侧应答（finally 里还原），
        # 三条回报首条 type=bad 永久 400，须落死信且不阻塞后面的 trade/order
        old_status = handler_mod.post_report_status
        handler_mod.post_report_status = fake_status
        h.start_sender()
        try:
            h._push({"type": "bad", "seq": 1})
            h._push({"type": "trade", "seq": 2})
            h._push({"type": "order", "seq": 3})
            deadline = time.time() + 5
            while s.outbox_count() > 0 and time.time() < deadline:
                time.sleep(0.05)
            self.assertEqual(s.outbox_count(), 0, "毒丸不得滞留队首")
            self.assertEqual([p.get("type") for p in delivered], ["trade", "order"],
                             "毒丸之后的回报必须照常投递")
            # 死信留痕
            with s._lock:
                rows = s._conn.execute(
                    "SELECT payload, reason FROM outbox_dead").fetchall()
            self.assertEqual(len(rows), 1)
            self.assertIn('"seq": 1', rows[0]["payload"])
            self.assertIn("400", rows[0]["reason"])
        finally:
            h.stop_sender()
            handler_mod.post_report_status = old_status


# 模拟成交引擎单测：mock 通道下的委托撮合与持仓更新。
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


# xtquant 延迟导入单测：无 xtquant 环境可实例化，connect 时才报错。
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


# 网关 HTTP 集成单测：起真实 ThreadingHTTPServer 走全链路。
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
        self.gw.stop()  # §UAT-D8 走完整优雅停机（置停+停 sender+join 后台线程）
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

    def test_settlement_endpoint(self):
        """§P0-1a（2026-09-15）：/settlement 日终对账权威源——此端点此前根本不存在，
        Go 侧 FetchSettlement 一直 404，README §U-2 宣称的日终四方对账从未生效。"""
        from datetime import datetime, timedelta, timezone
        day = datetime.now(timezone(timedelta(hours=8))).strftime("%Y-%m-%d")
        # 缺日期 → 400
        status, body = self._req("GET", "/settlement")
        self.assertEqual(status, 400)
        status, body = self._req("GET", "/settlement?date=20260915")
        self.assertEqual(status, 400)

        # 下一单并等 mock 成交（fills 落库带 trade_id 流水号）
        status, body = self._req("POST", "/order", {
            "signal_id": "SETTLE1", "code": "600519.SH", "name": "贵州茅台", "side": "买入",
            "price_type": "market", "price": 1510, "qty": 100, "amount": 151000,
            "created_at": "t",
        })
        self.assertEqual(status, 200)
        order_id = body["order_id"]
        time.sleep(1.2)

        status, body = self._req("GET", "/settlement?date=" + day)
        self.assertEqual(status, 200)
        self.assertTrue(body["connected"])
        self.assertEqual(body["date"], day)
        self.assertEqual(len(body["trades"]), 1)
        t = body["trades"][0]
        self.assertEqual(t["ts_code"], "600519.SH")
        self.assertEqual(t["side"], "买入")
        self.assertEqual(t["price"], 1510)
        self.assertEqual(t["qty"], 100)
        self.assertEqual(t["order_id"], order_id)
        # serial = §G1 唯一成交编号（Go 侧物理事实键之外的第二关联锚点）
        self.assertTrue(t["serial"])
        self.assertTrue(t["traded_at"].startswith(day))
        # cash 快照缺失时为数值 dict（Go 侧按 cash.cash>0 才计现金差，不误报）
        self.assertIsInstance(body["cash"].get("cash"), float)

        # 无成交日期 → 空 trades 不误报
        status, body = self._req("GET", "/settlement?date=2020-01-02")
        self.assertEqual(status, 200)
        self.assertEqual(body["trades"], [])

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


class TestM2PendingLeak(unittest.TestCase):
    """§M-2（2026-09-22 修复批）下单窗口异常不再遗留 pending 占位 + 运行期巡检兜底。

    缺陷原文：`gateway.py:1042` 的 `place_order` 裸调用没有 try 包裹，通道抛异常时
    异常被 `_Handler._dispatch`（:1225 的 §G8 顶层保护）吞成 500，**但已抢到的 pending
    占位不会 release**；而 `release_stale_pending` 只在 `start()`（:258-262）调一次、
    运行期 `_dispatch_reap_loop`（:494-502）只清 dispatch 的 inflight 不清 orders 的
    pending → 同 signal_id 之后每次重试恒 409「duplicate signal_id in-flight」，
    该信号永久死锁。
    本类锁两个不变式：① 未 settle 的路径（异常/settle 失败）绝不遗留占位，且网关
    自身不重发；② 运行期巡检只解锁超龄**本地占位**，不重排、不重发（ids.py 的
    「不重复下单」fail-safe 语义必须原样保留）。
    """

    def _gw(self):
        """构造一台不启动 HTTP 的 mock 通道网关（只驱动 _do_order 的业务段）。"""
        fd, dbpath = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        os.unlink(dbpath)
        return Gateway({"listen": "127.0.0.1:0", "token": "tk", "broker": "mock",
                        "account": "M", "db": dbpath, "report_url": "",
                        "reconcile_sec": 0, "seed": []})

    def _body(self, sid, side="买入"):
        """600519 一手限价单：只换 signal_id/方向，其余与实盘同形。"""
        return {"signal_id": sid, "code": "600519.SH", "side": side,
                "price_type": "limit", "price": 10, "qty": 100,
                "amount": 1000, "created_at": "t"}

    def test_place_order_exception_releases_claim_and_allows_retry(self):
        """place_order 抛异常 → 500 且占位释放 → 同 signal_id 可再次真正受理。"""
        gw = self._gw()
        real = gw.active_broker

        class _BoomBroker(object):
            """通道桩：place_order 必抛异常（复刻 xt IPC 抖动 / 桥写文件失败）。"""

            def is_connected(self):
                return True

            def place_order(self, req):
                raise RuntimeError("xt IPC boom")

        gw.active_broker = _BoomBroker()
        try:
            status, body = gw._do_order(self._body("M2A"))
            self.assertEqual(status, 500, "异常必须显式回 500（不得静默成 200/409）")
            self.assertFalse(body["ok"])
            self.assertIn("boom", body["err"])
            # 关键断言：占位已释放，orders 里不留 pending 行
            self.assertIsNone(gw.store.order_by_signal("M2A"),
                              "§M-2 异常路径遗留了 pending 占位（旧缺陷现场）")
            # 通道恢复后同 signal_id 重试：旧实现此处恒 409
            gw.active_broker = real
            s2, b2 = gw._do_order(self._body("M2A"))
            self.assertEqual(s2, 200, "异常后同 signal_id 无法再次受理: %s" % b2)
            self.assertTrue(b2["ok"])
        finally:
            gw.active_broker = real
            gw.stop()

    def test_settle_exception_also_releases_claim(self):
        """settle 自身抛异常（DB 抖动）同样不留占位——finally 兜底覆盖整个未 settle 窗口。"""
        gw = self._gw()
        original = gw.ids.settle
        try:
            gw.ids.settle = lambda order: (_ for _ in ()).throw(RuntimeError("db locked"))
            status, body = gw._do_order(self._body("M2B"))
            self.assertEqual(status, 500)
            self.assertIsNone(gw.store.order_by_signal("M2B"))
        finally:
            gw.ids.settle = original
            gw.stop()

    def test_failed_place_still_releases_once_only(self):
        """broker 返回失败的既有语义不变：释放占位 + 400，且不会留下 pending 行。"""
        gw = self._gw()
        real = gw.active_broker

        class _Reject(object):
            def is_connected(self):
                return True

            def place_order(self, req):
                return False, "", "counter rejected"

        gw.active_broker = _Reject()
        try:
            status, body = gw._do_order(self._body("M2C"))
            self.assertEqual(status, 400)
            self.assertIn("counter rejected", body["err"])
            self.assertIsNone(gw.store.order_by_signal("M2C"))
        finally:
            gw.active_broker = real
            gw.stop()

    def test_runtime_sweep_releases_only_stale_local_placeholder(self):
        """§M-2 巡检：只删超龄本地占位（默认 600s），未超龄保留，且**绝不重发**订单。"""
        gw = self._gw()
        old_ts = (datetime.now(CN_TZ) - timedelta(hours=25)).strftime("%Y-%m-%dT%H:%M:%S+08:00")
        fresh_ts = (datetime.now(CN_TZ) - timedelta(seconds=5)).strftime("%Y-%m-%dT%H:%M:%S+08:00")
        try:
            self.assertTrue(gw.store.claim_order({
                "signal_id": "M2-OLD", "code": "600519.SH", "side": "买入",
                "price": 10.0, "qty": 100, "created_at": old_ts})[0])
            self.assertTrue(gw.store.claim_order({
                "signal_id": "M2-NEW", "code": "600519.SH", "side": "买入",
                "price": 10.0, "qty": 100, "created_at": fresh_ts})[0])
            n = gw._sweep_stale_pending("单测")
            self.assertEqual(n, 1, "只应释放超龄的那一条占位")
            self.assertIsNone(gw.store.order_by_signal("M2-OLD"), "超龄占位未解锁")
            row = gw.store.order_by_signal("M2-NEW")
            self.assertIsNotNone(row)
            self.assertEqual(row["status"], "pending", "未超龄占位被误删（在途单窗口）")
            # 「只解锁不重发」硬断言：清理只是删掉超龄占位行，行数不增、也不产生新单
            self.assertEqual(len(gw.store.list_orders()), 1,
                             "巡检后委托行数 != 1 → 要么多删了未超龄行，要么发生了隐式重发")
            self.assertEqual(gw.store.dispatch_pending(limit=5), [],
                             "§M-2 巡检不得把清理掉的占位重新排进派发队列")
        finally:
            gw.stop()

    def test_sweep_disabled_by_zero_threshold(self):
        """pending_stale_sec=0 → 巡检关闭（保留运维"完全关闭自动解锁"的开关）。"""
        gw = self._gw()
        gw.cfg["pending_stale_sec"] = 0
        old_ts = (datetime.now(CN_TZ) - timedelta(hours=25)).strftime("%Y-%m-%dT%H:%M:%S+08:00")
        try:
            gw.store.claim_order({"signal_id": "M2-OFF", "code": "600519.SH", "side": "买入",
                                  "price": 10.0, "qty": 100, "created_at": old_ts})
            self.assertEqual(gw._sweep_stale_pending(), 0)
            self.assertIsNotNone(gw.store.order_by_signal("M2-OFF"), "关闭开关后仍被清理")
        finally:
            gw.stop()

    def test_reap_loop_calls_pending_sweep(self):
        """§M-2 巡检接线：运行期 reap 循环必须真的调用 pending 清理（旧循环只清 inflight）。"""
        src = inspect.getsource(Gateway._dispatch_reap_loop)
        self.assertIn("_sweep_stale_pending()", src,
                      "reap 循环未接入 pending 巡检 → 运行期占位依旧无人解锁")


if __name__ == "__main__":
    unittest.main()