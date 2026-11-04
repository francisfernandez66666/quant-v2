# test_windows_four_source.py — §CANCEL-STATE 时段窗「四源同源」的等值锁（2026-11-04）。
#
# 背景：禁撤/禁挂窗口在本仓原本有三份逐字同步的副本（Go 的 rules.qmt.no_cancel_windows
# 出厂值、网关 broker.py 的 DEFAULT_NO_CANCEL_WINDOWS、桥 qmt_bridge_strategy.py 的同名常量），
# AGENTS 明写「三处副本必须同批同步」。11-05 之后 QMT 内置策略被券商禁掉文件读写与网络，
# 执行落点变成**物理机上的第四份**（本地执行器烘参 qmt_gateway/local_executor_params.json）。
# 方案 docs/IMPL_QMT_INTERNAL_LOOP_20261103.md §6 最后一条把这件事写成最容易漏的一处，
# 因为它一旦漂了不会报错——只会每天在某分钟上做出与其余三处相反的决定。
#
# 本锁判两件事（都是实测，不是文档）：
#   1) **字面等值**：四处窗口串逐字相同（渲染成同一个 "HH:MM-HH:MM,..." 形态比）。
#   2) **语义等值**：第四份的判定必须是**左闭右开** [起, 止)——右端点闭会让
#      「09:20-09:30」连 09:30 一起拦，而 09:30 正是连续竞价开始那一刻，
#      其余三处放行、第四处拒单，方向还是"少下单"这种不会响的错。
#
# 英文摘要：four-source equality lock for the cancel/new-order time windows,
# covering both the literal strings and the half-open comparison semantics.
import json
import os
import re
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

import local_executor as lex  # noqa: E402


def _read(rel):
    with open(os.path.join(REPO, rel), "r", encoding="utf-8") as handle:
        return handle.read()


def _go_const(name):
    """从 Go 源里取字符串常量值（不 import Go，只做字节级取证）。"""
    m = re.search(r'%s\s*=\s*"([^"]+)"' % re.escape(name), _read("internal/trading/session_guard.go"))
    if not m:
        raise AssertionError("Go 侧常量 %s 取不到（锚漂了＝这把锁是摆设）" % name)
    return m.group(1)


def _py_const(rel, name):
    """从 Python 源里取 DEFAULT_* 常量值。"""
    m = re.search(r'^%s\s*=\s*"([^"]+)"' % re.escape(name), _read(rel), re.M)
    if not m:
        raise AssertionError("%s 里的 %s 取不到（锚漂了）" % (rel, name))
    return m.group(1)


def _render_pairs(pairs):
    """把 [["09:20","09:30"],...] 渲染成 "09:20-09:30,..." 以对齐其余三处的书写形态。"""
    return ",".join("%s-%s" % (w[0], w[1]) for w in pairs)


class TestWindowsFourSource(unittest.TestCase):
    """四处窗口字面等值（AGENTS「三处副本必须同批同步」升到四处）。"""

    def setUp(self):
        with open(os.path.join(REPO, "qmt_gateway", "local_executor_params.json"),
                  encoding="utf-8") as handle:
            self.params = json.load(handle)["params"]

    def test_no_cancel_windows_equal_across_four_copies(self):
        exec_spec = _render_pairs(self.params["no_cancel_windows"])
        go_spec = _go_const("DefaultNoCancelWindows")
        gw_spec = _py_const("qmt_gateway/broker.py", "DEFAULT_NO_CANCEL_WINDOWS")
        bridge_spec = _py_const("qmt_gateway/qmt_bridge_strategy.py", "DEFAULT_NO_CANCEL_WINDOWS")
        self.assertEqual(exec_spec, go_spec,
                         "执行器禁撤窗 %r 与 Go %r 不同值——断链后第四份会做出相反决定"
                         % (exec_spec, go_spec))
        self.assertEqual(exec_spec, gw_spec)
        self.assertEqual(exec_spec, bridge_spec)

    def test_no_new_order_windows_equal_with_go(self):
        """禁挂窗只有 Go 与执行器两份（网关/桥没有这条常量），但这两份必须同值。

        为什么单独说：把"四处等值"当成一个笼统数字会掩盖"某处根本没有这条腿"，
        将来谁给网关加这条常量时必须同时进本锁，否则又是两套账。
        """
        exec_spec = _render_pairs(self.params["no_new_order_windows"])
        go_spec = _go_const("DefaultNoNewOrderWindows")
        self.assertEqual(exec_spec, go_spec)
        self.assertNotIn("DEFAULT_NO_NEW_ORDER_WINDOWS", _read("qmt_gateway/broker.py"),
                         "网关新加了禁挂窗常量却没进这把四源锁")

    def test_windows_are_beijing_minute_strings(self):
        """窗口一律 'HH:MM' 两位小时串——其余三处按分钟数比较，字典序只有在
        零填充时与时间序一致；写成 "9:20" 会让比较整体错位（Go 的 parseHHMM 会拒收）。"""
        pattern = re.compile(r"^\d{2}:\d{2}$")
        for key in ("no_cancel_windows", "no_new_order_windows"):
            for pair in self.params[key]:
                for value in pair:
                    self.assertRegex(value, pattern, "%s 里的 %r 不是 HH:MM" % (key, value))


class TestWindowComparisonIsHalfOpen(unittest.TestCase):
    """语义等值：执行器的窗口判定必须左闭右开，与其余三处一致。"""

    def params_with(self, windows):
        base = dict(self.canonical)
        base["no_cancel_windows"] = windows
        return base

    def setUp(self):
        with open(os.path.join(REPO, "qmt_gateway", "local_executor_params.json"),
                  encoding="utf-8") as handle:
            self.canonical = json.load(handle)["params"]

    def test_start_minute_is_blocked(self):
        p = self.params_with([["09:20", "09:30"]])
        ctx = {"trading_day": True, "hhmm": "09:20"}
        allowed, reason = lex.rule_no_cancel_window(p, ctx)
        self.assertFalse(allowed, "09:20 必须拦（左闭）")
        self.assertEqual(reason, "no_cancel_window")

    def test_end_minute_is_allowed(self):
        """反证靶：把 rule_window_hit 改回 `<= hhmm <=`（右闭）⇒ 这条必红。
        09:30 是连续竞价开始那一刻，其余三处放行，第四处也必须放行。"""
        p = self.params_with([["09:20", "09:30"]])
        ctx = {"trading_day": True, "hhmm": "09:30"}
        allowed, reason = lex.rule_no_cancel_window(p, ctx)
        self.assertTrue(allowed, "09:30 不得再拦（窗口是左闭右开），实得 %s" % reason)

    def test_last_minute_before_end_is_blocked(self):
        p = self.params_with([["14:57", "15:00"]])
        self.assertFalse(lex.rule_no_cancel_window(p, {"trading_day": True, "hhmm": "14:59"})[0])
        self.assertTrue(lex.rule_no_cancel_window(p, {"trading_day": True, "hhmm": "15:00"})[0])

    def test_wrapping_window_stays_half_open(self):
        """跨午夜窗口（起>止）同样右开：23:30-00:30 含 00:00、不含 00:30。"""
        p = self.params_with([["23:30", "00:30"]])
        self.assertFalse(lex.rule_no_cancel_window(p, {"trading_day": True, "hhmm": "00:00"})[0])
        self.assertTrue(lex.rule_no_cancel_window(p, {"trading_day": True, "hhmm": "00:30"})[0])
        self.assertFalse(lex.rule_no_cancel_window(p, {"trading_day": True, "hhmm": "23:45"})[0])


if __name__ == "__main__":
    unittest.main()
