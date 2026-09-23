# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_side_auth2.py — §SIDE-AUTH-2（2026-09-23 夜间批）方向权威补漏回归。

事故形态（docs/FIX_PLAN_20260923NIGHT.md §2）：09-22 10:08 一笔真实**卖出**（603468.SH 900股
@22.55）被记成**买入**——网关 _apply_trade 的三级派发行回查（seq→交易所委托号→signal_id）
全落空时，旧代码**不加任何标记**地直接采用桥/柜台的枚举推断方向；而桥侧 _side_of 是
多枚举空间猜测（order_type 23/24 vs 1101/1102、offset_type 48/50，本项目已两次踩坑），
猜错即"主动把卖出说成买入"，Go 侧照单入库并改持仓账。

本文件锁三条正反对（①② 桥/HTTP 入口，③ xtquant 直连回调＝现网实盘主通道）：
  ① 命中派发行 → 派发方向覆盖桥推断（§P0 既有行为不回退）、回报**不带** side_unverified、
     持仓账照常变动（卖真减仓）；
  ② 未命中派发行 → 打 side_unverified=true 并 warning 留痕；本地账走「待核对」通道
     （fills 证据行保留全部可复核字段、side 落「待核对」字面量），**绝不改动持仓账**；
     上报载荷带标记（首尔侧据此留痕拒入账本）。
  附加：未证成交的重放不产生第二条待核对行（判重口径与落库口径一致）。
（English: §SIDE-AUTH-2 regression — dispatch-verified fills override the bridge guess without
the flag (positions move); unverified fills are flagged side_unverified, journaled to the
unresolved (待核对) channel and must never touch the position book.）
"""
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from gateway import Gateway  # noqa: E402
from store import UNRESOLVED_STATUS  # noqa: E402


def _new_gw():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    cfg = {"listen": "127.0.0.1:0", "token": "t", "broker": "mock", "account": "M",
           "db": path, "report_url": "", "report_token": "", "user_id": ""}
    gw = Gateway(cfg)
    pushed = []
    gw.handler.report_url = "http://seoul.invalid/api/qmt/report"
    gw.handler.store.outbox_enqueue = lambda p: (pushed.append(p), 1)[1]
    gw.handler.store.outbox_trim = lambda n: 0
    gw.handler.store.outbox_count = lambda: 0
    return gw, pushed


def _fills(store):
    return [dict(r) for r in store._conn.execute("SELECT * FROM fills").fetchall()]


class TestDispatchVerifiedOverridesInference(unittest.TestCase):
    """① 命中派发行：派发方向为唯一权威（覆盖桥的错误推断），不带 unverified 标记。"""

    def test_dispatch_hit_overrides_and_moves_book(self):
        gw, pushed = _new_gw()
        # 先造底仓：一笔已证的买入成交（派发行也齐全），持仓 900 股
        seq0 = gw.store.dispatch_enqueue_order(
            {"signal_id": "SA2-BUY", "code": "603468.SH", "side": "买入",
             "price_type": "limit", "price": 20.0, "qty": 900, "strategy": "t"})
        gw.store.dispatch_set_result(seq0, {"ok": True, "order_id": "EXC-BUY", "err": ""})
        code, _ = gw._do_dispatch_result({
            "type": "trade", "seq": seq0, "order_id": "EXC-BUY", "trade_id": "T-BUY",
            "code": "603468.SH", "side": "买入",  # 桥推断恰好=派发项
            "price": 20.0, "qty": 900, "amount": 18000.0,
            "traded_at": "2026-09-22T10:00:00+08:00", "signal_id": "SA2-BUY",
        })
        self.assertEqual(code, 200)
        self.assertEqual(gw.store.list_positions()[0]["qty"], 900)

        # 真实卖出：派发项 side=卖出，桥却把方向推断成"买入"（事故形态复输入）
        seq1 = gw.store.dispatch_enqueue_order(
            {"signal_id": "SA2-SELL", "code": "603468.SH", "side": "卖出",
             "price_type": "limit", "price": 22.55, "qty": 900, "strategy": "t"})
        gw.store.dispatch_set_result(seq1, {"ok": True, "order_id": "EXC-SELL", "err": ""})
        del pushed[:]
        code, _ = gw._do_dispatch_result({
            "type": "trade", "seq": seq1, "order_id": "EXC-SELL", "trade_id": "T-SELL",
            "code": "603468.SH", "side": "买入",  # ← 桥的错误推断，必须被派发项覆盖
            "price": 22.55, "qty": 900, "amount": 20295.0,
            "traded_at": "2026-09-22T10:08:32+08:00", "signal_id": "SA2-SELL",
        })
        self.assertEqual(code, 200)
        # 上报腿：方向已被派发项纠正，且不携带 side_unverified（命中即已证，契约面零噪声）
        ev = pushed[-1]
        self.assertEqual(ev["side"], "卖出", "§P0 权威覆盖不得回退：派发方向必须赢")
        self.assertNotIn("side_unverified", ev, "命中派发行的成交不得携带 unverified 标记")
        # 持仓账照常变动：清仓删行（这笔把 900 股全卖光）
        self.assertEqual(gw.store.list_positions(), [], "已证卖出必须动持仓账")
        rows = _fills(gw.store)
        sell_row = [r for r in rows if r["trade_id"] == "T-SELL"][0]
        self.assertEqual(sell_row["side"], "卖出")


class TestUnverifiedSideGoesUnresolved(unittest.TestCase):
    """② 未命中派发行：打标记+留痕，落「待核对」通道，不动持仓账。"""

    def test_dispatch_miss_flags_and_freezes_position_book(self):
        gw, pushed = _new_gw()
        # 桥凭空推来一笔"买入"（无任何派发行可对，推断方向无从证实）
        code, _ = gw._do_dispatch_result({
            "type": "trade", "order_id": "EXC-GHOST", "trade_id": "T-GHOST",
            "code": "603468.SH", "side": "买入",  # 可能恰恰是卖出——不可采信
            "price": 22.55, "qty": 900, "amount": 20295.0,
            "traded_at": "2026-09-22T10:08:32+08:00",
        })
        self.assertEqual(code, 200, "标记后仍受理（证据行要落库），不是拒收")
        # 上报腿：带 side_unverified=true + 原推断方向（Go 侧留痕用，字段面在契约 golden 内）
        self.assertEqual(len(pushed), 1)
        ev = pushed[0]
        self.assertIs(ev.get("side_unverified"), True)
        self.assertEqual(ev["side"], "买入")
        # 本地账：fills 证据行落「待核对」，可复核字段全保留；持仓账纹丝不动
        rows = _fills(gw.store)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["side"], UNRESOLVED_STATUS,
                         "未证成交的 fills 行必须落「待核对」而非采信推断方向")
        for k, want in (("code", "603468.SH"), ("price", 22.55), ("qty", 900),
                        ("order_id", "EXC-GHOST"), ("trade_id", "T-GHOST"),
                        ("amount", 20295.0)):
            self.assertEqual(rows[0][k], want, "待核对行缺可复核字段 %s" % k)
        self.assertEqual(gw.store.list_positions(), [], "未证成交绝不许改动持仓账")

        # 同一笔重放：不得产生第二条待核对行（trade_id 判重 + 待核对口径复合键双保险）
        code, _ = gw._do_dispatch_result({
            "type": "trade", "order_id": "EXC-GHOST", "trade_id": "T-GHOST",
            "code": "603468.SH", "side": "买入",
            "price": 22.55, "qty": 900, "amount": 20295.0,
            "traded_at": "2026-09-22T10:08:32+08:00",
        })
        self.assertEqual(code, 200)
        self.assertEqual(len(_fills(gw.store)), 1, "重放不得追加第二条待核对行")


class _FakeTrade:
    """xtquant 成交回调对象的最小替身（字段名与 broker._CallbackAdapter 转发的真实对象一致）。

    order_type 用本机构买的"买入=23"，用来复现"柜台枚举推断与派发行事实相反"的事故输入。
    """

    def __init__(self, order_id, trade_id, code, order_type, price, qty, signal_id=""):
        self.order_id = order_id
        self.trade_id = trade_id
        self.stock_code = code
        self.stock_name = "测试票"
        self.order_type = order_type
        self.traded_price = price
        self.traded_volume = qty
        self.traded_amount = price * qty
        self.order_remark = signal_id
        self.commission = 0.0


class TestXTDirectChannelVouched(unittest.TestCase):
    """③ xtquant 直连回调通道（**现网实盘主通道**，broker.XtBroker.register_callback →
    handler.on_stock_trade）必须装同一道方向闸。

    为什么单独立一条：§SIDE-AUTH-2 最初只补在 `gateway._apply_trade`（桥/HTTP `/api/trade`
    那条入口）里，而 on_stock_trade 根本不经过它——方向仍是从 `handler._side_of` 的
    多枚举空间裸猜、且不携带任何标记。只补一条入口＝把 603468.SH 那类污染留在主通道上。
    English: the live xtquant callback path must vouch the side the same way; fixing only the
    bridge/HTTP entry would leave the accident path unguarded.
    """

    def test_xt_dispatch_hit_overrides_counter_guess(self):
        gw, pushed = _new_gw()
        # 底仓：xt 通道的已证买入（派发行 side=买入，柜台也推断买入 → 建仓 900）
        seq0 = gw.store.dispatch_enqueue_order(
            {"signal_id": "XT-BUY", "code": "603468.SH", "side": "买入",
             "price_type": "limit", "price": 20.0, "qty": 900, "strategy": "t"})
        gw.store.dispatch_set_result(seq0, {"ok": True, "order_id": "XT-EXC-BUY", "err": ""})
        gw.handler.on_stock_trade(_FakeTrade("XT-EXC-BUY", "XT-T-BUY", "603468.SH", 23, 20.0, 900, "XT-BUY"))
        self.assertEqual(gw.store.list_positions()[0]["qty"], 900)

        # 事故输入：真实卖出（派发行 side=卖出），柜台枚举却推成"买入"（order_type=23）
        seq1 = gw.store.dispatch_enqueue_order(
            {"signal_id": "XT-SELL", "code": "603468.SH", "side": "卖出",
             "price_type": "limit", "price": 22.55, "qty": 900, "strategy": "t"})
        gw.store.dispatch_set_result(seq1, {"ok": True, "order_id": "XT-EXC-SELL", "err": ""})
        del pushed[:]
        gw.handler.on_stock_trade(_FakeTrade("XT-EXC-SELL", "XT-T-SELL", "603468.SH", 23, 22.55, 900, "XT-SELL"))

        ev = pushed[-1]
        self.assertEqual(ev["side"], "卖出", "xt 通道必须以派发行方向为准")
        self.assertNotIn("side_unverified", ev, "命中派发行的成交不得带 unverified 标记")
        self.assertEqual(gw.store.list_positions(), [], "已证卖出必须真减仓（清仓删行）")
        row = [r for r in _fills(gw.store) if r["trade_id"] == "XT-T-SELL"][0]
        self.assertEqual(row["side"], "卖出")

    def test_xt_dispatch_miss_routes_to_unresolved(self):
        gw, pushed = _new_gw()
        # 无派发行可查（柜台/手工单回报）：方向只是枚举猜测 → 待核对，绝不动持仓账
        gw.handler.on_stock_trade(_FakeTrade("XT-EXC-GHOST", "XT-T-GHOST", "603468.SH", 23, 22.55, 900))

        self.assertEqual(len(pushed), 1, "证据行仍要上报（首尔侧留痕），不是丢弃")
        ev = pushed[0]
        self.assertIs(ev.get("side_unverified"), True)
        self.assertEqual(ev["side"], "买入", "载荷保留原推断值供人工比对，不落当真账")
        rows = _fills(gw.store)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["side"], UNRESOLVED_STATUS)
        self.assertEqual(gw.store.list_positions(), [], "未证成交绝不许改动持仓账")


if __name__ == "__main__":
    unittest.main()
