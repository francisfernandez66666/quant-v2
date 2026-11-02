#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§FRIEND-PROFILE 批 2 回归锁：远端"能不能关 QMT"的判据（close_gate.decide）。

钉住的三件事，全都朝"保守"一侧倒：
  ① 三条判据任一不成立就不关（收盘快照没发 / 有撤单中卡单 / 当日仍有非终态委托）；
  ② **读不到一律算不关**——"没数"和"没问题"必须是两种字（11-02 J0 那次就是把零回包
     编码成了健康，于是必推告警一整天没响）；
  ③ 超时不改变"不关"的判定，只是把处置转成人工 + 告警（over_limit），
     绝不允许出现"等太久就硬关"的第三种形态——那正是我们要防的带着未收敛账关门。
另外锁住 CLI 锚点是纯 ASCII：中文经 PowerShell 管道会被码页换成 `?`，
锚点判读会**永远不匹配**而看门狗照样往下走（11-02 现网两次假象同源）。
"""
import json
import os
import subprocess
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from close_gate import (  # noqa: E402
    EXIT_ALLOWED,
    EXIT_GIVE_UP,
    EXIT_HELD,
    EXIT_UNREADABLE,
    decide,
    exit_code_for,
)

DAY = "20261102"


def ok_status(day=DAY, at="15:00:03", stuck=0):
    return {
        "close_snapshot": {"day": day, "at": at, "orders_n": 12, "positions_n": 3, "trades_n": 9},
        "stuck_cancel_orders": {"n": stuck, "oldest_age_sec": 0},
    }


def terminal_rows():
    return [{"order_id": "1", "status": "已成"}, {"order_id": "2", "status": "已撤"},
            {"order_id": "3", "status": "废单"}]


class TestCloseGateAllows(unittest.TestCase):
    def test_all_clear_allows_and_exit_zero(self):
        d = decide(ok_status(), terminal_rows(), DAY)
        self.assertTrue(d["allowed"])
        self.assertEqual(d["reason"], "ok")
        self.assertEqual(exit_code_for(d), EXIT_ALLOWED)

    def test_day_format_normalized(self):
        # 两本账各写一种日期形态（YYYYMMDD / YYYY-MM-DD），比对必须先归一，
        # 否则门永远判"快照不是今天的" ⇒ 朋友的 QMT 整晚不关，是保守侧的误伤
        d = decide(ok_status(day="2026-11-02"), terminal_rows(), DAY)
        self.assertTrue(d["allowed"])


class TestCloseGateHolds(unittest.TestCase):
    def test_missing_close_snapshot_blocks(self):
        d = decide(ok_status(day="20261031"), terminal_rows(), DAY)
        self.assertFalse(d["allowed"])
        self.assertEqual(d["reason"], "no_close_snapshot")
        self.assertEqual(exit_code_for(d), EXIT_HELD)

    def test_snapshot_present_but_at_empty_blocks(self):
        # day 写了但 at 空＝桥从来没真发过那一帧（600362 的现网判据就是这个组合）
        d = decide(ok_status(at=""), terminal_rows(), DAY)
        self.assertEqual(d["reason"], "no_close_snapshot")

    def test_stuck_cancel_blocks(self):
        d = decide(ok_status(stuck=2), terminal_rows(), DAY)
        self.assertEqual(d["reason"], "stuck_cancel")
        self.assertEqual(d["stuck_n"], 2)

    def test_nonterminal_rows_block_and_count(self):
        rows = terminal_rows() + [{"order_id": "9", "status": "已报"}, {"order_id": "10", "status": "撤单中"}]
        d = decide(ok_status(), rows, DAY)
        self.assertEqual(d["reason"], "nonterminal")
        self.assertEqual(d["blocked_n"], 2)

    def test_unknown_status_counts_as_nonterminal(self):
        # §W7-C：「未知(原始值)」那类串不进三本资金账，绝不能被当成终态放行关门
        d = decide(ok_status(), [{"order_id": "7", "status": "未知(1049)"}], DAY)
        self.assertEqual(d["reason"], "nonterminal")

    def test_empty_status_counts_as_nonterminal(self):
        d = decide(ok_status(), [{"order_id": "7", "status": ""}], DAY)
        self.assertEqual(d["reason"], "nonterminal")


class TestUnreadableIsConservative(unittest.TestCase):
    """读不到 ≠ 没问题：这两种情况给 EXIT_UNREADABLE（30），比"还差一步"更该被看见。"""

    def test_admin_status_missing_blocks(self):
        d = decide(None, terminal_rows(), DAY)
        self.assertFalse(d["allowed"])
        self.assertEqual(d["reason"], "status_unreadable")
        self.assertEqual(exit_code_for(d), EXIT_UNREADABLE)

    def test_stuck_key_missing_blocks(self):
        # 快照在、卡单读数缺＝网关版本旧或该行没读到，都不许当成 0
        d = decide({"close_snapshot": {"day": DAY, "at": "15:00:03"}}, terminal_rows(), DAY)
        self.assertEqual(d["reason"], "status_unreadable")

    def test_orders_none_blocks(self):
        d = decide(ok_status(), None, DAY)
        self.assertEqual(d["reason"], "orders_unreadable")
        self.assertEqual(exit_code_for(d), EXIT_UNREADABLE)

    def test_orders_empty_list_is_terminal(self):
        # 整天零委托行是合法事实（当日没下单），与"读不到"必须分家——
        # 混同就会让门永远不开，朋友的 QMT 天天整晚开着
        d = decide(ok_status(), [], DAY)
        self.assertTrue(d["allowed"])


class TestOverLimitNeverHardCloses(unittest.TestCase):
    def test_over_limit_keeps_closed_false_and_give_up_code(self):
        d = decide(ok_status(stuck=1), terminal_rows(), DAY, elapsed_sec=3601, hold_limit_sec=3600)
        self.assertFalse(d["allowed"])
        self.assertEqual(d["reason"], "stuck_cancel")
        self.assertEqual(d["over_limit"], 1)
        self.assertEqual(exit_code_for(d), EXIT_GIVE_UP)

    def test_over_limit_with_all_clear_still_allows(self):
        d = decide(ok_status(), terminal_rows(), DAY, elapsed_sec=9999, hold_limit_sec=3600)
        self.assertTrue(d["allowed"])
        self.assertEqual(d["over_limit"], 0)


class TestCLIAnchor(unittest.TestCase):
    """CLI 锚点必须是纯 ASCII 且退出码可用——看门狗只认这一行与这个码。"""

    def _run(self, payload):
        path = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "close_gate.py")
        p = subprocess.run([sys.executable, path], input=json.dumps(payload),
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)
        return p.returncode, p.stdout.strip()

    def test_allowed_line_is_ascii_and_exit_zero(self):
        rc, out = self._run({"admin_status": ok_status(), "orders": terminal_rows(), "day": DAY})
        self.assertEqual(rc, EXIT_ALLOWED)
        self.assertTrue(out.startswith("CLOSE_GATE allowed=1 reason=ok"), out)
        self.assertTrue(all(ord(c) < 128 for c in out), "锚点行含非 ASCII 字符：PS 管道会把中文换成 ? ⇒ 判读恒假")

    def test_held_exit_code_and_reason(self):
        rc, out = self._run({"admin_status": ok_status(), "orders": [{"status": "已报"}], "day": DAY})
        self.assertEqual(rc, EXIT_HELD)
        self.assertIn("reason=nonterminal", out)

    def test_bad_payload_exit_unreadable(self):
        path = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "close_gate.py")
        p = subprocess.run([sys.executable, path], input="{not json",
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)
        self.assertEqual(p.returncode, EXIT_UNREADABLE)
        self.assertIn("payload_unparseable", p.stdout)


class TestTerminalSetIsSharedSource(unittest.TestCase):
    """终态集合不许在 close_gate 里另立一份（第一版就是手写三元组漏了 部撤/已废）。"""

    def test_close_gate_uses_store_terminal_set(self):
        from close_gate import TERMINAL_STATUS
        from store import TERMINAL_STATUSES
        self.assertEqual(tuple(TERMINAL_STATUS), tuple(TERMINAL_STATUSES))

    def test_bu_che_allows_closing(self):
        # 部撤/已废 都是终态：漏任何一个都会让门永远不开，
        # 现场读起来像"网络问题"，实际是我们自己的口径缺项
        for st in ("部撤", "已废"):
            d = decide(ok_status(), [{"order_id": "5", "status": st}], DAY)
            self.assertTrue(d["allowed"], "终态 %s 被判成在途" % st)


if __name__ == "__main__":
    unittest.main()
