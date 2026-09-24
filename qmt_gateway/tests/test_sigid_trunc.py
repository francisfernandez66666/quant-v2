# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_sigid_trunc.py — §SIGID-TRUNC（2026-09-24）成交回报 signal_id 被截到
24 字符的回归。

事故形态（owner 2026-09-24 裁决"这个要修"）：柜台的 userOrderId 槽是短字段，桥 embed_place 提交
时把 signal_id 截到 24 位；本仓 signal_id 天然 25 字符（`buy:603468:fac_1:20260922`），于是
① 末位日期被削掉 ⇒ 跨两个交易日塌成同一个编号，② 成交回报里的 remark 也就是这 24 位 ⇒ 网关
三级派发行回查（seq→交易所委托号→signal_id 精确查）里最该命中的那一级恒不命中。

资金链三处静默失效（本文件逐条锁死）：
  A 归因：`embed_resolve` / `resolve_order_id` 拿**完整** id 去等值比对柜台的**截断** remark ⇒
    恒假 ⇒ 静默降级成"指纹匹配（code/op/price/qty 首条未认领）"，归属靠猜；
  B 方向：查不到派发行 ⇒ §SIDE-AUTH-2 判"方向不可证" ⇒ 每一笔这样的成交都掉进「待核对」通道、
    持仓账永不变动（09-22 603468.SH 事故链的源头解释）；
  C 账本：落库的 fills.signal_id 是截断值 ⇒ Go 侧 `signal_id LIKE orders.signal_id||'%'` 的
    部成在途净额聚合永不命中。

修法：截断量收敍成单一常量 `WIRE_REF_MAX` + 单一函数 `wire_ref()`（提交腿与两条反查腿共用，
幂等），网关加第四级回落 `dispatch_by_signal_prefix`（substr 前缀＋同代码＋同交易日，候选必须
(signal_id, side, code) 完全一致才认），命中后把 req.signal_id 换成派发项的**完整**值再背书方向、
再落库。歧义不猜。
（English: the counter only carries a 24-char wire form of our signal id; one helper models the
truncation on both the submit and the two lookup legs, and the gateway repairs the wire ref back
to the full dispatch signal_id through a prefix lookup that refuses ambiguous candidates.）
"""
import datetime
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import qmt_bridge_strategy as bs  # noqa: E402
from gateway import Gateway  # noqa: E402


# 本仓真实形态的 signal_id：25 字符，截到 24 恰好削掉日期末位
FULL_SID = "buy:603468:fac_1:20260922"
WIRE_SID = FULL_SID[:24]


class TestWireRef(unittest.TestCase):
    """A-0：截断只有一处定义，且对两条反查腿幂等可用。"""

    def test_wire_ref_is_the_single_truncation(self):
        self.assertEqual(bs.WIRE_REF_MAX, 24)
        self.assertEqual(bs.wire_ref(FULL_SID), WIRE_SID)
        self.assertNotEqual(bs.wire_ref(FULL_SID), FULL_SID, "25 字符编号必然被截，别以为能原样上线")
        # 幂等：柜台回的 remark 已经是截断值，再过一次 wire_ref 必须不变（否则反查又会错位）
        self.assertEqual(bs.wire_ref(bs.wire_ref(FULL_SID)), WIRE_SID)
        self.assertEqual(bs.wire_ref(""), "")
        self.assertEqual(bs.wire_ref(None), "")

    def test_two_trading_days_collapse_to_one_wire_ref(self):
        """把"跨日塌成同一编号"钉成事实：这正是前缀查必须再加 code/day 约束的理由。"""
        d1 = "buy:603468:fac_1:20260922"
        d2 = "buy:603468:fac_1:20260923"
        self.assertNotEqual(d1, d2)
        self.assertEqual(bs.wire_ref(d1), bs.wire_ref(d2),
                         "两天削掉末位后同前缀——归因若不约束交易日就会张冠李戴")


class _EmbedOrder:
    """embed_resolve 读的 ORDER 行替身（只带它真正 getattr 的字段）。"""

    def __init__(self, remark, oid, code="603468", order_type=23, price=22.55, volume=900):
        self.m_strRemark = remark
        self.m_strOrderSysID = oid
        self.m_strInstrumentID = code
        self.m_nOrderType = order_type
        self.m_dOrderPrice = price
        self.m_nOrderVolume = volume


class _XtOrder:
    """resolve_order_id 非 embed 腿读的 ORDER 行替身（miniQMT 字段名）。"""

    def __init__(self, remark, oid):
        self.order_remark = remark
        self.order_id = oid


class _FakeTrader:
    def __init__(self, rows):
        self._rows = rows

    def query_stock_orders(self, _acc):
        return self._rows


class TestLookupsUseWireForm(unittest.TestCase):
    """A-1/A-2：两条反查腿必须按线上传值比对（旧代码拿完整 id 等值比 ⇒ 恒假）。"""

    def _ops(self):
        # dry_run=True 只为构造一个不碰 xtquant 的实例；embed_resolve 本身不看 dry_run。
        return bs._XtOps(account="ACC", dry_run=True, xt_path="", session_id=2)

    def test_embed_resolve_matches_truncated_remark(self):
        ops = self._ops()
        # _last_place 故意与真实委托的价格/数量不一致 ⇒ 指纹分支必不命中，
        # 只有 remark 一条路能返回委托号（旧代码在此返回空串，静默降级）。
        ops._last_place = ("603468", 23, 21.00, 100, 0.0)
        ops.embed_orders = lambda: [_EmbedOrder(WIRE_SID, "EXC-7788")]
        oid = ops.embed_resolve({"code": "603468.SH", "signal_id": FULL_SID}, timeout_sec=1.0)
        self.assertEqual(oid, "EXC-7788", "柜台 remark 是截断值，反查必须按线上传值比")

    def test_resolve_order_id_matches_truncated_remark(self):
        ops = self._ops()
        ops.dry_run = False
        ops.embed_usable = lambda: False
        ops._trader = _FakeTrader([_XtOrder(WIRE_SID, "EXC-9911")])
        ops._acc = "ACC"
        got = ops.resolve_order_id(FULL_SID, "seq:1")
        self.assertEqual(got, "EXC-9911")
        self.assertTrue(ops.last_resolve_confirmed, "柜台出现过本端委托就必须判已确认（M-3 语义）")


class TestStorePrefixLookup(unittest.TestCase):
    """B/C 前置：dispatch_by_signal_prefix 的命中、约束与"歧义不猜"。"""

    def setUp(self):
        fd, self.path = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        os.unlink(self.path)
        self.gw = Gateway({"listen": "127.0.0.1:0", "token": "t", "broker": "mock",
                           "account": "M", "db": self.path, "report_url": "",
                           "report_token": "", "user_id": ""})
        self.store = self.gw.store
        self.today = datetime.date.today().strftime("%Y-%m-%d")

    def tearDown(self):
        try:
            self.store.close()
        except Exception:
            pass

    def _enqueue(self, signal_id, code="603468.SH", side="买入", order_id=""):
        seq = self.store.dispatch_enqueue_order(
            {"signal_id": signal_id, "code": code, "side": side,
             "price_type": "limit", "price": 22.55, "qty": 900, "strategy": "t"})
        if order_id:
            self.store.dispatch_set_result(seq, {"ok": True, "order_id": order_id, "err": ""})
        return seq

    def _set_day(self, seq, day):
        """改派发行日期（造跨日同前缀的场景；created_at 缺省=今天）。"""
        self.store._conn.execute("UPDATE dispatch SET created_at=? WHERE seq=?",
                                 (day + "T09:31:00+08:00", seq))
        self.store._conn.commit()

    def test_hit_returns_full_dispatch_id(self):
        self._enqueue(FULL_SID, side="卖出", order_id="EXC-1")
        row, why = self.store.dispatch_by_signal_prefix(WIRE_SID, "603468.SH", self.today)
        self.assertEqual(why, "ok")
        self.assertEqual(row["signal_id"], FULL_SID, "还原出的必须是派发项完整编号，不是柜台截断值")
        self.assertEqual(row["side"], "卖出")

    def test_wrong_day_and_wrong_code_do_not_match(self):
        self._enqueue(FULL_SID, side="卖出", order_id="EXC-1")
        row, why = self.store.dispatch_by_signal_prefix(WIRE_SID, "600000.SH", self.today)
        self.assertIsNone(row, "代码不同就不是这笔单，不能靠前缀相似就认")
        self.assertEqual(why, "miss")
        row, why = self.store.dispatch_by_signal_prefix(WIRE_SID, "603468.SH", "2020-01-01")
        self.assertIsNone(row)
        self.assertEqual(why, "miss")

    def test_cross_day_same_prefix_needs_the_day_leg(self):
        """两天同前缀（TestWireRef 已证会塌）⇒ 带 day 唯一命中，不带 day 判歧义不猜。"""
        other = "buy:603468:fac_1:20260923"
        self.assertEqual(bs.wire_ref(other), WIRE_SID)
        self._enqueue(FULL_SID, side="卖出", order_id="EXC-1")     # created_at = 今天
        self._enqueue(other, side="卖出", order_id="EXC-2")
        self._set_day("seq:2", "2026-09-23")                        # 第二行属于另一个交易日
        row, why = self.store.dispatch_by_signal_prefix(WIRE_SID, "603468.SH", self.today)
        self.assertEqual(why, "ok", "同日约束下只有一个候选")
        self.assertEqual(row["signal_id"], FULL_SID)
        row, why = self.store.dispatch_by_signal_prefix(WIRE_SID, "603468.SH", "")
        self.assertIsNone(row, "跨日两笔真 signal_id ⇒ 不许取最新的猜一个")
        self.assertEqual(why, "ambiguous")

    def test_retry_same_id_still_hits(self):
        """同一 signal_id 重试产生的多派发行（编号/方向/代码全一致）不是歧义，认最新一条。"""
        self._enqueue(FULL_SID, side="卖出", order_id="EXC-1")
        self._enqueue(FULL_SID, side="卖出", order_id="EXC-2")
        row, why = self.store.dispatch_by_signal_prefix(WIRE_SID, "603468.SH", self.today)
        self.assertEqual(why, "ok")
        self.assertEqual(row["order_id"], "EXC-2")

    def test_underscore_is_not_a_like_wildcard(self):
        """substr 比对而非 LIKE：`fac_1` 不得把 `facX1` 也认进来。"""
        self._enqueue("buy:603468:facX1:2026092", side="卖出")
        row, why = self.store.dispatch_by_signal_prefix(WIRE_SID, "603468.SH", self.today)
        self.assertIsNone(row, "LIKE 的通配把 _ 当任意字符时会误命中")
        self.assertEqual(why, "miss")


class TestGatewayRepairsAndVouches(unittest.TestCase):
    """B/C：网关成交腿——还原编号 ⇒ 方向有背书（不再落待核对）⇒ 账本存完整编号。"""

    def _new_gw(self):
        fd, path = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        os.unlink(path)
        gw = Gateway({"listen": "127.0.0.1:0", "token": "t", "broker": "mock",
                      "account": "M", "db": path, "report_url": "",
                      "report_token": "", "user_id": ""})
        pushed = []
        gw.handler.report_url = "http://seoul.invalid/api/qmt/report"
        gw.handler.store.outbox_enqueue = lambda p: (pushed.append(p), 1)[1]
        gw.handler.store.outbox_trim = lambda n: 0
        gw.handler.store.outbox_count = lambda: 0
        return gw, pushed

    def test_truncated_report_is_repaired_and_moves_book(self):
        gw, pushed = self._new_gw()
        today = datetime.date.today().strftime("%Y-%m-%d")
        # 底仓：已证买入（走 seq 精确归因，与本批无关，只为建仓）
        seq0 = gw.store.dispatch_enqueue_order(
            {"signal_id": "TR-BUY", "code": "603468.SH", "side": "买入",
             "price_type": "limit", "price": 20.0, "qty": 900, "strategy": "t"})
        gw.store.dispatch_set_result(seq0, {"ok": True, "order_id": "EXC-BUY", "err": ""})
        gw._do_dispatch_result({
            "type": "trade", "seq": seq0, "order_id": "EXC-BUY", "trade_id": "T-BUY",
            "code": "603468.SH", "side": "买入", "price": 20.0, "qty": 900, "amount": 18000.0,
            "traded_at": today + "T10:00:00+08:00", "signal_id": "TR-BUY"})
        self.assertEqual(gw.store.list_positions()[0]["qty"], 900)

        # 事故输入：卖出派发行在，柜台回报带的是**截断编号**、order_id 也是 remark（桥的真实形态）
        sid = "buy:603468:fac_1:" + today.replace("-", "")
        self.assertEqual(len(sid), 25, "本仓编号形态前提：25 字符，必然被柜台截")
        seq1 = gw.store.dispatch_enqueue_order(
            {"signal_id": sid, "code": "603468.SH", "side": "卖出",
             "price_type": "limit", "price": 22.55, "qty": 900, "strategy": "t"})
        gw.store.dispatch_set_result(seq1, {"ok": True, "order_id": "EXC-SELL", "err": ""})
        del pushed[:]
        gw._do_dispatch_result({
            "type": "trade", "order_id": sid[:24], "trade_id": "T-SELL",
            "code": "603468.SH", "side": "买入",  # 柜台枚举推断（与派发事实相反）
            "price": 22.55, "qty": 900, "amount": 20295.0,
            "traded_at": today + "T10:08:32+08:00", "signal_id": sid[:24]})

        ev = pushed[-1]
        self.assertEqual(ev["signal_id"], sid, "账本必须落派发项的完整编号（否则 Go 侧按编号聚合失明）")
        self.assertEqual(ev["side"], "卖出", "还原编号 ⇒ 派发行查到 ⇒ 方向权威覆盖桥推断")
        self.assertNotIn("side_unverified", ev, "已证成交不得再被打进待核对通道")
        self.assertEqual(gw.store.list_positions(), [], "已证卖出必须真减仓（清仓删行）")
        row = [r for r in (dict(x) for x in
                           gw.store._conn.execute("SELECT * FROM fills").fetchall())
               if r["trade_id"] == "T-SELL"][0]
        self.assertEqual(row["signal_id"], sid)
        self.assertEqual(row["side"], "卖出")

    def test_ambiguous_prefix_stays_unverified(self):
        """歧义不猜：还原不了就照 §SIDE-AUTH-2 走待核对，且账本保留柜台原始编号（不改写）。"""
        gw, pushed = self._new_gw()
        today = datetime.date.today().strftime("%Y-%m-%d")
        d0 = "buy:603468:fac_1:" + today.replace("-", "")
        d1 = "buy:603468:fac_1:" + today.replace("-", "") + "x"  # 同前 24 位，尾字符不同
        self.assertEqual(d0[:24], d1[:24])
        for s in (d0, d1):
            seq = gw.store.dispatch_enqueue_order(
                {"signal_id": s, "code": "603468.SH", "side": "卖出",
                 "price_type": "limit", "price": 22.55, "qty": 900, "strategy": "t"})
            gw.store.dispatch_set_result(seq, {"ok": True, "order_id": "EXC-" + s[:6], "err": ""})
        gw._do_dispatch_result({
            "type": "trade", "order_id": "EXC-FOREIGN", "trade_id": "T-AMB",
            "code": "603468.SH", "side": "买入", "price": 22.55, "qty": 900, "amount": 20295.0,
            "traded_at": today + "T10:08:32+08:00", "signal_id": d0[:24]})
        ev = pushed[-1]
        self.assertIs(ev.get("side_unverified"), True, "歧义不许被当成已证方向入账")
        self.assertEqual(ev["signal_id"], d0[:24], "还原不了就保留柜台原始值，不编一个编号出来")
        self.assertEqual(gw.store.list_positions(), [], "未证成交绝不许改动持仓账")


if __name__ == "__main__":
    unittest.main()
