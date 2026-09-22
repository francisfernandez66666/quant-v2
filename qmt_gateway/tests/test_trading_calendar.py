#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§A5（2026-09-22 PM 批清扫）交易日历回归：网关三处时段判定必须消费 Go 落盘的
trading_calendar.json（closed_days），日历不可得时降级回旧「周末口径」启发式。

English: §A5 regression — gateway session checks now consume the Go engine's on-disk
calendar; missing/unparseable calendar degrades to the legacy weekday-only heuristic.
"""
import json
import os
import sys
import tempfile
import time
import unittest
from datetime import datetime

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import handler as handler_mod  # noqa: E402
import qmt_bridge  # noqa: E402
import trading_calendar as cal  # noqa: E402


def _write_calendar(dirpath, closed, saved_at="20260922"):
    os.makedirs(dirpath, exist_ok=True)
    with open(os.path.join(dirpath, "trading_calendar.json"), "w", encoding="utf-8") as f:
        json.dump({"saved_at": saved_at, "closed_days": closed}, f)


def _reset_state():
    # 缓存是模块级全局——每个用例强制重读盘（时间戳回退 + 集合清空）。
    with cal._lock:
        cal._state.update(ts=0.0, closed=None, sig=None, warned=False)


class TradingCalendarTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self._env = os.environ.get("QUANT_DATA_DIR")
        os.environ["QUANT_DATA_DIR"] = self.tmp.name
        _reset_state()

    def tearDown(self):
        if self._env is None:
            os.environ.pop("QUANT_DATA_DIR", None)
        else:
            os.environ["QUANT_DATA_DIR"] = self._env
        _reset_state()
        self.tmp.cleanup()

    def test_holiday_closed_when_calendar_present(self):
        """日历命中节假日 → is_trading_day False（旧启发式恒 True 的缺陷点）。"""
        _write_calendar(self.tmp.name, ["20261001", "20261002"])
        self.assertFalse(cal.is_trading_day(datetime(2026, 10, 1, 10, 0)))
        self.assertFalse(cal.is_trading_day(datetime(2026, 10, 2, 10, 0)))
        self.assertTrue(cal.is_trading_day(datetime(2026, 10, 8, 10, 0)))  # 周四，未列休市

    def test_weekend_closed_regardless(self):
        """周末恒 False，不依赖日历。"""
        _write_calendar(self.tmp.name, [])
        self.assertFalse(cal.is_trading_day(datetime(2026, 10, 10, 10, 0)))  # 周六

    def test_missing_calendar_degrades_to_weekday_only(self):
        """文件不存在 → closed_days None，交易日判定退回周末口径（工作日 True）。"""
        self.assertIsNone(cal.closed_days())
        self.assertTrue(cal.is_trading_day(datetime(2026, 10, 1, 10, 0)))  # 无日历，周四=工作日

    def test_corrupt_calendar_degrades(self):
        """坏 JSON 不崩、返回上次口径（此处为 None=降级）。"""
        os.makedirs(self.tmp.name, exist_ok=True)
        with open(os.path.join(self.tmp.name, "trading_calendar.json"), "w", encoding="utf-8") as f:
            f.write("{not-json")
        self.assertIsNone(cal.closed_days())
        self.assertTrue(cal.is_trading_day(datetime(2026, 10, 1, 10, 0)))

    def test_calendar_file_refresh_picked_up(self):
        """Go 端 24h 刷新落盘新文件 → 超过缓存周期后 closed_days 更新。"""
        _write_calendar(self.tmp.name, ["20261001"])
        self.assertEqual(cal.closed_days(), {"20261001"})
        time.sleep(0.01)
        _write_calendar(self.tmp.name, ["20261001", "20261002"])
        _reset_state()  # 模拟 5 分钟缓存过期
        self.assertEqual(cal.closed_days(), {"20261001", "20261002"})

    def test_bridge_is_trading_window_respects_calendar(self):
        """qmt_bridge.is_trading_window：节假日窗口外判定与日历联动。"""
        _write_calendar(self.tmp.name, ["20261001"])
        self.assertFalse(qmt_bridge.is_trading_window(datetime(2026, 10, 1, 10, 30)))
        self.assertTrue(qmt_bridge.is_trading_window(datetime(2026, 10, 8, 10, 30)))
        self.assertFalse(qmt_bridge.is_trading_window(datetime(2026, 10, 8, 15, 30)))  # 收盘后

    def test_handler_session_respects_calendar(self):
        """handler.is_active_trading_session 经同一日历：休市日 False（时段内也拦）。

        时段用真实时钟不可控，直接验证日历注入点：_CAL_OK 为真时休市日必 False；
        再把 _CAL_OK 关掉验证退回周末口径。
        """
        hol = datetime(2026, 10, 1, 10, 30)
        _write_calendar(self.tmp.name, ["20261001"])
        self.assertTrue(cal.is_trading_day.__module__ == "trading_calendar")
        # 单元口径：日历层已锤实；这里锤 handler 的接线（休市→False、工作→按时段）
        saved_ok, saved_fn = handler_mod._CAL_OK, handler_mod._is_trading_day
        try:
            handler_mod._CAL_OK = True
            handler_mod._is_trading_day = lambda now: cal.is_trading_day(now)
            saved_now = handler_mod._now_beijing
            handler_mod._now_beijing = lambda: hol
            self.assertFalse(handler_mod.is_active_trading_session())
            handler_mod._now_beijing = lambda: datetime(2026, 10, 8, 10, 30)
            self.assertTrue(handler_mod.is_active_trading_session())
            # 日历不可得（模块缺失兜底路径）：仍按周末口径
            handler_mod._CAL_OK = False
            handler_mod._is_trading_day = None
            self.assertTrue(handler_mod.is_active_trading_session())
            handler_mod._now_beijing = lambda: datetime(2026, 10, 10, 10, 30)  # 周六
            self.assertFalse(handler_mod.is_active_trading_session())
        finally:
            handler_mod._CAL_OK, handler_mod._is_trading_day = saved_ok, saved_fn
            handler_mod._now_beijing = saved_now


if __name__ == "__main__":
    unittest.main()
