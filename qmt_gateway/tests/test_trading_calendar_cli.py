#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§FRIEND-PROFILE 批 2 回归锁：交易日历的只读判定入口（`python trading_calendar.py`）。

远端看门狗要知道"今天该不该起 QMT"。这条锁的意义不在日历本身（那里早有 Go 同口径的
closed_days 逻辑），而在**判据只许有一份**：
ps1 自己再判一次周末/节假日，就是第三份日历——漂开的那天没有任何一条读数会红，
现场形态是"朋友的机器交易日没起来"或"休市日白开一场柜台会话"。
"""
import os
import subprocess
import sys
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CLI = os.path.join(HERE, "trading_calendar.py")


class TestTradingCalendarCLI(unittest.TestCase):
    def test_anchor_is_ascii_and_code_matches(self):
        p = subprocess.run([sys.executable, CLI], stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE, universal_newlines=True)
        # 退出码语义固定：0＝交易日、3＝休市；别的码值说明有人改了判据却忘了同步看门狗
        self.assertIn(p.returncode, (0, 3))
        line = p.stdout.strip().splitlines()[-1] if p.stdout.strip() else ""
        self.assertTrue(line.startswith("TRADING_DAY="), line)
        self.assertTrue(all(ord(c) < 128 for c in line), "日历锚点含非 ASCII：PS 管道会换成 ? ⇒ 判读恒假")
        want = "TRADING_DAY=1" if p.returncode == 0 else "TRADING_DAY=0"
        self.assertTrue(line.startswith(want), "退出码与锚点行不一致（两份口径必有一份是错的）")

    def test_no_network_and_no_write(self):
        # 这个入口必须只读：看门狗每 5 分钟调一次，任何写盘/联网都会变成常态副作用
        src = open(CLI, encoding="utf-8").read()
        head = src[src.index('if __name__ == "__main__":'):]
        for tok in ("open(", "urllib", "requests", "socket"):
            self.assertNotIn(tok, head, "日历入口里出现 %s（只读判定不该有副作用）" % tok)


if __name__ == "__main__":
    unittest.main()
