#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway 单测：xtquant 回调对象映射层（§P2-15，2026-09-15）。

handler._side_of / _signal_of / _status 是历史上三次生产事故的源头
（side 全判卖 / signal 恒空 / status 丢拒因），此前零覆盖。
用 SimpleNamespace 模拟 xtquant 回调对象，逐枚举空间断言，无需 Windows/xtquant。

§W7-C（2026-10-09 波 7）追加：状态映射由 fail-open 改 fail-closed，本文件同时钉两件事——
未登记/缺失/非数字码不得冒充任何已知态（TestStatus），以及映射失败在处理器上可计数
（TestUnknownStatusVisible，/admin/status 的 unknown_status 段读的就是它）。
"""
import logging
import os
import sys
import types
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
logging.disable(logging.CRITICAL)

from handler import ReportHandler  # noqa: E402
# §W7-C：映射表与前缀提到模块级当单一事实源，测试两处都从模块取——
# 「无法识别」那一侧必须按真实表求值（写死一份"已知态清单"的话，表加了码测试照样绿），
# 而「已登记」那一侧保留字面真值并与表做**双向等值**（详见 test_known_codes 的注释）。
import handler as handler_mod  # noqa: E402


def xt_obj(**kw):
    """构造模拟 xtquant 回调对象（缺失属性 getattr 默认值与真实对象一致）。"""
    return types.SimpleNamespace(**kw)


class TestSideOf(unittest.TestCase):
    """§FIX 2026-08-31 方向识别：多枚举空间探测（本机构建 23/24、主流 1101/1102、柜台 48/50）。"""

    def test_local_build_enum(self):
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=23)), "买入")
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=24)), "卖出")

    def test_legacy_doc_enum(self):
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=1101)), "买入")
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=1102)), "卖出")

    def test_counter_offset_enum(self):
        """柜台回推口径（实盘成交实证）：offset_type 48=买 / 50=卖。"""
        self.assertEqual(ReportHandler._side_of(xt_obj(offset_type=48)), "买入")
        self.assertEqual(ReportHandler._side_of(xt_obj(offset_type=50)), "卖出")

    def test_order_type_wins_over_offset(self):
        """order_type 优先于 offset_type。"""
        self.assertEqual(
            ReportHandler._side_of(xt_obj(order_type=23, offset_type=50)), "买入")

    def test_unrecognized_returns_empty(self):
        """无法识别返回空串（绝不默认走卖——旧事故形态）。"""
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type=999, offset_type=999)), "")
        self.assertEqual(ReportHandler._side_of(xt_obj()), "")
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type="abc")), "")

    def test_string_numeric_tolerated(self):
        """柜台偶发字符串数字（"48"）可容忍。"""
        self.assertEqual(ReportHandler._side_of(xt_obj(order_type="23")), "买入")


class TestSignalOf(unittest.TestCase):
    """§FIX 2026-08-31 signal_id 归因：strategy_name → order_remark → remark 首个非空。"""

    def test_prefers_strategy_name(self):
        self.assertEqual(
            ReportHandler._signal_of(xt_obj(strategy_name="S1", order_remark="S2")), "S1")

    def test_falls_back_to_remark(self):
        self.assertEqual(ReportHandler._signal_of(xt_obj(order_remark="buy:600000:dragon:d")), "buy:600000:dragon:d")
        self.assertEqual(ReportHandler._signal_of(xt_obj(remark="S3")), "S3")

    def test_empty_when_absent(self):
        self.assertEqual(ReportHandler._signal_of(xt_obj()), "")
        self.assertEqual(ReportHandler._signal_of(xt_obj(strategy_name="  ")), "")


class TestStatus(unittest.TestCase):
    """xtquant 委托状态码映射（handler._status，§W7-C fail-closed）。"""

    def test_known_codes(self):
        cases = {48: "未报", 49: "待报", 50: "已报", 51: "已报待撤", 52: "部成待撤",
                 53: "部撤", 54: "已撤", 55: "部成", 56: "已成", 57: "废单", 255: "未知"}
        # 等值锁（不是单向锁）：上面这份是「交付真值」，模块表是「运行时事实」，两者必须逐键相等。
        # 只按字面表逐个断言 ⇒ 表里**多**一个码没人看得见（多出来的码照样能映射、用例照样绿），
        # 而那份多出来的语义正是 §W7-C 要防的「有人悄悄往资金账入口谓词里加了一个新状态」。
        self.assertEqual(set(cases.keys()), set(handler_mod.XT_STATUS_CODES.keys()),
                         "状态码登记表与本用例点名的码集不一致（新增/删除码要同批改这里）")
        self.assertEqual(len(cases), len(handler_mod.XT_STATUS_CODES))
        for code, want in cases.items():
            self.assertEqual(ReportHandler._status(code), want, "code=%s" % code)

    def test_string_code(self):
        """柜台偶发字符串数字（"56"）按登记码走原映射。"""
        self.assertEqual(ReportHandler._status("56"), "已成")
        self.assertEqual(ReportHandler._status(" 50 "), "已报")

    def test_unknown_never_impersonates_known(self):
        """§W7-C 主断言：读不懂的码一律落「未知…」，绝不冒充任何已知态。

        已知态集合＝XT_STATUS_CODES 的值集。「已报」是引擎侧三本资金账（买入冻结 /
        跨日降废 / 在途卖量）的入口谓词，冒充它＝替柜台编造一条从未确认的委托进度。
        旧实现 `m.get(int(code or 0), "已报")` 在这一条上必红（P22 反证靶）。
        """
        known = set(handler_mod.XT_STATUS_CODES.values())
        for code in (0, None, "", "   ", 999, "abc", -1, "已报待撤", 3.7, [], {}):
            got = ReportHandler._status(code)
            self.assertTrue(got.startswith(handler_mod.UNKNOWN_STATUS_PREFIX),
                            "code=%r → %r 必须以未知前缀落点" % (code, got))
            self.assertNotIn(got, known,
                             "code=%r 被伪造成已知态 %r（旧 fail-open 形态）" % (code, got))

    def test_raw_value_survives_in_marker(self):
        """原始值必须留在标记串里：现网只看到「未知」不足以补表，要能看到是哪个码/哪种类型。"""
        self.assertEqual(ReportHandler._status(999), "未知(999)")
        self.assertEqual(ReportHandler._status("abc"), "未知(abc)")
        self.assertEqual(ReportHandler._status(None), "未知(缺失)")
        self.assertEqual(ReportHandler._status(""), "未知(缺失)")


class _RecordingStore(object):
    """最小账本替身：只记 upsert_order 的载荷（未配置 report_url 时 _push 直接返回，不碰 store）。"""

    def __init__(self):
        self.orders = []

    def upsert_order(self, ev):
        self.orders.append(dict(ev))


def _handler():
    """构造一个只用于映射可见化断言的处理器：无上报地址（不入 outbox 线程）。"""
    return ReportHandler(_RecordingStore(), "", "", user_id="u1")


class TestUnknownStatusVisible(unittest.TestCase):
    """§W7-C 告警计数：映射失败要在处理器上数得到（P21），而不只是躺在日志里等人翻。

    /admin/status 的 unknown_status 段读的就是这两个属性，日志本身在测试里被
    logging.disable(CRITICAL) 关掉，因此判据只能落在属性上。
    """

    def test_unregistered_code_counts_and_lands_unknown(self):
        h = _handler()
        h.on_stock_order(xt_obj(order_id="O1", stock_code="600000.SH", order_status=999,
                                order_type=23, price=10.0, order_volume=100,
                                strategy_name="sig-1"))
        self.assertEqual(h.unknown_status_total, 1)
        self.assertEqual(h.last_unknown_status, "999")
        self.assertEqual(len(h.store.orders), 1)
        self.assertEqual(h.store.orders[0]["status"], "未知(999)")

    def test_missing_attribute_counts(self):
        """回调对象根本没有 order_status 属性（跨构建字段名变化）——旧实现从这里滑进「已报」。"""
        h = _handler()
        h.on_stock_order(xt_obj(order_id="O2", stock_code="600000.SH", order_type=23,
                                price=10.0, order_volume=100, strategy_name="sig-2"))
        self.assertEqual(h.unknown_status_total, 1)
        self.assertEqual(h.store.orders[0]["status"], "未知(缺失)")

    def test_known_codes_do_not_count(self):
        """已登记码（含 50「已报」真值）不得被计数——否则现网正常单会把告警刷满。"""
        h = _handler()
        for code in (50, 55, 56):
            h.on_stock_order(xt_obj(order_id="O%d" % code, stock_code="600000.SH",
                                    order_status=code, order_type=23, price=10.0,
                                    order_volume=100, strategy_name="sig-%d" % code))
        self.assertEqual(h.unknown_status_total, 0)
        self.assertEqual([o["status"] for o in h.store.orders], ["已报", "部成", "已成"])

    def test_broker_unknown_255_counts(self):
        """柜台自带的 255「未知」同样要计数：它同样不进三本账，同样得有人看。"""
        h = _handler()
        h.on_stock_order(xt_obj(order_id="O3", stock_code="600000.SH", order_status=255,
                                order_type=23, price=10.0, order_volume=100,
                                strategy_name="sig-3"))
        self.assertEqual(h.unknown_status_total, 1)
        self.assertEqual(h.store.orders[0]["status"], "未知")

    def test_no_signal_id_still_counts(self):
        """无法归因的单在 on_order 里被丢弃，但映射失败这件事已经先记了数（否则恒不可见）。"""
        h = _handler()
        h.on_stock_order(xt_obj(order_id="O4", stock_code="600000.SH", order_status=999,
                                order_type=23, price=10.0, order_volume=100))
        self.assertEqual(h.unknown_status_total, 1)
        self.assertEqual(len(h.store.orders), 0)


if __name__ == "__main__":
    unittest.main()
