# test_executor_cancel_leg.py — 本地执行器「超时撤单 + 收盘清场」那条腿的用例（2026-11-04）。
#
# 背景：桥活着的时候，撤单状态机（§CANCEL-STATE）与收盘清场（§CLOSE-RECON）都在量仔侧做。
# 2026-11-05 券商关掉 QMT 内置策略的文件读写/网络之后，那条通道整体消失，
# 物理机上的执行器就是唯一还能动的手——**外面没人再帮你撤单**。owner 11-04 令「P0 缺口本轮要补」。
#
# 本文件锁七件事（每条都是"撤错方向比不撤更贵"的形状）：
#   1. 只撤自己的单：委托备注里没有我们的短码 ⇒ 一律不动（别人在手机 App 手工挂的单不是我们的资产）；
#   2. 终态不动、**未知态也不动**（terminal 三态里 None＝词表取不到/码不认识 ⇒ 不动手）；
#   3. 超时判据要有时间基：拿不到 order_time 就**不猜**"老了"，只有收盘清场那条路能撤它；
#   4. 同一个委托号今天撤过一次就不再撤（重启也认，靠 cancelled_orders 留痕）——
#      反复对同一个号发撤单正是"频繁瞬时撤单"监测口径要抓的形态；
#   5. 交易日维度先判：休市日说"今天休市"，不许说"集合竞价不接受委托"那句假话（§138 同口径）；
#   6. 禁撤窗口左闭右开，且与 Go/网关/桥三处同源同值（另一份用例 test_windows_four_source.py 管等值）；
#   7. 申报+撤单合并计频：预算用尽本轮不动手，撤单失败**不记留痕**（下一轮才允许重试）。
#
# 另有一条词表单点锁：状态码表与终态集合必须 import 真源，不许在执行器里手抄子集。
#
# English: behaviour tests for the executor's cancel leg (stale + close sweep), including the
# three-valued terminal flag, the once-per-id rule, the trading-day axis and the shared
# per-minute operation budget.
import json
import os
import sys
import tempfile
import time
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

import local_executor as lex  # noqa: E402


def _canonical_params():
    with open(os.path.join(REPO, "qmt_gateway", "local_executor_params.json"), encoding="utf-8") as fh:
        return json.load(fh)["params"]


class FakeBroker(object):
    """只记账的柜台替身：orders_today 给出预置行，cancel_order 记录被撤的号。"""

    def __init__(self, rows, ok=True):
        self._rows = rows
        self.ok = ok
        self.cancelled = []
        self.placed = []

    def orders_today(self):
        return list(self._rows)

    def cancel_order(self, order_id):
        self.cancelled.append(str(order_id))
        return (True, "ok") if self.ok else (False, "counter refused")

    def place_order(self, signal, remark):
        self.placed.append((signal, remark))
        return True, "SEQ-1", "ok"

    def positions(self):
        return {}

    def available_cash(self):
        return 1000000.0


def row(order_id, short, terminal, age_sec=None, traded=0, symbol="600000.SH"):
    """造一行柜台委托：terminal=True 终态 / False 在途 / None 未知。"""
    remark = "%s-%s" % ("v01-20261103", short) if short else ""
    order_time = None if age_sec is None else time.time() - age_sec
    return {"order_id": order_id, "symbol": symbol, "remark": remark,
            "status": "已报" if terminal is False else "已成",
            "terminal": terminal, "qty": 1000, "price": 10.0,
            "traded": traded, "order_time": order_time}


class CancelLegCase(unittest.TestCase):
    def setUp(self):
        self.params = _canonical_params()
        self.dir = tempfile.mkdtemp()
        self.state = lex.LocalState(os.path.join(self.dir, "exec_state.db"))

    def executor_at(self, broker, hhmm, trading_day=True):
        """构造一个时钟与"当天分钟数"都被钉住的执行器（判据只吃 PARAMS 与注入时钟）。"""
        ex = lex.Executor(self.params, data=None, broker=broker, state=self.state)
        day = "2026-11-04" if trading_day else "2026-11-08"
        weekday = 2 if trading_day else 6  # 周三 / 周日
        closed = [] if trading_day else [day]

        def clock():
            import datetime
            base = datetime.date(2026, 11, 4) if trading_day else datetime.date(2026, 11, 8)
            hh, mm = hhmm.split(":")
            # 用带年份的构造函数避免裸数字进判据（这里只是夹具时钟，不是策略阈值）
            return datetime.datetime.combine(base, datetime.time(int(hh), int(mm)))

        ex.clock = clock
        ex._eb_day = day
        ex._eb_closed = closed
        ex._eb_trading_day = trading_day
        return ex

    def run_cancel(self, broker, hhmm="10:30", trading_day=True):
        ex = self.executor_at(broker, hhmm, trading_day)
        import datetime
        now = datetime.datetime.combine(
            datetime.date(2026, 11, 4) if trading_day else datetime.date(2026, 11, 8),
            datetime.time(*[int(x) for x in hhmm.split(":")]))
        day = now.strftime("%Y-%m-%d")
        closed_days = [] if trading_day else [day]
        return ex.cancel_leg(day, broker.orders_today(), now, closed_days)


class TestOnlyOurOwnOrders(CancelLegCase):
    def test_foreign_remark_is_never_touched(self):
        """备注里没有我们的短码 ⇒ 结构上碰不到它（别人手工挂的单不是我们的资产）。"""
        broker = FakeBroker([row("OID-9", "", terminal=False, age_sec=99999)])
        out = self.run_cancel(broker)
        self.assertEqual(broker.cancelled, [])
        self.assertEqual(out["attempted"], 0)

    def test_terminal_row_is_left_alone(self):
        broker = FakeBroker([row("OID-1", "PB600000", terminal=True, age_sec=99999)])
        self.run_cancel(broker)
        self.assertEqual(broker.cancelled, [])

    def test_unknown_terminal_is_left_alone(self):
        """terminal=None（词表取不到/码不认识）⇒ 既不当在途去撤，也不当已了结。"""
        broker = FakeBroker([row("OID-2", "PB600000", terminal=None, age_sec=99999)])
        out = self.run_cancel(broker)
        self.assertEqual(broker.cancelled, [], "未知状态被当成在途撤掉了")
        self.assertEqual(out["attempted"], 0)

    def test_no_order_time_is_not_treated_as_stale(self):
        """取不到下单时刻 ⇒ 不猜"已经很老"；未到清场时点就不撤。"""
        broker = FakeBroker([row("OID-3", "PB600000", terminal=False, age_sec=None)])
        self.run_cancel(broker, hhmm="10:30")
        self.assertEqual(broker.cancelled, [])


class TestStaleAndSweep(CancelLegCase):
    def test_stale_live_order_is_cancelled(self):
        stale_sec = int(self.params["stale_order_sec"])
        broker = FakeBroker([row("OID-4", "PB600000", terminal=False, age_sec=stale_sec + 60)])
        out = self.run_cancel(broker, hhmm="10:30")
        self.assertEqual(broker.cancelled, ["OID-4"])
        self.assertEqual(out["cancelled"][0]["why"], "stale")

    def test_not_yet_stale_is_left_running(self):
        stale_sec = int(self.params["stale_order_sec"])
        broker = FakeBroker([row("OID-5", "PB600000", terminal=False, age_sec=stale_sec - 60)])
        self.run_cancel(broker, hhmm="10:30")
        self.assertEqual(broker.cancelled, [])

    def test_close_sweep_cancels_every_live_one_of_ours(self):
        """过 close_sweep_from：自己的在途单不论年龄全撤（收盘前不留挂单）。"""
        sweep = self.params["close_sweep_from"]
        broker = FakeBroker([
            row("OID-6", "PB600000", terminal=False, age_sec=5),
            row("OID-7", "SL600000", terminal=False, age_sec=None),
            row("OID-8", "TP600000", terminal=True, age_sec=5),
            row("OID-9", "", terminal=False, age_sec=5),
        ])
        out = self.run_cancel(broker, hhmm=sweep)
        self.assertEqual(sorted(broker.cancelled), ["OID-6", "OID-7"])
        self.assertTrue(all(c["why"] == "close_sweep" for c in out["cancelled"]))

    def test_partial_fill_row_is_still_cancellable_once(self):
        """部成腿撤的是剩余部分，而"已成多少"来自柜台行本身（traded），不是内存计数。"""
        stale_sec = int(self.params["stale_order_sec"])
        r = row("OID-10", "PB600000", terminal=False, age_sec=stale_sec + 1, traded=300)
        broker = FakeBroker([r])
        self.run_cancel(broker)
        self.assertEqual(broker.cancelled, ["OID-10"])


class TestIdempotencyOfCancel(CancelLegCase):
    def test_same_id_is_never_cancelled_twice(self):
        stale_sec = int(self.params["stale_order_sec"])
        rows = [row("OID-11", "PB600000", terminal=False, age_sec=stale_sec + 1)]
        broker = FakeBroker(rows)
        self.run_cancel(broker)
        broker2 = FakeBroker(rows)
        out2 = self.run_cancel(broker2)
        self.assertEqual(broker2.cancelled, [], "同一委托号被撤了第二次")
        self.assertEqual(out2["attempted"], 0)

    def test_failed_cancel_is_not_marked(self):
        """撤单失败**不记留痕**：记了就等于"这单已经处理过"，下一轮永远不会再试。"""
        stale_sec = int(self.params["stale_order_sec"])
        broker = FakeBroker([row("OID-12", "PB600000", terminal=False, age_sec=stale_sec + 1)], ok=False)
        out = self.run_cancel(broker)
        self.assertEqual(out["attempted"], 1)
        self.assertEqual(out["skipped_reason"], "cancel_failed")
        self.assertNotIn("OID-12", self.state.cancelled_ids("2026-11-04"))
        # 下一轮（同样这行）仍然会试
        broker2 = FakeBroker(broker.orders_today(), ok=True)
        self.run_cancel(broker2)
        self.assertEqual(broker2.cancelled, ["OID-12"])


class TestWindowsAndFrequency(CancelLegCase):
    def test_closed_day_says_not_trading_day(self):
        """休市日：理由必须是"今天休市"，不许说"集合竞价不接受委托"那句假话。"""
        broker = FakeBroker([row("OID-13", "PB600000", terminal=False, age_sec=99999)])
        out = self.run_cancel(broker, hhmm="10:30", trading_day=False)
        self.assertEqual(out["skipped_reason"], "not_trading_day")
        self.assertEqual(broker.cancelled, [])

    def test_no_cancel_window_blocks(self):
        window = self.params["no_cancel_windows"][0]
        broker = FakeBroker([row("OID-14", "PB600000", terminal=False, age_sec=99999)])
        out = self.run_cancel(broker, hhmm=window[0])
        self.assertEqual(out["skipped_reason"], "no_cancel_window")
        self.assertEqual(broker.cancelled, [])

    def test_frequency_budget_stops_the_leg(self):
        """申报+撤单共用每分钟预算：预算用尽 ⇒ 本轮不动手（不是撤一半）。"""
        limit = int(self.params["max_ops_per_min"])
        stale_sec = int(self.params["stale_order_sec"])
        for _ in range(limit):
            self.state.record_op("place")
        broker = FakeBroker([row("OID-15", "PB600000", terminal=False, age_sec=stale_sec + 1),
                             row("OID-16", "SL600000", terminal=False, age_sec=stale_sec + 1)])
        out = self.run_cancel(broker)
        self.assertEqual(out["skipped_reason"], "ops_frequency_exceeded")
        self.assertEqual(broker.cancelled, [])


class TestStatusVocabularyIsSingleSource(CancelLegCase):
    """状态词表与终态集合必须 import 真源（handler.XT_STATUS_CODES / store.TERMINAL_STATUSES）。"""

    def test_vocab_comes_from_the_gateway_sources(self):
        codes, terminals = lex.counter_status_vocab()
        import handler
        import store
        self.assertEqual(codes, handler.XT_STATUS_CODES)
        self.assertEqual(terminals, store.TERMINAL_STATUSES)

    def test_every_live_code_maps_to_a_known_literal(self):
        codes, terminals = lex.counter_status_vocab()
        for code, literal in codes.items():
            if code == 255:
                continue
            text, terminal = lex.normalize_order_status(code, codes, terminals)
            self.assertEqual(text, literal)
            self.assertIn(terminal, (True, False), "码 %s 既不是终态也不是在途？" % code)

    def test_unavailable_vocab_is_unknown_not_a_guess(self):
        """词表取不到（返回空）⇒ 一律 unknown + None，绝不折成"非终态"。"""
        text, terminal = lex.normalize_order_status(50, {}, ())
        self.assertIsNone(terminal)
        self.assertTrue(text.startswith("unknown("))

    def test_unmapped_code_is_unknown(self):
        codes, terminals = lex.counter_status_vocab()
        text, terminal = lex.normalize_order_status(999, codes, terminals)
        self.assertIsNone(terminal, "认识不了的码被折成了终态/非终态")
        self.assertIn("999", text)


class TestOrdersTodayCarriesTheFlags(CancelLegCase):
    def test_broker_rows_surface_terminal_and_traded(self):
        """XtBroker 归一化出来的行必须带 terminal / traded / order_time——撤单腿只吃这三样。"""
        src = open(os.path.join(REPO, "qmt_gateway", "local_executor.py"), encoding="utf-8").read()
        for key in ('"terminal"', '"traded"', '"order_time"'):
            self.assertIn(key, src, "orders_today 归一化里丢了 %s" % key)


if __name__ == "__main__":
    unittest.main()
