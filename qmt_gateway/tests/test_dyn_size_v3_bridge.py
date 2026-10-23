# -*- coding: utf-8 -*-
"""§DYN-SIZE-v3 桥侧整手地板锁。

服务端预算已保证整手/额度；桥侧 `order_floor_mode=one_lot` 只是兜底：
把子 1 手的 qty 抬到 1 手，且一手成本不得超过 `lot_fallback_cap`。
legacy（默认）必须逐字不改 qty。

English: bridge-side one-lot floor fallback. The server owns sizing; this only raises a sub-lot
qty to one lot under `order_floor_mode=one_lot`, bounded by `lot_fallback_cap`. Legacy leaves qty
untouched.
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import qmt_bridge_strategy as bs  # noqa: E402


class TestOrderFloorFallback(unittest.TestCase):
    def test_legacy_leaves_qty_untouched(self):
        cmd = {"qty": 50, "price": 10, "signal_id": "S1"}
        bs._apply_order_floor(cmd, {"order_floor_mode": "legacy", "lot_fallback_cap": 5000})
        self.assertEqual(cmd["qty"], 50)

    def test_default_mode_is_legacy(self):
        cmd = {"qty": 50, "price": 10, "signal_id": "S1"}
        bs._apply_order_floor(cmd, {})
        self.assertEqual(cmd["qty"], 50)

    def test_one_lot_raises_sub_lot(self):
        cmd = {"qty": 50, "price": 10, "signal_id": "S1"}
        bs._apply_order_floor(cmd, {"order_floor_mode": "one_lot", "lot_fallback_cap": 5000})
        self.assertEqual(cmd["qty"], 100)

    def test_one_lot_refuses_when_cap_exceeded(self):
        cmd = {"qty": 50, "price": 60, "signal_id": "S1"}  # one lot = 6000 > cap 5000
        bs._apply_order_floor(cmd, {"order_floor_mode": "one_lot", "lot_fallback_cap": 5000})
        self.assertEqual(cmd["qty"], 50)

    def test_one_lot_never_rewrites_whole_lots(self):
        cmd = {"qty": 300, "price": 10, "signal_id": "S1"}
        bs._apply_order_floor(cmd, {"order_floor_mode": "one_lot", "lot_fallback_cap": 5000})
        self.assertEqual(cmd["qty"], 300)

    def test_zero_or_negative_is_not_promoted(self):
        for q in (0, -100):
            cmd = {"qty": q, "price": 10, "signal_id": "S1"}
            bs._apply_order_floor(cmd, {"order_floor_mode": "one_lot", "lot_fallback_cap": 5000})
            self.assertEqual(cmd["qty"], q)


if __name__ == "__main__":
    unittest.main()
