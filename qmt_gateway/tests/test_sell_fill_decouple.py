# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_sell_fill_decouple.py — §SELLFILL-DECOUPLE（2026-10-06 修复批 波 2 / P1-B）。

缺陷本体（docs/AUDIT_20261005_全量评价报告.md P1-B，实测锤实）：
``store.apply_fill`` 的卖出分支在**查不到持仓行**时执行 ``return None, False``，而这个 return
位于本函数下方 ``INSERT INTO fills`` **之前** ⇒ 该笔卖出成交在网关账本上根本不存在。
注意后果不是"成本算不出"而是"流水没落"，三条下游腿全指望这一行：
  · Go /settlement 三方对账（券商有成交、本地 fills 缺行 ⇒ 资金事实悬空，且这条**系统性**缺行
    会把真差异淹成"每次都有一堆差"的对账噪声）；
  · SumSellFilledAmountByDay（少算当日回款 ⇒ 预算占满后无法释放，与 09-22 "卖出记成买入 →
    回款 0 → 预算占满"错账同族后果）；
  · TodayRealizedPnl / 胜率（行都不在，连"该不该计"都问不到）。
触发条件不需要任何异常：网关重启后持仓空窗、柜台侧手工卖出而本地无行、交割单补记路径。

修法＝**记账与成本核算解耦**：流水无条件落，持仓缺行只影响"本笔不动持仓账"这一件事。
成本不可知态**不写 0、也不加第二本成本账**——fills 表本来就没有成本列（见本文件 E5），
成本可知性由持仓行决定而非由流水决定；Go 侧 costBasisFor 无持仓且无当日买入 ⇒ 该笔
fail-open 不计入 TodayRealizedPnl，这条口径本批一字未动。

本文件覆盖波 2 计划里的断言编号（E1/E3/E4/E5 + E6 的行为腿；E2 是镜像反证、
E6 的静态口径对齐锁在门禁 §109）：
  E1 无底仓卖出 ⇒ fills **恰好新增 1 行**、side='卖出'、身份与可复核字段全保留、持仓账不动；
  E3 幂等锚不被解耦破坏：同一 trade_id 重放仍 1 行；无 trade_id 时复合键 + 时间窗仍 1 行；
  E4 持仓腿照常：有底仓减仓改 qty、清仓删行（旧语义不得被"顺手简化"掉）；
  E5 无幽灵持仓行 + 无 0 值假成本：fills 表**不存在**任何成本列（列不存在＝不可知的正确表达），
     同时对照买入腿（买入无底仓**必须**建行）以证明"不建行"只发生在无底仓卖出分支；
  E5b 「待核对」通道与"无底仓卖出"互不干扰：side_unverified 仍落第三态字面量、仍不动持仓账，
     两个独立机制不叠加成第三种形态；
  E6 行为腿（HTTP 桥上报补偿，owner 裁决 3「两桥补偿口径对齐」）：POST 未送达 ⇒ tid **不记**、
     下轮重推；送达 ⇒ 只记一次、下轮不重复推。策略桥的补偿形态是每轮重推全量 DEAL，
     本批把 HTTP 桥拉到同一语义（静态口径对齐锁见门禁 §109 锁面）。
（English: §SELLFILL-DECOUPLE regression — the fills journal is unconditional, a missing
position row only suppresses the position mutation; cost remains unknowable-by-absence (there
is no cost column to fake with 0), the replay anchors still hold, and the HTTP bridge now marks a
trade as reported only after the gateway actually accepted it, matching the strategy bridge's
"re-report the full DEAL snapshot every round" compensation semantics.）
"""
import os
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from handler import ReportHandler  # noqa: E402
from qmt_bridge import Bridge  # noqa: E402
from store import UNRESOLVED_STATUS, Store  # noqa: E402

# CN_TZ 与 store 内部判重窗下界用的是同一个 UTC+8 固定偏移（不是机器时区）：
# 断言 traded_at 落在 120s 判重窗内时必须按这把尺子造时间，否则跨时区机器上 E3 会假红。
CN_TZ = timezone(timedelta(hours=8))


def _new_store():
    """建一个干净的临时账本（Store 首建走 _init_schema 全量表结构）。"""
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)  # 让 Store 以"文件不存在"形态首次建库
    return Store(path)


def _fills(store):
    """读全部 fills 行（按自增 id 升序），便于断言"恰好新增 1 行"与字段值。"""
    return [dict(r) for r in store._conn.execute(
        "SELECT * FROM fills ORDER BY id").fetchall()]


def _fills_count(store):
    return len(_fills(store))


def _now_in_window():
    """当前北京时秒串：保证落在 FILL_DEDUP_WINDOW_SEC(120s) 判重窗**内**。

    E3 的复合键腿（无 trade_id）必须用"现在"的时间，用固定历史日期会让判重恒判非重放，
    那条用例就变成空跑（判重窗口是按 traded_at 字符串与 cutoff 比较的）。
    """
    return datetime.now(CN_TZ).strftime("%Y-%m-%dT%H:%M:%S")


def _sell_fill(**over):
    """一笔"柜台说卖了、本地查无底仓"的卖出回报（P1-B 事故形态的最小输入）。"""
    ev = {"code": "600580.SH", "side": "卖出", "price": 20.0, "qty": 900,
          "amount": 18000.0, "order_id": "OID-ORPHAN", "trade_id": "TID-ORPHAN",
          "signal_id": "sell:600580:stop_loss:20261005",
          "traded_at": "2026-10-05T14:30:00", "user_id": "uA",
          "fee": 5.0, "stamp_tax": 9.0}
    ev.update(over)
    return ev


class TestE1SellFillJournalIsUnconditional(unittest.TestCase):
    """E1：无底仓卖出流水照落——这是本波的第一职责（记账与成本核算解耦）。"""

    def setUp(self):
        self.s = _new_store()

    def test_orphan_sell_journals_exactly_one_row(self):
        # 前置：持仓账里确实没有这只票（重启后空窗 / 柜台手工卖过的形态）
        self.assertEqual(self.s.list_positions(), [], "夹具必须是无底仓态")
        before = _fills_count(self.s)

        pos, is_dup = self.s.apply_fill(_sell_fill())

        # 旧缺陷复现点：这条断言在修前必红（整笔卖出在账本上不存在）
        self.assertEqual(_fills_count(self.s) - before, 1,
                         "§SELLFILL-DECOUPLE 无底仓卖出必须落 1 行流水，不是 0 行")
        row = _fills(self.s)[-1]
        self.assertEqual(row["side"], "卖出", "流水方向按回报原值落，不因缺持仓而降级")
        self.assertEqual(row["trade_id"], "TID-ORPHAN", "身份锚不得丢（重放判重靠它）")
        self.assertEqual(row["order_id"], "OID-ORPHAN")
        self.assertEqual(row["code"], "600580.SH")
        self.assertEqual(row["qty"], 900)
        self.assertEqual(row["price"], 20.0)
        self.assertEqual(row["amount"], 18000.0, "成交额照回报落，不得因无持仓写 0")
        self.assertEqual(row["signal_id"], "sell:600580:stop_loss:20261005")
        self.assertEqual(row["user_id"], "uA", "多账号归属腿不得丢")
        # §P2-FEE 费用腿同样保留：/settlement 的费用差对账要看得到这两列
        self.assertEqual(row["fee"], 5.0)
        self.assertEqual(row["stamp_tax"], 9.0)
        # 返回值语义：None＝"这个 code 当前无持仓"，不是"这笔没发生"；is_dup 仍为 False
        self.assertIsNone(pos)
        self.assertFalse(is_dup)


class TestE3ReplayAnchorsSurviveDecoupling(unittest.TestCase):
    """E3：解耦不得破坏幂等锚（重放若变成两行，就是把"漏记"修成了"重复记"）。"""

    def setUp(self):
        self.s = _new_store()

    def test_same_trade_id_replay_stays_one_row(self):
        # 第一腿 trade_id 判重锚：同一成交编号重放只记一次
        self.s.apply_fill(_sell_fill())
        pos, is_dup = self.s.apply_fill(_sell_fill())
        self.assertTrue(is_dup, "同 trade_id 重放必须判成重放")
        self.assertEqual(_fills_count(self.s), 1, "重放不得追加第二行流水")
        # 无底仓卖出重放返回 (None, True)：持仓本来就该是 None，判重位仍要是 True
        self.assertIsNone(pos)
        # 第三条同样：三次喂入仍然 1 行
        self.s.apply_fill(_sell_fill())
        self.assertEqual(_fills_count(self.s), 1)

    def test_composite_key_replay_without_trade_id(self):
        # 旧通道无 trade_id ⇒ 退回 (order_id,side,price,qty)+时间窗判重；
        # traded_at 必须取"现在"才落在 120s 窗内（用固定历史日期这条腿就白跑了）
        ts = _now_in_window()
        self.s.apply_fill(_sell_fill(trade_id="", traded_at=ts))
        pos, is_dup = self.s.apply_fill(_sell_fill(trade_id="", traded_at=ts))
        self.assertTrue(is_dup, "无 trade_id 时复合键判重必须在无底仓卖出路径上照常生效")
        self.assertEqual(_fills_count(self.s), 1)
        self.assertIsNone(pos)

    def test_distinct_partial_fills_are_two_rows(self):
        # 反向对照：同一委托的两笔真实部成（trade_id 不同）不得被并成一行——
        # §M4 的既有语义在无底仓分支同样成立（少记成交＝持仓虚高、后续卖出拿不存在的量）
        self.s.apply_fill(_sell_fill(trade_id="TID-P1"))
        self.s.apply_fill(_sell_fill(trade_id="TID-P2"))
        self.assertEqual(_fills_count(self.s), 2,
                         "两笔不同成交编号必须各落一行（判重锚只能收紧不能误伤）")


class TestE4PositionLegStillWorks(unittest.TestCase):
    """E4：有底仓时减仓/清仓语义照常（解耦只改"缺行"这一支，不许顺手简化持仓账）。"""

    def setUp(self):
        self.s = _new_store()

    def _open_position(self, code="600580.SH", qty=900, price=20.0):
        # 建仓走买入腿（apply_fill 自己就是唯一的持仓写入口，不手工插行）
        self.s.apply_fill({"code": code, "side": "买入", "price": price, "qty": qty,
                           "amount": price * qty, "order_id": "OID-BUY",
                           "trade_id": "TID-BUY-" + code, "signal_id": "buy:%s:dragon:20261005" % code,
                           "traded_at": "2026-10-05T09:30:00", "user_id": "uA"})

    def test_partial_sell_reduces_qty(self):
        self._open_position(qty=900, price=20.0)
        pos, is_dup = self.s.apply_fill(_sell_fill(qty=300, trade_id="TID-S1", amount=6000.0))
        self.assertFalse(is_dup)
        self.assertIsNotNone(pos, "有底仓时卖出必须返回减仓后的持仓")
        self.assertEqual(pos["qty"], 600)
        self.assertEqual(_fills_count(self.s), 2, "买入腿 + 卖出腿各 1 行")

    def test_full_sell_deletes_position_row(self):
        self._open_position(qty=900, price=20.0)
        pos, _ = self.s.apply_fill(_sell_fill(qty=900, trade_id="TID-S2"))
        self.assertIsNone(pos, "清仓后该 code 无持仓行")
        self.assertEqual(self.s.list_positions(), [], "清仓必须删行，不能留 qty=0 的幽灵仓")
        self.assertEqual(_fills_count(self.s), 2)


class TestE5NoGhostRowAndNoFakeCost(unittest.TestCase):
    """E5：不可知成本的正确表达＝列不存在 + 不建幽灵持仓行（0 会被下游当真实成本，比缺失更坏）。

    计划原文写的是"断言 fills 成本字段 IS NULL"，实测锤实后发现 **fills 表根本没有成本列**
    （schema：order_id/code/side/price/qty/amount/traded_at/signal_id/user_id/trade_id/fee/stamp_tax）。
    于是本腿按真形态改写为两条更硬的断言：① 成本列不存在 ⇒ 任何实现都不可能在缺持仓时
    写出 0 假成本；② 无底仓卖出不得建持仓行。若将来真有人加成本列，①会立刻红并要求重新表态。
    English: fills has no cost column at all, so the leg asserts (a) the absence stays true and
    (b) no ghost position row is created — the correct encoding of "cost is unknowable".
    """

    def setUp(self):
        self.s = _new_store()

    def _columns(self):
        return [r["name"] for r in self.s._conn.execute("PRAGMA table_info(fills)").fetchall()]

    def test_fills_has_no_cost_column_to_fake(self):
        cols = self._columns()
        # 成本语义的列名一律不许出现在流水表上（cost/cost_price/成本）
        offenders = [c for c in cols if "cost" in c.lower()]
        self.assertEqual(offenders, [],
                         "fills 出现成本列 %s ⇒ 本波 E5 的前提变了：必须回来重新表达"
                         "「成本不可知态不落 0」这条口径（0 会被下游当真实成本）" % offenders)
        # 现有可复核字段仍在（本波没有把流水瘦身为"只有方向"的残缺行）
        for required in ("amount", "fee", "stamp_tax", "trade_id", "user_id"):
            self.assertIn(required, cols)

    def test_orphan_sell_creates_no_position_row(self):
        self.s.apply_fill(_sell_fill())
        self.assertEqual(self.s.list_positions(), [],
                         "无底仓卖出绝不许建 qty<=0 的幽灵持仓行（会被持仓页/止盈止损逻辑当真仓读走）")

    def test_buy_leg_still_creates_the_row(self):
        # 对照腿：买入无底仓**必须**建仓 ⇒ 证明"不建行"只发生在无底仓卖出分支，
        # 而不是整段被写成了"缺行就什么都不做"
        self.s.apply_fill(_sell_fill())  # 卖出腿：不建行
        self.assertEqual(self.s.list_positions(), [])
        self.s.apply_fill({"code": "600581.SH", "side": "买入", "price": 10.0, "qty": 100,
                           "amount": 1000.0, "order_id": "OID-B2", "trade_id": "TID-B2",
                           "signal_id": "buy:600581:momentum:20261005",
                           "traded_at": "2026-10-05T09:31:00", "user_id": "uA"})
        held = self.s.list_positions()
        self.assertEqual([p["ts_code"] for p in held], ["600581.SH"], "买入建仓腿不得被本波改动")
        self.assertEqual(held[0]["cost_price"], 10.0)

    def test_orphan_sell_with_unverified_side_stays_two_independent_mechs(self):
        # E5b：§SIDE-AUTH-2（待核对）与 §SELLFILL-DECOUPLE（无底仓）互不干扰——
        # 方向未证的卖出仍走第三态字面量、仍不动持仓账，两个机制不叠加成第三种形态
        pos, is_dup = self.s.apply_fill(_sell_fill(side_unverified=True, trade_id="TID-U1"))
        self.assertEqual(_fills_count(self.s), 1, "待核对证据行照落（与无底仓卖出同姿势）")
        self.assertEqual(_fills(self.s)[0]["side"], UNRESOLVED_STATUS,
                         "方向未证不得落「卖出」当真账")
        self.assertEqual(self.s.list_positions(), [], "两条腿都不许动持仓账")
        self.assertFalse(is_dup)


class TestE1HandlerIngestPath(unittest.TestCase):
    """E1 端到端腿：入库入口 handler.on_trade 在无底仓卖出上照常落库 + 照常上报。

    为什么单独测 handler 而不只测 store：缺陷的**可见后果**发生在入口——旧实现里
    store.apply_fill 提前 return，handler 仍会 `_push` 上报首尔，于是"本地无账、远端有账"
    变成双向对不上。解耦后两本账必须都有这行。
    English: the entry point must journal and report the orphan sell — the old early return made
    the local ledger and the remote ledger disagree in both directions.
    """

    def setUp(self):
        self.s = _new_store()
        self.h = ReportHandler(self.s, "http://seoul.invalid/api/qmt/report", "tok", user_id="uA")
        self.pushed = []
        # 拦掉真实网络入队，只断言"这笔进了上报通道"（outbox 本身有 §W2C/§W2D 专测）
        self.h._push = self.pushed.append

    def test_orphan_sell_is_journaled_and_reported(self):
        accepted = self.h.on_trade(_sell_fill())
        self.assertTrue(accepted, "无底仓卖出必须受理（旧实现虽然也受理，但本地不记账）")
        self.assertEqual(_fills_count(self.s), 1, "入口腿：本地账要有这行")
        self.assertEqual(len(self.pushed), 1, "上报腿：远端账也要有这行")
        self.assertEqual(self.pushed[0]["type"], "trade")
        self.assertEqual(self.pushed[0]["trade_id"], "TID-ORPHAN")
        # 重放：判重命中 ⇒ 不再上报（避免首尔侧二次累加回款）
        again = self.h.on_trade(_sell_fill())
        self.assertFalse(again, "同 trade_id 重放在入口就判重，不得再推一次")
        self.assertEqual(len(self.pushed), 1)
        self.assertEqual(_fills_count(self.s), 1)


class _StubAdapter:
    """桥成交查询替身：固定返回一批成交，令 _report_new_trades 可单测。"""

    def __init__(self, trades):
        self.trades = trades

    def query_trades(self):
        return list(self.trades)


class TestE6HttpBridgeReportsOnlyWhenDelivered(unittest.TestCase):
    """E6 行为腿：HTTP 桥"未送达不记 tid、下轮重推"（owner 裁决 3 两桥补偿口径对齐）。

    旧实现把 ``_seen_trades.add(tid)`` 放在 ``_post`` **之前** ⇒ 网关重启/网络抖动窗口里的
    成交在 HTTP 桥上永久消失，而策略桥每轮重推全量 DEAL 会自愈——同一条成交会不会丢
    取决于走哪条桥。本腿钉住"送达才记"的行为，静态口径对齐锁在门禁 §109。
    English: a POST that never reached the gateway must not mark the trade as reported; the next
    poll re-reports it, matching the strategy bridge's snapshot-based compensation.
    """

    def _bridge(self, trades, statuses):
        b = Bridge("http://gateway.invalid", token="t", account="M", dry_run=True)
        b.adapter = _StubAdapter(trades)
        calls = []
        seq = list(statuses)

        def fake_post(path, payload):
            # 弹出预置状态码：0＝未送达（urllib 异常分支），200＝网关已受理
            status = seq.pop(0) if seq else 200
            calls.append((path, payload, status))
            return status, None

        b._post = fake_post
        return b, calls

    def test_undelivered_post_is_retried_next_round(self):
        trade = {"trade_id": "T-RETRY", "code": "600580.SH", "side": "卖出",
                 "price": 20.0, "qty": 900, "order_id": "O-RETRY"}
        b, calls = self._bridge([trade], [0, 200])  # 第一轮不送达，第二轮成功

        b._report_new_trades()
        self.assertEqual(len(calls), 1, "第一轮确实尝试过一次")
        self.assertNotIn("T-RETRY", b._seen_trades,
                          "§SELLFILL-RETRY 未送达不得把 tid 记成已上报（永久消失的成因）")

        b._report_new_trades()
        self.assertEqual(len(calls), 2, "未送达的成交下一轮必须重推（与策略桥同语义）")
        self.assertIn("T-RETRY", b._seen_trades, "送达后必须记账，避免无限重推")

        b._report_new_trades()
        self.assertEqual(len(calls), 2, "已送达的成交第三轮不得再推（幂等腿）")

    def test_delivered_once_and_never_again(self):
        trade = {"trade_id": "T-OK", "code": "600580.SH", "side": "卖出",
                 "price": 20.0, "qty": 900, "order_id": "O-OK"}
        b, calls = self._bridge([trade], [200])
        b._report_new_trades()
        b._report_new_trades()
        self.assertEqual(len(calls), 1, "网关已受理的成交不得重复上报（否则靠网关侧判重兜底）")
        self.assertIn("T-OK", b._seen_trades)

    def test_http_4xx_counts_as_delivered(self):
        # 4xx 是网关**收到并拒了**（如 §REJECT 身份锚皆空），不是"没送达"：
        # 这类回报重推一百次结果也一样，必须记成已上报，否则每轮刷告警
        trade = {"trade_id": "T-400", "code": "600580.SH", "side": "卖出",
                 "price": 20.0, "qty": 900, "order_id": "O-400"}
        b, calls = self._bridge([trade], [400])
        b._report_new_trades()
        self.assertIn("T-400", b._seen_trades,
                      "只有未送达（status==0）才重推；被明确拒绝的回报不属漏记面")
        b._report_new_trades()
        self.assertEqual(len(calls), 1)

    def test_query_failure_leaves_seen_untouched(self):
        # 对照腿：查询成交失败连"要不要记"都问不到，_seen_trades 必须不动（既有语义）
        b, calls = self._bridge([], [200])

        class Boom:
            def query_trades(self):
                raise RuntimeError("counter session down")

        b.adapter = Boom()
        b._seen_trades.add("T-KEPT")
        b._report_new_trades()
        self.assertEqual(calls, [], "查询失败不得发出任何上报")
        self.assertIn("T-KEPT", b._seen_trades, "本轮失败不得清空或改写已上报集合")


if __name__ == "__main__":
    unittest.main()
